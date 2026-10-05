---
description: Every nsl command, its options, name rules, guest commands and environment variables.
---

# Commands

Run `nsl help` for a quick command summary. For commands that take a machine name, put the flags after the name.

## Machines

| Command | Behavior |
| --- | --- |
| `nsl [-m NAME]` | Login shell in NAME or the default machine, in the translated host directory or the guest home. |
| `nsl run [-m NAME] [--root] [--cd PATH] COMMAND [ARGS...]` | Run a command in the machine. Arguments pass literally; a terminal is used when stdin and stdout are terminals. Flags precede COMMAND; `--` ends them. |
| `nsl create NAME --distro DISTRO:RELEASE [--offline] [--isolated] [--default] [--user NAME]` | Create a machine from a verified catalogue image. Selects the catalogue's VM image first when none is selected. |
| `nsl create NAME --image FILE --digest sha256:HEX [--isolated] [--default] [--user NAME]` | Create a machine from a local machine image, offline. |
| `nsl list [--json]` | Every nsl VM with its state, image, resources and data disk, anything pending until its next start, and every machine with its state, image, trust tier and default marker. |
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
| `nsl images [--offline \| --refresh] [--json]` | Authenticated machine-image selections and the VM image in effect. The default reuses a catalogue checked less than one hour ago; `--refresh` checks immediately. |
| `nsl pull DISTRO:RELEASE [--offline]` | Verify and cache a machine image without creating a machine. |
| `nsl update [--offline]` | Select the catalogue's current VM image for the next start of each nsl VM. |
| `nsl update --image FILE --digest sha256:HEX` | Select a local VM image instead. |

Add `@sha256:HEX` to a selector to pin an image's OCI manifest. That digest must still be in the current catalogue.

## Maintenance

| Command | Behavior |
| --- | --- |
| `nsl config [--json]` | The effective configuration, each value's source, and any change waiting for a VM restart. |
| `nsl recover [NAME]` | Restart the shared VM, or isolated machine NAME's VM, from a fresh root; check its data disk and resume interrupted work. |
| `nsl resize [NAME] --disk GiB` | Grow the stopped shared VM's data disk, or isolated machine NAME's, up to 4096 GiB. Never shrinks. |
| `nsl doctor` | Check the host's prerequisites. |
| `nsl version` | The build version. |
| `nsl help` | Usage. |

## Output for scripts

`nsl list`, `nsl images` and `nsl config` print tables for people. Add `--json` and they print one JSON document instead, with the same facts as typed values: numbers, booleans, full digests and the line a setting came from. Use it from scripts, editors and terminals rather than parsing the tables.

```bash
nsl list --json | jq -r '.machines[] | select(.state == "running") | .name'
```

On failure nothing is printed on stdout; the message goes to stderr and the exit status is non-zero. Ignore fields you do not know; nsl may add more. The [CLI contract ↗](https://github.com/frostyard/nsl/blob/main/docs/specs/cli.md#machine-readable-output) lists every field.

## Names

- **Machine names** start with a lowercase ASCII letter, followed by lowercase letters, digits or interior hyphens, at most 24 characters.
- **`--user`** takes a POSIX account name: a lowercase letter or underscore, then lowercase letters, digits, underscores or hyphens, at most 32 characters.

## Guest commands

These commands are installed inside every machine image:

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
