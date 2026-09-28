# Plan: Machines in a shared VM

**Status: Phases 1 and 2 complete, 2026-09-27.** This plan implements [ADR-0016](../adr/0016-wsl-style-machines.md) and [ADR-0017](../adr/0017-shared-vm-and-machine-images.md). nsl becomes WSL-style machines, running as systemd-nspawn containers in one nsl-owned VM, from signed Frostyard machine images, behind the [CLI contract](../specs/cli.md). The [shared-VM experiment](shared-vm-experiment.md) proved every mechanism in Python; this plan turns them into the Go CLI, a VM-side agent, two image pipelines and published artifacts.

## Working rules

- **Replace in place, with no bridges.** nsl is pre-release and has no users. Do not keep the environment CLI working, migrate state, version catalogues or archives for old readers, or leave compatibility shims. Each phase deletes the code, tests, image profiles, scripts and docs it replaces. The CLI may be incomplete between phases.
- **Rewrite ADRs, specs and plans in place** when a decision changes ([ADR-0001](../adr/0001-record-architecture-decisions.md)).
- **Keep the security checks.** Publisher identity, trust root, digest verification, catalogue freshness, ownership and unit validation, locks, and never overwriting a user's existing machine or file are not bridges.
- **Keep the repository's conventions.** `make ci` stays green. Unit tests use the fake runner and local processes, with no root or VM. Guest commands use argv arrays. Integration evidence is recorded in this plan as each phase completes.
- **Protocol version fields detect mismatched host and VM builds.** A mismatch is rejected, never negotiated.

## Start here

For a fresh session, read these in order:

1. ADR-0016 and ADR-0017: what nsl is and how it runs.
2. This plan, including the requirements table below.
3. The target contracts: the [CLI](../specs/cli.md) with its configuration file, the [agent protocol](../specs/agent.md), the [VM image](../specs/vm-image.md), [machine images](../specs/machine-images.md) and [image delivery](../specs/image-delivery.md).
4. The [lifecycle design](../design/lifecycle.md): how those pieces fit together.
5. The [experiment plan](shared-vm-experiment.md): the measured evidence, every finding and the hub image tally.

The experiment code in [`experiments/shared-vm/`](../../experiments/shared-vm/README.md) is the reference implementation. Port its behavior, not its structure.

| Experiment code | Becomes |
| --- | --- |
| `layer/`, `build.sh` | The VM role in the image composer (Phase 3) |
| `driver.py`: `vmspawn_args`, `devices`, `ready`, `start`, `stop` | VM lifecycle in `vm.go` and `state.go` (Phase 5) |
| `driver.py check` | `scripts/probe-vm.py` (Phase 3) |
| `machines.py`: `pull`, `verify_signature` | Only as evidence; machine images come from Frostyard's catalogue |
| `machines.py`: `create`, `populate` | Machine creation (Phase 7); its per-distro `BOOTSTRAP` moves into machine images (Phase 4) |
| `machines.py`: `entry(method='run')` | The agent's reference behavior (Phase 6) |
| `machines.py check` | The agent's acceptance matrix (Phase 6) |
| `workloads.py` | `scripts/probe-machines.py` (Phase 4) |
| `measure.py` | The memory and start-time regression check (Phases 7 and 10) |

Delete `experiments/shared-vm/` once Phase 7 has ported everything it holds.

Dependencies:

- Phase 1 comes first.
- Phase 2 is independent.
- Phases 3 and 4 can run beside 5 and 6.
- Phase 7 needs 4, 5 and 6. Phases 8 and 9 need 7.
- Phase 10 is last.

## Phase 1 — Contracts

- **Move the [machine CLI](../specs/cli.md) to `docs/specs/cli.md`,** replacing the environment contract, and fix every link.
- **Rewrite the guest contract.** `docs/specs/machine-images.md` becomes the machine-image contract:
  - the ADR-0017 machine layer;
  - the `/usr/lib/nsl/machine.json` descriptor;
  - capabilities and per-family adapters.

  A new `docs/specs/vm-image.md` covers the VM:
  - boot and readiness;
  - the data disk (format blank, refuse foreign signatures);
  - identity and SSH host keys off the replaceable root;
  - `/mnt/host` binds, the agent, Waypipe, nspawn version and descriptor.
