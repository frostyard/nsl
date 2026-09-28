# Plan: Machines in a shared VM

**Status: planned, 2026-09-27.** This plan implements [ADR-0016](../adr/0016-wsl-style-machines.md) and [ADR-0017](../adr/0017-shared-vm-and-machine-images.md). nsl becomes WSL-style machines, running as systemd-nspawn containers in one nsl-owned VM, from signed Frostyard machine images, behind the [machine CLI](../specs/machine-cli.md). The environment CLI stays the implemented product until the cutover in Phase 11. There is no migration.

The [shared-VM experiment](shared-vm-experiment.md) proved each mechanism in Python. This plan turns them into the Go CLI, a VM-side agent, two image pipelines and published artifacts. Every phase keeps `make ci` green, and unit tests use the fake runner without root or a VM. Integration evidence is recorded here as each phase completes. No dates are inferred; re-estimate after each phase's acceptance.

Dependencies:

- Phase 1 comes first.
- Phase 2 is independent.
- Phases 3 and 4 can run beside 5 and 6.
- Phase 7 needs 4, 5 and 6. Phases 8 and 9 need 7.
- Phase 10 needs 3 and 4 plus CLI-driven acceptance.
- Phase 11 is last.

## Phase 1 — Contracts

- **Split the [guest contract](../specs/guest-images.md)** into two new specs:
  - `docs/specs/vm-image.md`: boot and readiness, the machine data disk (format blank, refuse foreign signatures), identity and SSH host keys off the replaceable root, `/mnt/host` binds, the agent, Waypipe, nspawn version and descriptor.
  - `docs/specs/machine-images.md`: the machine layer from ADR-0017, the `/usr/lib/nsl/machine.json` descriptor, capabilities and per-family adapters.

  The current contract stays until cutover.
- **Write `docs/specs/agent.md`** for the host-to-agent protocol:
  - a versioned framed request: machine, argv, working directory, user or root, tty, environment allowlist;
  - stdio and PTY semantics, and exit mapping, including 128+N for signal deaths;
  - errors;
  - validation of machine names and trust tiers.
- **Extend the [delivery contract](../specs/image-delivery.md).**
  - A machine artifact type (`application/vnd.frostyard.nsl.machine.v1`): a single-layer zstd root filesystem with a signed descriptor, package inventory, provenance and acceptance report.
  - The VM image as a disk artifact whose descriptor declares its role.
  - A new `catalogue-v2` tag with `kind` fields and its own sequence and rollback state. The v0.3 CLI decodes catalogues with unknown fields rejected, so `catalogue-v1` keeps its current schema until cutover.
- **Detail the [machine CLI](../specs/machine-cli.md)'s** import path, idle-session accounting and archive format.
- **Done when:** the specs are indexed and cross-linked, and each carries fixtures or test cases a later phase can implement.

## Phase 2 — Configuration file

- `config.go` parses the `nsl.conf` INI subset into typed settings with their sources.
- Defaults come from `/proc/meminfo` and the CPU count.
- Unknown sections or keys, duplicates, bad values and out-of-range numbers fail with `file:line`.
- `$XDG_CONFIG_HOME` selects the file; tests set it.
- `nsl config` prints effective values and sources. Pending-restart reporting arrives with Phase 5.
- **Done when:** tests cover the absent file, every key's range, defaults, comments and whitespace, and each error class. `nsl config` works against a fixture file.

## Phase 3 — The nsl VM image

- **Promote `experiments/shared-vm/layer`** into a VM role of the image composer, on the Debian trixie profile: `systemd-container`, `nsl-machines-storage` and `var-lib-machines.mount` (blank-disk format, foreign-signature refusal), and the agent once Phase 6 provides it.
- **Move state off the replaceable root.** VM identity and SSH host keys move to an `nsl-state` subvolume on the data disk, or to credentials. Replacing the VM root must keep them, along with every machine.
- **Replaceable root.** The host creates the VM root overlay per image version; the data disk persists.
- **Acceptance** in `scripts/probe-vm.py`, ported from `experiments/shared-vm/driver.py check`:
  - readiness, data-disk formatting and refusal, and persistence across root replacement;
  - `/mnt/host` ownership, bounded guest root, and unproxied sockets.
