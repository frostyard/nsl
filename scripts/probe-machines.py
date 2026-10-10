#!/usr/bin/env python3
"""Acceptance checks for nsl machine images through the CLI (docs/specs/machine-images.md).

Creates a machine from each image in a disposable state directory, then runs
the agent's entry matrix (docs/specs/agent.md), the hub-image tally checks and
the workload checks, including host integration: automatic port forwarding,
the nsl-open broker and ssh-config. Writes JSON evidence; exits nonzero if a
check fails.

  probe-machines.py --nsl build/nsl --vm-image VM.raw --machine-image M1.tar.zst ... \\
      --evidence build/image/evidence/machines-probe.json [--gui] [--isolated M.tar.zst]

--isolated also creates an isolated machine, named isolated, from an image, and
runs the workload checks other than host integration on it, plus checks that it
has no host files, desktop or broker.

When the host has a Wayland compositor, machines get desktop sessions; the
broker check records what nsl-open asks for instead of opening it. --gui also
opens a window from each machine on the host desktop for three seconds.
"""
import argparse
import base64
import datetime
import fcntl
import hashlib
import json
import os
from pathlib import Path
import pty
import re
import select
import shlex
import shutil
import socket
import statistics
import subprocess
import sys
import tempfile
import time
import urllib.request
import uuid

WORKLOAD = ['python3', 'jq', 'podman']
# Package commands by the descriptor's family.
INSTALL = {
    'debian': lambda pkgs: ['env', 'DEBIAN_FRONTEND=noninteractive', 'apt-get', 'install', '-y', '-qq', *pkgs],
    'rpm': lambda pkgs: ['dnf', 'install', '-y', '-q', *pkgs],
    'arch': lambda pkgs: ['pacman', '-S', '--noconfirm', '--needed', *['python' if p == 'python3' else p for p in pkgs]],
    'suse': lambda pkgs: ['zypper', '--non-interactive', 'install', *pkgs],
}
REFRESH = {'debian': ['apt-get', 'update', '-qq'], 'rpm': ['true'], 'arch': ['pacman', '-Sy', '--noconfirm'],
           'suse': ['zypper', '--non-interactive', '--gpg-auto-import-keys', 'refresh']}
REMOVE = {
    'debian': ['env', 'DEBIAN_FRONTEND=noninteractive', 'apt-get', 'remove', '-y', '-qq', 'jq'],
    'rpm': ['dnf', 'remove', '-y', '-q', 'jq'],
    'arch': ['pacman', '-R', '--noconfirm', 'jq'],
    'suse': ['zypper', '--non-interactive', 'remove', 'jq'],
}
ZONE_DATA = {
    'debian': ['env', 'DEBIAN_FRONTEND=noninteractive', 'apt-get', 'install', '--reinstall', '-y', '-qq', 'tzdata'],
    'rpm': ['dnf', 'reinstall', '-y', '-q', 'tzdata'],
    'arch': ['pacman', '-S', '--noconfirm', 'tzdata'],
    'suse': ['zypper', '--non-interactive', 'install', '--force', 'timezone'],
}
# Podman 5 defaults to pasta networking; Arch packages it separately.
EXTRA = {'arch': ['passt']}
# A Wayland application's package and command, by distribution. galculator is not
# packaged for openSUSE, CentOS Stream 10 or Azure Linux 4.0: foot is a Wayland-native
# terminal, zenity a GTK 4 dialog, and gtk-lshw, run without lshw-gui's pkexec, a
# GTK 3 window that waits for an answer.
GUI_APP = {'opensuse': ('foot', ['foot']), 'centos': ('zenity', ['zenity', '--info', '--text=nsl']),
           'azure': ('lshw-gui', ['gtk-lshw'])}
DEFAULT_GUI_APP = ('galculator', ['galculator'])
# The compositor's globals as wayland-info prints them: wl_display.get_registry and
# a sync, then each wl_registry.global until the callback is done.
WAYLAND_GLOBALS = r'''
import os, socket, struct, sys
display = os.environ['WAYLAND_DISPLAY']
s = socket.socket(socket.AF_UNIX)
s.connect(os.path.join(os.environ.get('XDG_RUNTIME_DIR', '/'), display))
s.sendall(struct.pack('<IHHI', 1, 1, 12, 2) + struct.pack('<IHHI', 1, 0, 12, 3))
data = b''
while True:
    while len(data) < 8 or len(data) < struct.unpack_from('<IHH', data)[2]:
        chunk = s.recv(65536)
        if not chunk:
            sys.exit('compositor closed the connection')
        data += chunk
    sender, opcode, size = struct.unpack_from('<IHH', data)
    body, data = data[8:size], data[size:]
    if sender == 1 and opcode == 0:
        sys.exit('wl_display.error')
    if sender == 2 and opcode == 0:
        name, length = struct.unpack_from('<II', body)
        interface = body[8:8 + length - 1].decode()
        version = struct.unpack_from('<I', body, 8 + (length + 3) // 4 * 4)[0]
        print(f"interface: '{interface}', version: {version}, name: {name}")
    if sender == 3 and opcode == 0:
        break
'''
# The command that lists the globals and its package, by distribution. Azure Linux 4.0
# packages no wayland-utils, so the probe lists them with the machine's Python.
WAYLAND_INFO = {'azure': (None, ['python3', '-c', WAYLAND_GLOBALS])}
DEFAULT_WAYLAND_INFO = ('wayland-utils', ['wayland-info'])
ARGV = ['plain', 'with space', "single'quote", 'double"quote', '$HOME', '${HOME}', '$$', '%h', '%%', '*', '',
        'new\nline', 'tab\there', 'ünïcødé', '--flag', ';', '|', '&&', '\\backslash']
SIGNALS = {'TERM': 143, 'INT': 130, 'HUP': 129, 'PIPE': 141, 'KILL': 137, 'SEGV': 139}
OSC3008 = re.compile(r'\x1b\]3008;[^\x1b\x07]*(?:\x1b\\|\x07)')


