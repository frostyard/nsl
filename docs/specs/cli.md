# Spec: nsl CLI

Contract for the `nsl` binary and its tests under [ADR-0016](../adr/0016-wsl-style-machines.md) and [ADR-0017](../adr/0017-shared-vm-and-machine-images.md). nsl manages named machines: systemd-nspawn containers in one shared VM, or, for isolated machines, each in a VM of its own. The [implementation plan](../plans/shared-vm-implementation.md) records which parts are live and what each phase proved.

Mechanisms: [lifecycle](../design/lifecycle.md). Related contracts: the [agent protocol](agent.md), the [VM image](vm-image.md), [machine images](machine-images.md) and [image delivery](image-delivery.md).

## Interface

| Command | Behavior |
| --- | --- |
| `nsl [-m NAME]` | Login shell in NAME or the default machine, in the translated host directory or the guest home. |
| `nsl run [-m NAME] [--root] [--cd PATH] [--host-files CODE:BEFORE:AFTER] COMMAND [ARGS...]` | Run argv in the machine. A PTY is used when stdin and stdout are terminals. Flags precede COMMAND; `--` ends them. `--host-files` translates the host files or URLs among ARGS to machine paths. |
| `nsl create NAME --distro DISTRO:RELEASE [--offline] [--isolated] [--default] [--user NAME]` | Prepare a machine from a verified catalogue selection, and select the catalogue's VM image first when none is selected. `--image FILE --digest sha256:HEX` replaces `--distro` to select a local machine image. |
| `nsl list [--json]` | Every nsl VM (the shared VM, and each isolated machine's) with its state, image and resources, any pending restart, and every owned machine with its state, image, trust tier and default marker. |
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
| `nsl app add [-m NAME] APP` | Add the machine's application `APP.desktop` to the host's menu as `nsl-NAME-APP.desktop`, or refresh the entry nsl wrote before. |
| `nsl app remove [-m NAME] APP` | Remove that menu entry and its icon. |
| `nsl app list [-m NAME]` | The menu entries nsl owns, for one machine or for all. |
| `nsl images [--offline \| --refresh] [--json]` | List authenticated machine-image selections and the VM image in effect. Reuse a catalogue checked less than one hour ago by default; `--refresh` checks the registry immediately. |
| `nsl pull DISTRO:RELEASE [--offline]` | Verify and cache a machine image without creating a machine. |
| `nsl update [--offline]` | Select the catalogue's current VM image for the next start of each nsl VM, isolated ones included. |
| `nsl update --image FILE --digest sha256:HEX` | Select a local VM image instead. |
| `nsl config [--json]` | Print the effective configuration, the source of each value and any change waiting for a VM restart. |
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
- `--host-files CODE:BEFORE:AFTER` marks host files or URLs in argv ([ADR-0022](../adr/0022-desktop-menu-entries.md#files-and-urls)). CODE is `f`, `F`, `u` or `U`, the desktop-entry field code they came from; BEFORE and AFTER are decimal counts of the command's own arguments before and after them, COMMAND counting in BEFORE. Argv MUST have at least BEFORE + AFTER arguments and BEFORE MUST be at least 1; the arguments between are the files, and `f` and `u` MUST have at most one. Each file is translated, and the command runs with the results in their place; any refusal MUST fail the command before anything runs. A path, relative to the host working directory when it is not absolute, has its symlinks resolved on the host when it exists; it MUST then lie in a shared tree, matched as the working directory is, and becomes its `/mnt/host` path. For `u` and `U`, an argument that starts with a URL scheme (a letter, then letters, digits, `+`, `-` or `.`, then `:`) is a URL: a `file:` URL MUST have no host or `localhost` and its path is translated to the `file:` URL of the machine path, and a URL of any other scheme is passed unchanged. For `f` and `F` every argument is a path. An isolated machine MUST refuse any file.

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
- Launcher entries and terminal profiles MUST be user-scoped, nsl-prefixed and removed with their machine. They MUST NOT overwrite unrelated files. Terminal profiles belong to a later phase.

### Menu entries

`nsl app` manages host menu entries for machine applications ([ADR-0022](../adr/0022-desktop-menu-entries.md)). `-m NAME` selects the machine, the default machine when absent, as with `run`.

- APP is a desktop-file ID without `.desktop` (a trailing `.desktop` is removed): an ASCII letter or digit, then letters, digits, `.`, `_` or `-`, at most 200 characters. The host entry is `$XDG_DATA_HOME/applications/nsl-NAME-APP.desktop`; when `XDG_DATA_HOME` is unset, empty or relative, it is `~/.local/share/applications`.
- `app add` MUST refuse an isolated machine and an unprepared one. It starts the machine and reads `APP.desktop` with the agent's `run` operation and argv `cat -- PATH`, as the machine account, from the first of `/home/USER/.local/share/applications`, `/usr/local/share/applications` and `/usr/share/applications` that has it. A `cat` exit status of 1 means absent; any other failure stops the command. Reads are bounded: 64 KiB for an entry, 1 MiB for an icon.
- The guest entry MUST be UTF-8 with `[Desktop Entry]` as its first group, `Type=Application`, no `Hidden=true`, and an `Exec`. Only that group's unlocalized keys are read, and a key that appears twice is refused; string escapes (`\s`, `\n`, `\t`, `\r`, `\\`) are decoded, and a decoded value with a control character is refused.
- `Exec` is split by the desktop-entry rules: arguments separated by spaces, double-quoted arguments with `\"`, `` \` ``, `\$` and `\\` escapes. A closing quote MUST be followed by a space or the end of the line; quoting part of an argument is refused. An unquoted argument containing `"`, `'`, `\`, `>`, `<`, `~`, `|`, `&`, `;`, `$`, `*`, `?`, `#`, `(`, `)` or `` ` `` MUST be refused as shell syntax. The first argument after the command that is exactly `%f`, `%F`, `%u` or `%U` is the file field code and is kept; any other one, and arguments that are exactly `%i`, `%c`, `%k`, `%d`, `%D`, `%n`, `%N`, `%v` or `%m`, are dropped; `%%` becomes `%`; any other `%` MUST be refused. A result with no command MUST be refused.
- The host entry holds `[Desktop Entry]`, `Type=Application`, `Name` as the guest `Name` with ` (NAME)` appended, `Exec`, `TryExec`, `Terminal` (`true` only when the guest says so), and, when the guest has them, `GenericName`, `Comment`, `Icon`, `Categories`, `Keywords` and `StartupWMClass`, then `X-Nsl-Machine=NAME`, `X-Nsl-Machine-ID=ID` and `X-Nsl-App=APP`. It has no `MimeType`, `Actions` or `DBusActivatable`. Values are written with the string escapes.
- `Exec` MUST run `NSL run -m NAME --cd DIR -- ARGV`, or, when the guest has a file field code, `NSL run -m NAME --cd DIR --host-files CODE:BEFORE:AFTER -- ARGV` with the field code at its place in ARGV, where BEFORE and AFTER count the guest arguments around it. NSL is the absolute path of `nsl` on `PATH`, or the running binary when `PATH` has none; `TryExec` is NSL. DIR is the guest `Path` when it is absolute, otherwise `/home/USER`. Each argument containing a space, tab, `"`, `'`, `\`, `>`, `<`, `~`, `|`, `&`, `;`, `$`, `*`, `?`, `#`, `(`, `)` or `` ` ``, or that is empty, is double-quoted with `"`, `` ` ``, `$` and `\` escaped by a backslash; then every `%` becomes `%%`, except in the file field code, and the string escapes apply.
- `Icon` from the guest is an absolute path ending in `.png` or `.svg`, or a name of letters, digits, `.`, `_`, `+` or `-`. A name is looked up as `/usr/share/icons/hicolor/scalable/apps/ICON.svg`, then `ICON.png` under `512x512`, `256x256`, `192x192`, `128x128`, `96x96`, `64x64`, `48x48` and `32x32`. When none of those is a valid icon, nsl lists `/usr/share/icons` with argv `ls -1 -- /usr/share/icons` and, for each entry other than `hicolor` that is a name of letters, digits, `.`, `_`, `+` or `-`, in byte order and at most 32, reads `THEME/index.theme` (at most 256 KiB; absent or unparsable skips the theme). The candidates of a theme are its `Directories` and `ScaledDirectories` (each a relative path of names of letters, digits, `.`, `_`, `+`, `-` or `@`, joined by `/`, with no `.` or `..` component) whose group has `Context=Applications`: those with `Type=Scalable` first, then by `Size` times `Scale` (1 when absent), largest first, each as `ICON.svg` and then `ICON.png`; then `/usr/share/pixmaps/ICON.svg` and `ICON.png`. At most 2048 candidates are checked, with one argv `ls -1d -- CANDIDATE...`, whose exit status 0, 1 or 2 is a listing; the files it prints are read in candidate order. A file found MUST start with the PNG signature, or contain `<svg` in its first 4 KiB, for the matching extension; one that does not is skipped with a note on stderr, and the lookup goes on. An `Icon` that is neither form finds no icon. It is written, mode 0600, to `NSL_HOME/machines/NAME.apps/APP.png` or `APP.svg`, replacing that APP's other icon, and `Icon` names that path. No icon found means no `Icon` key and a note on stderr.
- A host entry is owned when it is a regular file of the user, at most 1 MiB, whose `[Desktop Entry]` group parses and whose `X-Nsl-Machine`, `X-Nsl-Machine-ID` and `X-Nsl-App` name the machine, its current ID and APP. `app add` MUST write the entry under the machine lock and reject a machine whose ID changed. A new entry MUST be published without replacing any path; an existing entry MUST be replaced atomically only when owned, and any other file at the path MUST be refused unchanged.
- `app remove` MUST delete only an owned entry, then the icons of APP. A missing entry is an error.
- Ownership does not depend on the other keys, so an owned entry the user edited stays owned. `app remove` MUST refuse an icon directory that is not a private directory of the user; machine removal deletes `NAME.apps` without following a symlink.
- `app list` MUST print `MACHINE`, `APP` and `NAME` for each owned entry of an existing machine, sorted by machine and APP, or a line saying there are none.
- `remove NAME --yes` MUST delete every owned entry of the machine's ID and `NAME.apps/` after the machine is gone and before the tombstone, so a resumed removal finishes them. Entries of another ID or of no nsl machine MUST be left. The preview without `--yes` MUST name how many menu entries go with the machine when there are any, and fail when it cannot count them.

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
- `doctor` and VM launch MUST fail when systemd-vmspawn comes from a systemd older than 259, naming the installed and required versions ([ADR-0005](../adr/0005-vmspawn-and-nspawn-images.md)). `doctor` MUST report that before the firmware check, and launch MUST refuse before changing any state.
- `doctor` MUST fail when systemd-vmspawn lists no firmware that launch's `--secure-boot=no` selects. It reads the descriptors vmspawn lists, in vmspawn's order, and accepts the first that systemd 259 to 262 would all select: UEFI with an NVRAM template, an x86-64 q35 target, and neither Secure Boot nor enrolled keys. vmspawn 259 cannot describe its choice, and the list succeeds when there is no firmware.
- VM launch MUST pass only flags that systemd 259's vmspawn accepts, and both VM disks MUST be raw.
- `doctor` and VM launch MUST check `kvm` membership in the host account database before invoking `sg` or `newgrp`. Missing membership MUST produce instructions to join the group and skip the group switch; lookup failures MUST be reported separately. Membership added since login MUST be recognized. Doctor MUST still probe device access when membership is confirmed.

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

### Machine-readable output

`--json` makes `list`, `images` and `config` print a JSON document for other programs ([ADR-0021](../adr/0021-machine-readable-output.md)).

- On success, stdout MUST hold exactly one JSON object and a newline, and nothing else. On failure, stdout MUST stay empty; the message goes to stderr and the exit status is non-zero, as without `--json`.
- The document and the table MUST be rendered from the same values. `--json` MUST NOT start, stop or change anything the table command would not.
- Arrays MUST be present, possibly empty. Consumers MUST ignore fields they do not know.
- An image is its build ID when nsl knows it, otherwise `sha256:` and the full hex digest; `""` when there is none.

`nsl list --json`:

```json
{
  "vms": [
    {"name": "shared", "role": "shared", "state": "running", "image": "nsl-vm-trixie-x86-64-r9",
     "cpus": 8, "memory_gib": 16, "data_disk_gib": 128, "pending": ["vm.memory 4 GiB (running with 8)"]}
  ],
  "machines": [
    {"name": "trixie", "state": "running", "image": "nsl-machine-debian-trixie-x86-64-r4", "tier": "shared", "default": true},
    {"name": "old", "state": "removing", "image": "", "tier": "", "default": false}
  ]
}
```

| Field | Meaning |
| --- | --- |
| `vms[].name` | `shared`, or the isolated machine the VM belongs to. |
| `vms[].role` | `shared` or `isolated`. |
| `vms[].state` | `running`, `stopping`, `failed` or `stopped`. |
| `vms[].image` | The VM image of the VM's root. |
| `vms[].cpus`, `vms[].memory_gib` | The running VM's resources; absent while it is not running. |
| `vms[].data_disk_gib` | The committed data-disk capacity. |
| `vms[].pending` | Changes that apply at the VM's next start, as `list` prints them. |
| `machines[].state` | `running`, `starting`, `stopping` or `stopped` from the VM, `incomplete` when creation did not finish, or `removing` while removal is unfinished. |
| `machines[].image` | The machine image's build ID; `""` for imported machines and machines being removed. |
| `machines[].tier` | `shared` or `isolated`; `""` while being removed. |
| `machines[].default` | Whether this is the default machine. |

`nsl images --json`:

```json
{
  "catalogue": {"sequence": 14, "expires": "2026-11-04T13:09:26Z"},
  "images": [
    {"kind": "vm", "selectors": [], "build": "nsl-vm-trixie-x86-64-r11", "manifest": "sha256:…", "cached": false},
    {"kind": "machine", "selectors": ["debian:trixie", "debian:13"], "build": "nsl-machine-debian-trixie-x86-64-r4",
     "manifest": "sha256:…", "cached": true}
  ],
  "vm_image": {"image": "nsl-vm-trixie-x86-64-r9", "pending": false}
}
```

`images` lists the catalogue's x86-64 entries in catalogue order; `manifest` is the OCI manifest digest a selector can pin with `@sha256:HEX`. `vm_image` is `null` when no VM image is selected; `pending` is `true` when the image is selected for the next VM start.

`nsl config --json`:

```json
{
  "path": "/home/u/.config/nsl/nsl.conf",
  "present": true,
  "settings": [
    {"key": "vm.memory", "value": 16, "min": 1, "max": 128, "unit": "GiB", "source": "default", "reason": "half of host memory"},
    {"key": "vm.cpus", "value": 4, "min": 1, "max": 64, "unit": "CPUs", "source": "file", "line": 3},
    {"key": "machines.autostart", "value": true, "source": "default"}
  ],
  "pending": [{"vm": "shared", "change": "vm.cpus 4 (running with 8)"}]
}
```

`settings` lists every key in the table's order. A number has `min`, `max` and `unit`; a boolean has none. `source` is `default` or `file`; `line` is present for `file`, and `reason` for a default derived from the host.

### Open interface questions

- Whether to translate absolute host symlinks that point into shared trees; see the [experiment plan](../plans/shared-vm-experiment.md).
- Whether to trigger host automounts for guest access. virtiofs lookups do not trigger them, so an unmounted autofs point appears empty until the host mounts it.

## Validation boundaries

Unit tests use fake tools and local processes and need neither root nor a VM. Integration evidence comes from disposable real VMs and is recorded in the [implementation plan](../plans/shared-vm-implementation.md) as each phase completes.

## References

- Rationale: [ADR-0016](../adr/0016-wsl-style-machines.md), [ADR-0017](../adr/0017-shared-vm-and-machine-images.md), [ADR-0006](../adr/0006-stopped-vm-backups.md), [ADR-0008](../adr/0008-offline-storage-management.md), [ADR-0021](../adr/0021-machine-readable-output.md), [ADR-0022](../adr/0022-desktop-menu-entries.md).
- Contracts: [agent](agent.md), [VM image](vm-image.md), [machine images](machine-images.md), [image delivery](image-delivery.md), [provisioning](provisioning.md) (deferred).
- Evidence: [shared-VM experiment](../plans/shared-vm-experiment.md). Implementation: [machines in a shared VM](../plans/shared-vm-implementation.md).