- **Update the [validate-image skill](../../.agents/skills/validate-image/SKILL.md)** for the VM role.
- **Done when:** a locally built VM image passes `probe-vm.py`, and a root replacement keeps identity and machines.

## Phase 4 — Machine images

- **`image/machines/`:** compose from the pinned `mkosi-definitions` recipes without the disk profile, as a tar or OCI root filesystem, in the existing Lima builder with the tools trees the RPM, SUSE and Arch profiles already use.
- **Common machine layer:**
  - locked root and an uninitialized machine ID;
  - networkd and resolved disabled;
  - `sudo`;
  - an `nsl` PAM service built from each family's account and session stacks, with `pam_systemd`;
  - `run-nsl-proc.mount` with its preset, and the Podman `containers.conf.d` drop-in;
  - zone data, fonts and a cursor theme;
  - `nsl-path`;
  - the descriptor.
- **Family adapters:**
  - Debian: `libpam-systemd`, `dbus-user-session`, `tzdata`.
  - Fedora: `systemd-pam`, `tzdata`.
  - Arch: no shipped `/etc/pacman.d/gnupg`; a first-boot unit runs `pacman-key --init` and `--populate`.
  - Tumbleweed: `shadow`, `timezone`.
- **Acceptance** in `scripts/probe-machines.py`, ported from `experiments/shared-vm/workloads.py`: the 32 Phase 3 checks in a CI shared VM, plus tally regression checks. Those confirm there is:
  - no usable root password;
  - no private key under a package keyring;
  - no enabled networkd or resolved;
  - no machine ID value;
  - `pam_systemd`, `sudo`, a user bus, fonts and the nesting mount present.
- **Done when:** Debian 13, Fedora 44, Arch and Tumbleweed images build locally and pass `probe-machines.py`, with every tally item verified absent, and creation needs no network.

## Phase 5 — VM lifecycle in the CLI

- **Shared VM record.** Replace per-environment metadata with one record per state directory: ID, CID, image version, the data disk and the resources in effect.
- **Units** named `nsl-UID-vm-ID.service`, validated by description before any control, as today.
- **Launch** keeps the device-descriptor path. It adds the data disk as an extra drive, the allowlist binds, and a read-only nsl cache share for image import, replacing the experiment's `/mnt/host` shortcut.
- **Readiness** through the agent's identity and descriptor, with the SSH transport unchanged.
- **`stop`, `recover` and `resize --disk`** act on the VM and its data disk, under ADR-0008's locking and resumption. The VM stops when no machine runs. Configuration changes show as pending restarts in `nsl config` and `nsl list`.
- **Keep** the runner interface, ownership and lock checks, atomic writes and private-file checks from `state.go` and `vm.go`.
- **Done when:** fake-runner tests cover launch arguments, configuration application, unit ownership, locking and data-disk growth. The CLI starts, stops, recovers and grows a locally built VM.

## Phase 6 — Agent and command execution

- **The agent** is installed in the VM image and reached through a forced SSH command. It starts transient units in machines through systemd's D-Bus API and machined:
  - literal argv, without environment expansion;
  - exit status from `ExecMainCode`/`ExecMainStatus`, with 128+N for signals;
  - PTYs from machined with no title or color sequences (OSC 3008 passes);
  - PAM through the image's `nsl` service;
  - the working directory, and an environment allowlist (`TERM`, `WAYLAND_DISPLAY`, locale).