def tail(text, n=300):
    return text[-n:] if text else ''


class Result:
    def __init__(self, r, seconds):
        self.returncode, self.stdout, self.stderr, self.seconds = r.returncode, r.stdout, r.stderr, round(seconds, 3)
        self.text = r.stdout.decode(errors='replace').strip()
        self.err = r.stderr.decode(errors='replace').strip()


class Probe:
    def __init__(self, nsl, home):
        self.nsl, self.home = nsl, home
        # The broker's opener records targets rather than opening them.
        self.opened = home/'opened'
        opener = home/'opener'
        opener.write_text(f'#!/bin/sh\nprintf "%s\\n" "$1" >> {shlex.quote(str(self.opened))}\n')
        opener.chmod(0o755)
        self.env = dict(os.environ, NSL_HOME=str(home), NSL_OPENER=str(opener))
        runtime = Path(os.environ.get('XDG_RUNTIME_DIR', f'/run/user/{os.getuid()}'))
        display = os.environ.get('WAYLAND_DISPLAY') or ('wayland-0' if (runtime/'wayland-0').is_socket() else '')
        self.desktop = bool(display)
        if display:
            self.env['WAYLAND_DISPLAY'] = display
        self.user = os.environ.get('USER') or subprocess.run(['id', '-un'], capture_output=True, text=True).stdout.strip()
        self.machines = {}
        self.isolated = set()

    def cli(self, *args, check=True, timeout=600):
        r = subprocess.run([str(self.nsl), *args], env=self.env, capture_output=True, text=True, timeout=timeout)
        if check and r.returncode:
            raise RuntimeError(f'nsl {" ".join(args)} failed: {tail(r.stderr)}')
        return r

    def argv(self, name, argv, root=False, cd=None):
        return [str(self.nsl), 'run', '-m', name, *(['--root'] if root else []), '--cd', cd or '/home/' + self.user, '--', *argv]

    def m(self, name, *argv, root=False, stdin=None, cd=None, timeout=300, env=None):
        began = time.monotonic()
        r = subprocess.run(self.argv(name, argv, root, cd), env=dict(self.env, **(env or {})), input=stdin, capture_output=True, timeout=timeout)
        return Result(r, time.monotonic() - began)

    def background(self, name, *argv, cd=None):
        return subprocess.Popen(self.argv(name, argv, cd=cd), env=self.env, stdout=subprocess.PIPE, stderr=subprocess.PIPE)

    def vm(self, *argv, machine=None, stdin=None, timeout=60):
        """Run argv as root in the VM that hosts machine, the shared VM by default."""
        request = base64.b64encode(json.dumps({'protocol': 1, 'op': 'vm', 'argv': list(argv)}).encode()).decode()
        return subprocess.run(self.ssh_base(machine=machine) + [request], input=stdin, capture_output=True, text=True, timeout=timeout)

    def ssh_base(self, *options, machine=None):
        config = self.home/'isolated'/machine/'ssh.config' if machine in self.isolated else self.home/'vm/ssh.config'
        return ['ssh', '-F', str(config), *options, '-T', 'vm']

    def terminal(self, name, argv, keys=None, wait=2.0, limit=15):
        """Run nsl in a real terminal; optionally type keys after wait seconds."""
        master, slave = pty.openpty()
        process = subprocess.Popen(self.argv(name, argv), env=dict(self.env, TERM='xterm-256color'), stdin=slave, stdout=slave, stderr=slave,
                                   start_new_session=True)
        os.close(slave)
        output, typed, ended, began = b'', None, None, time.monotonic()
        while time.monotonic() - began < limit:
            if keys is not None and typed is None and time.monotonic() - began >= wait:
                alive = process.poll() is None
                os.write(master, keys)
                typed = (time.monotonic(), alive)
            if ended is None and process.poll() is not None:
                ended = time.monotonic()
            if select.select([master], [], [], .05)[0]:
                try:
                    chunk = os.read(master, 4096)
                except OSError:
                    chunk = b''  # EIO: every slave is closed
                output += chunk
                if not chunk and ended is not None:
                    break
            elif ended is not None:
                break
        if process.poll() is None:
            process.kill()
        process.wait()
        os.close(master)
        after = round((ended or time.monotonic()) - typed[0], 3) if typed else None
        return process.returncode, output.decode(errors='replace'), after, typed[1] if typed else None


