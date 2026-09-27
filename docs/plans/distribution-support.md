# Plan: Broad Linux distribution support

Make Ubuntu, Debian, Fedora, CentOS, Arch and SUSE-family guests work through the same nsl interface. This is near-term architecture work, alongside storage and before further Debian-only product features. [ADR-0009](../adr/0009-distribution-neutral-guest-contract.md) defines the boundary; the [guest contract](../specs/guest-images.md) defines acceptance.

## Current matrix

The pinned nspawn disk profiles at `68263d05169784f44168ca65241d989865ed011b` name kernels for all five families. nsl now validates Debian 13, Ubuntu 24.04 LTS, Fedora 44, CentOS Stream 10, openSUSE Leap 16.0, Tumbleweed 20260923 and Arch on x86_64 through explicit profiles. The [public publication report](public-image-delivery.md) records the current artifacts. Earlier reports cover [Debian/Ubuntu](image-profiles-and-ubuntu.md), [Fedora/CentOS](rpm-guests.md) and [SUSE/Arch](suse-and-arch.md). Recipe availability is distinct from a working nsl image.

| Guest target | Inputs available | nsl evidence | Next gate |
| --- | --- | --- | --- |
| Debian stable | `debian` profile, `linux-image-amd64` | v7 profile, btrfs; common acceptance suite on Snow | Actual newer-kernel upgrade; broader host coverage |
| Ubuntu LTS | `ubuntu` profile, `linux-generic` | v7 noble profile, ext4 and AppArmor; common acceptance suite on Snow | Additional supported Ubuntu releases; actual newer-kernel upgrade |
| Fedora | `fedora` profile, `kernel-core` | v5 Fedora 44, btrfs; full common/maintenance suite, SELinux enforcing | Actual newer-kernel upgrade; broader host coverage |
| CentOS Stream | `centos` profile, `kernel-core` | v4 Stream 10, ext4; full common/maintenance suite, SELinux enforcing | Actual newer-kernel upgrade; broader host coverage |
| openSUSE Leap | Explicit 16.0 profile, `kernel-default` | v6 full common/maintenance suite, btrfs, SELinux enforcing | Actual newer-kernel upgrade; broader host coverage |
| openSUSE Tumbleweed | Snapshot 20260923, `kernel-default` | v6 full common/maintenance suite, btrfs, SELinux enforcing | New rolling snapshot/kernel upgrade; broader host coverage |
| Arch | Snapshot 2026/09/25, native `linux` package | v3 full suite, including kernel 7.2.6→7.2.7; btrfs | Broader host coverage |
| SUSE Linux Enterprise | Not established by the openSUSE recipe | Planned research | Verify authorized image sources, entitlements/redistribution and compatible build route |
| AlmaLinux / Rocky Linux | CentOS-family recipe matches both | Planned follow-up | Independent artifacts and maintenance tests before inheriting a support claim |

CentOS means **CentOS Stream** for initial work, following the [current project download route](https://www.centos.org/download/). The SUSE family has separate [Leap and Tumbleweed tracks](https://get.opensuse.org/desktop/); enterprise support is a separate target.

## Phase 1 — Separate common integration from Debian

Implemented in [the profiles/Ubuntu slice](image-profiles-and-ubuntu.md): layered composition, explicit input validation, static vsock SSH, image descriptors, package manifests and profile-selected maintenance. Host capability negotiation remains open.

- Split `image/` into common integration plus explicit per-distro profiles. Parameterize build distribution/release/architecture and give outputs unique build identities; reject unsupported combinations.
- Keep nspawn/mkosi pins and record package versions. Use a suitable build tools environment for each target; the current Debian builder's `ToolsTree=no` must not be assumed sufficient for RPM/openSUSE builds.
- Extract package-manager and kernel-maintenance commands from `probe-maintenance.py`; keep lifecycle, execution, file, network, backup and storage probes shared.
- Add the proposed image descriptor/capability negotiation before the catalogue and avoid embedding apt or btrfs assumptions in host commands.
- Provide a tested nsl vsock socket/unit fallback where the distro lacks systemd-ssh-generator. Separate `ssh.service` and `sshd.service` handling. Test account creation, sudo and lingering on each family.
- **Done when:** Debian still passes and adding a distro does not require editing host lifecycle/backup/storage logic.

## Phase 2 — Ubuntu and Fedora vertical tests

[Fedora and CentOS validation](rpm-guests.md) passed.

Ubuntu noble was the first additional profile and exposed ext4/AppArmor differences. Fedora then exercised RPM/dracut/SELinux. Keep testing Ubuntu releases separately.

- For each: build, create two VMs, verify distinct fresh identities, argv/PTY, shares/ownership, localhost conflicts, rootless Podman, recovery, export/restore with no cache, disk growth and removal.
- Exercise package hooks, a real kernel-version change when available, reboot, and transport recovery. Preserve the original security policy and record required integration adjustments.
- Run common filesystem-growth checks against the actual configured root filesystem. Add an ext4 or XFS guest to the matrix before declaring the growth contract portable beyond btrfs.
- **Done when:** exact supported releases/builds and feature results are published; missing features are explicit, not silently disabled.

## Phase 3 — CentOS and SUSE family

Completed for CentOS Stream 10, Leap 16.0 and Tumbleweed 20260923; [SUSE/Arch results](suse-and-arch.md) also cover the Arch adapter.

- Resolve boot packages, older systemd capability gaps and native maintenance for CentOS Stream, openSUSE Leap and a pinned Tumbleweed snapshot.
- Reuse family adapters only where tested; avoid requiring an unsupported replacement of systemd or a disabled security policy.
- Research SLE access and redistribution before promising prebuilt enterprise images. Keep access credentials outside source, manifests and logs.
- **Done when:** each named target passes its own common/maintenance checks. A shared family adapter alone is insufficient.

## Phase 4 — Distribution and host release matrix

Optional cloud-init is a planned per-image capability, with Debian and Ubuntu as its first validation targets. The [provisioning plan](cloud-init-provisioning.md) requires native package/schema tests, preserved security policy, management access during failures, and stable instance identity across restore. A distro booting successfully does not establish provisioning support.

- Pin currently supported stable/LTS releases; pin rolling snapshots by immutable image build. Record upstream EOL and stop advertising unsupported releases.
- Version guest integration independently of guest package state; test upgrades of customized environments without replacing their disks.
- Publish signed bases and provenance through public GHCR OCI artifacts and a signed catalogue, following [ADR-0012](../adr/0012-signed-image-distribution.md) and the [image delivery plan](image-distribution.md). Promote tested digests only. Run the shared matrix in isolated KVM jobs, with lighter root-free tests for every change.
- Test guest distro separately from atomic host distro: Snow and Fedora Atomic first, then openSUSE atomic desktops. arm64 is a separate image/hardware gate.
- Desktop support gets its own capability matrix; core development support should not wait for GPU/audio/portal parity.

## References

- [SUSE and Arch validation](suse-and-arch.md).

- [v0.2.0 and v0.3.0 release gates](v0.2-v0.3-release.md).

- [Pinned nspawn disk profiles](https://github.com/nspawn/mkosi-definitions/tree/68263d05169784f44168ca65241d989865ed011b/mkosi.profiles/disk/mkosi.conf.d).
- [Main roadmap](wsl2-equivalent.md), [storage milestone](storage-management.md), [backup/maintenance evidence](backup-and-reliability.md), [image build](../../image/README.md).
