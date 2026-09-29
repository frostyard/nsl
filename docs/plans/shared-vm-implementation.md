# Plan: Machines in a shared VM

**Status: complete, 2026-09-28. Images are published weekly, and v0.4.0 is the first release of this design.** This plan implements [ADR-0016](../adr/0016-wsl-style-machines.md) and [ADR-0017](../adr/0017-shared-vm-and-machine-images.md). nsl becomes WSL-style machines, running as systemd-nspawn containers in one nsl-owned VM, from signed Frostyard machine images, behind the [CLI contract](../specs/cli.md). The [shared-VM experiment](shared-vm-experiment.md) proved every mechanism in Python; this plan turns them into the Go CLI, a VM-side agent, two image pipelines and published artifacts.

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

The experiment code, `experiments/shared-vm/` at commit `2d4ff7c`, was the reference implementation. Phase 7 finished porting its behavior and deleted it:

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
| `measure.py` | `scripts/measure-machines.py`, the memory and start-time regression check (Phases 7 and 10) |

The hub-image signature check in `machines.py` stays evidence only, and `measure.py`'s one-VM-per-machine side has nothing left to measure.

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

- **Turn `experiments/shared-vm/layer` into a VM role** of `scripts/compose-image.py` on the Debian trixie profile, following the [VM image contract](../specs/vm-image.md): `systemd-container`, the `nsl-data` disk service and mounts, the boot service that writes nspawn settings, and the agent. The idle monitor arrives with Phase 7.
- **Move state off the root.** VM identity and SSH host keys move to the data disk's `state` subvolume, so replacing the VM root keeps them and every machine. The host creates a fresh root overlay for each VM image version.
- **Delete the per-distro bootable images:** the Ubuntu, Fedora, CentOS, openSUSE and Arch profiles; the RPM, SUSE and Arch boot and kernel adapters; and `scripts/probe-distribution.py`, `probe-maintenance.py` and the other per-VM probes they replace. Keep only what the Debian VM base needs. Delete ADR-0014 with the Arch hooks, and drop its mentions from ADR-0017 and the index.
- **Acceptance** in `scripts/probe-vm.py`, from `driver.py check`:
  - readiness, data-disk formatting and refusal, and persistence across root replacement;
  - `/mnt/host` ownership, bounded guest root, and unproxied sockets.
- **Update the [validate-image skill](../../.agents/skills/validate-image/SKILL.md)** for the VM and machine images.
- **Done when:** a locally built VM image passes `probe-vm.py`, a root replacement keeps identity and machines, and the deleted profiles are gone from the tree and the docs.

**Result, 2026-09-27: complete, with the Phase 5 CLI.** `image/vm/` is the VM layer on the Debian trixie recipe, and `scripts/compose-image.py --role vm` composes it with the agent built by `make agent`. The descriptor's `integration_sha256` covers the composer, the layer and the agent binary. The VM's boot services are the agent itself (`nsl-agent storage`, `setup` and `boot`), so no Python helper ships.

- **Build:** `nsl-vm-trixie-x86-64-r5`, SHA256 `69e79cb3c0db3a90f7a4830f3760e371001fa1424c9e609a1008420da9101c82`, a 2.7 GiB sparse raw disk holding 1.8 GiB (a 133 MiB UKI). r3–r5 carry the Phase 6 agent fixes, LLMNR off, and remote Unix-socket forwarding for Waypipe. It has systemd 257.13, kernel 6.12.107, OpenSSH 10.0p1, btrfs-progs 6.14 and Waypipe 0.9.2. The first build, including creating the Lima builder, took about 3.5 minutes; a rebuild took 77 s.
- **Acceptance:** `probe-vm.py` passed all nine checks on r2 and again on r5 (`nsl-vm-trixie-x86-64-r5-probe.json`). Snow 13: host kernel 7.1.8, systemd 261.2, QEMU 10.0.13, virtiofsd 1.13.2. r2's figures:

| Check | Result |
| --- | --- |
| Readiness | First boot, formatting the data disk, 8.07 s; a later boot 8.56 s. `identity.json` matches the record. |
| Formatting | `nsl-data` btrfs with `machines` and `state` subvolumes, mounted with `compress=zstd:1,noatime`. |
| Allowlist | Exactly the home, `/mnt` and the read-only image cache are virtiofs mounts; `/mnt/host/home → var/home` exists. |
| Ownership | Host files show 1000:1000; VM root's writes land as 1000:1000; VM root cannot write root-owned `/mnt`. |
| Sockets | A host Unix socket refuses the VM (`ConnectionRefusedError`); the host listener is never reached. |
| Root replacement | `recover` discards the root: a marker on the root is gone; a marker under `/var/lib/machines`, `identity.json` and the pinned host key survive. |
| Growth | After `resize --disk 160`, the filesystem reports 160 GiB. |
| Refusal | A data disk carrying a swap signature powers the VM off (7.86 s to the error); the disk is byte-identical. |
| Binding | A bound data disk under another VM's credential powers the VM off (10.87 s); the console says it does not match. |