- **Write `docs/specs/agent.md`** for the host-to-agent protocol:
  - a framed request: machine, argv, working directory, user or root, tty, environment allowlist;
  - stdio and PTY semantics, and exit mapping with 128+N for signal deaths;
  - errors;
  - validation of machine names and trust tiers.
- **Rewrite the [delivery contract](../specs/image-delivery.md) in place.**
  - A machine artifact type: a single-layer zstd root filesystem with a signed descriptor, package inventory, provenance and acceptance report.
  - The VM image as a disk artifact whose descriptor declares its role.
  - The catalogue schema changed directly to carry both kinds. Reset its sequence floor; there are no old readers.
- **Fix the tail.** Mark the [provisioning contract](../specs/provisioning.md) deferred until cloud-init is re-validated in machines, and bring the [lifecycle design](../design/lifecycle.md) and ADRs 0005–0014 into line with ADR-0017. Delete what no longer applies rather than annotating it.
- **Done when:** the specs describe the target system, are indexed and cross-linked, and carry the fixtures or test cases later phases implement.

**Result, 2026-09-27: complete.**

- **Moved and rewritten:** `docs/specs/cli.md` is the machine CLI. It replaces the environment contract and carries its ownership, locking, storage and archive rules explicitly. `guest-images.md` became [`machine-images.md`](../specs/machine-images.md), since "guest" no longer says whether a VM or a machine is meant.
- **New:** [`vm-image.md`](../specs/vm-image.md) and [`agent.md`](../specs/agent.md).
- **Rewritten in place:** [image delivery](../specs/image-delivery.md), the deferred [provisioning contract](../specs/provisioning.md) and its [plan](cloud-init-provisioning.md), the [lifecycle design](../design/lifecycle.md), ADRs 0005–0013, [AGENTS.md](../../AGENTS.md) and the [index](../README.md). ADR-0014 stays until Phase 3 deletes the Arch hooks it documents.
- **Links:** every relative link and anchor under `docs/`, `AGENTS.md`, `README.md` and `image/` resolves. The only unresolved links point into ignored `build/` evidence from historical reports.

Decisions the specs now fix, each of which later phases implement:

- **Agent transport.** The host logs in to the VM as `root` over vsock SSH with a key forced to `/usr/lib/nsl/nsl-agent`, and sends one base64 JSON request per session in `SSH_ORIGINAL_COMMAND`. The session's stdio carries the command's streams unframed. Agent errors exit 255 with `nsl-agent: CODE: message`.
- **Agent scope.** The agent runs commands, and also creates, starts, stops, exports, imports and removes machines. Creation therefore cleans up in the VM even when the SSH session drops. Every machine request carries the host's machine ID, and the agent refuses a mismatch.
- **Agent language.** Go, with a pinned `github.com/godbus/dbus/v5`, sharing request types and validation with the CLI.
- **Data disk.** A btrfs filesystem labelled `nsl-data` with subvolumes `machines` (`/var/lib/machines`) and `state` (`/var/lib/nsl`). The state subvolume keeps the binding, SSH host keys and the VM's machine records. nspawn settings are regenerated into `/run/systemd/nspawn` at every boot, because settings next to the image are only partly trusted.
- **Boot credential.** `nsl.vm` carries the VM ID, role, UID and GID, public key, autostart, shares and aliases.
- **Idle stop.** An idle monitor in the VM counts `nsl-run-*` units and Waypipe clients. `start` and `run` requests carry `idle_timeout`, so a changed value applies without a restart.
- **Catalogue.** Entries gain `kind`. One `vm` entry per architecture and agent protocol; machine entries keep selectors and gain `machine_protocol`. The sequence floor resets to the first catalogue carrying both kinds.
- **CLI additions.** `nsl start NAME`; `nsl update` selects the VM image (catalogue or local `--image`) for each VM's next start, which gives Phase 5 its local-VM path. `ssh-config` reaches the machine through nsl with a per-machine key and an inetd-style `sshd` in the machine; nothing listens on the network.
- **Archives.** A tar of `manifest.json` and `rootfs.tar.zst`, preserving numeric owners, xattrs (including file capabilities) and ACLs ([ADR-0006](../adr/0006-stopped-vm-backups.md)).
- **Configuration.** `idle_timeout` ranges over 0–1440 minutes. Comments are whole lines only; numbers are bare decimal digits; sections and keys appear once and are case-sensitive.

## Phase 2 — Configuration file