- **Host side:** `nsl run` and bare `nsl`, with device-and-inode directory translation. An untranslatable directory fails `run` and starts a shell in the guest home.
- **Replace the `systemd-run --machine` reference path** once the agent passes the same matrix.
- **Done when:** the Phase 2 experiment matrix passes through the CLI on all four machine images, run by a port of `machines.py check`: argv, exit and signal status, separate and binary streams, identity, session, PTY and Ctrl-C. Latency is recorded against the experiment's 65 ms.

## Phase 7 — Machines in the CLI

- **`create`** verifies a cached machine image, imports it into a subvolume through the cache share, then applies per-machine data: account, hostname and hosts entry, the host zone link before anything runs, the `sudo` rule and nspawn settings. Settings are `PrivateUsers=no`, the VM's network, `Bind=/mnt/host`, the VM's resolver and `Timezone=off`. A failure removes the subvolume and keeps the name free.
- **`list`, `default`, `start`, `stop`, `shutdown` and `remove`** (stopped machines only).
- **Autostart and idle stop.** At VM start, `autostart` enables or disables `machines.target` membership. Idle stop counts agent sessions and Waypipe clients.
- **`export` and `import`:** a versioned archive of the subvolume (numeric owners, xattrs, ACLs) with a checksummed manifest. ADR-0006's validation carries over: no links outside the tree, no traversal, no duplicates or trailing data. The trust tier is chosen at import.
- **Done when:** four machines are created offline from the cache and pass `probe-machines.py` through the CLI. An export and import round trip preserves packages, home and services. Fake-runner tests cover creation cleanup, removal and archive validation.

## Phase 8 — Host integration

- **Files.** Allowlist binds, top-level alias symlinks under `/mnt/host` (such as `home → var/home`), and `nsl-path`.
- **Ports.** One forwarder for the VM, reusing `ports.go` discovery. The VM's namespace is shared, so a machine-to-machine conflict is reported per port.
- **Desktop.** One persistent Waypipe session per machine, with its display socket attached through `machinectl bind`, and `WAYLAND_DISPLAY` in agent sessions. GUI is on by default for machines that are not isolated.
- **Broker.** `nsl-open`, authenticated per machine; it accepts only `http`/`https` URLs and translatable `/mnt/host` paths.
- **Done when:** the files, ports, translation and GUI checks of `probe-machines.py` pass through the CLI, and broker tests refuse every other target.

## Phase 9 — Isolated machines

- `create --isolated` and `import --isolated` give a machine its own VM from the same image, with `[isolated]` resources. It has no `/mnt/host`, broker, Waypipe or peers, and its lifecycle mirrors the shared VM's.
- **Done when:** an isolated machine passes the workload checks other than host integration, and tests show it has no host share, broker socket or display socket.

## Phase 10 — Publication

- **`images.yml`** builds and accepts the VM image and the four machine images with `probe-vm.py` and `probe-machines.py`. It signs them with the existing workflow identity and promotes `catalogue-v2`.
- **Rebuild cadence:** at least weekly for distro security updates, inside ADR-0015's 30-day freshness window.
- **Public artifacts** exclude private evidence, under the [publication design](../design/image-publication.md).
- **Done when:** the CLI creates machines from a published `catalogue-v2` on a host with no local builds, and the [image publication design](../design/image-publication.md) documents both artifact kinds.

## Phase 11 — Cutover and release

- **Switch `main.go`** to the machine grammar.
- **Remove the environment code:**
  - environment commands;
  - the qcow2 backup path in `backup.go`;
  - per-environment storage;
  - per-distro VM profiles and families, apart from the VM's Debian base;
  - the ADR-0014 hooks;
  - the per-distro VM probes.
- **Update docs.** Make the machine CLI the implemented contract, retire `cli.md` and the old guest contract, and update the README, [AGENTS.md](../../AGENTS.md) live conventions, the [lifecycle design](../design/lifecycle.md) and the index. Regenerate `THIRD_PARTY_NOTICES.txt` if the agent adds dependencies.
- **Release v0.4.0** as a breaking release. Stop promoting per-distro disks in `catalogue-v1`, and keep its referenced digests available.
- **Done when:** `make ci`, the image acceptance and release checks pass, and on a clean host `nsl create debian --distro debian:13` followed by `nsl` opens a shell in the current directory.

