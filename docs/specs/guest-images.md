# Spec: Cross-distribution guest images

Target contract under [ADR-0009](../adr/0009-distribution-neutral-guest-contract.md). The present Debian image implements credential/command protocol 1. Capability discovery and a distribution matrix are planned; this document does not imply other images work today.

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

The descriptor is not yet implemented. Protocol-1 environment metadata currently records the base-image digest and guest binding; archives retain that digest and the whole guest disk. A future descriptor must survive export/restore without consulting an image catalogue.

## Acceptance levels

1. **Recipe available:** upstream or local build definitions exist; no support claim.
2. **Buildable:** artifact built with recorded inputs; no workflow claim.
3. **Core verified:** first boot, identity, commands, files, networking, two VMs, backup/restore, growth/removal and a rootless container workload pass.
4. **Maintenance verified:** package hooks and an actual kernel-version upgrade pass, with crash/recovery and disk-full cases separately recorded.
5. **Desktop verified:** named Wayland/X11/toolkit features pass visual/input/session tests on specified host desktops.

Publish the exact release, image build and host/hypervisor versions. A passing reinstall does not satisfy a newer-kernel upgrade gate. CLI cross-compilation does not establish guest architecture support.

## References

- [Distribution roadmap and matrix](../plans/distribution-support.md), [CLI](cli.md), [image pipeline](../../image/README.md).
