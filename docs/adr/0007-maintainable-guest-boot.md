# 0007 — Boot the VM image from a UKI and update it by replacement

- **Status:** Accepted
- **Date:** 2026-09-26

## Context

A kernel reinstall in an early Debian guest failed because `/boot` was a FAT EFI partition. Debian's package manager could not create backup hard links there, and the failed operation removed the boot entry. At the time, each guest maintained its own kernel through apt.

Under [ADR-0017](0017-shared-vm-and-machine-images.md), machines have no kernel. The nsl VM image is the only bootable image, and nsl replaces its root as a unit to deliver kernel and userspace updates.

## Decision

- The VM image mounts the EFI System Partition at `/efi` and keeps `/boot` on the root filesystem. It boots a unified kernel image (UKI) built with `systemd-ukify` when the image is composed.
- Kernel and bootloader updates arrive only as new VM images. The VM does not maintain its kernel in place; changes made on the root are discarded at the next root replacement.
- The vsock SSH listener belongs to the image ([ADR-0011](0011-image-profiles-and-portable-vsock.md)), not to automatic generator discovery.

## Consequences

The kernel is part of a tested, signed VM image, and every VM gets it at its next start. nsl owns a kernel security cadence, like WSL. A running VM cannot pick up a kernel fix until it restarts.

## Alternatives considered

- **Maintain the VM kernel with apt:** reintroduces per-VM drift and the failure modes this image avoids, on a root that holds no user state.
- **FAT at `/boot`:** incompatible with Debian package replacement, should anyone run apt for diagnosis.

## References

- [VM image](../specs/vm-image.md), [image build](../../image/README.md), [ADR-0017](0017-shared-vm-and-machine-images.md).
- History: [backup/reliability validation](../plans/backup-and-reliability.md). [systemd kernel-install](https://www.freedesktop.org/software/systemd/man/latest/kernel-install.html).
