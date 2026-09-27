# Plan: Signed prebuilt image delivery

**Status: client and publication implemented; all seven images are public on GHCR.** See [publication results](public-image-delivery.md) for exact artifacts and release acceptance.

[ADR-0015](../adr/0015-image-verification-and-catalogue-policy.md) and the [delivery contract](../specs/image-delivery.md) settle the verifier, formats, expiry, rollback, revocation and offline policy.

Let users select a supported distribution and create a VM without building its image locally. [ADR-0012](../adr/0012-signed-image-distribution.md) selects public GHCR OCI artifacts, a signed catalogue and verification tied to the Frostyard publishing workflow. This builds on [image profiles](image-profiles-and-ubuntu.md) and the [distribution support matrix](distribution-support.md).

Implemented CLI syntax:

```sh
nsl images
nsl create dev --distro ubuntu:24.04
nsl create work --distro fedora:44
```

The catalogue must advertise only published, tested combinations. nsl selects the architecture, resolves a compatible digest, downloads and verifies the base, and creates an independent writable environment. Public downloads require neither a registry login nor Lima/mkosi; host VM prerequisites still apply.

## Phase 1 — Artifact and trust contract

- Define an OCI artifact containing `disk.raw.zst`, a versioned image descriptor, package inventory and build provenance. Record compressed and uncompressed sizes/digests, distro/release/architecture, integration protocol range, source revisions and validation coverage.
- Define the signed catalogue mapping distro/release/channel names to immutable artifact digests. Friendly labels may advance; exact digest references remain available for repeatable creation.
- Bind signatures to an explicit trusted Frostyard publishing workflow identity and OIDC issuer. Authenticate the catalogue and its selected artifact; checksum validation alone does not establish a publisher.
- Resolve catalogue expiry, rollback protection, revocation, signing-identity rotation and offline verification policy before enabling automatic selection. Use established verification tooling or libraries rather than implementing a new signing scheme.
- **Done when:** the format and trust policy are documented, compatibility checks are testable, and fixtures cover altered metadata, mismatched digests, wrong signer/issuer, unsupported protocols and stale catalogue state.

## Phase 2 — Build, test and publish

- Build each supported distro/release/architecture from pinned recipes and integration inputs. Record resolved package versions; live repositories do not imply bit-for-bit reproducibility.
- Run the image's required boot, command/PTY, identity, files, networking, recovery, storage and maintenance checks in isolated KVM jobs. Publish exact coverage, including whether kernel maintenance tested a reinstall or a newer version.
- Publish the artifact by digest, sign it using the designated GitHub Actions identity, and attach provenance/package inventory. Promote the same tested digest into the signed catalogue after all required gates pass.
- Keep image release cadence independent of CLI tags. Define rebuild triggers for integration changes and refreshed distro packages, supported release/EOL policy, and retention for published digests.
- **Done when:** a clean CI build produces a publicly downloadable signed artifact and catalogue entry; failed gates prevent promotion, and the published digest matches the tested disk.

## Phase 3 — Download and create

- Publish optional provisioning capability metadata only after the image passes the [cloud-init acceptance plan](cloud-init-provisioning.md). Base-image delivery remains usable without provisioning; the proposed `--cloud-init` interface builds on verified selection and capability negotiation.
- Add catalogue listing and distro selection after guest protocol compatibility checks are available. Keep explicit local-image creation for development and offline use.
- Implement resumable downloads into private staging, bounded size/decompression checks, signature and digest verification, and atomic publication into a digest-addressed cache. Concurrent requests must not publish partial or conflicting entries.
- Verify the decompressed disk against its recorded digest before handing it to the existing image-import/create path. Each environment receives its own writable disk and credentials.
- Show useful download/verification progress and actionable errors. Test network interruption, retry, disk exhaustion, corrupt compressed content, architecture mismatch and offline use of a previously verified cache.
- **Done when:** a clean supported host creates a working VM from a catalogue selection without local image-building tools; failed verification leaves no named VM or usable unverified cache entry.

## Phase 4 — Updates and operations

- Catalogue refresh and new image downloads affect future creations only. Existing guest disks, package state, home and keys remain intact; distro package managers own ordinary kernel/package updates.
- Design guest integration updates separately, with protocol compatibility, a stopped backup and recovery tests. Updating the CLI or refreshing a base must never replace a customized root filesystem.
- Keep image provenance available for diagnosis and backups. Measure download size, latency, storage and bandwidth before changing the delivery format or adding infrastructure.
- **Done when:** updating the catalogue/base leaves a customized running and stopped VM unchanged, and users can identify the exact image build used for an environment.

## Later / ideas

- Delta downloads after measuring the benefit against complexity and recovery cost.
- Regional/object-storage mirrors, enterprise registries and private authentication.
- Export/import bundles carrying enough metadata and signatures for the agreed offline trust policy.
- Additional architectures after real boot and workflow validation.

## Current implementation and remaining work

The CLI implements catalogue listing, verified/resumable pulls, explicit offline
selection and `create --distro`. Unit tests cover real Sigstore verification,
identity/payload/log tampering, catalogue expiry and rollback, concurrent pulls,
interruption/range checks, corrupt compression/raw images, unsafe cache paths and
readiness incompatibility. These tests use local registry fixtures; they do not
establish public delivery or replace the real VM acceptance gate.

All seven profiles passed the full VM suite and were signed/published by the main-branch workflow. The [publication report](public-image-delivery.md) records anonymous pull/boot acceptance and download measurements. The [operations runbook](../design/image-publication.md) covers rebuild, refresh and withdrawal. Optional provisioning, guest integration updates and additional host/architecture validation remain future work.

## References

- [v0.2.0 and v0.3.0 release gates](v0.2-v0.3-release.md).

- Implements: [ADR-0012](../adr/0012-signed-image-distribution.md), [guest image contract](../specs/guest-images.md).
- Related: [main roadmap](wsl2-equivalent.md), [distribution plan](distribution-support.md), [image build](../../image/README.md).
- Primary references: [GHCR](https://docs.github.com/en/packages/working-with-a-github-packages-registry/working-with-the-container-registry), [ORAS publication](https://oras.land/docs/1.2/how_to_guides/remote_registries/), [Sigstore verification](https://docs.sigstore.dev/cosign/verifying/verify/).
