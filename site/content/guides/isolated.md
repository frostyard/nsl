---
description: Machines in a VM of their own, with no host files, desktop or host actions, for untrusted software.
---

# Isolated machines

Use an isolated machine to try software you don't trust with your home directory. It runs in its own small VM, without access to your host files or desktop. An ordinary machine has read-write access to your home.

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

The isolated VM uses the same VM image as the shared VM, but runs only this machine. Its credential allows no host file shares. The VM can read the verified image cache on the host; the machine can't see even that mount.

```sh
nsl run -m sandbox --cd /home/you uname -a
```

## Managing its VM

`list`, `ports`, `logs`, `update` and `shutdown` include isolated VMs. To recover or resize one, pass its machine's name:

```sh
nsl recover sandbox              # restart its VM from a fresh root
nsl resize sandbox --disk 256    # grow its stopped VM's data disk
```

Removing the machine stops its VM and deletes its disks.

## Choosing the tier

You choose the tier when creating or importing a machine. To change it later, export the machine and import it under an unused name, with or without `--isolated`. The archive itself doesn't set the tier:

```sh
nsl stop tool
nsl export tool ~/tool.tar
nsl import tool-sandboxed ~/tool.tar --isolated
```

See the [trust model](../concepts/trust.md) for what isolation does and does not protect.
