# Plan: Ubuntu, CentOS Stream and openSUSE Leap machine images

**Status: complete, 2026-09-28. Catalogue sequence 7 publishes all seven machine images.**

Add the three machine images that [ADR-0009](../adr/0009-distribution-neutral-guest-contract.md) names after the first four: Ubuntu 26.04 LTS, CentOS Stream 10 and openSUSE Leap 16.0. Each joins the catalogue only after it passes the [machine-image acceptance](../specs/machine-images.md#acceptance). The [shared-VM implementation plan](shared-vm-implementation.md) listed them as later work.

## Phase 1 — Profiles and acceptance

- **Profiles.** Each image is a profile of an existing family, built from the pinned `nspawn/mkosi-definitions` recipes (`68263d0`), which already carry `ubuntu` `resolute`, `centos` `10` and `opensuse` `16.0`:
  - Ubuntu 26.04 LTS (`resolute`) in the `debian` family. Its profile selects the Fedora 44 tools tree, which carries the Ubuntu archive keyring that the Debian builder lacks.
  - CentOS Stream 10 in the `rpm` family, with the Fedora 44 tools tree. The recipe adds EPEL.
  - openSUSE Leap 16.0 in the `suse` family, with the Tumbleweed tools tree.
- **Probe.** `probe-machines.py` chooses package commands by the descriptor's family rather than its distribution, and names machines `DISTRIBUTION-RELEASE`, since Tumbleweed and Leap share `opensuse`. galculator is not packaged for CentOS Stream 10 or Leap 16.0: Leap uses foot, as Tumbleweed does, and CentOS zenity.
- **Memory gate.** `measure-machines.py` takes exactly four images, and the publisher passes the Debian, Fedora, Arch and Tumbleweed images that set the 950 MiB budget, so seven images do not change what the gate measures.
- **Network masks.** The Ubuntu build showed that the recipes also enable `systemd-networkd-resolve-hook.socket`, which the machine layer did not mask; the published Fedora r3 and Arch r4 images had it enabled too, and Arch also ships `systemd-networkd-varlink-metrics.socket`. The finalize script now masks every networkd and resolved unit an image installs, and the tally requires every one of them to be masked, not just the two services. This changed the common layer, so Debian, Fedora, Arch and Tumbleweed moved to r4, r4, r5 and r4.
- **Done when:** all seven machine images build, and every check of `probe-machines.py --gui --isolated` passes on each with VM image r8.

**Result, 2026-09-28: complete.**

| Image | systemd | Size (zstd) | Entry latency, median |
| --- | --- | --- | --- |
| `nsl-machine-debian-trixie-x86-64-r4` | 257.13 | 96 MB | 67 ms |
| `nsl-machine-ubuntu-resolute-x86-64-r1` | 259.5 | 109 MB | 59 ms |
| `nsl-machine-fedora-44-x86-64-r4` | 259.9 | 119 MB | 62 ms |
| `nsl-machine-centos-10-x86-64-r1` | 257 | 167 MB | 67 ms |
| `nsl-machine-arch-rolling-x86-64-r5` | 262 | 212 MB | 72 ms |
| `nsl-machine-opensuse-tumbleweed-x86-64-r4` | 261.2 | 86 MB | 58 ms |
| `nsl-machine-opensuse-16.0-x86-64-r1` | 257.13 | 83 MB | 66 ms |

- **Builds.** Each new image built on the first attempt from the pinned recipes, in one to two minutes with a warm builder. Ubuntu's recipe runs `passwd -u root`, but mkosi's first-boot settings run later and leave root locked (`root:!`). Ubuntu 26.04's `sudo` package is classic sudo (`sudo.ws`), not sudo-rs.
- **Acceptance** (`machines-probe-seven-1.json`, VM image r8, `--gui --isolated`): seven shared machines and an isolated Debian machine in one VM ran 92 checks. All passed but CentOS's GUI check: zenity, a GTK 4 application, aborted because `libGLESv2.so.2` was missing. A scratch machine confirmed that CentOS Stream 10's `gtk4` 4.16 does not pull in `libglvnd-gles`, while Fedora 44's `gtk4` 4.22 does. After installing it, zenity opened its window. The CentOS profile now installs `libglvnd-gles`, and the rebuilt image passed all 12 checks beside a Debian peer (`machines-probe-seven-2.json`). The package requires Mesa's EGL, and with it the DRI drivers and LLVM, which grows the image from 106 to 167 MB compressed. Every GTK 4 application installs those anyway; without them, the first one a user runs dumps core, and every image declares the `gui` capability.
- **Memory** (`measure-seven-1.json`, the Debian r4, Fedora r4, Arch r5 and Tumbleweed r4 images): four idle machines at 862 MiB against the 950 MiB budget, an additional machine at p95 0.67 s, and a no-op command at a median of 86 ms.

## Phase 2 — Publication

- Add the selectors `ubuntu:resolute`, `ubuntu:26.04`, `centos:10`, `centos-stream:10`, `opensuse:16.0` and `opensuse-leap:16.0` to the publisher.
- Dispatch `images.yml` from `main`. The CLI needs no change: it is distribution-neutral, and catalogue sequence 7 is above its minimum of 6.
- **Done when:** a signed catalogue lists all seven machine images, and `nsl create` works from it for each new selector on a host with an empty cache.

**Result, 2026-09-28: complete.** PR frostyard/nsl#8 merged as `e8e4790`; no CLI release was needed.

- **Publication** ([run 7](https://github.com/frostyard/nsl/actions/runs/36412981733), 31 minutes on an ephemeral runner in the maintainer's session): the VM image r8 and the seven machine images were built from `e8e4790` and passed every check on KVM. The VM probe passed; `probe-machines.py --gui --isolated` ran 92 checks on seven shared machines and an isolated one, with none failing. `measure-machines.py` measured four idle machines at 843 MiB, an additional machine at p95 0.55 s, and 159 MiB more for four desktop sessions. The run signed and pushed each image and promoted catalogue sequence **7**, manifest `sha256:c78902f64be601483fe63586c19214ba05e172fed7836db8551b90cd0efdf7d8`, which expires 2026-10-28. Published sizes match the local builds: 83 MB (Leap) to 212 MB (Arch), with CentOS at 167 MB.
- **Clean host.** The released v0.4.0 binary, with a new state directory and an empty cache, listed catalogue 7 with all eight images. `nsl create` with `ubuntu:26.04`, `centos:10` and `opensuse-leap:16.0` verified and downloaded each image and created the machine: 30 s for the first, including the VM image and its first boot, then 10 s and 6 s. `nsl run` in each reported Ubuntu 26.04.1 LTS, CentOS Stream 10 and openSUSE Leap 16.0, as the host account, in `/mnt/host/var/home/bjk`.

## Later / ideas

- Further releases of the same families, such as Debian 12, Ubuntu 24.04 LTS, Fedora 43 or CentOS Stream 9, each as a profile that passes acceptance.
- AlmaLinux and Rocky Linux, which the recipes also carry, in the `rpm` family.
- Azure Linux 4.0, which the recipes do not carry: [its own plan](azure-linux.md).
- Track SUSE Linux Enterprise separately, including access and redistribution terms ([ADR-0009](../adr/0009-distribution-neutral-guest-contract.md)).

## Open questions

- Should the memory budget cover more than four idle machines now that the catalogue has seven images? The gate stays at four, as the experiment measured, until a use case asks for more.

## References

- Implements: [machine images](../specs/machine-images.md), [image publication](../design/image-publication.md), [ADR-0009](../adr/0009-distribution-neutral-guest-contract.md).
- Builds on: [Machines in a shared VM](shared-vm-implementation.md), Phases 4 and 10.