## Requirements carried from the experiment

Each item was found by a failing check and must not regress.

| Requirement | Phase |
| --- | --- |
| Blank-disk formatting only; refuse any existing signature | 3 |
| Machine images apply presets on first boot; integration units ship presets | 4 |
| Nesting: a fully visible procfs, `keyring = false`, `default_sysctls = []` | 4 |
| No enabled networkd or resolved; bind the VM's resolver | 4, 7 |
| Link the host zone before anything runs in a tree; `Timezone=off`, including offline nspawn runs | 7 |
| One `nsl` PAM service; no per-distro PAM mapping in the CLI | 4, 6 |
| Literal argv, 128+N signal status, PTY without title or color escapes | 6 |
| Directory translation by device and inode, plus `/mnt/host` aliases | 6, 8 |
| Waypipe `--display` socket bound into the running machine | 8 |
| Creation cleans up after failure | 7 |
| Four idle machines at or below the experiment's 950 MiB; an additional machine at p95 ≤ 2 s | 7, recorded with `experiments/shared-vm/measure.py` |

## Later / ideas

- Size the VM to its running machines (virtio-mem or balloon targets); one machine currently costs twice a single VM.
- Keep agent sessions warm to recover the experiment's 48 ms transport overhead.
- Measure and bound virtiofsd memory under file load.
- Find the 1.3–2 s Ctrl-C delay in the SSH transport; it predates this work.
- Translate absolute host symlinks; handle `/run/media/USER` appearing after VM start, and host automounts.
- Launcher exports and terminal integration through OSC 3008 context markers.
- More machine images (Ubuntu, CentOS Stream, openSUSE Leap), each after acceptance.
- Re-validate cloud-init inside machines before [provisioning](../specs/provisioning.md) ships.
- Private user namespaces with idmapped virtiofs mounts, if trusted machines ever need isolation from each other.

## Open questions

| Question | Default proposal | Resolve by |
| --- | --- | --- |
| Agent language and D-Bus library? | Go with a pinned D-Bus library, built into the VM image, with license notices regenerated. Python with `jeepney` is the lighter alternative. | Phase 1 |
| Where do VM identity and host keys live? | An `nsl-state` subvolume on the data disk. | Phase 3 |
| Machine image output format? | mkosi `Format=tar`, repackaged as one zstd OCI layer, so the importer needs no OCI whiteout handling. | Phase 4 |
| Same GHCR repository for all artifact kinds? | Yes: `frostyard/nsl-images` with distinct artifact types, one `catalogue-v2`. | Phase 1 |
| Machine archive encoding? | tar with numeric owners, xattrs and ACLs rather than `btrfs send`, which ties archives to btrfs versions. | Phase 7 |
| Idle-session accounting? | The agent counts command sessions; the Waypipe session reports connected clients. | Phase 7 |

## References

- Decisions: [ADR-0016](../adr/0016-wsl-style-machines.md), [ADR-0017](../adr/0017-shared-vm-and-machine-images.md), [ADR-0012](../adr/0012-signed-image-distribution.md), [ADR-0015](../adr/0015-image-verification-and-catalogue-policy.md), [ADR-0008](../adr/0008-offline-storage-management.md), [ADR-0006](../adr/0006-stopped-vm-backups.md).
- Contracts: [machine CLI](../specs/machine-cli.md), [image delivery](../specs/image-delivery.md), [guest images](../specs/guest-images.md).
- Evidence and reference code: [shared-VM experiment](shared-vm-experiment.md), [experiment code](../../experiments/shared-vm/README.md).
- Pipeline: [image publication](../design/image-publication.md), [image build](../../image/README.md).
