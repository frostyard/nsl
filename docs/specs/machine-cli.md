# Spec: nsl machine CLI

**Planned; not implemented.** This contract replaces the [current CLI contract](cli.md) under [ADR-0016](../adr/0016-wsl-style-machines.md). Under [ADR-0017](../adr/0017-shared-vm-and-machine-images.md), machines are systemd-nspawn containers in one shared VM, and each isolated machine has a VM of its own. There is no compatibility or migration path from the environment CLI.

## Interface

| Command | Behavior |
| --- | --- |
| `nsl [-m NAME]` | Login shell in NAME or the default machine, in the translated host directory or the guest home. |
| `nsl run [-m NAME] [--root] [--cd PATH] COMMAND [ARGS...]` | Run argv in the machine. A PTY is used when stdin and stdout are terminals. |
| `nsl create NAME --distro DISTRO:RELEASE [--isolated] [--default] [--user NAME] [--offline]` | Prepare a machine from a verified catalogue selection. `--image FILE --digest sha256:HEX` selects a local image. |
| `nsl list` | Name, state, distro, trust tier and default marker for every owned machine. |
| `nsl default NAME` | Make NAME the default machine. |
| `nsl stop NAME` | Stop one machine and preserve all state. |
| `nsl shutdown` | Stop every running machine and every nsl VM. |
| `nsl export NAME FILE` | Write a versioned archive of a stopped machine's root filesystem and manifest; never overwrite. |
| `nsl import NAME FILE [--isolated]` | Verify and import an archive under an unused name. |
| `nsl remove NAME [--yes]` | Preview, then permanently remove a stopped machine. |
| `nsl ports [NAME]` | Forwarding status and conflicts for one machine or all machines. |
| `nsl logs [NAME]` | Recent host-side logs for one machine or all machines. |
| `nsl ssh-config NAME` | Start if needed and print an SSH configuration for remote editors. |
| `nsl images`, `nsl pull DISTRO:RELEASE` | Unchanged from the [delivery contract](image-delivery.md). |
| `nsl config` | Print the effective configuration, the source of each value (default, file or flag) and any change waiting for a VM restart. |
| `nsl recover [NAME]` | Restart the shared VM, or an isolated machine's VM, check its data disk and resume interrupted work; preserve machines. |
| `nsl resize [NAME] --disk GiB` | Grow the stopped shared VM's machine data disk, or an isolated machine's; never shrink. |
| `nsl doctor`, `nsl version`, `nsl help` | Host checks, build version and usage. |

Guest commands installed by every image:

| Command | Behavior |
| --- | --- |
| `nsl-open TARGET` | Open an `http`/`https` URL or a path under `/mnt/host` with the host's default handler. Also set as `BROWSER` and the `xdg-open` handler. |
| `nsl-path [--host \| --guest] PATH` | Translate between host and guest paths using the `/mnt/host` prefix. |

Machine names follow the current rules: a lowercase ASCII letter first, then lowercase letters, digits or interior hyphens, at most 24 characters.

## Rules

### Selection and entry

- There MUST be zero or one default machine. The first machine created MUST become the default; `--default` MUST select it explicitly.
- Removing the default MUST leave no default. Bare `nsl` without a default MUST fail and list machines; it MUST NOT pick one implicitly.
- The host working directory MUST be matched against shared trees by the device and inode of its ancestors, so symlink and bind-mount aliases translate. For example, `/home` and `/var/home` can be two mounts of one subvolume. When it lies in a shared tree and the machine is not isolated, the guest directory MUST be `/mnt/host` followed by the tree's canonical path and the remaining components.
- A shell whose directory cannot be translated MUST start in the guest home and say so on stderr.
- `run` with an untranslatable directory and no `--cd` MUST fail without running anything. A command intended for the project directory must not run somewhere else.
- `--cd` MUST take an absolute guest path.

### Host files

- Machines that are not isolated MUST see the user's home, `/run/media/USER` and `/mnt`, read-write, at their canonical host paths under `/mnt/host`. Nothing else from the host filesystem MAY be shared.
- Top-level host aliases of a shared tree, whether symlinks or bind mounts, SHOULD appear as matching relative symlinks under `/mnt/host`.
- Files created through `/mnt/host` MUST be owned by the host user. Guest root MUST NOT gain host permissions beyond the host user's.
- Unix sockets under shared trees MUST NOT be proxied.
- Isolated machines MUST have no `/mnt/host` content, desktop session or broker access.

