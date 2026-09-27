# Documentation

| Directory | Question | Contents |
| --- | --- | --- |
| [adr/](adr/) | Why? | Accepted architecture decisions |
| [design/](design/) | How? | Current mechanisms |
| [specs/](specs/) | What exactly? | Testable contracts |
| [plans/](plans/) | When? | Phased work |

## Current state

The main Go CLI launches full Debian VMs using systemd-vmspawn/QEMU and nspawn-derived bootable images. Lima is the image builder and historical runtime baseline. Read the [user guide](../README.md), [measured vmspawn results](plans/vmspawn-implementation.md) and [prioritized roadmap](plans/wsl2-equivalent.md).

The completed storage increment follows [backup/restore and guest reliability](plans/backup-and-reliability.md): [safe removal and disk growth](plans/storage-management.md). [Broad distribution support](plans/distribution-support.md) is the next architectural increment. The contracts describe implemented behavior; the plans distinguish measured checks from pending acceptance work. Historical reports retain their original measurements and are labeled accordingly.

## Index

### Decisions

- [0010 — Explicit guest root growth](adr/0010-explicit-guest-root-growth.md)
- [0009 — Distribution-neutral guest contract](adr/0009-distribution-neutral-guest-contract.md)
- [0008 — Offline storage management](adr/0008-offline-storage-management.md)

- [0007 — Maintainable Debian guest boot layout](adr/0007-maintainable-guest-boot.md)
- [0006 — Self-contained backups of stopped VMs](adr/0006-stopped-vm-backups.md)
- [0005 — Use vmspawn and nspawn-derived development images](adr/0005-vmspawn-and-nspawn-images.md)
- [0001 — Record architecture decisions](adr/0001-record-architecture-decisions.md)
- [0002 — Agent-portable instruction surface](adr/0002-agent-portable-instruction-surface.md)
- [0003 — Wrap nspawn for atomic-host development](adr/0003-wrap-nspawn-for-development.md)
- [0004 — Manage full development VMs with native host integration](adr/0004-managed-development-vms.md)

### Design

- [Lifecycle and mounts](design/lifecycle.md)

### Specs

- [Guest image contract and acceptance levels](specs/guest-images.md)

- [CLI contract](specs/cli.md)

### Plans

- [Broad distribution support](plans/distribution-support.md)
- [Safe removal and disk growth](plans/storage-management.md)

- [Backup/restore and daily-work reliability](plans/backup-and-reliability.md)
- [vmspawn Go implementation and validation](plans/vmspawn-implementation.md)
- [v0.1.0 retrospective](plans/v0.1.0.md)
- [nspawn-derived image and vmspawn comparison](plans/vmspawn-comparison.md)
- [VM proof of concept — experiment and results](plans/vm-proof-of-concept.md)
- [WSL2-like environments on atomic Linux — research and roadmap](plans/wsl2-equivalent.md)

New docs start from each category's `TEMPLATE.md`. ADRs are immutable after acceptance (except supersession/link repairs). Design docs evolve with implementation; specs evolve with code. Cross-link related documents in both directions. Canonical instructions: [AGENTS.md](../AGENTS.md).