def check_entry(p, name, machine):
    """The agent's entry matrix: argv, status, streams, identity, session, PTY and Ctrl-C."""
    out = {}
    r = p.m(name, 'printf', '%s\\0', *ARGV)
    out['argv_literal'] = {'pass': r.returncode == 0 and r.stdout == b''.join(a.encode() + b'\0' for a in ARGV), 'returncode': r.returncode}
    r = p.m(name, 'sh', '-c', 'exit 42')
    out['exit_status'] = {'pass': r.returncode == 42, 'returncode': r.returncode}
    got = {sig: p.m(name, 'sh', '-c', f'kill -{sig} $$').returncode for sig in SIGNALS}
    out['signal_status'] = {'pass': got == SIGNALS, 'statuses': got}
    r = p.m(name, 'sh', '-c', 'printf out; printf err >&2')
    out['separate_streams'] = {'pass': r.stdout == b'out' and r.stderr.endswith(b'err'), 'stderr': tail(r.err, 80)}
    data = os.urandom(1 << 20) + b'\0\r\n\x03\x04' * 64
    r = p.m(name, 'cat', stdin=data)
    out['binary_stream'] = {'pass': hashlib.sha256(r.stdout).digest() == hashlib.sha256(data).digest(), 'received': len(r.stdout), 'seconds': r.seconds}
    # An isolated machine has no host files, so it works in its own home.
    directory = '/home/' + p.user if name in p.isolated else '/mnt/host' + str(Path.home().resolve())
    r = p.m(name, 'sh', '-c', 'id -u; id -g; id -un; cat /proc/sys/kernel/hostname; echo "$HOME"; pwd; stat -c %u:%g "$0"', directory, cd=directory)
    fields = dict(zip(['uid', 'gid', 'user', 'hostname', 'home', 'pwd', 'owner'], r.text.splitlines()))
    out['identity'] = {'pass': r.returncode == 0 and fields.get('uid') == str(os.getuid()) and fields.get('gid') == str(os.getgid())
                       and fields.get('user') == p.user and fields.get('hostname') == name and fields.get('home') == '/home/' + p.user
                       and fields.get('pwd') == directory and fields.get('owner') == f'{os.getuid()}:{os.getgid()}', **fields}
    r = p.m(name, 'sh', '-c', 'echo "${XDG_RUNTIME_DIR:-none}"; systemctl --user is-system-running 2>&1; loginctl show-session "${XDG_SESSION_ID:-none}" --property=Class --value 2>&1')
    lines = r.text.splitlines()
    out['user_session'] = {'pass': len(lines) >= 2 and lines[0] == f'/run/user/{os.getuid()}' and lines[1] in ('running', 'degraded'),
                           'runtime_dir': lines[0] if lines else None, 'user_manager': lines[1] if len(lines) > 1 else None}
    status, text, _, _ = p.terminal(name, ['sh', '-c', 'test -t 0 && test -t 1 && tty'])
    plain = OSC3008.sub('', text).strip()
    out['pty'] = {'pass': status == 0 and plain.startswith('/dev/pts/') and '\x1b' not in plain, 'tty': plain[-40:],
                  'osc3008': len(OSC3008.findall(text)), 'returncode': status}
    status, text, after, alive = p.terminal(name, ['sleep', '30'], keys=b'\x03')
    out['ctrl_c'] = {'pass': alive is True and after is not None and after < 3, 'seconds': after, 'returncode': status}
    samples = [p.m(name, 'true').seconds * 1000 for _ in range(50)]
    out['latency_ms'] = {'median': round(statistics.median(samples), 1), 'p95': round(sorted(samples)[47], 1)}
    return out


def machine_id_tally(tree_id, reads):
    """The image tree has no machine ID, and every machine read its own: 32 characters, shared by no other.

    A failed read or a shared ID names the machines, so evidence shows which half failed. Machine
    IDs are confidential; the evidence records a read's output only when it is no ID.
    """
    failed = {n: {'returncode': r.returncode, 'length': len(r.text), 'output': '' if len(r.text) == 32 else r.text[:64],
                  'stderr': tail(r.err, 200), 'seconds': r.seconds}
              for n, r in reads.items() if r.returncode or len(r.text) != 32}
    holders = {}
    for n, r in reads.items():
        if n not in failed:
            holders.setdefault(r.text, []).append(n)
    shared = sorted(sorted(names) for names in holders.values() if len(names) > 1)
    return {'pass': tree_id in ('', 'uninitialized') and not failed and not shared,
            'image': tree_id, 'machines': len(reads), 'failed_reads': failed, 'shared': shared}


def check_tally(p, name, machine, image):
    """What the hub images needed fixing at creation must not recur."""
    listing = subprocess.run(['tar', '--zstd', '-tf', str(image)], capture_output=True, text=True).stdout.splitlines()
    tree_id = subprocess.run(['tar', '--zstd', '-xOf', str(image), './etc/machine-id'], capture_output=True, text=True).stdout.strip()
    out = {}
    shadow = p.m(name, 'getent', 'shadow', 'root', root=True).text.split(':')
    out['root_password'] = {'pass': len(shadow) > 1 and shadow[1][:1] in ('!', '*', ''), 'hash_prefix': shadow[1][:2] if len(shadow) > 1 else None}
    secrets = [e for e in listing if 'private-keys-v1.d/' in e and not e.endswith('/') or e.endswith('secring.gpg')]
    keyring = p.m(name, 'sh', '-c', 'find /etc /usr/share /var/lib -path "*private-keys-v1.d/*" -o -name secring.gpg 2>/dev/null | head', root=True).text
    out['keyrings'] = {'pass': not secrets, 'image_private_keys': secrets[:5], 'machine_generated': keyring.splitlines()[:5]}
    listed = p.m(name, 'systemctl', 'list-unit-files', '--no-legend', '--plain', 'systemd-networkd*', 'systemd-resolved*').text
    states = {fields[0]: fields[1] for fields in map(str.split, listed.splitlines()) if len(fields) > 1}
    out['network'] = {'pass': {'systemd-networkd.service', 'systemd-resolved.service'} <= set(states)
                      and all(state == 'masked' for state in states.values()), 'states': states}
    out['machine_id'] = machine_id_tally(tree_id, {n: p.m(n, 'cat', '/etc/machine-id') for n in p.machines})
    keys = [e for e in listing if re.search(r'etc/ssh/ssh_host_.*key', e)]
    sshd = p.m(name, 'sh', '-c', 'systemctl is-enabled ssh.service sshd.service ssh.socket sshd.socket 2>/dev/null').text.split()
    out['ssh'] = {'pass': not keys and 'enabled' not in sshd, 'image_host_keys': keys, 'units': sshd}
    r = p.m(name, 'sh', '-c', 'grep -c pam_systemd /etc/pam.d/nsl; command -v sudo; test -S "$XDG_RUNTIME_DIR/bus" && echo bus; '
            'find /usr/share/fonts -type f | head -1 | wc -l; grep " /run/nsl/proc " /proc/self/mountinfo | grep -q " - proc " && echo proc')
    lines = r.text.splitlines()
    out['session'] = {'pass': len(lines) == 5 and lines[0] != '0' and lines[2] == 'bus' and lines[3] == '1' and lines[4] == 'proc', 'found': lines}
    r = p.m(name, 'sudo', '-n', 'true')
    host = p.m(name, 'getent', 'hosts', name).text
    out['hostname'] = {'pass': r.returncode == 0 and 'unable to resolve' not in r.err and name in host, 'hosts': host, 'sudo_stderr': tail(r.err, 120)}
    # A host locale the machine lacks becomes C.UTF-8, and a missing LC_* goes, so nothing warns.
    r = p.m(name, 'sh', '-c', 'locale >/dev/null; locale charmap; printf "%s %s" "$LANG" "${LC_TIME:-unset}"',
            env={'LANG': 'en_US.UTF-8', 'LC_TIME': 'xx_XX.UTF-8', 'LC_ALL': ''})
    lines = r.text.splitlines()
    out['locale'] = {'pass': r.returncode == 0 and not r.err and lines[:1] == ['UTF-8'] and lines[1:] in (['en_US.UTF-8 unset'], ['C.UTF-8 unset']),
                     'found': lines, 'warnings': tail(r.err, 200)}
    link = p.m(name, 'readlink', '/etc/localtime').text
    host_zone = os.readlink('/etc/localtime').split('zoneinfo/', 1)[-1] if os.path.islink('/etc/localtime') else 'Etc/UTC'
    p.m(name, *REFRESH[machine['family']], root=True, timeout=600)
    zone = p.m(name, *ZONE_DATA[machine['family']], root=True, timeout=900)
    out['time_zone'] = {'pass': link.endswith('zoneinfo/' + host_zone) and zone.returncode == 0, 'link': link, 'zone_data_install': zone.returncode,
                        'error': tail(zone.err) if zone.returncode else ''}
    return out


