#!/usr/bin/env python3
"""Machines as systemd-nspawn containers inside the shared VM (Phase 2).

Images come from hub.nspawn.org and are verified on the host against the
project key pinned from nspawn/mkosi-definitions. The VM imports them as btrfs
subvolumes. Machines share the VM's network namespace and UIDs, and bind the
VM's /mnt/host.

  pull REPO:TAG
  create NAME REPO:TAG
  start NAME | stop NAME | remove NAME | list
  exec [--method run|nsenter|shell] [--root] [--tty] [--cd DIR] NAME [--] COMMAND...
  check [NAME...]      record Phase 2 entry-method evidence (default: debian fedora)
"""
import argparse
import base64
import datetime
import hashlib
import json
import os
from pathlib import Path
import pty
import pwd
import re
import select
import statistics
import subprocess
import sys
import tempfile
import time
import urllib.request

import driver

HUB = 'https://hub.nspawn.org'
KEY = Path(__file__).with_name('nspawn-hub-cosign.pub')
CACHE = Path.home()/'.cache/nsl-shared-vm/blobs'
NAME = re.compile(r'^[a-z][a-z0-9-]{0,23}$')
REFERENCE = re.compile(r'^([a-z0-9][a-z0-9._-]*):([A-Za-z0-9._-]+)$')
MANIFEST = 'application/vnd.oci.image.manifest.v1+json'
LAYER = 'application/vnd.oci.image.layer.v1.tar+zstd'
BUNDLE = 'application/vnd.dev.sigstore.bundle.v0.3+json'
DEFAULT_IMAGES = {'debian': 'debian:13', 'fedora': 'fedora:44'}


def fetch(path, accept=None, limit=1 << 20):
    request = urllib.request.Request(HUB + path, headers={'Accept': accept} if accept else {})
    with urllib.request.urlopen(request, timeout=30) as response:
        body = response.read(limit + 1)
        if len(body) > limit:
            raise ValueError('registry response too large: ' + path)
        return body, response.headers


def verify_signature(repo, digest):
    """Accept the manifest only if a DSSE bundle verifies with the pinned project key."""
    index = json.loads(fetch(f'/v2/{repo}/referrers/{digest}')[0])
    for referrer in index.get('manifests', []):
        if referrer.get('artifactType') != BUNDLE:
            continue
        manifest = json.loads(fetch(f'/v2/{repo}/manifests/{referrer["digest"]}', MANIFEST)[0])
        layer = manifest['layers'][0]
        bundle_bytes = fetch(f'/v2/{repo}/blobs/{layer["digest"]}')[0]
        if 'sha256:' + hashlib.sha256(bundle_bytes).hexdigest() != layer['digest']:
            raise ValueError('signature bundle digest mismatch')
        bundle = json.loads(bundle_bytes)
        if 'publicKey' not in bundle.get('verificationMaterial', {}):
            continue  # The keyless signature needs Sigstore verification; the key signature suffices here.
        envelope = bundle['dsseEnvelope']
        payload = base64.b64decode(envelope['payload'])
        kind = envelope['payloadType'].encode()
        # DSSE pre-authentication encoding.
        pae = b'DSSEv1 %d %s %d %s' % (len(kind), kind, len(payload), payload)
        with tempfile.TemporaryDirectory() as work:
            Path(work, 'pae').write_bytes(pae)
            Path(work, 'sig').write_bytes(base64.b64decode(envelope['signatures'][0]['sig']))
            r = subprocess.run(['openssl', 'dgst', '-sha256', '-verify', str(KEY), '-signature',
                                str(Path(work, 'sig')), str(Path(work, 'pae'))], capture_output=True, text=True)
        if r.returncode:
            continue
        statement = json.loads(payload)
        subjects = {s['digest'].get('sha256') for s in statement.get('subject', [])}
        if statement.get('predicateType') == 'https://sigstore.dev/cosign/sign/v1' and digest.split(':', 1)[1] in subjects:
            return referrer['digest']
    raise ValueError(f'no signature by the pinned nspawn hub key covers {repo}@{digest}')


