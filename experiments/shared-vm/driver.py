#!/usr/bin/env python3
"""Shared-VM experiment driver, separate from the nsl CLI.

Phase 1 of docs/plans/shared-vm-experiment.md: one nsl-owned VM with a btrfs
machine-storage disk and the ADR-0016 host allowlist mirrored under /mnt/host.
It never reads or changes the CLI's state or systemd units.

  init IMAGE [--cpus N] [--memory GiB] [--disk GiB] [--data GiB]
  start | stop | status
  exec [--root] [--tty] [--workdir DIR] [--] COMMAND [ARGS...]
  check [--submounts]   record Phase 1 acceptance evidence as JSON
"""
import argparse
import base64
import datetime
import fcntl
import grp
import hashlib
import json
import os
from pathlib import Path
import pwd
import re
import shlex
import shutil
import socket
import stat
import subprocess
import sys
import time
import uuid

ROOT = Path(__file__).resolve().parents[2]
STATE = Path(os.environ.get('NSL_SHARED_VM_STATE') or Path.home()/'.local/share/nsl-shared-vm').absolute()
RUNTIME = Path('/run/user')/str(os.getuid())/'nsl-shared-vm'
UNIT = 'nsl-shared-vm.service'
EXEC = '/usr/local/libexec/nsl-exec'
UNSAFE = re.compile(r'[:\n\r\0]')


def private(path, directory=False):
    st = path.lstat()
    right_type = stat.S_ISDIR(st.st_mode) if directory else stat.S_ISREG(st.st_mode)
    if not right_type or st.st_uid != os.getuid() or st.st_mode & 0o077:
        raise RuntimeError(f'refusing unowned or shared experiment path {path}')


def load():
    private(STATE, directory=True)
    private(STATE/'state.json')
    data = json.loads((STATE/'state.json').read_text())
    if data.get('schema') != 1 or data.get('owner') != os.getuid() or not re.fullmatch('[0-9a-f]{32}', data.get('id', '')):
        raise RuntimeError('invalid experiment metadata')
    return data


def save(data):
    temporary = STATE/'state.json.tmp'
    temporary.write_text(json.dumps(data, indent=2) + '\n')
    temporary.chmod(0o600)
    temporary.replace(STATE/'state.json')


def cid(data):
    return 0x40000000 | (int(data['id'][:8], 16) & 0x3fffffff)


def description(data):
    return 'nsl shared VM ' + data['id']


def unit_state(data):
    out = subprocess.run(['systemctl', '--user', 'show', UNIT, '--property=LoadState,ActiveState,Description'],
                         capture_output=True, text=True, check=True).stdout
    values = dict(line.split('=', 1) for line in out.splitlines() if '=' in line)
    if values.get('LoadState') == 'not-found':
        return 'inactive'
    if values.get('Description') != description(data):
        raise RuntimeError('refusing foreign systemd unit ' + UNIT)
    return values.get('ActiveState', 'inactive')


def shares():
    """ADR-0016 allowlist: home, removable media and /mnt, at canonical paths."""
    user = pwd.getpwuid(os.getuid()).pw_name
    result = []
    for tree in (Path.home(), Path('/run/media')/user, Path('/mnt')):
        if not tree.is_dir():
            continue
        source = str(tree.resolve())
        if UNSAFE.search(source):
            raise ValueError('unsupported host path ' + source)
        result.append([source, '/mnt/host' + source])
    return result


def payload(argv, directory=''):
    return base64.b64encode(json.dumps({'version': 1, 'argv': argv, 'directory': directory}).encode()).decode()


def ssh_base(tty=False):
    return ['ssh', '-F', str(STATE/'ssh.config'), '-tt' if tty else '-T', 'shared']


def guest(argv, root=False, directory='', timeout=30):
    remote = [EXEC, payload(argv, directory)]
    if root:
        remote = ['sudo', '-n', '--', *remote]
    return subprocess.run(ssh_base() + remote, capture_output=True, text=True, timeout=timeout)


