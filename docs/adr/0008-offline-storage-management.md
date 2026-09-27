# 0008 — Remove and grow stopped environments

- **Status:** Accepted; partially superseded by [ADR-0017](0017-shared-vm-and-machine-images.md): growth applies to the shared VM's data disk, removal to machine subvolumes
- **Date:** 2026-09-26

## Context

Whole-system backups now restore without the original image cache. Users also need to discard unused environments and increase disk capacity without editing qcow2 files manually. Neither operation should interrupt an active VM or confuse concurrent commands when a name is reused.

## Decision

`remove NAME` previews deletion. `remove NAME --yes` requires the owned VM and forwarding unit to be stopped. Under the manager and environment locks, move the environment into a private `removing/NAME` directory, then delete its contents. Preserve metadata until the end so interrupted deletion can resume with the same command. Block name reuse until deletion finishes. Shared host projects, cached base images and exported backups remain outside removal's scope. Lifecycle commands waiting on an old environment must reject a replacement ID.

`resize NAME --disk GiB` grows a stopped, prepared standalone disk. Shrinking is forbidden. Record the pending target in metadata before invoking qemu-img, sync and verify the resulting disk, then commit the new size. Retrying resize or running recover completes interrupted growth; start and export refuse a pending resize. The guest's existing repart configuration grows its root on the next boot. The CLI reports virtual capacity separately from guest filesystem growth.

## Consequences

Deletion is explicit and resumable. Growth does not replace the disk, guest identity or credentials. A pending growth intent is not rolled back by shrinking. Backups remain advisable before storage changes. Filesystem growth, especially on customized guests, must be verified independently; a larger virtual disk alone is not success.

## Alternatives considered

- **Implicitly stop active VMs:** interrupts work; require an explicit stop.
- **Delete in place:** leaves partially removed environments in the active namespace.
- **Shrink disks:** needs filesystem-specific offline shrink support; excluded.
- **Copy the entire disk for growth:** adds time and storage cost without replacing the need for a backup; use durable intent and retry instead.

## References

- [Lifecycle](../design/lifecycle.md), [CLI contract](../specs/cli.md), [storage milestone](../plans/storage-management.md).
- [Backup decision](0006-stopped-vm-backups.md), [roadmap](../plans/wsl2-equivalent.md).
