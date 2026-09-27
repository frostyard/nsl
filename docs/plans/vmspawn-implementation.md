# Plan and result: vmspawn as the Go runtime

**Date:** 2026-09-26. This implements [ADR-0005](../adr/0005-vmspawn-and-nspawn-images.md) after the [runtime comparison](vmspawn-comparison.md). The main CLI now manages vmspawn directly. Lima remains an image-builder tool and a preserved local comparison binary.

## Phase 1 — Generic image and independent environments

Implemented:

- A generic Debian image built from pinned nspawn mkosi recipes, with no embedded client key or fixed development account.
- Per-environment boot credentials, SSH keypairs, host-key trust, IDs, user units and vsock addresses. The account uses the host's numeric UID and primary GID.
- Verified raw-image import and independent qcow2 disks. Both tested roots grew to **16,641,929,216 bytes**, approximately 15.5 GiB, within 16 GiB virtual disks.
- Explicit project sharing and desktop opt-in. Existing earlier prototype state is not adopted or overwritten.

The final local image is `build/image/share/nsl-debian-v3.raw`, SHA256 `11469f81e59060c57f57088cd6cf4deec235ba3e3649af064d1cfedc52107bc3`. Its protocol version is 1. The reproducible build procedure is in [image/README.md](../../image/README.md); live Debian repositories mean the artifact itself is not claimed to be bit-for-bit reproducible.

**Done when:** two independently authenticated VMs run simultaneously, own separate home data, and grow their roots. Passed on Snow Linux 13, x86_64, systemd 261.2, QEMU 10.0.13 and virtiofsd 1.13.2. Both guests use Debian 13 and 2 CPUs/2 GiB RAM.

## Phase 2 — Lifecycle, forwarding and recovery

Implemented `recover`, `ports` and `logs`, with authenticated readiness checks even for a running unit. Recovery resumes interrupted preparation or restarts a checked existing disk. Foreign unit descriptions and invalid state are rejected. Runtime sockets are separate from long storage paths.

The [native lifecycle probe](../../scripts/probe-native.py) passed:

- Distinct client keys, guest boot IDs and persistent homes.
- An existing host HTTP listener survives a guest port collision.
- A reported conflict is retried and starts forwarding after the host listener closes.
- Two guest services contend for one host port; the second takes over only after the first releases it.
- A forced VM exit followed by `recover` preserves a **synced** test file and changes that VM's boot ID.
- The peer keeps its boot ID and continues serving HTTP during recovery.
- A stopped forwarding service is recreated by the next command.
- Both environments stop successfully after the probe.

### Defects found and corrected

The first generic image omitted Debian's separately packaged `systemd-repart`, so resizing the virtual disk did not enlarge the root. The final image includes that package and the growth definition.

The first forced-exit trial exposed incomplete first-boot durability: the account entry survived while its new home/key files did not. Guest setup now handles partial home creation and syncs its writes. First-use readiness also syncs guest storage before marking the environment initialized, including generated SSH host keys. The corrected image passed the same forced-exit test. This does not guarantee preservation of arbitrary unflushed application writes, filesystem repair or backup recovery.

The old disposable image builder filled after accumulating several build trees. The tracked build script now owns a separate 64 GiB sparse builder, removes successful guest workspaces and stops the builder on exit. Its finalized full build/export completed successfully.

**Done when:** lifecycle, forwarding conflicts and a controlled forced-exit recovery work without replacing data or disturbing the peer. Passed for these cases. Unit tests separately cover interrupted disk preparation, key preservation, missing image cache with an existing standalone disk, ownership rejection, first-use syncing, request validation and argument fidelity.

## Phase 3 — Development workflow and measurements

Go 1.24.4 was installed **inside the development VM**. The [development probe](../../scripts/probe-development.py) copied the current nsl source into a temporary shared project directory, ran its tests and compiled the CLI inside the guest. It then compiled and ran a Go HTTP service, edited its source from the host, and verified the rebuilt response through host localhost.

- Guest tests and build: passed, **7.02 seconds** in the measured run.
- Host edit → polling → guest compilation/restart → updated localhost response: **0.416 seconds** in one observation.
- Polling interval: 100 ms. Native cross-boundary inotify remains unavailable; this result explicitly uses polling.

