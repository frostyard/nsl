# VM proof of concept: implementation and measured results

**Historical Lima baseline.** Its local test guests were subsequently deleted at the user’s request. The decision and measurements below describe the earlier implementation. [ADR-0005](../adr/0005-vmspawn-and-nspawn-images.md) and the [Go vmspawn report](vmspawn-implementation.md) supersede its runtime choice. Reproduction requires the preserved local `build/nsl-lima` binary; the current `build/nsl` uses different state and image contracts.

**Date:** 2026-09-26. **Decision:** continue with a Lima/QEMU-backed nsl. The basic development-VM workflow works on the current Snow host. Full WSL2/WSLg-like integration remains a product project with open gates.

This is the experiment deliverable from Phase 1 of the [WSL2-equivalent roadmap](wsl2-equivalent.md), with part of the Phase 2 CLI implemented. Rationale: [ADR-0004](../adr/0004-managed-development-vms.md). Mechanisms: [lifecycle](../design/lifecycle.md). Contract: [CLI](../specs/cli.md).

## Phase 1 — What was built

- Replaced the nspawn CLI wrapper with a Go manager for one full distro VM per environment. No container layer inside the VM and no host sudo.
- Added create/list/start/stop, automatic startup for shell/exec/gui, configurable CPU/RAM and explicit project sharing.
- Added owned metadata, lifecycle locking, image digest validation and refusal to overwrite existing resources.
- Added structured guest argv execution, binary streams, exit-code propagation, guest root selection and SSH terminal support.
- Reused Lima for QEMU/KVM lifecycle, virtiofs, SSH and automatic host-loopback TCP forwarding; Waypipe supplies software-rendered individual Wayland windows.
- Added local tool bootstrap and real-VM measurement scripts. Existing nspawn machines were untouched.
- **Done when:** shell, shared files, localhost HTTP and a guest Wayland surface work together, then survive repeated VM restarts. This gate passed with the file-watcher limitation below.

The image route selected for this prototype is Debian's official generic cloud image. The nspawn mkosi disk-profile build and an nspawn code fork were researched but not implemented. No language rewrite beyond the small Python guest helper was needed.

## Test configuration

| Component | Tested version/configuration |
| --- | --- |
| Host | Snow Linux 13, x86_64, GNOME Wayland, about 58 GiB RAM |
| Hypervisor | QEMU 10.0.13, KVM acceleration, UEFI |
| Manager | Lima 2.2.0, downloaded locally; release SHA256 checked |
| File server | virtiofsd 1.13.2 |
| Guest | Debian 13 generic image `20260914-2601`; SHA512 pinned in the historical Lima implementation |
| Guest kernel | `6.12.107+deb13-amd64` |
| Per VM | 2 vCPUs, 2 GiB configured RAM, 16 GiB virtual disk |
| Identity | Guest UID/GID 1000/1000; persistent `/home/nsl` |
| Desktop | Waypipe 0.9.2, galculator, software rendering |
| Share | Dedicated disposable host project mounted at `/work` |

The host already had QEMU, firmware, virtiofsd and KVM access. Lima and Waypipe were downloaded/extracted into ignored `build/poc`; no host packages, sudoers or device permissions were changed. This is not a clean-install or cross-distribution packaging test.

### vmspawn and vsock comparison

A bounded `systemd-vmspawn` 261.2 probe launched QEMU rootlessly with a separate overlay disk and user-mode networking. It was stopped at the configured 60-second limit; guest command transport/readiness was not established. That timeout is not evidence that vmspawn cannot work.

`/dev/vhost-vsock` is root:kvm `0660` without a user ACL. The current session is not in kvm; a direct read/write open returns EACCES. `/dev/kvm` has an ACL granting the current user access. Codex was running with an unrestricted filesystem/process permission profile. This is a host device-permission gap, likely addressable independently of launcher choice.

Lima was selected because it delivered the whole workflow with substantially less new integration code. An unrestricted-vsock vmspawn workflow was not benchmarked, so this is not a performance comparison between launchers.

## Phase 2 — Measurements

### Repeated command and lifecycle trials