def guest_json(argv, **kwargs):
    r = guest(argv, **kwargs)
    if r.returncode:
        raise RuntimeError(f'{argv[0]} failed in guest: {r.stderr.strip()}')
    return json.loads(r.stdout)


def initialize(image, cpus, memory, disk, data_gib):
    image = Path(image).resolve(strict=True)
    if not image.is_file() or UNSAFE.search(str(image)) or UNSAFE.search(str(STATE)):
        raise ValueError('image must be a regular file; paths must not contain ":" or newlines')
    STATE.parent.mkdir(parents=True, exist_ok=True)
    STATE.mkdir(mode=0o700)  # Deliberately refuses a preexisting directory.
    ident = uuid.uuid4().hex
    subprocess.run(['ssh-keygen', '-q', '-t', 'ed25519', '-N', '', '-C', 'nsl-shared-vm-' + ident,
                    '-f', str(STATE/'identity')], check=True)
    public = (STATE/'identity.pub').read_text().strip()
    boot = STATE/'boot.json'
    boot.write_text(json.dumps({'version': 1, 'id': ident, 'uid': os.getuid(), 'gid': os.getgid(), 'public_key': public}))
    boot.chmod(0o600)
    subprocess.run(['qemu-img', 'create', '-q', '-f', 'qcow2', '-F', 'raw', '-b', str(image),
                    str(STATE/'disk.qcow2'), f'{disk}G'], check=True)
    subprocess.run(['qemu-img', 'create', '-q', '-f', 'qcow2', str(STATE/'machines.qcow2'), f'{data_gib}G'], check=True)
    for name in ('disk.qcow2', 'machines.qcow2'):
        (STATE/name).chmod(0o600)
    digest = hashlib.sha256()
    with image.open('rb') as f:
        while chunk := f.read(1 << 22):
            digest.update(chunk)
    data = {'schema': 1, 'owner': os.getuid(), 'id': ident, 'image': str(image),
            'image_sha256': digest.hexdigest(), 'cpus': cpus, 'memory_gib': memory,
            'disk_gib': disk, 'data_gib': data_gib, 'initialized': False}
    quote = json.dumps
    config = STATE/'ssh.config'
    config.write_text(f'''Host shared
    Hostname vsock/{cid(data)}
    User nsl
    IdentityFile {quote(str(STATE/'identity'))}
    IdentitiesOnly yes
    BatchMode yes
    StrictHostKeyChecking accept-new
    UserKnownHostsFile {quote(str(STATE/'known_hosts'))}
    HostKeyAlias nsl-shared-vm-{ident}
    ProxyCommand /usr/lib/systemd/systemd-ssh-proxy %h %p
    ProxyUseFdpass yes
    ConnectTimeout 2
    ControlMaster auto
    ControlPath {quote(str(RUNTIME/(ident[:12] + '.sock')))}
    ControlPersist 60
    ServerAliveInterval 10
    ServerAliveCountMax 3
''')
    config.chmod(0o600)
    save(data)
    print('Initialized shared VM', ident, 'in', STATE)


def vmspawn_args(data, binds):
    args = ['--user', '--no-ask-password', '--keep-unit', '--register=no',
            '--image=' + str(STATE/'disk.qcow2'), '--image-format=qcow2',
            '--machine=nsl-shared-' + data['id'][:12],
            f'--cpus={data["cpus"]}', f'--ram={data["memory_gib"]}G',
            '--kvm=yes', '--vsock=yes', f'--vsock-cid={cid(data)}', '--tpm=no', '--secure-boot=no',
            '--network-user-mode', '--notify-ready=no', '--pass-ssh-key=no', '--console=read-only',
            '--load-credential=nsl.config:' + str(STATE/'boot.json'),
            '--extra-drive=qcow2:virtio-blk:' + str(STATE/'machines.qcow2')]
    args += [f'--bind={source}:{target}' for source, target in binds]
    if os.environ.get('NSL_DEBUG') == '1':
        args.append('systemd.journald.forward_to_console=yes')
    # vmspawn mounts binds from the initrd; a writable root lets it create mount points.
    return args + ['rw']


