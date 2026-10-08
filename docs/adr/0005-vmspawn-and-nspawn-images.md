# 0005 — Run nsl VMs with systemd-vmspawn

- **Status:** Accepted; what runs in the VM is set by [ADR-0017](0017-shared-vm-and-machine-images.md)
- **Date:** 2026-09-26

## Context

The [vmspawn comparison](../plans/vmspawn-comparison.md) established a complete workflow on Snow with systemd-vmspawn and QEMU: rootless KVM and vsock, commands, localhost forwarding and Waypipe. It passed 20 restart trials, started faster than the Lima prototype, and returned freed anonymous memory to the host.

vmspawn's flags change between systemd releases. qcow2 disks (`--image-format`, a format in `--extra-drive`) and `--user` arrived in 260; `--firmware=describe` and a disk type in `--extra-drive` arrived in 261. Fedora 44 and Ubuntu 26.04 LTS ship systemd 259 and Debian 13 ships 257; no stable distribution ships 260 ([#54](https://github.com/frostyard/nsl/issues/54)).

## Decision

Run every nsl VM with systemd-vmspawn and QEMU under a systemd user unit. Lima remains only an image-build tool.

- **Launch** through the rootless named-device-descriptor path: open `/dev/kvm` and `/dev/vhost-vsock` under the `kvm` group, then run vmspawn in an unprivileged user namespace that maps the user to themselves.
- **Host floor:** systemd 259. Launch passes only flags that vmspawn 259 accepts. It omits `--user`: the namespace keeps the user's UID, so every release from 259 on already runs vmspawn in the user's service manager. `doctor` and launch fail on an older vmspawn and name both versions.
- **Disks** are raw files, because vmspawn 259 attaches no other format. The root is a copy of the cached VM image, cloned where the filesystem shares extents, otherwise copied without its holes and zero blocks, then extended to 16 GiB. The data disk is a sparse file that grows by extension. vmspawn names the raw format to QEMU, so nothing a guest writes to a disk can make QEMU open another file.
- **Group membership:** doctor and launch check the host account database before invoking `sg` or `newgrp`, so a non-member gets instructions to join `kvm` instead of a password prompt. Use host `getent` and `id` for NSS lookups: release binaries disable cgo, so Go's `os/user` would read only local files. Query the named account's groups so membership added since login takes effect immediately.
- **Identity:** supply the VM's ID, the host UID and primary GID, and a unique public SSH key through a systemd credential. The VM binds that identity on first boot and rejects a different one later. Private keys stay on the host.
- **Uniqueness:** give each VM its own unit names and vsock CID, and control a unit only after its description names the VM's ID.
- **Transport:** SSH over vsock, with SSH, terminal and Waypipe implementations kept external.
- **Forwarding:** nsl owns TCP discovery and loopback forwarding. It reports and retries conflicts without evicting host listeners.
- **Recovery:** `recover` resumes interrupted preparation and restarts an existing VM without replacing its disks. It is not a backup.

## Consequences

- nsl owns lifecycle, readiness and forwarding behavior, which need failure and multi-VM tests.
- Host systemd-vmspawn from systemd 259 or newer, vhost-vsock access through `kvm`, and user namespaces are prerequisites. Tested hosts run systemd 261 and 262 on x86-64. Each new floor needs an acceptance run on a host that ships it.
- `doctor` cannot ask vmspawn 259 which firmware it would select. It lists vmspawn's firmware descriptors and applies the selection rules of systemd 259 to 262 itself.
- Without shared extents, as on ext4, each fresh root writes the image's non-zero blocks, about 1 GB of the 2.9 GB image: at creation, after `update` and on `recover`, for the shared VM and each isolated one. On btrfs and XFS the copy shares the image's blocks.
- `qemu-img` is no longer a prerequisite. A raw disk has no metadata to verify, so the data disk check is its ownership, type and size.
- Generic images configure the host's numeric UID and GID at boot without rebuilding.
- Host file changes do not produce inotify events in the guest; watchers need polling or guest-native files.

## Alternatives considered

- **Keep Lima as the runtime:** mature integration, but the measured vmspawn path justified owning the missing pieces.
- **libvirt or Incus:** capable VM managers that add a daemon and their own image and network models.
- **Another implementation language:** no demonstrated need; Go manages the required tools and state.
- **Require systemd 261:** keeps qcow2 overlays and `--firmware=describe`, but excludes Fedora 44 and Ubuntu 26.04 LTS. Requiring 260 gains no distribution.
- **qcow2 on newer systemd, raw on 259:** two disk formats and two launch paths to test, for a saving only on hosts without shared extents.
- **Serve qcow2 to vmspawn 259 as a raw file through `qemu-storage-daemon`'s FUSE export:** adds a daemon and FUSE to every disk access.

## References

- [Lifecycle design](../design/lifecycle.md), [CLI contract](../specs/cli.md), [VM image](../specs/vm-image.md).
- [Comparison and evidence](../plans/vmspawn-comparison.md), [roadmap](../plans/wsl2-equivalent.md).
