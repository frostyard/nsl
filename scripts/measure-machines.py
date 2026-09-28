#!/usr/bin/env python3
"""Memory and start-time regression check for machines in the shared VM.

Creates a machine from each image in a disposable state directory, then
measures through the CLI, as the shared-VM experiment did:

  - cold start of the first machine, VM included, and start of an additional
    machine while the first runs (median and p95 of --trials);
  - no-op `nsl run` latency (--latency-trials);
  - host PSS of the VM unit's processes, idle with 1, 2 and all machines.

The VM gets 4 vCPUs and 8 GiB, autostart off and idle stop off. Criteria from
the implementation plan: four idle machines at or below 950 MiB, and an
additional machine at p95 <= 2 s. Like the experiment that set that budget,
the gated figures run without desktop sessions. When the host has a Wayland
session, the evidence also records four idle machines with their desktop
sessions, and the difference, ungated. Writes JSON evidence; exits nonzero
when a criterion fails.

  measure-machines.py --nsl build/nsl --vm-image VM.raw --machine-image M1.tar.zst ... \\
      --evidence build/image/evidence/measure.json
"""
import argparse
import datetime
import hashlib
import json
import os
from pathlib import Path
import shutil
import statistics
import subprocess
import sys
import tempfile
import time

MEMORY_LIMIT_MIB = 950
ADDITIONAL_P95_S = 2.0


def sha256(path):
    h = hashlib.sha256()
    with open(path, 'rb') as f:
        for chunk in iter(lambda: f.read(1 << 20), b''):
            h.update(chunk)
    return h.hexdigest()


def stats(samples):
    ordered = sorted(samples)
    p95 = ordered[min(len(ordered) - 1, round(0.95 * (len(ordered) - 1)))]
    return {'median': round(statistics.median(ordered), 3), 'p95': round(p95, 3), 'min': round(ordered[0], 3),
            'max': round(ordered[-1], 3), 'trials': len(ordered)}


class CLI:
    def __init__(self, nsl, home, config):
        self.nsl = nsl
        # Gated figures run without desktop sessions; desktop() measures them.
        self.display = os.environ.get('WAYLAND_DISPLAY', '')
        self.env = dict(os.environ, NSL_HOME=str(home), XDG_CONFIG_HOME=str(config))
        self.env.pop('WAYLAND_DISPLAY', None)
        self.home = home

    def __call__(self, *args, check=True, timeout=900):
        r = subprocess.run([str(self.nsl), *args], env=self.env, capture_output=True, text=True, timeout=timeout)
        if check and r.returncode:
            raise RuntimeError(f'nsl {" ".join(args)} failed: {r.stderr.strip()[-400:]}')
        return r

    def true(self, name):
        self('run', '-m', name, '--cd', '/', 'true')

    def unit(self):
        record = json.loads((self.home/'vm/vm.json').read_text())
        return f'nsl-{record["owner"]}-vm-{record["id"]}.service'


def unit_pss(unit):
    """PSS in KiB of every process in a user unit's cgroup tree, by command name."""
    cgroup = subprocess.run(['systemctl', '--user', 'show', unit, '--property=ControlGroup', '--value'],
                            capture_output=True, text=True).stdout.strip()
    totals = {}
    if not cgroup:
        return totals
    for procs in Path('/sys/fs/cgroup', cgroup.lstrip('/')).rglob('cgroup.procs'):
        for pid in procs.read_text().split():
            try:
                name = Path(f'/proc/{pid}/comm').read_text().strip()
                kib = next(int(l.split()[1]) for l in Path(f'/proc/{pid}/smaps_rollup').read_text().splitlines() if l.startswith('Pss:'))
            except (OSError, StopIteration):
                continue
            totals[name] = totals.get(name, 0) + kib
    return totals


def memory(unit):
    """Median of three samples, five seconds apart, in MiB."""
    totals, breakdown = [], {}
    for i in range(3):
        if i:
            time.sleep(5)
        breakdown = unit_pss(unit)
        totals.append(sum(breakdown.values()))
    mib = lambda kib: round(kib / 1024, 1)
    return {'pss_mib': mib(statistics.median(totals)), 'by_process_mib': {k: mib(v) for k, v in sorted(breakdown.items())}}


def version(*argv):
    try:
        return subprocess.run(argv, capture_output=True, text=True).stdout.splitlines()[0].strip()
    except (OSError, IndexError):
        return None


def host():
    read = lambda p: Path(p).read_text().strip() if Path(p).exists() else None
    return {'kernel': os.uname().release, 'cpus': os.cpu_count(), 'ksm_run': read('/sys/kernel/mm/ksm/run'),
            'thp': read('/sys/kernel/mm/transparent_hugepage/enabled'),
            'mem_total_kib': next(int(l.split()[1]) for l in Path('/proc/meminfo').read_text().splitlines() if l.startswith('MemTotal:')),
            'vmspawn': version('systemd-vmspawn', '--version'), 'qemu': version('qemu-system-x86_64', '--version'),
            'virtiofsd': version('/usr/libexec/virtiofsd', '--version')}


