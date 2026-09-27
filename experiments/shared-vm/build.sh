#!/usr/bin/env bash
# Build the shared-VM experiment image: the Debian trixie profile plus the
# experiment layer, inside a disposable Lima builder. Never installs host packages.
# The builder steps mirror scripts/build-image.sh; keep them in sync.
set -euo pipefail
cd "$(dirname "$0")/../.."
root="$PWD"
work="$root/build/shared-vm"
limactl="${NSL_LIMACTL:-limactl}"
# Lima socket paths must stay under 108 bytes, so the builder lives outside the checkout.
export LIMA_HOME="${NSL_SHARED_VM_LIMA_HOME:-$HOME/.local/share/nsl-shared-vm-build}"
builder=nsl-shared-vm-builder
recipes=$(sed -n 's/^recipes=//p' scripts/build-image.sh)
mkosi=$(sed -n 's/^mkosi=//p' scripts/build-image.sh)
[[ $recipes =~ ^[0-9a-f]{40}$ && $mkosi =~ ^[0-9a-f]{40}$ ]] || { echo 'Cannot read source pins.' >&2; exit 1; }
output=nsl-shared-vm-trixie-x86-64-v1
mkdir -p "$work/src" "$work/share" "$work/evidence"
exec 9>"$work/builder.lock"
flock -n 9 || { echo 'Shared-VM builder is already in use.' >&2; exit 1; }
for suffix in raw manifest json; do
  path="$work/share/$output.$suffix"
  [[ ! -e $path && ! -L $path ]] || { echo "Artifact exists: $path; refusing to overwrite." >&2; exit 1; }
done
for item in mkosi-definitions mkosi; do
  if [[ $item == mkosi-definitions ]]; then url=https://github.com/nspawn/mkosi-definitions.git; revision=$recipes
  else url=https://github.com/systemd/mkosi.git; revision=$mkosi; fi
  if [[ ! -d $work/src/$item ]]; then
    git clone "$url" "$work/src/$item"
    git -C "$work/src/$item" checkout --detach "$revision"
  fi
  [[ $(git -C "$work/src/$item" rev-parse HEAD) == "$revision" ]] || { echo "Unexpected revision: $item" >&2; exit 1; }
done
staging=$(mktemp -d "$work/share/integration.XXXXXX")
integration="$staging/tree"
python3 scripts/compose-image.py --distribution debian --release trixie --architecture x86-64 \
  --destination "$integration" --recipes "$recipes" --mkosi "$mkosi" >/dev/null
# Apply the experiment layer and rename the output so it cannot pass for a profile image.
python3 - "$integration" "$root/experiments/shared-vm/layer" "$output" <<'PY'
import hashlib, json, shutil, sys
from pathlib import Path
tree, layer, output = Path(sys.argv[1]), Path(sys.argv[2]), sys.argv[3]
shutil.copytree(layer/'overlay', tree/'overlay', dirs_exist_ok=True, symlinks=True)
config = (tree/'mkosi.local.conf').read_text()
lines = [l for l in config.splitlines() if not l.startswith(('Output=', 'ImageId='))]
config = '\n'.join(lines) + '\n' + (layer/'mkosi.conf').read_text()
config += f'\n[Output]\nOutput={output}\nImageId=nsl-shared-vm\n'
(tree/'mkosi.local.conf').write_text(config)
digest = hashlib.sha256()
for path in sorted(p for p in layer.rglob('*') if p.is_file()):
    digest.update(f'{path.relative_to(layer)} {path.stat().st_mode & 0o7777:o} '.encode())
    digest.update(hashlib.sha256(path.read_bytes()).hexdigest().encode() + b'\n')
