# Plan and results: Backups and daily-work reliability

Preserve customized guest systems and exercise the next unproven development workflows. Follows the [vmspawn implementation](vmspawn-implementation.md) and the priority order in the [roadmap](wsl2-equivalent.md).

**Status, 2026-09-26:** export/restore implemented and validated on real guests. Image v4 passed kernel reinstallation, three subsequent boots, and a rootless Podman workflow. A newer kernel was not available; version-upgrade behavior remains unproven.

## Phase 1 — Export and restore

- Implement stopped-VM export and restore under a new name, following [ADR-0006](../adr/0006-stopped-vm-backups.md).
- Verify strict archive validation, no overwrite, preserved files/packages/settings/SSH trust and new runtime identity.
- **Done when:** a customized VM restores and boots with an empty image cache; corruption is rejected without publishing an environment; the original remains usable. Unit tests and `make ci` pass.

## Phase 2 — Guest maintenance and containers

- Use a disposable restored VM to test Debian kernel package hooks, reboot and command/forwarding recovery. Record whether a newer kernel was actually available.
- Install and exercise a rootless container engine: build, run, network and persistent volume access.
- **Done when:** the workload survives a VM restart and package maintenance; record versions, failures and practical limits.

## Results

### Backup round trips

Both trials restored into a separate state directory with an empty `images/` cache. Packages, home files, configuration and SSH trust matched; runtime IDs differed. Original and restored VMs ran concurrently, writes stayed independent, and damaged disk data was rejected before publishing a named environment.

| Source guest | Archive | Export | Restore | First restored start |
| --- | --- | --- | --- | --- |
| v3, with Go 1.24.4 | 2.08 GB | 4.60 s | 2.03 s | 6.99 s |
| v4, after kernel reinstall and Podman installation | 2.86 GB | 9.26 s | 2.91 s | 5.07 s |

These are single observations, not latency distributions. Archives preserve guest credentials and machine identity, omit shared host project contents, and require matching host numeric UID/primary GID. Source and destination can coexist, but restore is not a fresh-identity clone.

Unit tests cover malformed manifests, wrong UID/GID/architecture, unexpected/traversal/link/duplicate/missing entries, checksum damage, truncated data, trailing data, unsafe qcow2 references, failed disk checks, key mismatch, active-VM refusal and existing destinations. A header test covers disks above tar's 8 GiB traditional size limit; exports use GNU numeric fields without PAX metadata. Whole-archive tests exercise export, restore, repeated restore, identity retention and an absent image cache.

### Kernel maintenance: defect found and fixed

A real reinstall of Debian `linux-image-6.12.107+deb13-amd64` failed in the v3 guest. Its FAT `/boot` could not support dpkg's backup hard links; the failed operation removed the boot entry. Repairing the disposable restored guest established the fix, then a fresh image build verified it independently.

Image v4 keeps `/boot` on btrfs, mounts a 1 GiB ESP at `/efi`, includes `systemd-ukify`, and configures the UKI update path. First boot supplies a root UUID command line for later initramfs-tools images. The host CLI requests the vsock SSH listener explicitly: automatic detection missed it on one boot with a regenerated initramfs. [ADR-0007](../adr/0007-maintainable-guest-boot.md).

The fresh v4 VM completed the kernel reinstall, produced a changed UKI, had no `dpkg --audit` findings, and passed three subsequent boots with distinct boot IDs and working commands. The running kernel remained **6.12.107+deb13-amd64**: apt's installed and candidate package were both **6.12.107-1**. This tests package hooks and booting the regenerated image, not upgrading to a newer kernel. Two additional starts took 4.70 and 4.34 seconds; this sample is too small to replace the earlier startup benchmark.

Existing v3 disks are unchanged and need a deliberate boot-layout migration before kernel maintenance. A fresh v4 VM is the tested path; no automatic migration is implemented.

### Rootless containers