def devices():
    """Runs inside the unit under `sg kvm`: pass device descriptors to vmspawn."""
    load()
    private(STATE/'launch.json')
    launch = json.loads((STATE/'launch.json').read_text())
    account = pwd.getpwuid(os.getuid())
    primary = grp.getgrgid(account.pw_gid).gr_name
    opened = [os.open('/dev/kvm', os.O_RDWR), os.open('/dev/vhost-vsock', os.O_RDWR)]
    # Move above the target range first so a dup2 cannot close the other device.
    copies = [fcntl.fcntl(fd, fcntl.F_DUPFD_CLOEXEC, 10) for fd in opened]
    for fd in opened:
        os.close(fd)
    for target, fd in zip((3, 4), copies):
        os.dup2(fd, target, inheritable=True)
        os.close(fd)
    for key in [k for k in os.environ if k.startswith('LISTEN_')]:
        del os.environ[key]
    os.environ.update(LISTEN_FDS='2', LISTEN_FDNAMES='kvm:vhost-vsock')
    vmspawn = shutil.which('systemd-vmspawn')
    command = ['unshare', '--user', '--map-current-user', '--keep-caps', vmspawn, *launch['vmspawn']]
    # sg runs a shell; only this fixed script and shlex-quoted argv are evaluated.
    os.execvp('sg', ['sg', primary, '-c', 'LISTEN_PID=$$; export LISTEN_PID; exec ' + shlex.join(command)])


def ready(data):
    try:
        r = guest(['cat', '/var/lib/nsl/identity.json'], timeout=5)
    except subprocess.TimeoutExpired:
        return None
    if r.returncode:
        return None
    identity = json.loads(r.stdout)
    if (identity.get('version'), identity.get('id'), identity.get('uid'), identity.get('gid')) != \
            (1, data['id'], os.getuid(), os.getgid()):
        raise RuntimeError('guest identity does not match this shared VM')
    return guest_json(['cat', '/usr/lib/nsl/image.json'], timeout=5)


def start(data):
    """Start if needed; return (started, seconds, descriptor)."""
    state = unit_state(data)
    if state == 'deactivating':
        raise RuntimeError('shared VM is stopping; retry after it stops')
    started = state not in ('active', 'activating')
    began = time.monotonic()
    if started:
        if state == 'failed':
            subprocess.run(['systemctl', '--user', 'reset-failed', UNIT], check=True)
        probe = socket.socket(socket.AF_VSOCK, socket.SOCK_STREAM)
        probe.settimeout(.3)
        try:
            probe.connect((cid(data), 22))
        except OSError:
            pass
        else:
            raise RuntimeError('vsock CID already answers SSH; refusing to reach another VM')
        finally:
            probe.close()
        RUNTIME.mkdir(mode=0o700, parents=True, exist_ok=True)
        binds = shares()
        launch = STATE/'launch.json'
        launch.write_text(json.dumps({'binds': binds, 'vmspawn': vmspawn_args(data, binds)}, indent=2) + '\n')
        launch.chmod(0o600)
        subprocess.run(['systemd-run', '--user', '--unit=' + UNIT, '--description=' + description(data), '--collect',
                        '--property=Type=exec', '--property=TimeoutStopSec=30', '--property=KillMode=mixed',
                        '--setenv=NSL_SHARED_VM_STATE=' + str(STATE), '--setenv=NSL_DEBUG=' + os.environ.get('NSL_DEBUG', ''),
                        '--', 'sg', 'kvm', '-c', 'exec ' + shlex.join([sys.executable, str(Path(__file__).resolve()), '_devices'])],
                       check=True, stdout=sys.stderr)
    deadline = began + 120
    while time.monotonic() < deadline:
        if unit_state(data) not in ('active', 'activating'):
            raise RuntimeError('vmspawn exited; inspect journalctl --user -u ' + UNIT)
        descriptor = ready(data)
        if descriptor is not None:
            if not data['initialized']:
                # First-boot identity and host keys must reach stable storage.
                guest(['sync'], timeout=30).check_returncode()
                data['initialized'] = True
                save(data)
            return started, round(time.monotonic() - began, 3), descriptor
        time.sleep(.2)
    raise RuntimeError('guest readiness timed out; state retained')


