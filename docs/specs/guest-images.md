# Spec: Cross-distribution guest images

Target contract under [ADR-0009](../adr/0009-distribution-neutral-guest-contract.md). Debian and Ubuntu v6 profiles implement credential/command protocol 1 and carry a build descriptor. Host capability negotiation is planned. The [acceptance report](../plans/image-profiles-and-ubuntu.md) defines measured coverage.

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

A first build descriptor is implemented at `/usr/lib/nsl/image.json`: schema, build ID, distro/release/architecture, family/revision, root filesystem, protocol minimum/maximum, transport and source pins/checksum. A sibling artifact JSON and mkosi package manifest record inputs. Optional capability discovery, signature metadata and host-side negotiation remain planned. Environment metadata records the raw image digest and guest binding; archives preserve the descriptor as guest disk content without an image catalogue.

## Acceptance levels

1. **Recipe available:** upstream or local build definitions exist; no support claim.
2. **Buildable:** artifact built with recorded inputs; no workflow claim.
3. **Core verified:** first boot, identity, commands, files, networking, two VMs, backup/restore, growth/removal and a rootless container workload pass.
4. **Maintenance verified:** package hooks and an actual kernel-version upgrade pass, with crash/recovery and disk-full cases separately recorded.
5. **Desktop verified:** named Wayland/X11/toolkit features pass visual/input/session tests on specified host desktops.

Publish the exact release, image build and host/hypervisor versions. A passing reinstall does not satisfy a newer-kernel upgrade gate. CLI cross-compilation does not establish guest architecture support.

## References

- [Distribution roadmap and matrix](../plans/distribution-support.md), [CLI](cli.md), [image pipeline](../../image/README.md).