def blob(repo, digest):
    target = CACHE/digest.split(':', 1)[1]
    if target.exists():
        return target
    CACHE.mkdir(mode=0o700, parents=True, exist_ok=True)
    digest_state = hashlib.sha256()
    partial = target.with_suffix('.partial')
    with urllib.request.urlopen(HUB + f'/v2/{repo}/blobs/{digest}', timeout=60) as response, partial.open('wb') as out:
        while chunk := response.read(1 << 20):
            digest_state.update(chunk)
            out.write(chunk)
    if 'sha256:' + digest_state.hexdigest() != digest:
        partial.unlink()
        raise ValueError('layer digest mismatch for ' + digest)
    partial.chmod(0o600)
    partial.rename(target)
    return target


def pull(reference):
    match = REFERENCE.match(reference)
    if not match:
        raise ValueError('expected REPO:TAG')
    repo, tag = match.groups()
    body, headers = fetch(f'/v2/{repo}/manifests/{tag}', MANIFEST)
    digest = 'sha256:' + hashlib.sha256(body).hexdigest()
    if headers.get('Docker-Content-Digest') not in (None, digest):
        raise ValueError('registry digest header does not match the manifest')
    manifest = json.loads(body)
    if manifest.get('mediaType') != MANIFEST or len(manifest.get('layers', [])) != 1 or manifest['layers'][0]['mediaType'] != LAYER:
        raise ValueError('expected a single-layer zstd OCI manifest')
    signature = verify_signature(repo, digest)
    layer = blob(repo, manifest['layers'][0]['digest'])
    return {'reference': reference, 'manifest': digest, 'signature': signature,
            'layer': manifest['layers'][0]['digest'], 'layer_bytes': manifest['layers'][0]['size'],
            'version': manifest.get('annotations', {}).get('org.opencontainers.image.version'), 'path': str(layer)}


def vm(argv, timeout=300, check=True):
    r = driver.guest(argv, root=True, timeout=timeout)
    if check and r.returncode:
        raise RuntimeError(f'{argv[0]} failed in the shared VM: {r.stderr.strip()[-400:]}')
    return r


def offline(name, *argv, timeout=300):
    """Run a command in a stopped machine's tree, without booting it, using the VM's resolver."""
    return vm(['systemd-nspawn', '--quiet', '--register=no', '--pipe', '--bind-ro=/run/systemd/resolve',
               '--directory=/var/lib/machines/' + name, '--', *argv], timeout=timeout)


def account():
    entry = pwd.getpwuid(os.getuid())
    group = subprocess.run(['id', '-gn'], capture_output=True, text=True, check=True).stdout.strip()
    return entry.pw_name, group, os.getuid(), os.getgid()


SETTINGS = '''[Exec]
Boot=yes
PrivateUsers=no

[Network]
VirtualEthernet=no

[Files]
Bind=/mnt/host
# Machines share the VM's network namespace, so they use its resolver too.
BindReadOnly=/run/systemd/resolve
'''

# Hub images configure networkd and resolved for a private nspawn bridge. In the VM's
# namespace they would race the VM's own services, so machines use the VM's instead.
NETWORK_UNITS = ['systemd-networkd.service', 'systemd-networkd.socket', 'systemd-networkd-varlink.socket',
                 'systemd-networkd-wait-online.service', 'systemd-resolved.service',
                 'systemd-resolved-monitor.socket', 'systemd-resolved-varlink.socket']
