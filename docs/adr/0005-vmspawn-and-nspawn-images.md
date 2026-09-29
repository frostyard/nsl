# 0005 — Run nsl VMs with systemd-vmspawn

- **Status:** Accepted; what runs in the VM is set by [ADR-0017](0017-shared-vm-and-machine-images.md)
- **Date:** 2026-09-26

## Context

The [vmspawn comparison](../plans/vmspawn-comparison.md) established a complete workflow on Snow with systemd-vmspawn and QEMU: rootless KVM and vsock, commands, localhost forwarding and Waypipe. It passed 20 restart trials, started faster than the Lima prototype, and returned freed anonymous memory to the host.

## Decision

Run every nsl VM with systemd-vmspawn and QEMU under a systemd user unit. Lima remains only an image-build tool.

- **Launch** through the rootless named-device-descriptor path: open `/dev/kvm` and `/dev/vhost-vsock` under the `kvm` group, then run vmspawn in an unprivileged user namespace that maps the user to themselves.
- **Group membership:** doctor and launch check the host account database before invoking `sg` or `newgrp`, so a non-member gets instructions to join `kvm` instead of a password prompt. Use host `getent` and `id` for NSS lookups: release binaries disable cgo, so Go's `os/user` would read only local files. Query the named account's groups so membership added since login takes effect immediately.
- **Identity:** supply the VM's ID, the host UID and primary GID, and a unique public SSH key through a systemd credential. The VM binds that identity on first boot and rejects a different one later. Private keys stay on the host.
- **Uniqueness:** give each VM its own unit names and vsock CID, and control a unit only after its description names the VM's ID.
- **Transport:** SSH over vsock, with SSH, terminal and Waypipe implementations kept external.
- **Forwarding:** nsl owns TCP discovery and loopback forwarding. It reports and retries conflicts without evicting host listeners.
- **Recovery:** `recover` resumes interrupted preparation and restarts an existing VM without replacing its disks. It is not a backup.

## Consequences

- nsl owns lifecycle, readiness and forwarding behavior, which need failure and multi-VM tests.
- Host systemd-vmspawn, vhost-vsock access through `kvm`, and user namespaces are prerequisites. The tested baseline is systemd 261 on x86-64.
- Generic images configure the host's numeric UID and GID at boot without rebuilding.
- Host file changes do not produce inotify events in the guest; watchers need polling or guest-native files.

## Alternatives considered

- **Keep Lima as the runtime:** mature integration, but the measured vmspawn path justified owning the missing pieces.
- **libvirt or Incus:** capable VM managers that add a daemon and their own image and network models.
- **Another implementation language:** no demonstrated need; Go manages the required tools and state.

## References

- [Lifecycle design](../design/lifecycle.md), [CLI contract](../specs/cli.md), [VM image](../specs/vm-image.md).
- [Comparison and evidence](../plans/vmspawn-comparison.md), [roadmap](../plans/wsl2-equivalent.md).
