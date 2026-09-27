#!/usr/bin/env python3
"""Phase 4: memory and start-time comparison, shared VM versus one VM per machine.

  setup   stage offline Go build inputs and create four per-VM nsl environments
  run     measure both topologies (never both running at once) and write evidence

The per-VM side is the real nsl CLI (build/nsl) with its own NSL_HOME, so the
user's environments are never touched. Both sides get an 8 GiB guest-memory
ceiling: four 2 GiB VMs, or one 8 GiB shared VM.
"""
import base64
import datetime
import json
import os
from pathlib import Path
import shutil
import statistics
import subprocess
import sys
import time

import driver
import machines
import workloads

NSL = driver.ROOT/'build/nsl'
PERVM_HOME = Path.home()/'.local/share/nsl-phase4'
WORK = Path.home()/'.cache/nsl-phase4/work'
NAMES = ['debian', 'fedora', 'arch', 'tumbleweed']
CATALOGUE = {'debian': 'debian:trixie', 'fedora': 'fedora:44', 'arch': 'arch:rolling', 'tumbleweed': 'opensuse:tumbleweed'}
SETTLE = 30
TRIALS, LATENCY_TRIALS = 20, 50


def nsl(*args, check=True, timeout=900):
    env = dict(os.environ, NSL_HOME=str(PERVM_HOME))
    r = subprocess.run([str(NSL), *args], env=env, capture_output=True, text=True, timeout=timeout)
    if check and r.returncode:
        raise RuntimeError(f'nsl {" ".join(args)}: {r.stderr.strip()[-400:]}')
    return r


def pervm(name):
    return 'p4-' + name


def stage():
    if (WORK/'.complete').exists():
        return
    WORK.mkdir(parents=True, exist_ok=True)
    goroot = subprocess.run(['go', 'env', 'GOROOT'], capture_output=True, text=True, check=True).stdout.strip()
    shutil.copytree(goroot, WORK/'go', symlinks=True)
    env = dict(os.environ, GOMODCACHE=str(WORK/'mod'), GOFLAGS='-mod=mod')
    subprocess.run(['go', 'mod', 'download'], cwd=driver.ROOT, env=env, check=True)
    (WORK/'src').mkdir()
    archive = subprocess.run(['git', 'archive', 'HEAD'], cwd=driver.ROOT, capture_output=True, check=True).stdout
    subprocess.run(['tar', '-x', '-C', str(WORK/'src')], input=archive, check=True)
    (WORK/'.complete').write_text(subprocess.run(['git', 'rev-parse', 'HEAD'], cwd=driver.ROOT, capture_output=True,
                                                 text=True, check=True).stdout)


def setup():
    stage()
    for name in NAMES:
        if not (PERVM_HOME/'environments'/pervm(name)).exists():
            began = time.monotonic()
            nsl('create', pervm(name), '--distro', CATALOGUE[name], '--project', str(WORK), timeout=3600)
            print(f'created {pervm(name)} in {time.monotonic() - began:.0f}s', flush=True)
        nsl('exec', pervm(name), '--', 'true', timeout=600)  # First boot: setup and root growth.
        nsl('stop', pervm(name))
    print('setup complete')


# Memory: proportional set size of every process in the measured units.

def unit_pss(unit):
    cgroup = subprocess.run(['systemctl', '--user', 'show', unit, '--property=ControlGroup', '--value'],
                            capture_output=True, text=True).stdout.strip()
    totals = {}
    procs = Path('/sys/fs/cgroup', cgroup.lstrip('/'), 'cgroup.procs')
    if not cgroup or not procs.exists():
        return totals
    for pid in procs.read_text().split():
        try:
            name = Path(f'/proc/{pid}/comm').read_text().strip()
            kib = next(int(l.split()[1]) for l in Path(f'/proc/{pid}/smaps_rollup').read_text().splitlines() if l.startswith('Pss:'))
        except (OSError, StopIteration):
            continue
        totals[name] = totals.get(name, 0) + kib
    return totals


def pervm_units():
    units, helpers = [], []
    for name in NAMES:
        meta = json.loads((PERVM_HOME/'environments'/pervm(name)/'environment.json').read_text())
        units.append(f'nsl-{meta["owner"]}-{meta["id"]}.service')
        helpers.append(f'nsl-{meta["owner"]}-{meta["id"]}-ports.service')
    return units, helpers


def sample(units, helpers=()):
    """Median of three samples, five seconds apart, in MiB."""
    totals, breakdown, helper_totals = [], {}, []
    for i in range(3):
        if i:
            time.sleep(5)
        merged = {}
        for unit in units:
            for name, kib in unit_pss(unit).items():
                merged[name] = merged.get(name, 0) + kib
        totals.append(sum(merged.values()))
        breakdown = merged
        helper_totals.append(sum(sum(unit_pss(u).values()) for u in helpers))
    mib = lambda kib: round(kib / 1024, 1)
    return {'pss_mib': mib(statistics.median(totals)), 'by_process_mib': {k: mib(v) for k, v in sorted(breakdown.items())},
            'forwarders_mib': mib(statistics.median(helper_totals))}


