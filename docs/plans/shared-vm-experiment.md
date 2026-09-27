# Experiment: machines as containers in one shared VM

**Status: Phase 1 passed, 2026-09-27; Phase 2 next.** This plan chooses the machine topology for [ADR-0016](../adr/0016-wsl-style-machines.md) from measured evidence. The options are [ADR-0005](../adr/0005-vmspawn-and-nspawn-images.md)'s one VM per machine, or WSL2's shape: one nsl-owned VM that runs each machine as a systemd-nspawn container. The result is a new ADR that either supersedes ADR-0005's topology or records why it stands.

[ADR-0004](../adr/0004-managed-development-vms.md) deferred the shared VM because no benefit had been demonstrated. The [machine CLI](../specs/machine-cli.md) now makes several running machines a primary workflow, which changes that trade.

## Hypotheses

A shared VM would:

- Start additional machines in about a second and share one kernel and page cache.
- Replace bootable per-distro disks with signed root filesystems. nspawn's hub already publishes signed OCI images for many distros.
- Retire the per-distro bootloader, UKI, root-growth and kernel-maintenance adapters ([ADR-0007](../adr/0007-maintainable-guest-boot.md), [ADR-0010](../adr/0010-explicit-guest-root-growth.md), [ADR-0014](../adr/0014-arch-kernel-maintenance.md)).
- Share one virtiofs, forwarding and Waypipe stack across machines.

It would cost:

- nsl would ship and update the shared VM's kernel, as WSL does.
- A second boundary inside the VM for mounts, UIDs, cgroups and nested containers.
- Distro kernels and distro MAC policy would no longer apply inside machines. Fedora's enforcing-SELinux validation would not carry over.
- A weaker `--isolated`: a container escape reaches a VM that mounts host storage for the other machines.

## Phase 1 — Shared VM baseline

- Build a minimal nsl-owned VM image from the Debian trixie profile, with systemd-nspawn, virtiofs and the vsock SSH service. The image holds no user state; machines live on a separate btrfs data disk, one subvolume each.
- Launch it through the existing vmspawn path. Share the ADR-0016 allowlist through virtiofs and mount it at `/mnt/host` in the VM.
- **Done when:** the VM passes authenticated readiness, mounts the data disk and shows `/mnt/host` with host ownership.

**Result, 2026-09-27: passed.** [`experiments/shared-vm/`](../../experiments/shared-vm/README.md) builds `nsl-shared-vm-trixie-x86-64-v1` in about three minutes: the Debian trixie profile plus `systemd-container` and a machine-storage layer. Measured on Snow 13 (systemd 261.2, QEMU 10.0.13, virtiofsd 1.13.2) with 4 vCPUs and an 8 GiB ceiling. These are one-host observations.

| Check | Result |
| --- | --- |
| Authenticated readiness | First boot 8.6 s, including formatting the 128 GiB data disk; second boot 6.9 s. |
| Machine storage | The blank second disk was formatted as btrfs `nsl-machines` and mounted at `/var/lib/machines` with zstd. A marker file survived restart. A disk with an existing ext4 signature was refused and left byte-identical. |
| Host allowlist | The home (`/var/home/bjk`) and `/mnt` mounted as separate virtiofs devices under `/mnt/host`. `/run/media/bjk` did not exist, so it was not shared. |
| Ownership | Host files appear as 1000:1000. Writes by the guest user and by guest root both land on the host as 1000:1000. Root-owned host `/mnt` appears as 65534, and guest root cannot write to it. |
| Sockets | A host Unix socket appears as a socket, but connecting is refused and the host listener is never reached. |
| Idle cost, no machines | QEMU 780–820 MiB PSS; virtiofsd about 4.5 MiB in total; vmspawn 3 MiB. The root overlay held 33 MiB after two boots. |

Findings:

- **Bind-mount aliases.** This host mounts one subvolume at both `/home` and `/var/home`; neither is a symlink. The usual working directory `/home/bjk/projects` therefore does not resolve into the shared `/var/home/bjk`. Translation must match by device and inode, and machines should see `/mnt/host/home` as an alias. The [machine CLI](../specs/machine-cli.md) now says so.
- **Guest access does not trigger automounts.** An autofs point under a shared tree (`/mnt/framework-backup`) is empty in the guest until the host mounts it; afterwards its content appears without a restart. virtiofsd's `O_PATH` lookups do not trigger automounts, but mount propagation into its namespace works.
- **Nested filesystems are visible.** Mounted CIFS shares under the home and a second btrfs device under `/mnt` list with the same entry counts as on the host.
- **The VM runs systemd 257, the host 261.** Machines get Debian trixie's nspawn unless the shared VM tracks a newer systemd. Phase 2 must record any nspawn limitation this causes.
- **The whole home is visible, including nsl state and private keys.** ADR-0016 intends this. Deciding whether nsl state should move outside shared trees remains open.

