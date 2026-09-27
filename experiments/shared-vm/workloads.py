#!/usr/bin/env python3
"""Phase 3 workload acceptance for machines in the shared VM.

  check [--gui] NAME...   record evidence for the named machines (created if missing)

Ports the Podman workflow of scripts/probe-maintenance.py and the watcher/polling
checks of scripts/probe-files.py; those scripts drive the nsl CLI's /work share.
--gui briefly opens a calculator window from each machine on the host desktop.
"""
import datetime
import json
import os
from pathlib import Path
import shutil
import socket
import subprocess
import sys
import time
import urllib.request
import uuid

import driver
import machines

IMAGES = {'debian': 'debian:13', 'fedora': 'fedora:44', 'arch': 'archlinux:rolling', 'tumbleweed': 'opensuse:tumbleweed'}
WORKLOAD = ['python3', 'jq', 'podman', 'wayland-utils', 'galculator']
INSTALL = {
    'debian': lambda pkgs: ['env', 'DEBIAN_FRONTEND=noninteractive', 'apt-get', 'install', '-y', '-qq', *pkgs],
    'fedora': lambda pkgs: ['dnf', 'install', '-y', '-q', *pkgs],
    'arch': lambda pkgs: ['pacman', '-S', '--noconfirm', '--needed', *['python' if p == 'python3' else p for p in pkgs]],
    'opensuse-tumbleweed': lambda pkgs: ['zypper', '--non-interactive', 'install', *['foot' if p == 'galculator' else p for p in pkgs]],
}
REMOVE = {
    'debian': ['env', 'DEBIAN_FRONTEND=noninteractive', 'apt-get', 'remove', '-y', '-qq', 'jq'],
    'fedora': ['dnf', 'remove', '-y', '-q', 'jq'],
    'arch': ['pacman', '-R', '--noconfirm', 'jq'],
    'opensuse-tumbleweed': ['zypper', '--non-interactive', 'remove', 'jq'],
}
WAYPIPE = os.environ.get('NSL_WAYPIPE', 'waypipe')
# Podman 5 defaults to pasta networking; Arch packages it separately.
# Minimal images carry no fonts; GTK dependencies pull some in elsewhere, but not for foot.
EXTRA = {'arch': ['passt'], 'opensuse-tumbleweed': ['dejavu-fonts']}
# galculator is not packaged for Tumbleweed; foot is a Wayland-native terminal.
GUI_APP = {'opensuse-tumbleweed': 'foot'}


def m(name, *argv, root=False, stdin=None, cd='', timeout=120):
    r, seconds = machines.run(name, list(argv), root=root, directory=cd, stdin=stdin, timeout=timeout)
    r.seconds = round(seconds, 3)
    r.text = r.stdout.decode(errors='replace').strip()
    r.err = r.stderr.decode(errors='replace').strip()
    return r


def tail(text, n=300):
    return text[-n:] if text else ''


def host_path(machine_path):
    return machine_path[len('/mnt/host'):]


def translate(path):
    """Host path to machine path, matching shared trees by device and inode (ADR-0016)."""
    trees = [source for source, _ in json.loads((driver.STATE/'launch.json').read_text())['binds']]
    path = Path(os.path.abspath(path))
    for ancestor in [path, *path.parents]:
        for tree in trees:
            try:
                if os.path.samefile(ancestor, tree):
                    return '/mnt/host' + str(Path(tree)/path.relative_to(ancestor))
            except OSError:
                continue
    return None


def check_system(name, distro):
    r = m(name, 'sh', '-c', 'cat /proc/1/comm; systemctl is-system-running; systemctl --failed --no-legend --plain | cut -d" " -f1')
    lines = r.text.splitlines()
    state = lines[1] if len(lines) > 1 else None
    return {'pass': lines[:1] == ['systemd'] and state in ('running', 'degraded'), 'pid1': lines[0] if lines else None,
            'state': state, 'failed_units': lines[2:]}


