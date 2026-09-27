#!/usr/bin/env bash
# Build the nsl image inside a disposable Lima guest. Never install host packages.
set -euo pipefail
cd "$(dirname "$0")/.."
root="$PWD"
work="$root/build/image"
limactl="${NSL_LIMACTL:-limactl}"
export LIMA_HOME="$work/lima"
recipes=68263d05169784f44168ca65241d989865ed011b
mkosi=4736cd836108a97772142c461c49f1ddb4172348
# Validate the explicit support matrix before touching builder state.
selection=()
while (($#)); do
  case "$1" in
    --distribution|--release|--architecture)
      (($# >= 2)) || { echo "Missing value for $1" >&2; exit 2; }
      selection+=("$1" "$2"); shift 2 ;;
    --help|-h)
      echo 'Usage: scripts/build-image.sh [--distribution debian|ubuntu] [--release trixie|noble] [--architecture x86-64]'
      exit 0 ;;
    *) echo "Unknown option: $1" >&2; exit 2 ;;
  esac
done
profile=$(python3 scripts/compose-image.py "${selection[@]}")
distribution=$(python3 -c 'import json,sys; print(json.loads(sys.argv[1])["distribution"])' "$profile")
release=$(python3 -c 'import json,sys; print(json.loads(sys.argv[1])["release"])' "$profile")
architecture=$(python3 -c 'import json,sys; print(json.loads(sys.argv[1])["architecture"])' "$profile")
revision=$(python3 -c 'import json,sys; print(json.loads(sys.argv[1])["revision"])' "$profile")
output="nsl-$distribution-$release-$architecture-v$revision.raw"
mkdir -p "$work/src" "$work/share" "$work/evidence"
exec 9>"$work/builder.lock"
flock -n 9 || { echo 'Image builder is already in use.' >&2; exit 1; }
for suffix in raw manifest json; do
  path="$work/share/${output%.raw}.$suffix"
  [[ ! -e $path && ! -L $path ]] || { echo "Artifact exists: $path; refusing to overwrite." >&2; exit 1; }
done
for item in mkosi-definitions mkosi; do
  if [[ $item == mkosi-definitions ]]; then
    url=https://github.com/nspawn/mkosi-definitions.git
    source_revision=$recipes
  else
    url=https://github.com/systemd/mkosi.git
    source_revision=$mkosi
  fi
  if [[ ! -d $work/src/$item ]]; then
    git clone "$url" "$work/src/$item"
    git -C "$work/src/$item" checkout --detach "$source_revision"
  fi
  [[ $(git -C "$work/src/$item" rev-parse HEAD) == "$source_revision" ]] || { echo "Unexpected revision: $item" >&2; exit 1; }
done
staging=$(mktemp -d "$work/share/integration.XXXXXX")
integration="$staging/tree"
python3 scripts/compose-image.py --distribution "$distribution" --release "$release" \
  --architecture "$architecture" --destination "$integration" --recipes "$recipes" --mkosi "$mkosi" >/dev/null
# Export committed source only; previous outputs or dirty checkout files cannot enter it.
source_tree=$(mktemp -d "$work/share/source.XXXXXX")
for item in mkosi mkosi-definitions; do
  mkdir "$source_tree/$item"
  git -C "$work/src/$item" archive HEAD | tar -x -C "$source_tree/$item"
done
tar -C "$source_tree" -cf "$integration/sources.tar" mkosi mkosi-definitions
if [[ ! -f $work/builder-owned ]]; then
  [[ ! -e $LIMA_HOME/nsl-image-builder ]] || { echo 'Refusing an existing unowned builder.' >&2; exit 1; }
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
if [[ ! -d $LIMA_HOME/nsl-image-builder ]]; then
  "$limactl" --tty=false create --name=nsl-image-builder "$work/builder.json"
fi
trap '"$limactl" --tty=false stop nsl-image-builder >&2 || true' EXIT
"$limactl" --tty=false start --timeout=8m nsl-image-builder
"$limactl" shell --workdir / nsl-image-builder sudo -n sh -c '
set -eu
apt-get update
DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends python3 git systemd-container systemd-ukify systemd-repart systemd-boot bubblewrap dosfstools mtools fdisk uidmap zstd cpio btrfs-progs e2fsprogs ubuntu-keyring debootstrap openssl kmod
build_dir=$(mktemp -d /root/nsl-build.XXXXXX)
tar -xf "$1/sources.tar" -C "$build_dir"
cp -a "$1/." "$build_dir/mkosi-definitions/"
cd "$build_dir/mkosi-definitions"
PYTHONPATH="$build_dir/mkosi" python3 -m mkosi --profile=disk -d "$2" -r "$3" --architecture "$4" build
# Publish raw disk plus mkosi package manifest and nsl build descriptor.
cp --sparse=always "mkosi.output/$5.raw" "$1/$5.raw"
cp "mkosi.output/$5.manifest" "$1/$5.manifest"
cp overlay/usr/lib/nsl/image.json "$1/$5.json"
chown nsl:nsl "$1/$5.raw" "$1/$5.manifest" "$1/$5.json"
rm -rf "$build_dir"
' sh "/work/$(basename "$staging")/tree" "$distribution" "$release" "$architecture" "${output%.raw}"
# Hard links publish complete artifacts without replacing another build.
for suffix in raw manifest json; do
  ln "$integration/${output%.raw}.$suffix" "$work/share/${output%.raw}.$suffix"
done
sha256sum "$work/share/$output" > "$work/evidence/${output%.raw}.sha256"
printf 'Built %s\n' "$work/share/$output"