- **Found and fixed:** without `FailureAction=poweroff`, a refused disk or a mismatched binding left the host waiting 90 s for readiness. r2 powers the VM off instead.
- **Deleted:** the `image/common`, `families` and `profiles` layers (seven profiles and their RPM, SUSE, Debian and Arch adapters), `guest/setup.py`, ADR-0014 and the Arch hook test. Also every per-VM probe: `probe-distribution`, `probe-maintenance`, `probe-backup`, `probe-storage`, `probe-files`, `probe-development`, `probe-native`, `probe-published-images` and `measure-poc`.
- **Not yet:**
  - `images.yml` and `scripts/publish-images.py` still describe the retired disks and cannot run until Phase 10 rewrites them.
  - The isolated-VM check waits for Phase 9.
  - The idle monitor arrives with Phase 7.
  - The machine-image section of the validate-image skill arrives with Phase 4.

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

**Result, 2026-09-27: complete.**

- **Composition:** `image/machines/` composes a common machine layer, a family adapter and a profile with `scripts/compose-image.py --role machine`. `scripts/build-image.sh --role machine` builds each from the recipe's container output as a zstd tar.
- **Machine layer:** one `nsl` PAM service serves all four families, using only modules every family ships. A finalize script runs after the recipes' own post-install scripts. It masks networkd and resolved, disables SSH services, removes SSH host keys, sets the machine ID to `uninitialized` and fills in the descriptor.
- **Arch adapter:** it deletes the keyring the recipe populates, private master key included, and ships `nsl-pacman-keyring.service` for first boot.

| Image | Compressed | SHA256 (prefix) | systemd | First start | Workload install |
| --- | --- | --- | --- | --- | --- |
| `nsl-machine-debian-trixie-x86-64-r2` | 96 MB | `d62559a63c66` | 257.13 | 0.91 s | 14.6 s |
| `nsl-machine-fedora-44-x86-64-r2` | 119 MB | `9e5139f6473a` | 259.9 | 1.17 s | 23.5 s |
| `nsl-machine-arch-rolling-x86-64-r3` | 212 MB | `6fc1f25b9366` | 262 | 4.16 s (keyring) | 14.5 s |
| `nsl-machine-opensuse-tumbleweed-x86-64-r2` | 86 MB | `61c4c5152efe` | 261.2 | 1.48 s | 6.1 s |

The four total 513 MB against the hub's 464 MB; they add `sudo`, PAM, zone data, fonts, cursors, Wayland client libraries and OpenSSH. Builds with a warm cache take 30–70 s each.

- **Acceptance:** `probe-machines.py` passed all 40 checks, ten per image, on VM image r5 (evidence `machines-probe-3.json`, 2026-09-28T02:12Z). They cover the system, packages, rootless Podman, `/mnt/host` files, forwarded ports, translation, a Waypipe window and persistence across a VM restart.
- **Tally:** on every image, the probe verified:
  - root has no usable password;
  - no image carries a keyring private key;
  - networkd and resolved are masked;
  - the image has no machine ID, and the four machines have distinct ones;
  - there are no SSH host keys or enabled SSH units;
  - `pam_systemd`, `sudo`, a user bus, a font and the nesting mount are present;
  - the machine resolves its own name;
  - `/etc/localtime` follows the host, and zone data reinstalls.
- **Found and fixed:**
  - **Arch hostname:** Arch's nsswitch asked resolved before `/etc/hosts` and stopped at its answer, so the machine could not resolve its own name. Its adapter now puts `files myhostname` first.
  - **Tools tree key:** Tumbleweed's tools tree needed `RepositoryKeyFetch=yes`.
  - **File modes:** overlay files carried the checkout's umask, so the composer now normalizes modes to 0755 or 0644, and the integration hash no longer depends on the umask.
- **Not yet:** `nsl-open` arrives with the broker in Phase 8. Capability declarations are checked against acceptance only when Phase 10 publishes.

## Phase 5 — VM lifecycle in the CLI

- **Rewrite `state.go` and `vm.go`** around one VM record per state directory: ID, CID, image version, the data disk and the resources in effect. Delete the environment record, its schema checks and the environment commands.
- **Units** named `nsl-UID-vm-ID.service`, validated by description before any control.
- **VM images:** `nsl update`, from the catalogue or a local `--image`, records the image for the next start, which replaces the root overlay. This is how a locally built VM reaches the CLI.
- **Launch** keeps the device-descriptor path. It adds the data disk as an extra drive, the allowlist binds, and a read-only nsl cache share for image import, replacing the experiment's `/mnt/host` shortcut.
- **Readiness** through the agent's identity and descriptor, over the existing vsock SSH transport.
- **`stop`, `recover` and `resize --disk`** act on the VM and its data disk, under ADR-0008's locking and resumption. The VM stops when no machine runs. Configuration changes show as pending restarts in `nsl config` and `nsl list`.
- **Configuration:** `nsl config` and `nsl list` report pending restarts and a pending VM image.
- **Done when:** fake-runner tests cover launch arguments, configuration application, unit ownership, locking and data-disk growth, and the CLI starts, stops, recovers and grows a locally built VM.

**Result, 2026-09-27: complete.**