def stop(data):
    if unit_state(data) in ('active', 'activating'):
        try:
            guest(['systemctl', 'poweroff'], root=True, timeout=5)
        except subprocess.TimeoutExpired:
            pass
        deadline = time.monotonic() + 30
        while unit_state(data) not in ('inactive', 'failed') and time.monotonic() < deadline:
            time.sleep(.1)
    if unit_state(data) != 'inactive':
        subprocess.run(['systemctl', '--user', 'stop', UNIT], check=True)
    subprocess.run(ssh_base() + ['-O', 'exit'], capture_output=True)


def memory(data):
    """Proportional set size of every process in the VM unit, by command name."""
    cgroup = subprocess.run(['systemctl', '--user', 'show', UNIT, '--property=ControlGroup', '--value'],
                            capture_output=True, text=True, check=True).stdout.strip()
    totals = {}
    for pid in Path('/sys/fs/cgroup', cgroup.lstrip('/'), 'cgroup.procs').read_text().split():
        try:
            name = Path(f'/proc/{pid}/comm').read_text().strip()
            rollup = Path(f'/proc/{pid}/smaps_rollup').read_text()
        except OSError:
            continue
        kib = next(int(line.split()[1]) for line in rollup.splitlines() if line.startswith('Pss:'))
        totals[name] = totals.get(name, 0) + kib
    return {name: round(kib / 1024, 1) for name, kib in sorted(totals.items())}


def version(*argv):
    try:
        return subprocess.run(argv, capture_output=True, text=True, timeout=10).stdout.splitlines()[0].strip()
    except (OSError, IndexError, subprocess.TimeoutExpired):
        return None


GUEST_SOCKET = '''
import os, socket, stat, sys
path = sys.argv[1]
result = {"is_socket": stat.S_ISSOCK(os.stat(path).st_mode)}
s = socket.socket(socket.AF_UNIX)
s.settimeout(3)
try:
    s.connect(path)
    result["connect"] = "connected"
except OSError as e:
    result["connect"] = type(e).__name__ + ": " + os.strerror(e.errno or 0)
print(__import__("json").dumps(result))
'''

GUEST_WRITE = 'import sys; open(sys.argv[1], "x").write(sys.argv[2])'


