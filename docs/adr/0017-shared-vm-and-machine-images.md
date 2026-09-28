# 0017 — Run machines as containers in one shared VM, from Frostyard machine images

- **Status:** Accepted
- **Date:** 2026-09-27

## Context

[ADR-0016](0016-wsl-style-machines.md) made nsl a set of WSL-style machines trusted as the user, and left the topology to the [shared-VM experiment](../plans/shared-vm-experiment.md). The experiment compared [ADR-0005](0005-vmspawn-and-nspawn-images.md)'s one VM per machine with WSL2's shape: one VM whose distributions are containers.

Measured on Snow 13, with an 8 GiB guest-memory ceiling on both sides:

- Four idle machines used 950 MiB in one shared VM, against 2,328 MiB for four VMs (41%). After one build in each machine, 2,612 against 5,586 MiB.
- An additional machine started at p95 0.84 s, against 7.79 s for another VM.
- A single machine cost more: 723 MiB against 355 MiB, breaking even at two. The first cold start was 0.9 s slower (7.97 against 7.05 s median). Commands paid about 48 ms more transport.
- Debian 13, Fedora 44, Arch and Tumbleweed machines passed 32 of 32 workload checks in one VM. These covered rootless Podman, `/mnt/host` files, forwarded services, directory translation, Waypipe windows and restart persistence.
- Machine root filesystems downloaded at 464 MB for the four distros, against 2,336 MB of bootable disks.

Per-VM images need a bootloader, UKI, root-growth and kernel adapter per distro ([ADR-0007](0007-maintainable-guest-boot.md), [ADR-0010](0010-explicit-guest-root-growth.md), [ADR-0014](0014-arch-kernel-maintenance.md)).

The hub.nspawn.org images worked, but only after 13 creation-time workarounds recorded in the experiment's image tally. Three stand out:

- They ship a root password of `root`.
- Arch ships its pacman master private key.
- Their signing identity and tag policy do not meet [ADR-0015](0015-image-verification-and-catalogue-policy.md).

The project has no users; nothing needs migration.

## Decision

**Topology.** Machines are systemd-nspawn containers in one nsl-owned VM per state directory, launched through systemd-vmspawn as today.

- The VM root is an nsl image that holds no user state. Machines live on a separate btrfs data disk at `/var/lib/machines`, one subvolume each.
- Machines run with `PrivateUsers=no`, in the VM's network namespace with its resolver, and bind the VM's `/mnt/host`.
- Machine root is effectively VM root, so machines are not isolated from one another. The VM keeps the host OS out of reach, as ADR-0016 intends.

**Isolated machines.** `--isolated` runs a machine in its own VM from the same image and launcher. That VM has no host shares, broker, desktop session or other machines. This is the only other topology, and it reuses the same image, launcher and agent.

**The VM image.** The VM image is built from the Debian trixie profile plus `systemd-container` and the machine-storage layer the experiment proved.

- nsl replaces the VM root as a unit to deliver kernel and userspace updates, at the next VM start. The data disk and machines are untouched.
- The VM's identity and SSH host keys move off the replaceable root, onto the data disk or into credentials.

**Machine images.** Frostyard publishes signed machine images: single-layer zstd OCI root filesystems built from the pinned `nspawn/mkosi-definitions` recipes without the disk profile, plus an nsl machine layer.

- They go through the existing publication workflow and catalogue. [ADR-0012](0012-signed-image-distribution.md) and ADR-0015 apply unchanged, including workflow identity, freshness and rollback.
- The initial catalogue is the four tested distros; others join after acceptance.
- The machine layer supplies:
  - no root password;
  - an uninitialized machine ID;
  - no shipped package keyring with private keys; they are generated on first boot;
  - networkd and resolved disabled;
  - `pam_systemd`, a user D-Bus session, `sudo`, shadow tools, zone data, fonts and a cursor theme;
  - one `nsl` PAM service;
  - the nesting procfs mount enabled by preset, and the Podman `containers.conf` drop-in;
  - an image descriptor with protocol and capabilities.
