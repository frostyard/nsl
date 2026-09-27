# Plan: SUSE-family and Arch development VMs

Historical milestone report. Current image revisions, signed artifacts and acceptance are recorded in [public image delivery results](public-image-delivery.md).

Complete the remaining [v0.2.0 distribution gates](v0.2-v0.3-release.md). All three profiles passed the full suite on Snow Linux x86-64.

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

Leap's Podman package did not bring in `container-selinux`. Without it, pasta entered the wrong SELinux domain and could not open the container network namespace. Installing the distribution's container policy and relabeling the already-created container store fixed the diagnostic build. The validated Leap v5 image installs `container-selinux` before any container storage exists. No local policy exceptions or enforcement changes are used.

SUSE kernel RPMs call `update-bootloader`, which needs an explicit `LOADER_TYPE="systemd-boot"` setting. Without that configuration, reinstalling the kernel changes its separate initramfs but leaves the UKI unchanged. The SUSE adapter installs the native bootloader adapter and selects its kernel-install path; fresh-image maintenance validation passed on Leap v5 and Tumbleweed v5.

## Arch result

The revision 2 Arch image passed the full common suite on Snow Linux x86-64. The package refresh upgraded the kernel from `7.2.6-arch2-1` to `7.2.7-arch1-1`; the test verified the new kernel after reboot. Rootless Podman, saved containers/volumes, further reboots, backup/restore, 16→24 GiB growth and safe removal passed.

Image: `nsl-arch-rolling-x86-64-v2`; SHA256 `b6dbda7908f00342ff2e395ed1f37bb09b6470c1c1b000b142a2d8b63f7578bf`. systemd 262, Podman 6.1.2, btrfs. Root capacity grew from 16,105,058,304 to 24,694,992,896 bytes. Passing guests were removed. Revision 2 also rejects option-like kernel inventory values and path components.

Local evidence: [results](../../build/native/evidence/arch-v2/results.json), [lifecycle](../../build/native/evidence/arch-v2/lifecycle.json), [maintenance](../../build/native/evidence/arch-v2/maintenance.json), [storage](../../build/native/evidence/arch-v2/storage.json).

## Leap result

Leap 16.0 v5 passed the complete common suite with SELinux enforcing before and after maintenance. The kernel reinstall regenerated the UKI; three subsequent boots and rootless container persistence passed. Backup/restore, 16→24 GiB root growth and safe removal passed. A newer kernel version was not available in this run.

Image: `nsl-opensuse-16.0-x86-64-v5`; SHA256 `93083cbdeedd675b369b40a878fa3a32f04cdf81205313f5b9052c366ce4928c`. systemd 257 (257.13+suse.131.g575b4807b7), kernel 6.12.0-160000.38-default, Podman 5.4.2, btrfs. Passing guests were removed.

Local evidence: [results](../../build/native/evidence/leap-v5/results.json), [lifecycle](../../build/native/evidence/leap-v5/lifecycle.json), [maintenance](../../build/native/evidence/leap-v5/maintenance.json), [storage](../../build/native/evidence/leap-v5/storage.json).

## Tumbleweed result

Tumbleweed snapshot 20260923 v5 passed the complete suite with SELinux enforcing. Native kernel RPM hooks regenerated the UKI, and three subsequent boots plus saved container/volume tests passed. Backup/restore, 16→24 GiB btrfs growth and safe removal passed. This run tested a kernel reinstall, not a newer version. The initial image is snapshot-pinned; guest repositories follow the native rolling service.

Image: `nsl-opensuse-tumbleweed-x86-64-v5`; SHA256 `2cc8eff33b631d56bde8e0d45c7a3d5dc1f22ea0da1f41e1dbb5e8d8488e2ae0`. systemd 261 (261.2), kernel 7.2.6-1-default, Podman 6.0.2. Passing guests and obsolete diagnostic guests were removed.

Local evidence: [results](../../build/native/evidence/tumbleweed-v5/results.json), [lifecycle](../../build/native/evidence/tumbleweed-v5/lifecycle.json), [maintenance](../../build/native/evidence/tumbleweed-v5/maintenance.json), [storage](../../build/native/evidence/tumbleweed-v5/storage.json).

## References

- [Distribution matrix](distribution-support.md), [image build](../../image/README.md), [RPM results](rpm-guests.md), [guest contract](../specs/guest-images.md).
