---
description: Host requirements, installing with Homebrew, downloading and verifying a release, building from source, and nsl doctor.
---

# Install nsl

nsl is a single binary that runs as your user. You'll need an x86-64 Linux host with KVM to run the x86-64 VM and machine images. The tested setup is Snow Linux 13 with systemd 261.2, QEMU 10.0.13, virtiofsd 1.13.2 and GNOME Wayland.

## Host requirements

| Requirement                                                                                        | Used for                                              |
| -------------------------------------------------------------------------------------------------- | ----------------------------------------------------- |
| `systemd-vmspawn`, `systemd-run`, `systemctl` and a user systemd manager                           | Running each VM as a user unit                        |
| QEMU (`qemu-system-x86_64`, `qemu-img`) with KVM                                                   | The VM and its disks                                  |
| UEFI firmware without Secure Boot, with a QEMU firmware descriptor (Debian's `ovmf`)               | Booting the VM                                        |
| virtiofsd at `/usr/libexec/virtiofsd`                                                              | Sharing your files and the image cache with the VM    |
| OpenSSH (`ssh`, `ssh-keygen`) and `/usr/lib/systemd/systemd-ssh-proxy`                             | Reaching the VM's agent over vsock                    |
| `sg` or util-linux `newgrp` (on Debian 14, both from `util-linux-extra`), and util-linux `unshare` | Opening the KVM devices through your `kvm` membership |
| `getent` and `id` | Checking `kvm` membership in the host account database |
| Membership in `kvm`, with access to `/dev/kvm` and `/dev/vhost-vsock`                              | Hardware virtualization and vsock                     |
| Unprivileged user namespaces                                                                       | Launching the VM without root                         |
| Waypipe (optional)                                                                                 | Wayland windows from machines                         |

Run `nsl doctor` to find out which prerequisites you're missing. Install them with your host's own tools. nsl doesn't install host packages or change device permissions, groups or sudoers.

## Install with Homebrew

On Linux with [Homebrew ↗](https://brew.sh/), install the CLI from the
[Frostyard tap ↗](https://github.com/frostyard/homebrew-tap):

```sh
brew install --cask frostyard/tap/nsl
nsl doctor
```

The cask becomes available with the first stable release after this integration
lands. It installs only the CLI; the host requirements above still apply.
VM and machine images are downloaded and verified when you create a machine.
Uninstalling the cask leaves your machines and nsl state intact.

To update the CLI:

```sh
brew update
brew upgrade --cask frostyard/tap/nsl
```

## Download a release

Each release has a tarball for `linux_amd64`, a `checksums.txt`, and GitHub build provenance for both.

<div class="steps" markdown>

1. Download the tarball and the checksums from the [latest release ↗](https://github.com/frostyard/nsl/releases/latest):

   ```sh
   version=0.5.1
   base=https://github.com/frostyard/nsl/releases/download/v$version
   curl -LO "$base/nsl_${version}_linux_amd64.tar.gz" -LO "$base/checksums.txt"
   ```

2. Check the tarball against the checksums, and its provenance with the GitHub CLI:

   ```sh
   sha256sum --ignore-missing -c checksums.txt
   gh attestation verify "nsl_${version}_linux_amd64.tar.gz" --repo frostyard/nsl
   ```

3. Unpack the binary somewhere on your `PATH`:

   ```sh
   tar -xzf "nsl_${version}_linux_amd64.tar.gz" nsl
   install -m 0755 nsl ~/.local/bin/nsl
   ```

</div>

## Build from source

To build it yourself, use Go 1.25.8 or newer.

```sh
git clone https://github.com/frostyard/nsl.git
cd nsl
make build   # writes build/nsl
```

## Check the host

```sh
nsl doctor
```

`doctor` prints `OK` or `MISSING` for each tool and for the UEFI firmware systemd-vmspawn would use. It checks device access in your current session, looks up your `kvm` membership in the host account database, then tries KVM and vsock access through that group. It also checks user namespaces and the user systemd manager. A missing VM prerequisite makes it exit with an error.

Having firmware installed isn't enough on its own. systemd-vmspawn needs a QEMU firmware descriptor in `/usr/share/qemu/firmware` or `/etc/qemu/firmware`; without one, `doctor` reports the firmware as missing. Waypipe is optional, but you'll need it to display machine windows on the host.

If you aren't a member of `kvm`, doctor and VM launch report the missing membership with a command such as `sudo usermod -aG kvm 'YOUR_USERNAME'` for an administrator to run. They skip the group switch so it cannot ask for a group password. Once membership is added, retry nsl; no new login is needed. Device permissions must still allow the `kvm` group access.

Next, [create your first machine](first-machine.md).
