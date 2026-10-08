#!/usr/bin/env python3
"""Acceptance checks for a locally built nsl VM image (docs/specs/vm-image.md).

Drives the nsl CLI in disposable state directories and inspects the VM through
the agent's vm operation. Writes JSON evidence; exits nonzero if a check fails.

  probe-vm.py --nsl build/nsl --image build/image/share/NAME.raw --evidence build/image/evidence/NAME-probe.json
"""
import argparse
import base64
import datetime
import hashlib
import json
import os
from pathlib import Path
import shutil
import socket
import subprocess
import sys
import tempfile
import time
import uuid

GUEST_SOCKET = '''
import os, socket, stat, sys
s = socket.socket(socket.AF_UNIX)
s.settimeout(3)
try:
    s.connect(sys.argv[1])
    print("connected")
except OSError as e:
    print(type(e).__name__)
'''


class Probe:
    def __init__(self, nsl, image, root):
        self.nsl, self.image, self.root = nsl, image, root
        self.digest = 'sha256:' + hashlib.sha256(image.read_bytes()).hexdigest()
        self.results = {}

    def env(self, home):
        return dict(os.environ, NSL_HOME=str(home))

    def cli(self, home, *args, check=True, timeout=240):
        r = subprocess.run([str(self.nsl), *args], env=self.env(home), capture_output=True, text=True, timeout=timeout)
        if check and r.returncode:
            raise RuntimeError(f'nsl {" ".join(args)} failed: {r.stderr.strip()}')
        return r

    def vm(self, home, *argv, stdin=None, timeout=60):
        request = base64.b64encode(json.dumps({'protocol': 1, 'op': 'vm', 'argv': list(argv)}).encode()).decode()
        return subprocess.run(['ssh', '-F', str(home/'vm/ssh.config'), '-T', 'vm', request],
                              input=stdin, capture_output=True, text=True, timeout=timeout)

    def record(self, name, passed, **details):
        self.results[name] = {'pass': bool(passed), **details}
        print('PASS' if passed else 'FAIL', name, json.dumps(details)[:300], flush=True)

    def home(self, name):
        path = self.root/name
        path.mkdir(mode=0o700)
        return path

    def fresh(self, name):
        home = self.home(name)
        self.cli(home, 'update', '--image', str(self.image), '--digest', self.digest)
        return home

    def unit_journal(self, home):
        vm = json.loads((home/'vm/vm.json').read_text())
        unit = f'nsl-{os.getuid()}-vm-{vm["id"]}.service'
        return subprocess.run(['journalctl', '--user', '-u', unit, '--no-pager', '-n', '200'],
                              capture_output=True, text=True).stdout

    def readiness(self, home):
        began = time.monotonic()
        self.cli(home, 'recover')
        first = round(time.monotonic() - began, 2)
        listing = self.cli(home, 'list').stdout
        identity = json.loads(self.vm(home, 'cat', '/var/lib/nsl/identity.json').stdout)
        vm = json.loads((home/'vm/vm.json').read_text())
        self.cli(home, 'shutdown')
        began = time.monotonic()
        self.cli(home, 'recover')
        later = round(time.monotonic() - began, 2)
        self.record('readiness', 'running' in listing and identity['id'] == vm['id'] and identity['uid'] == os.getuid(),
                    first_boot_seconds=first, later_boot_seconds=later, image_build=vm.get('image_build'))

    def formatting(self, home):
        mounts = self.vm(home, 'findmnt', '--json', '--output', 'TARGET,SOURCE,FSTYPE,LABEL,OPTIONS')
        found = {m['target']: m for m in json.loads(mounts.stdout)['filesystems'][0].get('children', [])
                 if m['target'] in ('/var/lib/machines', '/var/lib/nsl')}
        subvolumes = self.vm(home, 'btrfs', 'subvolume', 'list', '/var/lib/nsl').stdout
        self.record('formatting', len(found) == 2 and all(m['fstype'] == 'btrfs' and m['label'] == 'nsl-data' for m in found.values())
                    and 'path machines' in subvolumes and 'path state' in subvolumes,
                    mounts=found, subvolumes=subvolumes.strip().splitlines())

    def root_replacement(self, home):
        marker = uuid.uuid4().hex
        known = (home/'vm/known_hosts').read_text()
        self.vm(home, 'mkdir', '/var/lib/machines/.probe-' + marker)
        self.vm(home, 'touch', '/root/probe-' + marker)
        self.cli(home, 'recover')
        kept = self.vm(home, 'test', '-d', '/var/lib/machines/.probe-' + marker).returncode == 0
        root_gone = self.vm(home, 'test', '-e', '/root/probe-' + marker).returncode != 0
        self.vm(home, 'rmdir', '/var/lib/machines/.probe-' + marker)
        self.record('root_replacement', kept and root_gone and known == (home/'vm/known_hosts').read_text(),
                    machines_marker_kept=kept, root_marker_discarded=root_gone, pinned_host_key_unchanged=known == (home/'vm/known_hosts').read_text())

    def growth(self, home):
        self.cli(home, 'shutdown')
        self.cli(home, 'resize', '--disk', '160')
        self.cli(home, 'recover')
        size = int(self.vm(home, 'findmnt', '--bytes', '--noheadings', '--output', 'SIZE', '/var/lib/nsl').stdout.strip())
        self.record('growth', size > 150 * (1 << 30), filesystem_bytes=size)

    def host_files(self, home):
        vm = json.loads((home/'vm/nsl.vm').read_text())
        mounts = json.loads(self.vm(home, 'findmnt', '--json', '--types', 'virtiofs', '--output', 'TARGET,OPTIONS').stdout)['filesystems']
        expected = sorted(['/mnt/host' + s['source'] for s in vm['shares']] + ['/var/cache/nsl/images'])
        cache = [m for m in mounts if m['target'] == '/var/cache/nsl/images']
        self.record('allowlist', sorted(m['target'] for m in mounts) == expected and cache and 'ro' in cache[0]['options'].split(','),
                    expected=expected, mounted=mounts, aliases=vm['aliases'])
        home_dir = str(Path.home().resolve())
        owner = self.vm(home, 'stat', '--format=%u:%g', '/mnt/host' + home_dir).stdout.strip()
        scratch = Path.home()/'.cache/nsl-probe-vm'/uuid.uuid4().hex
        scratch.mkdir(mode=0o700, parents=True)
        guest = '/mnt/host' + str(scratch.resolve())
        try:
            self.vm(home, 'touch', guest + '/from-root')
            st = (scratch/'from-root').stat()
            root_dir = next((p for p in ('/mnt',) if Path(p).is_dir() and Path(p).stat().st_uid == 0), None)
            denied = None
            if root_dir:
                probe = f'/mnt/host{root_dir}/.nsl-probe-{uuid.uuid4().hex}'
                denied = self.vm(home, 'touch', probe).returncode != 0 and not Path(probe[len('/mnt/host'):]).exists()
            self.record('ownership', owner == f'{os.getuid()}:{os.getgid()}' and (st.st_uid, st.st_gid) == (os.getuid(), os.getgid()) and denied is not False,
                        guest_owner=owner, root_write_owner=f'{st.st_uid}:{st.st_gid}', root_denied_in_root_owned_dir=denied)
            listener = socket.socket(socket.AF_UNIX)
            listener.bind(str(scratch/'probe.sock'))
            listener.listen(1)
            listener.setblocking(False)
            outcome = self.vm(home, 'python3', '-c', GUEST_SOCKET, guest + '/probe.sock').stdout.strip()
            try:
                listener.accept()
                reached = True
            except BlockingIOError:
                reached = False
            listener.close()
            self.record('sockets', outcome != 'connected' and not reached, guest=outcome, reached_host=reached)
        finally:
            shutil.rmtree(scratch)

    def refusal(self):
        home = self.fresh('refusal')
        data = home/'vm/data.raw'
        # A Linux swap signature: any existing signature must be refused, and this
        # one needs no formatting tool on an atomic host.
        with data.open('r+b') as f:
            f.seek(1024)
            f.write((1).to_bytes(4, 'little') + ((128 << 30) // 4096 - 1).to_bytes(4, 'little') + bytes(4) + uuid.uuid4().bytes)
            f.seek(4096 - 10)
            f.write(b'SWAPSPACE2')
        before = signature(data)
        began = time.monotonic()
        r = self.cli(home, 'recover', check=False)
        seconds = round(time.monotonic() - began, 2)
        journal = self.unit_journal(home)
        self.cli(home, 'shutdown', check=False)
        after = signature(data)
        self.record('refusal', r.returncode != 0 and before == after and 'refusing to format' in journal,
                    seconds=seconds, error=r.stderr.strip()[-200:], unchanged=before == after)

    def binding(self, bound):
        home = self.fresh('binding')
        sparse_copy(bound/'vm/data.raw', home/'vm/data.raw')
        vm = json.loads((home/'vm/vm.json').read_text())
        vm['data_gib'] = json.loads((bound/'vm/vm.json').read_text())['data_gib']
        (home/'vm/vm.json').write_text(json.dumps(vm))
        began = time.monotonic()
        r = self.cli(home, 'recover', check=False)
        seconds = round(time.monotonic() - began, 2)
        journal = self.unit_journal(home)
        self.cli(home, 'shutdown', check=False)
        self.record('binding', r.returncode != 0 and 'does not match' in journal, seconds=seconds, error=r.stderr.strip()[-200:])


def signature(path):
    """Hash a sparse disk's size and allocated data without reading its holes."""
    h = hashlib.sha256()
    with path.open('rb') as f:
        fd = f.fileno()
        size = os.fstat(fd).st_size
        h.update(size.to_bytes(8, 'little'))
        offset = 0
        while offset < size:
            try:
                start = os.lseek(fd, offset, os.SEEK_DATA)
            except OSError:  # no data after offset
                break
            offset = os.lseek(fd, start, os.SEEK_HOLE)
            h.update(start.to_bytes(8, 'little'))
            f.seek(start)
            for chunk in iter(lambda: f.read(min(1 << 20, offset - f.tell())), b''):
                h.update(chunk)
    return h.hexdigest()


def sparse_copy(source, target):
    """Replace target with a copy of a sparse disk, keeping its holes."""
    subprocess.run(['cp', '--reflink=auto', '--sparse=always', str(source), str(target)], check=True)


def version(*argv):
    try:
        return subprocess.run(argv, capture_output=True, text=True, timeout=10).stdout.splitlines()[0].strip()
    except (OSError, IndexError, subprocess.TimeoutExpired):
        return None


def main():
    p = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    p.add_argument('--nsl', type=Path, required=True)
    p.add_argument('--image', type=Path, required=True)
    p.add_argument('--evidence', type=Path, required=True)
    o = p.parse_args()
    if o.evidence.exists():
        p.error('evidence file exists')
    # State lives under the home so the VM can reach its own image cache share.
    root = Path(tempfile.mkdtemp(prefix='nsl-probe-vm-', dir=Path.home()/'.local/share'))
    probe = Probe(o.nsl.resolve(), o.image.resolve(), root)
    homes = []
    try:
        home = probe.fresh('main')
        homes.append(home)
        for check in (probe.readiness, probe.formatting, probe.host_files, probe.root_replacement, probe.growth):
            try:
                check(home)
            except (RuntimeError, subprocess.TimeoutExpired, ValueError, KeyError) as error:
                probe.record(check.__name__, False, error=str(error)[-400:])
        probe.cli(home, 'shutdown', check=False)
        for check in (probe.refusal, lambda: probe.binding(home)):
            try:
                check()
            except (RuntimeError, subprocess.TimeoutExpired, ValueError, KeyError) as error:
                probe.record(getattr(check, '__name__', 'binding'), False, error=str(error)[-400:])
    finally:
        for name in ('main', 'refusal', 'binding'):
            if (root/name/'vm').exists():
                probe.cli(root/name, 'shutdown', check=False)
        shutil.rmtree(root)
    evidence = {'schema': 1, 'date': datetime.datetime.now(datetime.timezone.utc).isoformat(timespec='seconds'),
                'image': str(o.image), 'digest': probe.digest,
                'host': {'kernel': os.uname().release, 'vmspawn': version('systemd-vmspawn', '--version'),
                         'qemu': version('qemu-system-x86_64', '--version'), 'virtiofsd': version('/usr/libexec/virtiofsd', '--version')},
                'results': probe.results}
    o.evidence.parent.mkdir(parents=True, exist_ok=True)
    with o.evidence.open('x') as f:
        json.dump(evidence, f, indent=2)
    failed = [k for k, v in probe.results.items() if not v['pass']]
    print('Evidence:', o.evidence, '| failed:', failed or 'none')
    return 1 if failed else 0


if __name__ == '__main__':
    sys.exit(main())
