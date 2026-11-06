<!-- readme-kind: tool -->
<img src="assets/nsl.svg" alt="nsl icon" width="112" align="right">

# nsl

[![Tests](https://github.com/frostyard/nsl/actions/workflows/ci.yml/badge.svg)](https://github.com/frostyard/nsl/actions/workflows/ci.yml)

**Leave the host alone. Work in another distro.**

nsl gives you persistent Linux machines on a Linux host, as WSL does on Windows. Use one to `apt install` a project's dependencies, try a toolchain packaged for another distro, or run a service, and keep your host clean. Each machine is a whole distro with its own packages and services. It opens in the directory you were in, works on your files, and stops when you stop using it.

**Status:** pre-release. v0.4.0 was the first release of the current design; the tested host is Snow Linux 13.

**Documentation:** [frostyard.github.io/nsl](https://frostyard.github.io/nsl/)

## What it does

- **Eleven signed machine images.** Debian 13 and testing, Ubuntu 26.04 LTS and 24.04 LTS, Fedora 44 and Rawhide, CentOS Stream 10, Arch, openSUSE Tumbleweed and Leap 16.0, and the Azure Linux 4.0 beta, rebuilt weekly and verified against the Frostyard publishing workflow before use. [Machine images](https://frostyard.github.io/nsl/reference/machine-images/)
- **Your files and your account.** Your home, removable media and `/mnt` appear at `/mnt/host`, and you have your own username, UID and GID with passwordless `sudo`. [Host files](https://frostyard.github.io/nsl/guides/host-files/)
- **Ports and windows on the host.** A server in a machine is reachable at the same port on host `127.0.0.1`, and Wayland applications open windows on your desktop. [Ports](https://frostyard.github.io/nsl/guides/ports/), [desktop applications](https://frostyard.github.io/nsl/guides/desktop/)
- **Your editor.** `nsl ssh-config` gives VS Code or any SSH client a host alias for a machine. [Editors over SSH](https://frostyard.github.io/nsl/guides/editors/)
- **Isolation when you need it.** `--isolated` puts a machine in a VM of its own, with no host files, desktop or host actions, for software you do not trust. [Isolated machines](https://frostyard.github.io/nsl/guides/isolated/)
- **Backups and moves.** Export a stopped machine to an archive, and import it later or on another host. [Export and import](https://frostyard.github.io/nsl/guides/export-import/)
- **A host left alone.** nsl runs as your user, with no daemon of its own. It installs no host packages and changes no device permissions, groups or sudoers.

## Requirements

An x86-64 Linux host with KVM, systemd-vmspawn from systemd 259 or newer (as in Fedora 44 and Ubuntu 26.04 LTS), QEMU with UEFI firmware, virtiofsd and membership in the `kvm` group; Waypipe adds desktop windows. `nsl doctor` checks each requirement.

## How it works

Machines are systemd-nspawn containers inside one small VM, launched with systemd-vmspawn and QEMU/KVM. The VM's root is replaceable and holds no user state; your machines live on its data disk. [How nsl works](https://frostyard.github.io/nsl/concepts/how-it-works/)

An ordinary machine is trusted as you: it can read and write your home, including keys and tokens, just as WSL can. It cannot become root on the host or reach host sockets. Use `--isolated` for anything you would not run as yourself. [Trust model](https://frostyard.github.io/nsl/concepts/trust/)

## Install

```sh
brew tap frostyard/tap
brew install --cask frostyard/tap/nsl
nsl doctor
```

[Install](https://frostyard.github.io/nsl/getting-started/install/) lists the host requirements and the other ways: a release tarball with its provenance, or a build from source.

## Use

```sh
nsl create debian --distro debian:13   # the first machine becomes the default
nsl                                    # a login shell in the default machine, here
nsl run cat /etc/os-release            # 1 command from the host
```

[Create your first machine](https://frostyard.github.io/nsl/getting-started/first-machine/) walks through these steps. The [command reference](https://frostyard.github.io/nsl/reference/cli/) and the [configuration reference](https://frostyard.github.io/nsl/reference/configuration/) cover the rest.

## Problem records

On a panic or 1 of 4 internal errors, a release build of nsl writes 1 entry to your local systemd journal: its app ID, its version, the error code or the panic's function names (no source file paths and no arguments), and the error or panic message. nsl sends nothing over the network; chairlift's Problems page can show the entries. It keeps a small crash file in `~/.local/state/frostyard-problems/org.frostyard.nsl` for crashes, and with no journal it drops each entry silently. [Problem records](https://frostyard.github.io/nsl/reference/limits/#problem-records)

## Limits

- Windows are Wayland only.
- Host file edits produce no inotify events in machines.
- Idle machines stop even when a service inside them is busy.
- v0.3.0 and earlier are a retired prototype.

[Limits and troubleshooting](https://frostyard.github.io/nsl/reference/limits/) has the full list.

## Credits

The original [WSL](https://wsl.dev/) is the biggest inspiration. Other notable projects:

- [nspawn.org](https://nspawn.org), which recently relaunched and gave the idea.
- [systemd](https://systemd.io), which does nearly all the heavy lifting with systemd-vmspawn and systemd-nspawn.
- [Lima](https://github.com/lima-vm/lima), an early pioneer in this space with a slightly different intended use.

## Develop

```sh
mise install   # the pinned Go, golangci-lint, Node and svu
make ci        # what CI runs
```

[Build from source](https://frostyard.github.io/nsl/contributing/build/) covers the CLI, the images and the tests.

## Where to go next

- [AGENTS.md](AGENTS.md): the rules for agents and contributors.
- [docs/README.md](docs/README.md): the decisions, designs, specs and plans.
- [SECURITY.md](SECURITY.md): report a vulnerability privately.
- [LICENSE](LICENSE): MIT. [Third-party license notices](THIRD_PARTY_NOTICES.txt).
