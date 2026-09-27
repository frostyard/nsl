# Experiment: nspawn-derived Debian image with vmspawn

**Historical comparison.** Its local test guests were subsequently deleted at the user’s request. The recommendation below was implemented in the [main Go CLI](vmspawn-implementation.md). Lima is now used for building images only; its runtime remains a local comparison baseline.

**Date:** 2026-09-26. **Result:** the alternative works. A bootable image built from nspawn's recipes runs directly under rootless systemd-vmspawn with vsock commands, shared projects, localhost HTTP and Waypipe. It passed the same repeated lifecycle checks as the Lima prototype.

**Recommendation:** advance vmspawn to the next development prototype using this nspawn-derived image pipeline. The faster startup and observed memory reclamation make the additional integration work worth evaluating. Keep the current Lima CLI available until vmspawn has owned multi-environment lifecycle, recovery and another-host validation. The existing [ADR-0004](../adr/0004-managed-development-vms.md) still governs the main CLI; this experiment does not change its runtime.

**Follow-up:** [The Go implementation](vmspawn-implementation.md) now follows [ADR-0005](../adr/0005-vmspawn-and-nspawn-images.md). The comparison below records the earlier runtime choices and measurements.

## Phase 1 — Build the nspawn image

The experiment uses the upstream Debian definition and `disk` profile from `nspawn/mkosi-definitions` at `68263d05169784f44168ca65241d989865ed011b`. It uses mkosi v27 at `4736cd836108a97772142c461c49f1ddb4172348`.

