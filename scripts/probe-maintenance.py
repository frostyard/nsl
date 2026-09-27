#!/usr/bin/env python3
"""Destructive maintenance probe for a disposable nsl VM with a stopped backup."""
import argparse
import base64
import json
import importlib.util
import os
from pathlib import Path
import socket
import subprocess
import time
import urllib.request
import uuid

p = argparse.ArgumentParser(description=__doc__)
p.add_argument('--nsl', required=True)
p.add_argument('--home', required=True)
p.add_argument('--environment', required=True)
p.add_argument('--output', required=True)
a = p.parse_args()
env = dict(os.environ, NSL_HOME=str(Path(a.home).resolve()))
binary = str(Path(a.nsl).resolve())
output = Path(a.output).resolve()
output.parent.mkdir(parents=True, exist_ok=True)
log = output.with_suffix('.log').open('w')
result = {'environment': a.environment, 'home': env['NSL_HOME']}
label = 'nsl-probe-' + uuid.uuid4().hex[:12]
container_created = False


def run(*args, input=None, check=True):
    proc = subprocess.run([binary, *args], env=env, input=input,
                          capture_output=True, text=True, timeout=900)
    log.write(f'$ nsl {args}\n{proc.stdout}{proc.stderr}\n')
    log.flush()
    if check and proc.returncode:
        raise RuntimeError(f'{args[:3]} failed: {proc.stderr[-2000:]}')
    return proc.stdout


def guest(*args, root=False, input=None, check=True):
    flags = ['--root'] if root else []
    return run('exec', a.environment, *flags, '--', *args, input=input, check=check)


def uki_hashes(pattern):
    code = '\n'.join([
        'import glob, hashlib, sys',
        'from pathlib import Path',
        'files = sorted(glob.glob(sys.argv[1]))',
        'assert files, "no UKI files found"',
        'for path in files:',
        '    print(hashlib.sha256(Path(path).read_bytes()).hexdigest(), path)',
    ])
    return guest('python3', '-c', code, pattern, root=True).strip()


def installed_kernels():
    return json.loads(guest('python3', '-c',
        'from pathlib import Path; import json; '
        'print(json.dumps(sorted(p.parent.name for p in Path("/usr/lib/modules").glob("*/modules.dep"))))'))


def request_server(port):
    opener = urllib.request.build_opener(urllib.request.ProxyHandler({}))
    deadline = time.monotonic() + 30
    while time.monotonic() < deadline:
        try:
            with opener.open(f'http://127.0.0.1:{port}', timeout=2) as response:
                body = response.read().decode()
            if body.strip() == label:
                return
        except OSError:
            pass
        time.sleep(0.2)
    raise RuntimeError('container HTTP was not reachable on host localhost')