- `config.go` parses the `nsl.conf` INI subset into typed settings with their sources.
- Defaults come from `/proc/meminfo` and the CPU count.
- Unknown sections or keys, duplicates, bad values and out-of-range numbers fail with `file:line`.
- `$XDG_CONFIG_HOME` selects the file; tests set it.
- `nsl config` prints effective values and sources.
- **Done when:** tests cover the absent file, every key's range, defaults, comments and whitespace, and each error class.

**Result, 2026-09-27: complete.** `config.go` parses the file into typed settings, each with its source, and `nsl config` prints them. `config_test.go` covers:

- the absent file, and empty and comment-only files;
- both ends of every range and one past each;
- the memory default's rounding and its clamps to 2 and 128, and the CPU clamp to 64;
- CRLF line endings, tabs, `key=value` without spaces, `[ vm ]`, and indented headers;
- `XDG_CONFIG_HOME` absolute, relative and unset;
- each error class: syntax, unknown section, duplicate section, key outside a section, unknown key, duplicate key, invalid value, out of range, invalid UTF-8, NUL, oversize, a directory, an unreadable file and a dangling symlink.

On Snow 13 (MemTotal 58.6 GiB, 32 CPUs), the release build printed:

```text
$ XDG_CONFIG_HOME=/scratch/xdg nsl config      # nsl/nsl.conf absent
SETTING                VALUE  SOURCE
vm.memory              29     default (half of host memory)
vm.cpus                32     default (host CPUs)
machines.autostart     true   default
machines.idle_timeout  15     default
isolated.memory        2      default
isolated.cpus          2      default

$ nsl config    # after writing [vm] memory = 16G
nsl: /scratch/xdg/nsl/nsl.conf:2: vm.memory must be a whole number of GiB from 1 to 128, got "16G"
```

A file setting `memory`, `cpus` and `idle_timeout` showed each as `file (line N)`; a repeated `autostart` failed with `duplicate key machines.autostart (first on line 2)`. `make ci` passed.

Not in this phase:

- **Pending restarts** in `nsl config` need the VM record, so they arrive in Phase 5.
- **Flag overrides:** no command has a resource flag yet, so none were built.
- **Host facts:** the parser reads `/proc/meminfo` even when the file sets both `vm` values, and an unreadable one is an error.

## Phase 3 — The nsl VM image

- **Turn `experiments/shared-vm/layer` into a VM role** of `scripts/compose-image.py` on the Debian trixie profile, following the [VM image contract](../specs/vm-image.md): `systemd-container`, the `nsl-data` disk service and mounts, the boot service that writes nspawn settings, the idle monitor, and the agent once Phase 6 provides it.
- **Move state off the root.** VM identity and SSH host keys move to the data disk's `state` subvolume, so replacing the VM root keeps them and every machine. The host creates a fresh root overlay for each VM image version.
- **Delete the per-distro bootable images:** the Ubuntu, Fedora, CentOS, openSUSE and Arch profiles; the RPM, SUSE and Arch boot and kernel adapters; and `scripts/probe-distribution.py`, `probe-maintenance.py` and the other per-VM probes they replace. Keep only what the Debian VM base needs. Delete ADR-0014 with the Arch hooks, and drop its mentions from ADR-0017 and the index.
- **Acceptance** in `scripts/probe-vm.py`, from `driver.py check`:
  - readiness, data-disk formatting and refusal, and persistence across root replacement;
  - `/mnt/host` ownership, bounded guest root, and unproxied sockets.
- **Update the [validate-image skill](../../.agents/skills/validate-image/SKILL.md)** for the VM and machine images.
- **Done when:** a locally built VM image passes `probe-vm.py`, a root replacement keeps identity and machines, and the deleted profiles are gone from the tree and the docs.

## Phase 4 — Machine images

- **`image/machines/`:** compose from the pinned `mkosi-definitions` recipes without the disk profile, as a tar root filesystem repackaged as one zstd OCI layer, in the existing Lima builder with the tools trees the RPM, SUSE and Arch builds already use.
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
- **Acceptance** in `scripts/probe-machines.py`, from `workloads.py`: the 32 checks, plus tally regression checks confirming there is:
  - no usable root password;
  - no private key in a package keyring;
  - no enabled networkd or resolved;
  - no machine ID value;
  - `pam_systemd`, `sudo`, a user bus, fonts and the nesting mount present.
