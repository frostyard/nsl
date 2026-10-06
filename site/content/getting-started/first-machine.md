---
description: Create a machine from a signed image, enter it, run commands and stop it.
---

# Your first machine

Let's create a Debian 13 machine, run a few commands and stop it. The first creation downloads the VM image and the Debian [machine image](../concepts/how-it-works.md#terms). Allow several minutes for the first download and setup; the time depends on your connection and host. Download progress is printed in bytes, followed by verification messages. Once both are cached, creating another machine from them needs no network.

<div class="steps" markdown>

1. **Create the machine.**

   ```sh
   nsl create debian --distro debian:13
   ```

   nsl downloads the signed image catalogue, checks that both images came from the Frostyard publishing workflow, and caches them. Then it starts the [shared VM](../concepts/how-it-works.md#terms) and creates your Debian machine. Since this is your first machine, it becomes the [default](../concepts/how-it-works.md#terms). The name and an image selection are required; the [command reference](../reference/cli.md#machines) also covers local images.

   Success ends with a line like this (`BUILD_ID` varies with the published image):

   ```text
   Created debian from BUILD_ID
   ```

   If startup stalls or fails, check [`nsl logs`](../guides/storage.md#logs). For a VM that cannot become ready, [`nsl recover`](../guides/storage.md#recover-a-vm) restarts it from a fresh root. See [troubleshooting](../reference/limits.md#troubleshooting).

2. **Open a shell.**

   ```sh
   mkdir -p ~/src/project
   cd ~/src/project
   nsl
   ```

   You're now in a login shell in the default machine, working in the same files. Their path starts with `/mnt/host`, followed by the canonical host path. Your username, UID and GID match the host's, and `sudo` needs no password. This works for directories under your home, `/run/media/USER` and `/mnt`. From anywhere else, nsl opens the shell in your guest home and tells you.

   For example, with host username `you` and home `/var/home/you`, a session looks like this (your prompt may differ):

   ```console
   you@debian:/mnt/host/var/home/you/src/project$ pwd
   /mnt/host/var/home/you/src/project
   you@debian:/mnt/host/var/home/you/src/project$ exit
   ```

   Type `exit` or press Ctrl-D to return to your host shell. Leaving the shell does not stop the machine immediately; it stays available until you stop it or the idle timeout expires. Run the remaining steps on the host.

3. **Run one command from the host.**

   ```sh
   nsl run cat /etc/os-release
   ```

   The output identifies Debian 13. `run` passes arguments literally, keeps stdout and stderr separate, and returns the command's exit status. For installing packages and running Podman, see [software in a machine](../guides/machines.md#software-in-a-machine).

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