- Creation needs no network. It verifies a cached image, imports it, and applies only per-machine data: the account (host username, UID and primary GID), hostname and hosts entry, the host time-zone link, the `sudo` rule and nspawn settings.
- hub.nspawn.org remains the upstream reference. Using its images directly would need its own trust decision.

**Execution.** A VM-side nsl agent runs each command as a transient unit in the target machine through systemd's D-Bus API and machined.

- It passes argv literally, without environment expansion.
- It reports signal deaths from `ExecMainCode`/`ExecMainStatus` as 128+N.
- It forwards PTYs without terminal-title or color sequences; OSC 3008 context markers may pass.
- It opens a PAM session through the image's `nsl` service.
- Until the agent exists, `systemd-run --machine` with the experiment's adjustments is the reference behavior. `nsenter` and `machinectl shell` are not entry paths.
- The host reaches the VM over the existing vsock SSH transport ([ADR-0011](0011-image-profiles-and-portable-vsock.md)).

**Desktop.** Each machine gets one persistent Waypipe session. Its display socket is created in the VM and bind-mounted into the running machine. ADR-0016's GUI default applies.

**Lifecycle and autostart.**

- The VM starts on first use.
- By default, every machine starts with the VM, through `machines.target`. Setting `autostart = false` in the configuration file makes machines start on first use instead.
- ADR-0016's per-machine idle stop still applies, and the VM stops when no machine is running.

**Configuration file.** One user-owned file, `$XDG_CONFIG_HOME/nsl/nsl.conf` (default `~/.config/nsl/nsl.conf`), holds settings, kept separate from state in `NSL_HOME`.

- The format is INI, in the style of `.wslconfig` and systemd units: `[section]`, `key = value`, `#` comments. It is parsed without new dependencies.
- It starts with:
  - `[vm]` `memory` and `cpus` for the shared VM;
  - `[machines]` `autostart` (default `true`) and `idle_timeout` (minutes, default 15, `0` disables);
  - `[isolated]` `memory` and `cpus` for isolated machines' VMs.
- Resource defaults follow WSL: half the host's memory and every host CPU, within vmspawn's limits.
- An absent file means defaults. Unknown sections or keys, duplicates and invalid values are errors, and nsl refuses to guess.
- A command-line flag overrides the file for one invocation where one exists.
- VM resource changes apply at the next VM start, and nsl reports the pending restart. nsl reads the file but never rewrites it.

**Storage, backup and recovery.**

- The data disk grows offline and never shrinks, under [ADR-0008](0008-offline-storage-management.md)'s rules for locking and resumption. Machines share its capacity; per-machine quotas are later work.
- `remove` deletes a stopped machine's subvolume and settings.
- A stopped machine exports as a versioned archive of its root filesystem, preserving numeric owners, xattrs and ACLs, with a checksummed manifest. Import restores it into a new subvolume under an unused name. The trust tier is chosen at import (ADR-0016).
- The VM holds no user state and is not exported. `recover` applies to the VM.

**Retired and narrowed decisions.**

- Per-distro bootable VM images and ADR-0014 are retired.
- ADR-0007 and ADR-0010 narrow to the nsl VM image.
- [ADR-0009](0009-distribution-neutral-guest-contract.md)'s guest contract becomes a machine-image contract.
- ADR-0011's profiles become machine-image profiles; its vsock transport stays, for the VM.
- [ADR-0006](0006-stopped-vm-backups.md)'s qcow2 archive gives way to machine archives.
- This supersedes ADR-0005's one VM per environment and per-distro bootable images.

The environment CLI, its state and the per-distro images are replaced in place by the [machine CLI](../specs/cli.md), with no compatibility, migration or transition period. The [implementation plan](../plans/shared-vm-implementation.md) phases the work.

## Consequences

