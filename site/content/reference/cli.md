---
description: Every nsl command, its options, name rules, guest commands and environment variables.
---

# Commands

Run `nsl help` for a quick command summary. For commands that take a machine name, put the flags after the name.

## Machines

| Command | Behavior |
| --- | --- |
| [`nsl [-m NAME]`](../guides/machines.md#enter-a-machine) | Login shell in NAME or the default machine, in the translated host directory or the guest home. |
| [`nsl run [-m NAME] [--root] [--cd PATH] COMMAND [ARGS...]`](../guides/machines.md#enter-a-machine) | Run a command in the machine. Arguments pass literally; a terminal is used when stdin and stdout are terminals. Flags precede COMMAND; `--` ends them. |
| [`nsl create NAME --distro DISTRO:RELEASE [--offline] [--isolated] [--default] [--user NAME]`](../guides/machines.md#create-a-machine) | Create a machine from a verified catalogue image. Selects the catalogue's VM image first when none is selected. |
| [`nsl create NAME --image FILE --digest sha256:HEX [--isolated] [--default] [--user NAME]`](../guides/images.md#local-images) | Create a machine from a local machine image, offline. |
| [`nsl list`](../guides/storage.md#the-data-disk) | Every nsl VM with its state, image, resources and data disk, anything pending until its next start, and every machine with its state, image, trust tier and default marker. |
| [`nsl default NAME`](../guides/machines.md#the-default-machine) | Make NAME the default machine. |
| [`nsl start NAME`](../guides/machines.md#start-stop-and-idle) | Start a machine, and its VM if needed, and wait for readiness. |
| [`nsl stop NAME`](../guides/machines.md#start-stop-and-idle) | Stop one machine and keep all its state. |
| [`nsl shutdown`](../guides/machines.md#start-stop-and-idle) | Stop every machine and every nsl VM, isolated ones included. |
| [`nsl remove NAME [--yes]`](../guides/machines.md#remove-a-machine) | Preview, then permanently remove a stopped machine. |

## Archives

| Command | Behavior |
| --- | --- |
| [`nsl export NAME FILE`](../guides/export-import.md) | Write an archive of a stopped machine. Never overwrites. |
| [`nsl import NAME FILE [--isolated]`](../guides/export-import.md) | Verify an archive and create a machine from it under an unused name. |

## Host integration

| Command | Behavior |
| --- | --- |
| [`nsl ports [NAME]`](../guides/ports.md) | Forwarded ports and conflicts, for one machine or all. |
| [`nsl ssh-config NAME`](../guides/editors.md) | Start the machine if needed and print an SSH host alias, `nsl-NAME`, for remote editors. |
| [`nsl logs [NAME]`](../guides/storage.md#logs) | Without NAME: every VM, its forwarder and desktop sessions. With NAME: that machine's desktop session; isolated machines also include their own VM and forwarder. Prints a hint for the machine's own journal. |

## Images

| Command | Behavior |
| --- | --- |
| [`nsl images [--offline \| --refresh]`](../guides/images.md#browse-and-cache) | Authenticated machine-image selections and the VM image in effect. The default reuses a catalogue checked less than one hour ago; `--refresh` checks immediately. |
| [`nsl pull DISTRO:RELEASE [--offline]`](../guides/images.md#browse-and-cache) | Verify and cache a machine image without creating a machine. |
| [`nsl update [--offline]`](../guides/images.md#update-the-vm) | Select the catalogue's current VM image for the next start of each nsl VM. |
| [`nsl update --image FILE --digest sha256:HEX`](../guides/images.md#local-images) | Select a local VM image instead. |

Add `@sha256:HEX` to a selector to pin an image's OCI manifest. That digest must still be in the current catalogue.

## Maintenance

| Command | Behavior |
| --- | --- |
| [`nsl config`](configuration.md) | The effective configuration, each value's source, and any change waiting for a VM restart. |
| [`nsl recover [NAME]`](../guides/storage.md#recover-a-vm) | Restart the shared VM, or isolated machine NAME's VM, from a fresh root; check its data disk and resume interrupted work. |
| [`nsl resize [NAME] --disk GiB`](../guides/storage.md#grow-the-data-disk) | Grow the stopped shared VM's data disk, or isolated machine NAME's, up to 4096 GiB. Never shrinks. |
| [`nsl doctor`](../getting-started/install.md#check-the-host) | Check the host's prerequisites. |
| [`nsl version`](limits.md#report-a-problem) | The build version. |
| [`nsl help`](../guides/machines.md) | Usage. |

## Names

- **Machine names** start with a lowercase ASCII letter, followed by lowercase letters, digits or interior hyphens, at most 24 characters.
- **`--user`** takes a POSIX account name: a lowercase letter or underscore, then lowercase letters, digits, underscores or hyphens, at most 32 characters.

## Guest commands

These commands are installed inside every machine image:

| Command | Behavior |
| --- | --- |
| [`nsl-open TARGET`](../guides/desktop.md) | Open an `http`/`https` URL or a path under `/mnt/host` with the host's default handler. Also `BROWSER` and the handler for web links. |
| [`nsl-path [--host \| --guest] PATH`](../guides/host-files.md#paths-and-aliases) | Translate between host and guest paths using the `/mnt/host` prefix. |

## Exit status

`nsl run` and `nsl` exit with the command's status, or 128+N when it was killed by signal N. Other commands exit 0 on success and non-zero with a message on stderr.

## Environment

| Variable | Effect |
| --- | --- |
| `NSL_HOME` | State directory. Default `$XDG_DATA_HOME/nsl`, or `~/.local/share/nsl`. |
| `XDG_CONFIG_HOME` | Where `nsl/nsl.conf` is read from. Default `~/.config`. |
| `NSL_WAYPIPE` | Path of the host's Waypipe. Default `waypipe` on `PATH`. |
| `NSL_OPENER` | The host command that opens links and files. Default `xdg-open`. |
| `WAYLAND_DISPLAY` | When set, commands start machines' desktop sessions. |
