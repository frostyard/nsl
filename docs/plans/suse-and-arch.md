# Plan: SUSE-family and Arch development VMs

Complete the remaining [v0.2.0 distribution gates](v0.2-v0.3-release.md). Arch's first profile passed the full suite; openSUSE Leap and Tumbleweed remain in validation.

## Phase 1 — Profiles and native integration

- Give Leap 16.0 and Tumbleweed separate explicit profiles. Pin the Tumbleweed repository snapshot to 20260923 and the initial Arch repository snapshot to 2026/09/25.
- Use native package hooks and security policy. Install openSUSE's split systemd-resolved and the boot tools available in each release. Leap 16 includes ukify in systemd-experimental; Tumbleweed has systemd-ukify.
- Use kernel-install/dracut/ukify for Arch package updates, preserving the previous entry when generation fails; see [ADR-0014](../adr/0014-arch-kernel-maintenance.md).
- **Done when:** each generic image builds and boots with the common command/share contract and its native security policy.

## Phase 2 — Shared acceptance

- Run the complete distribution suite separately for each artifact. Require real execution/PTY, file ownership, two-VM lifecycle/forwarding, rootless containers, maintenance/reboots, backups and storage growth/removal.
- When maintenance installs a new kernel version, require the reboot to run that version. A successful reboot into the old kernel is insufficient.
- Record hashes, package inventory, initial and final kernel versions, security status and limitations. Remove passing disposable guests.
- **Done when:** all three profiles have independent passing evidence and `make ci` passes.

## Findings

SUSE's `getenforce` is outside the ordinary user's PATH. The security probe now uses the explicit root execution path to query it. Leap was already globally enforcing; its upstream policy separately marks some domains permissive. nsl retains that policy without local allow rules.

Tumbleweed's kernel selected AppArmor even with the SELinux packages installed. Its profile now selects SELinux using `security=selinux selinux=1`, matching the distribution's installer choice. The platform hook preserves those arguments in subsequent UKIs. [openSUSE's default-policy announcement](https://news.opensuse.org/2025/02/13/tw-plans-to-adopt-selinux-as-default/).

Fresh Leap SSH connections took about 20 seconds. A syscall trace showed PAM's audit path resolving OpenSSH's non-IP peer marker `UNKNOWN` through DNS. Resolving this marker locally as `0.0.0.0` reduced a fresh command to 0.17 seconds. The SUSE image adds this explicit `/etc/hosts` entry; audit records receive the unspecified address for the non-IP peer. Public-key authentication, PAM and native SELinux policy remain enabled. No host timeout change was needed.

A Tumbleweed build using Fedora tools stalled with zypp-rpm blocked writing package progress while zypper polled a different pipe. The interrupted artifact was not published. The SUSE family now uses a native openSUSE tools tree pinned to the same snapshot; both builds completed with that adapter.

Leap's Podman package did not bring in `container-selinux`. Without it, pasta entered the wrong SELinux domain and could not open the container network namespace. Installing the distribution's container policy and relabeling the already-created container store fixed the diagnostic build. The next clean Leap image installs `container-selinux` before any container storage exists. No local policy exceptions or enforcement changes are used.

SUSE kernel RPMs call `update-bootloader`, which needs an explicit `LOADER_TYPE="systemd-boot"` setting. Without that configuration, reinstalling the kernel changes its separate initramfs but leaves the UKI unchanged. The SUSE adapter installs the native bootloader adapter and selects its kernel-install path; fresh-image maintenance validation is pending.

## Arch result

The initial Arch image passed the full common suite on Snow Linux x86-64. The package refresh upgraded the kernel from `7.2.6-arch2-1` to `7.2.7-arch1-1`; the test verified the new kernel after reboot. Rootless Podman, saved containers/volumes, further reboots, backup/restore, 16→24 GiB growth and safe removal passed.

Image: `nsl-arch-rolling-x86-64-v1`; SHA256 `e71c08441e247e9865f1c5c4c3f3d7d4972e290a6bbd2c420b379d91cacc2403`. systemd 262, Podman 6.1.2, btrfs. Root capacity grew from 16,105,058,304 to 24,694,992,896 bytes. Passing guests were removed. Revision 2 tightens inventory validation to reject option-like values and path components; its clean rebuild is in validation.

Local evidence: [results](../../build/native/evidence/arch-v1/results.json), [lifecycle](../../build/native/evidence/arch-v1/lifecycle.json), [maintenance](../../build/native/evidence/arch-v1/maintenance.json), [storage](../../build/native/evidence/arch-v1/storage.json).

## References

- [Distribution matrix](distribution-support.md), [image build](../../image/README.md), [RPM results](rpm-guests.md), [guest contract](../specs/guest-images.md).
