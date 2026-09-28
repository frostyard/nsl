# 0006 — Export stopped machines as self-contained archives

- **Status:** Accepted
- **Date:** 2026-09-26

## Context

A machine holds packages, services, configuration and a guest-native home. Under [ADR-0017](0017-shared-vm-and-machine-images.md), it is a btrfs subvolume on the VM's data disk, and the VM root holds no user state. Users need to back up, move and restore machines without the original image cache or catalogue, and without trusting an archive to choose its own host access.

## Decision

Export only stopped machines. An archive is a tar file with two members:

- `manifest.json`: the format version, machine name, account name, UID and GID, image build ID, and the SHA256 and size of the root filesystem;
- `rootfs.tar.zst`: a zstd tar of the subvolume that preserves numeric owners, modes, xattrs (including file capabilities) and ACLs.

Publish a mode-0600 archive without replacing an existing path, and leave the source unchanged. The VM is not exported.

Import under an unused name:

- Require x86-64 and the archive's UID and primary GID to match the host user.
- Validate the manifest, checksums and every entry in private staging before publishing. Refuse absolute or `..` paths, links leaving the tree, duplicates, unexpected entries and trailing data. An invalid archive leaves no named machine.
- Apply per-machine data for the new name: hostname, hosts entry, host time zone and nspawn settings.
- Choose the trust tier from the import flags, never from the archive ([ADR-0016](0016-wsl-style-machines.md)).
- Preserve the machine ID and contents. An imported copy is the same system, not a fresh clone.

The format version detects a mismatched reader; archives from other versions are refused, not converted. Checksums detect damage; they do not authenticate the archive's origin. Archives are unencrypted and can contain credentials.

## Consequences

Users can keep and restore customized machines independently of image availability. An export needs the machine stopped and temporary space. Importing on a host with a different UID needs remapping, which is later work. Two imports of one archive share a machine ID, so applications may need their own identity changes before acting as distinct systems.

## Alternatives considered

- **`btrfs send` streams:** fast, but tie archives to btrfs and are harder to validate entry by entry. A stream can also reference other subvolumes for clones.
- **Whole-VM qcow2 archives:** the VM holds every machine and no user state of its own.
- **Live snapshots:** need guest coordination for consistency; stopped export is enough for now.
- **Rebuild from the image plus home:** loses installed packages and system configuration.

## References

- [CLI contract](../specs/cli.md#export-and-import), [agent](../specs/agent.md), [lifecycle](../design/lifecycle.md#export-and-import).
- [ADR-0017](0017-shared-vm-and-machine-images.md), [ADR-0016](0016-wsl-style-machines.md). History: [backup and reliability plan](../plans/backup-and-reliability.md).