def check_packages(name, distro):
    began = time.monotonic()
    r = m(name, 'sudo', '-n', *INSTALL[distro](WORKLOAD + EXTRA.get(distro, [])), timeout=900)
    install_seconds = round(time.monotonic() - began, 1)
    present = m(name, 'sh', '-c', 'for c in python3 jq podman wayland-info galculator foot; do command -v $c >/dev/null && echo $c; done').text.split()
    removed = m(name, 'sudo', '-n', *REMOVE[distro], timeout=300)
    gone = m(name, 'sh', '-c', 'command -v jq').returncode != 0
    sudo = m(name, 'sudo', '-n', 'id', '-u')
    return {'pass': r.returncode == 0 and set(present) >= {'python3', 'jq', 'podman', 'wayland-info'} and removed.returncode == 0 and gone
            and sudo.text == '0' and 'unable to resolve' not in sudo.err, 'install_seconds': install_seconds, 'present': present,
            'install_error': tail(r.err) if r.returncode else '', 'jq_removed': gone, 'sudo': sudo.text, 'sudo_stderr': tail(sudo.err, 120)}


def check_podman(name, distro, port, peer):
    result = {}
    info = m(name, 'podman', 'info', '--format', 'json')
    try:
        data = json.loads(info.stdout)
        result.update(rootless=data['host']['security']['rootless'], driver=data['store']['graphDriverName'],
                      cgroup_manager=data['host']['cgroupManager'], version=data['version']['Version'])
    except (ValueError, KeyError):
        return {'pass': False, 'error': tail(info.err)}
    steps = {}
    work = '/home/' + machines.machine(name)['user'] + '/nsl-podman-probe'
    steps['pull'] = m(name, 'podman', 'pull', '-q', 'docker.io/library/alpine:3.22', timeout=300).returncode == 0
    digest = m(name, 'podman', 'image', 'inspect', '--format', '{{.Digest}}', 'docker.io/library/alpine:3.22').text
    containerfile = (f'FROM docker.io/library/alpine@{digest}\nRUN echo built > /built\n'
                     'CMD ["sh", "-c", "while true; do printf \\"HTTP/1.0 200 OK\\\\r\\\\n\\\\r\\\\nok\\" | nc -l -p 8080; done"]\n')
    m(name, 'sh', '-c', f'rm -rf {work} && mkdir -p {work}/data')
    m(name, 'tee', work + '/Containerfile', stdin=containerfile.encode())
    build = m(name, 'podman', 'build', '-q', '-t', 'localhost/nsl-probe', work, timeout=300)
    steps['build'] = build.returncode == 0
    volume = m(name, 'podman', 'run', '--rm', '--userns=keep-id', '-v', work + '/data:/data', 'localhost/nsl-probe',
               'sh', '-c', 'printf data > /data/value')
    owner = m(name, 'stat', '-c', '%u:%g', work + '/data/value').text
    record = machines.machine(name)
    steps['keep_id_volume'] = volume.returncode == 0 and owner == f'{record["uid"]}:{record["gid"]}'
    https = m(name, 'podman', 'run', '--rm', 'localhost/nsl-probe', 'wget', '-qO-', 'https://deb.debian.org/debian/README', timeout=120)
    steps['container_https'] = https.returncode == 0 and 'Debian' in https.text
    m(name, 'podman', 'rm', '-f', 'nsl-probe')
    published = m(name, 'podman', 'run', '-d', '--name', 'nsl-probe', '-p', f'127.0.0.1:{port}:8080', 'localhost/nsl-probe')
    time.sleep(1.5)
    fetch = ['python3', '-c', f'import urllib.request; print(urllib.request.urlopen("http://127.0.0.1:{port}/", timeout=5).read().decode())']
    steps['port_in_machine'] = published.returncode == 0 and m(name, *fetch).text == 'ok'
    steps['port_in_vm'] = driver.guest(fetch).stdout.strip() == 'ok'
    steps['port_in_peer'] = peer is not None and m(peer, *fetch).text == 'ok'
    m(name, 'podman', 'rm', '-f', 'nsl-probe')
    result['steps'] = steps
    result['errors'] = {k: tail(v.err) for k, v in (('build', build), ('volume', volume), ('https', https)) if v.returncode}
    result['pass'] = result['rootless'] is True and all(v for k, v in steps.items() if k != 'port_in_peer' or peer)
    return result