### Account and execution

- The guest account MUST use the host username unless `--user` overrides it, together with the host numeric UID and primary GID. Its home is `/home/USERNAME` on machine storage.
- A username that conflicts with an existing guest account MUST fail creation clearly without changing that account.
- The guest hostname MUST be the machine name.
- Argv MUST be preserved literally. Binary streams, separate stdout/stderr, exit status and signals MUST survive execution.
- `--root` MUST select guest root and never host root.

### Desktop and host actions

- When the host has a Wayland session and the machine is not isolated, guest sessions MUST receive a `WAYLAND_DISPLAY` served by a persistent per-machine Waypipe session. Host display sockets MUST NOT be shared directly.
- The broker MUST authenticate each machine. It MUST accept only `http`/`https` URLs and translatable `/mnt/host` paths, and refuse every other target.
- Launcher entries and terminal profiles MUST be user-scoped, nsl-prefixed and removed with their machine. They MUST NOT overwrite unrelated files. This work belongs to a later phase.

### Lifecycle

- Commands MUST start a stopped machine and wait for authenticated readiness.
- The shared VM MUST start on first use. When it starts, it MUST start every machine unless `autostart` is `false`; machines then start on first use. The VM MUST stop when no machine is running.
- A machine with no nsl command sessions and no connected GUI clients MUST stop after `idle_timeout`. Services started inside the machine do not keep it running.
- `stop` and idle stop MUST preserve all machine state.
- Automatic forwarding MUST bind host loopback, report and retry conflicts, and never evict an existing listener. Machines share the VM's network namespace, so the same port in two machines conflicts inside the VM, as in WSL.
- Import MUST choose the trust tier from its flags, defaulting to not isolated. It MUST NOT read the tier from the archive.
- Ownership validation, locking, safe removal and archive validation from the [current contract](cli.md) carry over.
- Resource limits MUST come from the configuration: one budget for the shared VM, and one for each isolated machine's VM.

### Configuration

The optional file `$XDG_CONFIG_HOME/nsl/nsl.conf` (default `~/.config/nsl/nsl.conf`) is separate from state in `NSL_HOME`. It uses `[section]` headers, `key = value` lines and `#` comments.

| Section | Key | Value | Default |
| --- | --- | --- | --- |
| `vm` | `memory` | GiB ceiling for the shared VM, 1–128 | Half the host's memory, at least 2 |
| `vm` | `cpus` | vCPUs for the shared VM, 1–64 | Every host CPU, at most 64 |
| `machines` | `autostart` | `true` or `false`: start every machine when the VM starts | `true` |
| `machines` | `idle_timeout` | Minutes without sessions before a machine stops; `0` disables | `15` |
| `isolated` | `memory` | GiB for each isolated machine's VM, 1–128 | `2` |
| `isolated` | `cpus` | vCPUs for each isolated machine's VM, 1–64 | `2` |

- An absent file MUST mean defaults.
- Unknown sections or keys, duplicate keys and invalid values MUST be errors naming the file and line. nsl MUST NOT start a VM with a partially understood file.
- A command-line flag MAY override a key for one invocation. nsl MUST NOT rewrite the file.
- Changes to `vm` or `isolated` resources MUST apply at the next start of the affected VM, and `nsl config` and `nsl list` MUST report the pending restart. `autostart` applies at the next VM start; `idle_timeout` applies without a restart.

### Open interface questions

- Whether to translate absolute host symlinks that point into shared trees; see the [experiment plan](../plans/shared-vm-experiment.md).
- Whether to trigger host automounts for guest access. virtiofs lookups do not trigger them, so an unmounted autofs point appears empty until the host mounts it.

## References

- Rationale: [ADR-0016](../adr/0016-wsl-style-machines.md), [ADR-0017](../adr/0017-shared-vm-and-machine-images.md). Current contract: [CLI](cli.md), [guest images](guest-images.md), [provisioning](provisioning.md).
- Evidence: [shared-VM experiment](../plans/shared-vm-experiment.md).