- **Done when:** Debian 13, Fedora 44, Arch and Tumbleweed images build locally and pass `probe-machines.py` with every tally item verified absent.

## Phase 5 — VM lifecycle in the CLI

- **Rewrite `state.go` and `vm.go`** around one VM record per state directory: ID, CID, image version, the data disk and the resources in effect. Delete the environment record, its schema checks and the environment commands.
- **Units** named `nsl-UID-vm-ID.service`, validated by description before any control.
- **VM images:** `nsl update`, from the catalogue or a local `--image`, records the image for the next start, which replaces the root overlay. This is how a locally built VM reaches the CLI.
- **Launch** keeps the device-descriptor path. It adds the data disk as an extra drive, the allowlist binds, and a read-only nsl cache share for image import, replacing the experiment's `/mnt/host` shortcut.
- **Readiness** through the agent's identity and descriptor, over the existing vsock SSH transport.
- **`stop`, `recover` and `resize --disk`** act on the VM and its data disk, under ADR-0008's locking and resumption. The VM stops when no machine runs. Configuration changes show as pending restarts in `nsl config` and `nsl list`.
- **Configuration:** `nsl config` and `nsl list` report pending restarts and a pending VM image.
- **Done when:** fake-runner tests cover launch arguments, configuration application, unit ownership, locking and data-disk growth, and the CLI starts, stops, recovers and grows a locally built VM.

## Phase 6 — Agent and command execution

- **The agent** is a Go program in this repository, installed in the VM image and reached through a forced SSH command ([agent protocol](../specs/agent.md)). It starts transient units in machines through systemd's D-Bus API and machined:
  - literal argv, without environment expansion;
  - exit status from `ExecMainCode`/`ExecMainStatus`, with 128+N for signals;
  - PTYs from machined with no title or color sequences (OSC 3008 passes);
  - PAM through the image's `nsl` service;
  - the working directory, and an environment allowlist (`TERM`, `WAYLAND_DISPLAY`, locale).
- **Host side:** bare `nsl` and `nsl run`, with device-and-inode directory translation. An untranslatable directory fails `run` and starts a shell in the guest home.
- **Delete** `guest/exec.py` and the old `shell`, `exec` and `gui` paths once the agent replaces them.
- **Done when:** the experiment's entry matrix passes through the CLI on all four machine images: argv, exit and signal status, separate and binary streams, identity, session, PTY and Ctrl-C. Latency is recorded against the experiment's 65 ms.

## Phase 7 — Machines in the CLI

- **`create`** verifies a cached machine image, imports it into a subvolume through the cache share, then applies per-machine data: account, hostname and hosts entry, the host zone link before anything runs, the `sudo` rule and nspawn settings. Settings are `PrivateUsers=no`, the VM's network, `Bind=/mnt/host`, the VM's resolver and `Timezone=off`. A failure removes the subvolume and keeps the name free.
- **`list`, `default`, `start`, `stop`, `shutdown` and `remove`** (stopped machines only).
- **Autostart and idle stop.** At VM start, the boot credential's `autostart` decides whether every machine starts. The VM's idle monitor counts `nsl-run-*` units and Waypipe clients, and uses the `idle_timeout` carried by the latest request.
- **`export` and `import`** replace `backup.go`: a tar of the subvolume (numeric owners, xattrs, ACLs) with a checksummed manifest. They keep ADR-0006's validation: no links outside the tree, no traversal, no duplicates or trailing data. The trust tier is chosen at import.
- **Remove `experiments/shared-vm/`** once everything it holds is ported.
- **Done when:** four machines are created offline from the cache and pass `probe-machines.py` through the CLI. An export and import round trip preserves packages, home and services. Four idle machines stay at or below the experiment's 950 MiB, and another machine starts at p95 ≤ 2 s.

## Phase 8 — Host integration

- **Files.** Allowlist binds, top-level alias symlinks under `/mnt/host` (such as `home → var/home`), and `nsl-path`.
- **Ports.** One forwarder for the VM, reusing `ports.go` discovery; conflicts are reported per port.
- **Desktop.** One persistent Waypipe session per machine, with its display socket attached through `machinectl bind`, and `WAYLAND_DISPLAY` in agent sessions. GUI is on by default for machines that are not isolated.
- **Broker.** `nsl-open`, authenticated per machine; it accepts only `http`/`https` URLs and translatable `/mnt/host` paths.
- **Remote editors.** `ssh-config` prints a host alias whose proxy runs `sshd -i` in the machine through the agent, with a per-machine key.
- **Agent operations.** Specify `listeners` and `display` in the [agent protocol](../specs/agent.md) before implementing them.
- **Done when:** the files, ports, translation and GUI checks pass through the CLI, and broker tests refuse every other target.

