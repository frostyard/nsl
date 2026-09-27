# 0012 — Distribute signed VM images through GHCR

- **Status:** Accepted
- **Date:** 2026-09-26

## Context

Local image builds establish the guest integration, but ordinary users should be able to choose a distribution without installing Lima or mkosi. nsl derives bootable VM disks from nspawn recipes and must publish and authenticate those derived artifacts itself. The image contract already records distro, architecture, integration protocol and build inputs.

## Decision

Use public GitHub Container Registry (GHCR) repositories for nsl VM images packaged as OCI artifacts. Initially ship one compressed raw disk (`disk.raw.zst`) per distro/release/architecture build, together with image metadata, package inventory and build provenance. OCI supplies artifact storage and transport; the payload boots directly as a VM.

Publish and select artifacts by immutable digest. A signed catalogue maps friendly distro/release/channel names to tested digests and records compatibility and validation status. Sign the artifacts and catalogue using Sigstore with the designated Frostyard GitHub Actions publishing identity. Clients must verify that specific workflow identity and issuer, plus content digests; a valid signature from another publisher is insufficient.

Build from pinned recipes and integration inputs, record resolved package versions, run the required VM acceptance checks, then publish and promote the tested digest. Image releases are independent of CLI releases. The client downloads into staging, verifies the artifact, decompresses into a digest-addressed cache, and prepares an independent writable VM. Interrupted downloads must be resumable; incomplete or unverified content must never become a usable base.

New bases affect new environments. Existing environments retain their disks and use their distro package manager for normal updates. Guest integration updates need a separate versioned mechanism; catalogue refresh must never replace a customized guest root.

## Consequences

Users can obtain prebuilt guests without local image-building tools or a registry login for public images. The project gains responsibilities for image publication, catalogue freshness, compatibility, retention and signing identity management. The first delivery milestone must resolve those policies and test rejection paths before advertising automatic downloads.

One compressed disk keeps initial delivery simple. Delta updates, regional mirrors and private registry authentication are later work. Transport should preserve digest-based identity so enterprise mirrors and offline imports can reuse the same verification contract.

## Alternatives considered

- **Require local image builds:** retains a useful developer path, but adds build dependencies and provisioning work to ordinary installation.
- **Host disks only as release attachments:** a possible mirror, but an OCI registry gives the distro matrix a standard artifact transport and digest model.
- **Download nspawn's container images directly:** those artifacts do not supply the tested nsl VM disk; a transformation still needs its own provenance and signature.
- **Operate a custom image service immediately:** adds infrastructure before download volume or operational requirements justify it.

## References

- [Image delivery plan](../plans/image-distribution.md), [guest image contract](../specs/guest-images.md), [main roadmap](../plans/wsl2-equivalent.md).
- [Distribution boundary](0009-distribution-neutral-guest-contract.md), [image profiles](0011-image-profiles-and-portable-vsock.md).
- [ORAS: artifacts on GHCR](https://oras.land/docs/1.2/how_to_guides/remote_registries/), [GitHub Container Registry](https://docs.github.com/en/packages/working-with-a-github-packages-registry/working-with-the-container-registry), [Sigstore verification](https://docs.sigstore.dev/cosign/verifying/verify/).