# pam_systemd gives command sessions a logind session and user manager; sudo lets the
# passwordless account administer its own machine.
BOOTSTRAP = {
    'debian': [['apt-get', 'update', '-qq'],
               ['env', 'DEBIAN_FRONTEND=noninteractive', 'apt-get', 'install', '-y', '-qq', '--no-install-recommends',
                'libpam-systemd', 'sudo']],
    'fedora': [['dnf', 'install', '-y', '-q', '--setopt=install_weak_deps=False', 'systemd-pam', 'sudo']],
}
# A root-initiated stack with account and pam_systemd session modules. Fedora's systemd
# ships systemd-run0; Debian trixie has none, but its runuser-l falls back to common-*.
PAM_SERVICE = {'debian': 'runuser-l', 'fedora': 'systemd-run0'}

WRITE = 'import os, sys; p = sys.argv[1]; open(p, "w").write(sys.argv[2]); os.chmod(p, int(sys.argv[3], 8))'
APPEND_HOST = ('import sys; p, n = sys.argv[1:]; t = open(p).read() if __import__("os").path.exists(p) else ""\n'
               'if n not in t.split(): open(p, "a").write(("" if not t or t.endswith("\\n") else "\\n") + "127.0.1.1\\t" + n + "\\n")')


def create(data, name, reference):
    if not NAME.match(name):
        raise ValueError('invalid machine name')
    record_path = driver.STATE/'machines'/(name + '.json')
    if record_path.exists():
        raise ValueError('machine already exists: ' + name)
    image = pull(reference)
    driver.start(data)
    root = '/var/lib/machines/' + name
    if vm(['test', '-e', root], check=False).returncode == 0:
        raise ValueError(f'{root} already exists in the shared VM')
    layer = '/mnt/host' + str(Path(image['path']).resolve())
    listing = vm(['tar', '--zstd', '--list', '--file', layer]).stdout.splitlines()
    if any(Path(entry).name.startswith('.wh.') for entry in listing):
        raise ValueError('layer contains OCI whiteouts; this importer handles single-layer images only')
    began = time.monotonic()
    vm(['btrfs', 'subvolume', 'create', root])
    vm(['tar', '--zstd', '--extract', '--preserve-permissions', '--numeric-owner', '--xattrs',
        '--xattrs-include=*', '--file', layer, '--directory', root])
    user, group, uid, gid = account()
    offline(name, 'groupadd', '--gid', str(gid), group)
    offline(name, 'useradd', '--uid', str(uid), '--gid', str(gid), '--create-home', '--shell', '/bin/bash', user)
    # Hub images ship RootPassword=root; a machine must not accept it.
    offline(name, 'usermod', '--lock', 'root')
    vm(['python3', '-c', WRITE, root + '/etc/hostname', name + '\n', '644'])
    # Debian lacks nss-myhostname; sudo and others resolve the hostname through /etc/hosts.
    vm(['python3', '-c', APPEND_HOST, root + '/etc/hosts', name])
    vm(['systemctl', '--root=' + root, 'mask', *NETWORK_UNITS])
    distro = next((line.split('=', 1)[1].strip().strip('"') for line in vm(['cat', root + '/usr/lib/os-release']).stdout.splitlines()
                   if line.startswith('ID=')), '')
    if distro not in BOOTSTRAP:
        raise ValueError('no machine bootstrap for ' + distro)
    bootstrap_began = time.monotonic()
    for command in BOOTSTRAP[distro]:
        offline(name, *command, timeout=900)
    bootstrap_seconds = round(time.monotonic() - bootstrap_began, 2)
    vm(['python3', '-c', WRITE, root + '/etc/sudoers.d/nsl', f'{user} ALL=(ALL) NOPASSWD: ALL\n', '440'])
    vm(['mkdir', '-p', '/etc/systemd/nspawn'])
    vm(['python3', '-c', WRITE, f'/etc/systemd/nspawn/{name}.nspawn', SETTINGS, '644'])
    has_bus = any(vm(['test', '-e', root + path], check=False).returncode == 0
                  for path in ('/usr/bin/dbus-broker', '/usr/bin/dbus-daemon'))
    record = {'name': name, 'image': image, 'distro': distro, 'user': user, 'uid': uid, 'gid': gid, 'root_locked': True,
              'masked': NETWORK_UNITS, 'bootstrap': BOOTSTRAP[distro], 'bootstrap_seconds': bootstrap_seconds,
              'has_dbus_broker': has_bus, 'create_seconds': round(time.monotonic() - began, 2),
              'created': datetime.datetime.now(datetime.timezone.utc).isoformat(timespec='seconds')}
    record_path.parent.mkdir(mode=0o700, exist_ok=True)
    record_path.write_text(json.dumps(record, indent=2) + '\n')
    record_path.chmod(0o600)
    return record


