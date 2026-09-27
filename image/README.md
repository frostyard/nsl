# nsl guest images

Build bootable VMs from pinned [nspawn/mkosi-definitions](https://github.com/nspawn/mkosi-definitions) disk recipes, mkosi v27 and nsl integration. The guest runs applications directly; no nspawn runtime or nested container is required.

## Build

```sh
source build/poc/env.sh  # when using the locally downloaded Lima tool
scripts/build-image.sh --distribution debian --release trixie
scripts/build-image.sh --distribution ubuntu --release noble
scripts/build-image.sh --distribution fedora --release 44
scripts/build-image.sh --distribution centos --release 10
```

The default is Debian trixie. The combinations below have passed the full VM suite; `--architecture` currently accepts `x86-64`. Unsupported combinations fail before starting the builder. See [distribution acceptance](../docs/plans/image-profiles-and-ubuntu.md) for measured coverage.

| Profile | Release | Root filesystem | Output under `build/image/share/` |
| --- | --- | --- | --- |
| Debian | 13 / trixie | btrfs | `nsl-debian-trixie-x86-64-v6.raw` |
| Ubuntu | 24.04 LTS / noble | ext4 | `nsl-ubuntu-noble-x86-64-v6.raw` |
| Fedora | 44 | btrfs | `nsl-fedora-44-x86-64-v4.raw` |
| CentOS Stream | 10 | ext4 | `nsl-centos-10-x86-64-v3.raw` |

[Fedora/CentOS evidence](../docs/plans/rpm-guests.md) includes SELinux enforcing, kernel maintenance and rootless containers. openSUSE 16.0 and the pinned Tumbleweed 20260923 snapshot have experimental profiles under active validation; their availability in the builder is not a support claim.

The script requires Lima 2.2.0, Python 3, Git, `flock` and the host VM prerequisites. Build packages stay inside an owned Debian builder with 4 CPUs, 4 GiB RAM and a 64 GiB sparse disk. It shares only `build/image/share`, serializes builds with a lock, removes successful guest build workspaces, and stops on exit. Failed workspaces remain for diagnosis.

Images are published without replacing existing files. Each raw disk has a sibling `.manifest` with package versions and `.json` with nsl build identity, selected profile, protocol range, source pins and an integration source checksum. The raw SHA256 is written to `build/image/evidence/OUTPUT.sha256`. Move retained outputs before rebuilding. Live package repositories mean builds are not bit-for-bit reproducible; checksums and manifests do not authenticate publishers. Signed catalogue delivery is future work.

## Planned prebuilt image delivery

The [image delivery plan](../docs/plans/image-distribution.md) selects public GHCR OCI artifacts containing a compressed raw disk, image metadata, package inventory and provenance. A signed catalogue will map distro selections to tested immutable digests. nsl will verify the publishing workflow identity and content, resume interrupted downloads, cache verified bases and create independent writable VMs. Ordinary users will not need Lima or mkosi for this path.

Publication, catalogue selection and client signature verification are planned. Current sibling manifests describe local builds and do not authenticate them. New bases will affect new environments; existing guests retain their disks and use their distro package manager for ordinary updates. [ADR-0012](../docs/adr/0012-signed-image-distribution.md), [guest contract](../docs/specs/guest-images.md).

## Integration layers

`scripts/compose-image.py` assembles these layers in order:

1. `common/`: account/command helpers, network setup, SSH authentication policy, vsock transport, root growth and EFI layout.
2. `families/FAMILY/`: initramfs-tools for Debian/Ubuntu or dracut and SELinux labels for RPM guests, plus UKI layout and the platform hook that records the root UUID.
3. `profiles/PROFILE/`: explicit release/architecture/build revision, packages, optional filesystem overrides and maintenance commands.

The composer rejects an existing destination. Root-free tests exercise profile validation, composition and no-overwrite behavior. The RPM family supplies dracut/UKI setup, first-boot labels and a Fedora 44 tools tree; it must not require changes to host lifecycle or storage code. [ADR-0011](../docs/adr/0011-image-profiles-and-portable-vsock.md), [distribution plan](../docs/plans/distribution-support.md).

## Guest contract and transport

Generic images contain no client private key, fixed development account or guest SSH host keys. `nsl-setup` validates the boot credential and binds the disk to the environment ID, selected UID/primary GID and public key. It generates missing host keys and preserves them on subsequent boots. The family platform hook runs during setup.

All profiles use `nsl-ssh.socket` on vsock port 22 and `nsl-ssh@.service` with OpenSSH's inetd mode. Connections require successful setup and root growth. The systemd SSH generator is masked to prevent duplicate listeners; distro SSH units remain available for guest administration. This works with Ubuntu's older systemd without replacing systemd. AppArmor remains enabled on Ubuntu; SELinux remains enforcing on Fedora and CentOS. A common systemd preset keeps nsl units enabled after distro presets run.

`/usr/lib/nsl/image.json` carries build identity and the declared protocol range. It survives backup/restore as guest disk content. Protocol 1 still performs readiness/authentication; the host does not yet negotiate the descriptor's optional capabilities. [Guest contract](../docs/specs/guest-images.md).

## Boot layout and maintenance

The 1 GiB EFI partition mounts at `/efi`; `/boot` stays on the root filesystem so package managers can replace kernel files using hard links. Debian-family kernel hooks use initramfs-tools and systemd-ukify; RPM profiles use dracut and systemd-ukify. The platform hook preserves an administrator's existing `/etc/kernel/cmdline`; otherwise it records the root UUID. The initramfs includes the virtio-vsock driver.

Setup explicitly requires `systemd-growfs-root.service` after repartitioning/remounting. Ubuntu explicitly includes `udev` for device discovery, plus ext4 tools. These details belong in image profiles, while the host only changes virtual capacity.

Normal package/kernel updates use the guest package manager. New bases affect new VMs; updating the CLI never replaces a customized guest root. `scripts/probe-maintenance.py` selects command arrays from the image's distro profile, and `scripts/probe-distribution.py` runs common lifecycle/storage checks. A newer-kernel upgrade and an ordinary reinstall are recorded separately.

## Planned cloud-init support

Optional creation-time cloud-init will use a local NoCloud seed on images advertising a tested provisioning capability. Image profiles will own cloud-init installation, schema/module support and boot ordering; nsl will keep control of its management account, SSH identity, networking and root growth. User configuration runs as guest root, with separate status/wait/log commands and preserved provisioning identity on restore. The feature is planned, starting with Debian and Ubuntu. [Interface](../docs/specs/provisioning.md), [implementation plan](../docs/plans/cloud-init-provisioning.md), [ADR-0013](../docs/adr/0013-optional-cloud-init-provisioning.md).

## Earlier experimental images

- v3 placed `/boot` on FAT and failed kernel replacement. The historical VMs were deleted with user authorization.
- v4 fixed the boot layout, but maintained guests could skip root filesystem growth after kernel regeneration.
- v5 explicitly required root growth. v6 retains that fix and introduces profiles and the portable SSH socket.

Existing guests retain their installed integration. Historical backups remain useful for reproducing failures; there is no automatic guest migration. See [boot layout decision](../docs/adr/0007-maintainable-guest-boot.md), [root growth decision](../docs/adr/0010-explicit-guest-root-growth.md) and [storage results](../docs/plans/storage-management.md).