- **Code:** `state.go` holds one VM record (`NSL_HOME/vm/vm.json`) and `vm.go` launches `nsl-UID-vm-ID.service` through the device-descriptor path. `storage.go` holds data-disk growth, `update` and `list`.
- **Launch:** vmspawn gets the root overlay, the data disk as an extra drive, the allowlist binds and the read-only image cache.
- **Readiness:** the agent's `identity` answer, checked for the binding and for the descriptor's agent protocol, machine protocol, transport and architecture.
- **Commands:** `update --image` caches and selects a VM image; the next start replaces the root. `recover` rebuilds the root, checks the data disk and completes growth. `resize --disk` grows the stopped data disk with the recorded-intent protocol. `config` and `list` show pending resources and images.
- **Deleted:** the environment record, schema and commands: `create`, `start`, `shell`, `exec`, `gui`, `export`, `restore`, `remove`, `ports`, `logs` and `ssh-config`. Also `backup.go`, `ports.go` (the forwarder returns in Phase 8) and `guest/exec.py`. `images` and `pull` still read the old catalogue until the delivery rework.
- **Tests:** fake-runner tests cover:
  - image selection and root replacement at the next start;
  - refused digests and arguments;
  - launch arguments, and the credential's shares, aliases and settings;
  - readiness rejecting a wrong ID, UID, role, agent or machine protocol, architecture or unknown field;
  - foreign units, loose permissions and unknown record fields;
  - a VM replaced while waiting for its lock;
  - growth that records its target before `qemu-img resize` and survives a failed resize until `recover`;
  - `recover` keeping the data disk byte-identical;
  - pending restarts in `config` and `list`.
- **Integration:** `probe-vm.py` drives this CLI: it starts (`recover`), stops (`shutdown`), recovers and grows a locally built VM, with the results in Phase 3.
- **Not yet:** `nsl update` from the catalogue waits for published VM images (Phase 10).

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

**Result, 2026-09-27: complete.**

- **Agent:** `cmd/nsl-agent` answers `identity`, `vm`, `machines`, `start`, `stop`, `run`, `create` and `remove`, with request types and validation shared through `internal/protocol`. `export` and `import` answer "not implemented" until Phase 7.
- **Host side:**
  - Bare `nsl` and `nsl run` translate the working directory by device and inode, with a home fallback for shells.
  - A PTY is used when stdin and stdout are terminals.
  - `TERM`, `COLORTERM` and locale variables are forwarded.
- **Entry matrix:** it passed on all four images through the CLI:
  - literal argv (19 hostile arguments), exit 42, and SIGTERM, INT, HUP, PIPE, KILL and SEGV as 143, 130, 129, 141, 137 and 139;
  - separate streams and 1 MiB of binary data;
  - identity, a logind session with a running user manager, and a clean PTY;
  - Ctrl-C ending a command in about 10 ms.
- **Latency:** a no-op `nsl run` takes a median of 69–92 ms and p95 of 116–133 ms, against the experiment's 65 ms for `systemd-run` over SSH and 66 ms at the CLI. It is three SSH sessions over one ControlMaster (VM identity, `start` and `run`), plus a transient unit with a PAM session. Folding readiness into the first request is the obvious saving.
- **Found and fixed in the VM:**
  - **Manager socket:** a machine's `/run/systemd/private` refuses peers from another PID namespace, so the agent uses the machine's system bus.
  - **Start job:** a queued start job leaves the unit inactive for a moment, which the first version took for an exit.
  - **PTY hangup:** reading a PTY master before the slave is opened reports a hangup, so the agent holds the slave open, through the leader's root, until the command's output is drained.
  - **LLMNR:** sshd resolves the vsock peer ("UNKNOWN") for every PTY session, and LLMNR made that take 4–12 s. The VM image turns LLMNR and mDNS off.
  - **SSH message:** the host's SSH configuration logs at `ERROR`, so sessions do not print "Shared connection closed".
- **Tests:** unit tests cover the host commands against a fake agent. That includes argv, the translated directory, root, forwarded environment, refusal outside shared trees and the shell fallback.

## Phase 7 — Machines in the CLI

- **`create`** verifies a cached machine image, imports it into a subvolume through the cache share, then applies per-machine data: account, hostname and hosts entry, the host zone link before anything runs, the `sudo` rule and nspawn settings. Settings are `PrivateUsers=no`, the VM's network, `Bind=/mnt/host`, the VM's resolver and `Timezone=off`. A failure removes the subvolume and keeps the name free.
- **`list`, `default`, `start`, `stop`, `shutdown` and `remove`** (stopped machines only).
- **Autostart and idle stop.** At VM start, the boot credential's `autostart` decides whether every machine starts. The VM's idle monitor counts `nsl-run-*` units and Waypipe clients, and uses the `idle_timeout` carried by the latest request.
- **`export` and `import`** replace `backup.go`: a tar of the subvolume (numeric owners, xattrs, ACLs) with a checksummed manifest. They keep ADR-0006's validation: no links outside the tree, no traversal, no duplicates or trailing data. The trust tier is chosen at import.
- **Remove `experiments/shared-vm/`** once everything it holds is ported.
- **Done when:** four machines are created offline from the cache and pass `probe-machines.py` through the CLI. An export and import round trip preserves packages, home and services. Four idle machines stay at or below the experiment's 950 MiB, and another machine starts at p95 ≤ 2 s.

**Result, 2026-09-28: complete.** Creation, `list`, `default`, `start`, `stop` and resumable `remove` were built with Phases 4 and 6, because their acceptance runs through the CLI. This phase added the rest.