| Check | Result | Median | p95 |
| --- | --- | --- | --- |
| Warm `nsl exec … -- true` | **50/50 passed** | **40.77 ms** | **44.53 ms** |
| Stop, automatic cold start, persistent-file verification | **20/20 passed** | **10.65 s** | **11.18 s** |

Cold measurements include startup through the first successful guest command. Every trial verified a different kernel boot ID and the same persistent home-file contents. Stop time was recorded separately. Downloads and first provisioning are excluded. p95 is the nearest-rank percentile. Two other previously created test VMs remained active during most of this run; this is a real desktop-host measurement, not an isolated benchmark.

The five-second cold-start target in the roadmap was **missed**. Warm latency is comfortably below its 200 ms target. Provisioning with guest package downloads took considerably longer and varied across runs; these results do not represent first-use latency.

**What this says about success probability:** the observed success rates are 100% for these specific repeated operations after the fix. Under a simple binomial model, two-sided 95% Wilson intervals are **92.9–100% for warm execution** and **83.9–100% for cold start/persistence**. The same machine, disk image and implementation were reused, so shared failures and serial dependence limit that model. These intervals are not a probability that the product will succeed, nor a support claim for other hosts. The evidence is sufficient to recommend continuing the architecture; it is insufficient to claim high production reliability or desktop parity.

### Functional checks

| Workflow | Observation |
| --- | --- |
| Default identity and guest root | Correct UID/home; explicit root returned UID 0 |
| Binary streams and error status | NUL/non-UTF8 bytes preserved, separate stderr preserved, guest exit 37 returned; supplemental run compares exact bytes |
| Argument transport | Spaces, quotes, literal shell syntax, newlines and empty strings survived |
| Project read/write/rename | Both directions passed; host ownership remained UID 1000 |
| Interactive terminal | Initial 33×101 size and resize to 41×121 passed |
| Localhost HTTP | Guest loopback server reachable automatically from host; discovery about 0.82 s in the measured final run |
| Wayland application | galculator exchanged surface configure/commit messages through Waypipe; intentionally ended by a 3-second timeout |
| Multiple environments | Two running VMs had separate homes and different kernel boot IDs |
| Persistent OS/application data | Home-file test passed across 20 cold starts; galculator remained available and rendered again afterwards |
| CI | `make ci`: vet, formatting, unit tests, race tests and Linux amd64/arm64 cross-builds passed |
| Local bootstrap | Checksum verification, local extraction/build and `nsl doctor` passed |

GUI evidence is a protocol-level smoke test. It was not a human visual inspection or a test of clipboard, keyboard input, audio, GPU acceleration, Qt/Electron, app launchers or session recovery. The terminal test allowed 300 ms for the asynchronous resize request; the earlier immediate read incorrectly appeared to fail.

### File watchers: a remaining gap, with a working fallback

The baseline share delivered **no inotify events** for host create, modify, rename, delete or nested-file creation. The files themselves were visible and readable.

A separate environment enabled Lima's experimental `mountInotify`. It generated `IN_ATTRIB` (`0x4`) for create/modify/rename/nested-create, but **no event for deletion**. That is not native event fidelity and cannot be assumed to work with every development watcher. It remains disabled in the generated nsl configuration. Lima documents this as an experimental facility in its [mount documentation](https://lima-vm.io/docs/config/mount/#mount-inotify).

A guest polling loop at 100 ms intervals detected the host content change in **101.4 ms** without the bridge, and **101.8 ms** with it. Use a framework's polling mode for shared projects, or place source in the guest home and connect an editor through SSH. Only the simple content-polling fallback was exercised; large-tree polling overhead and framework-specific settings remain to test.

### Memory observations

A `/proc/PID/smaps_rollup` snapshot found about **442 MiB QEMU PSS** for a recently restarted final VM, and **1.50–1.54 GiB QEMU PSS** for two VMs that had installed GUI packages and stayed running. Each was configured with 2 GiB RAM. These numbers exclude Lima, SSH and virtiofsd and were not collected at identical workload states.

This demonstrates that post-workload memory retention needs attention. It does not establish a minimal idle footprint or a one/two/four-VM scaling curve. Ballooning, cache reclamation and aggregate budgets remain open before claiming WSL-like resource behavior.

