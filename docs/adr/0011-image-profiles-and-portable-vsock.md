# 0011 — Compose guest images from common integration and explicit profiles

- **Status:** Accepted
- **Date:** 2026-09-26

## Context

The Debian-only build mixes credentials, command transport and distribution boot hooks. Ubuntu 24.04 LTS provides an older systemd baseline that cannot rely on Debian's SSH generator. [ADR-0009](0009-distribution-neutral-guest-contract.md) requires distribution differences to stay in image adapters.

## Decision

Compose images from `image/common`, an optional family layer and an explicit distribution/release/architecture profile. Initially allow Debian trixie and Ubuntu noble on x86-64. Reject other combinations before starting the builder. Keep package installation inside the owned Lima builder. Give artifacts distinct names and record build inputs and package manifests.

Use an nsl-owned systemd vsock socket and inetd-style OpenSSH service on both images. Guest setup generates host keys before connections run. Mask the guest SSH generator to avoid competing listeners; existing host kernel arguments are harmless with the generator masked. Leave normal distro SSH units available for deliberate guest administration.

Move Debian-family kernel command-line setup out of the common account helper into an image-owned platform hook. Keep the v5 explicit root-growth dependency. This slice establishes composable images and a second distro; capability negotiation, Fedora's RPM/SELinux adapter and signed image distribution follow separately.

## Consequences

The host lifecycle, storage and backup code remain distro-independent. Root filesystem, boot hooks, package names and security policy remain profile decisions. A profile is not a support claim until its workflows pass. Ubuntu 24.04 is deliberately an older supported LTS compatibility target, not a claim about all Ubuntu releases.

## References

- [Distribution plan](../plans/distribution-support.md), [guest contract](../specs/guest-images.md), [image build](../../image/README.md).
- [Ubuntu support lifecycle](https://ubuntu.com/about/release-cycle).
