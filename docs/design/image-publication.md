# Image publication

Living document. Rationale: [ADR-0015](../adr/0015-image-verification-and-catalogue-policy.md). Contract: [signed image delivery](../specs/image-delivery.md).

## Overview

The manually dispatched `.github/workflows/images.yml` workflow builds all seven
x86-64 profiles, runs the common KVM acceptance suite, then signs and publishes
tested generic images. The signed catalogue is promoted only after every profile
passes. Initial public publication is pending.

## Design

A runner labeled `nsl-image-builder` supplies the existing host VM prerequisites
and user systemd session. Keep the account's normal primary GID when launching
the runner so Lima can map exported file ownership; nsl handles its own KVM group
entry. Clear inherited `GOROOT`/`GOBIN` so Actions selects its requested Go toolchain.
Use a temporary runner restricted to this trusted
manual main-branch workflow; remove it after the job. Pull requests do not invoke
this workflow. The runner installs no host packages or services. Pinned Lima,
ORAS and cosign binaries are downloaded into ignored build output and checked
against SHA256 values. Guest build packages remain inside the owned Lima VM.

`scripts/publish-images.py build` runs CI, builds each profile from pinned upstream
recipes, and exercises disposable guests with `probe-distribution.py`. It checks
all lifecycle cases, maintenance and storage outcomes against the exact generic
raw image and descriptor. Each public directory contains only compressed raw,
package inventory, provenance, acceptance report and signed descriptor. Public
reports use an explicit field list; private logs and backup archives are excluded.
The integration checksum covers selected source inputs, their executable modes
and the composer. Unrelated profiles and documentation do not alter it.

The separate publication step receives a repository-scoped Actions token and uses
GitHub OIDC to sign with the exact workflow identity. It verifies signatures
against the CLI's embedded root, uploads immutable image tags, then signs and
promotes `catalogue-v1`. Tags include the profile revision and workflow run number.
All promoted digests must be retained. Signatures bind both disk forms and the
package/provenance/acceptance evidence.

## Operational notes

- Dispatch a new main-branch run for a rebuild; rerunning an old sequence is
  refused. Profile revisions change when integration inputs change. Image
  publication is independent of CLI release tags.
- The workflow needs `contents: read`, `packages: write` and `id-token: write`.
  Registry credentials are available only to the publication step; temporary
  registry config is deleted afterward.
- Initial GHCR package creation may default to private. Set the package public,
  verify anonymous catalogue/blob access, and exercise CLI pull/create/start/exec
  from a clean cache before claiming public delivery.
- Each catalogue expires after 30 days. Dispatch `operation=refresh` before
  expiry to re-sign the same authenticated selections with a higher sequence and
  renewed expiry. Use `operation=publish` for integration or package refreshes.
  Refresh verifies the prior bundle; it never substitutes untested image bytes.
- For emergency withdrawal, dispatch `operation=withdraw` with `revoke` set to
  the affected full OCI manifest digests (space or comma separated). The job
  verifies the current catalogue, removes those selections, preserves earlier
  revocations and signs a higher sequence. Online clients reject withdrawn
  selections after refresh; offline clients remain bounded by expiry. Running
  guests are unaffected. Do not delete retained artifacts as a substitute for
  publishing a withdrawal.
- Publication, refresh and withdrawal share a workflow concurrency group. A
  sequence cannot be reused; the prior signed sequence must be smaller. Preserve
  this workflow's run-number history. Removing/recreating it requires coordinated
  sequence handling and, when necessary, a CLI minimum-sequence update.
- Preserve private evidence locally for failures, stop diagnostic guests, and
  inspect the image-validation skill. Never upload the evidence directory as a
  general Actions artifact.
- The generic raw image is never booted directly. Tests create independent guest
  disks; published payloads contain no per-environment private key or identity.
- Embedded root updates use `go run scripts/refresh-sigstore-root.go`, review,
  verification against current publications, and a CLI release. See
  [trust inputs](../../trust/README.md).

## References

- [Delivery plan](../plans/image-distribution.md), [release gates](../plans/v0.2-v0.3-release.md).
- [Image builder](../../image/README.md), [distribution acceptance](../plans/distribution-support.md).
- [Image-validation skill](../../.agents/skills/validate-image/SKILL.md).
