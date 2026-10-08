#!/usr/bin/env bash
# Build an nsl image inside a disposable Lima guest. Never install host packages.
set -euo pipefail
cd "$(dirname "$0")/.."
root="$PWD"
work="$root/build/image"
limactl="${NSL_LIMACTL:-limactl}"
# Lima socket paths must stay under 108 bytes, so the builder lives outside the checkout.
export LIMA_HOME="${NSL_IMAGE_LIMA_HOME:-${XDG_DATA_HOME:-$HOME/.local/share}/nsl-image-build}"
builder=nsl-image-builder
recipes=68263d05169784f44168ca65241d989865ed011b
mkosi=4736cd836108a97772142c461c49f1ddb4172348
# Validate the explicit support matrix before touching builder state.
selection=()
while (($#)); do
  case "$1" in
    --role|--distribution|--release)
      (($# >= 2)) || { echo "Missing value for $1" >&2; exit 2; }
      selection+=("$1" "$2"); shift 2 ;;
    --help|-h)
      echo 'Usage: scripts/build-image.sh --role vm | --role machine --distribution debian|ubuntu|fedora|centos|arch|opensuse|azure [--release RELEASE]'
      exit 0 ;;
    *) echo "Unknown option: $1" >&2; exit 2 ;;
  esac
done
profile=$(python3 scripts/compose-image.py "${selection[@]}")
field() { python3 -c 'import json,sys; print(json.loads(sys.argv[1])[sys.argv[2]])' "$profile" "$1"; }
role=$(field role)
distribution=$(field distribution)
release=$(field release)
architecture=$(field architecture)
if [[ $role == vm ]]; then
  output="nsl-vm-$release-$architecture-r$(field revision)"
  payload=raw
else
  output="nsl-machine-$distribution-$release-$architecture-r$(field revision)"
  payload=tar.zst
fi
mkdir -p "$work/src" "$work/share" "$work/evidence" "$LIMA_HOME"
exec 9>"$work/builder.lock"
flock -n 9 || { echo 'Image builder is already in use.' >&2; exit 1; }
for suffix in $payload manifest json; do
  path="$work/share/$output.$suffix"
  [[ ! -e $path && ! -L $path ]] || { echo "Artifact exists: $path; refusing to overwrite." >&2; exit 1; }
done
[[ $role != vm ]] || make -s agent
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
python3 scripts/compose-image.py "${selection[@]}" --destination "$integration" \
  --recipes "$recipes" --mkosi "$mkosi" --agent build/nsl-agent >/dev/null
# Export committed source only; previous outputs or dirty checkout files cannot enter it.
source_tree=$(mktemp -d "$work/share/source.XXXXXX")
for item in mkosi mkosi-definitions; do
  mkdir "$source_tree/$item"
  git -C "$work/src/$item" archive HEAD | tar -x -C "$source_tree/$item"
done
tar -C "$source_tree" -cf "$integration/sources.tar" mkosi mkosi-definitions
owned="$LIMA_HOME/nsl-builder-owned"
if [[ ! -f $owned ]]; then
  [[ ! -e $LIMA_HOME/$builder ]] || { echo 'Refusing an existing unowned builder.' >&2; exit 1; }
  (umask 077; printf '%s\n' "$(id -u)" > "$owned")
fi
[[ ! -L $owned && -O $owned && $(cat "$owned") == "$(id -u)" ]] || { echo 'Invalid builder ownership.' >&2; exit 1; }
python3 - "$work" "$LIMA_HOME" <<'PY'
import json,os,sys
from pathlib import Path
work, home = Path(sys.argv[1]), Path(sys.argv[2])
config=dict(vmType='qemu',arch='x86_64',cpus=4,memory='4GiB',disk='64GiB',
 images=[dict(location='https://cloud.debian.org/images/cloud/trixie/20260914-2601/debian-13-generic-amd64-20260914-2601.qcow2',arch='x86_64',digest='sha512:a733e7d49442a03e70d03e4eb5aaf3967f3efc69ef70952f9bb10fc1ee2c4876eb95956b5ad2d31350e5fada768feb651352535fb8cd1233f61998a5a7d2e93c')],
 mountType='virtiofs',mounts=[dict(location=str(work/'share'),mountPoint='/work',writable=True)],
 user=dict(name='nsl',uid=os.getuid(),home='/home/nsl',shell='/bin/bash'),
 containerd=dict(system=False,user=False),ssh=dict(forwardAgent=False,forwardX11=False,loadDotSSHPubKeys=False),
 portForwards=[dict(guestPortRange=[1,65535],guestIP='0.0.0.0',guestIPMustBeZero=False,proto='any',ignore=True)])
(home/'builder.json').write_text(json.dumps(config,indent=2)+'\n')
PY
if [[ ! -d $LIMA_HOME/$builder ]]; then
  "$limactl" --tty=false create --name="$builder" "$LIMA_HOME/builder.json"
fi
# The builder's mount is fixed at creation; another checkout needs its own builder.
grep -qF "\"$work/share\"" "$LIMA_HOME/$builder/lima.yaml" || {
  echo "The builder in $LIMA_HOME mounts another checkout; set NSL_IMAGE_LIMA_HOME for this one." >&2
  exit 1
}
trap '"$limactl" --tty=false stop "$builder" >&2 || true' EXIT
"$limactl" --tty=false start --timeout=8m "$builder"
"$limactl" shell --workdir / "$builder" sudo -n sh -c '
set -eu
apt-get update
DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends python3 git systemd-container systemd-ukify systemd-repart systemd-boot bubblewrap dosfstools mtools fdisk uidmap zstd cpio btrfs-progs e2fsprogs debootstrap openssl kmod dnf rpm policycoreutils
build_dir=$(mktemp -d /root/nsl-build.XXXXXX)
tar -xf "$1/sources.tar" -C "$build_dir"
cp -a "$1/." "$build_dir/mkosi-definitions/"
cd "$build_dir/mkosi-definitions"
# Keep downloads and tools trees across builds; outputs stay per build.
mkdir -p /var/cache/nsl-mkosi
if [ "$6" = vm ]; then
  PYTHONPATH="$build_dir/mkosi" python3 -m mkosi --cache-directory=/var/cache/nsl-mkosi --profile=disk -d "$2" -r "$3" --architecture "$4" build
  cp --sparse=always "mkosi.output/$5.raw" "$1/$5.raw"
  systemd-dissect --copy-from "mkosi.output/$5.raw" /usr/lib/nsl/image.json "$1/$5.json"
else
  PYTHONPATH="$build_dir/mkosi" python3 -m mkosi --cache-directory=/var/cache/nsl-mkosi -d "$2" -r "$3" --architecture "$4" build
  cp "mkosi.output/$5.tar.zst" "$1/$5.tar.zst"
  tar --zstd -xOf "mkosi.output/$5.tar.zst" ./usr/lib/nsl/machine.json > "$1/$5.json"
fi
# Publish the payload, the package manifest and the descriptor as built.
cp "mkosi.output/$5.manifest" "$1/$5.manifest"
chown nsl:nsl "$1/$5".*
rm -rf "$build_dir"
' sh "/work/$(basename "$staging")/tree" "$distribution" "$release" "$architecture" "$output" "$role"
# Hard links publish complete artifacts without replacing another build.
for suffix in $payload manifest json; do
  ln "$integration/$output.$suffix" "$work/share/$output.$suffix"
done
rm -rf "$staging" "$source_tree"
sha256sum "$work/share/$output.$payload" > "$work/evidence/$output.sha256"
printf 'Built %s\n' "$work/share/$output.$payload"