Raw evidence is in ignored `build/shared-vm/evidence/phase1-*.json`; the 18:38:19 file is the deliberate refusal test.

## Phase 2 — Machines as containers

- Import Debian and Fedora root filesystems into subvolumes, from nspawn hub OCI images or the distros' container images. Add the account (host username, UID and primary GID), hostname and argv helper.
- Boot each with `systemd-nspawn --boot` in the VM's network namespace, bind `/mnt/host`, and use no private user namespace, so UIDs match across host, VM and machine.
- Route commands through the VM's SSH forced command into the target machine. Compare `systemd-run --machine`, `machinectl shell` and `nsenter` for PTY, signal and exit-status fidelity.
- **Done when:** two machines run concurrently and each passes the argv, PTY, exit-status and binary-stream checks with separate hostnames and homes.

## Phase 3 — Workload acceptance

Run inside each machine, reusing `scripts/probe-development.py`, `probe-files.py` and `probe-native.py` where they fit:

- systemd as PID 1, a working user session, package install and removal, `sudo`.
- Rootless Podman build, run, network and volume, without a privileged container.
- `/mnt/host` ownership, spaces, symlinks, executable bits, rename and delete, including one file edited from two machines.
- Localhost forwarding from each machine, including a same-port conflict between machines.
- A Waypipe GUI application launched from inside a machine.
- Directory translation, and stop/start preserving packages, home and services.
- **Done when:** each check has pass/fail evidence for Debian and Fedora, then Arch and openSUSE Tumbleweed for systemd-version and family breadth.

## Phase 4 — Measurements against one VM per machine

Use the same host (Snow 13), distros and guest memory ceiling for both topologies. Record versions and configuration.

- Host proportional set size of all QEMU and virtiofsd processes: idle with 1, 2 and 4 machines, after one real build in each machine, and after dropping guest caches.
- Cold start of the first machine and warm start of an additional machine: 20 trials each, median and p95.
- Warm no-op `run` latency: 50 trials.
- Compressed artifact size per distro: root filesystem compared with bootable disk.
- **Done when:** a JSON evidence file under `build/` and a summary table in this plan cover both topologies.

## Decision rule

The thresholds below are proposals; adjust them before measuring, not after.

- **Adopt the shared VM** if Phase 3 passes for Debian and Fedora with every weakened security control documented, four idle machines use at most half the memory of four separate VMs, and an additional machine starts at p95 ≤ 2 seconds.
- **Keep one VM per machine** if rootless Podman or systemd user sessions cannot work without a privileged container, or the measured benefit misses the thresholds.
- **If adopted,** the ADR must also settle how `--isolated` works (see open questions), the image contract split, and which ADR-0007/0010/0014 adapters are retired.

## Later / ideas

- virtiofs DAX and KSM across machines.
- Stopping the shared VM once no machine is running.
- Per-machine cgroup limits inside a global VM budget.

## Open questions

| Question | Default proposal | Resolve by |
| --- | --- | --- |
| Container manager? | systemd-nspawn, for machined integration and prior nsl use. | Phase 2 |
| Shared UIDs or a private user namespace per machine? | Shared UIDs, matching the trust model. | Phase 2 |
| Machine storage? | btrfs subvolumes on one data disk: no hot-plug, cheap snapshots. Measure export and removal. | Phase 2 |
| Shared VM image ownership? | Immutable and nsl-updated as a unit. The distro is Debian for the experiment only. | Adoption ADR |
| `--isolated` under a shared VM? | Record what an escape reaches. A separate VM for isolated machines may be the one justified exception to a single topology. | Adoption ADR |
| Absolute host symlinks into shared trees? | Link the host's canonical top-level directory (for example `/var/home`) to `/mnt/host` inside machines where it is absent. | Phase 3 |
| cloud-init inside containers? | Test NoCloud in Debian and Fedora containers before revising the [provisioning contract](../specs/provisioning.md). | Phase 3 |

## References

- Decision: [ADR-0016](../adr/0016-wsl-style-machines.md). Interface: [machine CLI](../specs/machine-cli.md).
- Current topology: [ADR-0005](../adr/0005-vmspawn-and-nspawn-images.md), [lifecycle](../design/lifecycle.md), [guest contract](../specs/guest-images.md).
- Prior comparison: [WSL2 roadmap](wsl2-equivalent.md), [vmspawn comparison](vmspawn-comparison.md), [nspawn image route](wsl2-equivalent.md#reusing-nspawn-images-recipes-and-code-are-separate-choices).
- [WSLg architecture](https://github.com/microsoft/wslg#user-distro), [nspawn hub images](https://nspawn.org/docs/images/).