The standard command/share/PTY/Wayland and repeated-start harness is recorded in [measurement data](../../build/native/evidence/measurement.json). The builder and peer VM were stopped during these measurements.

| Check | Result |
| --- | --- |
| Warm no-op commands | 50/50; median **63.37 ms**, p95 **69.65 ms** |
| Cold start with a new boot ID and persistent home file | 20/20; median **7.44 s**, p95 **7.94 s** |
| Stop | Median **1.24 s** |
| UID/home, binary streams, exit status, quoted argv, guest root | Passed |
| Shared files and rename | Passed |
| Localhost forwarding | Passed; one discovery observation 1.02 s |
| PTY size and resize | Passed |
| Wayland surface configure/commit | Passed |
| Native host-to-guest inotify | Failed; polling used in the development workflow |

The five-second cold-start target is still unmet. The GUI check observes protocol traffic, not visual/input quality or launch-time parity. Under a simple binomial model, 20/20 cold successes give a 95% Wilson interval of 83.9–100%, and 50/50 warm successes give 92.9–100%. Trials share one host and dependencies; these intervals do not establish production reliability across distributions.

`make ci` passed, including unit tests, race tests and Linux amd64/arm64 builds. Python and shell syntax checks also passed. No host packages, permissions, groups or sudoers were changed.

## Follow-up

This report records the runtime milestone using image v3. Later kernel-maintenance testing found its FAT `/boot` incompatible with Debian kernel replacement; image v4 corrects the layout. Existing v3 disks are not silently upgraded. [Image details](../../image/README.md). The [backup and reliability plan](backup-and-reliability.md) tracks the next implementation and its results; the [roadmap](wsl2-equivalent.md) is the current priority order. Backup commands added afterward are documented in the living CLI contract.

## Later / release gates at this milestone

1. Validate installation and launch on another atomic distribution; only Snow is available in this environment.
2. Test host reboot/suspend, guest kernel updates and container-engine workloads.
3. Add backup/restore, image catalogue/signing and an integration update policy for customized guests.
4. Broaden desktop testing to visual/input correctness, clipboard, audio, GPU and portals.
5. Improve cold-start latency, port discovery overhead and multi-VM resource accounting using measured workloads.

The main Go runtime has replaced the experiment adapter for new environments. These remaining gates are not claims of completed support.

## Reproduce and inspect

The measured v3 environments were `dev` and `peer` under `~/.local/share/nsl-vm`; the user subsequently authorized their deletion. The commands below illustrate the historical setup and require recreating a suitable environment. `build/native/env.sh` now selects the retained v5 maintenance fixture; see the [storage report](storage-management.md).

```sh
source build/native/env.sh
build/nsl shell dev
build/nsl ports dev
build/nsl stop dev
```

Use only disposable environments with the lifecycle probe: it deliberately kills one VM. The development probe requires Go in the guest. User setup is in the [main README](../../README.md).

Local evidence is intentionally ignored by Git:

- [Image build](../../build/native/evidence/image-build-v3.log), [checksum](../../build/image/evidence/image.sha256).
- [Lifecycle results](../../build/native/evidence/lifecycle-v3.json), [log](../../build/native/evidence/lifecycle-v3.log).
- [Development workflow](../../build/native/evidence/development.json), [package installation](../../build/native/evidence/development-packages.log).
- [Repeated measurements](../../build/native/evidence/measurement.json), [log](../../build/native/evidence/measurement.log).
- [CI](../../build/native/evidence/ci-final.log), [initial crash diagnosis](../../build/native/evidence/first-boot-crash.log).

## References

- Decision: [ADR-0005](../adr/0005-vmspawn-and-nspawn-images.md).
- Mechanisms and contract: [lifecycle](../design/lifecycle.md), [CLI](../specs/cli.md).
- Earlier evidence: [vmspawn comparison](vmspawn-comparison.md), [Lima baseline](vm-proof-of-concept.md).
- Delivery plan: [WSL2-equivalent roadmap](wsl2-equivalent.md).
