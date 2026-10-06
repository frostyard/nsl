---
description: What a machine can and cannot do on the host, why there is no read-only tier, and when to isolate.
---

# Trust model

An ordinary machine can read and write your home with your permissions. That includes your keys, shell configuration and nsl's own state. Only run software there that you'd trust with those files.

The VM separates the machine's kernel, packages, services and root from your host system. The file sharing works much like WSL: keeping packages off the host doesn't protect the files you share with the machine.

## What an ordinary machine can do

- Read and write your home, `/run/media/USER` and `/mnt`, with your host permissions. That includes shell startup files, SSH keys, tokens and nsl's own state in `NSL_HOME`.
- Open windows on your desktop through Waypipe.
- Ask the host to open web links, and files in the shared trees, with your default handlers.
- Listen on ports that the host forwards on `127.0.0.1`.

## What it cannot do

- Become root on the host. `--root` and `sudo` mean root in the machine. Files it creates through `/mnt/host` belong to you, and it cannot exceed your permissions.
- See host `/usr`, `/etc`, `/tmp`, the rest of `/run`, devices or pseudo-filesystems.
- Reach host sockets: D-Bus, the SSH agent, the GPU and the display are not shared, and Unix sockets do not connect across `/mnt/host`.
- Run arbitrary commands through the host-action broker, which only opens links and shared files. A machine can still write shell startup files or other code in your shared home that later runs as you on the host.
- Change host sudoers, device permissions or packages. nsl itself never changes them either.

## Machines share a VM

The machines in the shared VM can reach one another. Root in any one of them is effectively root in the VM, with access to the other machines. Use the same level of trust for all machines in that VM.

## Why there is no read-only tier

A read-only home still exposes your keys and credentials. A writable home lets a machine change your shell startup files and run code as you later. Turning individual integration features off doesn't fix either problem. That's why nsl has two choices: share your files with a machine you trust, or use an isolated machine without those shares.

## Isolated machines

For software you do not trust, use [`--isolated`](../guides/isolated.md). An isolated machine runs in its own VM, with no host files, no desktop session and no host actions. Its ports still reach host loopback. You choose the trust tier when creating or importing the machine; nsl never takes that choice from an archive.

## Images and archives

- Published images are verified against the signed Frostyard publishing workflow before use, and no switch disables that. See [how images are verified](../guides/images.md#how-images-are-verified).
- Local images selected with `--image` and `--digest` are a developer path. The digest proves the bytes, not their origin.
- [Archives](../guides/export-import.md) are unencrypted and can contain credentials. Their checksums detect damage, not tampering.