try:
    before = guest('uname', '-r').strip()
    boot_before = guest('cat', '/proc/sys/kernel/random/boot_id').strip()
    result['kernel_before'] = before
    result['boot_before'] = boot_before
    result['installed_kernels_before'] = installed_kernels()
    assert before in result['installed_kernels_before']
    image = json.loads(guest('cat', '/usr/lib/nsl/image.json'))
    distro = image['distribution']
    spec = importlib.util.spec_from_file_location('compose', Path(__file__).with_name('compose-image.py'))
    profiles = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(profiles)
    directory = profiles.profile_directory(Path(__file__).resolve().parents[1], distro, image['release'])
    profile = json.loads((directory/'maintenance.json').read_text())
    result['image'] = image
    if image['family'] in ('rpm', 'suse'):
        result['selinux_before'] = guest('getenforce', root=True).strip()
        assert result['selinux_before'] == 'Enforcing'
    guest(*profile['refresh'], root=True)
    guest(*profile['install_containers'], root=True)
    result['kernel_policy'] = guest(*profile['kernel_policy']).strip()
    info = json.loads(guest('podman', 'info', '--format', 'json'))
    assert info['host']['security']['rootless'] is True
    result['podman_version'] = info['version']['Version']
    result['rootless'] = True
    result['storage_driver'] = info['store']['graphDriverName']
    guest('podman', 'pull', 'docker.io/library/alpine:3.22')
    image = json.loads(guest('podman', 'image', 'inspect', 'docker.io/library/alpine:3.22'))[0]
    result['container_base_digest'] = image['Digest']
    work = '/home/nsl/' + label
    containerfile = ('FROM docker.io/library/alpine@' + image['Digest'] + '\n'
                     'RUN apk add --no-cache busybox-extras && mkdir /www && printf "%s\\n" "' + label + '" > /www/index.html\n'
                     'CMD ["busybox-extras", "httpd", "-f", "-p", "8080", "-h", "/www"]\n')
    guest('python3', '-c', 'from pathlib import Path; import sys; '
          'p=Path(sys.argv[1]); p.mkdir(); (p/"data").mkdir(); '
          '(p/"Containerfile").write_text(sys.stdin.read())', work, input=containerfile)
    guest('podman', 'build', '-t', label, work)
    guest('podman', 'run', '--rm', '--userns=keep-id', '-v', work + '/data:/data:Z',
          label, 'sh', '-c', 'printf "persistent container data" > /data/value')
    guest('python3', '-c', 'from pathlib import Path; import os,sys; '
          'p=Path(sys.argv[1])/"data/value"; assert p.stat().st_uid==os.getuid(); '
          'assert p.read_text()=="persistent container data"', work)
    guest('podman', 'run', '--rm', label, 'wget', '-qO-', 'https://deb.debian.org/debian/README')
    result['build_bind_volume_dns_https'] = True
    with socket.socket() as listener:
        listener.bind(('127.0.0.1', 0))
        port = listener.getsockname()[1]
    container_created = True
    guest('podman', 'run', '-d', '--name', label, '-p', f'127.0.0.1:{port}:8080', label)
    request_server(port)
    result['host_localhost_http'] = True
    # Exercise package/initramfs/UKI hooks even when the repository has no newer
    # kernel. Record a version upgrade only when the reboot runs a new kernel.
    result['uki_before'] = uki_hashes(profile['uki_glob'])
    guest(*(arg.format(kernel=before) for arg in profile['kernel_reinstall']), root=True)
    result['uki_after'] = uki_hashes(profile['uki_glob'])
    assert result['uki_after'] != result['uki_before'], 'kernel transaction did not regenerate the booted UKI'
    result['package_audit'] = guest(*profile['audit'], root=True).strip()
    if profile.get('audit_empty', True):
        assert not result['package_audit']
    result['installed_kernels_after'] = installed_kernels()
    added_kernels = set(result['installed_kernels_after']) - set(result['installed_kernels_before'])
    guest('sync')
    run('stop', a.environment)
    run('start', a.environment)
    after = guest('uname', '-r').strip()
    boot_after = guest('cat', '/proc/sys/kernel/random/boot_id').strip()
    assert boot_before != boot_after
    assert after in (added_kernels or {before}), 'did not boot the newly installed kernel'
    result['kernel_after'] = after
    result['boot_after'] = boot_after
    result['kernel_reinstall_and_reboot'] = True
    result['newer_kernel_upgrade_tested'] = before != after
    assert guest('cat', work + '/data/value').strip() == 'persistent container data'
    guest('podman', 'start', label)
    request_server(port)
    result['container_and_volume_survive_vm_restart'] = True
    # Repeat both without the container to check the rebuilt boot path.
    guest('podman', 'stop', label)
    result['additional_restarts'] = []
    for _ in range(2):
        run('stop', a.environment)
        start = time.monotonic()
        run('start', a.environment)
        result['additional_restarts'].append(time.monotonic() - start)
        current = guest('cat', '/proc/sys/kernel/random/boot_id').strip()
        assert current != boot_after
        boot_after = current
        guest('podman', 'run', '--rm', label, 'true')
    if result['image']['family'] in ('rpm', 'suse'):
        result['selinux_after'] = guest('getenforce', root=True).strip()
        assert result['selinux_after'] == 'Enforcing'
    result['passed'] = True
except Exception as exc:
    result['error'] = str(exc)
    # Capture AVCs and boot diagnostics before shutdown discards the journal.
    # Use the existing transport directly so a failed boot is not restarted.
    try:
        request = base64.b64encode(json.dumps(dict(version=1,
            argv=['journalctl', '-b', '--no-pager', '-n', '500'])).encode()).decode()
        diagnostics = subprocess.run(['ssh', '-F', str(Path(env['NSL_HOME'])/'environments'/a.environment/'ssh.config'),
            '-T', 'guest', 'sudo', '-n', '--', '/usr/local/libexec/nsl-exec', request],
            capture_output=True, text=True, timeout=10)
        log.write('\nFailure journal:\n' + diagnostics.stdout + diagnostics.stderr)
        log.flush()
    except (OSError, subprocess.TimeoutExpired):
        pass
    raise
finally:
    if container_created:
        # Cleanup must not try to boot an unreachable guest after a boot failure.
        request = base64.b64encode(json.dumps(dict(version=1, argv=['podman', 'rm', '-f', label])).encode()).decode()
        try:
            subprocess.run(['ssh', '-F', str(Path(env['NSL_HOME'])/'environments'/a.environment/'ssh.config'),
                            '-T', 'guest', '/usr/local/libexec/nsl-exec', request],
                           capture_output=True, timeout=10)
        except subprocess.TimeoutExpired:
            pass
    run('stop', a.environment, check=False)
    output.write_text(json.dumps(result, indent=2) + '\n')
    print(json.dumps(result, indent=2))
    log.close()