This is **an image built from nspawn's recipes**, not a pull of the hub's OCI artifact. No nspawn runtime or container layer is inside the VM. The disk profile supplies a Debian kernel, initrd, systemd-boot and GPT/btrfs layout. [Pinned upstream disk profile](https://github.com/nspawn/mkosi-definitions/blob/68263d05169784f44168ca65241d989865ed011b/mkosi.profiles/disk/mkosi.conf).

A disposable Lima builder installed its build dependencies inside the guest. The upstream Fedora tools tree was disabled in favor of the builder's Debian tools. No build packages were installed on the atomic host.

The tracked [experiment overlay](../../experiments/vmspawn/README.md) adds:

- UID/GID 1000 development user with a persistent home and guest sudo.
- Python command helper, CA certificates, Waypipe, galculator and DHCP configuration.
- An explicit `systemd-boot-efi` dependency, needed in addition to the upstream Debian disk profile's `systemd-boot` package.
- Locked root password and removal of build-time SSH host keys. Debian's first-boot key generation and `systemd-ssh-generator` supply unique keys and a vsock listener.
- A test-only public client key. The corresponding private key remains in ignored host state.

The raw artifact is about 2.36 GiB logical/0.97 GiB allocated after sparse export, with a btrfs root and 512 MiB ESP. A separate qcow2 overlay holds runtime changes. The source image was kept immutable. This small disk is adequate for the experiment, not the intended development-environment capacity.

Artifact: `build/vmspawn/share/nsl-debian-vm-v3.raw`.

SHA256: `2a2e8b60b58315b2e01acf9d6d8dae5104bd0f3b67e6957ed5f355ec498e12d0`.

Package repositories are rolling, so source pinning does not imply bit-for-bit reproducible images. Signing, provenance publication, image updates and removal of experiment-specific authorization remain prerequisites for distribution.

## Phase 2 — Complete the vmspawn workflow

Host: Snow Linux 13, x86_64, systemd-vmspawn/systemd-ssh-proxy 261.2, QEMU 10.0.13, virtiofsd 1.13.2, GNOME Wayland. Guest: Debian 13, kernel `6.12.107+deb13-amd64`, 2 vCPUs and 2 GiB RAM. The Lima builder was stopped before the final measurements.

### Permission and boot findings

1. **KVM group membership fixed device access.** The existing agent session lacked the new supplementary group, but `sg kvm` could open `/dev/vhost-vsock`. No permission or group changes were made by this experiment.
2. **A plain rootless vmspawn bind failed.** Its virtiofs setup logged `Failed to enter user namespace for virtiofsd: Operation not permitted`. Launching in a rootless user namespace with capabilities scoped to that namespace passed this stage. The relevant [vmspawn source](https://github.com/systemd/systemd/blob/v261/src/vmspawn/vmspawn.c) calls the [namespace helper](https://github.com/systemd/systemd/blob/v261/src/basic/namespace-util.c) from its virtiofs setup; this is consistent with the observed failure when the caller has neither the required namespace capability nor a namespace FD to enter.
3. **The primary group matters for file sharing.** Making `kvm` the process's primary group left guest GID 1000 unmapped; guest writes failed with EINVAL. A small helper now opens KVM/vsock while in `kvm`, restores the normal primary group, and passes the devices through vmspawn's documented named-descriptor interface. The VM namespace maps host UID/GID 1000 to themselves. File creation then preserved **1000:1000** on the host. [Documented descriptor interface](https://github.com/systemd/systemd/blob/v261/man/systemd-vmspawn.xml).
4. **The root must be writable during early share mounting.** mkosi removes its `/work` build directory during cleanup. vmspawn mounts the share from the initrd; with a read-only root it could not recreate the mount point. Passing the `rw` kernel argument fixed this. An earlier failed boot had also passed the one-time key-generation stage without usable keys, so the final run used a fresh overlay. Failed overlays remain separate for inspection.
5. **Debian already supplies vsock SSH.** An initial custom socket conflicted with the generated listener. The final image relies on the distro's generator and contains no custom SSH listener service.
6. **The minimal package set needed CA certificates.** An outbound HTTPS check reached Debian but failed certificate verification. Adding `ca-certificates` produced the final v3 image; the complete lifecycle harness was repeated on a fresh overlay.

The final guest had zero failed systemd services. Certificate-verified HTTPS returned 200, and `apt-get update` fetched and verified the Debian package indexes successfully. The launch path uses no host sudo and grants no host-root capabilities. The user-namespace and descriptor steps are extra integration work that the Lima baseline did not need.

### Adapter responsibilities

The experimental adapter supports one environment named `probe`. It starts/stops a transient user unit, waits for authenticated guest command readiness, preserves host-key trust, runs encoded argv requests over vsock SSH, and invokes Waypipe for GUI commands.

For localhost networking, a second user service polls guest TCP listeners once per second and adds/removes SSH forwards on host `127.0.0.1`. This passed the IPv4 HTTP check. General IPv6 handling, richer conflict reporting, lower-overhead discovery and multi-environment management remain unimplemented. The adapter and device helper comprise roughly 235 lines of Python, separate from the production Go CLI.

## Phase 3 — Compare measured behavior

| Metric | Lima + Debian cloud image | vmspawn + nspawn-derived image |
| --- | --- | --- |
| Warm no-op command success | 50/50 | 50/50 |
| Warm median / p95 | 40.77 / 44.53 ms | 62.51 / 66.21 ms |
| Cold start + persistence success | 20/20 | 20/20 |
| Cold median / p95 | 10.65 / 11.18 s | 7.52 / 7.76 s |
| Median stop | 3.30 s | 1.23 s |
| Shared files, rename, ownership | Passed | Passed; explicit 1000:1000 write checked |
| Binary streams, quoting, exit status | Passed | Passed |
| PTY size/resize | Passed | Passed |
| Automatic host-loopback HTTP | Passed | Passed; one discovery observation 1.02 s |
| Wayland surface configure/commit | Passed | Passed |
| Native host-to-guest file notifications | Failed | Failed |

The vmspawn combination's cold p95 was about **31% lower**. Both missed the original five-second cold-start goal. Warm commands remain below 200 ms on both paths. The final image also missed native notifications for create, modify, rename, delete and nested-create host operations; a 100 ms content-polling loop detected its test edit in 101 ms.

This is a comparison of **complete implementations**, not an isolated launcher benchmark. The images, initialization work, disk formats and frontend languages differ. Lima's earlier run also had other idle test VMs active. In particular, the Python adapter contributes to its warm-command overhead, and the minimal image avoids Lima's cloud-init/guest-agent startup sequence. A same-image, same-frontend comparison is needed before attributing the difference to vmspawn itself.

As before, 20/20 cold successes give a 95% Wilson interval of approximately **83.9–100%**, and 50/50 warm successes give **92.9–100%**, under a simple binomial model. Repeated trials on one host have shared dependencies and are not evidence of broad production reliability.

The GUI check verifies Wayland protocol traffic, not visual quality, application input, clipboard, audio, GPU acceleration or desktop portals. No host reboot, suspend/resume, guest kernel update, backup/restore or alternate-distro installation was tested.

### Memory reclamation

With the builder stopped, each backend ran alone. After 12 seconds of settling, the guest allocated and released 512 MiB three times, with a ten-second wait after each allocation. Values below are **QEMU process proportional set size (PSS)** in MiB; they exclude helper processes and are not total system memory or guest RSS.

| Backend | Before first allocation | Immediately after allocation exited, cycles 1 / 2 / 3 | Ten seconds later, cycles 1 / 2 / 3 |
| --- | --- | --- | --- |
| Lima, current configuration | 533.8 | 1045.7 / 1045.5 / 1045.5 | 1045.5 / 1045.5 / 1045.5 |
| vmspawn, final image | 343.3 | 853.3 / 865.2 / 857.3 | 357.2 / 363.2 / 371.3 |

vmspawn returned approximately **486–502 MiB per cycle** within ten seconds. The Lima configuration retained almost all of the first allocation's additional host memory and reused those pages in later cycles. An earlier run on the v2 image also showed vmspawn reclaiming memory.

The inspected [systemd v261 launch code](https://github.com/systemd/systemd/blob/v261/src/vmspawn/vmspawn.c) enables `virtio-balloon` with `free-page-reporting=on`; Lima's observed QEMU command line had no balloon device. This provides a plausible mechanism for the measured difference. It does not establish that Lima cannot support equivalent configuration, or that vmspawn will reclaim package/build file caches as effectively: this test exercises anonymous memory only. Images and helper workloads also differ.

## Later / decision gates

The experiment establishes that **both image choices and both runtime directions are feasible**. The image pipeline can be adopted independently, but using this exact minimal image with Lima would require its expected provisioning and agent integration; that combination has not been tested.

Before replacing Lima:

1. Test the launch path on another supported atomic distribution and pin a realistic minimum systemd version.
2. Replace the fixed unit/CID adapter with owned multi-environment lifecycle, interruption recovery and backup/restore.
3. Test a real edit/build/live-reload workflow, with polling or guest-native source files where needed.
4. Measure a comparable image under both runtimes, including file-cache reclamation after actual builds. Check whether enabling equivalent balloon configuration closes the Lima memory gap.
5. Decide whether the measured startup/resource benefit outweighs owning command/session readiness, port discovery and guest integration updates.

The next implementation step is to port the proven launch/transport path into Go and add unique per-environment units/CIDs, configurable user identity, disk growth and recovery. Then test a real edit/build/live-reload workflow on Snow and a Fedora Atomic desktop before changing the default runtime. This is additional product work beyond the bounded comparison; no language rewrite is needed to use vmspawn.

## Reproduce and inspect

Instructions and code: [experiment README](../../experiments/vmspawn/README.md). The prepared environment is under `~/.local/share/nsl-vmspawn-eval`; run `experiments/vmspawn/driver.py exec probe -- id` or `gui probe -- galculator` after loading `build/poc/env.sh`. All test VMs are stopped at handoff.

Local evidence, intentionally ignored by Git:

- [Final trial data](../../build/vmspawn/evidence/measurement-v3.json), [trial log](../../build/vmspawn/evidence/measurement-v3.log).
- [Image build log](../../build/vmspawn/evidence/image-build-v3.log), [image checksum](../../build/vmspawn/evidence/image-v3.sha256).
- [Working share/ownership check](../../build/vmspawn/evidence/fd-start.log), [initial bind failure](../../build/vmspawn/evidence/bind-failure.log).
- [Wayland traffic](../../build/vmspawn/evidence/gui-protocol.log), [memory comparison](../../build/vmspawn/evidence/memory-comparison.json), [file probes](../../build/vmspawn/evidence/files-v3.json).
- [Verified HTTPS](../../build/vmspawn/evidence/network-tls-v3.log), [APT update](../../build/vmspawn/evidence/apt-update-v3.log), [guest health](../../build/vmspawn/evidence/guest-health-v3.log).
- [CI output](../../build/vmspawn/evidence/ci.log). `make ci` passed; experiment Python compilation, shell syntax checks and `git diff --check` also passed.

## References

- Baseline: [Lima proof of concept](vm-proof-of-concept.md).
- Roadmap: [WSL2-equivalent plan](wsl2-equivalent.md).
- Current runtime decision: [ADR-0004](../adr/0004-managed-development-vms.md).
- Current implementation: [lifecycle](../design/lifecycle.md), [CLI contract](../specs/cli.md).
