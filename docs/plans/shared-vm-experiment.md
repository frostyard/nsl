# Experiment: machines as containers in one shared VM

**Status: all four phases complete, 2026-09-27. The decision rule is met, and [ADR-0017](../adr/0017-shared-vm-and-machine-images.md) adopts the shared VM and Frostyard machine images.** This plan chooses the machine topology for [ADR-0016](../adr/0016-wsl-style-machines.md) from measured evidence. The options are [ADR-0005](../adr/0005-vmspawn-and-nspawn-images.md)'s one VM per machine, or WSL2's shape: one nsl-owned VM that runs each machine as a systemd-nspawn container. The result is a new ADR that either supersedes ADR-0005's topology or records why it stands.

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

**Result, 2026-09-27: passed.** [`machines.py`](../../experiments/shared-vm/README.md#machines) pulls hub.nspawn.org images. It verifies the key-signed DSSE bundle against the project key pinned from `mkosi-definitions` `68263d0`, using `openssl`; a random key and a mismatched digest are both rejected. As VM root, it imports the single zstd layer into a btrfs subvolume. Creation then:

- adds the host account (`bjk`, 1000:1000) and sets the hostname;
- locks the image's `root:root` password;
- masks the image's networkd and resolved;
- installs `pam_systemd` and `sudo`;
- writes a `.nspawn` file: `PrivateUsers=no`, the VM's network namespace, `Bind=/mnt/host` and the VM's resolver.

| | Debian 13 | Fedora 44 |
| --- | --- | --- |
| Hub image | 20260927, 76 MB layer | 20260927, 112 MB layer |
| Create, including bootstrap | 5.2 s (packages 3.9 s) | 8.5 s (packages 6.2 s) |
| First boot to `running` | 0.68 s | 0.43 s |
| systemd inside | 257 | 259 |
| Command latency, median of 10 | 73 ms | 75 ms |

Both machines ran concurrently with distinct hostnames, machine IDs and homes. A file in one home was absent from the other. Entry-method comparison, identical on both machines unless noted:

| Check | `systemd-run --machine` | `nsenter` | `machinectl shell` |
| --- | --- | --- | --- |
| Literal argv | pass¹ | pass | fail: PTY newline translation |
| Exit status | pass | pass | fail: always 0 |
| Signal death as 128+N | pass² | fail: 255 | fail: 0 |
| Separate stdout and stderr | pass | pass | fail: merged |
| 1 MiB binary stream | pass | pass | fail: truncated at stdin EOF |
| Account, hostname, home, `/mnt/host` ownership | pass | pass | Debian only |
| logind session and user manager | pass³ | fail: VM session cgroup | Debian only |
| PTY | pass⁴ | fail: VM `/dev/pts` invisible | fail: forwarder escapes |
| Ctrl-C | pass | pass | pass |
| Median latency | 73–75 ms | 30–32 ms | 76–86 ms |

`systemd-run --machine` is selected. Each adjustment below was found by a failing check:

1. `--expand-environment=no`. `ExecStart=` semantics otherwise expand `$VAR`, `${VAR}` and `$$` in argv.
2. systemd counts SIGTERM, SIGINT, SIGHUP and SIGPIPE deaths as clean (`systemd-run` returns 0), and SIGKILL or SIGSEGV as 255. A non-exec `/bin/sh -c '"$@"; exit $?'` parent restores 128+N. Production should instead use a VM-side agent that reads `ExecMainCode`/`ExecMainStatus` over D-Bus.
3. A per-distro PAM service: Debian's `runuser-l`, whose modules fall back to its permissive `common-*`, and Fedora's `systemd-run0`, since Fedora's `other` denies account management. Neither distro ships one stack that works for both.
4. `SYSTEMD_ADJUST_TERMINAL_TITLE=0 SYSTEMD_COLORS=0`. The PTY forwarder otherwise injects title and color sequences.

Findings:

- **Machines in the VM's network namespace must use the VM's resolver.** Hub images run networkd and resolved for a private nspawn bridge. Inside the VM's namespace, Fedora's `nss-resolve` queried its own resolver, which had no upstream servers. Debian's libc happened to reach the VM's stub. Machines now mask both services and bind the VM's `/run/systemd/resolve`.
- **Hub images are not user machines as shipped.** They have root password `root`, no `pam_systemd` and no `sudo`, and Debian cannot resolve its own hostname (no `nss-myhostname`, so creation adds it to `/etc/hosts`). Creation needs a per-distro bootstrap, like the image profiles' adapters.
- **Fedora runs without SELinux.** The VM kernel does not enable it, so Fedora's enforcing-SELinux acceptance does not transfer.
- **systemd 258+ marks PTY sessions with OSC 3008 context sequences** naming the machine and user. Terminals such as Ptyxis use these to label tabs, which could provide WSL-style terminal integration without generated profiles.
- **A newer systemd inside a machine works.** Fedora's 259 booted under the VM's nspawn 257.
- **Image transfer went through `/mnt/host`.** The host cache lives in the home and the VM read the layer through the share. Production needs a dedicated nsl share, so a VM without host files can still import.
- **Ctrl-C took 1.3–2.0 s end to end with every method, including with no machine involved.** The delay is in the SSH transport or the harness, not the topology. It needs a separate check from a real interactive terminal.
- **Memory is not yet measured.** With two machines after package installs, QEMU was at 2.0 GiB PSS and virtiofsd at 208 MiB; Phase 4 measures this properly.

Raw evidence is in ignored `build/shared-vm/evidence/phase2-*.json`; the 19:06:58 file is the final run.

## Phase 3 — Workload acceptance

Run inside each machine, reusing `scripts/probe-development.py`, `probe-files.py` and `probe-native.py` where they fit:

- systemd as PID 1, a working user session, package install and removal, `sudo`.
- Rootless Podman build, run, network and volume, without a privileged container.
- `/mnt/host` ownership, spaces, symlinks, executable bits, rename and delete, including one file edited from two machines.
- Localhost forwarding from each machine, including a same-port conflict between machines.
- A Waypipe GUI application launched from inside a machine.
- Directory translation, and stop/start preserving packages, home and services.
- **Done when:** each check has pass/fail evidence for Debian and Fedora, then Arch and openSUSE Tumbleweed for systemd-version and family breadth.

**Result, 2026-09-27: passed.** [`workloads.py`](../../experiments/shared-vm/README.md#workloads) ran all eight checks on four freshly created machines in one VM: 32 of 32 passed. The CLI-bound probes hard-code `/work` and `nsl exec`, so their Podman and watcher workflows were ported rather than reused. All four hub images were dated 20260927.

| | Debian 13 | Fedora 44 | Arch | Tumbleweed |
| --- | --- | --- | --- | --- |
| Create, including bootstrap | 6.1 s | 9.6 s | 10.6 s | 11.0 s |
| Workload install (python3, jq, Podman, wayland-utils, GUI app) | 17.4 s | 24.4 s | 14.4 s | 5.9 s |
| Rootless Podman | 5.4.2 | 5.8.7 | 6.1.2 | 6.0.2 |
| GUI application | galculator | galculator | galculator | foot |
| Machine start after a VM restart | 0.46 s | 0.71 s | 1.64 s | 0.70 s |
| Disk after workloads | 1.14 GiB | 1.35 GiB | 1.43 GiB | 0.53 GiB |

Every machine reached `running` with no failed units. The checks covered:

- package install and removal, and `sudo`;
- rootless Podman: build, `--userns=keep-id` volume ownership, container HTTPS, and a published port reachable from the machine, the VM and a peer machine;
- `/mnt/host` operations: spaces and Unicode, relative symlinks, executable bits, rename, delete and fsync;
- a user service forwarded to host loopback;
- directory translation;
- a Waypipe window;
- packages, home, an enabled system service and a Podman image surviving a VM restart.

Findings:

- **Nested containers need three machine-level adjustments,** applied by creation to every distro. The first is a spare, fully visible procfs mount: the kernel's `mount_too_revealing` rule refuses a new procfs in a user namespace while nspawn masks `/proc`; LXC and Incus nest the same way. The other two are `keyring = false`, because nspawn filters keyring syscalls, and `default_sysctls = []`, because `/proc/sys` is read-only.
- **With `PrivateUsers=no`, machine root is effectively VM root.** The spare procfs exposes writable kernel sysctls, so nspawn's `/proc` restrictions were hygiene, not a boundary. This fits the trust model and confirms that an `--isolated` machine cannot be a peer in the shared VM.
- **Integration units need preset files.** Images with an uninitialized machine ID apply presets on first boot. Fedora's disable-all preset silently undid `systemctl --root enable`.
- **Machines show the host's time zone.** Creation links `/etc/localtime` to the host's zone before anything runs, and nspawn leaves it alone (`Timezone=off`). The VM itself remains on UTC.
- **Cross-machine file semantics beat separate VMs.** An edit in one machine produced inotify events in another, and `flock` conflicts between machines were honored, because both share one kernel. Host edits still produced no events (polling saw them in 0.05 s). The host acquired a lock a machine held. These last two apply to both topologies.
- **Networking behaves like WSL.** Machines share the VM's network namespace, so a port in one is visible to the VM and to its peers, and binding the same port in a second machine fails with `Address already in use`. The VM sees machine listeners, so today's forwarding mechanism works unchanged.
- **GUI works with one persistent Waypipe session per machine.** The host runs `waypipe --display /run/nsl-wayland/NAME/wayland-0 ssh … sleep`, and `machinectl bind` places that directory in the running machine without a restart. All four machines saw 33 Wayland interfaces, including `xdg_wm_base`.
- **Directory translation handled the bind-mount alias.** A `/mnt/host/home → var/home` symlink in the VM, plus device-and-inode matching, mapped the worktree under `/home/bjk/…` correctly. `/usr/share` was correctly refused.
- **Machines do not start with the VM.** Autostart through `machines.target` or lazy start per command is a design choice for the adoption ADR.

Raw evidence is in ignored `build/shared-vm/evidence/phase3-*.json`; the 19:40:28 file is the final four-machine run.

## Image source tally: hub.nspawn.org or our own

A running record, updated each phase, for deciding whether to publish Frostyard machine images. "Own image fixes it" assumes images built from the same pinned `mkosi-definitions` recipes plus an nsl layer, published through the existing [signed delivery pipeline](../design/image-publication.md).

| # | Found | Pain point with hub images | Impact | Experiment workaround | Own image fixes it |
| --- | --- | --- | --- | --- | --- |
| 1 | Phase 2 | Recipe sets `RootPassword=root`; confirmed in Debian, Fedora, Arch and Tumbleweed. | Every machine starts with a known root password. | Lock root at creation. | Yes |
| 2 | Phase 2 | networkd and resolved are enabled for a private nspawn bridge. | In the VM's network namespace, Fedora DNS fails. | Mask seven units; bind the VM's resolver. | Yes |
| 3 | Phase 2 | No `pam_systemd`: Debian lacks `libpam-systemd`, Fedora lacks `systemd-pam`. Arch and Tumbleweed include it. | No logind session, runtime directory or user manager. | Install at creation. | Yes |
| 4 | Phase 2 | No `sudo` in any of the four images. | A passwordless account cannot administer its machine. | Install at creation; NOPASSWD rule. | Yes |
| 5 | Phase 2 | Debian cannot resolve its own hostname. | `sudo` and similar tools warn on every run. | Add `127.0.1.1 NAME` to `/etc/hosts`. | Yes |
| 6 | Phase 2 | No PAM stack works on both distros: Debian has no `login` or `systemd-run0`, and Fedora's `other` denies. | Entry needs per-distro PAM knowledge. | Map Debian to `runuser-l`, Fedora to `systemd-run0`. | Yes: ship one `nsl` PAM service |
| 7 | Phase 2 | Signed with nspawn.org's key and identity. Mutable tags (`13` moves daily), no signed catalogue, no rollback or freshness policy. | Does not meet [ADR-0015](../adr/0015-image-verification-and-catalogue-policy.md); trust rests on a third party's key. | Pin the key; verify the manifest digest. | Yes |
| 8 | Phase 2 | Items 3–5 need network access and live repositories at creation. | Creation cannot be offline, is not reproducible, and adds 4–6 s. | Accepted for the experiment. | Yes |
| 9 | Phase 3 | Debian ships `/etc/localtime` as a regular file, with no `tzdata` or zone data. | nspawn can only bind the VM's zone over it, so the first `tzdata` install in every new machine fails with `EBUSY`. Every package depending on `tzdata` (python3, Podman's CRIU bindings) is left unconfigured. | Link `/etc/localtime` to the host's zone right after extraction; `Timezone=off` for boots and offline runs. | Yes |
| 10 | Phase 3 | Debian lacks `dbus-user-session`; Fedora's `dbus-broker` provides the user bus. | No user D-Bus: Podman falls back to cgroupfs, and desktop applications lose their session bus. | Install at creation. | Yes |
| 11 | Phase 3 | Arch ships a populated pacman keyring, including the local master **private key** (`private-keys-v1.d`, `secring.gpg`). | Every machine from the image shares one master key that anyone holding the image also holds. Keys it certifies are fully trusted, so this is a package supply-chain risk. | Delete the keyring; `pacman-key --init` and `--populate` at creation. | Yes |
| 12 | Phase 3 | Tumbleweed has no shadow tools: no `useradd`, `groupadd`, `usermod` or `passwd`. | Accounts, subordinate UID ranges and root locking need a package install first. | Bootstrap installs `shadow` before creating the account. | Yes |
| 13 | Phase 3 | No desktop baseline: Tumbleweed has no fonts. On the other distros, galculator's GTK dependencies happened to pull some in. | The Wayland connection works, but the first GUI application can fail to start (`foot`: failed to match font). | Install a font with the workload. | Yes: a desktop layer with fonts and a cursor theme |

In favor of hub images so far:

- Small single-layer zstd OCI images (Debian 76 MB, Fedora 112 MB, Arch 202 MB, Tumbleweed 73 MB) that boot systemd and D-Bus in under a second.
- After bootstrap, all four passed the full Phase 3 workload, including rootless Podman and GUI.
- No image shares a machine ID: all four ship `uninitialized`.
- Daily rebuilds with distro security updates, plus dated tags for pinning.
- A broad catalogue: Arch, Debian, Ubuntu, Fedora, CentOS, Alma, Rocky, openSUSE and Kali, plus development and language variants.
- Signed with both a project key and a keyless Sigstore identity, discoverable through the OCI referrers API.
- The recipes are the ones nsl already pins, so rebuilding our own is the same Lima/mkosi pipeline without the disk profile.

### Assessment: publish Frostyard machine images

After four phases the tally holds 13 pain points, and every one was worked around at creation time; none blocked a workload. The case for our own images rests on four of them:

- **Trust (item 7).** Hub images meet neither the signing identity nor the catalogue rollback and freshness policy of [ADR-0015](../adr/0015-image-verification-and-catalogue-policy.md). Pinning a third-party key does not change that.
- **Security hygiene (items 1 and 11).** Every image ships a root password of `root`, and Arch ships its pacman master private key. Correcting another publisher's defaults on every creation is a standing obligation.
- **Offline, reproducible creation (item 8).** The per-distro bootstrap needs live repositories, adds 4–7 s and makes each machine depend on repository state at creation time.
- **One integration layer at build time.** Items 2–6, 9, 10, 12 and 13, the nesting mount, the presets and the Podman drop-in currently run as fragile per-distro steps on every creation. Built once and tested with the image, they become the image contract.

The cost is small. The images come from the same pinned `mkosi-definitions` recipes without the disk profile, so they need no bootloader, UKI, root-growth or kernel adapter per distro. They would be published through the existing signed pipeline, and should stay close to the hub's sizes: about a fifth of the bootable disks. The hub remains the upstream reference, and a fallback for distributions we do not build.

**Decision:** [ADR-0017](../adr/0017-shared-vm-and-machine-images.md) adopts Frostyard machine images built and signed from the same recipes, starting with the four distros tested here.

## Phase 4 — Measurements against one VM per machine

Use the same host (Snow 13), distros and guest memory ceiling for both topologies. Record versions and configuration.

- Host proportional set size of all QEMU and virtiofsd processes: idle with 1, 2 and 4 machines, after one real build in each machine, and after dropping guest caches.
- Cold start of the first machine and warm start of an additional machine: 20 trials each, median and p95.
- Warm no-op `run` latency: 50 trials.
- Compressed artifact size per distro: root filesystem compared with bootable disk.
- **Done when:** a JSON evidence file under `build/` and a summary table in this plan cover both topologies.

**Result, 2026-09-27: complete.** [`measure.py`](../../experiments/shared-vm/README.md#measurements) ran both topologies on Snow 13 (32 host CPUs, 60 GiB, KSM off, THP `always`), never both at once.
- **Shared VM:** 4 vCPUs and 8 GiB, running the four Phase 3 machines.
- **One VM per machine:** the nsl CLI (`v0.3.0-5-gcd2891f`) in its own `NSL_HOME`, with four signed catalogue images at the default 2 vCPUs and 2 GiB each.

Memory is the PSS of every process in the VM units, taken as the median of three samples after 30 s of settling. The build is nsl itself: `go build -p 2`, offline, from one staged toolchain, module cache and source tree shared read-only. That is `/work` for the separate VMs and `/mnt/host` for the machines.

| Measurement | Shared VM | One VM per machine | Shared ÷ per-VM |
| --- | --- | --- | --- |
| Idle, 1 machine | 723 MiB | 355 MiB | 2.04 |
| Idle, 2 machines | 802 MiB | 795 MiB | 1.01 |
| Idle, 4 machines | **950 MiB** | 2,328 MiB | **0.41** |
| After one build in each of 4 | 2,612 MiB | 5,586 MiB | 0.47 |
| After dropping guest caches | 1,442 MiB | 3,871 MiB | 0.37 |
| Cost of each additional idle machine | about 75 MiB | 440–770 MiB | |
| Cold start of the first machine, median / p95 | 7.97 / 8.33 s | 7.05 / 7.40 s | 1.13 |
| Start of an additional machine, median / p95 | **0.63 / 0.84 s** | 7.58 / 7.79 s | 0.08 |
| No-op round trip over SSH, median | 65 ms | 17 ms | |
| No-op command, median (per-VM through `nsl exec`) | 66 ms | 74 ms | |
| Build, seconds per machine | 21–23 | 30–34 | |
| Compressed download, four distros | 464 MB (rootfs) | 2,336 MB (disks) | 0.20 |

Per distro, the compressed downloads are Debian 76 against 508 MB, Fedora 112 against 409 MB, Arch 203 against 847 MB, and Tumbleweed 74 against 572 MB. The separate VMs also ran four forwarder services, 51–56 MiB in total, which are not included above.

**Decision rule.** Phase 3 passed on all four distros with the weakened controls documented. Four idle machines used 41% of the memory of four VMs, within the 50% threshold. An additional machine started at p95 0.84 s, within 2 s. **All three criteria are met: adopt the shared VM.**

Caveats the adoption ADR must carry:

- **A single machine costs twice as much.** The shared VM idled at 723 MiB with one machine, against 355 MiB for one VM, and broke even at two. Its 8 GiB memory map, nspawn and a second systemd, the btrfs data disk and caches from Phase 3 all contribute; the split is unmeasured. Sizing the VM to its running machines (virtio-mem or balloon targets) is follow-up work.
- **The first cold start is 0.9 s slower,** because the VM boots and then the machine does. Neither topology meets the roadmap's provisional 5 s cold-shell goal.
- **Commands pay about 48 ms more in transport** for `sudo`, `systemd-run` and a PAM session per command. Per-VM `nsl exec` spends similar time on Go startup and readiness, so the CLI-level medians are close. A VM-side agent could reuse sessions.
- **Build times are confounded.** Both used `-p 2`, but the shared VM had 4 vCPUs against 2 per VM. It also served every build's toolchain and module reads from one guest page cache, where each separate VM cached its own copy.
- **virtiofsd memory is significant under file load in both topologies:** 769 MiB shared and 1,287 MiB per-VM after the builds, with 229 and 716 MiB still held after dropping guest caches. It deserves its own measurement.
- **KSM was off.** Enabling it would narrow the per-VM gap somewhat, at a CPU and side-channel cost.

Raw evidence is in ignored `build/shared-vm/evidence/phase4-2026-09-27T231157+0000.json`.

## Decision rule

The thresholds below are proposals; adjust them before measuring, not after.

- **Adopt the shared VM** if Phase 3 passes for Debian and Fedora with every weakened security control documented, four idle machines use at most half the memory of four separate VMs, and an additional machine starts at p95 ≤ 2 seconds.
- **Keep one VM per machine** if rootless Podman or systemd user sessions cannot work without a privileged container, or the measured benefit misses the thresholds.
- **If adopted,** the ADR must also settle how `--isolated` works (see open questions), the image contract split, and which ADR-0007/0010/0014 adapters are retired. It must also settle the VM-side execution agent, the per-distro machine bootstrap, and whether machine images come from the hub's key or a Frostyard-signed rebuild.

## Later / ideas

- virtiofs DAX and KSM across machines.
- Stopping the shared VM once no machine is running.
- Per-machine cgroup limits inside a global VM budget.

## Open questions

| Question | Default proposal | Resolve by |
| --- | --- | --- |
| Container manager? | systemd-nspawn, for machined integration and prior nsl use. Phase 2 used it with `systemd-run --machine`. | Phase 2: works |
| Shared UIDs or a private user namespace per machine? | Shared UIDs, matching the trust model. Ownership matched across host, VM and machine. | Phase 2: works |
| Machine storage? | btrfs subvolumes on one data disk: no hot-plug, cheap snapshots. Import and removal worked; measure export. | Phase 3 |
| Shared VM image ownership? | Immutable and nsl-updated as a unit. The distro is Debian for the experiment only. | Adoption ADR |
| `--isolated` under a shared VM? | Record what an escape reaches. A separate VM for isolated machines may be the one justified exception to a single topology. | Adoption ADR |
| Absolute host symlinks into shared trees? | Link the host's canonical top-level directory (for example `/var/home`) to `/mnt/host` inside machines where it is absent. Phase 3 tested top-level aliases under `/mnt/host` only. | Adoption ADR |
| cloud-init inside containers? | Test NoCloud in Debian and Fedora containers before revising the [provisioning contract](../specs/provisioning.md). Not tested in Phase 3. | Adoption ADR |

## References

- Decision: [ADR-0016](../adr/0016-wsl-style-machines.md). Interface: [machine CLI](../specs/machine-cli.md).
- Current topology: [ADR-0005](../adr/0005-vmspawn-and-nspawn-images.md), [lifecycle](../design/lifecycle.md), [guest contract](../specs/guest-images.md).
- Prior comparison: [WSL2 roadmap](wsl2-equivalent.md), [vmspawn comparison](vmspawn-comparison.md), [nspawn image route](wsl2-equivalent.md#reusing-nspawn-images-recipes-and-code-are-separate-choices).
- [WSLg architecture](https://github.com/microsoft/wslg#user-distro), [nspawn hub images](https://nspawn.org/docs/images/).