FILES = r'''
import fcntl, json, os, subprocess, sys
d = sys.argv[1]; r = {}
name = os.path.join(d, 'with space ünï.txt')
open(name, 'w').write('spaces')
r['spaces'] = open(name).read() == 'spaces'
os.symlink('with space ünï.txt', os.path.join(d, 'link'))
r['symlink'] = open(os.path.join(d, 'link')).read() == 'spaces'
script = os.path.join(d, 'run.sh')
open(script, 'w').write('#!/bin/sh\necho exec-ok\n')
os.chmod(script, 0o755)
r['exec_bit'] = subprocess.run([script], capture_output=True, text=True).stdout.strip() == 'exec-ok'
os.rename(name, name + '.renamed')
r['rename'] = os.path.exists(name + '.renamed') and not os.path.exists(name)
open(os.path.join(d, 'gone'), 'w').close(); os.unlink(os.path.join(d, 'gone'))
r['delete'] = not os.path.exists(os.path.join(d, 'gone'))
with open(os.path.join(d, 'synced'), 'w') as f:
    f.write('fsync'); f.flush(); os.fsync(f.fileno())
r['fsync'] = True
r['from_host'] = open(os.path.join(d, 'from-host')).read() == 'host'
print(json.dumps(r))
'''

HOLD_LOCK = r'''
import fcntl, sys, time
f = open(sys.argv[1], 'a'); fcntl.flock(f, fcntl.LOCK_EX); print('LOCKED', flush=True); time.sleep(float(sys.argv[2]))
'''
TRY_LOCK = r'''
import fcntl, sys
f = open(sys.argv[1], 'a')
try:
    fcntl.flock(f, fcntl.LOCK_EX | fcntl.LOCK_NB); print('acquired')
except BlockingIOError:
    print('blocked')
'''
WATCH = r'''
import ctypes, json, os, select, struct, sys, time
libc = ctypes.CDLL(None, use_errno=True)
fd = libc.inotify_init1(0)
assert libc.inotify_add_watch(fd, sys.argv[1].encode(), 0xfff) >= 0
print('READY', flush=True)
events, end = [], time.monotonic() + float(sys.argv[2])
while time.monotonic() < end:
    if not select.select([fd], [], [], max(0, end - time.monotonic()))[0]:
        break
    b = os.read(fd, 65536)
    while b:
        wd, mask, cookie, n = struct.unpack('iIII', b[:16])
        events.append(b[16:16 + n].split(b'\0')[0].decode()); b = b[16 + n:]
print(json.dumps(events))
'''
POLL = r'''
import pathlib, sys, time
p = pathlib.Path(sys.argv[1]); before = p.read_text(); print('READY', flush=True); start = time.monotonic()
while time.monotonic() - start < 5:
    if p.read_text() != before:
        print(round(time.monotonic() - start, 3)); sys.exit(0)
    time.sleep(.05)
sys.exit(1)
'''


def background(name, code, *args):
    remote = [driver.EXEC, driver.payload(machines.entry(name, ['python3', '-c', code, *args]))]
    return subprocess.Popen(driver.ssh_base() + ['sudo', '-n', '--', *remote], stdout=subprocess.PIPE, stderr=subprocess.PIPE)