def check(data, submounts):
    results, findings = {}, []

    def record(name, passed, **details):
        results[name] = {'pass': passed, **details}
        print(('PASS' if passed else 'FAIL'), name, json.dumps(details)[:300])

    started, seconds, descriptor = start(data)
    record('authenticated_readiness', True, started_now=started, seconds=seconds, build_id=descriptor.get('build_id'))
    launch = json.loads((STATE/'launch.json').read_text())

    root_source = guest(['findmnt', '--noheadings', '--nofsroot', '--output', 'SOURCE', '/']).stdout.strip()
    r = guest(['findmnt', '--json', '--output', 'TARGET,SOURCE,FSTYPE,LABEL,OPTIONS', '/var/lib/machines'])
    if r.returncode:
        units = guest(['systemctl', 'status', '--no-pager', 'var-lib-machines.mount', 'nsl-machines-storage.service'])
        record('machine_storage', False, root=root_source, error='/var/lib/machines is not a mount',
               units=units.stdout.strip()[-600:])
    else:
        fs = json.loads(r.stdout)['filesystems'][0]
        record('machine_storage', fs['fstype'] == 'btrfs' and fs['label'] == 'nsl-machines' and fs['source'] != root_source,
               root=root_source, **fs)
    nspawn = guest(['systemd-nspawn', '--version']).stdout.splitlines()
    record('nspawn_present', bool(nspawn), version=nspawn[0] if nspawn else None)

    mounts = guest_json(['findmnt', '--json', '--types', 'virtiofs', '--output', 'TARGET,SOURCE,OPTIONS'])['filesystems']
    expected = sorted(target for _, target in launch['binds'])
    record('host_allowlist_mounted', sorted(m['target'] for m in mounts) == expected, expected=expected, mounted=mounts)

    home = str(Path.home().resolve())
    owner = guest(['stat', '--format=%u:%g', '/mnt/host' + home]).stdout.strip()
    record('home_ownership', owner == f'{os.getuid()}:{os.getgid()}', guest_owner=owner)

    scratch = Path.home()/'.cache/nsl-shared-vm-probe'/uuid.uuid4().hex
    scratch.mkdir(mode=0o700, parents=True)
    translated = '/mnt/host' + str(scratch.resolve())
    try:
        token = uuid.uuid4().hex
        (scratch/'from-host').write_text(token)
        seen = guest(['cat', translated + '/from-host']).stdout
        record('host_to_guest_file', seen == token)

        r = guest(['python3', '-c', GUEST_WRITE, translated + '/from-guest', token])
        st = (scratch/'from-guest').stat() if (scratch/'from-guest').exists() else None
        record('guest_user_write', r.returncode == 0 and st is not None and (st.st_uid, st.st_gid) == (os.getuid(), os.getgid())
               and (scratch/'from-guest').read_text() == token, host_owner=st and f'{st.st_uid}:{st.st_gid}')

        r = guest(['python3', '-c', GUEST_WRITE, translated + '/from-root', token], root=True)
        st = (scratch/'from-root').stat() if (scratch/'from-root').exists() else None
        record('guest_root_bounded_by_host_user', st is None or st.st_uid == os.getuid(),
               returncode=r.returncode, stderr=r.stderr.strip()[-200:], host_owner=st and f'{st.st_uid}:{st.st_gid}')

        if Path('/mnt').is_dir():
            owner = guest(['stat', '--format=%u:%g', '/mnt/host/mnt']).stdout.strip()
            probe = f'/mnt/host/mnt/.nsl-probe-{token}'
            r = guest(['touch', probe], root=True)
            record('guest_root_cannot_write_host_root_dir', r.returncode != 0 and not Path(probe[len('/mnt/host'):]).exists(),
                   guest_owner=owner, stderr=r.stderr.strip()[-200:])

        listener = socket.socket(socket.AF_UNIX)
        listener.bind(str(scratch/'probe.sock'))
        listener.listen(1)
        listener.setblocking(False)
        try:
            outcome = guest_json(['python3', '-c', GUEST_SOCKET, translated + '/probe.sock'])
            try:
                listener.accept()
                reached = True
            except BlockingIOError:
                reached = False
        finally:
            listener.close()
        record('unix_sockets_not_proxied', outcome['connect'] != 'connected' and not reached, reached_host=reached, **outcome)
    finally:
        shutil.rmtree(scratch)

    user = pwd.getpwuid(os.getuid()).pw_name
    alias = Path('/home')/user
    if alias.exists() and str(alias) != home and os.path.samefile(alias, home):
        visible = guest(['test', '-e', '/mnt/host' + str(alias)]).returncode == 0
        kind = 'symlink' if Path('/home').is_symlink() else 'bind mount'
        findings.append(f'Host {alias} is the same directory as {home} through a {kind}; '
                        f'guest {"has" if visible else "lacks"} /mnt/host{alias}. Directory translation must '
                        'match by device and inode, not only by resolving symlinks.')

    host_mounts = subprocess.run(['findmnt', '--raw', '--noheadings', '--output', 'TARGET,FSTYPE'],
                                 capture_output=True, text=True, check=True).stdout.splitlines()
    nested = [line.split(' ', 1) for line in host_mounts
              if any(line.split(' ')[0].startswith(source + '/') for source, _ in launch['binds'])]
    if nested:
        details = []
        for target, fstype in nested:
            entry = {'host': target, 'host_fstype': fstype}
            if submounts:
                try:
                    r = guest(['timeout', '15', 'ls', '-A', '/mnt/host' + target], timeout=30)
                    entry.update(guest_entries=len(r.stdout.splitlines()), guest_returncode=r.returncode)
                except subprocess.TimeoutExpired:
                    entry['guest_timeout'] = True
            details.append(entry)
        results['nested_host_mounts'] = {'probed': submounts, 'mounts': details}
        findings.append('Shared trees contain nested host mounts (autofs, CIFS, other filesystems); see nested_host_mounts.')
    results['memory_pss_mib'] = memory(data)

    evidence = {
        'schema': 1, 'phase': 1,
        'date': datetime.datetime.now(datetime.timezone.utc).isoformat(timespec='seconds'),
        'host': {'kernel': os.uname().release, 'vmspawn': version('systemd-vmspawn', '--version'),
                 'qemu': version('qemu-system-x86_64', '--version'), 'virtiofsd': version('/usr/libexec/virtiofsd', '--version')},
        'vm': {k: data[k] for k in ('id', 'image', 'image_sha256', 'cpus', 'memory_gib', 'disk_gib', 'data_gib')},
        'descriptor': descriptor, 'binds': launch['binds'], 'results': results, 'findings': findings,
    }
    out = ROOT/'build/shared-vm/evidence'
    out.mkdir(parents=True, exist_ok=True)
    path = out/f'phase1-{evidence["date"].replace(":", "")}.json'
    with path.open('x') as f:
        json.dump(evidence, f, indent=2)
    for finding in findings:
        print('FINDING', finding)
    failed = [name for name, r in results.items() if isinstance(r, dict) and r.get('pass') is False]
    print('Evidence:', path)
    return 1 if failed else 0


