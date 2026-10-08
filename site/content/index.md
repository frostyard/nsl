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

# Linux development on an atomic host {#keep-the-host-atomic-work-in-any-distro}

<p class="fy-hero__lede">Install your development tools in a Debian, Fedora or other Linux machine and leave the host alone. nsl works much like WSL: each machine keeps its packages, services and files between sessions. The machines run as systemd-nspawn containers inside a shared VM, start when you need them, and can work in your host files.</p>

[Install nsl →](getting-started/install.md){ .md-button .md-button--primary }
[How it works](concepts/how-it-works.md){ .md-button }

</div>
<img class="fy-hero__icon" src="assets/nsl.svg" alt="">
</div>

[Create your first machine](getting-started/first-machine.md) walks through creation, a login shell, running a command and stopping the VM.

## What a machine gives you

<div class="grid cards" markdown>

-   **A shell where you need it**

    ---

    Run `nsl` from your project directory to open a shell in the default machine, working in the same files. Use `nsl run` for a single command; its exit status comes back to the host.

-   **Your files and your account**

    ---

    Your `$HOME`, `/run/media/USER` and `/mnt` are available under `/mnt/host`. The machine uses your username, UID and GID, so files you create there still belong to you. You also get passwordless `sudo` inside the machine.

-   **Ports and windows on the host**

    ---

    Run a development server in the machine and reach its forwarded port on host `127.0.0.1`. Wayland applications can open windows on your desktop through Waypipe.

-   **Eight signed distros**

    ---

    Choose Debian, Ubuntu, Fedora, CentOS Stream, Arch, openSUSE Tumbleweed or Leap, or try the Azure Linux 4.0 beta. The images are rebuilt weekly. nsl verifies that they came from the signed Frostyard publishing workflow before using them.

-   **Isolation when you need it**

    ---

    Use `--isolated` for software you don't trust. It gets its own VM, without access to your host files, desktop or host actions.

-   **Runs as your user**

    ---

    nsl runs as your user. You'll need the host prerequisites installed first; nsl doesn't install packages or change device permissions, groups or sudoers.

</div>

## Start here

<div class="grid cards" markdown>

- <span class="fy-index">01</span> **[Get started](getting-started/install.md)**

    Check the host, install nsl and create your first machine.

- <span class="fy-index">02</span> **[Architecture](concepts/how-it-works.md)**

    How the VM runs your machines and connects them to the host.

- <span class="fy-index">03</span> **[Guides](guides/machines.md)**

    Work with machines, host files, ports, desktop applications and backups.

- <span class="fy-index">04</span> **[Command reference](reference/cli.md)**

    Look up commands, settings in `nsl.conf` and published images.

- <span class="fy-index">05</span> **[Limits and troubleshooting](reference/limits.md)**

    Check known limits and find help when a command fails.

</div>

!!! note "Pre-release"

    nsl has no stable release yet. v0.4.0 is the first release of this design; v0.3.0 and earlier are a retired prototype. The tested host is Snow Linux 13 on x86-64 with systemd 261.2, QEMU 10.0.13, virtiofsd 1.13.2 and GNOME Wayland.