def machine(name):
    path = driver.STATE/'machines'/(name + '.json')
    driver.private(path)
    return json.loads(path.read_text())


def running(name):
    r = vm(['machinectl', 'show', name, '--property=State', '--value'], check=False)
    return r.returncode == 0 and r.stdout.strip() == 'running'


def start(data, name):
    machine(name)
    driver.start(data)
    began = time.monotonic()
    if not running(name):
        vm(['machinectl', 'start', name])
    deadline = began + 60
    while time.monotonic() < deadline:
        r = vm(['systemd-run', '--machine=' + name, '--quiet', '--wait', '--pipe', '--',
                'systemctl', 'is-system-running', '--wait'], check=False, timeout=70)
        if r.stdout.strip() in ('running', 'degraded'):
            return round(time.monotonic() - began, 3), r.stdout.strip()
        time.sleep(.2)
    raise RuntimeError(f'machine {name} did not reach running: {r.stdout.strip()} {r.stderr.strip()[-300:]}')


def stop(data, name):
    machine(name)
    if running(name):
        vm(['machinectl', 'poweroff', name])
        deadline = time.monotonic() + 30
        while running(name) and time.monotonic() < deadline:
            time.sleep(.2)


def remove(data, name):
    path = driver.STATE/'machines'/(name + '.json')
    machine(name)
    driver.start(data)
    stop(data, name)
    if running(name):
        raise RuntimeError('machine is still running')
    root = '/var/lib/machines/' + name
    if vm(['test', '-e', root], check=False).returncode == 0:
        vm(['btrfs', 'subvolume', 'delete', '--recursive', root])
    vm(['rm', '-f', f'/etc/systemd/nspawn/{name}.nspawn'])
    path.unlink()


def entry(name, argv, method='run', root=False, tty=False, directory=''):
    """Build the VM-root argv that runs argv inside the machine."""
    m = machine(name)
    user, uid, gid = ('root', 0, 0) if root else (m['user'], m['uid'], m['gid'])
    home = '/root' if root else '/home/' + user
    directory = directory or home
    if method == 'run':
        # ExecStart= semantics expand $VAR in argv unless disabled. systemd also treats
        # SIGTERM/SIGINT deaths as clean exits, so a non-exec shell parent reports 128+N.
        # The PTY forwarder otherwise injects terminal-title and color sequences.
        args = ['env', 'SYSTEMD_ADJUST_TERMINAL_TITLE=0', 'SYSTEMD_COLORS=0',
                'systemd-run', '--machine=' + name, '--uid=' + user, '--quiet', '--expand-environment=no',
                '--wait', '--collect', '--pty' if tty else '--pipe', '--working-directory=' + directory]
        if not root:
            args.append('--property=PAMName=' + PAM_SERVICE[m['distro']])
        return args + ['--', '/bin/sh', '-c', '"$@"; exit $?', 'nsl', *argv]
    if method == 'nsenter':
        leader = vm(['machinectl', 'show', name, '--property=Leader', '--value']).stdout.strip()
        # nsenter --wd resolves in the caller's namespace; change directory inside instead.
        return ['nsenter', '--target', leader, '--all', '--setuid', str(uid), '--setgid', str(gid),
                '--', 'env', '-i', '-C', directory, 'HOME=' + home, 'USER=' + user, 'LOGNAME=' + user,
                'PATH=/usr/local/bin:/usr/bin:/bin:/usr/sbin:/sbin', 'TERM=' + os.environ.get('TERM', 'xterm-256color'), *argv]
    if method == 'shell':
        return ['machinectl', 'shell', '--quiet', f'{user}@{name}', '/usr/bin/env', '-C', directory, *argv]
    raise ValueError('unknown method ' + method)


