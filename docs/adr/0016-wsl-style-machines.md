# 0016 — Model nsl as WSL-style machines trusted as the user

- **Status:** Accepted
- **Date:** 2026-09-27

## Context

The implemented CLI mixes three product models under one noun, the "environment":

- **Persistent distro VM (WSL):** independent disk, native package manager, export/restore, localhost forwarding.
- **Project binding (devcontainer):** `--project` fixed at creation and mounted at `/work`; restore re-selects the project.
- **Capability sandbox (Flatpak-like):** `--desktop` opt-in, a separate `gui` verb, a fixed `nsl` account and no host home.

Creation therefore asks which distro, which project and which grants. WSL asks only which distro. It then supplies a default distro, entry at the current directory, host drives at `/mnt/c`, a Linux-native home, localhost, GUI applications and host interop. It trusts the distro with the Windows user's authority.

Host files shared through virtiofs are already accessed with the host user's permissions. A guest that can write the host home can change host startup files and keys. Per-grant opt-ins therefore do not create a sandbox once a writable home share exists. Unix sockets do not connect across virtiofs, so sharing storage does not pass D-Bus, agent or display sockets.

The project is one day old and has no users or compatibility obligations. The product goal since [ADR-0004](0004-managed-development-vms.md) is the WSL2 experience.

## Decision

**One noun: machine.** nsl manages named, persistent Linux machines. A machine is a distro, its packages, services and a guest-native home. Projects are ordinary directories, not machine properties. Per-project environment definitions such as devcontainers or Compose run inside a machine. "Machine" replaces "environment" in the CLI, state and documentation.

**Trust model.** A machine is a trusted extension of the host user. The VM boundary keeps guest kernels, packages, services and root off the host OS; it does not protect the user's files from the machine. Guest root is never host root. `--isolated` creates a machine with no host files, desktop session or guest-to-host actions. There is no read-only tier: read access to a home already exposes keys and credentials.

**Host files at `/mnt/host`.** Machines that are not isolated see an allowlist of host storage, read-write, at its canonical host path under `/mnt/host`: the user's home, `/run/media/USER` and `/mnt`. Translation is a prefix after resolving symlinks: host `P` ↔ guest `/mnt/host/P`. Top-level host symlinks into a shared tree, such as `/home → var/home`, appear as relative symlinks. Host `/usr`, `/etc`, `/tmp`, other `/run` content and pseudo-filesystems are not shared. Sharing is per machine, never per project.

**Account.** The guest account uses the host username, numeric UID and primary GID unless creation overrides the name. Its home is `/home/USERNAME` on machine storage, not the host home. The guest hostname is the machine name.

**Entry.** There is at most one default machine; the first machine created becomes it. Bare `nsl` opens a login shell in the default machine, in the translated current directory when it is shared, otherwise in the guest home. Commands use the same translation.

**Desktop.** Machines that are not isolated run GUI applications by default. A persistent per-machine Waypipe session supplies `WAYLAND_DISPLAY` to guest sessions, so applications start from the shell without a separate verb. Launcher entries and terminal profiles are user-scoped host files owned by nsl and removed with their machine.

**Guest-to-host actions.** Start with opening URLs and shared host paths through an on-demand, per-machine authenticated broker. The guest command `nsl-open` is the `BROWSER` and `xdg-open` handler. Arbitrary host command execution needs a separate decision.

**Lifecycle.** Commands start machines lazily. A machine with no nsl sessions or connected GUI clients stops after an idle timeout; `nsl shutdown` stops all machines. Export and import move whole machines. The trust tier is chosen at import, defaulting as for creation, and is never read from an archive.

**Topology is separate.** This decision holds whether machines are separate VMs ([ADR-0005](0005-vmspawn-and-nspawn-images.md)) or containers in one shared VM. The [shared-VM experiment](../plans/shared-vm-experiment.md) decides the topology. The CLI must not expose per-VM concepts that would prevent either.

This supersedes the explicit capability-grant posture of [ADR-0004](0004-managed-development-vms.md) and [ADR-0005](0005-vmspawn-and-nspawn-images.md), and the project and desktop restore grants of [ADR-0006](0006-stopped-vm-backups.md). Their other decisions stand.

## Consequences

- `--project`, `/work`, `--desktop`, `gui`, the fixed `nsl` account and restore grant flags are removed. The current [CLI contract](../specs/cli.md) stays valid for the implemented binary until the planned [machine CLI](../specs/machine-cli.md) replaces it.
- The [guest contract](../specs/guest-images.md) changes: account name, hostname, `/mnt/host`, the Waypipe session and the broker client. Images need new revisions.
- `/mnt/host` has no reliable inotify, like WSL's `/mnt/c`. Watched or heavy builds belong in the guest home, with remote editor access.
- A compromised machine that is not isolated can act as the host user through files. Document this plainly and recommend `--isolated` for untrusted software. Host root, sudoers, devices, D-Bus, GPU and SSH-agent sockets remain unshared.
- Several machines can edit the same host files. virtiofs cache settings must favor coherence across machines over single-machine speed.
- [Creation-time provisioning](0013-optional-cloud-init-provisioning.md) becomes machine bootstrap, such as toolchains and dotfiles, rather than project definition.
- The repository boundary in [AGENTS.md](../../AGENTS.md) changes from "never mount host home" to this allowlist.

## Alternatives considered

- **Devcontainer model:** project-bound, rebuildable environments conflict with long-lived machines. Devcontainers remain available inside a machine.
- **Distrobox model:** sharing the host home as the guest home keeps identical paths but mixes dotfiles and build caches across distros and libcs.
- **Host root at `/mnt/c`:** a drive letter is Windows-specific. Host root includes live kernel interfaces, session runtime state and the host `/usr` the machine exists to replace.
- **Read-only host files:** still exposes credentials and suggests isolation that does not exist.
- **Per-grant opt-ins:** multiply creation questions without producing a sandbox.
- **Identical-path mirroring:** host paths at the same guest paths collide with the guest home on non-atomic hosts. Prefix translation stays explicit.

## References

- Planned interface: [machine CLI](../specs/machine-cli.md). Topology: [shared-VM experiment](../plans/shared-vm-experiment.md).
- Current behavior: [CLI contract](../specs/cli.md), [lifecycle](../design/lifecycle.md), [guest contract](../specs/guest-images.md).
- Prior decisions: [ADR-0004](0004-managed-development-vms.md), [ADR-0005](0005-vmspawn-and-nspawn-images.md), [ADR-0006](0006-stopped-vm-backups.md), [ADR-0013](0013-optional-cloud-init-provisioning.md). Research: [WSL2 roadmap](../plans/wsl2-equivalent.md).
- [WSL file systems](https://learn.microsoft.com/en-us/windows/wsl/filesystems), [WSL commands](https://learn.microsoft.com/en-us/windows/wsl/basic-commands).
