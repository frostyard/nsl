# 0013 — Offer optional cloud-init provisioning at creation

- **Status:** Accepted
- **Date:** 2026-09-26

## Context

Prebuilt images provide a common starting point. Users and teams also need repeatable installation of packages, repositories, certificates and project configuration without maintaining a custom base for each project. Cloud-init provides an established guest provisioning format, including support for local NoCloud metadata.

nsl already owns the development account, host UID/primary GID mapping, management SSH identity, networking integration and filesystem growth. Backup restore allocates a new runtime ID while preserving guest identity. Provisioning must respect those boundaries.

## Decision

Add optional `create --cloud-init FILE` for images advertising a tested provisioning capability. Begin with a documented subset of YAML `#cloud-config` for packages, files, commands, repositories and certificates. Use a local read-only NoCloud seed; no metadata server or implicit network datasource discovery is required. Cloud-init and filesystem/seed integration belong in image profiles and the launcher, with no host cloud-init package dependency.

Creation snapshots the supplied bytes into private environment state. Ordinary creation remains lazy; first start runs provisioning. An explicit `--wait-provisioning` option boots and waits during creation. Expose provisioning status, wait and logs separately from VM readiness so installation failures leave management access available for diagnosis.

Keep nsl-owned account, SSH, network and root-growth modules under image control. Reject conflicting declarative options rather than silently merging them. User commands execute as guest root and can modify guest state, including management access; accepted cloud-config is explicit guest administration.

Give each fresh environment a persistent provisioning ID independent of runtime identity. Preserve it, the seed and cloud-init execution state on backup/restore. Recovery must not clean cloud-init state or invent a new instance ID. Introduce the required versioned archive support before enabling provisioning; arbitrary script effects are not guaranteed to execute exactly once across crashes.

## Consequences

The normal unprovisioned path remains available. Users gain reusable setup files and observable failures. Images need per-distro cloud-init tests, explicit module ownership and startup ordering. Provisioning adds private input/state to the backup contract. Image capability negotiation is a prerequisite; the feature should follow reliable image delivery in the product roadmap, while local-image tests can begin earlier.

## Alternatives considered

- **Custom nsl setup YAML:** duplicates an established format and its distro package-manager support.
- **Make cloud-init responsible for all nsl bootstrap:** couples management access and identity to optional user provisioning.
- **Reuse runtime ID as instance ID:** restore could incorrectly trigger first-boot configuration again.
- **Automatically clean/retry failed provisioning:** can repeat partially applied, non-idempotent commands.

## References

- [Provisioning interface](../specs/provisioning.md), [implementation plan](../plans/cloud-init-provisioning.md), [guest image contract](../specs/guest-images.md).
- [Stopped VM backups](0006-stopped-vm-backups.md), [distribution boundary](0009-distribution-neutral-guest-contract.md), [image delivery](0012-signed-image-distribution.md).
- [NoCloud datasource](https://docs.cloud-init.io/en/latest/reference/datasources/nocloud.html), [module behavior](https://cloudinit.readthedocs.io/en/latest/reference/modules.html), [reported status](https://cloudinit.readthedocs.io/en/24.1/howto/status.html).
