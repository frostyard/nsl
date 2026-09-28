---
description: Where nsl keeps state, growing the data disk, recovering a VM and reading logs.
---

# Storage and recovery

## Where state lives

nsl keeps its state in `NSL_HOME`, by default `$XDG_DATA_HOME/nsl` or `~/.local/share/nsl`. Settings live apart from it, in [`nsl.conf`](../reference/configuration.md).

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

Each VM has two disks. The root is a throwaway overlay on the cached VM image. The data disk holds the machines and the VM's own state, and is what matters.

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

`recover` stops the VM, finishes interrupted growth, checks the data disk without repairing it, and starts the VM again from a fresh root. Machines, keys and pinned host keys stay.

`recover` is not a backup. It cannot rebuild deleted keys or repair a damaged filesystem. [Export](export-import.md) machines you cannot lose.

## Logs

```sh
nsl logs          # the VM, its port forwarder and every desktop session
nsl logs dev      # those of one machine
```

A VM that fails to become ready keeps its disks, and the error points to `logs` and `recover`.
