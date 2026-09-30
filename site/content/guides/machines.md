---
description: Create, enter, start, stop and remove machines; the default machine; your account; containers inside a machine.
---

# Machines

A machine keeps a Linux distro, its installed packages, services and guest home under a name you choose. You can work on several projects in one machine; to nsl, they're just directories. Machines run as systemd-nspawn containers in the shared VM. An [isolated](isolated.md) machine gets a VM of its own.

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

nsl verifies the cached image and imports it onto the VM's data disk. Then it sets the host time zone, hostname, account and `sudo` rule for the new machine.

If creation fails, nsl cleans up before making the name available again. If the VM can't confirm cleanup, `nsl list` shows the machine as `incomplete`. Run the suggested `nsl remove NAME --yes` command to retry cleanup and free the name.

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

This prevents a project command from silently running in the guest home when its host directory isn't shared.

## The default machine

The first machine you create or import becomes the default. Change it with `nsl default NAME`. If you remove the default machine, you'll need to choose another. Until you do, bare `nsl` fails and lists the available machines.

## Your account in a machine

- The username is your host username, unless `--user` chose another.
- The UID and primary GID are your host's, so files you create through `/mnt/host` are yours on the host.
- The home is `/home/USERNAME` on machine storage, separate from your host home. Keep dotfiles and build caches there.
- The shell is `/bin/bash`, and `sudo` needs no password.
- The hostname is the machine name.

Creation fails, and changes nothing, when the image already has an account with that name or UID.

## Start, stop and idle

You don't need to start the VM before using a machine. nsl starts it on demand and, by default, starts all its machines too. Set `autostart = false` in the [configuration](../reference/configuration.md) if you'd rather start each machine only when you use it.

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

Stop the machine before removing it. Removal is permanent, though your host files, cached images and exported archives stay where they are. Removing an isolated machine also deletes its VM and disks. If removal is interrupted, `nsl list` shows `Removing` and the name stays reserved. Run the same removal command to finish.

To keep a copy first, [export](export-import.md) the machine.
