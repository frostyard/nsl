---
description: What a machine can and cannot do on the host, why there is no read-only tier, and when to isolate.
---

# Trust model

A machine is a trusted extension of you. The VM keeps a machine's kernel, packages, services and root away from the host system. It does not keep the machine away from your files: a machine that is not isolated can read and write your home, as you.

This is the same bargain WSL makes, and we state it plainly so you can choose.

## What an ordinary machine can do

- Read and write your home, `/run/media/USER` and `/mnt`, with your host permissions. That includes shell startup files, SSH keys, tokens and nsl's own state in `NSL_HOME`.
- Open windows on your desktop through Waypipe.
- Ask the host to open web links, and files in the shared trees, with your default handlers.
- Listen on ports that the host forwards on `127.0.0.1`.

## What it cannot do

- Become root on the host. `--root` and `sudo` mean root in the machine. Files it creates through `/mnt/host` belong to you, and it cannot exceed your permissions.
- See host `/usr`, `/etc`, `/tmp`, the rest of `/run`, devices or pseudo-filesystems.
- Reach host sockets: D-Bus, the SSH agent, the GPU and the display are not shared, and Unix sockets do not connect across `/mnt/host`.
- Run arbitrary commands on the host. The only host action is opening a link or a shared file.
- Change host sudoers, device permissions or packages. nsl itself never changes them either.

## Machines share a VM

Machines in the shared VM are not isolated from one another. Root in any machine is effectively root in the VM, and so can reach every other machine there. Treat the machines in the shared VM as one trust domain.

## Why there is no read-only tier

Read access to a home already exposes keys and credentials, so a read-only share would suggest a protection that does not exist. And a writable share cannot be made safe with per-feature opt-ins: a machine that can write your startup files can do anything you can. nsl offers two honest tiers instead.

## Isolated machines

For software you do not trust, use [`--isolated`](../guides/isolated.md). An isolated machine runs in its own VM, with no host files, no desktop session and no host actions. Its ports still reach host loopback. Isolation is chosen when a machine is created or imported, and is never removed from an archive.

## Images and archives

- Published images are verified against the signed Frostyard publishing workflow before use, and no switch disables that. See [how images are verified](../guides/images.md#how-images-are-verified).
- Local images selected with `--image` and `--digest` are a developer path. The digest proves the bytes, not their origin.
- [Archives](../guides/export-import.md) are unencrypted and can contain credentials. Their checksums detect damage, not tampering.
