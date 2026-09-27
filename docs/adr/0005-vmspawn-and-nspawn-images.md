# 0005 — Use vmspawn and nspawn-derived development images

- **Status:** Accepted for the next development prototype
- **Date:** 2026-09-26

## Context

The [vmspawn comparison](../plans/vmspawn-comparison.md) established a complete workflow on Snow: rootless KVM/vsock, project sharing, commands, localhost forwarding and Waypipe. It passed 20 restart trials, improved measured cold startup, and returned freed anonymous memory to the host. The user authorized continuing this direction and has no compatibility requirement.

## Decision

Replace the Lima runtime in the Go CLI with systemd-vmspawn/QEMU. Preserve one full VM per environment, structured guest argv transport and explicit host integration. Lima may remain an image-build tool. Keep the measured prototype files and local state for comparison; do not migrate or adopt them.

Build a generic Debian disk from the pinned nspawn mkosi recipes. Remove the experiment's fixed user and client key. Supply an environment ID, host UID/GID and a unique public SSH key through a systemd VM credential. An idempotent guest service configures the development account and validates the persisted identity on every boot. Private keys remain on the host.

Require an explicit local image and SHA256 until a signed image catalogue exists. Import it into owned state, create independent writable qcow2 disks, and expand the root partition/filesystem in the guest. Use unique systemd user units and vsock IDs for each environment. A recovery command resumes interrupted preparation or restarts an existing disk without replacing it. Do not advertise crash recovery as backup.

Use the proven rootless user namespace and named-device-descriptor launch path. Keep SSH, terminal and Waypipe protocol implementations external. Own TCP discovery and loopback forwarding; expose conflicts and retry them without evicting host listeners.

This supersedes the runtime and image choices in [ADR-0004](0004-managed-development-vms.md). Broader distribution support, image signing, backups, host suspend/reboot and full desktop integration remain release gates.

## Consequences

- nsl owns more lifecycle, readiness and forwarding behavior; these need failure and multi-VM tests.
- Host systemd-vmspawn, vhost-vsock access and user namespaces become prerequisites. The tested baseline is systemd 261 on x86_64.
- Generic images can configure the host's numeric UID and primary GID without rebuilding.
- Existing experimental Lima state remains outside the new schema and is never overwritten.
- File watchers still need polling or guest-native source files. Memory measurements do not establish file-cache reclamation.

## Alternatives considered

- **Keep Lima as the runtime:** retains mature integration; the measured vmspawn path now warrants owning the missing pieces for this prototype.
- **Rewrite in another language:** no demonstrated need; Go can manage the required tools and state.
- **Promote the Python adapter unchanged:** fixed identity, addresses and storage are insufficient for multiple environments and recovery.

## References

- [Lifecycle design](../design/lifecycle.md), [CLI contract](../specs/cli.md).
- [Comparison and evidence](../plans/vmspawn-comparison.md), [roadmap](../plans/wsl2-equivalent.md).
