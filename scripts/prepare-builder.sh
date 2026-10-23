#!/bin/sh
# Prepare the persistent Lima image builder before a build: purge kernels other
# than the running and the newest, confirm the EFI system partition can hold the
# newest kernel that systemd-boot copies there, then finish pending configuration.
set -eu

NSL_BOOT=${NSL_BOOT:-/boot}
NSL_ESP=${NSL_ESP:-/boot/efi}

running="linux-image-$(uname -r)"
# The newest kernel is the one the metapackage depends on; it is empty when the
# builder never installed the metapackage.
newest=
for word in $(dpkg-query -W -f='${Depends}' linux-image-amd64 2>/dev/null || true); do
  newest=$word
  break
done

if [ -z "$newest" ]; then
  # With no kernel known to be the newest, keeping only the running one could
  # purge the newest kernel.
  echo "linux-image-amd64 is not installed; keeping all builder kernels."
else
  # Purge with dpkg, not apt: apt configures the pending kernel first, and that
  # needs the free space this step makes.
  packages=$(dpkg-query -W -f='${db:Status-Abbrev} ${Package}\n' 'linux-image-[0-9]*' 2>/dev/null || true)
  while read -r status package; do
    [ -n "$package" ] || continue
    # The second status character is "n" when dpkg does not have the package.
    case "$status" in
      ?n*) continue ;;
    esac
    [ "$package" != "$running" ] || continue
    [ "$package" != "$newest" ] || continue
    echo "Purging old builder kernel $package"
    DEBIAN_FRONTEND=noninteractive dpkg --purge "$package" </dev/null
  done <<EOF
$packages
EOF

  # Check before configuration: a configured kernel is already copied to the ESP,
  # so only this order predicts whether the pending kernel fits.
  if [ -d "$NSL_ESP" ]; then
    version=${newest#linux-image-}
    need=0
    for file in "$NSL_BOOT/vmlinuz-$version" "$NSL_BOOT/initrd.img-$version"; do
      [ -e "$file" ] || continue
      kib=$(du -sk "$file" | cut -f1)
      need=$((need + kib))
    done
    have=$(df -Pk "$NSL_ESP" | awk 'NR == 2 { print $4 }')
    have=${have:-0}
    # systemd-boot replaces its existing copies, so the space they hold counts.
    for copy in "$NSL_ESP"/*/"$version"; do
      [ -d "$copy" ] || continue
      kib=$(du -sk "$copy" | cut -f1)
      have=$((have + kib))
    done
    need_mib=$(((need + 1023) / 1024))
    have_mib=$((have / 1024))
    if [ "$have" -lt "$need" ]; then
      df -h "$NSL_ESP" >&2 || true
      echo "Error: the image builder's EFI system partition is too full for the newest kernel ($newest): $need_mib MiB ($need KiB) is needed and $have_mib MiB ($have KiB) is available." >&2
      echo "Delete the builder with 'limactl delete nsl-image-builder' (with LIMA_HOME set to the builder's Lima home); the next build creates a new builder." >&2
      exit 1
    fi
    echo "The builder's EFI system partition has $have_mib MiB free; the newest kernel needs $need_mib MiB."
  fi
fi

if ! DEBIAN_FRONTEND=noninteractive dpkg --configure -a </dev/null; then
  df -h "$NSL_ESP" >&2 || true
  echo "Error: pending package configuration failed in image builder; its EFI system partition ($NSL_ESP) can be full." >&2
  echo "Delete the builder with 'limactl delete nsl-image-builder' (with LIMA_HOME set to the builder's Lima home); the next build creates a new builder." >&2
  exit 1
fi