def run(name, argv, method='run', root=False, tty=False, directory='', stdin=None, timeout=60):
    remote = [driver.EXEC, driver.payload(entry(name, argv, method, root, tty, directory))]
    command = driver.ssh_base(tty) + ['sudo', '-n', '--', *remote]
    began = time.monotonic()
    r = subprocess.run(command, input=stdin, capture_output=True, timeout=timeout)
    return r, time.monotonic() - began


ARGV = ['plain', 'with space', "single'quote", 'double"quote', '$HOME', '${HOME}', '$$', '%h', '%%', '*', '',
        'new\nline', 'tab\there', 'ünïcødé', '--flag', ';', '|', '&&', '\\backslash']
IDENTITY = ('id -u; id -g; id -un; cat /proc/sys/kernel/hostname; printf "%s\\n" "$HOME" "$(pwd)" "${XDG_RUNTIME_DIR:-none}"; '
            'cat /etc/machine-id; . /etc/os-release; echo "$ID"; stat -c %u:%g /mnt/host"$HOME_HOST"; '
            'cat /proc/self/cgroup | tail -1')


def ctrl_c(name, method):
    """Type ^C into a real host terminal, as an interactive user would."""
    command = driver.ssh_base(True) + ['sudo', '-n', '--', driver.EXEC, driver.payload(entry(name, ['sleep', '30'], method, tty=True))]
    master, slave = pty.openpty()
    process = subprocess.Popen(command, stdin=slave, stdout=slave, stderr=slave, start_new_session=True)
    os.close(slave)
    deadline = time.monotonic() + 2
    while time.monotonic() < deadline:
        if select.select([master], [], [], .1)[0]:
            try:
                os.read(master, 4096)
            except OSError:
                break
    alive = process.poll() is None
    began = time.monotonic()
    os.write(master, b'\x03')
    interrupted = None
    while time.monotonic() < began + 10:
        if process.poll() is not None:
            interrupted = round(time.monotonic() - began, 3)
            break
        if select.select([master], [], [], .1)[0]:
            try:
                os.read(master, 4096)
            except OSError:
                pass
    if process.poll() is None:
        process.kill()
        process.wait()
    os.close(master)
    return {'pass': alive and interrupted is not None and interrupted < 3, 'alive_before': alive,
            'seconds': interrupted, 'returncode': process.returncode}