def main():
    parser = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    parser.add_argument('--nsl', type=Path, required=True)
    parser.add_argument('--vm-image', type=Path, required=True)
    parser.add_argument('--machine-image', type=Path, action='append', required=True)
    parser.add_argument('--evidence', type=Path, required=True)
    parser.add_argument('--trials', type=int, default=20)
    parser.add_argument('--latency-trials', type=int, default=50)
    parser.add_argument('--settle', type=int, default=30)
    o = parser.parse_args()
    if o.evidence.exists():
        parser.error('evidence file exists')
    if len(o.machine_image) < 2:
        parser.error('measuring an additional machine needs at least two images')
    log = lambda message: print(f'[{time.strftime("%H:%M:%S")}] {message}', flush=True)
    scratch = Path(tempfile.mkdtemp(prefix='nsl-measure-', dir=Path.home()/'.local/share'))
    home, config = scratch/'state', scratch/'config'
    (config/'nsl').mkdir(parents=True)
    (config/'nsl/nsl.conf').write_text('[vm]\nmemory = 8\ncpus = 4\n\n[machines]\nautostart = false\nidle_timeout = 0\n')
    nsl = CLI(o.nsl.resolve(), home, config)
    evidence = {'schema': 1, 'date': datetime.datetime.now(datetime.timezone.utc).isoformat(timespec='seconds'), 'host': host(),
                'nsl': nsl('version').stdout.strip(), 'vm': {'cpus': 4, 'memory_gib': 8}, 'settle_seconds': o.settle, 'machines': {}}
    try:
        vm_digest = sha256(o.vm_image)
        nsl('update', '--image', str(o.vm_image.resolve()), '--digest', 'sha256:' + vm_digest)
        evidence['vm']['image'] = {'file': o.vm_image.name, 'sha256': vm_digest}
        names = []
        for image in o.machine_image:
            descriptor = json.loads(subprocess.run(['tar', '--zstd', '-xOf', str(image), './usr/lib/nsl/machine.json'],
                                                   capture_output=True, check=True).stdout)
            name = descriptor['distribution']
            digest = sha256(image)
            began = time.monotonic()
            nsl('create', name, '--image', str(image.resolve()), '--digest', 'sha256:' + digest)
            evidence['machines'][name] = {'build_id': descriptor['build_id'], 'sha256': digest, 'create_seconds': round(time.monotonic() - began, 2)}
            names.append(name)
            log(f'created {name}')
        first, second = names[0], names[1]
        # First starts include each machine's first boot; keep them out of the trials.
        for name in names:
            nsl('start', name)
            nsl.true(name)
        nsl('shutdown')

        log(f'cold start of {first}, VM included, {o.trials} trials')
        cold = []
        for _ in range(o.trials):
            nsl('shutdown')
            began = time.monotonic()
            nsl('start', first)
            nsl.true(first)
            cold.append(time.monotonic() - began)
        evidence['cold_first_machine_s'] = stats(cold)

        log(f'start of {second} beside {first}, {o.trials} trials')
        additional = []
        for _ in range(o.trials):
            nsl('stop', second)
            began = time.monotonic()
            nsl('start', second)
            nsl.true(second)
            additional.append(time.monotonic() - began)
        evidence['additional_machine_s'] = stats(additional)

        log(f'no-op command latency, {o.latency_trials} trials')
        latency = []
        for _ in range(o.latency_trials):
            began = time.monotonic()
            nsl.true(first)
            latency.append((time.monotonic() - began) * 1000)
        evidence['noop_command_ms'] = stats(latency)

        log('idle memory')
        nsl('shutdown')
        evidence['memory'] = {}
        groups = [names[:1], names[1:2], names[2:]]
        for group in groups:
            for name in group:
                nsl('start', name)
                nsl.true(name)
            running = sum(len(g) for g in groups[:groups.index(group) + 1])
            time.sleep(o.settle)
            evidence['memory'][f'idle_{running}'] = memory(nsl.unit())
            log(f'  idle {running}: {evidence["memory"][f"idle_{running}"]["pss_mib"]} MiB')
        if nsl.display:
            # The same idle machines, each with its desktop session.
            nsl('shutdown')
            nsl.env['WAYLAND_DISPLAY'] = nsl.display
            for name in names:
                nsl('start', name)
                nsl.true(name)
            time.sleep(o.settle)
            desktop = memory(nsl.unit())
            evidence['memory'][f'idle_{len(names)}_desktop'] = desktop
            evidence['desktop_overhead_mib'] = round(desktop['pss_mib'] - evidence['memory'][f'idle_{len(names)}']['pss_mib'], 1)
            log(f'  idle {len(names)} with desktop sessions: {desktop["pss_mib"]} MiB')
    finally:
        nsl('shutdown', check=False)
        shutil.rmtree(scratch, ignore_errors=True)
    idle = evidence['memory'].get(f'idle_{len(names)}', {}).get('pss_mib')
    evidence['criteria'] = {
        'idle_machines': {'machines': len(names), 'pss_mib': idle, 'limit_mib': MEMORY_LIMIT_MIB,
                          'pass': len(names) >= 4 and idle is not None and idle <= MEMORY_LIMIT_MIB},
        'additional_machine_p95': {'seconds': evidence['additional_machine_s']['p95'], 'limit_s': ADDITIONAL_P95_S,
                                   'pass': evidence['additional_machine_s']['p95'] <= ADDITIONAL_P95_S},
    }
    o.evidence.parent.mkdir(parents=True, exist_ok=True)
    with o.evidence.open('x') as f:
        json.dump(evidence, f, indent=2)
    failed = [k for k, v in evidence['criteria'].items() if not v['pass']]
    log(f'Evidence: {o.evidence} | failed: {failed or "none"}')
    return 1 if failed else 0


if __name__ == '__main__':
    try:
        sys.exit(main())
    except (OSError, RuntimeError, subprocess.SubprocessError, KeyError, ValueError) as error:
        print('measure-machines:', error, file=sys.stderr)
        sys.exit(1)
