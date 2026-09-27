# 0006 — Self-contained backups of stopped VMs

- **Status:** Accepted
- **Date:** 2026-09-26

## Context

The vmspawn prototype now holds persistent packages, projects and settings. Restart recovery checks an existing disk but cannot replace a lost disk. The protocol-1 guest binds its first-boot identity to a public key and numeric UID/GID. A backup must restore that binding without requiring the original raw image or altering a customized guest offline.

## Decision

Export only stopped, prepared environments to a versioned tar archive containing a standalone qcow2 disk, a checksummed manifest, the client keypair and optional pinned host trust. Publish a private archive without replacing an existing path. Shared host projects are outside the backup.

Restore under a new name with a fresh host runtime ID, user-unit name, sockets and CID. Preserve the guest binding, SSH identities, machine ID, packages and files. This is a whole-system restore; creating a separately authenticated clone is future work. This qualifies ADR-0005's fresh-environment key isolation: separately created environments have distinct keys, while a backup and its restored copies retain the same guest authentication identity.

Initially require matching host numeric UID and primary GID and x86_64. Restore no project share or desktop grant unless explicitly requested. Validate the archive in private staging, reject unexpected/duplicate/link entries and disk references to external files, then publish verified state. Do not require the raw-image cache. Checksums detect damage; they do not authenticate the archive's publisher. Backups contain credentials and are unencrypted.

## Consequences

Users can preserve and recover customized guests independently of base-image availability. A backup needs temporary disk space and downtime. Restored copies may run simultaneously with different runtime addresses, but applications may need their own identity changes before treating a restored server as a distinct server. Cross-user migration and fresh-identity cloning need a future guest protocol.

## Alternatives considered

- **Live disk copying:** does not provide a consistent backup without guest coordination and snapshot machinery.
- **Rebuild from base plus home:** loses installed packages and system configuration.
- **Rotate guest identity during restore:** requires a tested rebind protocol and changes the backed-up system; defer to a distinct clone/migration feature.

## References

- [ADR-0005](0005-vmspawn-and-nspawn-images.md), [lifecycle](../design/lifecycle.md), [CLI contract](../specs/cli.md).
- [Backup and reliability plan](../plans/backup-and-reliability.md), [roadmap](../plans/wsl2-equivalent.md).
- [QEMU qcow2 format](https://www.qemu.org/docs/master/interop/qcow2.html).