def check_files(name, peer):
    scratch = Path.home()/'.cache/nsl-shared-vm-probe'/uuid.uuid4().hex
    scratch.mkdir(mode=0o700, parents=True)
    inside = translate(scratch)
    result = {}
    try:
        (scratch/'from-host').write_text('host')
        r = m(name, 'python3', '-c', FILES, inside)
        seen = json.loads(r.stdout) if r.returncode == 0 else {}
        renamed = scratch/'with space ünï.txt.renamed'
        link = scratch/'link'
        host = {'renamed_owner': renamed.exists() and renamed.stat().st_uid == os.getuid(),
                'symlink_relative': link.is_symlink() and os.readlink(link) == 'with space ünï.txt',
                'exec_mode': oct((scratch/'run.sh').stat().st_mode & 0o777) if (scratch/'run.sh').exists() else None,
                'deleted': not (scratch/'gone').exists(), 'synced': (scratch/'synced').read_text() == 'fsync'}
        result['operations'] = {'pass': bool(seen) and all(seen.values()) and host['renamed_owner'] and host['symlink_relative']
                                and host['exec_mode'] == '0o755' and host['deleted'] and host['synced'],
                                'machine': seen, 'host': host, 'error': tail(r.err)}
        # Polling sees host edits; notifications do not cross virtiofs.
        (scratch/'watched').write_text('before')
        watcher = background(name, WATCH, inside, '2')
        assert watcher.stdout.readline().strip() == b'READY'
        (scratch/'host-created').write_text('x')
        host_events = json.loads(watcher.communicate(timeout=15)[0])
        poller = background(name, POLL, inside + '/watched')
        assert poller.stdout.readline().strip() == b'READY'
        (scratch/'watched').write_text('after')
        out, _ = poller.communicate(timeout=15)
        result['host_edits'] = {'pass': poller.returncode == 0, 'inotify_events_from_host': host_events,
                                'polling_seconds': float(out) if poller.returncode == 0 else None}
        if peer:
            m(name, 'sh', '-c', f'echo {name} >> "$0"', inside + '/shared.txt')
            m(peer, 'sh', '-c', f'echo {peer} >> "$0"', inside + '/shared.txt')
            both = m(name, 'cat', inside + '/shared.txt').text.split()
            watcher = background(name, WATCH, inside, '2')
            assert watcher.stdout.readline().strip() == b'READY'
            m(peer, 'touch', inside + '/peer-created')
            peer_events = json.loads(watcher.communicate(timeout=15)[0])
            holder = background(name, HOLD_LOCK, inside + '/lock', '4')
            assert holder.stdout.readline().strip() == b'LOCKED'
            peer_lock = m(peer, 'python3', '-c', TRY_LOCK, inside + '/lock').text
            fd = os.open(scratch/'lock', os.O_WRONLY)
            try:
                import fcntl
                fcntl.flock(fd, fcntl.LOCK_EX | fcntl.LOCK_NB)
                host_lock = 'acquired'
            except BlockingIOError:
                host_lock = 'blocked'
            finally:
                os.close(fd)
            holder.communicate(timeout=15)
            result['cross_machine'] = {'pass': both == [name, peer] and (scratch/'shared.txt').read_text().split() == [name, peer]
                                       and 'peer-created' in peer_events and peer_lock == 'blocked',
                                       'machine_sees': both, 'inotify_events_from_peer': peer_events,
                                       'peer_flock': peer_lock, 'host_flock_while_machine_holds': host_lock}
    finally:
        shutil.rmtree(scratch)
    return result


def check_ports(name, port, peer):
    unit = f'nsl-http-{port}'
    started = m(name, 'systemd-run', '--user', '--unit=' + unit, '--quiet', 'python3', '-m', 'http.server', str(port), '--bind', '127.0.0.1')
    time.sleep(1)
    listening = str(port) in driver.guest(['ss', '-Hltn', f'sport = :{port}']).stdout
    forward = driver.ssh_base() + ['-O', 'forward', '-L', f'127.0.0.1:{port}:127.0.0.1:{port}']
    forwarded = subprocess.run(forward, capture_output=True).returncode == 0
    try:
        status = urllib.request.urlopen(f'http://127.0.0.1:{port}/', timeout=5).status
    except OSError as error:
        status = str(error)
    subprocess.run(driver.ssh_base() + ['-O', 'cancel', '-L', f'127.0.0.1:{port}:127.0.0.1:{port}'], capture_output=True)
    conflict = None
    if peer:
        conflict = m(peer, 'python3', '-c', f'import socket; s = socket.socket(); s.bind(("127.0.0.1", {port})); print("bound")').err.splitlines()[-1:]
    m(name, 'systemctl', '--user', 'stop', unit)
    return {'pass': started.returncode == 0 and listening and forwarded and status == 200
            and (peer is None or bool(conflict and 'Address already in use' in conflict[0])),
            'user_service': started.returncode == 0, 'vm_sees_listener': listening, 'host_status': status,
            'peer_bind_same_port': conflict[0] if conflict else None}