- **Several machines become cheap.** An idle machine costs about 75 MiB instead of 440–770 MiB, starts in under a second, and downloads at about a fifth of a disk's size.
- **One machine costs more.** It uses twice the memory, starts 0.9 s slower and pays about 48 ms more per command. Follow-up work: size the VM to its running machines (virtio-mem or balloon targets), and keep agent sessions warm.
- **nsl ships and updates a kernel.** The VM image needs a security release cadence, like WSL's kernel.
- **Machines lose their distro's kernel and MAC policy.** A distro's own policy, such as Fedora's enforcing SELinux, does not apply inside machines; the VM kernel's security modules govern them. All machines share one kernel failure domain. This is documented plainly.
- **Frostyard owns more.** It owns a machine image pipeline, an acceptance suite (the experiment's workload checks), catalogue entries and a rebuild cadence at least weekly for distro security updates. It no longer maintains per-distro boot and kernel adapters.
- **Provisioning must be re-validated.** [ADR-0013](0013-optional-cloud-init-provisioning.md) needs cloud-init re-tested inside machines before provisioning ships.
- **Specs change.** The [CLI contract](../specs/cli.md) gains the configuration file. The guest contract splits into the [VM image](../specs/vm-image.md) and [machine-image](../specs/machine-images.md) contracts, and the [agent protocol](../specs/agent.md) is new. [Image delivery](../specs/image-delivery.md) gains machine artifacts. The [lifecycle design](../design/lifecycle.md) and [AGENTS.md](../../AGENTS.md) change with the implementation.
- **Follow-ups from the experiment:**
  - virtiofsd memory under file load (up to 1.3 GiB);
  - the 1.3–2 s Ctrl-C delay in the SSH transport;
  - absolute host symlinks;
  - a dedicated nsl share for image import, instead of `/mnt/host`.

## Alternatives considered

- **Keep one VM per machine:** keeps distro kernels and MAC policy and isolates machines from each other. It costs 2.4 times the idle memory at four machines, 7.6 s per additional machine, five times the downloads, and a boot adapter per distro.
- **Private user namespaces per machine:** would contain machines from one another and from VM root. UID shifting breaks `/mnt/host` ownership without idmapped virtiofs mounts, which are unproven here, and complicates nesting. Revisit if isolation between trusted machines is needed.
- **Consume hub.nspawn.org images directly:** works with 13 creation-time workarounds. It fails ADR-0015, needs network at creation, and inherits unsafe defaults.
- **Isolated machines as peers in the shared VM:** machine root is VM root, and the VM mounts host files.
- **Incus/LXC as the container manager:** capable, but adds a daemon with its own image and network model. nspawn and machined already integrate with the systemd tools nsl uses.
- **Settings through flags, `nsl set`, JSON or TOML:** flags do not persist, and a command that rewrites a file loses comments. JSON has no comments, and TOML needs a parser dependency.

## References

- Evidence: [shared-VM experiment](../plans/shared-vm-experiment.md), including the image tally; [experiment code](../../experiments/shared-vm/README.md).
- Interface: [CLI contract](../specs/cli.md), [agent](../specs/agent.md), [VM image](../specs/vm-image.md), [machine images](../specs/machine-images.md). Identity and trust: [ADR-0016](0016-wsl-style-machines.md).
- Superseded or narrowed: [ADR-0005](0005-vmspawn-and-nspawn-images.md), [ADR-0006](0006-stopped-vm-backups.md), [ADR-0007](0007-maintainable-guest-boot.md), [ADR-0008](0008-offline-storage-management.md), [ADR-0009](0009-distribution-neutral-guest-contract.md), [ADR-0010](0010-explicit-guest-root-growth.md), [ADR-0011](0011-image-profiles-and-portable-vsock.md), [ADR-0014](0014-arch-kernel-maintenance.md).
- Unchanged and extended: [ADR-0012](0012-signed-image-distribution.md), [ADR-0013](0013-optional-cloud-init-provisioning.md), [ADR-0015](0015-image-verification-and-catalogue-policy.md).
- [WSL configuration](https://learn.microsoft.com/en-us/windows/wsl/wsl-config), [systemd-nspawn settings](https://www.freedesktop.org/software/systemd/man/latest/systemd.nspawn.html).
