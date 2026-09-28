# Plan: Safe removal and disk growth

**Status: implemented and validated on Snow Linux x86_64, Debian 13 image v5, 2026-09-26.**

Deliver explicit removal and resumable offline disk growth following [ADR-0008](../adr/0008-offline-storage-management.md).

## Phase 1 — Removal

- Preview deletion; require `--yes` and stopped owned units to remove.
- Preserve host project files, image cache and external backups.
- Resume interrupted deletion and reject stale lifecycle calls after name reuse.
- **Done when:** unit and real-VM checks prove refusal while running, correct removal, surviving shared files/peer state, and recreation under the old name with a new identity.

## Phase 2 — Growth

- Record pending disk growth before changing the disk; verify and sync before committing metadata.
- Resume after interruption; reject shrinking, running disks and inconsistent metadata.
- **Done when:** a restored maintained v5 fixture grows from 16 to 24 GiB, its root gains space after boot, data/keys persist, and the grown VM can still be backed up and restored without cache. `make ci` passes.

## Later / ideas

The following [image-profile increment](image-profiles-and-ubuntu.md) separates common integration from adapters and adds Ubuntu LTS. Fedora is next. See the [distribution plan](distribution-support.md) and [machine-image contract](../specs/machine-images.md). Defaults, working-directory mapping, editor/command exports and broader reliability remain in the [roadmap](wsl2-equivalent.md).

## References

- [Lifecycle](../design/lifecycle.md), [CLI](../specs/cli.md), [backup results](backup-and-reliability.md).

## Measured failure and image fix

The initial maintained v4 fixture failed the filesystem-capacity check: its qcow2 disk and root partition reached 24 GiB, but its btrfs root remained 16,105,058,304 bytes. The regenerated kernel command line used an explicit root UUID, and automatic root discovery no longer scheduled `systemd-growfs-root.service`. The installed service was inactive.

Image v5 explicitly requires that service before setup/readiness ([ADR-0010](../adr/0010-explicit-guest-root-growth.md)). The CLI remains independent of guest filesystem commands. A fresh v5 VM passed rootless Podman build/volume/DNS/HTTPS/localhost tests, a kernel package reinstall and three subsequent boots. The installed and candidate kernel were both `6.12.107-1`, so this does not establish a newer-kernel upgrade. Two additional startup observations were 4.46 seconds each; these are regression checks, not a replacement benchmark.

Local evidence is excluded from Git:

- [Initial v4 growth failure](../../build/native/evidence/storage.json), [log](../../build/native/evidence/storage.log).
- [v5 image build](../../build/image/evidence/v5-build.log).
- [v5 maintenance](../../build/native/evidence/maintenance-v5.json), [log](../../build/native/evidence/maintenance-v5.log).
- [Root-free CI](../../build/native/evidence/storage-ci.log).

## Final storage results

The maintained v5 backup passed `scripts/probe-storage.py`:

| Check | Result |
| --- | --- |
| Refuse resize/removal while running | Passed; original capacity/state preserved |
| Offline virtual growth, 16 → 24 GiB | Passed in 0.042 seconds (one observation, excluding boot) |
| Root filesystem on next boot | 16,105,058,304 → 24,694,992,896 bytes; exactly 8 GiB gained |
| Persistent file, runtime identity and client key | Preserved |
| Same-size retry / shrinking | Same size harmless; shrink refused |
| Export/restore of grown VM with no image cache | Passed; root capacity and guest data preserved |
| Removal preview / explicit deletion | Preview preserved state; deletion removed state and runtime sockets |
| Host project, symlink target, cache and external archives | Preserved |
| Recreate old name | New runtime identity booted successfully |
| Running peer | Boot ID unchanged throughout storage operations |

Root-free tests additionally inject interrupted growth before/after the disk changes, resume partial deletion, block reserved names, reject stale lifecycle calls and refuse foreign units/invalid disks. `make ci` passed unit tests, vet, formatting, race tests and Linux amd64/arm64 cross-builds. Those interruption tests model failure points; arbitrary host power-loss recovery and arm64 guest boots remain untested.

The probe deletes its disposable guests and restores the peer's initial stopped state. The retained current fixture is `~/.local/share/nsl-v5-eval/dev`, selected by `build/native/env.sh`, with Podman installed. Its maintained backup is `build/native/backups/dev-v5.nsl`; the v4 backup remains only for reproducing the earlier regression. The stopped current image builder remains available. Older VMs and original nsl-owned nspawn containers were removed under the user's cleanup authorization; unrelated machines and host projects were preserved.

Reproduce with unused output/state/project paths and a 16 GiB maintained fixture:

```sh
python3 scripts/probe-storage.py --nsl build/nsl \
  --archive build/native/backups/dev-v5.nsl \
  --home "$HOME/.local/share/nsl-storage-another-test" \
  --project build/native/storage-another-project \
  --peer-home "$HOME/.local/share/nsl-v5-eval" \
  --output build/native/evidence/storage-another-test/results.json
```

Evidence: [storage results](../../build/native/evidence/storage-v5.json), [full log](../../build/native/evidence/storage-v5.log), [historical guest cleanup](../../build/native/evidence/guest-cleanup.json). Next: [distribution adapters, Ubuntu and Fedora](distribution-support.md).
