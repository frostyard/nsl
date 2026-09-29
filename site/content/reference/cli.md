---
description: Every nsl command, its options, name rules, guest commands and environment variables.
---

# Commands

`nsl help` prints a summary. Flags follow the machine name.

## Machines

| Command | Behavior |
| --- | --- |
| `nsl [-m NAME]` | Login shell in NAME or the default machine, in the translated host directory or the guest home. |
| `nsl run [-m NAME] [--root] [--cd PATH] COMMAND [ARGS...]` | Run a command in the machine. Arguments pass literally; a terminal is used when stdin and stdout are terminals. Flags precede COMMAND; `--` ends them. |
| `nsl create NAME --distro DISTRO:RELEASE [--offline] [--isolated] [--default] [--user NAME]` | Create a machine from a verified catalogue image. Selects the catalogue's VM image first when none is selected. |
| `nsl create NAME --image FILE --digest sha256:HEX [--isolated] [--default] [--user NAME]` | Create a machine from a local machine image, offline. |
| `nsl list` | Every nsl VM with its state, image, resources and data disk, anything pending until its next start, and every machine with its state, image, trust tier and default marker. |
| `nsl default NAME` | Make NAME the default machine. |
| `nsl start NAME` | Start a machine, and its VM if needed, and wait for readiness. |
| `nsl stop NAME` | Stop one machine and keep all its state. |
| `nsl shutdown` | Stop every machine and every nsl VM, isolated ones included. |
| `nsl remove NAME [--yes]` | Preview, then permanently remove a stopped machine. |

## Archives

| Command | Behavior |
| --- | --- |
| `nsl export NAME FILE` | Write an archive of a stopped machine. Never overwrites. |
| `nsl import NAME FILE [--isolated]` | Verify an archive and create a machine from it under an unused name. |

## Host integration

| Command | Behavior |
| --- | --- |
| `nsl ports [NAME]` | Forwarded ports and conflicts, for one machine or all. |
| `nsl ssh-config NAME` | Start the machine if needed and print an SSH host alias, `nsl-NAME`, for remote editors. |
| `nsl logs [NAME]` | Recent logs of the host units nsl runs: each VM, its forwarder and desktop sessions, or those of one machine. |

## Images

| Command | Behavior |
| --- | --- |
| `nsl images [--offline \| --refresh]` | Authenticated machine-image selections and the VM image in effect. The default reuses a catalogue checked less than one hour ago; `--refresh` checks immediately. |
| `nsl pull DISTRO:RELEASE [--offline]` | Verify and cache a machine image without creating a machine. |
| `nsl update [--offline]` | Select the catalogue's current VM image for the next start of each nsl VM. |
| `nsl update --image FILE --digest sha256:HEX` | Select a local VM image instead. |

A selector may end in `@sha256:HEX` to pin one image's OCI manifest, which must still be in the current catalogue.

## Maintenance

| Command | Behavior |
| --- | --- |
| `nsl config` | The effective configuration, each value's source, and any change waiting for a VM restart. |
| `nsl recover [NAME]` | Restart the shared VM, or isolated machine NAME's VM, from a fresh root; check its data disk and resume interrupted work. |
| `nsl resize [NAME] --disk GiB` | Grow the stopped shared VM's data disk, or isolated machine NAME's, up to 4096 GiB. Never shrinks. |
| `nsl doctor` | Check the host's prerequisites. |
| `nsl version` | The build version. |
| `nsl help` | Usage. |

## Names

- **Machine names** start with a lowercase ASCII letter, followed by lowercase letters, digits or interior hyphens, at most 24 characters.
- **`--user`** takes a POSIX account name: a lowercase letter or underscore, then lowercase letters, digits, underscores or hyphens, at most 32 characters.

## Guest commands

Every machine image installs these:

| Command | Behavior |
| --- | --- |
| `nsl-open TARGET` | Open an `http`/`https` URL or a path under `/mnt/host` with the host's default handler. Also `BROWSER` and the handler for web links. |
| `nsl-path [--host \| --guest] PATH` | Translate between host and guest paths using the `/mnt/host` prefix. |

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
