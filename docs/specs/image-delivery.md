# Spec: Signed image delivery

Contract for publishing and verifying nsl images under [ADR-0012](../adr/0012-signed-image-distribution.md) and [ADR-0015](../adr/0015-image-verification-and-catalogue-policy.md). Two kinds of image share one registry, catalogue and trust policy: the [nsl VM image](vm-image.md) and [machine images](machine-images.md) ([ADR-0017](../adr/0017-shared-vm-and-machine-images.md)). The [publication design](../design/image-publication.md) describes the workflow.

## Interface

- `nsl images [--offline]` lists authenticated machine selections and the VM image in effect.
- `nsl pull DISTRO:RELEASE [--offline]` downloads and verifies a machine image without creating a machine.
- `nsl create NAME --distro DISTRO:RELEASE [--offline] ...` selects a verified machine image. It also fetches the current VM image when no VM image is cached. Local `--image FILE --digest sha256:HEX` selects an unauthenticated local machine image; the two sources are mutually exclusive.
- `nsl update [--offline]` selects the catalogue's current VM image for the next VM start. `nsl update --image FILE --digest sha256:HEX` selects a local VM image.
- An optional `@sha256:HEX` suffix on a selection pins the OCI manifest. The digest must still be present and unrevoked in the current catalogue.

The registry is `ghcr.io/frostyard/nsl-images`, and `catalogue-v1` is the discovery tag. Metadata selects SHA256 digests, never download URLs. Images are x86-64; CLI cross-compilation does not expand the image matrix.

### Trust

The issuer MUST be `https://token.actions.githubusercontent.com`. The signing certificate identity MUST be `https://github.com/frostyard/nsl/.github/workflows/images.yml@refs/heads/main`. Verification requires Sigstore certificate transparency, Rekor inclusion and an observer timestamp against the embedded public-good trusted root. No environment variable or CLI switch disables these checks.

### Catalogue

The catalogue OCI artifact contains `catalogue.json` and `catalogue.sigstore.json`:

```json
{
  "schema": 1,
  "sequence": 120,
  "created": "2026-10-05T04:00:00Z",
  "expires": "2026-11-04T04:00:00Z",
  "images": [
    {"kind": "vm", "architecture": "x86-64", "agent_protocol": 1,
     "manifest": "sha256:…", "build_id": "nsl-vm-trixie-x86-64-r1"},
    {"kind": "machine", "selectors": ["debian:trixie", "debian:13"], "architecture": "x86-64",
     "machine_protocol": 1, "manifest": "sha256:…", "build_id": "nsl-machine-debian-13-x86-64-r1"}
  ],
  "revoked": []
}
```

- `kind` is `vm` or `machine`.
- A `vm` entry has `agent_protocol` and no selectors. The CLI uses the single `vm` entry for its architecture and agent protocol.
- A `machine` entry has `selectors` and `machine_protocol`. Selectors are unique per architecture.
- `sequence` is the publishing workflow's run number; a retry uses a new run.

Clients MUST reject a catalogue that is expired or valid for more than 30 days, created more than five minutes in the future, or has an unsupported schema. They MUST also reject duplicate entries, more than one `vm` entry per architecture and agent protocol, rollback and equal-sequence equivocation. A sequence below the CLI's compiled minimum is rejected. The minimum is the sequence of the first catalogue carrying VM and machine images, set when Phase 10 of the [implementation plan](../plans/shared-vm-implementation.md) publishes it; earlier catalogues are not accepted.

### Artifacts

Each image is an OCI artifact whose layers are named files. The catalogue authenticates the OCI manifest, and the descriptor's own signature independently authorizes its payload.

| Kind | Files |
| --- | --- |
| `vm` | `descriptor.json`, `descriptor.sigstore.json`, `disk.raw.zst`, `packages.json`, `provenance.json`, `acceptance.json` |
| `machine` | `descriptor.json`, `descriptor.sigstore.json`, `rootfs.tar.zst`, `packages.json`, `provenance.json`, `acceptance.json` |

