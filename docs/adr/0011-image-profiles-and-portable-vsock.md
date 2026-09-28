# 0011 — Compose images from common integration and explicit profiles

- **Status:** Accepted
- **Date:** 2026-09-26

## Context

nsl builds two kinds of image under [ADR-0017](0017-shared-vm-and-machine-images.md): the nsl VM image and machine images for several distro families. [ADR-0009](0009-distribution-neutral-guest-contract.md) keeps distro differences in image adapters. An earlier Ubuntu baseline showed that the guest SSH generator alone does not reliably provide a vsock listener.

## Decision

- **Composition:** compose the VM image from common integration, the Debian trixie profile and the VM role. Compose each machine image from the machine layer, one family adapter and an explicit distro, release and architecture profile.
- **Builds:** reject any other combination before starting the builder. Install packages only inside the owned Lima builder. Give artifacts distinct names, and record build inputs and package manifests.
- **Transport:** the VM uses an nsl-owned systemd vsock socket and an inetd-style OpenSSH service. Host keys are generated on the data disk's state subvolume before any connection. The guest SSH generator is masked, so no second listener competes. Machines have no SSH listener.

## Consequences

Host lifecycle, storage and export code remain distro-independent. Package names, PAM stacks and similar differences are profile and adapter decisions. A profile is not a support claim until it passes acceptance.

## References

- [VM image](../specs/vm-image.md), [machine images](../specs/machine-images.md), [image build](../../image/README.md).
- History: [image profiles and Ubuntu](../plans/image-profiles-and-ubuntu.md).
