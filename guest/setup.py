#!/usr/bin/python3
"""Configure a generic image from the host's per-environment boot credential."""
import grp
import json
import os
from pathlib import Path
import pwd
import re
import subprocess


def configure(config):
    if config.get('version') != 1 or not re.fullmatch('[0-9a-f]{32}', config.get('id', '')):
        raise ValueError('invalid environment identity')
    uid, gid = config['uid'], config['gid']
    if any(type(x) is not int or not 1 <= x < 2**31 for x in (uid, gid)):
        raise ValueError('development UID/GID must be positive non-root IDs')
    # The host passes a public key only. No client private key enters the image.
    key = config['public_key'].strip()
    if not re.fullmatch(r'ssh-ed25519 [A-Za-z0-9+/]+={0,2}( [^\r\n]*)?', key):
        raise ValueError('expected one ed25519 public key')
    identity = dict(version=1, id=config['id'], uid=uid, gid=gid, public_key=key)
    record = Path('/var/lib/nsl/identity.json')
    if record.exists() and json.loads(record.read_text()) != identity:
        raise ValueError('boot credential does not match this disk')
    try:
        group = grp.getgrnam('nsl')
    except KeyError:
        subprocess.run(['groupadd', '--gid', str(gid), 'nsl'], check=True)
        group = grp.getgrnam('nsl')
    if group.gr_gid != gid:
        raise ValueError('existing nsl group has another GID')
    try:
        user = pwd.getpwnam('nsl')
    except KeyError:
        subprocess.run(['useradd', '--uid', str(uid), '--gid', str(gid),
                        '--create-home', '--shell', '/bin/bash', 'nsl'], check=True)
        user = pwd.getpwnam('nsl')
    if (user.pw_uid, user.pw_gid, user.pw_dir) != (uid, gid, '/home/nsl'):
        raise ValueError('existing nsl account has another identity')
    # useradd may have persisted passwd/group before a crash created the home.
    home = Path('/home/nsl')
    home.mkdir(mode=0o700, parents=True, exist_ok=True)
    os.chown(home, uid, gid)
    for folder in ('/home/nsl/.ssh', '/home/nsl/.config'):
        Path(folder).mkdir(mode=0o700, exist_ok=True)
        os.chown(folder, uid, gid)
    authorized = Path('/home/nsl/.ssh/authorized_keys')
    # Preserve user additions after initial setup, including on later boots.
    if not authorized.exists():
        authorized.write_text(key + '\n')
        authorized.chmod(0o600)
        os.chown(authorized, uid, gid)
    sudo = Path('/etc/sudoers.d/nsl')
    sudo.write_text('nsl ALL=(ALL) NOPASSWD: ALL\n')
    sudo.chmod(0o440)
    Path('/var/lib/systemd/linger').mkdir(parents=True, exist_ok=True)
    Path('/var/lib/systemd/linger/nsl').touch()
    record.parent.mkdir(parents=True, exist_ok=True)
    if not record.exists():
        temporary = record.with_suffix('.tmp')
        temporary.write_text(json.dumps(identity) + '\n')
        temporary.chmod(0o644)
        temporary.replace(record)
    subprocess.run(["/usr/local/libexec/nsl-platform-setup"], check=True)
    # Also cover interruption before first-boot key generation completed.
    # Existing host keys are kept; the host still rejects a changed key.
    subprocess.run(['ssh-keygen', '-A'], check=True)
    os.sync()


if __name__ == '__main__':
    configure(json.loads((Path(os.environ['CREDENTIALS_DIRECTORY']) / 'nsl.config').read_text()))
