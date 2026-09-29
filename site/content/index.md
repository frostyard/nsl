---
title: WSL-style Linux machines for Linux
description: WSL-style Linux machines for Linux hosts.
hide:
  - navigation
  - toc
---

<div class="fy-hero" markdown>
<div class="fy-hero__text" markdown>
<p class="fy-eyebrow">NSpawn Subsystem for Linux</p>

# Keep the host atomic. _Work in any distro._

<p class="fy-hero__lede">nsl gives a Linux host persistent Linux machines, as WSL does for Windows. Each machine is a whole distro with its own packages and services. It runs as a systemd-nspawn container in one small VM, starts when you use it, and works in your files.</p>

[Install nsl →](getting-started/install.md){ .md-button .md-button--primary }
[How it works](concepts/how-it-works.md){ .md-button }

</div>
<img class="fy-hero__icon" src="assets/nsl.svg" alt="NSL">
</div>

```sh
nsl create debian --distro debian:13   # verify the signed images; the first machine is the default
nsl                                    # a login shell in the machine, in this directory
nsl run make test                      # one command, with its exit status
```

## What a machine gives you

<div class="grid cards" markdown>

- **Any distro, one command away**

  ***

  `nsl` opens a login shell in your default machine, in the directory you were in. `nsl run` runs one command and returns its exit status.

- **Your files and your account**

  ***

  Your `$HOME`, `/run/media/USER` and `/mnt` appear at `/mnt/host`. Inside, you keep your username, UID and GID, and passwordless `sudo`.

- **Ports and windows on the host**

  ***

  A server listening in a machine is reachable at the same port on host `127.0.0.1`. Wayland applications open windows on your desktop.

- **Seven signed distros**

  ***

  Debian, Ubuntu, Fedora, CentOS Stream, Arch, openSUSE Tumbleweed and Leap. Rebuilt weekly, and verified against the signed Frostyard publishing workflow before use.

- **Isolation when you need it**

  ***

  `--isolated` gives a machine a VM of its own, with no access to host files, desktop or host actions, for software you do not trust.

- **A clean host**

  ***

  nsl runs as your user. It installs no host packages and changes no device permissions, groups or sudoers.

</div>

## Start here

<div class="grid cards" markdown>

- <span class="fy-index">01</span> **[Get started](getting-started/install.md)**

  Check the host, install nsl and create your first machine.

- <span class="fy-index">02</span> **[Architecture](concepts/how-it-works.md)**

  One VM, many machines, and what a machine may touch on the host.

- <span class="fy-index">03</span> **[Look it up, baby](reference/cli.md)**

  Every command, every setting in `nsl.conf`, and every published image.

</div>

!!! note "Pre-release"

    nsl has no stable release yet. v0.4.0 is the first release of this design; v0.3.0 and earlier are a retired prototype. The tested host is Snow Linux 13 on x86-64 with systemd 261.2, QEMU 10.0.13, virtiofsd 1.13.2 and GNOME Wayland.
