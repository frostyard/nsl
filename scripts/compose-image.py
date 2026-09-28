#!/usr/bin/env python3
"""Assemble the nsl VM image's mkosi input without running privileged build tools."""
import argparse
import hashlib
import json
from pathlib import Path
import shutil

# Must match internal/protocol; scripts/test_image_profiles.py checks both.
AGENT_PROTOCOL = 1
MACHINE_PROTOCOL = 1


def select(root, role, distribution=None, release=None, architecture='x86-64'):
    if role != 'vm':
        raise ValueError(f'unsupported image role: {role}')
    profile = json.loads((root/'image/vm/profile.json').read_text())
    if distribution not in (None, profile['distribution']) or release not in (None, profile['release']) \
            or architecture != profile['architecture']:
        raise ValueError('the VM image is {distribution} {release} on {architecture}'.format(**profile))
    return dict(profile, role=role)


def fingerprint(root, paths):
    """Hash exactly the selected inputs, including permissions and link targets."""
    digest = hashlib.sha256()
    for path in sorted(paths):
        if '__pycache__' in path.parts or path.suffix == '.pyc' or not (path.is_file() or path.is_symlink()):
            continue
        record = dict(path=path.relative_to(root).as_posix() if path.is_relative_to(root) else path.name,
                      mode=path.lstat().st_mode & 0o7777,
                      type='link' if path.is_symlink() else 'file',
                      content=(path.readlink().as_posix() if path.is_symlink()
                               else hashlib.sha256(path.read_bytes()).hexdigest()))
        digest.update(json.dumps(record, sort_keys=True, separators=(',', ':')).encode()+b'\n')
    return digest.hexdigest()


def compose(root, destination, profile, recipes, mkosi, agent):
    destination.mkdir()  # Never merge with an existing build input tree.
    layer = root/'image/vm'
    config = []
    for item in layer.iterdir():
        if item.name == 'mkosi.conf':
            config.append(item.read_text())
        elif item.name != 'profile.json':
            if item.is_dir():
                shutil.copytree(item, destination/item.name, dirs_exist_ok=True, symlinks=True,
                                ignore=shutil.ignore_patterns('__pycache__', '*.pyc'))
            else:
                shutil.copy2(item, destination/item.name, follow_symlinks=False)
    name = 'nsl-vm-{release}-{architecture}-r{revision}'.format(**profile)
    config.append(f'[Output]\nOutput={name}\nImageId=nsl-vm\n')
    (destination/'mkosi.local.conf').write_text('\n'.join(config))
    target = destination/'overlay/usr/lib/nsl/nsl-agent'
    target.parent.mkdir(parents=True, exist_ok=True)
    shutil.copyfile(agent, target)
    target.chmod(0o755)
    descriptor = dict(schema=1, role='vm', build_id=name, distribution=profile['distribution'],
                      release=profile['release'], architecture=profile['architecture'], revision=profile['revision'],
                      agent_protocol=AGENT_PROTOCOL, machine_protocol=MACHINE_PROTOCOL, transport='nsl-vsock-ssh',
                      # The finalize script fills in what the build installed.
                      systemd='@SYSTEMD@', kernel='@KERNEL@',
                      integration_sha256=fingerprint(root, [root/'scripts/compose-image.py', target, *layer.rglob('*')]),
                      recipes_revision=recipes, mkosi_revision=mkosi)
    (destination/'overlay/usr/lib/nsl/image.json').write_text(json.dumps(descriptor, indent=2)+'\n')
    return name


if __name__ == '__main__':
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument('--role', default='vm')
    p.add_argument('--distribution')
    p.add_argument('--release')
    p.add_argument('--architecture', default='x86-64')
    p.add_argument('--destination', type=Path)
    p.add_argument('--recipes', default='')
    p.add_argument('--mkosi', default='')
    p.add_argument('--agent', type=Path)
    a = p.parse_args()
    root = Path(__file__).resolve().parents[1]
    try:
        selected = select(root, a.role, a.distribution, a.release, a.architecture)
        if a.destination:
            if not a.agent or not a.agent.is_file():
                raise ValueError('the VM image needs --agent with a built nsl-agent')
            print(compose(root, a.destination, selected, a.recipes, a.mkosi, a.agent))
        else:
            print(json.dumps(selected))
    except (ValueError, FileExistsError) as exc:
        p.error(str(exc))
