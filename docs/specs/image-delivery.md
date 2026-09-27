# Spec: Signed image delivery

Contract for the v0.3.0 implementation under [ADR-0015](../adr/0015-image-verification-and-catalogue-policy.md). The client is implemented in the development branch; public publication is pending.

## Interface

- `nsl images [--offline]` lists authenticated catalogue selections.
- `nsl pull DISTRO:RELEASE [--offline]` downloads and verifies a base without creating a VM.
- `nsl create NAME --distro DISTRO:RELEASE [--offline] ...` selects a verified base and uses the normal independent-disk creation path. Local `--image FILE --digest sha256:HEX` remains available; the two sources are mutually exclusive.
- An optional `@sha256:HEX` suffix pins the selected OCI manifest. The digest must still be present and unrevoked in the current catalogue.

The registry is `ghcr.io/frostyard/nsl-images`; `catalogue-v1` is the discovery tag. Metadata selects SHA256 digests, never arbitrary download URLs. Images are x86-64 initially. CLI cross-compilation does not expand the image matrix.

## Trust and metadata

The issuer MUST be `https://token.actions.githubusercontent.com`. The signing certificate identity MUST be `https://github.com/frostyard/nsl/.github/workflows/images.yml@refs/heads/main`. Verification requires Sigstore certificate transparency, Rekor inclusion and an observer timestamp against the embedded public-good trusted root. No environment variable or CLI switch disables these checks.

The catalogue OCI artifact contains `catalogue.json` and `catalogue.sigstore.json`. Catalogue schema 1 contains `sequence`, `created`, `expires`, `images` and `revoked`. Each image has `selectors`, `architecture`, `manifest` and its `build_id`. Selectors are unique per architecture. Sequence is the publishing workflow run number; a retry uses a new run for promotion. Validity is at most 30 days. Reject expired metadata, creation more than five minutes in the future, duplicate entries, unsupported schemas, rollback, equal-sequence equivocation and sequences below the CLI's compiled minimum.

The image OCI artifact contains `descriptor.json`, `descriptor.sigstore.json`, `disk.raw.zst`, `packages.json`, `provenance.json` and `acceptance.json`. Descriptor schema 1 embeds the guest `image` descriptor and records `raw` and `compressed` SHA256 digests/sizes plus digests/sizes for the three evidence files. The catalogue authenticates the OCI manifest; the descriptor signature independently authorizes its payload. All layer hashes, sizes and expected names MUST match before use. Unknown, duplicate or missing layers MUST fail.

Signed metadata is bounded to 1 MiB per object; evidence files to 16 MiB each; compressed disks to 8 GiB; raw disks to 32 GiB. Decompression uses a 128 MiB window/memory bound and an exact output-size limit. These are format limits, independent of the environment's eventual virtual capacity.

## Cache and creation

- Download anonymously over HTTPS. Registry authorization tokens are scoped to the fixed repository; redirects MUST NOT send them to other origins.
- Resume private partial blobs by digest using validated HTTP ranges. If a server ignores a range, restart safely. Verify the entire resulting blob, including retained bytes.
- Serialize requests per cache entry, reject unowned files/symlinks, sync completed files and publish atomically. Failed verification MUST NOT publish a usable raw image or named environment.
- Record the greatest authenticated catalogue before fetching image payloads. Offline requests reverify stored signatures, metadata policy and disk digests, and require a complete cache. They MUST NOT download missing data.
- Recheck the latest locally authenticated catalogue before a pull returns; expiry or a concurrent withdrawal MUST reject selection. A later refresh affects later selections.
- Refresh affects future creations only. It MUST NOT mutate an existing VM or customized disk. Local-image creation is an explicit developer/offline path and does not authenticate a publisher.
- Readiness MUST validate the authenticated guest's build descriptor, architecture, transport and protocol range in addition to its environment identity. Schema 1's core contract includes argv, PTY and root growth. Optional capabilities remain absent until independently tested.

## Publication and operations

The main-branch workflow builds every supported profile from pinned recipes, records resolved package versions, runs the shared KVM suite and signs/publishes the tested generic raw image. Test guest disks and private backup archives MUST NOT be uploaded. Promote the catalogue only after all profiles pass. Record whether maintenance tested a reinstall or a newer kernel version.

Initial publication uses an explicitly labeled KVM-capable builder with the documented host prerequisites. Manual dispatch supports integration/package rebuilds; catalogue refresh is required before the 30-day expiry. Keep all promoted digests available. Emergency withdrawal publishes a higher sequence removing the selection and adding its digest to `revoked`. Root/publisher rotation ships in a CLI update; never relax the existing verification policy as a fallback.

## References

- [ADR-0012](../adr/0012-signed-image-distribution.md), [ADR-0015](../adr/0015-image-verification-and-catalogue-policy.md).
- [Publication workflow](../design/image-publication.md).
- [Image delivery plan](../plans/image-distribution.md), [guest contract](guest-images.md), [CLI](cli.md), [lifecycle](../design/lifecycle.md).
