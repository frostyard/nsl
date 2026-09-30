# 0020 — Publish a Linux cask to the Frostyard Homebrew tap

- **Status:** Accepted
- **Date:** 2026-09-30

## Context

Frostyard already distributes user-space tools through Linux casks in
[`frostyard/homebrew-tap`](https://github.com/frostyard/homebrew-tap).
nsl's tagged releases use GoReleaser Pro and publish Linux amd64 and arm64
archives with checksums and GitHub build provenance. Maintaining a separate
handwritten cask would duplicate that release metadata.

## Decision

Use GoReleaser's `homebrew_casks` integration to generate `Casks/nsl.rb` from
the release archives and publish it to `frostyard/homebrew-tap`. Publish only
stable tags; snapshots and prerelease tags must not replace the stable cask.
Keep the existing Linux-only build matrix and install only the `nsl` binary.

The release workflow supplies the existing Frostyard `ORG_PAT` secret as the
tap repository token. It needs Contents write access to the tap, and must be
available to this repository. GitHub's repository-scoped `GITHUB_TOKEN`
continues to publish nsl's release and provenance; it cannot write to the tap.
Fail before releasing if the tap token is missing.

Installation is `brew install --cask frostyard/tap/nsl`. The cask does not
install VM images, provision host prerequisites, change device permissions or
group membership, or remove machine state on uninstall. Direct release
downloads remain available.

## Consequences

- The next stable tagged release creates the cask; merging the integration
  alone does not make nsl installable from the tap.
- Later stable releases update the cask's URLs and checksums automatically.
- Homebrew manages the CLI binary, not the host's systemd, KVM, QEMU, firmware
  or virtiofsd. Users still run `nsl doctor` and provision prerequisites with
  their host's tools.
- Tap publication requires a cross-repository credential in addition to the
  existing GoReleaser Pro key. A failed tap update needs attention even if
  GitHub release artifacts have already been uploaded.

## Alternatives considered

- **Handwritten cask or separate bump workflow:** duplicates versions,
  archive names and checksums that GoReleaser already knows.
- **GoReleaser `brews` formula integration:** deprecated in favor of
  `homebrew_casks`; Linux casks also match the existing Frostyard tap.
- **Homebrew dependencies for host tools:** cannot satisfy the host's systemd,
  firmware descriptors, device permissions and fixed virtiofsd path reliably.

## References

- [GoReleaser Homebrew casks](https://goreleaser.com/customization/publish/homebrew_casks/)
- [Release configuration](../../.goreleaser.yaml)
- [Release workflow](../../.github/workflows/release.yml)
- [Install guide](../../site/content/getting-started/install.md)
- [Implementation plan, Phase 10](../plans/shared-vm-implementation.md#phase-10--publication-and-release)
- [ADR-0005 — systemd-vmspawn](0005-vmspawn-and-nspawn-images.md)
