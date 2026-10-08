#!/usr/bin/env python3
"""Assemble mkosi input for the nsl VM image or a machine image without running privileged build tools."""
import argparse
import hashlib
import json
from pathlib import Path
import shutil

# Must match internal/protocol; scripts/test_image_profiles.py checks both.
AGENT_PROTOCOL = 1
MACHINE_PROTOCOL = 1


# Machine profiles by distribution and release. CLI strings are lookup keys,
# never path components.
MACHINES = {
    ('debian', 'trixie'): 'debian',
    ('fedora', '44'): 'fedora',
    ('arch', 'rolling'): 'arch',
    ('opensuse', 'tumbleweed'): 'opensuse-tumbleweed',
    ('ubuntu', 'resolute'): 'ubuntu',
    ('centos', '10'): 'centos',
    ('opensuse', '16.0'): 'opensuse-leap',
    ('azure', '4.0'): 'azurelinux',
}


def profile_directory(root, profile):
    if profile['role'] == 'vm':
        return root/'image/vm'
    return root/'image/machines/profiles'/MACHINES[profile['distribution'], profile['release']]


def select(root, role, distribution=None, release=None, architecture='x86-64'):
    if role == 'vm':
        if distribution is not None or release is not None:
            raise ValueError('the VM image takes no --distribution or --release')
        profile = json.loads((root/'image/vm/profile.json').read_text())
    elif role == 'machine':
        if release is None:
            release = next((r for d, r in MACHINES if d == distribution), None)
        if (distribution, release) not in MACHINES:
            raise ValueError(f'unsupported machine image: {distribution}:{release}')
        profile = json.loads((root/'image/machines/profiles'/MACHINES[distribution, release]/'profile.json').read_text())
    else:
        raise ValueError(f'unsupported image role: {role}')
    if architecture != profile['architecture']:
        raise ValueError(f'unsupported image architecture: {architecture}')
    return dict(profile, role=role)


def output_name(profile):
    if profile['role'] == 'vm':
        return 'nsl-vm-{release}-{architecture}-r{revision}'.format(**profile)
    return 'nsl-machine-{distribution}-{release}-{architecture}-r{revision}'.format(**profile)


def normal_mode(path):
    """Checkouts differ in umask; images get 0755 or 0644 by the owner's execute bit."""
    return 0o755 if path.is_dir() or path.stat().st_mode & 0o100 else 0o644


def fingerprint(root, paths):
    """Hash exactly the selected inputs, including execute bits and link targets."""
    digest = hashlib.sha256()
    for path in sorted(paths):
        if '__pycache__' in path.parts or path.suffix == '.pyc' or not (path.is_file() or path.is_symlink()):
            continue
        record = dict(path=path.relative_to(root).as_posix() if path.is_relative_to(root) else path.name,
                      mode=0 if path.is_symlink() else normal_mode(path),
                      type='link' if path.is_symlink() else 'file',
                      content=(path.readlink().as_posix() if path.is_symlink()
                               else hashlib.sha256(path.read_bytes()).hexdigest()))
        digest.update(json.dumps(record, sort_keys=True, separators=(',', ':')).encode()+b'\n')
    return digest.hexdigest()


def compose(root, destination, profile, recipes, mkosi, agent=None):
    destination.mkdir()  # Never merge with an existing build input tree.
    if profile['role'] == 'vm':
        layers = [root/'image/vm']
    else:
        layers = [root/'image/machines/common', root/'image/machines/families'/profile['family'], profile_directory(root, profile)]
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
    for path in destination.rglob('*'):
        if not path.is_symlink():
            path.chmod(normal_mode(path))
    name = output_name(profile)
    inputs = [root/'scripts/compose-image.py', *(path for layer in layers for path in layer.rglob('*'))]
    nsl = destination/'overlay/usr/lib/nsl'
    nsl.mkdir(parents=True, exist_ok=True)
    common = dict(schema=1, role=profile['role'], build_id=name, distribution=profile['distribution'],
                  release=profile['release'], architecture=profile['architecture'])
    if profile['role'] == 'vm':
        config.append(f'[Output]\nOutput={name}\nImageId=nsl-vm\n')
        target = nsl/'nsl-agent'
        shutil.copyfile(agent, target)
        target.chmod(0o755)
        # The finalize script fills in what the build installed.
        descriptor = dict(common, revision=profile['revision'], agent_protocol=AGENT_PROTOCOL, machine_protocol=MACHINE_PROTOCOL,
                          transport='nsl-vsock-ssh', systemd='@SYSTEMD@', kernel='@KERNEL@',
                          integration_sha256=fingerprint(root, [*inputs, target]))
        path = nsl/'image.json'
    else:
        # A root filesystem, not a disk: one zstd tar the VM imports as a subvolume.
        config.append(f'[Output]\nOutput={name}\nImageId=nsl-machine-{profile["distribution"]}\nFormat=tar\nCompressOutput=zstd\n')
        descriptor = dict(common, family=profile['family'], revision=profile['revision'], machine_protocol=MACHINE_PROTOCOL,
                          os_id='@OS_ID@', os_version='@OS_VERSION@', systemd='@SYSTEMD@',
                          capabilities={'gui': {}, 'nesting': {}}, integration_sha256=fingerprint(root, inputs))
        path = nsl/'machine.json'
    (destination/'mkosi.local.conf').write_text('\n'.join(config))
    descriptor.update(recipes_revision=recipes, mkosi_revision=mkosi)
    path.write_text(json.dumps(descriptor, indent=2)+'\n')
    return name


if __name__ == '__main__':
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument('--role', required=True, choices=('vm', 'machine'))
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
            if selected['role'] == 'vm' and (not a.agent or not a.agent.is_file()):
                raise ValueError('the VM image needs --agent with a built nsl-agent')
            print(compose(root, a.destination, selected, a.recipes, a.mkosi, a.agent))
        else:
            print(json.dumps(selected))
    except (ValueError, FileExistsError) as exc:
        p.error(str(exc))