def check_system(p, name, machine):
    r = p.m(name, 'sh', '-c', 'cat /proc/1/comm; systemctl is-system-running; systemctl --failed --no-legend --plain | cut -d" " -f1')
    lines = r.text.splitlines()
    return {'pass': lines[:1] == ['systemd'] and len(lines) > 1 and lines[1] in ('running', 'degraded') and not lines[2:],
            'state': lines[1] if len(lines) > 1 else None, 'failed_units': lines[2:]}


def check_packages(p, name, machine):
    family = machine['family']
    package, command = GUI_APP.get(machine['distribution'], DEFAULT_GUI_APP)
    info_package, info = WAYLAND_INFO.get(machine['distribution'], DEFAULT_WAYLAND_INFO)
    began = time.monotonic()
    packages = WORKLOAD + ([info_package] if info_package else []) + [package] + EXTRA.get(family, [])
    r = p.m(name, 'sudo', '-n', *INSTALL[family](packages), timeout=1200)
    install = round(time.monotonic() - began, 1)
    present = p.m(name, 'sh', '-c', f'for c in python3 jq podman {info[0]} {command[0]}; do command -v $c >/dev/null && echo $c; done').text.split()
    removed = p.m(name, 'sudo', '-n', *REMOVE[family], timeout=600)
    gone = p.m(name, 'sh', '-c', 'command -v jq').returncode != 0
    sudo = p.m(name, 'sudo', '-n', 'id', '-u')
    return {'pass': r.returncode == 0 and set(present) >= {'python3', 'jq', 'podman', info[0]} and removed.returncode == 0 and gone and sudo.text == '0',
            'install_seconds': install, 'present': present, 'install_error': tail(r.err) if r.returncode else '', 'sudo': sudo.text}


def check_podman(p, name, machine, port, peer):
    info = p.m(name, 'podman', 'info', '--format', 'json')
    try:
        data = json.loads(info.stdout)
        result = dict(rootless=data['host']['security']['rootless'], driver=data['store']['graphDriverName'],
                      cgroup_manager=data['host']['cgroupManager'], version=data['version']['Version'])
    except (ValueError, KeyError):
        return {'pass': False, 'error': tail(info.err)}
    steps = {}
    work = '/home/' + p.user + '/nsl-podman-probe'
    steps['pull'] = p.m(name, 'podman', 'pull', '-q', 'docker.io/library/alpine:3.22', timeout=600).returncode == 0
    digest = p.m(name, 'podman', 'image', 'inspect', '--format', '{{.Digest}}', 'docker.io/library/alpine:3.22').text
    containerfile = (f'FROM docker.io/library/alpine@{digest}\nRUN echo built > /built\n'
                     'CMD ["sh", "-c", "while true; do printf \\"HTTP/1.0 200 OK\\\\r\\\\n\\\\r\\\\nok\\" | nc -l -p 8080; done"]\n')
    p.m(name, 'sh', '-c', f'rm -rf {work} && mkdir -p {work}/data')
    p.m(name, 'tee', work + '/Containerfile', stdin=containerfile.encode())
    build = p.m(name, 'podman', 'build', '-q', '-t', 'localhost/nsl-probe', work, timeout=600)
    steps['build'] = build.returncode == 0
    volume = p.m(name, 'podman', 'run', '--rm', '--userns=keep-id', '-v', work + '/data:/data', 'localhost/nsl-probe', 'sh', '-c', 'printf data > /data/value')
    steps['keep_id_volume'] = volume.returncode == 0 and p.m(name, 'stat', '-c', '%u:%g', work + '/data/value').text == f'{os.getuid()}:{os.getgid()}'
    https = p.m(name, 'podman', 'run', '--rm', 'localhost/nsl-probe', 'wget', '-qO-', 'https://deb.debian.org/debian/README', timeout=300)
    steps['container_https'] = https.returncode == 0 and 'Debian' in https.text
    p.m(name, 'podman', 'rm', '-f', 'nsl-probe')
    published = p.m(name, 'podman', 'run', '-d', '--name', 'nsl-probe', '-p', f'127.0.0.1:{port}:8080', 'localhost/nsl-probe')
    time.sleep(2)
    fetch = ['python3', '-c', f'import urllib.request; print(urllib.request.urlopen("http://127.0.0.1:{port}/", timeout=5).read().decode())']
    steps['port_in_machine'] = published.returncode == 0 and p.m(name, *fetch).text == 'ok'
    steps['port_in_vm'] = p.vm(*fetch, machine=name).stdout.strip() == 'ok'
    steps['port_in_peer'] = peer is not None and p.m(peer, *fetch).text == 'ok'
    p.m(name, 'podman', 'rm', '-f', 'nsl-probe')
    result['steps'] = steps
    result['errors'] = {k: tail(v.err) for k, v in (('build', build), ('volume', volume), ('https', https)) if v.returncode}
    result['pass'] = result['rootless'] is True and all(v for k, v in steps.items() if k != 'port_in_peer' or peer)
    return result


