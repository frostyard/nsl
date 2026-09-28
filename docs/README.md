# Documentation

| Directory | Question | Contents |
| --- | --- | --- |
| [adr/](adr/) | Why? | Accepted architecture decisions |
| [design/](design/) | How? | Current mechanisms |
| [specs/](specs/) | What exactly? | Testable contracts |
| [plans/](plans/) | When? | Phased work |

## Current state

nsl is becoming WSL-style machines on atomic Linux. [ADR-0016](adr/0016-wsl-style-machines.md) defines machines trusted as the user, with a default machine and host storage at `/mnt/host`. [ADR-0017](adr/0017-shared-vm-and-machine-images.md) runs them as systemd-nspawn containers in one shared VM, from signed Frostyard machine images, as the [shared-VM experiment](plans/shared-vm-experiment.md) supported. The specs below describe that target system; the [implementation plan](plans/shared-vm-implementation.md) phases the work and records what is live.

The binary implements the whole CLI contract: machines in the shared VM or, isolated, in VMs of their own; export and import; idle stop; forwarded ports, Waypipe windows, `nsl-open` and `ssh-config`; and the signed catalogue client. The [publication workflow](design/image-publication.md) publishes the VM image and seven machine images weekly: Debian 13, Ubuntu 26.04 LTS, Fedora 44, CentOS Stream 10, Arch, openSUSE Tumbleweed and Leap 16.0. Catalogue sequence 6, on 2026-09-28, was the first, with four machine images; sequence 7, the same day, added the three of the [second plan](plans/more-machine-images.md), and sequence 9 opens Electron applications on Wayland and falls back to `C.UTF-8` for host locales a machine lacks. The earlier one-VM-per-environment prototype, with seven signed bootable images ([v0.3.0](https://github.com/frostyard/nsl/releases/tag/v0.3.0), [publication results](plans/public-image-delivery.md)), has been removed from the code; its reports remain as history.

## Index

### Decisions

- [0019 — Persistent publication runner](adr/0019-persistent-publication-runner.md)
- [0018 — User documentation site](adr/0018-user-documentation-site.md)
- [0017 — Shared VM and Frostyard machine images](adr/0017-shared-vm-and-machine-images.md)
- [0016 — WSL-style machines trusted as the user](adr/0016-wsl-style-machines.md)
- [0015 — Image verification and catalogue policy](adr/0015-image-verification-and-catalogue-policy.md)
- [0013 — Optional cloud-init provisioning](adr/0013-optional-cloud-init-provisioning.md) — deferred.
- [0012 — Signed image distribution through GHCR](adr/0012-signed-image-distribution.md)
- [0011 — Image composition and the vsock transport](adr/0011-image-profiles-and-portable-vsock.md)
- [0010 — Data filesystem growth before readiness](adr/0010-explicit-guest-root-growth.md)
- [0009 — One machine-image contract across families](adr/0009-distribution-neutral-guest-contract.md)
- [0008 — Offline removal and data-disk growth](adr/0008-offline-storage-management.md)
- [0007 — VM image boot and update by replacement](adr/0007-maintainable-guest-boot.md)
- [0006 — Machine archives](adr/0006-stopped-vm-backups.md)
- [0005 — Run nsl VMs with systemd-vmspawn](adr/0005-vmspawn-and-nspawn-images.md)
- [0004 — Manage full development VMs with native host integration](adr/0004-managed-development-vms.md)
- [0003 — Wrap nspawn for atomic-host development](adr/0003-wrap-nspawn-for-development.md)
- [0002 — Agent-portable instruction surface](adr/0002-agent-portable-instruction-surface.md)
- [0001 — Record architecture decisions](adr/0001-record-architecture-decisions.md)

### Design

- [Machine lifecycle and host integration](design/lifecycle.md) — the shared VM, machines, files, ports and desktop.
- [Image publication](design/image-publication.md) — weekly KVM workflow for the VM and machine images, public artifact boundary and signing.

### Specs

- [CLI contract](specs/cli.md) — commands, rules and the `nsl.conf` configuration file.
- [Agent protocol](specs/agent.md) — host-to-VM requests, command execution and exit status.
- [VM image](specs/vm-image.md) — boot, data disk, identity, `/mnt/host` and acceptance.
- [Machine images](specs/machine-images.md) — machine layer, family adapters, descriptor and acceptance.
- [Signed image delivery](specs/image-delivery.md) — catalogue, artifacts, verification and cache.
- [Creation-time provisioning](specs/provisioning.md) — deferred cloud-init interface.

### Plans

- [Machines in a shared VM](plans/shared-vm-implementation.md) — implementation of ADR-0016 and ADR-0017; start here for new work.
- [Ubuntu, CentOS Stream and openSUSE Leap machine images](plans/more-machine-images.md) — three more machine images, accepted and published.
- [Shared-VM experiment](plans/shared-vm-experiment.md) — machines as containers in one VM; evidence for ADR-0017.
- [Optional cloud-init provisioning](plans/cloud-init-provisioning.md) — deferred until cloud-init is re-validated in machines.

Historical reports for the one-VM-per-environment design:

- [Public image delivery results](plans/public-image-delivery.md) — exact v0.3.0 artifacts and release acceptance.
- [Signed prebuilt image delivery](plans/image-distribution.md) — client and publication workflow.
- [v0.2.0 and v0.3.0 release gates](plans/v0.2-v0.3-release.md)
- [SUSE and Arch validation](plans/suse-and-arch.md)
- [Fedora and CentOS Stream guests](plans/rpm-guests.md)
- [Image profiles and Ubuntu](plans/image-profiles-and-ubuntu.md)
- [Broad distribution support](plans/distribution-support.md)
- [Safe removal and disk growth](plans/storage-management.md)
- [Backup/restore and daily-work reliability](plans/backup-and-reliability.md)
- [vmspawn Go implementation and validation](plans/vmspawn-implementation.md)
- [nspawn-derived image and vmspawn comparison](plans/vmspawn-comparison.md)
- [VM proof of concept — experiment and results](plans/vm-proof-of-concept.md)
- [v0.1.0 retrospective](plans/v0.1.0.md)
- [WSL2-like environments on atomic Linux — research and roadmap](plans/wsl2-equivalent.md)

User guides live in the [documentation site](../site/content/), published at [frostyard.github.io/nsl](https://frostyard.github.io/nsl/) ([ADR-0018](adr/0018-user-documentation-site.md)). New docs start from each category's `TEMPLATE.md`. While nsl is pre-release, ADRs, specs and plans are rewritten in place when decisions change ([ADR-0001](adr/0001-record-architecture-decisions.md)). Design docs evolve with implementation; specs evolve with code. Cross-link related documents in both directions. Canonical instructions: [AGENTS.md](../AGENTS.md).