## Phase 9 — Isolated machines

- `create --isolated` and `import --isolated` give a machine its own VM from the same image, with `[isolated]` resources. It has no `/mnt/host`, broker, Waypipe or peers.
- **Done when:** an isolated machine passes the workload checks other than host integration, and tests show it has no host share, broker socket or display socket.

## Phase 10 — Publication and release

- **Rewrite `images.yml` and `scripts/publish-images.py`** to build, accept, sign and publish the VM image and the four machine images, with `probe-vm.py` and `probe-machines.py`, into the rewritten catalogue. Stop publishing per-distro disks.
- **Rebuild cadence:** at least weekly, inside ADR-0015's 30-day freshness window.
- **Rewrite the README, [AGENTS.md](../../AGENTS.md) live conventions, the [publication design](../design/image-publication.md) and the index** for the new system. Regenerate `THIRD_PARTY_NOTICES.txt` if the agent adds dependencies.
- **Tag a release** when a clean host works end to end.
- **Done when:** on a clean host with no local builds, `nsl create debian --distro debian:13` followed by `nsl` opens a shell in the current directory. `make ci`, image acceptance and the release checks all pass.

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
| Four idle machines at or below 950 MiB; an additional machine at p95 ≤ 2 s | 7, 10 |

## Later / ideas

- Size the VM to its running machines (virtio-mem or balloon targets); one machine currently costs twice a single VM.
- Keep agent sessions warm to recover the experiment's 48 ms transport overhead.
- Measure and bound virtiofsd memory under file load.
- Find the 1.3–2 s Ctrl-C delay in the SSH transport.
- Translate absolute host symlinks; handle `/run/media/USER` appearing after VM start, and host automounts.
- Launcher exports and terminal integration through OSC 3008 context markers.
- More machine images (Ubuntu, CentOS Stream, openSUSE Leap), each after acceptance.
- Re-validate cloud-init inside machines before provisioning returns.
- Private user namespaces with idmapped virtiofs mounts, if trusted machines ever need isolation from each other.

## Open questions

| Question | Default proposal | Resolve by |
| --- | --- | --- |
| Agent language and D-Bus library? | Resolved in Phase 1: Go with a pinned `godbus/dbus/v5`, sharing request types with the CLI ([agent protocol](../specs/agent.md#implementation)). | Phase 1 |
| Where do VM identity and host keys live? | Specified in Phase 1: the data disk's `state` subvolume ([VM image](../specs/vm-image.md#disks-and-state)). Phase 3 validates it across a root replacement. | Phase 3 |
| Machine archive encoding? | Resolved in Phase 1: tar with numeric owners, xattrs and ACLs rather than `btrfs send` ([ADR-0006](../adr/0006-stopped-vm-backups.md)). | Phase 1 |
| Idle-session accounting? | Specified in Phase 1: the VM's idle monitor counts `nsl-run-*` units and Waypipe clients ([VM image](../specs/vm-image.md#machines)). | Phase 7 |

## References

- Decisions: [ADR-0016](../adr/0016-wsl-style-machines.md), [ADR-0017](../adr/0017-shared-vm-and-machine-images.md), [ADR-0012](../adr/0012-signed-image-distribution.md), [ADR-0015](../adr/0015-image-verification-and-catalogue-policy.md), [ADR-0008](../adr/0008-offline-storage-management.md), [ADR-0006](../adr/0006-stopped-vm-backups.md).
- Contracts: [CLI](../specs/cli.md), [agent](../specs/agent.md), [VM image](../specs/vm-image.md), [machine images](../specs/machine-images.md), [image delivery](../specs/image-delivery.md).
- Design: [machine lifecycle](../design/lifecycle.md).
- Evidence and reference code: [shared-VM experiment](shared-vm-experiment.md), [experiment code](../../experiments/shared-vm/README.md).
- Pipeline: [image publication](../design/image-publication.md), [image build](../../image/README.md).
