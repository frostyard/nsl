# 0010 — Grow the data filesystem before VM readiness

- **Status:** Accepted
- **Date:** 2026-09-26

## Context

The first real offline resize grew a guest's virtual disk and partition from 16 to 24 GiB, but its mounted btrfs filesystem kept its old capacity. After a kernel reinstall, the regenerated command line bypassed the automatic root discovery that had scheduled filesystem growth. A larger virtual disk alone was not growth.

Under [ADR-0017](0017-shared-vm-and-machine-images.md), the VM root has a fixed size and is replaced, not grown. The data disk, which holds every machine, is what grows.

## Decision

The VM image grows the data disk's btrfs filesystem to the size of its device at every boot, before the agent accepts sessions. A failure blocks readiness. The host CLI only changes and verifies virtual capacity; it runs no filesystem command.

## Consequences

Growth runs on every boot and is harmless when the sizes already match. Resize acceptance checks the filesystem size seen in the VM, not only the virtual disk.

## Alternatives considered

- **Automatic growth through generators:** reproduced the failure above.
- **Host-side filesystem commands:** couple the CLI to the VM's filesystem tools.

## References

- [VM image](../specs/vm-image.md#data-disk), [ADR-0008](0008-offline-storage-management.md).
- History: [storage validation](../plans/storage-management.md).
