# 0015 — Verify image bundles in the CLI and bound catalogue trust

- **Status:** Accepted
- **Date:** 2026-09-27

## Context

[ADR-0012](0012-signed-image-distribution.md) requires authenticated GHCR images and a signed catalogue. Atomic hosts should not need another installed command to verify downloads. The existing CLI uses the Go standard library, but implementing certificate/transparency verification ourselves would create unnecessary security work.

## Decision

Use the pinned `sigstore-go` library in the CLI, and `klauspost/compress` for bounded zstd decompression. Embed a Sigstore public-good trusted root obtained through its authenticated TUF client. Require certificate transparency, an artifact transparency-log inclusion proof and a verified observer timestamp. Accept only the exact GitHub Actions issuer and the main-branch `frostyard/nsl` image publication workflow identity specified in the [delivery contract](../specs/image-delivery.md).

Publish compressed raw images and signed descriptors as OCI artifacts in one public GHCR repository. The signed descriptor binds the image identity, raw/compressed digests and sizes, package inventory, provenance and acceptance report. A signed catalogue selects the immutable OCI manifest digest. Signing descriptors separately avoids a circular dependency between manifest and signature digests.

Catalogues expire within 30 days and have strictly monotonic sequence numbers. Remember the greatest verified sequence and its content hash; reject rollback and conflicting content at the same sequence. Removed entries and explicit revocations are unavailable for new creation. Offline selection requires an explicit flag, a still-valid signed catalogue and a complete verified cache. Network failures never silently bypass refresh. Existing VMs remain independent of catalogue freshness.

Trust-root or publisher-identity rotation requires a CLI release. Unknown signing authority fails closed. The CLI release carries a minimum acceptable catalogue sequence; correct host time and intact per-user state are trust assumptions. Deleting local state cannot recover its prior rollback history.

## Consequences

Users receive verification in the nsl binary, without installing cosign. This adds reviewed, pinned Go dependencies and a newer minimum Go toolchain. The publisher uses established signing tooling; the CLI implements only the application policy and bounded registry/cache transport.

The project must refresh catalogues before expiry and retain referenced immutable artifacts. Revocation takes effect on successful refresh and, at the latest, catalogue expiry for offline clients. It does not erase or stop existing environments. Root rotation and emergency withdrawal need documented release procedures.

## Alternatives considered

- Require a host cosign binary: increases installation requirements on atomic hosts.
- Implement cryptographic verification with standard-library primitives: duplicates an established protocol and its security checks.
- Accept cached images indefinitely: prevents effective withdrawal and conceals failed catalogue refreshes.
- Follow mutable registry tags directly: loses authenticated selection and rollback protection.

## References

- [Image delivery contract](../specs/image-delivery.md), [implementation plan](../plans/image-distribution.md), [guest contract](../specs/guest-images.md).
- [sigstore-go](https://github.com/sigstore/sigstore-go), [Sigstore bundle verification](https://docs.sigstore.dev/cosign/verifying/verify/).
