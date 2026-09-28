# 0012 — Distribute signed nsl images through GHCR

- **Status:** Accepted
- **Date:** 2026-09-26

## Context

Ordinary users should get a working nsl VM and choose machine distros without installing Lima or mkosi. nsl derives its images from nspawn recipes, so it must publish and authenticate those derived artifacts itself. Under [ADR-0017](0017-shared-vm-and-machine-images.md) there are two kinds: the nsl VM image, a bootable disk, and machine images, which are root filesystems.

## Decision

Publish nsl images as OCI artifacts in one public GitHub Container Registry (GHCR) repository. Each artifact carries one compressed payload, a descriptor, a package inventory, build provenance and an acceptance report:

- the VM image as `disk.raw.zst`;
- each machine image, one per distro, release and architecture, as `rootfs.tar.zst`.

Publish and select by immutable digest. A signed catalogue maps machine selectors to tested digests and names the current VM image for each agent protocol. Sign artifacts and the catalogue with Sigstore, using the designated Frostyard GitHub Actions workflow identity. Clients verify that identity, the issuer and content digests; a valid signature from another publisher is not enough.

Build from pinned recipes and integration inputs. Record resolved package versions, run the acceptance suites, then publish and promote the tested digests. Image releases are independent of CLI releases. The client downloads into staging, verifies, decompresses within bounds into a digest-addressed cache, and only then uses the image. Interrupted downloads resume, and unverified content never becomes usable.

New machine images affect new machines only. Existing machines keep their trees and update through their distro's package manager. A new VM image replaces each VM's root at its next start and leaves the data disk untouched.

## Consequences

Users obtain images without local build tools or a registry login. The project takes on publication, catalogue freshness, compatibility, retention and signing-identity management, and a rebuild cadence for distro and kernel security updates. Delta updates, mirrors and private registries are later work; digest-based identity keeps them compatible with this verification.

## Alternatives considered

- **Require local image builds:** a useful developer path, but a burden for ordinary installation.
- **Release attachments only:** a possible mirror; an OCI registry gives a standard artifact transport and digest model.
- **Use hub.nspawn.org images directly:** they fail [ADR-0015](0015-image-verification-and-catalogue-policy.md) and ship unsafe defaults; see the [image tally](../plans/shared-vm-experiment.md#image-source-tally-hubnspawnorg-or-our-own).
- **A custom image service:** infrastructure before volume or operations justify it.

## References

- [Image delivery](../specs/image-delivery.md), [VM image](../specs/vm-image.md), [machine images](../specs/machine-images.md), [publication](../design/image-publication.md).
- [Distribution boundary](0009-distribution-neutral-guest-contract.md), [image profiles](0011-image-profiles-and-portable-vsock.md). History: [image delivery plan](../plans/image-distribution.md).
- [ORAS: artifacts on GHCR](https://oras.land/docs/1.2/how_to_guides/remote_registries/), [GitHub Container Registry](https://docs.github.com/en/packages/working-with-a-github-packages-registry/working-with-the-container-registry), [Sigstore verification](https://docs.sigstore.dev/cosign/verifying/verify/).