def probe(name, method):
    results = {}
    home = str(Path.home().resolve())
    directory = '/mnt/host' + home

    r, _ = run(name, ['printf', '%s\\0', *ARGV], method)
    expected = b''.join(a.encode() + b'\0' for a in ARGV)
    results['argv_literal'] = {'pass': r.returncode == 0 and r.stdout == expected, 'returncode': r.returncode,
                               'stdout_prefix': r.stdout[:80].decode(errors='replace')}

    r, _ = run(name, ['sh', '-c', 'exit 42'], method)
    results['exit_status'] = {'pass': r.returncode == 42, 'returncode': r.returncode}
    r, _ = run(name, ['sh', '-c', 'kill -TERM $$'], method)
    results['signal_status'] = {'pass': r.returncode == 143, 'returncode': r.returncode}

    r, _ = run(name, ['sh', '-c', 'printf out; printf err >&2'], method)
    results['separate_streams'] = {'pass': r.stdout == b'out' and r.stderr.endswith(b'err'),
                                   'stdout': r.stdout.decode(errors='replace')[:40], 'stderr': r.stderr.decode(errors='replace')[-80:]}

    data = os.urandom(1 << 20) + b'\0\r\n\x03\x04' * 64
    r, seconds = run(name, ['cat'], method, stdin=data)
    results['binary_stream'] = {'pass': r.returncode == 0 and hashlib.sha256(r.stdout).digest() == hashlib.sha256(data).digest(),
                                'sent': len(data), 'received': len(r.stdout), 'seconds': round(seconds, 3)}

    r, _ = run(name, ['env', 'HOME_HOST=' + home, 'sh', '-c', IDENTITY], method, directory=directory)
    lines = r.stdout.decode(errors='replace').replace('\r', '').splitlines()
    m = machine(name)
    fields = dict(zip(['uid', 'gid', 'user', 'hostname', 'home', 'pwd', 'runtime_dir', 'machine_id', 'os', 'share_owner', 'cgroup'], lines))
    results['identity'] = {'pass': r.returncode == 0 and fields.get('uid') == str(m['uid']) and fields.get('gid') == str(m['gid'])
                           and fields.get('user') == m['user'] and fields.get('hostname') == name
                           and fields.get('home') == '/home/' + m['user'] and fields.get('pwd') == directory
                           and fields.get('share_owner') == f'{m["uid"]}:{m["gid"]}', **fields}

    r, _ = run(name, ['sh', '-c', 'echo "${XDG_RUNTIME_DIR:-none}"; systemctl --user is-system-running 2>&1; loginctl show-session "${XDG_SESSION_ID:-none}" --property=Class --value 2>&1'], method)
    out = r.stdout.decode(errors='replace').replace('\r', '').splitlines()
    results['user_session'] = {'pass': len(out) >= 2 and out[0] == f'/run/user/{m["uid"]}' and out[1] in ('running', 'degraded'),
                               'runtime_dir': out[0] if out else None, 'user_manager': out[1] if len(out) > 1 else None,
                               'session_class': out[2] if len(out) > 2 else None}

    r, _ = run(name, ['sh', '-c', 'test -t 0 && test -t 1 && tty'], method, tty=True)
    out = r.stdout.decode(errors='replace').strip()
    # systemd 258+ marks sessions with OSC 3008 context sequences for terminals; other
    # escapes would be forwarder noise such as title or color changes.
    context = re.findall(r'\x1b\]3008;[^\x1b\x07]*(?:\x1b\\|\x07)', out)
    plain = re.sub(r'\x1b\]3008;[^\x1b\x07]*(?:\x1b\\|\x07)', '', out)
    results['pty'] = {'pass': r.returncode == 0 and plain.startswith('/dev/pts/') and '\x1b' not in plain,
                      'returncode': r.returncode, 'tty': plain[-40:], 'osc3008_context': len(context),
                      'other_escape_sequences': '\x1b' in plain}

    results['ctrl_c'] = ctrl_c(name, method)

    samples = []
    for _ in range(10):
        r, seconds = run(name, ['true'], method)
        if r.returncode == 0:
            samples.append(seconds * 1000)
    results['latency_ms'] = {'median': round(statistics.median(samples), 1) if samples else None,
                             'max': round(max(samples), 1) if samples else None, 'samples': len(samples)}
    return results


