# 0009 — One guest contract across distribution families

- **Status:** Accepted; the guest contract becomes a machine-image contract under [ADR-0017](0017-shared-vm-and-machine-images.md)
- **Date:** 2026-09-26

## Context

The user requires broad guest support: Ubuntu, Debian, Fedora, CentOS and SUSE families. The current measured image is Debian 13. Pinned nspawn recipes already include Debian, Ubuntu, Fedora, CentOS-family and openSUSE kernel profiles, but a recipe does not establish nsl compatibility. Debian kernel maintenance exposed why boot and update behavior need real per-distribution tests.

## Decision

Keep the host manager distribution-neutral. Define a common authenticated guest contract for account setup, argv execution, readiness, files, networking, persistent identity and storage growth. Keep package names, service names, bootloader/initramfs hooks, security policy and package maintenance in distribution-specific image adapters. Continue using nspawn recipes where applicable, without requiring the nspawn runtime or nested containers.

Make a Fedora guest and an Ubuntu LTS guest the next image targets after the current storage milestone, before further Debian-specific conveniences or image-catalogue design. Then validate CentOS Stream and openSUSE Leap/Tumbleweed. Track SUSE Linux Enterprise separately, including image access, entitlements and redistribution conditions. AlmaLinux/Rocky can reuse a family adapter only after their own checks pass.

Use explicit capability/version metadata and fail clearly when a required guest capability is missing. Do not infer support solely from distro name, `ID_LIKE`, successful boot or package installation. Older supported systemd versions may need nsl-owned vsock socket/service units instead of the guest SSH generator; prove that path without replacing the guest's systemd wholesale. Do not disable SELinux/AppArmor to satisfy a test.

Keep guest distro, atomic host distro and CPU architecture as independent dimensions. Start with x86_64 guest images; arm64 requires its own boot artifacts and hardware validation. Every published image must pin its recipe/integration revision and record package inputs, checksums, provenance and validation status.

## Consequences

The protocol and storage commands cannot assume apt, Debian boot hooks or btrfs. Image adapters may use different package managers, initramfs implementations and filesystems while providing equivalent user-facing behavior. Support becomes an evidence-backed matrix; optional desktop features can lag core development support. The existing Debian image and tests must be separated into common and distro-specific parts before adding more images.

## Alternatives considered

- **Finish Debian first and add distributions later:** entrenches untested assumptions in interfaces and updates.
- **One large package-name substitution script:** hides boot, security-policy and filesystem differences.
- **Declare every upstream recipe supported:** confuses build inputs with verified behavior.

## References

- [Distribution plan](../plans/distribution-support.md), [guest contract](../specs/guest-images.md), [lifecycle](../design/lifecycle.md).
- [ADR-0005](0005-vmspawn-and-nspawn-images.md), [kernel-maintenance lesson](0007-maintainable-guest-boot.md).
