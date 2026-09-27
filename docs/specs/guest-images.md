# Spec: Cross-distribution guest images

Target contract under [ADR-0009](../adr/0009-distribution-neutral-guest-contract.md). The seven validated Debian, Ubuntu, Fedora, CentOS Stream, openSUSE and Arch profiles implement credential/command protocol 1 and carry a build descriptor. The development CLI checks schema, architecture, transport and protocol compatibility during readiness. Optional capability negotiation remains planned. The [Debian/Ubuntu report](../plans/image-profiles-and-ubuntu.md), [RPM report](../plans/rpm-guests.md) and [SUSE/Arch report](../plans/suse-and-arch.md) define measured coverage.

## Shared behavior

Every core-supported image MUST:

- Boot a full persistent distro VM, with no required nspawn container inside it.
- Accept the selected host UID/primary GID and public client key, create a normal development account, and bind its identity durably. Conflicts MUST fail clearly without changing unrelated accounts.
- Expose authenticated SSH over vsock and the bounded structured command protocol. Preserve literal argv, binary streams, exit status and PTY behavior.
- Generate guest machine/host-key identities at first boot. Preserve them on ordinary restart and whole-system restore.
- Provide explicit guest administration, usable outbound networking/DNS, discovered localhost TCP services, and explicitly selected project shares with correct ownership.
- Grow the selected root filesystem after virtual disk growth and expose verifiable capacity. The host CLI MUST NOT call distro-specific filesystem or package tools.
- Survive distro-native kernel/package maintenance, followed by authenticated readiness and file/network tests.
- Preserve the distro's security policy. Required SELinux labels/policy or AppArmor changes MUST be narrowly documented and tested.

Image adapters MUST own package names, SSH unit names, account tools, network configuration, root-growth implementation, kernel/initramfs/boot hooks and desktop packages. A Debian workaround MUST NOT become an unconditional host-side rule for every distro.

## Proposed image identity and capability discovery

Before introducing an image catalogue, add a versioned descriptor containing image build ID, distro `ID` and `VERSION_ID`, architecture, integration protocol range, recipe/integration revisions, root-growth capability, command/PTY transport and optional desktop capabilities. Artifact digest/signature and package provenance belong in the published manifest. Read guest identity/capabilities through the authenticated channel and validate required features before claiming readiness.

A first build descriptor is implemented at `/usr/lib/nsl/image.json`: schema, build ID, distro/release/architecture, family/revision, root filesystem, protocol minimum/maximum, transport and source pins/checksum. A sibling artifact JSON and mkosi package manifest record inputs. The host now reads this descriptor over authenticated SSH and rejects incompatible core protocols. Signed artifact metadata follows the [delivery contract](image-delivery.md); all seven images are public; optional capability discovery remains pending. Environment metadata records the raw image digest and guest binding; archives preserve the descriptor as guest disk content without an image catalogue.

## Planned distribution contract

The development CLI implements registry downloads and signature verification under [ADR-0012](../adr/0012-signed-image-distribution.md) and [ADR-0015](../adr/0015-image-verification-and-catalogue-policy.md). All seven x86-64 profiles are public on GHCR; see [publication results](../plans/public-image-delivery.md).

- Published bases MUST use immutable artifact digests. A signed catalogue MAY map friendly distro/release/channel names to those digests, with architecture, protocol compatibility and validation status.
- The initial OCI artifact MUST contain a compressed raw disk, image descriptor, package inventory and build provenance, with compressed/uncompressed sizes and digests. The disk remains a VM payload.
- Clients MUST authenticate the catalogue and artifact against the designated Frostyard workflow identity and issuer, verify content digests and compatibility, and reject untrusted or incomplete data before creating a VM. Catalogue freshness, rollback and trust-rotation policy MUST be settled before automatic selection ships.
- Download staging MUST support safe retries and atomic cache publication. Cache identity MUST use content digests; interrupted or failed verification MUST NOT leave a usable base or named environment.
- Catalogue refresh and new base downloads MUST NOT modify existing guest disks. Ordinary guest updates belong to the distro package manager; nsl integration updates require a separate versioned mechanism.

Publication gates and client acceptance tests are in the [image delivery plan](../plans/image-distribution.md).

## Planned provisioning capability

Images may optionally advertise a tested `provisioning.cloud_init` capability with its interface version, installed cloud-init version, local NoCloud datasource and supported cloud-config keys. Omission means unsupported. The [provisioning interface](provisioning.md) reserves nsl account/SSH/network/storage bootstrap, orders user setup after management prerequisites, and defines persistent instance identity across restore. Capability negotiation and this provisioning feature remain planned. See [ADR-0013](../adr/0013-optional-cloud-init-provisioning.md) and the [Debian/Ubuntu acceptance plan](../plans/cloud-init-provisioning.md).

## Acceptance levels

1. **Recipe available:** upstream or local build definitions exist; no support claim.
2. **Buildable:** artifact built with recorded inputs; no workflow claim.
3. **Core verified:** first boot, identity, commands, files, networking, two VMs, backup/restore, growth/removal and a rootless container workload pass.
4. **Maintenance verified:** package hooks and an actual kernel-version upgrade pass, with crash/recovery and disk-full cases separately recorded.
5. **Desktop verified:** named Wayland/X11/toolkit features pass visual/input/session tests on specified host desktops.

Publish the exact release, image build and host/hypervisor versions. A passing reinstall does not satisfy a newer-kernel upgrade gate. CLI cross-compilation does not establish guest architecture support.

## References

- [Distribution roadmap and matrix](../plans/distribution-support.md), [CLI](cli.md), [image pipeline](../../image/README.md).
- Planned changes: account, hostname, `/mnt/host` and desktop session under [ADR-0016](../adr/0016-wsl-style-machines.md); topology in the [shared-VM experiment](../plans/shared-vm-experiment.md).
