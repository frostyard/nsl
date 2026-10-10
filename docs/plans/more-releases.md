# Plan: Debian testing, Fedora Rawhide and Ubuntu 24.04 machine images

**Status: Phase 1 complete, 2026-10-10. Phase 2 waits for a publication run.**

Add three releases of families the catalogue already carries: Debian testing and Fedora Rawhide, the development branches, and Ubuntu 24.04 LTS, the previous LTS. Each joins the catalogue only after it passes the [machine-image acceptance](../specs/machine-images.md#acceptance), under [ADR-0009](../adr/0009-distribution-neutral-guest-contract.md). The [plan for Ubuntu, CentOS Stream and Leap](more-machine-images.md) listed further releases of the same families as later work.

## Phase 1 — Profiles and acceptance

- **Profiles.** Each image is a new profile of an existing family, built from the pinned `nspawn/mkosi-definitions` recipes (`68263d0`). The recipes carry `fedora` `rawhide`, `ubuntu` `noble` and `debian` `sid`; mkosi builds any Debian suite, and the Debian recipe has no release-specific settings.
  - Debian testing (`debian-testing`), release `testing`, in the `debian` family with the builder's own tools. The image's apt sources name `testing`, so a machine follows testing past forky's release, as Tumbleweed and Arch machines follow their distros. Its os-release has no `VERSION_ID`, so the descriptor's `os_version` is `rolling`, as Arch's is.
  - Fedora Rawhide (`fedora-rawhide`), release `rawhide`, in the `rpm` family with the Fedora 44 tools tree. Rawhide moves to the next release's signing key at each branch point; `RepositoryKeyFetch=yes` makes mkosi fetch the current one rather than trust the tools tree's copy.
  - Ubuntu 24.04 LTS (`ubuntu-noble`), release `noble`, in the `debian` family with the Fedora 44 tools tree for the Ubuntu archive keyring, as for 26.04.
- **`mount` in the Debian family.** Debian's systemd 262 only recommends `mount`, and mkosi installs no recommends, so the first Debian testing image had no `mount(8)` and every mount unit failed: `run-nsl-proc.mount`, and with it rootless Podman, as well as `dev-hugepages.mount` and `sys-fs-fuse-connections.mount`. The `debian` family now names `mount`. Debian 13 and Ubuntu 26.04 already had it through systemd, so their content does not change, but their inputs did: Debian moves to r6 and Ubuntu 26.04 to r3.
- **Selectors.** `debian:testing`, `fedora:rawhide`, and `ubuntu:noble` with `ubuntu:24.04`. The development branches get no version selector, since their version changes under them. The existing profiles stay each distribution's default release for `build-image.sh --distribution` without `--release`.
- **Done when:** the three images build, and every check of `probe-machines.py --gui --isolated` passes on them, and on the rebuilt Debian 13 and Ubuntu 26.04 images, with VM image r10.

**Result, 2026-10-10: complete.**

| Image | systemd | Size (zstd) | Entry latency, median |
| --- | --- | --- | --- |
| `nsl-machine-debian-testing-x86-64-r2` | 262-1 | 119 MB | 72 ms |
| `nsl-machine-fedora-rawhide-x86-64-r1` | 262-3.fc46 | 121 MB | 84 ms |
| `nsl-machine-ubuntu-noble-x86-64-r2` | 255.4 | 96 MB | 96 ms |
| `nsl-machine-debian-trixie-x86-64-r6` | 257.13 | 96 MB | 104 ms |
| `nsl-machine-ubuntu-resolute-x86-64-r3` | 259.5 | 109 MB | 86 ms |

- **Builds.** Every build succeeded in a new builder, in one to two minutes each. Rawhide is Fedora 46: mkosi fetched the Fedora 45, 46 and 47 keys, and the image's os-release reports `VERSION_ID=46`. Debian testing is forky with systemd 262; its descriptor reads `os_version` `rolling`. Ubuntu 24.04's systemd 255 is the oldest in the catalogue and older than the VM's 257.
- **First acceptance** (`more-releases-probe-1.json`, VM image r10 `511a0431…`, `--gui --isolated` with an isolated Ubuntu 24.04 machine): Fedora Rawhide r1 and Ubuntu 24.04 r1 passed every check, shared and isolated. Debian testing r1 failed `system`, `tally` (`session`), `podman` and `persistence`, all because `mount` was missing.
- **Second acceptance** (`more-releases-probe-2.json`, the same VM image, `--gui --isolated` with an isolated Debian testing machine): Debian testing r2, Fedora Rawhide r1, Ubuntu 24.04 r2 and Ubuntu 26.04 r3 passed all 12 checks each, and the isolated machine all 8. Debian 13 r6 failed its `packages` check because deb.debian.org served a file with a hash mismatch; its `podman`, `files`, `ports`, `gui` and `persistence` checks need those packages. A rerun minutes later (`more-releases-probe-3.json`) passed all 12 checks for Debian 13 r6 and again for Ubuntu 26.04 r3.
- **Workloads.** The packages check installed Podman 5.8.6 on Debian testing, 6.1.2 on Rawhide and 4.9.3 on Ubuntu 24.04, and galculator opened a window from each. No probe change was needed.
- **Memory.** Not measured: the gate's Debian 13 image did not change in content or size (96 MB at r5 and r6), and the new images are outside the measured four. The publication run measures again.

## Phase 2 — Publication

- Merge, then dispatch `images.yml` from `main`. The CLI needs no change: it is distribution-neutral, and selectors are data in the catalogue.
- **Done when:** a signed catalogue lists all eleven machine images, and `nsl create` works from each new selector on a host with an empty cache.

**First attempt, 2026-10-10: failed acceptance; nothing was published.** [Run 38062049493](https://github.com/frostyard/nsl/actions/runs/38062049493) built `e3dc4b7` on `nsl-builder`, and the VM probe passed. Of the machine checks, only openSUSE Tumbleweed r6's `tally` failed, on `machine_id`. Debian testing, Fedora Rawhide and Ubuntu 24.04 passed every check, shared and isolated. The failure was transient: that check also reads every other machine's live ID, and the tallies just before and after it, which read Tumbleweed's machine too, passed. A local rebuild of Tumbleweed r6 from the same commit and systemd 261.3 had `uninitialized` in its tree. The public log shows only the failure, and the evidence recorded only whether all IDs were distinct, so it could not name the machine whose read failed. The probe now records each failed read's exit status, output length and stderr, and the machines that share an ID, but never an ID itself. [Run 38072703300](https://github.com/frostyard/nsl/actions/runs/38072703300) retries the publication from the same commit.

## Later / ideas

- A development branch breaks more often than a stable release, and publication promotes a catalogue only when every image passes. Until that costs a week, a failing branch is fixed in its profile, and `operation=refresh` keeps the current catalogue alive meanwhile. If it happens often, the publisher could carry the last accepted image forward for a development branch that fails, instead of blocking the others.
- Debian 12, Fedora 43 and CentOS Stream 9, which the recipes also carry.

## Open questions

- None.

## References

- Implements: [machine images](../specs/machine-images.md), [image publication](../design/image-publication.md), [ADR-0009](../adr/0009-distribution-neutral-guest-contract.md).
- Follows: [Ubuntu, CentOS Stream and openSUSE Leap machine images](more-machine-images.md), [Azure Linux 4.0](azure-linux.md).
