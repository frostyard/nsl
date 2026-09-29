---
description: Create, enter, start, stop and remove machines; the default machine; your account; containers inside a machine.
---

# Machines

A machine is a named, persistent Linux system: a distro, its packages and services, and a guest home. Projects are ordinary directories, not machine properties. Machines run as systemd-nspawn containers in the shared VM, or, when [isolated](isolated.md), each in a VM of its own.

## Create a machine

```sh
nsl create NAME --distro DISTRO:RELEASE [--default] [--user NAME] [--isolated] [--offline]
```

- `--distro` takes a selector from `nsl images`, such as `debian:13`, `ubuntu:26.04` or `fedora:44`. See [machine images](../reference/machine-images.md).
- `--default` makes the new machine the default. The first machine is the default anyway.
- `--user` names the guest account instead of your host username.
- `--isolated` gives the machine its own VM without host access.
- `--offline` uses only verified images already in the cache.

Names start with a lowercase letter, followed by lowercase letters, digits or interior hyphens, up to 24 characters. Flags follow the name.

Creation verifies the cached image, imports it into the VM's data disk and applies only per-machine data: the host time zone, the hostname, the account and its `sudo` rule. A failed creation frees the name after cleanup succeeds. If the VM cannot confirm cleanup, `nsl list` shows the machine as `incomplete` and the error tells you to run `nsl remove NAME --yes`. That command retries cleanup before freeing the name.

## Enter a machine

| Command | What it does |
| --- | --- |
| `nsl` | A login shell in the default machine |
| `nsl -m NAME` | A login shell in NAME |
| `nsl run COMMAND [ARGS...]` | Run one command in the default machine |
| `nsl run -m NAME --root COMMAND` | Run it as guest root |
| `nsl run --cd /home/you/src COMMAND` | Run it in a guest directory |

`run` passes its arguments to the machine literally, with no shell in between. Stdout and stderr stay separate and binary-safe, and the command's exit status is `nsl`'s. A command killed by signal N exits with 128+N. `run` uses a terminal when stdin and stdout are terminals, so interactive programs work. Flags come before the command; `--` ends them.

`--root` means root in the machine, never on the host.

### The working directory

nsl translates the directory you run it from. A directory in a [shared tree](host-files.md) becomes `/mnt/host` followed by its path, so a build started from your checkout runs in the same files.

When the directory cannot be translated:

- a shell starts in the guest home and says so on stderr;
- `run` fails without running anything, unless `--cd` names an absolute guest directory.

A command meant for a project directory never runs somewhere else by accident.

## The default machine

There is at most one default. The first machine you create or import becomes it, and `nsl default NAME` changes it. Removing the default leaves none; bare `nsl` then fails and lists your machines instead of guessing.

## Your account in a machine

- The username is your host username, unless `--user` chose another.
- The UID and primary GID are your host's, so files you create through `/mnt/host` are yours on the host.
- The home is `/home/USERNAME` on machine storage, separate from your host home. Keep dotfiles and build caches there.
- The shell is `/bin/bash`, and `sudo` needs no password.
- The hostname is the machine name.

Creation fails, and changes nothing, when the image already has an account with that name or UID.

## Start, stop and idle

Commands start what they need. The shared VM starts on first use, and by default starts every machine with it; set `autostart = false` in the [configuration](../reference/configuration.md) to start machines on first use instead.

| Command | Effect |
| --- | --- |
| `nsl start NAME` | Start a machine, and its VM, and wait until it is ready |
| `nsl stop NAME` | Stop one machine; nothing is lost |
| `nsl shutdown` | Stop every machine and every nsl VM |

A machine with no nsl command sessions and no connected windows stops after `idle_timeout` minutes, 15 by default. Services running inside it do not keep it running. The VM powers itself off 60 seconds after its last machine stops and its last request ends. A command that arrives meanwhile waits and starts it again.

To keep machines running for services, disable automatic idle stopping in the [configuration](../reference/configuration.md):

```ini
[machines]
idle_timeout = 0
```

This setting applies to every machine. Each running VM picks up a changed value on its next machine start, command or SSH connection. It does not prevent an explicit `nsl stop`, `nsl shutdown` or host shutdown.

## Software in a machine

Install and update software with the machine's own package manager: `apt-get`, `dnf`, `pacman` or `zypper`. A newer machine image in the catalogue affects machines created later, never an existing one.

Machine images are prepared for nested containers, and rootless Podman works inside a machine:

```sh
nsl run sudo apt-get update
nsl run sudo apt-get install -y podman
nsl run podman run --rm docker.io/library/alpine echo hello
```

Per-project tools such as devcontainers and Compose run inside a machine too.

## Remove a machine

```sh
nsl stop old
nsl remove old         # preview what would be deleted
nsl remove old --yes   # delete it
```

Removal needs a stopped machine and cannot be undone. It keeps host files, cached images and exported archives. An isolated machine's VM and its disks go with it. The name stays reserved until deletion finishes; an interrupted removal shows as `Removing` in `nsl list`, and the same command resumes it.

To keep a copy first, [export](export-import.md) the machine.