def check(data, names):
    evidence = {'schema': 1, 'phase': 2, 'date': datetime.datetime.now(datetime.timezone.utc).isoformat(timespec='seconds'),
                'vm': {k: data[k] for k in ('id', 'cpus', 'memory_gib')}, 'machines': {}, 'concurrent': None}
    started, seconds, descriptor = driver.start(data)
    evidence['vm'].update(build_id=descriptor.get('build_id'), started_now=started, boot_seconds=seconds)
    evidence['vm']['nspawn'] = vm(['systemd-nspawn', '--version']).stdout.splitlines()[0]
    for name in names:
        record_path = driver.STATE/'machines'/(name + '.json')
        created = None if record_path.exists() else create(data, name, DEFAULT_IMAGES[name])
        boot, state = start(data, name)
        print(f'{name}: running in {boot}s ({state})')
        evidence['machines'][name] = {'record': machine(name), 'created_now': created is not None,
                                      'start_seconds': boot, 'system_state': state, 'methods': {}}
    listing = vm(['machinectl', 'list', '--no-legend', '--no-pager']).stdout
    evidence['concurrent'] = {'pass': all(re.search(rf'^{n}\s', listing, re.M) for n in names), 'machinectl_list': listing}
    for name in names:
        for method in ('run', 'nsenter', 'shell'):
            try:
                results = probe(name, method)
            except (subprocess.TimeoutExpired, RuntimeError, ValueError) as error:
                results = {'error': str(error)}
            evidence['machines'][name]['methods'][method] = results
            summary = ' '.join(f'{k}={"ok" if v.get("pass") else "FAIL"}' for k, v in results.items() if isinstance(v, dict) and 'pass' in v)
            print(f'{name} {method}: {summary} latency={results.get("latency_ms", {}).get("median")}ms')
    evidence['vm']['memory_pss_mib'] = driver.memory(data)
    ids = {n: m['methods'].get('run', {}).get('identity', {}).get('machine_id') for n, m in evidence['machines'].items()}
    evidence['distinct_machine_ids'] = len(set(ids.values())) == len(ids)
    out = driver.ROOT/'build/shared-vm/evidence'
    out.mkdir(parents=True, exist_ok=True)
    path = out/f'phase2-{evidence["date"].replace(":", "")}.json'
    with path.open('x') as f:
        json.dump(evidence, f, indent=2)
    print('Evidence:', path)
    return 0


def main():
    args = sys.argv[1:]
    if not args:
        print(__doc__.strip())
        return 2
    if args[0] == 'pull' and len(args) == 2:
        print(json.dumps(pull(args[1]), indent=2))
        return 0
    data = driver.load()
    if args[0] == 'create' and len(args) == 3:
        print(json.dumps(create(data, args[1], args[2]), indent=2))
    elif args[0] == 'start' and len(args) == 2:
        print('running in {}s ({})'.format(*start(data, args[1])))
    elif args[0] == 'stop' and len(args) == 2:
        stop(data, args[1])
    elif args[0] == 'remove' and len(args) == 2:
        remove(data, args[1])
    elif args == ['list']:
        driver.start(data)
        print(vm(['machinectl', 'list', '--no-pager']).stdout, end='')
    elif args[0] == 'check':
        return check(data, args[1:] or list(DEFAULT_IMAGES))
    elif args[0] == 'exec':
        p = argparse.ArgumentParser(prog='machines.py exec')
        p.add_argument('name')
        p.add_argument('--method', default='run', choices=('run', 'nsenter', 'shell'))
        p.add_argument('--root', action='store_true')
        p.add_argument('--tty', action='store_true')
        p.add_argument('--cd', default='')
        p.add_argument('command', nargs=argparse.REMAINDER)
        o = p.parse_args(args[1:])
        command = o.command[1:] if o.command[:1] == ['--'] else o.command
        if not command:
            raise ValueError('expected command')
        driver.start(data)
        remote = [driver.EXEC, driver.payload(entry(o.name, command, o.method, o.root, o.tty, o.cd))]
        return subprocess.run(driver.ssh_base(o.tty) + ['sudo', '-n', '--', *remote]).returncode
    else:
        raise ValueError('unknown command')
    return 0


if __name__ == '__main__':
    try:
        sys.exit(main())
    except (OSError, ValueError, RuntimeError, KeyError, subprocess.CalledProcessError, subprocess.TimeoutExpired) as error:
        print('shared-vm machines:', error, file=sys.stderr)
        sys.exit(1)