descriptor_path = tree/'overlay/usr/lib/nsl/image.json'
descriptor = json.loads(descriptor_path.read_text())
descriptor.update(build_id=output, experiment='shared-vm', experiment_layer_sha256=digest.hexdigest())
descriptor_path.write_text(json.dumps(descriptor, indent=2) + '\n')
PY
source_tree=$(mktemp -d "$work/share/source.XXXXXX")
for item in mkosi mkosi-definitions; do
  mkdir "$source_tree/$item"
  git -C "$work/src/$item" archive HEAD | tar -x -C "$source_tree/$item"
done
tar -C "$source_tree" -cf "$integration/sources.tar" mkosi mkosi-definitions
if [[ ! -f $work/builder-owned ]]; then
  [[ ! -e $LIMA_HOME/$builder ]] || { echo 'Refusing an existing unowned builder.' >&2; exit 1; }
  (umask 077; printf '%s\n' "$(id -u)" > "$work/builder-owned")
fi
[[ ! -L $work/builder-owned && -O $work/builder-owned && $(cat "$work/builder-owned") == "$(id -u)" ]] || { echo 'Invalid builder ownership.' >&2; exit 1; }
python3 - "$work" <<'PY'
import json,os,sys
from pathlib import Path
work=Path(sys.argv[1])
config=dict(vmType='qemu',arch='x86_64',cpus=4,memory='4GiB',disk='64GiB',
 images=[dict(location='https://cloud.debian.org/images/cloud/trixie/20260914-2601/debian-13-generic-amd64-20260914-2601.qcow2',arch='x86_64',digest='sha512:a733e7d49442a03e70d03e4eb5aaf3967f3efc69ef70952f9bb10fc1ee2c4876eb95956b5ad2d31350e5fada768feb651352535fb8cd1233f61998a5a7d2e93c')],
 mountType='virtiofs',mounts=[dict(location=str(work/'share'),mountPoint='/work',writable=True)],
 user=dict(name='nsl',uid=os.getuid(),home='/home/nsl',shell='/bin/bash'),
 containerd=dict(system=False,user=False),ssh=dict(forwardAgent=False,forwardX11=False,loadDotSSHPubKeys=False),
 portForwards=[dict(guestPortRange=[1,65535],guestIP='0.0.0.0',guestIPMustBeZero=False,proto='any',ignore=True)])
(work/'builder.json').write_text(json.dumps(config,indent=2)+'\n')
PY
if [[ ! -d $LIMA_HOME/$builder ]]; then
  "$limactl" --tty=false create --name="$builder" "$work/builder.json"
fi
trap '"$limactl" --tty=false stop "$builder" >&2 || true' EXIT
"$limactl" --tty=false start --timeout=8m "$builder"
"$limactl" shell --workdir / "$builder" sudo -n sh -c '
set -eu
apt-get update
DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends python3 git systemd-container systemd-ukify systemd-repart systemd-boot bubblewrap dosfstools mtools fdisk uidmap zstd cpio btrfs-progs e2fsprogs debootstrap openssl kmod
build_dir=$(mktemp -d /root/nsl-build.XXXXXX)
tar -xf "$1/sources.tar" -C "$build_dir"
cp -a "$1/." "$build_dir/mkosi-definitions/"
cd "$build_dir/mkosi-definitions"
PYTHONPATH="$build_dir/mkosi" python3 -m mkosi --profile=disk -d debian -r trixie --architecture x86-64 build
cp --sparse=always "mkosi.output/$2.raw" "$1/$2.raw"
cp "mkosi.output/$2.manifest" "$1/$2.manifest"
cp overlay/usr/lib/nsl/image.json "$1/$2.json"
chown nsl:nsl "$1/$2.raw" "$1/$2.manifest" "$1/$2.json"
rm -rf "$build_dir"
' sh "/work/$(basename "$staging")/tree" "$output"
for suffix in raw manifest json; do
  ln "$integration/$output.$suffix" "$work/share/$output.$suffix"
done
sha256sum "$work/share/$output.raw" > "$work/evidence/$output.sha256"
printf 'Built %s\n' "$work/share/$output.raw"
