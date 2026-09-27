# Plan and results: Public signed image delivery

All seven supported x86-64 profiles are published at `ghcr.io/frostyard/nsl-images`. This report records the v0.3.0 image-delivery acceptance on Snow Linux 13. The [delivery plan](image-distribution.md) and [contract](../specs/image-delivery.md) define the implementation.

## Phase 1 — Build, test, sign and publish

[Publication run 36295739774](https://github.com/frostyard/nsl/actions/runs/36295739774) completed successfully on 2026-09-27 from source `d1d85e81f9ff64a85f88d107f69073e281feaad2`. All seven profiles passed the common lifecycle, maintenance and storage suites before signing. The exact tested generic disks were compressed, signed and uploaded; test guests and private backups were excluded.

Host: Snow Linux 13 x86_64, systemd 261.2, QEMU 10.0.13 and virtiofsd 1.13.2. Inputs: nspawn recipes `68263d05169784f44168ca65241d989865ed011b`, mkosi v27 `4736cd836108a97772142c461c49f1ddb4172348`. Package inventories record resolved versions; live repositories do not imply reproducible builds.

| Build | Compressed MiB | Raw MiB | Maintenance kernel before → after |
| --- | ---: | ---: | --- |
| `nsl-debian-trixie-x86-64-v7` | 484.8 | 2942.0 | 6.12.107+deb13-amd64 → 6.12.107+deb13-amd64 |
| `nsl-ubuntu-noble-x86-64-v7` | 1328.4 | 3569.2 | 6.8.0-142-generic → 6.8.0-142-generic |
| `nsl-fedora-44-x86-64-v5` | 390.2 | 2235.3 | 7.2.7-200.fc44.x86_64 → 7.2.7-200.fc44.x86_64 |
| `nsl-centos-10-x86-64-v4` | 267.8 | 1806.5 | 6.12.0-269.el10.x86_64 → 6.12.0-269.el10.x86_64 |
| `nsl-arch-rolling-x86-64-v3` | 808.0 | 3598.0 | 7.2.6-arch2-1 → 7.2.7-arch1-1 |
| `nsl-opensuse-16.0-x86-64-v6` | 378.9 | 1924.9 | 6.12.0-160000.38-default → 6.12.0-160000.38-default |
| `nsl-opensuse-tumbleweed-x86-64-v6` | 545.3 | 2089.6 | 7.2.6-1-default → 7.2.6-1-default |

Arch exercised a newer kernel upgrade. The other six exercised reinstallation and regenerated-kernel reboots. Fedora, CentOS and both openSUSE profiles retained SELinux enforcing; Ubuntu retained AppArmor. Rootless Podman build, bind mounts, named volumes, DNS/HTTPS, localhost access and persistence passed on every profile.

The shared suite also passed argument/binary-stream/exit/PTY handling, project ownership, two-VM separation, forwarding conflict/retry/handoff, forced-exit recovery, backups/restores without a cache, an 8 GiB disk growth and removal that preserves external projects and peer VMs.

### Artifact identities

Each OCI artifact includes the signed descriptor, compressed disk, package inventory, provenance and filtered acceptance report. The descriptor binds raw/compressed hashes and all evidence hashes.

| Build | OCI manifest SHA256 | Raw SHA256 |
| --- | --- | --- |
| `nsl-debian-trixie-x86-64-v7` | `6504c6ec7c640bf826c8322495ee705fc3a7d7621e05e12a4798f48572b8653d` | `c7819bd7474bfdc13374a99692aafbf66f14ecf405a2b4cd66d370de2488aa23` |
| `nsl-ubuntu-noble-x86-64-v7` | `b996d24c9b0bb9c2399ff8e2a5591605d5c7216905cf97088a9e2139273bbace` | `a334ce88a5a669e073b9a099592c783201159601308f6305051478cb29178620` |
| `nsl-fedora-44-x86-64-v5` | `178e17d99f24e7df349f75dd19d7363b425aa12d14789e7a4427fdf353a603b7` | `649ece25880af4eb4cd02672739439aa13b726f9a5f9bc3a7d9ec31276013b66` |
| `nsl-centos-10-x86-64-v4` | `1f4f3900803af32acf56a01e0f1c16523bdbcb8868851b6f00bdc585ae1427b9` | `3dc0a6f14dd51a16ad34b411cebf42c2dde8ef28bd5995e31a047b355b14a158` |
| `nsl-arch-rolling-x86-64-v3` | `854f17904a41be05753723aaf98d8462012d0b8148c633740b2cb2b6498fdf18` | `d9564e6ae9ce6046a3de7798a4698fb560ac5cb67395d53193bd1412ddeecc78` |
| `nsl-opensuse-16.0-x86-64-v6` | `5c539f9e0404efebfe038d5d34da47879e0acf9d8bec08b235fefa106e462f9e` | `8d70fb52b1e2e1cf96360064536b99222eedfd135cb042de49c65c9910e22864` |
| `nsl-opensuse-tumbleweed-x86-64-v6` | `39e37a55b601a153aa82b90fa9fcf52d0c182543d76f0fe5dfe804e28a4380c9` | `e703dd70881d09b0b5b0bab7abcb6e54059859995ab14f32ef8fdb75892bbfc3` |

The first signed catalogue is sequence **3**, manifest `sha256:91a3d2cc2c45490d4c93d55a74df8cd7c2ae415fd195840055e50b0a81893be8`. Its expiry is 2026-10-27T05:40:46Z. The CLI minimum sequence is 3.

## Phase 2 — Anonymous delivery and real VM acceptance

**All seven passed** `scripts/probe-published-images.py` using CLI source `510d94b` and a previously unused cache. Anonymous catalogue and image verification succeeded. Two VMs per image had distinct machine IDs and SSH host keys. Every profile passed guest descriptor/UID/architecture checks, offline verification and restart persistence. All 14 temporary VMs were removed.

| Selector | Pull and verify seconds | Create from cache seconds | First boot seconds |
| --- | ---: | ---: | ---: |
| `debian:trixie` | 17.50 | 7.77 | 6.86 |
| `ubuntu:noble` | 43.81 | 14.02 | 6.62 |
| `fedora:44` | 28.87 | 9.77 | 10.41 |
| `centos:10` | 12.10 | 7.19 | 5.46 |
| `arch:rolling` | 23.15 | 5.74 | 9.72 |
| `opensuse:16.0` | 14.06 | 3.66 | 5.53 |
| `opensuse:tumbleweed` | 78.07 | 4.65 | 7.43 |

These are single trials on this host/network, not latency percentiles. Pull time includes metadata/signature checks, transfer and decompression; create time includes cache verification and independent disk creation. The run started with catalogue 3, then consumed refresh 4 during later selections. A separate empty cache also authenticated catalogue 4. Local evidence: `build/native/evidence/public-delivery.json` and `build/inspection/public-delivery.log`.

`make ci` passed for release source `54638a1`, including real Sigstore verification fixtures, malformed/tampered metadata, expiry/rollback/withdrawal, concurrent/resumed downloads, a real mid-write filesystem limit, bounded decompression, race tests and amd64/arm64 cross-compilation. [CI run](https://github.com/frostyard/nsl/actions/runs/36297965516). GoReleaser Pro configuration validation passed.

## Phase 3 — Operations and release

[Catalogue refresh run 36297987588](https://github.com/frostyard/nsl/actions/runs/36297987588) promoted sequence **4**, preserving all seven image selections and their digests. It verified the prior bundle, signed renewed metadata and advanced expiry to **2026-10-27T05:44:48Z**. The main publication rebuilt all profiles; refresh reused the tested artifacts. Withdrawal policy is covered by local tests for removal, retained revocations, unknown-digest rejection and monotonic sequence checks. No production image was withdrawn for a test.

The [publication runbook](../design/image-publication.md) requires refresh before expiry and retention of promoted digests. Temporary runners auto-unregister after their job. No host package/service installation is needed by the workflow.

Public acceptance and source CI are complete. The v0.3.0 release workflow will run CI again; archive checksums, GitHub provenance and a fresh-cache boot with the released binary are the final post-tag checks.

## Later / limits

- Other atomic hosts, host suspend/reboot and network transitions need separate sessions. This is one-host evidence.
- Newer-kernel upgrade coverage beyond Arch remains open.
- Guest integration updates, optional cloud-init, distro EOL tracking and recurring build/refresh operations remain follow-up work.
- arm64 CLI cross-compilation does not establish arm64 VM support. SLE, AlmaLinux and Rocky need independent artifacts and acceptance.

## References

- [User setup](../../README.md), [image builder](../../image/README.md), [distribution matrix](distribution-support.md).
- [Release gates](v0.2-v0.3-release.md), [main roadmap](wsl2-equivalent.md).
- [Delivery contract](../specs/image-delivery.md), [publication design](../design/image-publication.md).
