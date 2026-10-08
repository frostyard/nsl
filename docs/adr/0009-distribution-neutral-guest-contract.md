# 0009 — One machine-image contract across distribution families

- **Status:** Accepted
- **Date:** 2026-09-26

## Context

The user requires broad distro support: Debian, Ubuntu, Fedora, CentOS and SUSE families, and Arch. A recipe does not establish nsl compatibility; real per-distro tests do.

Under [ADR-0017](0017-shared-vm-and-machine-images.md), machines are root filesystems in the nsl VM, with no kernel or bootloader of their own. The shared-VM experiment ran hub.nspawn.org images, which needed 13 per-distro workarounds at creation. Examples were PAM stacks, `sudo`, zone data, keyrings, shadow tools and network services.

## Decision

Keep the host CLI and the VM agent distribution-neutral. Define one [machine-image contract](../specs/machine-images.md):

- a common machine layer;
- one adapter per family, built into the image, that owns package names, PAM stacks, keyring setup and similar differences;
- a descriptor with a machine-layer protocol and tested capabilities.

Creation applies only per-machine data: account, hostname, time zone, `sudo` rule and nspawn settings.

- Fail clearly when a required capability is missing. Do not infer support from a distro name, `ID_LIKE`, a successful boot or a package install.
- Support a distro release only after its image passes acceptance. Debian 13, Fedora 44, Arch and openSUSE Tumbleweed came first; Ubuntu 26.04 LTS, CentOS Stream 10 and openSUSE Leap 16.0 followed, then Azure Linux 4.0. A release its distro still calls beta may join on the same terms, and the user documentation marks it as a beta. Track SUSE Linux Enterprise separately, including access and redistribution terms.
- Keep guest distro, host distro and CPU architecture independent. Start with x86-64.
- Every published image pins its recipe and integration revisions and records package inputs, provenance and acceptance results.
- Distro MAC policy does not apply inside machines. Document that plainly; do not claim a distro's SELinux or AppArmor behavior.

## Consequences

The CLI and agent cannot assume apt, a PAM service name or a filesystem layout inside a machine. Support is an evidence-backed matrix, and optional desktop features can lag core support. Frostyard owns an adapter per family, but no longer a kernel or boot adapter per distro.

## Alternatives considered

- **Per-distro bootstrap at creation:** what the hub images needed. It needs network access at creation, is not reproducible, and spreads distro logic into the CLI.
- **One package-name substitution script:** hides PAM, keyring and service differences.
- **Declare every upstream recipe supported:** confuses build inputs with verified behavior.

## References

- [Machine images](../specs/machine-images.md), [ADR-0017](0017-shared-vm-and-machine-images.md), [shared-VM experiment](../plans/shared-vm-experiment.md).
- History: [distribution plan](../plans/distribution-support.md).