Podman **5.4.2** ran as guest user `nsl` with the overlay storage driver. A Containerfile based on Alpine 3.22 built successfully, made correctly owned bind-volume writes, resolved DNS and fetched HTTPS, and served a container HTTP endpoint through guest loopback to host localhost. After the VM restart, the saved container could be started and served the same endpoint; its volume data survived. Container execution also passed after two further VM restarts.

The Alpine manifest digest was `sha256:3e9b4b680bfc9fb5269227cffbd6d42be39fbf7c0b908123913864aa4447e764`. The probe installs `busybox-extras` for its HTTP server. This establishes a rootless Podman workflow, not Docker, every container workload, or automatic startup of user containers after boot.

`make ci` passed with unit/race tests and Linux amd64/arm64 builds. Python and shell syntax checks and `git diff --check` passed. All new maintenance/restore VMs and the builder are stopped after testing. The original `nsl-vm/dev` was running at the start and its running state was preserved. No host packages or permissions were changed.

## Reproduce and inspect

Use disposable guests for the probes. `probe-backup.py` briefly stops the source and restores its prior running/stopped state; it adds/removes unique test markers. Its destination state directory and archive must not exist. `probe-maintenance.py` installs packages, rebuilds the current kernel, creates container workloads, restarts the guest and leaves it stopped. Keep a backup first.

```sh
python3 scripts/probe-backup.py --nsl build/nsl \
  --source-home "$HOME/.local/share/nsl-v5-eval" --source dev \
  --restore-home "$HOME/.local/share/nsl-another-restore-test" \
  --archive build/native/backups/another-test.nsl \
  --output build/native/evidence/another-backup.json

# The current maintenance probe needs the v6 image descriptor.
python3 scripts/probe-distribution.py --nsl build/nsl \
  --image build/image/share/nsl-debian-trixie-x86-64-v6.raw \
  --home "$HOME/.local/share/nsl-another-maintenance-test" \
  --project build/another-maintenance-project \
  --evidence build/another-maintenance-evidence
```

The v4 fixture was retained through the initial cleanup, then replaced by the v5 validation guest during the [storage milestone](storage-management.md). All historical nsl VM/container test guests were deleted; their measurement logs remain. The stopped current image builder is retained for future image work. The maintained v4 backup remains at `build/native/backups/dev-v4.nsl` for reproducing the root-growth regression. These artifacts are private local state, excluded from Git.

Local evidence:

- [Initial backup results](../../build/native/evidence/backup.json), [maintained v4 backup](../../build/native/evidence/backup-v4.json).
- [v4 maintenance results](../../build/native/evidence/maintenance-v4-final.json), [complete log](../../build/native/evidence/maintenance-v4-final.log).
- [Initial kernel failure](../../build/native/evidence/kernel-reinstall-initial.log), [repair](../../build/native/evidence/kernel-repair.log), [transport recovery](../../build/native/evidence/kernel-transport-recovery.log).
- [v4 build](../../build/native/evidence/image-build-v4.log), [layout check](../../build/native/evidence/image-v4-layout.log), [CI](../../build/native/evidence/ci-backup-final.log).

## Next work

1. Safe environment removal and offline growth are now implemented in the [storage milestone](storage-management.md); it also records the v5 filesystem-growth fix.
2. [Broad guest distribution support](distribution-support.md): Debian/Ubuntu profiles now exist; Fedora next, then CentOS Stream and openSUSE. Host reboot/suspend, VPN/DNS changes, disk-full/interrupted-write trials and another atomic host remain reliability gates.
3. Default VM, cwd mapping, editor setup and command exports.
4. Signed prebuilt images, versioned host prerequisites and integration update policy.
5. Desktop launchers, visual/input validation, clipboard, audio, portals and graphics.

## Open questions

- Cross-UID restore and fresh-identity cloning require a guest rebind protocol; excluded from archive version 1.
- No second atomic test host is available in the current workspace. Host reboot/suspend must be scheduled with the user.

## References

- Implements: [lifecycle](../design/lifecycle.md), [CLI](../specs/cli.md), [ADR-0006](../adr/0006-stopped-vm-backups.md).
- User instructions: [export and import](../../site/content/guides/export-import.md).
