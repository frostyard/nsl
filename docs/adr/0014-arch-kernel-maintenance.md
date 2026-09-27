# 0014 — Synchronize Arch kernels into unified boot images

- **Status:** Accepted
- **Date:** 2026-09-27

## Context

The image builder supplies the initial UKI, but Arch's dracut package hooks normally generate separate kernel/initramfs files under `/boot`. nsl needs ordinary guest package updates to keep the EFI boot path working. The host must remain independent of the guest package manager, as required by [ADR-0009](0009-distribution-neutral-guest-contract.md).

## Decision

Use Arch's packaged dracut and systemd-ukify through kernel-install. Replace its separate-initramfs pacman hooks with an image-owned post-transaction hook. On kernel, dracut or ukify changes, regenerate every installed kernel before removing any obsolete nsl-managed boot entry.

Initialize a small inventory of managed kernel versions on first boot. Update it atomically only after successful generation and cleanup. Reject malformed inventories and refuse to remove the final kernel. Preserve previous entries when generating a replacement fails. Do not remove entries outside the recorded inventory.

The package manager continues to own kernel files and packages. nsl owns the conversion to UKIs in this image profile; host lifecycle commands contain no pacman/dracut logic.

## Consequences

Kernel upgrades and reinstalls regenerate the actual boot files. Failed generation keeps the previous entry for recovery. The acceptance suite must verify the newly installed kernel is running when the transaction adds a kernel version, rather than accepting a reboot into the previous kernel. Arch still follows its rolling-release package-update requirements.

## Alternatives considered

- Keep separate-initramfs hooks: they do not update the UKIs used by this image.
- Remove old entries before generating new ones: a failed build could leave no usable boot entry.

## References

- [SUSE and Arch validation](../plans/suse-and-arch.md), [guest contract](../specs/guest-images.md), [distribution plan](../plans/distribution-support.md).
- [Arch dracut package files](https://archlinux.org/packages/extra/x86_64/dracut/files/), [Arch systemd-ukify](https://archlinux.org/packages/core/x86_64/systemd-ukify/).
