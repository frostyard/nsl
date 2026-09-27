# nspawn image + vmspawn comparison

Historical comparison code. The main CLI now uses vmspawn; see [the current image build](../../image/README.md) and [implementation report](../../docs/plans/vmspawn-implementation.md). The builder and memory comparison here require the saved Lima binary at `build/nsl-lima` (or `NSL_LIMA_BASELINE`), which is a local artifact and is not shipped.

This was a bounded experiment alongside the Lima-backed CLI. This adapter supports one disposable environment, named `probe`, on the tested Snow host with UID/GID 1000. It does not replace nsl's runtime.

See the [comparison report](../../docs/plans/vmspawn-comparison.md) for measured results and limitations.

## Components

- `build.sh`: builds a bootable Debian disk inside a disposable Lima builder VM.
- `mkosi.local.conf`, `nsl-postinst.chroot`, `overlay/`: nsl additions to the upstream recipes.
- `driver.py`: lifecycle, vsock SSH, guest commands, Waypipe and localhost forwarding.
- `fd-launch.py`: opens KVM/vsock devices under the user's existing `kvm` membership, then restores the normal primary group before launching vmspawn.
- `measure-memory.py`: three identical 512 MiB guest allocations on the existing Lima `final` and vmspawn `probe` environments.

Sources are downloaded to ignored `build/vmspawn/src`:

| Source | Revision |
| --- | --- |
| nspawn/mkosi-definitions | `68263d05169784f44168ca65241d989865ed011b` |
| systemd/mkosi v27 | `4736cd836108a97772142c461c49f1ddb4172348` |

The recipe checkout supplies the Debian package set, disk profile, initrd/kernel/bootloader settings and GPT/btrfs layout. The overlay adds the development user, Python command helper, sudo, CA certificates, Waypipe/galculator and Ethernet DHCP. It locks the default root password and removes build-time host keys. Debian's systemd generates unique host keys and the vsock SSH listener at first boot. The test's public client key is embedded in the guest; its private key stays on the host. This is an experiment image, not a distributable image catalogue.

The upstream Fedora tools tree is disabled for this experiment; tools come from the disposable Debian builder instead. Packages are fetched from signed Debian repositories. Source revisions are pinned, but package repositories are rolling and builds are not claimed to be bit-for-bit reproducible. No nspawn source is copied into the tracked overlay.

## Requirements

The tested host has systemd-vmspawn/systemd-ssh-proxy 261.2, QEMU 10.0.13, virtiofsd 1.13.2, OpenSSH, `sg`, and util-linux `unshare`. The normal user belongs to `kvm`. Rootless user namespaces must work. The builder uses the existing nsl/Lima tool setup described in the [main README](../../README.md). Waypipe is required for the GUI check.

No host sudo, package installation, sudoers edit or device-permission change is performed. Dependencies needed to build the image are installed only in the builder VM.

## Build

```sh
source build/poc/env.sh
experiments/vmspawn/build.sh
```

The script creates a builder under `~/.local/share/nsl-vmspawn-build`, and refuses to overwrite an existing output image. It uses 4 CPUs and 4 GiB RAM for the builder. The resulting artifact is `build/vmspawn/share/nsl-debian-vm-v3.raw`, with a checksum under `build/vmspawn/evidence`.

The script leaves the builder running for inspection. Stop it explicitly when finished:

```sh
NSL_HOME="$HOME/.local/share/nsl-vmspawn-build" build/nsl-lima stop builder
```

## Initialize and exercise

```sh
mkdir -p build/vmspawn/project
experiments/vmspawn/driver.py init \
  build/vmspawn/share/nsl-debian-vm-v3.raw \
  build/vmspawn/project build/vmspawn/identity/id_ed25519
experiments/vmspawn/driver.py exec probe -- id
experiments/vmspawn/driver.py exec probe --workdir /work -- pwd
experiments/vmspawn/driver.py gui probe -- galculator
experiments/vmspawn/driver.py stop probe
```

Initialization refuses an existing state directory and creates a qcow2 overlay over the immutable raw image. State defaults to `~/.local/share/nsl-vmspawn-eval`; `NSL_VMSPAWN_STATE` overrides it. The raw base must remain at the same path. Do not run several instances of this adapter: its unit names and CID 19876 are fixed.

If state already exists from this experiment, use exec/start/gui directly rather than initializing again. The host project is shared at `/work`; guest home persists in the overlay. `--root` affects only guest commands. A `--tty` command supports interactive applications.

### Why the launcher has a device-descriptor helper

On the tested systemd version, the rootless bind path failed in `namespace_enter` without capabilities in a user namespace. The experiment launches vmspawn in a namespace mapping the caller's UID/GID to themselves, retaining capabilities only within that namespace. Running that namespace with primary group `kvm` caused guest GID 1000 operations to fail.

`fd-launch.py` opens the two devices while in `kvm`, restores the account's primary group through `sg`, and supplies the file descriptors through vmspawn's documented `LISTEN_FDS`/`LISTEN_FDNAMES` interface. The process retains its normal host UID; it gains no host-root capabilities. Both read/write file ownership checks then pass.

The `rw` kernel argument is required for this image: vmspawn mounts shares in the initrd, while mkosi removes `/work` during image cleanup. A writable root lets the mount unit recreate that path. Booting without it can fail before first-boot SSH key generation, leaving an incomplete overlay. Recovery from that state is not automated.

### Localhost forwarding

The adapter polls `ss` in the guest once per second and uses SSH multiplexing to add/remove host-loopback forwards. It excludes privileged ports and discovery ports. It handles the tested IPv4 TCP service; IPv6-only listeners, UDP and production-grade discovery/conflict reporting are not implemented. The forwarding service is additional code nsl would need to maintain on a vmspawn route.

## Measure

```sh
python3 scripts/measure-poc.py --nsl experiments/vmspawn/driver.py \
  --environment probe --project "$PWD/build/vmspawn/project" \
  --cold-trials 20 --warm-trials 50 \
  --output build/vmspawn/evidence/measurement.json
python3 scripts/probe-files.py --nsl experiments/vmspawn/driver.py \
  --environment probe --project "$PWD/build/vmspawn/project" \
  --output build/vmspawn/evidence/files.json
python3 experiments/vmspawn/measure-memory.py
```

The main harness and memory comparison stop their VMs. The file probe leaves its VM running. Inspect logs with `journalctl --user -u nsl-vmspawn-experiment.service` and `journalctl --user -u nsl-vmspawn-ports.service`. `NSL_VMSPAWN_DEBUG=1` forwards guest journal messages to the console; `NSL_VMSPAWN_NO_SHARE=1` is only a diagnostic boot mode.

## Deliberate limits

This is one-host evaluation code. It lacks multi-environment management, backups, recovery, idle shutdown, updates, signed image delivery and general distro/UID support. Host SSH trust is first-use pinning. Like the Lima prototype, sharing a project grants the guest access to its contents; root inside the guest can modify it. The host device-descriptor helper and rootless namespace launch need validation on other supported distributions before promotion into nsl.