The signed `descriptor.json` has `schema` 1 and `kind`, and embeds the image's own descriptor as `image`: the [VM descriptor](vm-image.md#descriptor) or the [machine descriptor](machine-images.md#descriptor). Its `role` MUST match `kind`. It records SHA256 digests and sizes of the payload, uncompressed and compressed, and of the three evidence files:

| Field | `vm` | `machine` |
| --- | --- | --- |
| Uncompressed payload | `raw`: the raw disk | `rootfs`: the root filesystem tar |
| Compressed payload | `compressed`: `disk.raw.zst` | `compressed`: `rootfs.tar.zst` |
| Evidence | `packages`, `provenance`, `acceptance` | `packages`, `provenance`, `acceptance` |

All layer hashes, sizes and names MUST match before use. Unknown, duplicate or missing layers MUST fail.

### Bounds

| Object | Limit |
| --- | --- |
| Signed metadata | 1 MiB each |
| Evidence files | 16 MiB each |
| VM disk | 8 GiB compressed, 32 GiB raw |
| Machine root filesystem | 4 GiB compressed, 16 GiB uncompressed |

Decompression uses a 128 MiB window and memory bound and an exact output-size limit. These are format limits, independent of a machine's eventual size.

## Rules

### Cache and selection

- Download anonymously over HTTPS. Registry authorization tokens are scoped to the fixed repository; redirects MUST NOT send them to other origins.
- Resume private partial blobs by digest using validated HTTP ranges. If a server ignores a range, restart safely. Verify the entire resulting blob, including retained bytes.
- Serialize requests per cache entry, reject unowned files and symlinks, sync completed files and publish atomically. Failed verification MUST NOT publish a usable image or named machine.
- VM images are cached as verified raw disks. Machine images are cached as verified `rootfs.tar.zst`, after streaming decompression proves the uncompressed digest and size. The machine-image cache is the directory the VM reads through its read-only image share.
- Record the greatest authenticated catalogue before fetching image payloads. Offline requests reverify stored signatures, metadata policy and payload digests, and require a complete cache. They MUST NOT download.
- Recheck the latest locally authenticated catalogue before a pull or update returns. Expiry or a concurrent withdrawal MUST reject the selection. A later refresh affects later selections.
- The CLI MUST reject a VM image whose `agent_protocol` differs from its own, and a machine image whose `machine_protocol` differs from the VM image's.
- A refresh affects future machines and the next VM start only. It MUST NOT change an existing machine. Machines update through their distro's package manager.
- Local images verify the selected bytes but not a publisher. They are an explicit developer path.

### Publication

- The main-branch workflow builds the VM image and every machine image from pinned recipes. It records resolved package versions and runs `scripts/probe-vm.py` and `scripts/probe-machines.py` on KVM. Then it signs and publishes the tested images.
- The catalogue is promoted only after every image passes. Test machines, data disks, archives and private evidence MUST NOT be uploaded.
- Images are rebuilt at least weekly for distro security updates, within the 30-day catalogue window. Manual dispatch supports urgent rebuilds.
- Keep all promoted digests available. Emergency withdrawal publishes a higher sequence that removes the selection and adds its digest to `revoked`.
- Trust-root or publisher rotation ships in a CLI release. Never relax verification as a fallback.

## References

- Rationale: [ADR-0012](../adr/0012-signed-image-distribution.md), [ADR-0015](../adr/0015-image-verification-and-catalogue-policy.md), [ADR-0017](../adr/0017-shared-vm-and-machine-images.md).
- Contracts: [VM image](vm-image.md), [machine images](machine-images.md), [CLI](cli.md). Workflow: [image publication](../design/image-publication.md).
- History: [image delivery plan](../plans/image-distribution.md), [public delivery results](../plans/public-image-delivery.md).
