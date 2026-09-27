# Generic nsl Debian image

This overlay builds a bootable Debian image from pinned [nspawn/mkosi-definitions](https://github.com/nspawn/mkosi-definitions) recipes and mkosi v27. It adds the guest command/setup helpers, SSH, CA certificates, systemd-repart, kernel update tooling, sudo, Waypipe and a sample calculator. It contains no client key or development account. Per-environment boot credentials create the account and bind the disk to its identity.

From the repository root:

```sh
source build/poc/env.sh  # if using the locally downloaded Lima tool
scripts/build-image.sh
```

The script requires Lima 2.2.0, Python 3, Git and the host VM prerequisites. It installs all image-building packages inside a disposable Debian builder with 4 CPUs, 4 GiB RAM and a 64 GiB sparse disk. Builder state and downloads live under ignored `build/image`. The builder shares only `build/image/share` and is stopped when the script exits. Successful guest build workspaces are removed. Failed workspaces remain available for diagnosis.

The output is `build/image/share/nsl-debian-v4.raw`; its checksum is in `build/image/evidence/image.sha256`. Existing output images are never overwritten. Move an output you want to retain before rebuilding. Source revisions are pinned in the script; live Debian package repositories mean the build is not bit-for-bit reproducible. Image signing and a downloadable catalogue are not implemented.

Guest setup reads `nsl.config` through systemd credentials. It validates a protocol version, environment ID, UID/GID and public SSH key; it never receives a client private key. Subsequent boots must match the persisted identity. Debian supplies SSH host-key generation and the vsock listener. The root partition/filesystem grows using the included repart definition.

Related: [runtime design](../docs/design/lifecycle.md), [ADR-0005](../docs/adr/0005-vmspawn-and-nspawn-images.md), [implementation validation](../docs/plans/vmspawn-implementation.md). The earlier fixed-user/key experiment is retained under `experiments/vmspawn` as historical evaluation code.

## Guest updates and restored systems

The generic image is for fresh environments. Existing guests keep their own packages and disk across CLI changes; replacing the base image does not update them. Kernel/package maintenance uses the guest package manager. A versioned integration-update mechanism and signed image delivery remain planned. [Roadmap](../docs/plans/wsl2-equivalent.md).

Whole-system backups preserve the protocol-1 guest binding, client key and host trust. Restore supplies that same binding with a new host runtime identity; no image rebuild or cache is required. See [ADR-0006](../docs/adr/0006-stopped-vm-backups.md) and [backup/reliability validation](../docs/plans/backup-and-reliability.md).

## Boot layout (v4)

The 1 GiB EFI System Partition mounts at `/efi`; `/boot` remains on btrfs. Only firmware boot files are copied to the EFI partition. This lets Debian replace kernel package files using hard links. `systemd-ukify` and Debian's kernel/initramfs hooks regenerate unified kernel images (UKIs); first boot writes the root UUID to `/etc/kernel/cmdline` if that file is absent. Existing administrator configuration is preserved. The initramfs includes the virtio-vsock driver, and the CLI explicitly requests the SSH listener to avoid early-detection races.

The built v4 artifact has SHA256 `8b4d1658a3dfaca8796fba7c688c06719fa66a8bb347422452faac2cf7dcb90d`. Its guest credential protocol remains version 1. [ADR-0007](../docs/adr/0007-maintainable-guest-boot.md) explains the change; [maintenance results](../docs/plans/backup-and-reliability.md) distinguish kernel reinstallation from a newer-version upgrade.

**Earlier v3 guests need attention before kernel maintenance.** Their FAT `/boot` layout failed a real Debian kernel reinstall, which also removed the boot entry in the disposable test VM. Updating the host CLI or importing v4 does not repair an existing v3 disk. Keep a stopped backup; use a fresh v4 VM for maintenance tests until an explicit guest migration is implemented. The original v3 image and environments are retained for comparison.
