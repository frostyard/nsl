#!/usr/bin/env bash
# Reproduce the image experiment inside a disposable Lima VM.
# Runtime dependencies stay on the host; image-building packages stay in the guest.
set -euo pipefail
cd "$(dirname "$0")/../.."
[[ $(id -u) == 1000 ]] || { echo 'This experiment profile currently assumes host UID 1000.' >&2; exit 1; }
root="$PWD"
cli="${NSL_LIMA_BASELINE:-$root/build/nsl-lima}"
[[ -x $cli ]] || { echo "Historical experiment requires the saved Lima prototype; use scripts/build-image.sh for the current image." >&2; exit 1; }
work="$root/build/vmspawn"
export NSL_HOME="${NSL_BUILDER_HOME:-$HOME/.local/share/nsl-vmspawn-build}"
export NSL_LIMACTL="${NSL_LIMACTL:-$root/build/poc/tools/bin/limactl}"
recipes=68263d05169784f44168ca65241d989865ed011b
mkosi=4736cd836108a97772142c461c49f1ddb4172348
mkdir -p "$work/src" "$work/share" "$work/evidence"
for item in mkosi-definitions mkosi; do
  if [[ $item == mkosi-definitions ]]; then
    url=https://github.com/nspawn/mkosi-definitions.git
    revision=$recipes
  else
    url=https://github.com/systemd/mkosi.git
    revision=$mkosi
  fi
  if [[ ! -d $work/src/$item ]]; then
    git clone "$url" "$work/src/$item"
    git -C "$work/src/$item" checkout --detach "$revision"
  fi
  [[ $(git -C "$work/src/$item" rev-parse HEAD) == "$revision" ]] || {
    echo "Unexpected source revision in $item" >&2; exit 1;
  }
done
# Never replace an existing key or built image.
if [[ ! -f $work/identity/id_ed25519 ]]; then
  (umask 077; mkdir -p "$work/identity"; ssh-keygen -q -t ed25519 -N '' -C nsl-vmspawn-experiment -f "$work/identity/id_ed25519")
fi
[[ ! -e $work/share/nsl-debian-vm-v3.raw ]] || { echo 'Built image already exists; refusing to overwrite.' >&2; exit 1; }
integration=$(mktemp -d "$work/share/integration.XXXXXX")
cp -a experiments/vmspawn/. "$integration"
mkdir -p "$integration/overlay/usr/local/libexec"
cp guest/exec.py "$integration/overlay/usr/local/libexec/nsl-exec"
cp "$work/identity/id_ed25519.pub" "$integration/overlay/usr/local/libexec/nsl-authorized-key"
tar -C "$work/src" -cf "$work/share/sources.tar" mkosi mkosi-definitions
if [[ ! -f $NSL_HOME/environments/builder/environment.json ]]; then
  "$cli" create builder --project "$work/share" --cpus 4 --memory 4
fi
"$cli" exec builder --root -- sh -c '
set -eu
apt-get update
DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends python3 git systemd-container systemd-ukify systemd-repart systemd-boot bubblewrap dosfstools mtools fdisk uidmap zstd cpio btrfs-progs debootstrap openssl kmod
build_dir=$(mktemp -d /root/nsl-build.XXXXXX)
tar -xf /work/sources.tar -C "$build_dir"
cp -a "$1/." "$build_dir/mkosi-definitions/"
cd "$build_dir/mkosi-definitions"
PYTHONPATH="$build_dir/mkosi" python3 -m mkosi --profile=disk -d debian -r trixie build
chmod a+r mkosi.output/nsl-debian-vm-v3.raw
cp --sparse=always mkosi.output/nsl-debian-vm-v3.raw /home/nsl/nsl-debian-vm-v3.raw
chown nsl:nsl /home/nsl/nsl-debian-vm-v3.raw
' sh "/work/$(basename "$integration")"
"$cli" exec builder -- cp --sparse=always /home/nsl/nsl-debian-vm-v3.raw /work/nsl-debian-vm-v3.raw
sha256sum "$work/share/nsl-debian-vm-v3.raw" > "$work/evidence/image.sha256"
echo "Built $work/share/nsl-debian-vm-v3.raw"
