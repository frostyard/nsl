# 0010 — Require root filesystem growth before guest readiness

- **Status:** Accepted; narrowed to the nsl VM image by [ADR-0017](0017-shared-vm-and-machine-images.md)
- **Date:** 2026-09-26

## Context

The first real offline resize grew a maintained v4 guest's virtual disk and root partition from 16 to 24 GiB, but its mounted btrfs filesystem stayed at its former capacity. After a Debian kernel reinstall, the explicit root UUID in the regenerated kernel command line bypassed the automatic root discovery that had supplied filesystem growth. The installed `systemd-growfs-root.service` was never started.

## Decision

The Debian v5 image makes `nsl-setup.service` require and run after `systemd-growfs-root.service`. That existing service runs after repartitioning and remounting. SSH already requires successful nsl setup, so growth failures prevent readiness. Keep filesystem logic in the guest image; the host CLI only changes and verifies virtual disk capacity.

This supplements [ADR-0008](0008-offline-storage-management.md). Existing v4 images and backups do not acquire the new dependency by updating the CLI. They need an explicit guest integration update or a fresh v5 environment.

## Consequences

Growth runs on each boot, including after package-manager kernel regeneration, and is harmless when capacity already matches. Other distributions must supply equivalent ordering and their filesystem tools through the [guest contract](../specs/guest-images.md). No btrfs command is embedded in the host CLI.

## Alternatives considered

Relying only on the repart GPT grow flag reproduced the failure. Running filesystem commands from the host would couple the CLI to guest filesystem and package choices.

## References

- [Storage validation](../plans/storage-management.md), [image build](../../image/README.md), [lifecycle](../design/lifecycle.md).
- [systemd root discovery](https://github.com/systemd/systemd/blob/main/man/systemd-gpt-auto-generator.xml), [growth services](https://github.com/systemd/systemd/blob/main/man/systemd-makefs%40.service.xml).