FILES = r'''
import json, os, subprocess, sys
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


def check_files(p, name, machine, peer):
    scratch = Path.home()/'.cache/nsl-probe-machines'/uuid.uuid4().hex
    scratch.mkdir(mode=0o700, parents=True)
    inside = '/mnt/host' + str(scratch.resolve())
    result = {}
    try:
        (scratch/'from-host').write_text('host')
        r = p.m(name, 'python3', '-c', FILES, inside)
        seen = json.loads(r.stdout) if r.returncode == 0 else {}
        renamed, link = scratch/'with space ünï.txt.renamed', scratch/'link'
        host = {'renamed_owner': renamed.exists() and renamed.stat().st_uid == os.getuid(),
                'symlink_relative': link.is_symlink() and os.readlink(link) == 'with space ünï.txt',
                'exec_mode': oct((scratch/'run.sh').stat().st_mode & 0o777) if (scratch/'run.sh').exists() else None,
                'deleted': not (scratch/'gone').exists(), 'synced': (scratch/'synced').read_text() == 'fsync'}
        result['operations'] = {'pass': bool(seen) and all(seen.values()) and host['renamed_owner'] and host['symlink_relative']
                                and host['exec_mode'] == '0o755' and host['deleted'] and host['synced'], 'machine': seen, 'host': host, 'error': tail(r.err)}
        (scratch/'watched').write_text('before')
        watcher = p.background(name, 'python3', '-c', WATCH, inside, '2')
        assert watcher.stdout.readline().strip() == b'READY'
        (scratch/'host-created').write_text('x')
        host_events = json.loads(watcher.communicate(timeout=30)[0])
        poller = p.background(name, 'python3', '-c', POLL, inside + '/watched')
        assert poller.stdout.readline().strip() == b'READY'
        (scratch/'watched').write_text('after')
        out, _ = poller.communicate(timeout=30)
        result['host_edits'] = {'pass': poller.returncode == 0, 'inotify_events_from_host': host_events,
                                'polling_seconds': float(out) if poller.returncode == 0 else None}
        if peer:
            p.m(name, 'sh', '-c', f'echo {name} >> "$0"', inside + '/shared.txt')
            p.m(peer, 'sh', '-c', f'echo {peer} >> "$0"', inside + '/shared.txt')
            both = p.m(name, 'cat', inside + '/shared.txt').text.split()
            watcher = p.background(name, 'python3', '-c', WATCH, inside, '3')
            assert watcher.stdout.readline().strip() == b'READY'
            p.m(peer, 'touch', inside + '/peer-created')
            peer_events = json.loads(watcher.communicate(timeout=30)[0])
            holder = p.background(name, 'python3', '-c', HOLD_LOCK, inside + '/lock', '5')
            assert holder.stdout.readline().strip() == b'LOCKED'
            peer_lock = p.m(peer, 'python3', '-c', TRY_LOCK, inside + '/lock').text
            fd = os.open(scratch/'lock', os.O_WRONLY)
            try:
                fcntl.flock(fd, fcntl.LOCK_EX | fcntl.LOCK_NB)
                host_lock = 'acquired'
            except BlockingIOError:
                host_lock = 'blocked'
            finally:
                os.close(fd)
            holder.communicate(timeout=30)
            result['cross_machine'] = {'pass': both == [name, peer] and 'peer-created' in peer_events and peer_lock == 'blocked',
                                       'machine_sees': both, 'inotify_events_from_peer': peer_events, 'peer_flock': peer_lock,
                                       'host_flock_while_machine_holds': host_lock}
    finally:
        shutil.rmtree(scratch)
    result['pass'] = all(v['pass'] for v in result.values())
    return result


def ports_states(p, name):
    states = {}
    for line in p.cli('ports', name).stdout.splitlines():
        fields = line.split(None, 2)
        if len(fields) == 3 and fields[0].startswith('127.0.0.1:'):
            states[int(fields[0].split(':')[1])] = fields[2]
    return states


def check_ports(p, name, machine, port, peer):
    """The forwarder follows machine listeners: IPv4, wildcard and ::1 only, and reports conflicts."""
    ipv4, ipv6, taken = port, port + 100, port + 200
    units = []
    host = socket.socket()
    host.bind(('127.0.0.1', taken))
    host.listen(1)
    try:
        for listen, bind in ((ipv4, '0.0.0.0'), (ipv6, '::1'), (taken, '127.0.0.1')):
            unit = f'nsl-http-{listen}'
            p.m(name, 'systemd-run', '--user', '--unit=' + unit, '--quiet', 'python3', '-m', 'http.server', str(listen), '--bind', bind)
            units.append(unit)
        states, began = {}, time.monotonic()
        while time.monotonic() - began < 20:
            states = ports_states(p, name)
            if states.get(ipv4) == 'forwarded' and states.get(ipv6) == 'forwarded' and states.get(taken, '').startswith('conflict'):
                break
            time.sleep(.5)
        seconds = round(time.monotonic() - began, 1)
        status = {}
        for listen in (ipv4, ipv6):
            try:
                status[listen] = urllib.request.urlopen(f'http://127.0.0.1:{listen}/', timeout=5).status
            except OSError as error:
                status[listen] = str(error)
        # The host's own listener still answers on the conflicting port.
        client = socket.create_connection(('127.0.0.1', taken), timeout=5)
        host.settimeout(5)
        kept = host.accept()[0] is not None
        client.close()
        conflict = None
        if peer:
            conflict = p.m(peer, 'python3', '-c', f'import socket; s = socket.socket(); s.bind(("127.0.0.1", {ipv4})); print("bound")').err.splitlines()[-1:]
        for unit in units:
            p.m(name, 'systemctl', '--user', 'stop', unit)
        gone, began = False, time.monotonic()
        while time.monotonic() - began < 10 and not gone:
            gone = ipv4 not in ports_states(p, name)
            time.sleep(.5)
    finally:
        host.close()
    return {'pass': states.get(ipv4) == 'forwarded' and states.get(ipv6) == 'forwarded' and states.get(taken, '').startswith('conflict')
            and status == {ipv4: 200, ipv6: 200} and kept and gone and (peer is None or bool(conflict and 'Address already in use' in conflict[0])),
            'states': {str(k): v for k, v in states.items() if k in (ipv4, ipv6, taken)}, 'seconds_to_forward': seconds,
            'host_status': {str(k): v for k, v in status.items()}, 'host_listener_kept': kept, 'cancelled_after_stop': gone,
            'peer_bind_same_port': conflict[0] if conflict else None}


def host_translation(directory):
    """The shared-tree path of a host directory, matching ancestors by device and inode."""
    home = Path(os.path.realpath(os.environ.get('HOME', str(Path.home()))))
    for ancestor in [directory, *directory.parents]:
        if os.path.samefile(ancestor, home):
            return str(home/directory.relative_to(ancestor))
    return str(directory.resolve())


def check_translation(p, name, machine):
    worktree = Path(__file__).resolve().parents[1]
    cases = {}
    for label, directory in (('worktree', worktree), ('alias_home', Path('/home')/p.user), ('unshared', Path('/usr/share'))):
        if not directory.is_dir():
            continue
        r = subprocess.run([str(p.nsl), 'run', '-m', name, 'pwd', '-P'], cwd=directory, env=dict(p.env, PWD=str(directory)),
                           capture_output=True, text=True, timeout=120)
        cases[label] = {'host': str(directory), 'machine': r.stdout.strip() or None, 'refused': r.returncode != 0}
    alias = p.m(name, 'test', '-d', '/mnt/host/home/' + p.user).returncode == 0 if (Path('/home')/p.user).exists() else None
    ok = (cases['worktree']['machine'] == '/mnt/host' + host_translation(worktree) and cases['unshared']['refused']
          and (('alias_home' not in cases) or cases['alias_home']['machine'] is not None))
    return {'pass': ok and alias is not False, 'cases': cases, 'alias_visible': alias}


def desktop_env(p, name, variable):
    """Wait for the machine's desktop session to reach command sessions."""
    for _ in range(40):
        value = p.m(name, 'sh', '-c', f'printf %s "${variable}"').text
        if value:
            return value
        time.sleep(.5)
    return ''


def check_gui(p, name, machine):
    """A window through the machine's persistent desktop session."""
    display = desktop_env(p, name, 'WAYLAND_DISPLAY')
    info = p.m(name, *WAYLAND_INFO.get(machine['distribution'], DEFAULT_WAYLAND_INFO)[1], timeout=60)
    interfaces = sorted({line.split("'")[1] for line in info.text.splitlines() if "interface: '" in line})
    # Chromium, Electron and Qt pick Wayland by the session type; logind keeps the background class.
    session = p.m(name, 'sh', '-c', 'printf "%s/%s " "$XDG_SESSION_TYPE" "$XDG_SESSION_CLASS"; '
                  'loginctl show-session "$XDG_SESSION_ID" -p Type -p Class | sort | tr "\\n" " "').text
    application = GUI_APP.get(machine['distribution'], DEFAULT_GUI_APP)[1]
    app = p.m(name, 'timeout', '3', 'env', 'GDK_BACKEND=wayland', *application, timeout=60)
    return {'pass': display == '/run/nsl/desktop/wayland-0' and info.returncode == 0 and 'wl_compositor' in interfaces
            and 'xdg_wm_base' in interfaces and session == 'wayland/background Class=background Type=wayland' and app.returncode == 124,
            'display': display, 'interfaces': len(interfaces), 'session': session, 'application': application,
            'app_returncode': app.returncode, 'app_stderr': tail(app.err, 200)}


def check_broker(p, name, machine):
    """nsl-open reaches the host broker, which accepts only web URLs and shared paths."""
    if not p.desktop:
        return {'pass': True, 'skipped': 'no Wayland compositor on the host'}
    browser = desktop_env(p, name, 'BROWSER')
    scratch = Path.home()/'.cache/nsl-probe-machines'/uuid.uuid4().hex
    scratch.mkdir(mode=0o700, parents=True)
    try:
        (scratch/'page.html').write_text('<p>nsl</p>')
        (scratch/'escape').symlink_to('/etc/hostname')
        inside = '/mnt/host' + str(scratch.resolve())
        page = str((scratch/'page.html').resolve())
        cases = {
            'url': (['nsl-open', 'https://example.com/nsl-probe?x=1&y="2"'], None, 'https://example.com/nsl-probe?x=1&y="2"'),
            'path': (['nsl-open', inside + '/page.html'], None, page),
            'relative': (['nsl-open', 'page.html'], inside, page),
            'file_url': (['nsl-open', 'file://' + inside + '/page.html'], None, page),
            'ftp': (['nsl-open', 'ftp://example.com/'], None, None),
            'machine_file': (['nsl-open', '/etc/hostname'], None, None),
            'symlink_escape': (['nsl-open', inside + '/escape'], None, None),
        }
        results = {}
        for label, (argv, cd, expected) in cases.items():
            p.opened.write_text('')
            r = p.m(name, *argv, cd=cd)
            recorded = p.opened.read_text().splitlines()
            ok = (r.returncode == 0 and recorded == [expected]) if expected else (r.returncode != 0 and not recorded)
            results[label] = {'pass': ok, 'returncode': r.returncode, 'recorded': recorded, 'stderr': tail(r.err, 160)}
    finally:
        shutil.rmtree(scratch)
    return {'pass': browser == 'nsl-open' and all(v['pass'] for v in results.values()), 'browser': browser, 'cases': results}


def check_ssh(p, name, machine):
    """ssh-config reaches the machine account through the agent, with nothing listening."""
    config = p.home/f'ssh-{name}.config'
    config.write_text(p.cli('ssh-config', name).stdout)
    command = ['ssh', '-F', str(config), '-o', 'BatchMode=yes', f'nsl-{name}', 'id -un; cat /proc/sys/kernel/hostname']
    first = subprocess.run(command, capture_output=True, text=True, timeout=120)
    again = subprocess.run(command, capture_output=True, text=True, timeout=120)
    listeners = p.vm('ss', '-Hltn', 'sport = :22', machine=name).stdout.strip()
    lines = first.stdout.split()
    return {'pass': first.returncode == 0 and lines == [p.user, name] and again.returncode == 0 and again.stdout == first.stdout and not listeners,
            'output': lines, 'second_connection': again.returncode, 'error': tail(first.stderr) or tail(again.stderr), 'tcp_22_listeners': listeners}


def check_isolation(p, name, machine, port):
    """An isolated machine's VM has no host files, desktop or broker, but forwards its ports."""
    out = {}
    credential = json.loads(p.vm('cat', '/run/nsl/vm.json', machine=name).stdout or '{}')
    mounts = p.vm('findmnt', '-n', '-t', 'virtiofs', '-o', 'TARGET', machine=name).stdout.split()
    visible = p.m(name, 'sh', '-c', 'ls -A /mnt/host 2>/dev/null | wc -l').text
    out['host_files'] = {'pass': credential.get('role') == 'isolated' and credential.get('shares') == [] and mounts == ['/var/cache/nsl/images']
                         and visible == '0', 'role': credential.get('role'), 'vm_virtiofs': mounts, 'machine_mnt_host_entries': visible}
    r = p.m(name, 'sh', '-c', 'test -e /run/nsl/desktop && echo desktop; echo "W=$WAYLAND_DISPLAY B=$BROWSER"')
    p.opened.write_text('')
    opened = p.m(name, 'nsl-open', 'https://example.com/isolated')
    out['desktop'] = {'pass': r.text == 'W= B=' and opened.returncode != 0 and not p.opened.read_text(),
                      'environment': r.text, 'nsl_open_returncode': opened.returncode}
    worktree = Path(__file__).resolve().parents[1]
    refused = subprocess.run([str(p.nsl), 'run', '-m', name, 'pwd'], cwd=worktree, env=p.env, capture_output=True, text=True, timeout=120)
    out['no_translation'] = {'pass': refused.returncode != 0 and 'not shared' in refused.stderr, 'error': tail(refused.stderr, 120)}
    unit = f'nsl-http-{port}'
    p.m(name, 'systemd-run', '--user', '--unit=' + unit, '--quiet', 'python3', '-m', 'http.server', str(port), '--bind', '0.0.0.0')
    states, began = {}, time.monotonic()
    while time.monotonic() - began < 20 and states.get(port) != 'forwarded':
        states = ports_states(p, name)
        time.sleep(.5)
    try:
        status = urllib.request.urlopen(f'http://127.0.0.1:{port}/', timeout=5).status
    except OSError as error:
        status = str(error)
    p.m(name, 'systemctl', '--user', 'stop', unit)
    out['ports'] = {'pass': states.get(port) == 'forwarded' and status == 200, 'state': states.get(port), 'host_status': status}
    return out


def check_persistence(p, name, machine):
    token = uuid.uuid4().hex
    home = '/home/' + p.user
    p.m(name, 'sh', '-c', f'echo {token} > ~/persist-marker')
    unit = '[Unit]\nDescription=nsl persistence probe\n\n[Service]\nExecStart=/bin/sleep infinity\n\n[Install]\nWantedBy=multi-user.target\n'
    p.m(name, 'tee', '/etc/systemd/system/nsl-probe.service', root=True, stdin=unit.encode())
    p.m(name, 'systemctl', 'enable', '--now', 'nsl-probe.service', root=True)
    p.cli('shutdown')
    began = time.monotonic()
    p.cli('start', name)
    seconds = round(time.monotonic() - began, 2)
    listing = p.cli('list').stdout
    # Starting a machine starts the others in its VM, which for an isolated machine is only itself.
    same_vm = [name] if name in p.isolated else [n for n in p.machines if n not in p.isolated]
    autostarted = all(re.search(rf'^{n}\s+running\s+nsl-machine', listing, re.M) for n in same_vm)
    after = {'marker': p.m(name, 'cat', home + '/persist-marker').text == token,
             'service': p.m(name, 'systemctl', 'is-active', 'nsl-probe.service').text == 'active',
             'package': p.m(name, 'sh', '-c', 'command -v python3').returncode == 0,
             'podman_image': p.m(name, 'podman', 'image', 'exists', 'localhost/nsl-probe').returncode == 0}
    p.m(name, 'systemctl', 'disable', '--now', 'nsl-probe.service', root=True)
    p.m(name, 'rm', '-f', '/etc/systemd/system/nsl-probe.service', root=True)
    p.m(name, 'rm', '-f', home + '/persist-marker')
    return {'pass': all(after.values()) and autostarted, **after, 'vm_and_machine_start_seconds': seconds, 'all_machines_autostarted': autostarted}


def machine_name(descriptor):
    """One machine per image: Tumbleweed and Leap are both opensuse."""
    return re.sub(r'[^a-z0-9]+', '-', f"{descriptor['distribution']}-{descriptor['release']}")


def summarize(result):
    if result.get('skipped'):
        return True, 'skipped: ' + result['skipped']
    parts = [f'{k}={"ok" if v.get("pass") else "FAIL"}' for k, v in result.items() if isinstance(v, dict) and 'pass' in v]
    return result.get('pass', bool(parts) and all(p.endswith('=ok') for p in parts)), ' '.join(parts)


def main():
    parser = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    parser.add_argument('--nsl', type=Path, required=True)
    parser.add_argument('--vm-image', type=Path, required=True)
    parser.add_argument('--machine-image', type=Path, action='append', required=True)
    parser.add_argument('--evidence', type=Path, required=True)
    parser.add_argument('--gui', action='store_true')
    parser.add_argument('--only', help='comma-separated checks to run, such as ssh,broker')
    parser.add_argument('--isolated', type=Path, help='a machine image for an isolated machine')
    o = parser.parse_args()
    if o.evidence.exists():
        parser.error('evidence file exists')
    home = Path(tempfile.mkdtemp(prefix='nsl-probe-machines-', dir=Path.home()/'.local/share'))
    p = Probe(o.nsl.resolve(), home)
    evidence = {'schema': 1, 'date': datetime.datetime.now(datetime.timezone.utc).isoformat(timespec='seconds'), 'machines': {}}
    try:
        vm_digest = hashlib.sha256(o.vm_image.read_bytes()).hexdigest()
        p.cli('update', '--image', str(o.vm_image.resolve()), '--digest', 'sha256:' + vm_digest)
        for image, isolated in [(i, False) for i in o.machine_image] + ([(o.isolated, True)] if o.isolated else []):
            descriptor = json.loads(subprocess.run(['tar', '--zstd', '-xOf', str(image), './usr/lib/nsl/machine.json'],
                                                   capture_output=True, check=True).stdout)
            name = 'isolated' if isolated else machine_name(descriptor)
            digest = hashlib.sha256(image.read_bytes()).hexdigest()
            began = time.monotonic()
            if isolated:
                p.isolated.add(name)
            p.cli('create', name, '--image', str(image.resolve()), '--digest', 'sha256:' + digest, *(['--isolated'] if isolated else []))
            p.machines[name] = dict(descriptor, image=image, digest=digest, create_seconds=round(time.monotonic() - began, 2))
            began = time.monotonic()
            p.cli('start', name)
            evidence['machines'][name] = {'build_id': descriptor['build_id'], 'systemd': descriptor['systemd'], 'digest': digest, 'isolated': isolated,
                                          'create_seconds': p.machines[name]['create_seconds'],
                                          'first_start_seconds': round(time.monotonic() - began, 2), 'checks': {}}
        names = [n for n in p.machines if n not in p.isolated]
        identity = p.vm('cat', '/usr/lib/nsl/image.json').stdout
        evidence['vm'] = {'image': str(o.vm_image), 'digest': vm_digest, 'descriptor': json.loads(identity) if identity else None,
                          'nspawn': p.vm('systemd-nspawn', '--version').stdout.splitlines()[0]}
        plan = []
        for name in names:
            plan += [(name, 'entry', check_entry, ()), (name, 'system', check_system, ()), (name, 'tally', check_tally, (p.machines[name]['image'],)),
                     (name, 'packages', check_packages, ())]
        for index, name in enumerate(names):
            peer = next((n for n in names if n != name), None)
            port = 18180 + index
            plan += [(name, 'podman', check_podman, (port + 10, peer)), (name, 'files', check_files, (peer,)),
                     (name, 'ports', check_ports, (port, peer)), (name, 'translation', check_translation, ()),
                     (name, 'broker', check_broker, ()), (name, 'ssh', check_ssh, ())]
            if o.gui:
                plan.append((name, 'gui', check_gui, ()))
        for name in p.isolated:
            plan += [(name, 'entry', check_entry, ()), (name, 'system', check_system, ()), (name, 'tally', check_tally, (p.machines[name]['image'],)),
                     (name, 'packages', check_packages, ()), (name, 'podman', check_podman, (18250, None)),
                     (name, 'isolation', check_isolation, (18260,)), (name, 'ssh', check_ssh, ())]
        # Last: it restarts the VM and every machine in it.
        plan += [(name, 'persistence', check_persistence, ()) for name in names + sorted(p.isolated)]
        if o.only:
            plan = [step for step in plan if step[1] in o.only.split(',')]
        for name, label, function, arguments in plan:
            began = time.monotonic()
            try:
                result = function(p, name, p.machines[name], *arguments)
            except (subprocess.SubprocessError, RuntimeError, ValueError, OSError, AssertionError, KeyError, IndexError) as error:
                result = {'pass': False, 'exception': f'{type(error).__name__}: {error}'}
            result['seconds'] = round(time.monotonic() - began, 1)
            passed, parts = summarize(result)
            result['pass'] = passed
            evidence['machines'][name]['checks'][label] = result
            print(f'{name} {label}: {"PASS" if passed else "FAIL"} {parts} ({result["seconds"]}s)', flush=True)
    finally:
        p.cli('shutdown', check=False)
        shutil.rmtree(home, ignore_errors=True)
    o.evidence.parent.mkdir(parents=True, exist_ok=True)
    with o.evidence.open('x') as f:
        json.dump(evidence, f, indent=2, default=str)
    failed = [f'{n} {c}' for n, m in evidence['machines'].items() for c, r in m['checks'].items() if not r['pass']]
    print('Evidence:', o.evidence, '| failed:', failed or 'none')
    return 1 if failed else 0


if __name__ == '__main__':
    sys.exit(main())