def stats(samples):
    ordered = sorted(samples)
    p95 = ordered[min(len(ordered) - 1, round(0.95 * (len(ordered) - 1)))]
    return {'median': round(statistics.median(ordered), 3), 'p95': round(p95, 3), 'min': round(ordered[0], 3),
            'max': round(ordered[-1], 3), 'trials': len(ordered)}


# Build workload: nsl itself, offline, from the same inputs in every guest.

def build_argv(work):
    script = ('export GOCACHE="$HOME/.cache/nsl-phase4-go"; rm -rf "$GOCACHE" "$HOME/nsl-phase4-bin"; cd "$0/src" && '
              'go build -p 2 -o "$HOME/nsl-phase4-bin" . && rm -f "$HOME/nsl-phase4-bin"')
    return ['env', f'GOROOT={work}/go', f'PATH={work}/go/bin:/usr/bin:/bin', f'GOMODCACHE={work}/mod', 'GOFLAGS=-mod=readonly',
            'GOPROXY=off', 'GOSUMDB=off', 'GOTOOLCHAIN=local', 'CGO_ENABLED=0', 'sh', '-c', script, work]


DROP_CACHES = ['sh', '-c', 'sync; echo 3 > /proc/sys/vm/drop_caches']


class Shared:
    label = 'shared-vm'

    def __init__(self):
        self.data = driver.load()

    def stop_all(self):
        driver.stop(self.data)

    def start(self, name):
        machines.start(self.data, name)

    def stop(self, name):
        machines.stop(self.data, name)

    def true(self, name):
        r, _ = machines.run(name, ['true'])
        if r.returncode:
            raise RuntimeError(r.stderr.decode()[-300:])

    def build(self, name):
        work = workloads.translate(WORK)
        r, seconds = machines.run(name, build_argv(work), timeout=1800)
        if r.returncode:
            raise RuntimeError(r.stderr.decode()[-400:])
        return round(seconds, 1)

    def drop_caches(self):
        driver.guest(DROP_CACHES, root=True)

    def memory(self):
        return sample([driver.UNIT])

    def config(self):
        return {'vms': 1, 'cpus': self.data['cpus'], 'memory_gib': self.data['memory_gib'], 'image': 'nsl-shared-vm-trixie-x86-64-v1',
                'machines': {n: machines.machine(n)['image']['reference'] for n in NAMES}}


class PerVM:
    label = 'vm-per-machine'

    def stop_all(self):
        for name in NAMES:
            nsl('stop', pervm(name))

    def start(self, name):
        nsl('start', pervm(name), timeout=300)

    def stop(self, name):
        nsl('stop', pervm(name))

    def true(self, name):
        nsl('exec', pervm(name), '--', 'true', timeout=300)

    def build(self, name):
        began = time.monotonic()
        nsl('exec', pervm(name), '--', *build_argv('/work'), timeout=1800)
        return round(time.monotonic() - began, 1)

    def drop_caches(self):
        for name in NAMES:
            nsl('exec', pervm(name), '--root', '--', *DROP_CACHES)

    def memory(self):
        return sample(*pervm_units())

    def config(self):
        metas = {n: json.loads((PERVM_HOME/'environments'/pervm(n)/'environment.json').read_text()) for n in NAMES}
        return {'vms': len(NAMES), 'cpus_each': metas['debian']['cpus'], 'memory_gib_each': metas['debian']['memory'],
                'images': {n: CATALOGUE[n] for n in NAMES}}


def raw_latency(topology):
    """No-op round trips over the existing SSH connection, without CLI overhead."""
    samples = []
    if isinstance(topology, Shared):
        for _ in range(LATENCY_TRIALS):
            r, seconds = machines.run('debian', ['true'])
            if r.returncode == 0:
                samples.append(seconds * 1000)
    else:
        config = PERVM_HOME/'environments'/pervm('debian')/'ssh.config'
        command = ['ssh', '-F', str(config), '-T', 'guest', driver.EXEC, driver.payload(['true'])]
        for _ in range(LATENCY_TRIALS):
            began = time.monotonic()
            if subprocess.run(command, capture_output=True).returncode == 0:
                samples.append((time.monotonic() - began) * 1000)
    return stats(samples)


def cli_latency(topology):
    samples = []
    for _ in range(LATENCY_TRIALS):
        began = time.monotonic()
        topology.true('debian')
        samples.append((time.monotonic() - began) * 1000)
    return stats(samples)


