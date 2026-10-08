---
description: The published machine images, their selectors, and what every image provides.
---

# Machine images

Choose from the machine images below, rebuilt by Frostyard at least weekly. Run `nsl images` to see what's available in the current authenticated catalogue. By default, it reuses a catalogue checked less than one hour ago. Use `nsl images --refresh` to check immediately.

| Distro | Selectors | Family |
| --- | --- | --- |
| Debian 13 | `debian:13`, `debian:trixie` | debian |
| Ubuntu 26.04 LTS | `ubuntu:26.04`, `ubuntu:resolute` | debian |
| Fedora 44 | `fedora:44` | rpm |
| CentOS Stream 10 | `centos:10`, `centos-stream:10` | rpm |
| Arch Linux | `arch:rolling`, `arch:btw` | arch |
| openSUSE Tumbleweed | `opensuse:tumbleweed`, `opensuse-tumbleweed:rolling` | suse |
| openSUSE Leap 16.0 | `opensuse:16.0`, `opensuse-leap:16.0` | suse |
| Azure Linux 4.0 (beta) | `azurelinux:4.0`, `azure-linux:4.0` | rpm |

All images are x86-64. A new distro or release has to pass the same acceptance tests before it's added.

## What every image provides

- **Your account**, added at creation: your username, UID and GID, a home at `/home/USER`, `/bin/bash` and passwordless `sudo`. Root has no usable password.
- **A login session** for shells and commands run as your machine account: a PAM login with a logind session, `XDG_RUNTIME_DIR`, a user systemd manager and a user D-Bus session. `nsl run --root` does not create a PAM/logind session or provide `XDG_RUNTIME_DIR` or a user manager.
- **Networking from the VM.** The machine's own networkd and resolved are masked; it uses the VM's network and resolver.
- **Nested containers.** A full procfs for nesting and a Podman drop-in, so rootless Podman works.
- **Desktop support.** Time-zone data, a font, a cursor theme and the Wayland client libraries.
- **The `C.UTF-8` locale.** Install other locales with the distro's packages.
- **`nsl-open` and `nsl-path`**, with `nsl-open` as the handler for web links.
- **Your directory for new terminal tabs.** Interactive bash and zsh report the working directory with OSC 7 when the terminal asks for it, so [Igloo ↗](https://github.com/frostyard/igloo) opens new tabs in the directory you were in.
- **An OpenSSH server**, with no service enabled, for [`ssh-config`](../guides/editors.md).
- **A unique identity per machine.** Each machine gets its own machine ID on first boot. Images ship no SSH host keys and no package-keyring private keys.

## Distro notes

- **CentOS Stream 10** enables EPEL, and adds GLES libraries so GTK 4 applications render.
- **Arch Linux** initializes and populates its pacman keyring on the machine's first boot.
- **openSUSE** images add `glibc-locale-base` for `C.UTF-8`.
- **Azure Linux 4.0** is a beta, and its machines install packages from Microsoft's beta repositories. Those repositories offer few desktop applications; GTK 3 applications such as `gtk-lshw` open windows.

The full [machine-image contract ↗](https://github.com/frostyard/nsl/blob/main/docs/specs/machine-images.md) is in the repository.
