---
description: Host requirements, downloading and verifying a release, building from source, and nsl doctor.
---

# Install nsl

nsl is one binary that runs as your user. It needs an x86-64 Linux host with KVM, because the VM and machine images are x86-64. It was tested on Snow Linux 13 with systemd 261.2, QEMU 10.0.13, virtiofsd 1.13.2 and GNOME Wayland.

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

nsl does not install host packages or change device permissions, groups or sudoers. If a requirement is missing, install it with your host's own tools; `nsl doctor` checks each one.

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

Building needs Go 1.25.8 or newer.

```sh
git clone https://github.com/frostyard/nsl.git
cd nsl
make build   # writes build/nsl
```

## Check the host

```sh
nsl doctor
```

`doctor` prints `OK` or `MISSING` for each tool and for the UEFI firmware systemd-vmspawn would boot the VM with. It reports device access in the current session, checks `kvm` membership in the host account database, then probes KVM and vsock access through that group. It also checks user namespaces and the user systemd manager. systemd-vmspawn finds firmware only through a QEMU firmware descriptor in `/usr/share/qemu/firmware` or `/etc/qemu/firmware`, so firmware installed without one is reported missing. It reports Waypipe as optional. Without Waypipe, you can't display machine windows on the host. It exits with an error when a VM prerequisite is missing.

If you aren't a member of `kvm`, doctor and VM launch report the missing membership with a command such as `sudo usermod -aG kvm 'YOUR_USERNAME'` for an administrator to run. They skip the group switch so it cannot ask for a group password. Once membership is added, retry nsl; no new login is needed. Device permissions must still allow the `kvm` group access.

Next, [create your first machine](first-machine.md).
