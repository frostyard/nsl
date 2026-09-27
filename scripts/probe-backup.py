#!/usr/bin/env python3
"""Exercise backup/restore on a disposable guest; briefly stops the source VM."""
import argparse
import hashlib
import json
import os
from pathlib import Path
import shutil
import subprocess
import tarfile
import time
import uuid

p = argparse.ArgumentParser(description=__doc__)
p.add_argument('--nsl', required=True)
p.add_argument('--source-home', required=True)
p.add_argument('--source', required=True)
p.add_argument('--restore-home', required=True)
p.add_argument('--archive', required=True)
p.add_argument('--output', required=True)
a = p.parse_args()
binary = str(Path(a.nsl).resolve())
source_home = Path(a.source_home).resolve()
restore_home = Path(a.restore_home).resolve()
archive = Path(a.archive).resolve()
output = Path(a.output).resolve()
if restore_home.exists() or archive.exists():
    p.error('restore home and archive must not already exist')
archive.parent.mkdir(parents=True, exist_ok=True)
output.parent.mkdir(parents=True, exist_ok=True)
results = {'source': a.source, 'source_home': str(source_home),
           'restore_home': str(restore_home), 'archive': str(archive)}
marker = 'nsl-backup-probe-' + uuid.uuid4().hex


def cli(home, *args, check=True):
    env = dict(os.environ, NSL_HOME=str(home))
    result = subprocess.run([binary, *args], env=env, capture_output=True,
                            text=True, timeout=600)
    if check and result.returncode:
        raise RuntimeError(f'{args[0]} failed: {result.stderr}')
    return result


def guest(home, name, *args):
    return cli(home, 'exec', name, '--', *args).stdout


initial = next(line.split('\t')[1] for line in cli(source_home, 'list').stdout.splitlines()
               if line.split('\t')[0] == a.source)
restored = False
try:
    guest(source_home, a.source, 'python3', '-c',
          'from pathlib import Path; import sys,os; '
          'p=Path.home()/sys.argv[1]; p.write_text(sys.argv[1]); '
          'q=Path.home()/".config"/sys.argv[1]; q.write_text("setting="+sys.argv[1]); os.sync()', marker)
    packages = guest(source_home, a.source, 'dpkg-query', '-W', '-f=${Package}=${Version}\n')
    results['packages_sha256'] = hashlib.sha256(packages.encode()).hexdigest()
    results['python_version'] = guest(source_home, a.source, 'python3', '--version').strip()
    cli(source_home, 'stop', a.source)
    start = time.monotonic()
    cli(source_home, 'export', a.source, str(archive))
    results['export_seconds'] = time.monotonic() - start
    results['archive_bytes'] = archive.stat().st_size
    assert archive.stat().st_mode & 0o777 == 0o600
    start = time.monotonic()
    cli(restore_home, 'restore', 'restored', str(archive))
    restored = True
    results['restore_seconds'] = time.monotonic() - start
    assert not list((restore_home / 'images').iterdir())
    src = json.loads((source_home / 'environments' / a.source / 'environment.json').read_text())
    dst = json.loads((restore_home / 'environments/restored/environment.json').read_text())
    assert dst['id'] != src['id'] and dst['guest_id'] == src.get('guest_id', src['id'])
    assert not dst.get('project') and not dst['desktop']
    for name in ['keys/identity', 'keys/identity.pub', 'known_hosts']:
        assert ((source_home / 'environments' / a.source / name).read_bytes() ==
                (restore_home / 'environments/restored' / name).read_bytes())
    results['new_runtime_preserved_guest_identity'] = True
    start = time.monotonic()
    cli(restore_home, 'start', 'restored')
    results['first_restored_start_seconds'] = time.monotonic() - start
    restored_packages = guest(restore_home, 'restored', 'dpkg-query', '-W', '-f=${Package}=${Version}\n')
    assert restored_packages == packages
    assert guest(restore_home, 'restored', 'python3', '--version').strip() == results['python_version']
    guest(restore_home, 'restored', 'python3', '-c',
          'from pathlib import Path; import sys; '
          'assert (Path.home()/sys.argv[1]).read_text()==sys.argv[1]; '
          'assert (Path.home()/".config"/sys.argv[1]).read_text()=="setting="+sys.argv[1]', marker)
    results['files_settings_packages_preserved'] = True
    original_boot = guest(source_home, a.source, 'cat', '/proc/sys/kernel/random/boot_id').strip()
    restored_boot = guest(restore_home, 'restored', 'cat', '/proc/sys/kernel/random/boot_id').strip()
    assert original_boot != restored_boot
    guest(restore_home, 'restored', 'python3', '-c',
          'from pathlib import Path; import sys; (Path.home()/sys.argv[1]).write_text("restored-only")', marker)
    assert guest(source_home, a.source, 'cat', '/home/nsl/' + marker).strip() == marker
    results['simultaneous_vms_independent_writes'] = True
    bad = archive.with_name(archive.name + '.damaged')
    try:
        shutil.copyfile(archive, bad)
        bad.chmod(0o600)
        with tarfile.open(bad) as stream:
            offset = stream.getmember('disk.qcow2').offset_data + 200
        with bad.open('r+b') as stream:
            stream.seek(offset)
            value = stream.read(1)[0]
            stream.seek(offset)
            stream.write(bytes([value ^ 1]))
        rejected = cli(restore_home, 'restore', 'damaged', str(bad), check=False)
        assert rejected.returncode and 'checksum' in rejected.stderr
        assert not (restore_home / 'environments/damaged').exists()
        results['corruption_rejected_before_publication'] = True
    finally:
        bad.unlink(missing_ok=True)
    results['passed'] = True
finally:
    if restored:
        cli(restore_home, 'stop', 'restored', check=False)
    cli(source_home, 'exec', a.source, '--', 'rm', '-f',
        '/home/nsl/' + marker, '/home/nsl/.config/' + marker, check=False)
    if initial != 'Running':
        cli(source_home, 'stop', a.source, check=False)
    output.write_text(json.dumps(results, indent=2) + '\n')
    print(json.dumps(results, indent=2))
