#!/usr/bin/env python3
"""Exercise one image in disposable VMs; retain evidence/backups, remove passing guests."""
import argparse
import hashlib
import json
import os
from pathlib import Path
import pty
import select
import shlex
import subprocess
import time

p = argparse.ArgumentParser(description=__doc__)
p.add_argument('--nsl', default='build/nsl')
p.add_argument('--image', type=Path, required=True)
p.add_argument('--home', type=Path, required=True)
p.add_argument('--project', type=Path, required=True)
p.add_argument('--evidence', type=Path, required=True)
a = p.parse_args()
a.image = a.image.resolve(); a.home = a.home.resolve(); a.project = a.project.resolve(); a.evidence = a.evidence.resolve()
for path in (a.home, a.project, a.evidence):
    if path.exists():
        p.error(f'test path must be unused: {path}')
a.project.mkdir(parents=True); a.evidence.mkdir(parents=True)
binary = str(Path(a.nsl).resolve()); env = dict(os.environ, NSL_HOME=str(a.home))
result = {}; passed = False
log = (a.evidence/'commands.log').open('w')


def cli(*args, data=None, check=True):
    r = subprocess.run([binary, *args], env=env, input=data, capture_output=True, timeout=180)
    stdout = r.stdout.decode(errors='replace') if data is None else f'<{len(r.stdout)} bytes; sha256 {hashlib.sha256(r.stdout).hexdigest()}>\n'
    log.write(f'$ nsl {args}\n{stdout}{r.stderr.decode(errors="replace")}\n'); log.flush()
    if check and r.returncode:
        raise RuntimeError(f'{args[:3]} failed: {r.stderr.decode(errors="replace")}')
    return r


def guest(*args, root=False):
    return cli('exec', 'dev', *(['--root'] if root else []), '--', *args).stdout.decode().strip()


def probe(script, *args):
    with (a.evidence/(script+'.log')).open('w') as output:
        subprocess.run(['python3', 'scripts/'+script+'.py', '--nsl', binary, *map(str,args)],
                       env=env, stdout=output, stderr=subprocess.STDOUT, check=True, timeout=1800)


try:
    result['image_sha256'] = hashlib.file_digest(a.image.open('rb'), 'sha256').hexdigest()
    for name in ('dev','peer'):
        cli('create', name, '--image', str(a.image), '--digest', 'sha256:'+result['image_sha256'], '--project', str(a.project))
    result['image'] = json.loads(guest('cat', '/usr/lib/nsl/image.json'))
    result['os_release'] = guest('cat', '/etc/os-release')
    os_release = dict(line.split('=', 1) for line in shlex.split(result['os_release'], comments=True))
    assert os_release['ID'] == result['image'].get('os_id', result['image']['distribution'])
    assert (os_release.get('VERSION_CODENAME') or os_release.get('VERSION_ID')) == result['image'].get('os_version', result['image']['release'])
    assert guest('uname', '-m') == 'x86_64'
    result['systemd'] = guest('systemctl', '--version').splitlines()[0]
    result['root_filesystem'] = guest('findmnt', '-n', '-o', 'FSTYPE', '/')
    assert result['root_filesystem'] == result['image']['root_filesystem']
    result['transport'] = guest('systemctl', 'is-active', 'nsl-ssh.socket')
    # Native distro security stays enabled; record policy rather than assuming it.
    if result['image']['distribution'] == 'ubuntu':
        result['apparmor'] = guest('aa-status', '--json', root=True)
        assert json.loads(result['apparmor'])['profiles']
        assert guest('cat', '/sys/module/apparmor/parameters/enabled') == 'Y'
    if result['image']['family'] == 'rpm':
        result['selinux'] = guest('getenforce')
        assert result['selinux'] == 'Enforcing'
    payload = bytes(range(256))*16
    streams = cli('exec', 'dev', '--', 'python3', '-c', 'import sys; sys.stdout.buffer.write(sys.stdin.buffer.read()); sys.stderr.write("separate stderr"); sys.exit(37)', data=payload, check=False)
    assert streams.returncode == 37 and streams.stdout == payload and streams.stderr == b'separate stderr'
    literal = ['space value', '"quote"', '$(no-shell)', ';literal', '']
    assert json.loads(guest('python3', '-c', 'import json,sys; print(json.dumps(sys.argv[1:]))', *literal)) == literal
    # Pass a real host PTY, as the CLI deliberately refuses --tty on a pipe.
    master, slave = pty.openpty()
    process = subprocess.Popen([binary, 'exec', 'dev', '--tty', '--', 'python3', '-c',
                                'import os; assert os.isatty(0) and os.isatty(1); print("PTY_OK")'],
                               env=env, stdin=slave, stdout=slave, stderr=slave)
    os.close(slave); captured = bytearray(); deadline = time.monotonic()+30
    try:
        while time.monotonic() < deadline:
            if select.select([master], [], [], .2)[0]:
                try: chunk = os.read(master, 4096)
                except OSError: break
                if not chunk: break
                captured.extend(chunk)
            if process.poll() is not None: break
        assert process.wait(timeout=5) == 0 and b'PTY_OK' in captured
    finally:
        if process.poll() is None: process.kill(); process.wait()
        os.close(master)
    result['argv_binary_streams_exit_and_pty'] = True
    guest('python3', '-c', 'from pathlib import Path; import os; p=Path("/work/ownership"); p.write_text("shared"); assert p.stat().st_uid==os.getuid()')
    assert (a.project/'ownership').read_text() == 'shared' and (a.project/'ownership').stat().st_uid == os.getuid()
    result['share_ownership'] = True
    probe('probe-native', '--environment','dev','--peer','peer','--output',a.evidence/'lifecycle.json')
    cli('export','dev',str(a.evidence/'before-maintenance.nsl'))
    probe('probe-maintenance','--home',a.home,'--environment','dev','--output',a.evidence/'maintenance.json')
    cli('export','dev',str(a.evidence/'maintained.nsl'))
    probe('probe-storage','--archive',a.evidence/'maintained.nsl','--home',a.home.parent/(a.home.name+'-storage'),
          '--project',a.project.parent/(a.project.name+'-storage'),'--peer-home',a.home,'--output',a.evidence/'storage.json')
    result['passed'] = True; passed = True
except Exception as exc:
    result['error'] = str(exc)
    raise
finally:
    if not passed:
        (a.evidence/'boot.log').write_bytes(cli('logs', 'dev', check=False).stdout)
    for name in ('dev','peer'):
        cli('stop',name,check=False)
        if passed: cli('remove',name,'--yes')
    (a.evidence/'results.json').write_text(json.dumps(result,indent=2)+'\n')
    print(json.dumps(result,indent=2)); log.close()
