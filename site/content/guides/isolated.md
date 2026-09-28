---
description: Machines in a VM of their own, with no host files, desktop or host actions, for untrusted software.
---

# Isolated machines

An ordinary machine is trusted as you: it can read and write your home. For software you do not trust, create an isolated machine instead. It runs in its own small VM, with no access to your files or your desktop.

```sh
nsl create sandbox --distro fedora:44 --isolated
nsl -m sandbox
```

## What is different

| | Ordinary machine | Isolated machine |
| --- | --- | --- |
| VM | The shared VM, with other machines | Its own VM |
| Host files at `/mnt/host` | Home, `/run/media/USER`, `/mnt` | None |
| Windows on your desktop | Yes, through Waypipe | No |
| `nsl-open` to the host | Yes | No |
| Working directory | Translated from the host | Always the guest home; `run` needs `--cd` |
| Ports on host `127.0.0.1` | Yes | Yes |
| Resources | `[vm]` in `nsl.conf` | `[isolated]` in `nsl.conf`: 2 GiB and 2 CPUs by default |

The isolated VM boots the same VM image as the shared VM and runs only this machine. Its credential names no shares, and the only thing mounted from the host is the read-only, verified image cache, which the machine itself never sees.

```sh
nsl run -m sandbox --cd /home/you uname -a
```

## Managing its VM

Commands that cover every VM cover isolated ones too: `list`, `ports`, `logs`, `update` and `shutdown`. Two commands take the machine's name to address its VM:

```sh
nsl recover sandbox              # restart its VM from a fresh root
nsl resize sandbox --disk 256    # grow its stopped VM's data disk
```

Removing the machine stops its VM and deletes its disks.

## Choosing the tier

The tier is fixed when a machine is created or imported, and an archive never carries it. To move a machine between tiers, export it and import it under an unused name with or without `--isolated`:

```sh
nsl stop tool
nsl export tool ~/tool.tar
nsl import tool-sandboxed ~/tool.tar --isolated
```

See the [trust model](../concepts/trust.md) for what isolation does and does not protect.
