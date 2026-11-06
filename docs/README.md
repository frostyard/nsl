# Documentation

| Directory | Question | Contents |
| --- | --- | --- |
| [adr/](adr/) | Why? | Accepted architecture decisions |
| [design/](design/) | How? | Current mechanisms |
| [specs/](specs/) | What exactly? | Testable contracts |
| [plans/](plans/) | When? | Phased work |

## Current state

nsl is WSL-style machines on atomic Linux. [ADR-0016](adr/0016-wsl-style-machines.md) defines machines trusted as the user, with a default machine and host storage at `/mnt/host`. [ADR-0017](adr/0017-shared-vm-and-machine-images.md) runs them as systemd-nspawn containers in one shared VM, from signed Frostyard machine images, as the [shared-VM experiment](https://github.com/frostyard/nsl/blob/e617966e80f6534aa6525f016def59ab58c1d864/docs/plans/shared-vm-experiment.md) supported. The specs below describe that system; the [implementation plan](https://github.com/frostyard/nsl/blob/e617966e80f6534aa6525f016def59ab58c1d864/docs/plans/shared-vm-implementation.md) records how it was built and what each phase proved.

The binary implements the whole CLI contract: machines in the shared VM or, isolated, in VMs of their own; export and import; idle stop; forwarded ports, Waypipe windows, host menu entries, `nsl-open` and `ssh-config`; and the signed catalogue client. The [publication workflow](design/image-publication.md) publishes the VM image and eleven machine images weekly: Debian 13 and testing, Ubuntu 26.04 LTS and 24.04 LTS, Fedora 44 and Rawhide, CentOS Stream 10, Arch, openSUSE Tumbleweed and Leap 16.0, and the Azure Linux 4.0 beta. Catalogue sequence 6, on 2026-09-28, was the first, with four machine images; sequence 7, the same day, added the three of the [second plan](https://github.com/frostyard/nsl/blob/e617966e80f6534aa6525f016def59ab58c1d864/docs/plans/more-machine-images.md); sequence 9 opens Electron applications on Wayland and falls back to `C.UTF-8` for host locales a machine lacks; sequence 12 is the first from the dedicated `nsl-builder` runner ([ADR-0019](adr/0019-persistent-publication-runner.md)); sequence 16 added the Azure Linux 4.0 beta ([plan](plans/azure-linux.md)); and sequence 18 added Debian testing, Fedora Rawhide and Ubuntu 24.04 LTS ([plan](https://github.com/frostyard/nsl/blob/e617966e80f6534aa6525f016def59ab58c1d864/docs/plans/more-releases.md)). The earlier one-VM-per-environment prototype, with seven signed bootable images ([v0.3.0](https://github.com/frostyard/nsl/releases/tag/v0.3.0), [publication results](https://github.com/frostyard/nsl/blob/e617966e80f6534aa6525f016def59ab58c1d864/docs/plans/public-image-delivery.md)), has been removed from the code. Its reports and the finished plans of the shared-VM design are in git history at [`e617966`](https://github.com/frostyard/nsl/tree/e617966e80f6534aa6525f016def59ab58c1d864/docs/plans).

## Index

### Decisions

- [0024 — Adopt the frostyard repository baseline](adr/0024-adopt-the-repository-baseline.md)
- [0023 — Local problem records](adr/0023-local-problem-records.md)
- [0022 — Desktop menu entries for machine applications](adr/0022-desktop-menu-entries.md)
- [0021 — Machine-readable output with `--json`](adr/0021-machine-readable-output.md)
- [0020 — Homebrew tap publication](adr/0020-homebrew-tap-publication.md)
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

Only open work. A finished plan is deleted; git history keeps it.

- [Azure Linux 4.0 machine image](plans/azure-linux.md) — Phase 2 waits for a clean-host check.
- [Optional cloud-init provisioning](plans/cloud-init-provisioning.md) — deferred until cloud-init is re-validated in machines.
- [Unscheduled work after the shared VM](plans/later-work.md) — missing evidence and the ideas that the finished plans left open.

### Org-wide decisions

- [Core ADRs that bind nsl](org-adrs.md) — the frostyard/core decisions this repository follows, with where each applies.

User guides live in the [documentation site](../site/content/), published at [frostyard.github.io/nsl](https://frostyard.github.io/nsl/) ([ADR-0018](adr/0018-user-documentation-site.md)). New docs start from each category's `TEMPLATE.md`. While nsl is pre-release, ADRs, specs and plans are rewritten in place when decisions change ([ADR-0001](adr/0001-record-architecture-decisions.md)). Design docs evolve with implementation; specs evolve with code. Cross-link related documents in both directions. Canonical instructions: [AGENTS.md](../AGENTS.md).
