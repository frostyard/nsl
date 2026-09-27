#!/usr/bin/env python3
"""Assemble a supported image profile without running privileged build tools."""
import argparse
import hashlib
import json
from pathlib import Path
import shutil


PROFILES = {
    ('debian', 'trixie'): 'debian',
    ('ubuntu', 'noble'): 'ubuntu',
    ('fedora', '44'): 'fedora',
    ('centos', '10'): 'centos',
    ('opensuse', '16.0'): 'opensuse-leap',
    ('opensuse', 'tumbleweed'): 'opensuse-tumbleweed',
}


def profile_directory(root, distribution, release):
    # CLI/guest strings are lookup keys, never path components.
    try:
        return root/'image/profiles'/PROFILES[distribution, release]
    except KeyError:
        raise ValueError(f'unsupported image profile: {distribution}:{release}') from None


def select(root, distribution, release=None, architecture='x86-64'):
    if release is None:
        release = next((r for d, r in PROFILES if d == distribution), None)
    directory = profile_directory(root, distribution, release)
    profile = json.loads((directory/'profile.json').read_text())
    if architecture != profile['architecture']:
        raise ValueError(f'unsupported image architecture: {architecture}')
    return profile


def compose(root, destination, profile, recipes, mkosi):
    destination.mkdir()  # Never merge with an existing build input tree.
    layers = [root/'image/common', root/'image/families'/profile['family'],
              profile_directory(root, profile['distribution'], profile['release'])]
    config = []
    for layer in layers:
        for item in layer.iterdir():
            if item.name == 'mkosi.conf':
                config.append(item.read_text())
            elif item.name != 'profile.json':
                if item.is_dir():
                    shutil.copytree(item, destination/item.name, dirs_exist_ok=True, symlinks=True,
                                    ignore=shutil.ignore_patterns('__pycache__', '*.pyc'))
                else:
                    shutil.copy2(item, destination/item.name, follow_symlinks=False)
    name = 'nsl-{distribution}-{release}-{architecture}-v{revision}'.format(**profile)
    config.append(f'[Output]\nOutput={name}\nImageId=nsl-{profile["distribution"]}\n')
    (destination/'mkosi.local.conf').write_text('\n'.join(config))
    libexec = destination/'overlay/usr/local/libexec'
    libexec.mkdir(parents=True, exist_ok=True)
    for source, target in [('exec.py', 'nsl-exec'), ('setup.py', 'nsl-setup')]:
        shutil.copy2(root/'guest'/source, libexec/target)
    digest = hashlib.sha256()
    for base in [root/'image', root/'guest']:
        for path in sorted(base.rglob('*')):
            if '__pycache__' in path.parts or (not path.is_file() and not path.is_symlink()):
                continue
            digest.update(str(path.relative_to(root)).encode()+b'\0')
            digest.update(path.readlink().as_posix().encode() if path.is_symlink() else path.read_bytes())
            digest.update(b'\0')
    descriptor = dict(schema=1, build_id=name, **profile, protocol_min=1, protocol_max=1,
                      transport='nsl-vsock-ssh', integration_sha256=digest.hexdigest(),
                      recipes_revision=recipes, mkosi_revision=mkosi)
    target = destination/'overlay/usr/lib/nsl/image.json'
    target.parent.mkdir(parents=True, exist_ok=True)
    target.write_text(json.dumps(descriptor, indent=2)+'\n')
    return name


if __name__ == '__main__':
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument('--distribution', choices=sorted({d for d, _ in PROFILES}), default='debian')
    p.add_argument('--release')
    p.add_argument('--architecture', default='x86-64')
    p.add_argument('--destination', type=Path)
    p.add_argument('--recipes', default='')
    p.add_argument('--mkosi', default='')
    a = p.parse_args()
    root = Path(__file__).resolve().parents[1]
    try:
        selected = select(root, a.distribution, a.release, a.architecture)
        if a.destination:
            print(compose(root, a.destination, selected, a.recipes, a.mkosi))
        else:
            print(json.dumps(selected))
    except (ValueError, FileExistsError) as exc:
        p.error(str(exc))