def check_gui(name):
    display_dir = f'/run/nsl-wayland/{name}'
    driver.guest(['sh', '-c', f'mkdir -p {display_dir} && chown 1000:1000 {display_dir}'], root=True)
    env = dict(os.environ, WAYLAND_DISPLAY=os.environ.get('WAYLAND_DISPLAY', 'wayland-0'))
    session = subprocess.Popen([WAYPIPE, '--no-gpu', '--display', display_dir + '/wayland-0', 'ssh', '-F', str(driver.STATE/'ssh.config'),
                                'shared', 'sleep', '120'], env=env, stdout=subprocess.PIPE, stderr=subprocess.PIPE)
    try:
        deadline = time.monotonic() + 15
        while time.monotonic() < deadline and driver.guest(['test', '-S', display_dir + '/wayland-0']).returncode:
            time.sleep(.2)
        machines.vm(['machinectl', 'bind', '--mkdir', name, display_dir, '/run/nsl/wayland'])
        info = m(name, 'env', 'WAYLAND_DISPLAY=/run/nsl/wayland/wayland-0', 'wayland-info', timeout=30)
        interfaces = sorted({line.split("'")[1] for line in info.text.splitlines() if "interface: '" in line})
        application = GUI_APP.get(machines.machine(name)['distro'], 'galculator')
        app = m(name, 'timeout', '3', 'env', 'WAYLAND_DISPLAY=/run/nsl/wayland/wayland-0', 'GDK_BACKEND=wayland', application, timeout=30)
        m(name, 'umount', '/run/nsl/wayland', root=True)
        return {'pass': info.returncode == 0 and 'wl_compositor' in interfaces and app.returncode == 124,
                'interfaces': len(interfaces), 'has_xdg_wm_base': 'xdg_wm_base' in interfaces, 'application': application,
                'app_returncode': app.returncode, 'app_stderr': tail(app.err, 200), 'info_error': tail(info.err, 200)}
    finally:
        session.terminate()
        try:
            session.wait(timeout=10)
        except subprocess.TimeoutExpired:
            session.kill()


def check_translation(name):
    trees = [source for source, _ in json.loads((driver.STATE/'launch.json').read_text())['binds']]
    aliases = []
    for top in sorted(Path('/').iterdir()):
        if top.is_symlink() or not top.is_dir():
            continue
        for tree in trees:
            target = Path(tree)
            for ancestor in target.parents:
                if ancestor != Path('/') and str(top) != str(ancestor) and os.path.samefile(top, ancestor):
                    relative = os.path.relpath(ancestor, '/')
                    driver.guest(['ln', '-sfn', relative, '/mnt/host' + str(top)], root=True)
                    aliases.append(f'{top} -> {relative}')
    worktree = Path(__file__).resolve().parents[2]
    cwd = os.environ.get('PWD', str(worktree))
    cases = {}
    for label, path in (('worktree', str(worktree)), ('invocation_cwd', cwd), ('alias_home', '/home/' + machines.machine(name)['user']),
                        ('unshared', '/usr/share')):
        guest = translate(path)
        seen = m(name, 'sh', '-c', 'pwd -P && ls -A | head -3', cd=guest).text.splitlines() if guest else None
        cases[label] = {'host': path, 'machine': guest, 'machine_pwd': seen[0] if seen else None}
    alias_visible = m(name, 'test', '-d', '/mnt/host/home/' + machines.machine(name)['user']).returncode == 0
    ok = cases['worktree']['machine_pwd'] is not None and cases['unshared']['machine'] is None and cases['alias_home']['machine'] is not None
    return {'pass': ok and alias_visible, 'aliases': aliases, 'alias_visible_in_machine': alias_visible, 'cases': cases}


def check_persistence(name, data):
    token = uuid.uuid4().hex
    home = '/home/' + machines.machine(name)['user']
    m(name, 'sh', '-c', f'echo {token} > ~/persist-marker')
    unit = '[Unit]\nDescription=nsl persistence probe\n\n[Service]\nExecStart=/bin/sleep infinity\n\n[Install]\nWantedBy=multi-user.target\n'
    m(name, 'tee', '/etc/systemd/system/nsl-probe.service', root=True, stdin=unit.encode())
    m(name, 'systemctl', 'enable', '--now', 'nsl-probe.service', root=True)
    driver.stop(data)
    vm_seconds = driver.start(data)[1]
    autostarted = machines.running(name)
    machine_seconds = machines.start(data, name)[0]
    after = {'marker': m(name, 'cat', home + '/persist-marker').text == token,
             'service': m(name, 'systemctl', 'is-active', 'nsl-probe.service').text == 'active',
             'package': m(name, 'sh', '-c', 'command -v python3').returncode == 0,
             'podman_image': m(name, 'podman', 'image', 'exists', 'localhost/nsl-probe').returncode == 0}
    m(name, 'systemctl', 'disable', '--now', 'nsl-probe.service', root=True)
    m(name, 'rm', '-f', '/etc/systemd/system/nsl-probe.service', root=True)
    m(name, 'rm', '-f', home + '/persist-marker')
    return {'pass': all(after.values()), **after, 'vm_restart_seconds': vm_seconds,
            'machine_autostarted': autostarted, 'machine_start_seconds': machine_seconds}


