# Image publication

Living document. Rationale: [ADR-0015](../adr/0015-image-verification-and-catalogue-policy.md), [ADR-0017](../adr/0017-shared-vm-and-machine-images.md). Contract: [signed image delivery](../specs/image-delivery.md).

## Overview

`.github/workflows/images.yml` builds the nsl VM image and every machine image, accepts them together on KVM, then signs and publishes them and a catalogue that selects them. It runs weekly and on manual dispatch, from `main` only. The catalogue is promoted only when every image passes. The first catalogue carrying VM and machine images, sequence 6, was published on 2026-09-28 by [run 36400743847](https://github.com/frostyard/nsl/actions/runs/36400743847). Sequence 7, from [run 36412981733](https://github.com/frostyard/nsl/actions/runs/36412981733), added Ubuntu 26.04, CentOS Stream 10 and openSUSE Leap 16.0. The earlier disk catalogues, up to sequence 4, belong to the retired prototype, and the CLI refuses them.

## Design

A self-hosted runner labeled `nsl-image-builder` supplies the host prerequisites of `nsl doctor`, a user systemd session and a Wayland compositor, headless or not, named by `WAYLAND_DISPLAY`. The GUI acceptance opens windows on it. Its work directory must lie under the runner account's home, because the translation checks need the checkout in a tree shared with machines. The job fetches a pinned Waypipe and keeps its own Lima builder in `~/.local/share/nsl-publication-build`, since a builder's mount belongs to the checkout that created it. Keep the account's normal primary GID when launching the runner so Lima can map exported file ownership; nsl handles its own KVM group entry. Clear inherited `GOROOT` and `GOBIN` so Actions selects its requested Go toolchain. Use a temporary runner restricted to this trusted main-branch workflow and remove it afterwards. Pull requests do not invoke this workflow. The runner installs no host packages or services. Pinned Lima, ORAS and cosign binaries are downloaded into ignored build output and checked against SHA256 values. Guest build packages stay inside the owned Lima VM.

`scripts/publish-images.py build`:

1. runs `make ci`, then builds the VM image and the seven machine images from pinned recipes;
2. runs `probe-vm.py` on the VM image;
3. runs `probe-machines.py` with every machine image, an isolated machine and `--gui`, so every declared capability is accepted: `gui` by the GUI check and `nesting` by the Podman check;
4. runs `measure-machines.py` with the Debian, Fedora, Arch and Tumbleweed images, the four that set the budget: four idle machines at or below 950 MiB without desktop sessions, as the experiment measured, and an additional machine at p95 ≤ 2 s. The acceptance report also records what four desktop sessions add, ungated;
5. writes one public directory per image.

A public directory holds the payload, the package inventory, provenance, the acceptance report and the unsigned descriptor. VM disks are compressed with a pinned zstd (`scripts/zstd-image.go`). Machine images are published as built, and the same tool measures the root filesystem the descriptor records. Acceptance reports use an explicit field list: check names and results, protocols and a few timings. Probe logs, host paths, test state and archives stay in private evidence.

The publication step receives a repository-scoped Actions token and uses GitHub OIDC to sign with the exact workflow identity. It verifies each signature against the CLI's embedded root, pushes immutable image tags named by build ID and workflow run number, then signs and promotes `catalogue-v1` with one `vm` entry and one `machine` entry per profile. All promoted digests are kept.

## Operational notes

- A scheduled or dispatched run publishes; rerunning an old sequence is refused. Profile revisions change when integration inputs change. Image publication is independent of CLI release tags.
- The workflow needs `contents: read`, `packages: write` and `id-token: write`. Registry credentials reach only the publication step, and its registry configuration is deleted afterwards.
- `catalogueMinimum` in `image_contract.go` is 6, the first catalogue with VM and machine images, so no client accepts the disk catalogues. Raise it only with a CLI release, for example after recreating the workflow.
- The `nsl-images` package is public. After changing the workflow or the images, check anonymous catalogue access and `nsl create NAME --distro debian:13` on a host with an empty cache.
- Each catalogue expires after 30 days, and weekly runs replace it. Dispatch `operation=refresh` to re-sign the same selections with a higher sequence without rebuilding; refresh never substitutes untested bytes, and refuses a catalogue from before VM and machine images.
- For an emergency withdrawal, dispatch `operation=withdraw` with `revoke` set to the affected OCI manifest digests. The job verifies the current catalogue, removes those selections, keeps earlier revocations and signs a higher sequence. Online clients reject withdrawn selections after refresh; offline clients stay bounded by expiry. Existing machines are unaffected. Do not delete artifacts instead of withdrawing them.
- Publication, refresh and withdrawal share a concurrency group. A sequence is never reused, and the prior signed sequence must be smaller. Preserve this workflow's run-number history; recreating it needs coordinated sequence handling and a CLI minimum-sequence update.
- Keep private evidence locally after a failure, stop diagnostic VMs, and follow the image-validation skill. Never upload the evidence directory as an Actions artifact.
- Published payloads hold no machine, VM or SSH identity: machine images carry no machine ID or host keys, and the VM image keeps its identity on the data disk, which is never published.
- Embedded root updates use `go run scripts/refresh-sigstore-root.go`, review, verification against current publications, and a CLI release. See [trust inputs](../../trust/README.md).

## References

- [Implementation plan](../plans/shared-vm-implementation.md), Phase 10. History: [delivery plan](../plans/image-distribution.md), [release gates](../plans/v0.2-v0.3-release.md).
- [Image builder](../../image/README.md), [image-validation skill](../../.agents/skills/validate-image/SKILL.md).
