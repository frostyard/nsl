# 0008 — Remove stopped machines and grow the data disk offline

- **Status:** Accepted
- **Date:** 2026-09-26

## Context

Under [ADR-0017](0017-shared-vm-and-machine-images.md), machines share one btrfs data disk per VM. Users need to discard machines and add capacity without editing disk images by hand. Neither operation should interrupt active work, or confuse concurrent commands when a name is reused.

## Decision

`remove NAME` previews deletion. `remove NAME --yes`:

- requires a stopped machine, and the manager and machine locks;
- moves the host record into a private `removing/NAME` tombstone;
- has the agent delete the subvolume and the VM's record, then deletes the host record last, so an interrupted removal resumes with the same command;
- blocks reuse of the name until deletion finishes;
- leaves host files, cached images and exported archives alone.

Lifecycle commands that waited on a lock reject a replacement ID.

`resize [NAME] --disk GiB`:

- grows the stopped VM's data disk; shrinking is forbidden;
- records the pending target before extending the disk file, then syncs and verifies the disk before committing the new size;
- is completed by a repeated `resize` or by `recover`, and VM start refuses pending growth.

The VM grows its filesystem at the next boot ([ADR-0010](0010-explicit-guest-root-growth.md)). The CLI reports virtual capacity separately from filesystem growth.

## Consequences

Deletion is explicit and resumable. Growth keeps the VM's identity and every machine. A pending growth is never rolled back by shrinking. Machines share the disk's capacity; per-machine quotas are later work.

## Alternatives considered

- **Implicitly stop active machines:** interrupts work; require an explicit stop.
- **Delete in place:** leaves partly removed machines in the active namespace.
- **Shrink disks:** needs filesystem-specific offline shrinking.
- **One disk per machine:** needs hot-plug and loses shared capacity and cheap snapshots.

## References

- [CLI contract](../specs/cli.md#storage), [lifecycle](../design/lifecycle.md#storage), [VM image](../specs/vm-image.md#data-disk).
- History: [storage milestone](../plans/storage-management.md).