- **Export and import.** The agent's `export` streams a stopped machine as a zstd tar through the same validator `import` uses. The host writes it after space reserved for the manifest, hashes it on the way, fills in both tar headers, and publishes by hard link. `import` checks the manifest, the checksum and the archive's end before starting the VM. The agent verifies the digest again as it receives the stream, validates every entry, requires the account with the host's UID and GID, checks the descriptor, and applies per-machine data. Archives may carry device nodes, such as rootless Podman's overlay whiteouts; machine images may not.
- **Agent locks.** Lifecycle operations on one machine now serialize in the VM, waiting 30 s before `busy`. Every request holds a shared request lock and records its time; `start` and `run` mark the machine's activity.
- **Idle stop.** `nsl-idle.service` runs `nsl-agent idle` in the VM ([VM image](../specs/vm-image.md#machines)). It stops a machine idle for `idle_timeout`, and powers the VM off 60 s after its last machine and last request. Waypipe clients are counted as connections accepted on `/run/nsl/wayland/NAME/wayland-0`, which Phase 8 serves. Only commands that enter or start a machine autostart the others; `create`, `import`, `export` and `remove` start the VM without them. A command that meets a VM powering itself off waits and starts it again.
- **Catalogue delivery.** `images`, `pull`, `update` and `create --distro` read the two-kind catalogue ([delivery](../specs/image-delivery.md)). VM images are cached as raw disks in `images/vm`, and machine images as verified `rootfs.tar.zst` in the share directory, named by the digests local images use. Tests run against a fake registry; nothing is published until Phase 10, and `catalogueMinimum` is 1 until Phase 10 sets it to the first two-kind catalogue.
- **Deleted:** `experiments/shared-vm/`. `scripts/measure-machines.py` replaces `measure.py`; the rest was ported in Phases 3–6.
- **VM image r6:** `nsl-vm-trixie-x86-64-r6`, SHA256 `edc6b0fbd74c788727927b9fd8381c32550cc5fa9efc259392cf144b99b6935e`, adds the idle monitor. `probe-vm.py` passed all nine checks (`nsl-vm-trixie-x86-64-r6-probe.json`): readiness 7.72 s on first boot and 8.08 s later; refusal 7.79 s; binding 10.76 s.
- **Machine acceptance on r6:** the four machine images were created offline from the cache and passed all 36 checks of `probe-machines.py` other than GUI, which waits for Phase 8's display session (`machines-probe-4.json`).

Export and import on r6 (`phase7-archive-idle.json`). A Debian machine got `jq`, `attr`, `acl` and `libcap2-bin`, a home file with a user xattr and an ACL entry, `cap_net_raw` on a copied binary, and an enabled service:

| Check | Result |
| --- | --- |
| Export of a stopped machine | 2.1 s, a 185 MB archive, mode 0600: `manifest.json` (4 KiB reserved) then `rootfs.tar.zst` |
| Import as `copy` | 4.6 s |
| Preserved | package, home file, xattr, ACL, file capability, enabled and active service, home owner 1000:1000, zone link, `/etc/machine-id` |
| Per-machine data | hostname `copy`; `/etc/hosts` has `127.0.1.1 copy` and no longer `debian` |
| Refused | export of a running machine (`busy`), export onto an existing file, import under a taken name, a damaged archive (no record left) |

Idle stop on r6, with `idle_timeout = 1`:

| Check | Result |
| --- | --- |
| Three idle machines | Each stopped 66.6 s after starting: the timeout plus the 10 s poll |
| Idle VM | Powered off 72 s after the last request |
| Next command | `nsl run -m debian true` started the VM and machine in 7.7 s |
| A 90 s command | Kept its machine running past the timeout |

Measurements on r6 with `scripts/measure-machines.py` (`measure-machines-1.json`): Snow 13, 32 CPUs, 60 GiB, KSM off, THP `always`; the VM had 4 vCPUs and 8 GiB, as in the experiment. Machines were fresh images without the experiment's workload packages.

| Measurement | Result | Experiment |
| --- | --- | --- |
| Idle, 1 machine | 649 MiB | 723 MiB |
| Idle, 2 machines | 713 MiB | 802 MiB |
| Idle, 4 machines | **853 MiB** (limit 950) | 950 MiB |
| Cold start of the first machine, VM included, median / p95 | 7.69 / 8.02 s | 7.97 / 8.33 s |
| Start of an additional machine, median / p95 | 0.53 / **0.67 s** (limit 2 s) | 0.63 / 0.84 s |
| No-op `nsl run`, median / p95 | 89 / 111 ms | 66 ms |

QEMU is nearly all of the PSS; virtiofsd held 4.4 MiB and vmspawn 3 MiB while idle.

## Phase 8 — Host integration

- **Files.** Allowlist binds, top-level alias symlinks under `/mnt/host` (such as `home → var/home`), and `nsl-path`.
- **Ports.** One forwarder for the VM, reusing `ports.go` discovery; conflicts are reported per port.
- **Desktop.** One persistent Waypipe session per machine, with its display socket attached through `machinectl bind`, and `WAYLAND_DISPLAY` in agent sessions. GUI is on by default for machines that are not isolated. The Phase 4 probe proved the mechanism by hand: the host's `waypipe client` socket reaches the VM through `ssh -R`, and `waypipe server` runs as VM root. Its display socket must then be handed to the host UID before the machine account can connect.
- **Broker.** `nsl-open`, authenticated per machine; it accepts only `http`/`https` URLs and translatable `/mnt/host` paths.
- **Remote editors.** `ssh-config` prints a host alias whose proxy runs `sshd -i` in the machine through the agent, with a per-machine key.
- **Agent operations.** Specify `listeners` and `display` in the [agent protocol](../specs/agent.md) before implementing them.
- **Done when:** the files, ports, translation and GUI checks pass through the CLI, and broker tests refuse every other target.

**Result, 2026-09-28: complete.**

- **Agent operations**, specified first in the [agent protocol](../specs/agent.md):
  - `listeners` reads the VM's TCP tables and attributes listening sockets to machines through their processes' control groups;
  - `display` serves a machine's desktop session: it runs `waypipe server` on `/run/nsl/desktop/NAME/wayland-0`, hands it and the broker socket to the account, binds the directory into the machine at `/run/nsl/desktop`, and reports `ready`;
  - `ssh` runs `sshd -i` in the machine with its own host key and the host's per-machine key.

  `identity`, `machines`, `listeners` and `display` are passive, so watching ports or holding a desktop open does not keep an idle VM running. `run` sessions get `WAYLAND_DISPLAY` and `BROWSER=nsl-open` while the desktop session lasts. `start` binds the desktop directory whenever a machine starts, unless the machine already sees it.
- **Ports.** `nsl-UID-vm-ID-ports.service`, bound to the VM's unit, polls `listeners` every second on its own SSH connection. It forwards IPv4 and wildcard listeners to VM `127.0.0.1`, and `::1`-only listeners, such as dev servers bound to `localhost`, to `[::1]`; the VM key now permits both. `nsl ports [NAME]` reads its report. It replaces the prototype's `ss` parsing on the host.
- **Desktop.** `nsl-UID-vm-ID-desktop-NAME.service`, started when a command starts a shared machine from a Wayland session, runs the host's `waypipe client` and the broker and holds `display` open. It is a notify unit: starting it waits for `ready`, so even the first command after a cold start gets the display, at about 0.2 s. A session killed abruptly takes its sockets with it, and the unit restarts after a failure.
- **Broker.** `nsl-open` in the machine layer is a shell script calling `varlinkctl`, which all four images ship, against the Varlink method `io.frostyard.nsl.Broker.Open`. It is also the machines' `http`/`https` handler. The host accepts only `http`/`https` URLs with a host, and `/mnt/host` paths whose host paths, symlinks resolved, lie in a shared tree; it runs `xdg-open` (or `NSL_OPENER`) with the target as argv.
- **Remote editors.** `nsl ssh-config NAME` prints `Host nsl-NAME` with a per-machine key and pinned host key in `NSL_HOME/machines/NAME.ssh/`, and a proxy command, `nsl _ssh NAME`. SSH's own TCP forwarding works through it, as VS Code Remote needs.
- **Logs.** `nsl logs [NAME]` shows the journal of the VM, forwarder and desktop units.
- **Latency.** Checking the helper units on every command added two `systemctl` calls, about 20 ms. A fresh forwarder report or an answering broker now shows a live helper without asking systemd, and the no-op median is back to 69 ms (`machines-probe-7.json`).
- **Images:** VM image r8 (`nsl-vm-trixie-x86-64-r8`, SHA256 `8653578b2896c93bea66e2835edeb8929f43d31ced55372708f8c215f06fa884`); r7 was superseded by the readiness report before acceptance. Machine images Debian r3, Fedora r3, Arch r4 and Tumbleweed r3 add `nsl-open`, its desktop entry and `/etc/xdg/mimeapps.list`. `probe-vm.py` passed all nine checks on r8.

Acceptance on r8 with `--gui` (`machines-probe-5.json`, 2026-09-28): every check passed on all four machines except the new `ssh` check, whose own command used `hostname`, which minimal Fedora, Arch and Tumbleweed lack. The SSH connections themselves worked. With the probe fixed, `--only ssh` passed on all four (`machines-probe-6.json`).

| Check | Result on each of the four machines |
| --- | --- |
| Ports | A `0.0.0.0` and a `::1` server forwarded within 0.5–1.6 s and answered 200 on host `127.0.0.1`. A port the host already used showed `conflict`, and the host's listener kept its connections. Forwards went away when the servers stopped. |
| Translation | Unchanged from Phase 6. |
| GUI | `WAYLAND_DISPLAY=/run/nsl/desktop/wayland-0` from the agent, 33 Wayland interfaces, and a window for 3 s (galculator; foot on Tumbleweed) |
| Broker | `BROWSER=nsl-open`. Opened: an `https` URL with quotes, an absolute and a relative `/mnt/host` path, and a `file://` URL. Refused, with nothing opened: `ftp://`, `/etc/hostname` and a shared symlink to `/etc/hostname`. |
| SSH | `ssh -F <(nsl ssh-config NAME) nsl-NAME` logged in as the account twice with a pinned host key; nothing listens on TCP 22 in the VM. |

The hand check before the probe also covered what it does not: SSH port forwarding through the connection, and a killed desktop session leaving no stale display until the unit restarts it.

**Fixes after release, 2026-09-28: VM image r9, Tumbleweed r5 and Leap r2.**

- **Electron and Chromium.** VS Code exited with `Missing X server or $DISPLAY` in an Ubuntu machine, although galculator opened. Chromium, and so Electron, and Qt choose Wayland by `XDG_SESSION_TYPE`, and nsl's command sessions were `unspecified`; machines have no X server. The agent now adds `XDG_SESSION_TYPE=wayland` while the desktop session lasts. Alone, that made pam_systemd register a `user`-class session instead of `background`, so the agent also sets `XDG_SESSION_CLASS=background`, and logind accepts the pair. With r9, plain `code` from VS Code 1.139.1 opened its window on Wayland with no flags. The GUI check now requires a `wayland`, `background` logind session.
- **Locales.** foot in an openSUSE machine failed with `setlocale() failed … invalid locale, and failed to find a fallback`. The host's `LANG=en_US.UTF-8` reached machines that lack it: the openSUSE images had no compiled locale at all, and Debian, Ubuntu, CentOS and Arch had only `C.UTF-8`, so perl and `locale` warned there too. Fedora alone ships `en_US.UTF-8`. Every image now has `C.UTF-8`; the suse family adds `glibc-locale-base`, which also brings `en_US.UTF-8`. The agent keeps a host locale variable only when the machine has that locale, compiled under `/usr/lib/locale` or named in `locale-archive`, whose name table it reads without loading locale data. Otherwise `LANG` becomes `C.UTF-8` and an `LC_*` variable is dropped. The archive reader was checked against an archive built with `localedef` in a Debian machine, including an ISO-8859-1 locale and a `@latin` modifier. The tally's new locale check passes on every image.
- **Acceptance on r9** (`machines-probe-r9.json`, `--gui --isolated`): 92 checks on seven shared machines and an isolated one, none failing; `measure-r9.json`: four idle machines at 837 MiB and an additional machine at p95 0.67 s.
- **Publication** ([run 36439364451](https://github.com/frostyard/nsl/actions/runs/36439364451), from `7be952b`): every check passed on the runner, including a `wayland`/`background` session on all seven desktops, and four idle machines measured 854 MiB. It promoted catalogue sequence **9**, manifest `sha256:7e84c3ed73103e925d4cb87e37bdc3497b520c8316b385b244037eda28851263`, which expires 2026-10-28. Sequence 8 belonged to the week's scheduled run: it fired at 11:45 UTC instead of 05:23, waited for a runner, and was cancelled before building because it would have published the previous commit.
- **Clean host.** The released v0.4.0 CLI, with an empty state directory and `LANG=en_US.UTF-8`, created an Ubuntu machine from catalogue 9: its sessions were `wayland`/`background` with `LANG=C.UTF-8` and no locale warnings, and plain `code` opened its window on Wayland. A Tumbleweed machine kept `en_US.UTF-8`, and foot started; libxkbcommon still logs that openSUSE's foot lacks X11 compose data for the locale, which only affects compose sequences.

### Request lifetime fix (issue #24), 2026-09-28

The request handler now retains the shared request-lock file until the operation returns, and closes it on both success and failure. Previously the file became unreachable after `holdRequest`, so garbage collection could release the lock and let the idle monitor power off the VM during an active operation. The regression forces collection inside a real request handler and checks both the held lock and its release; it fails against the previous implementation.

VM image `nsl-vm-trixie-x86-64-r10`, raw SHA256 `0974e722530a090f1e05e351e03b926314794c67be5e93f2db9fe336bf3a723e`, passed all nine VM acceptance checks (`build/image/evidence/r10-probe.json`). The build log is `build/image/evidence/build-r10.log`; the image carries systemd 257.13 and kernel 6.12.107+deb13-amd64.

Acceptance host: 7.1.8+deb13-amd64, systemd 261 (261.2-1), QEMU emulator version 10.0.13 (Debian 1:10.0.13+ds-0+deb13u1), virtiofsd 1.13.2.

The four-machine benchmark also passed (`build/image/evidence/r10-measure.json`): 852.3 MiB idle and 0.674 s p95 to start another machine, using 20 startup trials and 50 command-latency trials. `make ci` passed, including race tests and cross builds.

## Phase 9 — Isolated machines

- `create --isolated` and `import --isolated` give a machine its own VM from the same image, with `[isolated]` resources. It has no `/mnt/host`, broker, Waypipe or peers.
- **Done when:** an isolated machine passes the workload checks other than host integration, and tests show it has no host share, broker socket or display socket.

**Result, 2026-09-28: complete.**

- **VM records.** `state.go` holds any number of VMs: the shared VM in `NSL_HOME/vm`, and each isolated machine's in `NSL_HOME/isolated/NAME`, with the `isolated` role and the machine's name and ID in its record, credential and binding. A new VM gets a vsock CID no other VM uses. The internal launch commands take the VM's ID.
- **Isolated machines.** `create --isolated` and `import --isolated` create the machine's VM from the VM image selected for the shared VM, with `[isolated]` resources and without autostart. Its credential names no shares, so vmspawn binds none; the machine-image cache is its only virtiofs mount. It gets no desktop session, and its working directory never translates. A failed creation deletes the VM, and `remove` stops the VM and deletes its directory after renaming it, so an interrupted removal resumes. The agent needed no change: it already enforced the isolated role.
- **VM-wide commands** cover every VM: `update` selects the image for all of them, `shutdown` stops all, `list` and `config` show each VM and its pending restarts, and `ports` and `logs` include each VM's forwarder and units. `recover NAME` and `resize NAME --disk` address an isolated machine's VM.
- **Tests:** the isolated VM's record, credential, launch arguments, resources and readiness; refusal of translation and desktop; every VM covered by `list`, `update`, `shutdown`, `resize` and `recover`; removal refusing a running machine and then deleting the VM; and a failed creation leaving no VM.

Acceptance on VM image r8 (`machines-probe-8.json`): `probe-machines.py --isolated` added an isolated Debian machine beside the four shared ones, and all 52 checks passed. The isolated machine passed the entry matrix, system, tally, packages, Podman and SSH checks, persistence across its VM's restart, and the isolation check:

| Check | Result |
| --- | --- |
| Host files | Credential role `isolated` with no shares; the VM's only virtiofs mount is `/var/cache/nsl/images`; `/mnt/host` is empty in the machine. |
| Desktop and broker | No `WAYLAND_DISPLAY` or `BROWSER`, and `nsl-open` fails. |
| Translation | `nsl run` from a host directory is refused and names the machine. |
| Ports | A server in the isolated machine was forwarded by its VM's forwarder and answered 200 on host `127.0.0.1`. |

Creating it, including its VM's first boot and data-disk formatting, took 10.5 s; its no-op command median was 81 ms.

## Phase 10 — Publication and release

- **Rewrite `images.yml` and `scripts/publish-images.py`** to build, accept, sign and publish the VM image and the four machine images, with `probe-vm.py` and `probe-machines.py`, into the rewritten catalogue. Stop publishing per-distro disks.
- **Rebuild cadence:** at least weekly, inside ADR-0015's 30-day freshness window.
- **Rewrite the README, [AGENTS.md](../../AGENTS.md) live conventions, the [publication design](../design/image-publication.md) and the index** for the new system. Regenerate `THIRD_PARTY_NOTICES.txt` if the agent adds dependencies.
- **Tag a release** when a clean host works end to end.
- **Done when:** on a clean host with no local builds, `nsl create debian --distro debian:13` followed by `nsl` opens a shell in the current directory. `make ci`, image acceptance and the release checks all pass.

**Result, 2026-09-28: complete; v0.4.0 is the release.**

- **Publisher.** `scripts/publish-images.py build` runs `make ci`, builds the VM image and the four machine images, and accepts them with `probe-vm.py`, `probe-machines.py --gui --isolated` and `measure-machines.py`. It then writes a public directory per image. Acceptance reports list only check names and results, protocols and a few timings, and are refused unless every required check passed, none was skipped, and they tested the exact payload. `publish` signs and pushes each image with its kind's artifact type and promotes a catalogue with one `vm` entry and one `machine` entry per profile. `refresh` refuses the old disk catalogue, which only a publication replaces. `scripts/zstd-image.go` replaces `compress-image.go`: it compresses VM disks and measures a machine image's root filesystem as the client decodes it.
- **Workflow.** `images.yml` also runs weekly, well inside the 30-day expiry. The runner must provide a Wayland compositor for the GUI checks; the build refuses to run without one.
- **Checked locally:** the publisher's unit tests cover explicit reports, refusal of failed, skipped, missing or mismatched checks, the measurement gate, entries by kind, a complete matrix, and refresh and withdrawal history. `prepare()` ran on the real r8 VM disk and the Debian r3 machine image, and the CLI's own validation accepted both descriptors and decompressed both payloads to their recorded digests. The VM disk compresses from 2.9 GB to 488 MB.
- **Docs.** The [publication design](../design/image-publication.md), [delivery contract](../specs/image-delivery.md), README, AGENTS.md and the index describe the finished system. `THIRD_PARTY_NOTICES.txt` is current; `make ci` checks it.
- **First publication run** ([run 5](https://github.com/frostyard/nsl/actions/runs/36376347934)): every acceptance check passed on the runner, including GUI and the isolated machine, but four idle machines measured 984 MiB against the 950 MiB budget. The runner had a Wayland session, so every machine also had a desktop session. Locally, the same images measured 853 MiB without desktop sessions and 972 MiB with them. The VM's Waypipe links 102 libraries, including ffmpeg and the AV1 codecs, and the first session pulls about 100 MiB of them into guest page cache; later sessions add about 6 MiB each. The experiment that set the budget ran no desktop sessions, so `measure-machines.py` now gates that configuration and records four idle machines with desktop sessions, and the difference, ungated: 844 and 992 MiB, a 149 MiB overhead, locally.
- **Publication** ([run 6](https://github.com/frostyard/nsl/actions/runs/36400743847), 22 minutes on an ephemeral runner in the maintainer's session): the VM image r8 and the four machine images passed every check on KVM. That covered `probe-vm.py`, `probe-machines.py` with GUI windows and an isolated machine, and `measure-machines.py`: four idle machines at 862 MiB, an additional machine at p95 0.55 s, and 142 MiB more for four desktop sessions. It signed and pushed each image and promoted catalogue sequence **6**, `sha256:856daefa740a2d89d797b61936e74ba9f73b6602971117dc06bbcdeff6412c7b`, which expires 2026-10-28. The `nsl-images` package was already public, and the catalogue answered anonymously. No scheduled run fired on the day the schedule landed.
- **Catalogue floor.** `catalogueMinimum` is 6, so the CLI refuses the disk catalogues, sequences 3 and 4.
- **Clean host.** With a new state directory, an empty cache and a binary with the new floor, `nsl images` showed the five signed images, verified against the embedded Sigstore root and the workflow identity. `nsl create debian --distro debian:13` downloaded and verified both images and created the machine in 29 s, including the VM's first boot. Bare `nsl` then opened bash in `/mnt/host/var/home/bjk/…`, the translated current directory. A second machine from the cache with `--offline` took 3 s, and an uncached image was refused offline.
- **Publication runner and memory budget** (2026-09-28, [ADR-0019](../adr/0019-persistent-publication-runner.md)). The first runs on the `nsl-builder` VM, a Debian 14 Incus guest on the maintainer's lab host with QEMU 11.1.1, built every image and passed every functional check once its compositor advertised a `wl_seat`: [run 36479109292](https://github.com/frostyard/nsl/actions/runs/36479109292) passed all 92 acceptance checks. Its start-time gate passed at p95 0.82 s for an additional machine. Four idle machines measured 1,047 MiB against the 950 MiB budget. `measure-machines.py` on the builder with catalogue 9's published images, which measured 854 MiB on the maintainer's workstation, gave 974 MiB (688.7, 818.4 and 974.1 MiB for one, two and four machines, nearly all QEMU PSS), an additional machine at p95 0.78 s and a no-op command at a 102 ms median. The builder adds about 120 MiB, and 950 MiB was the experiment's single measurement adopted without headroom, so the budget is now 1,200 MiB.
- **Publication on the builder** ([run 36498216989](https://github.com/frostyard/nsl/actions/runs/36498216989), from `1a1d052`): all 92 acceptance checks passed, GUI included. Four idle machines measured 1,005.5 MiB against the 1,200 MiB budget (987.7 MiB with desktop sessions), an additional machine started at p95 0.79 s, a cold first machine with the VM's boot at a 17 s median, and a no-op command at 103 ms. It promoted catalogue sequence **12**, manifest `sha256:ad709216edc9eee8153d01226f7a09e26f539c7fbf27b8ae996d317768f699b6`, which expires 2026-10-29. A fresh client with `main`'s CLI and an empty state directory verified catalogue 12 anonymously and listed the VM image and all seven machine images.

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
| Four idle machines at or below 1,200 MiB (950 until 2026-09-28; see Phase 10); an additional machine at p95 ≤ 2 s | 7, 10 |

## Later / ideas

- Size the VM to its running machines (virtio-mem or balloon targets); one machine currently costs twice a single VM.
- Trim the desktop's memory: the first desktop session costs about 100 MiB, mostly ffmpeg and codec libraries that `waypipe --no-gpu` never uses. A Waypipe built without video support, or free-page reporting to the host, would recover it.
- Keep agent sessions warm to recover the experiment's 48 ms transport overhead.
- Fold readiness and `start` into the first request: each command now makes three SSH round trips (`identity`, `start`, `run`), which costs most of the 89 ms no-op median.
- Measure and bound virtiofsd memory under file load.
- Find the 1.3–2 s Ctrl-C delay in the SSH transport.
- Translate absolute host symlinks; handle `/run/media/USER` appearing after VM start, and host automounts.
- Launcher exports and terminal integration through OSC 3008 context markers.
- More machine images: Ubuntu, CentOS Stream and openSUSE Leap are [their own plan](more-machine-images.md).
- Re-validate cloud-init inside machines before provisioning returns.
- Private user namespaces with idmapped virtiofs mounts, if trusted machines ever need isolation from each other.

## Open questions

| Question | Default proposal | Resolve by |
| --- | --- | --- |
| Agent language and D-Bus library? | Resolved in Phase 1: Go with a pinned `godbus/dbus/v5`, sharing request types with the CLI ([agent protocol](../specs/agent.md#implementation)). Built in Phase 6. | Phase 1 |
| Where do VM identity and host keys live? | Specified in Phase 1: the data disk's `state` subvolume ([VM image](../specs/vm-image.md#disks-and-state)). Phase 3 validates it across a root replacement. | Phase 3 |
| Machine archive encoding? | Resolved in Phase 1: tar with numeric owners, xattrs and ACLs rather than `btrfs send` ([ADR-0006](../adr/0006-stopped-vm-backups.md)). | Phase 1 |
| Idle-session accounting? | Resolved in Phase 7: the VM's idle monitor counts `nsl-run-*` units that have not exited and connections to the machine's Waypipe display socket, and takes the latest `start` or `run` as activity ([VM image](../specs/vm-image.md#machines)). | Phase 7 |

## References

- Decisions: [ADR-0016](../adr/0016-wsl-style-machines.md), [ADR-0017](../adr/0017-shared-vm-and-machine-images.md), [ADR-0012](../adr/0012-signed-image-distribution.md), [ADR-0015](../adr/0015-image-verification-and-catalogue-policy.md), [ADR-0008](../adr/0008-offline-storage-management.md), [ADR-0006](../adr/0006-stopped-vm-backups.md).
- Contracts: [CLI](../specs/cli.md), [agent](../specs/agent.md), [VM image](../specs/vm-image.md), [machine images](../specs/machine-images.md), [image delivery](../specs/image-delivery.md).
- Design: [machine lifecycle](../design/lifecycle.md).
- Evidence: [shared-VM experiment](shared-vm-experiment.md); its code is at commit `2d4ff7c`.
- Pipeline: [image publication](../design/image-publication.md), [image build](../../image/README.md).