def measure(topology, log):
    result = {'config': topology.config()}
    log(f'{topology.label}: cold start of the first machine, {TRIALS} trials')
    cold = []
    for _ in range(TRIALS):
        topology.stop_all()
        began = time.monotonic()
        topology.start('debian')
        topology.true('debian')
        cold.append(time.monotonic() - began)
    result['cold_first_machine_s'] = stats(cold)
    log(f'{topology.label}: warm start of an additional machine, {TRIALS} trials')
    warm = []
    for _ in range(TRIALS):
        topology.stop('fedora')
        began = time.monotonic()
        topology.start('fedora')
        topology.true('fedora')
        warm.append(time.monotonic() - began)
    result['additional_machine_s'] = stats(warm)
    log(f'{topology.label}: no-op latency, {LATENCY_TRIALS} trials each')
    result['noop_transport_ms'] = raw_latency(topology)
    result['noop_command_ms'] = cli_latency(topology)
    log(f'{topology.label}: idle memory with 1, 2 and 4 machines')
    topology.stop_all()
    result['memory'] = {}
    for count, names in ((1, NAMES[:1]), (2, NAMES[1:2]), (4, NAMES[2:])):
        for name in names:
            topology.start(name)
            topology.true(name)
        time.sleep(SETTLE)
        result['memory'][f'idle_{count}'] = topology.memory()
        log(f'  idle {count}: {result["memory"][f"idle_{count}"]["pss_mib"]} MiB')
    log(f'{topology.label}: one build in each machine, sequentially')
    result['build_s'] = {name: topology.build(name) for name in NAMES}
    time.sleep(SETTLE)
    result['memory']['after_builds_4'] = topology.memory()
    log(f'  after builds: {result["memory"]["after_builds_4"]["pss_mib"]} MiB; {result["build_s"]}')
    topology.drop_caches()
    time.sleep(SETTLE)
    result['memory']['after_drop_caches_4'] = topology.memory()
    log(f'  after dropping caches: {result["memory"]["after_drop_caches_4"]["pss_mib"]} MiB')
    topology.stop_all()
    return result


DISK_BUILD = {'debian': 'nsl-debian-trixie-', 'fedora': 'nsl-fedora-44-', 'arch': 'nsl-arch-rolling-',
              'tumbleweed': 'nsl-opensuse-tumbleweed-'}


def artifacts():
    """Compressed download per distro: hub root filesystem layer against the nsl bootable disk."""
    descriptors = [json.loads(base64.b64decode(json.loads(r.read_text())['descriptor']))
                   for r in (PERVM_HOME/'delivery').glob('*/receipt.json')]
    sizes = {}
    for name in NAMES:
        layer = machines.machine(name)['image']
        disk = next((d for d in descriptors if d['image']['build_id'].startswith(DISK_BUILD[name])), None)
        sizes[name] = {'rootfs': layer['reference'], 'rootfs_compressed_mb': round(layer['layer_bytes'] / 1e6, 1),
                       'disk': disk and disk['image']['build_id'],
                       'disk_compressed_mb': disk and round(disk['compressed']['size'] / 1e6, 1),
                       'disk_raw_mb': disk and round(disk['raw']['size'] / 1e6, 1)}
    return sizes


def host():
    read = lambda p: Path(p).read_text().strip() if Path(p).exists() else None
    return {'kernel': os.uname().release, 'cpus': os.cpu_count(), 'ksm_run': read('/sys/kernel/mm/ksm/run'),
            'thp': read('/sys/kernel/mm/transparent_hugepage/enabled'),
            'mem_total_kib': next(int(l.split()[1]) for l in Path('/proc/meminfo').read_text().splitlines() if l.startswith('MemTotal:')),
            'vmspawn': driver.version('systemd-vmspawn', '--version'), 'qemu': driver.version('qemu-system-x86_64', '--version'),
            'virtiofsd': driver.version('/usr/libexec/virtiofsd', '--version'), 'nsl': nsl('version').stdout.strip()}


def run():
    began = datetime.datetime.now(datetime.timezone.utc)
    log = lambda message: print(f'[{time.strftime("%H:%M:%S")}] {message}', flush=True)
    evidence = {'schema': 1, 'phase': 4, 'date': began.isoformat(timespec='seconds'), 'host': host(),
                'settle_seconds': SETTLE, 'build': {'inputs': (WORK/'.complete').read_text().strip(), 'command': 'go build -p 2, offline'}}
    shared, per = Shared(), PerVM()
    per.stop_all()
    evidence['shared-vm'] = measure(shared, log)
    evidence['vm-per-machine'] = measure(per, log)
    evidence['artifacts'] = artifacts()
    out = driver.ROOT/'build/shared-vm/evidence'
    out.mkdir(parents=True, exist_ok=True)
    path = out/f'phase4-{evidence["date"].replace(":", "")}.json'
    with path.open('x') as f:
        json.dump(evidence, f, indent=2)
    log(f'Evidence: {path}')


def main():
    command = sys.argv[1:]
    if command == ['setup']:
        setup()
    elif command == ['run']:
        run()
    else:
        print(__doc__.strip())
        return 2
    return 0


if __name__ == '__main__':
    try:
        sys.exit(main())
    except (OSError, ValueError, RuntimeError, KeyError, subprocess.CalledProcessError, subprocess.TimeoutExpired) as error:
        print('shared-vm measure:', error, file=sys.stderr)
        sys.exit(1)
