---
description: Create a machine from a signed image, enter it, run commands and stop it.
---

# Your first machine

Let's create a Debian 13 machine, run a few commands and stop it. The first creation downloads the VM image and the Debian machine image. Once both are cached, creating another machine from them needs no network.

<div class="steps" markdown>

1. **Create the machine.**

   ```sh
   nsl create [name] [flags]
   nsl create debian --distro debian:13
   ```

   nsl downloads the signed image catalogue, checks that both images came from the Frostyard publishing workflow, and caches them. Then it starts the shared VM and creates your Debian machine. Since this is your first machine, it becomes the default.

2. **Open a shell.**

   ```sh
   cd ~/src/project
   nsl              # a login shell at /mnt/host/home/src/project
   ```

   You're now in a login shell in the default machine, working in the same files. Their path starts with `/mnt/host`, followed by the host path. Your username, UID and GID match the host's, and `sudo` needs no password. This works for directories under your home, `/run/media/USER` and `/mnt`. From anywhere else, nsl opens the shell in your guest home and tells you.

3. **Run one command.**

   ```sh
   nsl run sudo apt-get update
   nsl run sudo apt-get install -y podman
   nsl run podman run --rm docker.io/library/alpine echo hello
   ```

   These commands install Podman in the machine and use it to run an Alpine container. `run` passes arguments literally, keeps stdout and stderr separate, and returns the command's exit status.

4. **Look around.**

   ```sh
   nsl list     # the VM, its resources and data disk, and every machine
   nsl images   # every published machine image
   ```

5. **Stop.**

   ```sh
   nsl shutdown
   ```

   You can usually leave this to nsl. A machine stops after 15 minutes without nsl sessions or windows, and the VM stops a minute after its last machine. The next `nsl` command starts them again.

   Starting from a stopped VM adds a few seconds to the command.

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
- [Trust model](../concepts/trust.md) explains what a machine can do with your files.
