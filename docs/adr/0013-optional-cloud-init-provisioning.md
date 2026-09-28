# 0013 — Offer optional cloud-init provisioning at creation

- **Status:** Accepted; deferred until cloud-init is re-validated inside machines ([ADR-0017](0017-shared-vm-and-machine-images.md))
- **Date:** 2026-09-26

## Context

Machine images provide a common starting point. Users also need repeatable installation of packages, repositories, certificates and dotfiles without maintaining a custom image. Cloud-init provides an established format, including local NoCloud seeds. Under [ADR-0016](0016-wsl-style-machines.md), this is machine bootstrap, not project definition.

nsl owns the account, the host UID and GID mapping, and per-machine data. Export and import move a machine without changing its identity. Provisioning must respect those boundaries.

## Decision

Add optional `create --cloud-init FILE` for machine images that advertise a tested provisioning capability. Begin with a documented subset of `#cloud-config` for packages, files, commands, repositories and certificates. Use a local NoCloud seed in the machine's tree; no metadata server or network datasource. Cloud-init belongs to machine images; the host needs no cloud-init package.

- **Input:** creation snapshots the supplied bytes into private state, and the machine's first boot runs provisioning. `--wait-provisioning` waits during creation. Status, wait and logs are separate from machine readiness, so a failed installation leaves the machine usable for diagnosis.
- **Reserved:** nsl-owned account and network behavior stay under image control. Conflicting options are rejected, not merged. User commands run as machine root, which is explicit machine administration.
- **Identity:** each machine has a persistent provisioning ID. It survives stop, start, `recover`, export and import, together with the seed and cloud-init's execution state. Recovery never cleans cloud-init state or invents a new instance ID. Arbitrary script effects are not guaranteed to run exactly once across crashes.

## Consequences

The unprovisioned path stays the default. Users gain reusable setup files and observable failures. Machine images need per-distro cloud-init tests and explicit module ownership. The feature waits until cloud-init is shown to work under nspawn in Debian and Fedora machines.

## Alternatives considered

- **Custom nsl setup YAML:** duplicates an established format and its package-manager support.
- **Make cloud-init responsible for nsl bootstrap:** couples readiness and identity to optional user provisioning.
- **Reuse the machine ID as instance ID:** conflates two identities with different lifetimes.
- **Automatically clean and retry failed provisioning:** can repeat partly applied commands.

## References

- [Provisioning contract](../specs/provisioning.md), [implementation plan](../plans/cloud-init-provisioning.md), [machine images](../specs/machine-images.md).
- [Machine exports](0006-stopped-vm-backups.md), [distribution boundary](0009-distribution-neutral-guest-contract.md), [image delivery](0012-signed-image-distribution.md).
- [NoCloud datasource](https://docs.cloud-init.io/en/latest/reference/datasources/nocloud.html), [module behavior](https://cloudinit.readthedocs.io/en/latest/reference/modules.html), [reported status](https://cloudinit.readthedocs.io/en/24.1/howto/status.html).
