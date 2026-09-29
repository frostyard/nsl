---
description: How machines see your home, removable media and /mnt at /mnt/host, and how paths translate.
---

# Host files

Machines that are not isolated see three host trees, read-write, at `/mnt/host` followed by their host path:

| Host                               | In a machine              |
| ---------------------------------- | ------------------------- |
| Your home, such as `/var/home/you` | `/mnt/host/var/home/you`  |
| `/run/media/you`                   | `/mnt/host/run/media/you` |
| `/mnt`                             | `/mnt/host/mnt`           |

Nothing else from the host is shared: not `/usr`, `/etc`, `/tmp`, the rest of `/run`, or any pseudo-filesystem. Sharing is per machine, never per project, and every machine that is not isolated sees the same trees.

## Paths and aliases

Translation is a prefix: host path `P` is `/mnt/host/P` in a machine. nsl matches your working directory by device and inode, so symlinked and bind-mounted aliases translate too. On atomic hosts, `/home` is often a link to `/var/home`; the machine gets a matching relative link, `/mnt/host/home → var/home`, so both spellings work.

Every machine image includes `nsl-path` for scripts that pass paths between the two sides:

```sh
nsl-path /mnt/host/var/home/you/notes.md   # → /var/home/you/notes.md
nsl-path --guest /var/home/you/notes.md    # → /mnt/host/var/home/you/notes.md
```

Without a flag, a path under `/mnt/host` becomes a host path and any other absolute path becomes a guest path. Relative paths start from the current directory.

## Ownership and permissions

Files created through `/mnt/host` belong to you on the host. The share runs with your host user's permissions, so root in a machine cannot do anything to your files that you could not.

## What does not cross

- **Unix sockets.** Sockets under a shared tree do not connect across the share. D-Bus, SSH agent, GPU and display sockets are never shared.
- **File events from the host.** Edits made on the host produce no inotify events in machines, as with WSL's `/mnt/c`. File watchers, hot reload and watched builds belong in the guest home. Edits between machines do produce events, because machines share one kernel.
- **Automounts.** A host automount point that is not mounted yet appears empty in machines until the host mounts it.

## Isolated machines

[Isolated machines](isolated.md) see none of these trees. Their working directory never translates: a shell starts in the guest home, and `run` needs `--cd`.

## What this means for trust

A machine that can write your home can change your shell startup files, keys and nsl's own state. That is the model: a machine is trusted as you. The [trust model](../concepts/trust.md) explains why there is no read-only tier, and when to use `--isolated` instead.
