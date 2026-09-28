# Plan: Image profiles and the first additional distribution

Historical milestone report. Current image revisions, signed artifacts and acceptance are recorded in [public image delivery results](public-image-delivery.md).

**Status: implemented and validated, 2026-09-26.**

Implement the first part of [broad distribution support](distribution-support.md): reusable image layers, a portable vsock transport, and Ubuntu 24.04 LTS alongside Debian 13. Decisions: [ADR-0009](../adr/0009-distribution-neutral-guest-contract.md), [ADR-0011](../adr/0011-image-profiles-and-portable-vsock.md).

## Scope

- Common guest integration, a Debian-family boot adapter and explicit Debian/Ubuntu profiles.
- Distribution/release/architecture validation before builder changes; unique output names, source identity and package manifests.
- nsl-owned SSH socket/unit on both guests, independent of systemd-ssh-generator.
- Native Ubuntu AppArmor policy and ext4 growth, with Debian btrfs regression coverage.
- Profile-selected package/kernel maintenance; one shared acceptance harness for both images.

Ubuntu noble deliberately exercises an older supported LTS and systemd than Debian trixie. Its [standard security maintenance runs through May 2029](https://ubuntu.com/about/release-cycle). This slice does not imply support for other Ubuntu releases or CPU architectures.

## Acceptance

Run `scripts/probe-distribution.py` on disposable environments. It verifies image identity, filesystem, transport, literal argv, binary streams, exit status, a real PTY and shared-file ownership. Reused probes check two independent VMs, loopback conflicts, forced-exit recovery, preserved peer state, rootless Podman, kernel reinstall/reboots, backup/restore without cache, 16→24 GiB growth and removal.

The harness keeps evidence and backups, removes successful test guests, and stops failed guests for inspection. It requires new home, project and evidence paths. Unit tests and `make ci` stay root-free.

```sh
python3 scripts/probe-distribution.py \
  --image build/image/share/nsl-ubuntu-noble-x86-64-v6.raw \
  --home "$HOME/.local/share/nsl-ubuntu-validation" \
  --project build/ubuntu-validation-project \
  --evidence build/ubuntu-validation-evidence
```

## Findings during implementation

Ubuntu's minimal upstream recipe omitted `udev`. Its first boot could not resolve the EFI UUID device and failed readiness. The Ubuntu profile now installs `udev` explicitly. A later kernel reinstall exposed a second missing package: the initial mkosi initramfs booted correctly, but the guest lacked `initramfs-tools` to regenerate it. The package hook produced a UKI with no initrd and the next boot panicked. The profile now installs the regeneration tooling too. This reinforces why an available disk recipe alone is insufficient evidence of support.

The host manager needed no distro-specific changes. Image metadata is recorded in `/usr/lib/nsl/image.json` and an artifact sidecar; authenticated capability negotiation remains a separate increment.

## Next slice

Add a Fedora profile with suitable RPM build tools, dracut/UKI maintenance and SELinux integration. Run the same acceptance suite without disabling security policy. Then proceed to CentOS Stream and openSUSE; retain separate release, architecture and host validation gates. [Distribution roadmap](distribution-support.md), [machine-image contract](../specs/machine-images.md).

## References

- [Image build](../../image/README.md), [lifecycle](../design/lifecycle.md), [CLI contract](../specs/cli.md).
- [Previous storage results](storage-management.md), [main roadmap](wsl2-equivalent.md).

## Debian regression results

The v6 Debian profile passed the complete suite on Snow Linux x86_64: systemd 257.13, kernel `6.12.107+deb13-amd64`, Podman 5.4.2 and btrfs. Its root increased from 16,105,058,304 to 24,694,992,896 bytes. Kernel reinstallation and three subsequent boots passed; no newer candidate kernel was available.

Artifact SHA256: `03630409e6547f0fc55910b2da552dfb2d1305d9af9e4b673e00c074f421f583`.

Local evidence: [results](../../build/native/evidence/debian-v6-final/results.json), [lifecycle](../../build/native/evidence/debian-v6-final/lifecycle.json), [maintenance](../../build/native/evidence/debian-v6-final/maintenance.json), [storage](../../build/native/evidence/debian-v6-final/storage.json). Artifacts and logs are excluded from Git.

Both finalized image builds completed successfully. [Debian build log](../../build/image/evidence/debian-trixie-v6-final-build.log).

## Ubuntu results

The corrected Ubuntu 24.04.5 LTS profile passed the full common suite on Snow Linux x86_64 with systemd 255.4, kernel `6.8.0-142-generic`, Podman 4.9.3 and ext4. AppArmor was enabled with native profiles loaded. Rootless container builds, volume ownership, DNS/HTTPS and host localhost worked with that policy intact.

Kernel reinstallation regenerated the initramfs/UKI, and three subsequent boots passed. The saved container and its volume survived. This was a reinstall of the current version; a newer-kernel upgrade remains untested.

The virtual disk grew from 16 to 24 GiB. The root filesystem increased from 15,773,675,520 to 24,228,343,808 bytes (filesystem metadata accounts for the difference from virtual capacity). The grown backup restored without the image cache; data and keys survived. Explicit removal preserved the host project/cache/archives, name reuse allocated fresh runtime identity, and a running peer remained uninterrupted.

Artifact SHA256: `2c96d22089044060a4efecd7371f2c593ff22123194c859983b055e77e49a3c1`.

Local evidence: [results](../../build/native/evidence/ubuntu-v6-final/results.json), [lifecycle](../../build/native/evidence/ubuntu-v6-final/lifecycle.json), [maintenance](../../build/native/evidence/ubuntu-v6-final/maintenance.json), [storage](../../build/native/evidence/ubuntu-v6-final/storage.json), [final build](../../build/image/evidence/ubuntu-noble-v6-final-build.log). Passing disposable guests were removed; maintained backups remain in each evidence directory. Failed Ubuntu prototype guests were also deleted after diagnosis.

## Remaining limits

Fedora, CentOS and SUSE-family guests, other Ubuntu releases, another atomic host, arm64 boots, newer-kernel upgrades and desktop visual/input checks remain separate gates. The v6 profiles include Waypipe, but this suite does not establish full Ubuntu desktop integration. Image descriptor negotiation and signed downloads are still future implementation work.

Validation also passed `make ci` (profile tests, Go unit/vet/format/race checks, amd64/arm64 cross-builds), Python/shell syntax checks and local documentation link checks. [CI log](../../build/native/evidence/profiles-ci.log).
