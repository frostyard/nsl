#!/usr/bin/env bash
# Install pinned development tools into ignored build/poc; never alters the host OS.
set -euo pipefail
cd "$(dirname "$0")/.."
if [[ $(uname -s) != Linux || $(uname -m) != x86_64 ]]; then
  echo 'This proof of concept requires Linux x86_64.' >&2
  exit 1
fi
poc_dir="$PWD/build/poc"
mkdir -p "$poc_dir/downloads" "$poc_dir/tools"
archive="$poc_dir/downloads/lima-2.2.0-Linux-x86_64.tar.gz"
if [[ ! -f $archive ]]; then
  curl --fail --location --retry 3 --output "$archive.tmp" \
    https://github.com/lima-vm/lima/releases/download/v2.2.0/lima-2.2.0-Linux-x86_64.tar.gz
  mv "$archive.tmp" "$archive"
fi
printf '%s  %s\n' a0ea1ccf6b7335a900adb5f8d2b8384457965fecb1ba72f09b4e3e46d12f424a "$archive" | sha256sum --check -
tar -xzf "$archive" -C "$poc_dir/tools"
# Optional on Debian-derived hosts: download and extract the authenticated distro
# package. This does not install it; its shared-library dependencies must exist.
if [[ ${1:-} == --waypipe && ! -x $poc_dir/tools/usr/bin/waypipe ]]; then
  command -v apt-get >/dev/null
  command -v dpkg-deb >/dev/null
  waypipe_dir=$(mktemp -d "$poc_dir/downloads/waypipe.XXXXXX")
  (cd "$waypipe_dir" && apt-get download waypipe)
  for package in "$waypipe_dir"/*.deb; do
    dpkg-deb --extract "$package" "$poc_dir/tools"
  done
fi
make build
{
  printf 'export NSL_LIMACTL=%q\n' "$poc_dir/tools/bin/limactl"
  if [[ -x $poc_dir/tools/usr/bin/waypipe ]]; then
    printf 'export NSL_WAYPIPE=%q\n' "$poc_dir/tools/usr/bin/waypipe"
  fi
} > "$poc_dir/env.sh"
echo "Load tool paths: source $poc_dir/env.sh"
echo 'Then run build/nsl doctor. QEMU, virtiofsd, SSH and KVM access must already exist.'