def check(names, gui):
    data = driver.load()
    started, boot, descriptor = driver.start(data)
    evidence = {'schema': 1, 'phase': 3, 'date': datetime.datetime.now(datetime.timezone.utc).isoformat(timespec='seconds'),
                'vm': {'build_id': descriptor.get('build_id'), 'nspawn': machines.vm(['systemd-nspawn', '--version']).stdout.splitlines()[0]},
                'machines': {}}
    for name in names:
        if not (driver.STATE/'machines'/(name + '.json')).exists():
            machines.create(data, name, IMAGES[name])
        seconds, state = machines.start(data, name)
        record = machines.machine(name)
        evidence['machines'][name] = {'image': record['image']['reference'], 'version': record['image']['version'],
                                      'distro': record['distro'], 'create_seconds': record['create_seconds'], 'checks': {}}
    # Every machine gets its packages first: cross-machine checks run Python in the peer.
    plan = []
    for name in names:
        distro = machines.machine(name)['distro']
        plan += [(name, 'system', check_system, (name, distro)), (name, 'packages', check_packages, (name, distro))]
    for index, name in enumerate(names):
        peer = next((n for n in names if n != name), None)
        port = 18180 + index
        plan += [(name, 'podman', check_podman, (name, None, port + 10, peer)), (name, 'files', check_files, (name, peer)),
                 (name, 'ports', check_ports, (name, port, peer)), (name, 'translation', check_translation, (name,))]
        if gui:
            plan.append((name, 'gui', check_gui, (name,)))
        # Last for each machine: it restarts the VM and every machine in it.
        plan.append((name, 'persistence', check_persistence, (name, data)))
    for name, label, function, arguments in plan:
        for needed in dict.fromkeys([name, *names]):
            if not machines.running(needed):
                machines.start(data, needed)
        began = time.monotonic()
        try:
            result = function(*arguments)
        except (subprocess.SubprocessError, RuntimeError, ValueError, OSError, AssertionError, KeyError, IndexError) as error:
            result = {'pass': False, 'exception': f'{type(error).__name__}: {error}'}
        result['seconds'] = round(time.monotonic() - began, 1)
        evidence['machines'][name]['checks'][label] = result
        parts = [f'{k}={"ok" if v.get("pass") else "FAIL"}' for k, v in result.items() if isinstance(v, dict) and 'pass' in v]
        passed = result.get('pass', bool(parts) and all(p.endswith('=ok') for p in parts))
        print(f'{name} {label}: {"PASS" if passed else "FAIL"} {" ".join(parts)} ({result["seconds"]}s)', flush=True)
    evidence['vm']['memory_pss_mib'] = driver.memory(data)
    out = driver.ROOT/'build/shared-vm/evidence'
    out.mkdir(parents=True, exist_ok=True)
    path = out/f'phase3-{evidence["date"].replace(":", "")}.json'
    with path.open('x') as f:
        json.dump(evidence, f, indent=2)
    print('Evidence:', path)


def main():
    args = sys.argv[1:]
    if not args or args[0] != 'check':
        print(__doc__.strip())
        return 2
    gui = '--gui' in args
    names = [a for a in args[1:] if a != '--gui'] or ['debian', 'fedora']
    check(names, gui)
    return 0


if __name__ == '__main__':
    try:
        sys.exit(main())
    except (OSError, ValueError, RuntimeError, KeyError, subprocess.CalledProcessError, subprocess.TimeoutExpired) as error:
        print('shared-vm workloads:', error, file=sys.stderr)
        sys.exit(1)
