# Plan: Fedora and CentOS Stream guests

Historical milestone report. Current image revisions, signed artifacts and acceptance are recorded in [public image delivery results](public-image-delivery.md).

**Status: Fedora 44 and CentOS Stream 10 passed the common acceptance suite on Snow Linux x86-64, 2026-09-27.**

Add Fedora 44 and CentOS Stream 10 through the image adapter boundary in [ADR-0011](../adr/0011-image-profiles-and-portable-vsock.md). This is part of the [v0.2.0 release gate](v0.2-v0.3-release.md).

## Phase 1 — Native RPM image integration

- Share dracut/kernel-install configuration through `image/families/rpm`. Keep distro package selection and maintenance commands in separate profiles.
- Use Fedora 44 build tools inside the owned Debian Lima builder. Debian's SELinux tools cannot read Fedora 44's policy version 35; the build must fail rather than omit labels.
- Preserve targeted SELinux in enforcing mode. Relabel newly created account/credential files at first boot and use Podman's private `:Z` label for its acceptance-test bind volume.
- Keep Fedora's btrfs root and use ext4 for CentOS, whose kernel does not provide btrfs. Both must pass the same root growth checks.
- **Done when:** both generic artifacts build, retain their native security policy and boot through the common transport.

## Phase 2 — Maintenance and common acceptance

- Run `scripts/probe-distribution.py` separately for each artifact, with unused home/project/evidence paths.
- Require the full identity, exec/PTY, file ownership, two-VM lifecycle, forwarding, rootless containers, backup/restore and storage suite.
- Reinstall the kernel through DNF and require a regenerated UKI and repeated successful boots. Report any actual version upgrade separately.
- Record artifact hashes, package inventories, security status and full probe results. Remove passing disposable VMs and retain evidence outside Git.
- **Done when:** both profiles have passing real VM evidence, documentation reports exact versions/limitations, and `make ci` passes.

## Findings so far

The initial Fedora build needed Fedora's repository key, which is absent from the Debian builder. mkosi now retrieves the upstream key and still verifies package signatures. Image and tools-tree configurations both need this bootstrap setting. Fedora repositories do not sign repository metadata; this is separate from RPM package signatures.

Using Debian `setfiles` failed on Fedora's newer SELinux binary policy. The RPM layer now selects a Fedora tools tree rather than attempting to label with the older Debian tool.

Fedora's first boot reached the interactive systemd setup wizard and waited for input on the read-only console. The RPM adapter masks that wizard because nsl configures the development account from its per-VM credential. This does not disable SELinux or alter the locked root-password policy.

CentOS Stream 10/EPEL resolves the runtime packages but has no `galculator` package. Its profile retains Waypipe without bundling that demo application.

mkosi applies distribution systemd presets after post-install scripts. Fedora's disable-by-default preset removed the nsl setup/socket symlinks and re-enabled ordinary SSH. The common layer now carries an explicit early preset for nsl's required units and transport.

The first Fedora command/share checks passed with SELinux enforcing, but its user manager failed because `systemd-pam` was absent from the minimal package set. The Fedora profile now installs it explicitly so PAM supplies the runtime directory and user-session integration.

CentOS Stream 10's systemd package already provides working PAM/user-session integration: its first boot started `user@1000.service` without Fedora's additional split package. Keep that package choice in the Fedora profile.

## References

- [Distribution plan](distribution-support.md), [machine-image contract](../specs/machine-images.md), [image build](../../image/README.md).
- [Fedora SELinux configuration](https://fedoraproject.org/wiki/SELinux/Config), [dracut kernel-install hook](https://github.com/dracut-ng/dracut/blob/main/install.d/50-dracut.install).

## Validated artifacts

### Fedora 44

- Image: `nsl-fedora-44-x86-64-v4`; SHA256 `53760af6ab8bf7cec7e91d43b5997ce2a1ddbf7ad4554de980945389b65c142b`.
- systemd 259 (259.9-1.fc44); kernel `7.2.7-200.fc44.x86_64`; Podman 5.8.7; btrfs.
- SELinux enforcing before and after maintenance. Kernel reinstallation regenerated the UKI and three subsequent boots passed; an actual newer-kernel upgrade remains untested.
- Root filesystem grew from 16,105,058,304 to 24,694,992,896 bytes. The grown backup restored without an image cache, and removal preserved host data and an active peer.
- Local evidence: [results](../../build/native/evidence/fedora-v4/results.json), [lifecycle](../../build/native/evidence/fedora-v4/lifecycle.json), [maintenance](../../build/native/evidence/fedora-v4/maintenance.json), [storage](../../build/native/evidence/fedora-v4/storage.json). Passing disposable guests were removed.

### CentOS Stream 10

- Image: `nsl-centos-10-x86-64-v3`; SHA256 `905afc0ac9ce7c8a32bf15bed60e1f1dbfb3fbe97055b7281b0138a0cc5d25a4`.
- systemd 257 (257-33.el10-g430dadd); kernel `6.12.0-269.el10.x86_64`; Podman 6.1.0; ext4.
- SELinux enforcing before and after maintenance. Kernel reinstallation regenerated the UKI and three subsequent boots passed; an actual newer-kernel upgrade remains untested.
- Root filesystem grew from 15,863,533,568 to 24,335,360,000 bytes. The grown backup restored without an image cache, and removal preserved host data and an active peer.
- Local evidence: [results](../../build/native/evidence/centos-v3/results.json), [lifecycle](../../build/native/evidence/centos-v3/lifecycle.json), [maintenance](../../build/native/evidence/centos-v3/maintenance.json), [storage](../../build/native/evidence/centos-v3/storage.json). Passing disposable guests were removed.

The Fedora v3 diagnostic guest also passed maintenance after manually installing the missing PAM package; the reported Fedora v4 result above comes from a fresh generic image with that package already included. Failed prototype guests were removed after diagnosis.

[SUSE and Arch profiles](suse-and-arch.md) subsequently passed their own complete suites. Additional hosts, architectures, desktop visual checks and actual kernel-version upgrades remain separate gates.
