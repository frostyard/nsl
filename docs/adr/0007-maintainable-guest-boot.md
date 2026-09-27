# 0007 — Keep Debian package files off the EFI filesystem

- **Status:** Accepted; narrowed to the nsl VM image by [ADR-0017](0017-shared-vm-and-machine-images.md)
- **Date:** 2026-09-26

## Context

Testing a kernel reinstall in a restored v3 guest exposed a FAT `/boot` mount. Debian's package manager could not create backup hard links for kernel files, and the failed package operation removed the boot entry. The original source VM and archive remained intact. A customized development VM must support its distribution's package maintenance.

## Decision

Keep `/boot` on the guest's btrfs root. Mount the EFI System Partition at `/efi`, copying only bootloader/UKI data there during image creation. Include Debian's `systemd-ukify` and use the existing Debian kernel/initramfs package hooks with `kernel-install`'s UKI layout. First boot records the root filesystem UUID in `/etc/kernel/cmdline` if no administrator configuration exists, so subsequent initramfs-tools images can locate the root.

Request the vsock SSH listener explicitly through the kernel command line. Automatic discovery alone can miss the transport when a newly generated initramfs loads its driver later. Keep that driver in the guest initramfs configuration as well.

Version the new base image separately. Existing customized v3 guests are not silently rewritten; a tested upgrade mechanism remains separate work. Keep stopped backups before kernel-maintenance experiments.

## Consequences

Kernel package files retain normal Linux filesystem behavior, while the firmware still boots UKIs from FAT. The guest owns kernel updates through apt. The EFI partition has finite space; kernel retention and disk-full recovery remain release gates. An actual newer-kernel upgrade still needs separate evidence when one is available.

## Alternatives considered

- **Keep FAT at `/boot`:** incompatible with observed Debian package replacement behavior.
- **Freeze kernels in the base image:** prevents normal guest maintenance and independence.
- **Replace the whole guest disk on update:** loses user-installed software and configuration.

## References

- [ADR-0005](0005-vmspawn-and-nspawn-images.md), [image build](../../image/README.md).
- [Lifecycle](../design/lifecycle.md), [backup/reliability validation](../plans/backup-and-reliability.md).
- [systemd kernel-install](https://raw.githubusercontent.com/systemd/systemd/v257/man/kernel-install.xml), [SSH generator](https://raw.githubusercontent.com/systemd/systemd/v257/src/ssh-generator/ssh-generator.c).
