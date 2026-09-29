# Spec: nsl CLI

Contract for the `nsl` binary and its tests under [ADR-0016](../adr/0016-wsl-style-machines.md) and [ADR-0017](../adr/0017-shared-vm-and-machine-images.md). nsl manages named machines: systemd-nspawn containers in one shared VM, or, for isolated machines, each in a VM of its own. The [implementation plan](../plans/shared-vm-implementation.md) records which parts are live and what each phase proved.

Mechanisms: [lifecycle](../design/lifecycle.md). Related contracts: the [agent protocol](agent.md), the [VM image](vm-image.md), [machine images](machine-images.md) and [image delivery](image-delivery.md).

## Interface

| Command | Behavior |
| --- | --- |
| `nsl [-m NAME]` | Login shell in NAME or the default machine, in the translated host directory or the guest home. |
| `nsl run [-m NAME] [--root] [--cd PATH] COMMAND [ARGS...]` | Run argv in the machine. A PTY is used when stdin and stdout are terminals. Flags precede COMMAND; `--` ends them. |
| `nsl create NAME --distro DISTRO:RELEASE [--offline] [--isolated] [--default] [--user NAME]` | Prepare a machine from a verified catalogue selection, and select the catalogue's VM image first when none is selected. `--image FILE --digest sha256:HEX` replaces `--distro` to select a local machine image. |
| `nsl list` | Every nsl VM (the shared VM, and each isolated machine's) with its state, image and resources, any pending restart, and every owned machine with its state, image, trust tier and default marker. |
| `nsl default NAME` | Make NAME the default machine. |
| `nsl start NAME` | Start a machine, and its VM if needed, and wait for readiness. |
| `nsl stop NAME` | Stop one machine and preserve all state. |
| `nsl shutdown` | Stop every running machine and every nsl VM, isolated ones included. |
| `nsl export NAME FILE` | Write an archive of a stopped machine; never overwrite. |
| `nsl import NAME FILE [--isolated]` | Verify and import an archive under an unused name. |
| `nsl remove NAME [--yes]` | Preview, then permanently remove a stopped machine. |
| `nsl ports [NAME]` | Forwarding status and conflicts for one machine or all machines. |
| `nsl logs [NAME]` | Recent logs of the host units nsl runs: the VM and its forwarder, and each machine's desktop session, or those of one machine. |
| `nsl ssh-config NAME` | Start if needed and print an SSH configuration for remote editors. |
| `nsl images [--offline]` | List authenticated machine-image selections and the VM image in effect. |
| `nsl pull DISTRO:RELEASE [--offline]` | Verify and cache a machine image without creating a machine. |
| `nsl update [--offline]` | Select the catalogue's current VM image for the next start of each nsl VM, isolated ones included. |
| `nsl update --image FILE --digest sha256:HEX` | Select a local VM image instead. |
| `nsl config` | Print the effective configuration, the source of each value and any change waiting for a VM restart. |
| `nsl recover [NAME]` | Restart the shared VM, or isolated machine NAME's VM, from a fresh root; check its data disk and resume interrupted work; preserve machines. |
| `nsl resize [NAME] --disk GiB` | Grow the stopped shared VM's data disk, or isolated machine NAME's; never shrink. |
| `nsl doctor`, `nsl version`, `nsl help` | Host checks, build version and usage. |

Guest commands installed by every machine image:

| Command | Behavior |
| --- | --- |
| `nsl-open TARGET` | Open an `http`/`https` URL or a path under `/mnt/host` with the host's default handler. Also set as `BROWSER` and the `xdg-open` handler. |
| `nsl-path [--host \| --guest] PATH` | Translate between host and guest paths using the `/mnt/host` prefix. |

Machine names start with a lowercase ASCII letter, then lowercase letters, digits or interior hyphens, at most 24 characters. Flags follow the machine name. `--user` takes a POSIX account name: a lowercase letter or underscore, then lowercase letters, digits, underscores or hyphens, at most 32 characters. Machines and VMs are x86-64.

## Rules

### Selection and entry

- There MUST be zero or one default machine. The first machine created MUST become the default; `--default` MUST select it explicitly.
- Removing the default MUST leave no default. Bare `nsl` without a default MUST fail and list machines; it MUST NOT pick one implicitly.
- The host working directory MUST be matched against shared trees by the device and inode of its ancestors, so symlink and bind-mount aliases translate. For example, `/home` and `/var/home` can be two mounts of one subvolume. When it lies in a shared tree and the machine is not isolated, the guest directory MUST be `/mnt/host` followed by the tree's canonical path and the remaining components.
- A shell whose directory cannot be translated MUST start in the guest home and say so on stderr.
- `run` with an untranslatable directory and no `--cd` MUST fail without running anything. A command intended for the project directory must not run somewhere else.
- `--cd` MUST take an absolute guest path.

### Host files

- Machines that are not isolated MUST see the user's home, `/run/media/USER` and `/mnt`, read-write, at their canonical host paths under `/mnt/host`. Nothing else from the host filesystem MAY be shared with machines.
- Top-level host aliases of a shared tree, whether symlinks or bind mounts, SHOULD appear as matching relative symlinks under `/mnt/host`.
- Files created through `/mnt/host` MUST be owned by the host user. Guest root MUST NOT gain host permissions beyond the host user's.
- Unix sockets under shared trees MUST NOT be proxied.
- Isolated machines MUST have no `/mnt/host` content, desktop session or broker access. Their VM's credential names no shares, and vmspawn gets no share binds; only the read-only machine-image cache is mounted in that VM.
- An isolated machine's working directory never translates: a shell starts in its home, and `run` needs `--cd`.
- A VM MAY read the host's verified machine-image cache through a separate read-only share, which machines never see.

### Account and execution

- The guest account MUST use the host username unless `--user` overrides it, together with the host numeric UID and primary GID. Its home is `/home/USERNAME` on machine storage.
- A username that conflicts with an existing guest account MUST fail creation clearly without changing that account.
- The guest hostname MUST be the machine name.
- Argv MUST be preserved literally. Binary streams, separate stdout/stderr, exit status and signals MUST survive execution. A command killed by signal N MUST exit 128+N.
- `--root` MUST select guest root and never host root.
- Host commands MUST run as the normal user, without implicit sudo or host configuration changes.

### Desktop and host actions

- When the host has a Wayland session and the machine is not isolated, guest sessions MUST receive a `WAYLAND_DISPLAY` served by a persistent per-machine Waypipe session, and `XDG_SESSION_TYPE=wayland`. Host display sockets MUST NOT be shared directly.
- A command that starts a machine MUST start its desktop session when the command's environment has `WAYLAND_DISPLAY` and Waypipe is available, and the session is not running. The session is a user unit, `nsl-UID-vm-ID-desktop-NAME.service`, bound to the VM's unit. It runs the host's `waypipe client` and the machine's broker, and holds the agent's [`display`](agent.md#display) operation open. It restarts after a failure and ends with the VM.
- The broker MUST authenticate each machine. It MUST accept only `http`/`https` URLs and translatable `/mnt/host` paths, and refuse every other target.
- The broker speaks [Varlink](https://varlink.org) on the machine's `/run/nsl/desktop/open.sock`: one method, `io.frostyard.nsl.Broker.Open`, whose `target` parameter is a URL or an absolute machine path. A URL MUST have the `http` or `https` scheme and a host. A path MUST lie under `/mnt/host`, and the host path it names, with symlinks resolved on the host, MUST lie in a shared tree. The broker opens the target with the host's `xdg-open` as argv. It answers `io.frostyard.nsl.Broker.Refused` with a `reason` for any other target, and `io.frostyard.nsl.Broker.Failed` when the opener fails. Each machine's session has its own socket, so the broker knows which machine asked.
- `ssh-config` MUST print a host alias that reaches the machine account through nsl, with a key generated for that machine. It MUST NOT enable a network listener in the machine. The alias is `nsl-NAME`; its proxy command, `nsl _ssh NAME`, starts the machine and runs the agent's [`ssh`](agent.md#ssh) operation. The key and the pinned host key live in `NSL_HOME/machines/NAME.ssh/` and are removed with the machine.
- Launcher entries and terminal profiles MUST be user-scoped, nsl-prefixed and removed with their machine. They MUST NOT overwrite unrelated files. This work belongs to a later phase.

### Lifecycle

- Commands MUST start a stopped machine and wait for authenticated readiness.
- The shared VM MUST start on first use. When a command that enters or starts a machine starts it, it MUST start every machine unless `autostart` is `false`; machines then start on first use. `create`, `import`, `export` and `remove` MUST start it without starting machines.
- A machine with no nsl command sessions and no connected GUI clients MUST stop after `idle_timeout`. Services started inside the machine do not keep it running. A `start` or command restarts the idle clock.
- The VM MUST stop when no machine has been running, and no request has been in flight, for 60 seconds. A command that meets a VM powering itself off MUST wait for it to stop and start it again.
- `stop` and idle stop MUST preserve all machine state.
- Automatic forwarding MUST bind host loopback, report and retry conflicts, and never evict an existing listener. Machines share the VM's network namespace, so the same port in two machines conflicts inside the VM, as in WSL.
- A forwarder user unit per VM, `nsl-UID-vm-ID-ports.service`, bound to the VM's unit, MUST poll the agent's `listeners` once a second. It forwards host `127.0.0.1:PORT` to VM `127.0.0.1:PORT` for listeners on `127.0.0.1`, `0.0.0.0` or `::`, and to `[::1]:PORT` for listeners only on `::1`. `nsl ports` MUST show each port's machine and state, `forwarded` or `conflict` with the error.
- Resource limits MUST come from the configuration: one budget for the shared VM, and one for each isolated machine's VM.
- An isolated machine's VM MUST be created with the machine, from the VM image selected for the shared VM, and MUST be deleted with it: `remove` stops it and deletes its disks. A failed creation MUST leave no isolated VM. Every VM has its own ID, units, runtime directory, keys and forwarder, and a vsock CID no other VM of the state directory uses.

### State, ownership and locking

- State and runtime unit identity MUST be validated before mutation: owner, file type and permissions of state files, and the description of every unit before nsl controls it. Foreign units, unsupported metadata and existing names MUST be rejected.
- Every VM and machine has a random 32-hex ID. Lifecycle changes MUST serialize per VM and per machine. A call that waited for a lock MUST reject a machine or VM whose ID changed while it waited. Command sessions MAY run concurrently after readiness.
- Readiness MUST authenticate the VM and verify its ID, UID, GID, role and image descriptor, including for an already running unit. It MUST reject an agent protocol, architecture or transport that differs from the CLI's before forwarding ports or running commands.
- Interrupted preparation MUST retain state. `recover` MUST preserve the data disk, keys and pinned host keys; it MUST NOT silently repair corruption or regenerate lost keys.
- nsl MUST NOT overwrite an existing machine, image, archive or file it did not create.

### Storage

- `remove --yes` MUST require a stopped owned machine and the manager and machine locks. It MUST reserve the name until deletion finishes, show the machine as `Removing` in `list`, and resume from the same command after interruption. It MUST preserve host files, cached images and exported archives. It MUST acquire the machine lock before the manager lock, recheck the machine ID after waiting, release the manager lock once the name is reserved for removal, and keep the machine lock until removal finishes.
- `resize --disk` MUST require the VM to be stopped. It MUST refuse shrinking and invalid capacities (up to 4096 GiB), record the pending target before changing the disk, and validate the result before committing the new capacity. Pending growth MUST appear in `list`, block VM start, and complete through `resize` or `recover`. A capacity matching neither the committed nor the pending size MUST be refused.
- The VM grows its data filesystem to the disk at boot; the CLI only changes and verifies virtual capacity. Machines share the data disk's capacity.

### Export and import

- `export` MUST hold the machine lock and refuse a machine that is not stopped. It MUST publish a mode-0600 archive without replacing an existing path, and preserve the source. The root filesystem it writes MUST pass import's entry validation.
- The archive is a tar file whose first entry is `manifest.json` and whose second and last is `rootfs.tar.zst`, followed only by zero padding. The manifest is a JSON object: `format` (`1`), `architecture` (`x86-64`), `machine` (the exported name), `account` (`user`, `group`, `uid`, `gid`), `build_id` of the machine's image, `created` (RFC 3339) and `rootfs` (`digest` as `sha256:HEX` and `size` of `rootfs.tar.zst`). The root filesystem stream preserves numeric owners, modes, xattrs and ACLs, and may hold device nodes, such as overlay whiteouts of rootless containers.
- `import` MUST require x86-64 and the archive's UID and primary GID to match the host user. Before publishing a machine, it MUST validate the format version, names, entry types, size bounds, checksums and every root filesystem entry. Absolute or `..` paths, links leaving the tree, duplicates, unexpected entries and trailing data MUST be refused. The account MUST exist in the root filesystem with the host UID and GID. An invalid archive MUST leave no named machine.
- Import MUST choose the trust tier from its flags, defaulting to not isolated. It MUST NOT read the tier from the archive. It MUST apply per-machine data for the new name: hostname, hosts entry, host time zone, `sudo` rule and nspawn settings. The first machine imported into an empty state directory becomes the default, as with `create`.
- Checksums detect damage; they do not authenticate the archive's origin. Archives are unencrypted and can contain credentials.

### Images

- `create --distro`, `pull` and `update` follow the [delivery contract](image-delivery.md): the catalogue and each image are authenticated against the Frostyard publishing identity before use. `--offline` MUST use only verified cached data and never download.
- Local `--image FILE --digest sha256:HEX` verifies the selected bytes but not a publisher. It is an explicit developer path.
- Creation MUST NOT need the network once the images are cached. A failed create or import MUST attempt cleanup. The host MUST retain its machine record and original ID until removal is confirmed, or the agent confirms that the machine is absent under its lifecycle lock. An uncertain cleanup MUST keep the name reserved, appear as incomplete in `list`, and direct the user to retry with `remove NAME --yes`.
- `update` MUST NOT start, stop or change a running VM. The selected VM image replaces each VM's root at that VM's next start, keeping its data disk. `config` and `list` MUST report the pending image.

### Configuration

The optional file `$XDG_CONFIG_HOME/nsl/nsl.conf` is separate from state in `NSL_HOME`. When `XDG_CONFIG_HOME` is unset, empty or relative, the file is `~/.config/nsl/nsl.conf`. nsl reads the file and never writes it.

```ini
# ~/.config/nsl/nsl.conf
[vm]
memory = 16
cpus = 8

[machines]
idle_timeout = 0
```

| Section | Key | Value | Default |
| --- | --- | --- | --- |
| `vm` | `memory` | GiB ceiling for the shared VM, 1–128 | Half the host's memory, rounded down, from 2 to 128 |
| `vm` | `cpus` | vCPUs for the shared VM, 1–64 | Every host CPU, at most 64 |
| `machines` | `autostart` | `true` or `false`: start every machine when the VM starts | `true` |
| `machines` | `idle_timeout` | Minutes without sessions before a machine stops, 0–1440; `0` disables | `15` |
| `isolated` | `memory` | GiB for each isolated machine's VM, 1–128 | `2` |
| `isolated` | `cpus` | vCPUs for each isolated machine's VM, 1–64 | `2` |

Syntax:

- The file is UTF-8 without NUL bytes, at most 64 KiB. Lines are trimmed of surrounding whitespace, including a CR before the LF.
- Blank lines are ignored. A line starting with `#` is a comment; there are no inline comments.
- `[section]` starts a section; each section appears at most once. `key = value` splits at the first `=`, and both sides are trimmed. Names are case-sensitive.
- Numbers are decimal digits only: no sign, unit or quotes. Booleans are exactly `true` or `false`.

Rules:

- An absent file MUST mean defaults. A symlink to a missing file, or anything other than a regular file, MUST be an error.
- Unknown sections or keys, duplicate sections or keys, keys outside a section, invalid values and out-of-range numbers MUST be errors naming the file and line, as `PATH:LINE: message`. nsl MUST NOT start a VM with a partially understood file.
- `nsl config` MUST print each setting, its value and its source: `default`, or `file` with its line.
- A command-line flag MAY override a key for one invocation, with the source `flag`. nsl MUST NOT rewrite the file.
- Changes to `vm` or `isolated` resources MUST apply at the next start of the affected VM, and `nsl config` and `nsl list` MUST report the pending restart. `autostart` applies at the next VM start; `idle_timeout` applies from the next nsl command.

### Open interface questions

- Whether to translate absolute host symlinks that point into shared trees; see the [experiment plan](../plans/shared-vm-experiment.md).
- Whether to trigger host automounts for guest access. virtiofs lookups do not trigger them, so an unmounted autofs point appears empty until the host mounts it.

## Validation boundaries

Unit tests use fake tools and local processes and need neither root nor a VM. Integration evidence comes from disposable real VMs and is recorded in the [implementation plan](../plans/shared-vm-implementation.md) as each phase completes.

## References

- Rationale: [ADR-0016](../adr/0016-wsl-style-machines.md), [ADR-0017](../adr/0017-shared-vm-and-machine-images.md), [ADR-0006](../adr/0006-stopped-vm-backups.md), [ADR-0008](../adr/0008-offline-storage-management.md).
- Contracts: [agent](agent.md), [VM image](vm-image.md), [machine images](machine-images.md), [image delivery](image-delivery.md), [provisioning](provisioning.md) (deferred).
- Evidence: [shared-VM experiment](../plans/shared-vm-experiment.md). Implementation: [machines in a shared VM](../plans/shared-vm-implementation.md).
