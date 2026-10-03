---
description: Where nsl keeps state, growing the data disk, recovering a VM and reading logs.
---

# Storage and recovery

## Where state lives

Machines, disks and keys live under `NSL_HOME`, which defaults to `$XDG_DATA_HOME/nsl` or `~/.local/share/nsl`. Settings go in a separate file, [`nsl.conf`](../reference/configuration.md).

| Path in `NSL_HOME` | Content |
| --- | --- |
| `vm/` | The shared VM: its record, root overlay, data disk, keys and boot credential |
| `isolated/NAME/` | The same for isolated machine NAME's VM |
| `machines/NAME.json` | A machine's record: ID, trust tier, account and image |
| `machines/NAME.ssh/` | The machine's key for [`ssh-config`](editors.md) |
| `default` | The default machine's name |
| `images/`, `delivery/` | The verified image cache and catalogue history |

nsl checks the owner, type and permissions of these files before changing anything, and never overwrites a machine, image, disk or archive it did not create.

## The data disk

Each VM has two disks. nsl can replace the root overlay from the cached VM image. The data disk needs more care: it holds your machines and the VM's own state.

The data disk starts at 128 GiB of virtual capacity and takes host space only as it fills. Machines in a VM share it. `nsl list` shows each VM's capacity.

## Grow the data disk

```sh
nsl shutdown
nsl resize --disk 256            # the shared VM
nsl resize sandbox --disk 192    # isolated machine sandbox's VM
```

The VM must be stopped. Capacity only grows, up to 4096 GiB; shrinking is not supported. The VM grows its filesystem at its next boot. If growth is interrupted, `nsl list` shows it as pending and the VM will not start until `resize` or `recover` finishes it.

## Recover a VM

```sh
nsl recover            # the shared VM
nsl recover sandbox    # isolated machine sandbox's VM
```

`recover` stops the VM and finishes any interrupted disk growth. It checks the data disk without repairing it, then starts the VM with a fresh root. Your machines, keys and pinned host keys stay in place.

Keep backups of machines you need. `recover` can't rebuild deleted keys or repair a damaged filesystem. Use [export](export-import.md) to save a copy before you need one.

## Logs

```sh
nsl logs          # every VM, its port forwarder and desktop sessions
nsl logs dev      # the desktop session of ordinary machine dev
```

For an isolated machine, `nsl logs NAME` also includes its own VM and forwarder. With a name, `logs` prints a hint for reading the machine's own journal:

```sh
nsl run -m dev --root journalctl -n 100
```

A VM that fails to become ready keeps its disks, and the error points to `logs` and `recover`.
