---
description: Create a machine from a signed image, enter it, run commands and stop it.
---

# Your first machine

This walk-through creates a Debian 13 machine, works in it and stops it. It takes one download of the VM image and the machine image; after that, creation needs no network.

<div class="steps" markdown>

1. **Create the machine.**

   ```sh
   nsl create [name] [flags]
   nsl create debian --distro debian:13
   ```

   nsl fetches the signed image catalogue, verifies the VM image and the Debian machine image against the Frostyard publishing workflow, and caches both. It starts the shared VM and creates the machine. Your first machine becomes the default.

2. **Open a shell.**

   ```sh
   cd ~/src/project
   nsl              # a login shell at /mnt/host/home/src/project
   ```

   You get a login shell in the default machine, in `/mnt/host` followed by the directory you were in. Your username, UID and GID match the host's, and `sudo` needs no password. Directories under your home, `/run/media/USER` and `/mnt` translate this way; from anywhere else, the shell starts in your guest home and says so.

3. **Run one command.**

   ```sh
   nsl run sudo apt-get update
   nsl run sudo apt-get install -y podman
   nsl run podman run --rm docker.io/library/alpine echo hello
   ```

   `run` passes its arguments literally, keeps stdout and stderr apart, and exits with the command's status.

4. **Look around.**

   ```sh
   nsl list     # the VM, its resources and data disk, and every machine
   nsl images   # every published machine image
   ```

5. **Stop.**

   ```sh
   nsl shutdown
   ```

   You rarely need to. A machine stops after 15 minutes without nsl sessions or windows, and the VM stops a minute after its last machine. The next `nsl` command starts them again.

   `nsl` startup takes just a few seconds, so a running a command when the system is stopped takes just a little bit longer.

</div>

## Add another distro

```sh
nsl create fedora --distro fedora:44
nsl -m fedora            # a shell in the fedora machine
nsl run -m fedora rpm -q systemd
nsl default fedora       # make fedora the machine bare `nsl` enters
```

Both machines run in the same VM and see the same host files. Each has its own packages, services and guest home.

## Next

- [Machines](../guides/machines.md) covers entering, the default machine, lifecycle and removal.
- [Host files](../guides/host-files.md) explains `/mnt/host` and directory translation.
- [Trust model](../concepts/trust.md) says what a machine can do with your files.
