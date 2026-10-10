---
description: How machines see your home, removable media and /mnt at /mnt/host, and how paths translate.
---

# Host files

In an ordinary machine, you can read and write files from three places on the host. Add `/mnt/host` to the front of their host paths:

| Host                               | In a machine              |
| ---------------------------------- | ------------------------- |
| Your home, such as `/home/you`     | `/mnt/host/home/you`  |
| `/run/media/you`                   | `/mnt/host/run/media/you` |
| `/mnt`                             | `/mnt/host/mnt`           |

Every ordinary machine sees the trees that exist as directories when the shared VM launches. For example, if `/run/media/USER` is created after launch, restart the VM with `nsl shutdown` and then `nsl` to share it. Creating a machine for a project doesn't limit it to that project's directory. Host `/usr`, `/etc`, `/tmp`, the rest of `/run` and pseudo-filesystems are not shared.

## Paths and aliases

Translation is a prefix: host path `P` is `/mnt/host/P` in a machine. nsl matches your working directory by device and inode, so symlinked and bind-mounted aliases translate too. For example, on some atomic hosts, `/home` links to `/var/home`; the machine gets a matching relative link, `/mnt/host/home → var/home`, so both spellings work.

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

Write access to your home includes your shell startup files, keys and nsl's own state. Treat software in an ordinary machine as software running with your permissions. The [trust model](../concepts/trust.md) explains why there is no read-only tier and when to use `--isolated`.