def main():
    args = sys.argv[1:]
    if not args:
        print(__doc__.strip())
        return 2
    if args[0] == 'init':
        p = argparse.ArgumentParser(prog='driver.py init')
        p.add_argument('image')
        p.add_argument('--cpus', type=int, default=4)
        p.add_argument('--memory', type=int, default=8)
        p.add_argument('--disk', type=int, default=16)
        p.add_argument('--data', type=int, default=128)
        o = p.parse_args(args[1:])
        initialize(o.image, o.cpus, o.memory, o.disk, o.data)
        return 0
    if args == ['_devices']:
        devices()
    data = load()
    with (STATE/'lock').open('a') as lock:
        fcntl.flock(lock, fcntl.LOCK_EX)
        if args == ['start']:
            started, seconds, descriptor = start(data)
            print(f'{"Started" if started else "Already running"}; ready in {seconds}s ({descriptor.get("build_id")})')
            return 0
        if args == ['stop']:
            stop(data)
            return 0
        if args == ['status']:
            print(unit_state(data))
            return 0
        if args[0] == 'check':
            return check(data, submounts=args[1:] == ['--submounts'])
        if args[0] != 'exec':
            raise ValueError('unknown command ' + args[0])
        start(data)
    p = argparse.ArgumentParser(prog='driver.py exec')
    p.add_argument('--root', action='store_true')
    p.add_argument('--tty', action='store_true')
    p.add_argument('--workdir', default='')
    p.add_argument('command', nargs=argparse.REMAINDER)
    o = p.parse_args(args[1:])
    command = o.command[1:] if o.command[:1] == ['--'] else o.command
    if not command:
        raise ValueError('expected command')
    remote = [EXEC, payload(command, o.workdir)]
    if o.root:
        remote = ['sudo', '-n', '--', *remote]
    return subprocess.run(ssh_base(o.tty) + remote).returncode


if __name__ == '__main__':
    try:
        sys.exit(main())
    except (OSError, ValueError, RuntimeError, KeyError, subprocess.CalledProcessError, subprocess.TimeoutExpired) as error:
        print('shared-vm experiment:', error, file=sys.stderr)
        sys.exit(1)