### Failures found and corrected during development

1. An explicit rule for guest SSH port 22 was rejected by Lima. Removed it; Lima handles the SSH transport separately.
2. Default forwarding rule matching did not exclude discovery listeners on every interface. Added wildcard guest matching, explicit protocol coverage and a final ignore rule.
3. The first reliability run failed **3/3 cold trials** because cloud-init regenerated guest host keys on every restart. nsl correctly rejected the changed identities. Provisioning now preserves each VM's unique first-boot keys; the subsequent **20/20** cold trials passed without resetting trust. [Upstream report of this behavior](https://github.com/lima-vm/lima/issues/678).
4. nsl now uses its own multiplexing socket so its key checks are not bypassed by Lima's already-open permissive SSH connection. Lima management/editor connections retain upstream defaults; broader trust hardening remains work.

The failed exploratory run is kept separately from the post-fix results. Its failures were not silently counted as successful retries.

## Reproduce and inspect

Follow [Build from source](../../site/content/contributing/build.md), then use the scripts:

```sh
python3 scripts/measure-poc.py --environment experiment \
  --project /absolute/path/to/scratch --cold-trials 20 --warm-trials 50
python3 scripts/probe-files.py --environment experiment \
  --project /absolute/path/to/scratch
```

The measurement harness stops its primary VM at the end. The focused file probe leaves it running. The latter can be compared with a separate disposable instance whose Lima config enables `mountInotify`; nsl does not expose that setting as a supported feature.

Local raw evidence (ignored, not release artifacts):

- [Final measurements](../../build/poc/evidence/final-measurement.json), [trial log](../../build/poc/evidence/final-measurement.log).
- [Supplemental exact-byte/GUI checks](../../build/poc/evidence/supplemental.json), [Wayland protocol log](../../build/poc/evidence/gui-protocol.log).
- [Baseline watcher tests](../../build/poc/evidence/files-default.json), [experimental bridge tests](../../build/poc/evidence/files-inotify-bridge.json).
- [Memory snapshot](../../build/poc/evidence/memory-snapshot.json), [vmspawn probe](../../build/poc/evidence/vmspawn.log).
- [Earlier failed cold run](../../build/poc/evidence/measurement.json).

On the experiment host, the reusable environment is `final` under `NSL_HOME=$HOME/.local/share/nsl-eval`. Load `build/poc/env.sh`, set that home, and run `build/nsl shell final` or `build/nsl gui final -- galculator`. All experiment VMs are stopped at handoff; their disks remain for inspection. The `bench` VM retains the old host-key defect, and `demo` is deliberately retained failed-creation metadata. Neither is the recommended demo environment.

## Follow-up

The [nspawn-derived image/vmspawn comparison](vmspawn-comparison.md) evaluates the alternative runtime after KVM group access was granted.

## Later / next decision gates

1. **Daily development:** verify polling with real Vite/webpack/watchfiles projects, expose remote-editor setup, add useful port-conflict diagnostics and working-directory mapping.
2. **Durability:** backup/restore, interrupted provisioning, disk-full recovery, host reboot/suspend and guest kernel/package updates. Keep user disks separate from integration updates.
3. **Resources:** controlled one/two/four-VM measurements after real builds, memory reclamation and startup profiling. Do not add a container layer merely to optimize before measuring.
4. **Desktop:** input, clipboard, audio, app export, portals, GNOME/KDE and accelerated rendering. This is the largest unproven part of the WSL-like experience.
5. **Distribution:** fresh Snow and Fedora Atomic installations, signed image verification, runtime packaging and native arm64 validation.

**Recommendation:** the core architecture is feasible enough to keep building. Prioritize usable development workflows and recovery before a broad desktop promise. No defensible single percentage for full-product success can be derived from this one-host proof of concept.

## References

- Implements: [lifecycle design](../design/lifecycle.md), [CLI contract](../specs/cli.md).
- Decision: [ADR-0004](../adr/0004-managed-development-vms.md).
- Next work and WSL research: [roadmap](wsl2-equivalent.md).
