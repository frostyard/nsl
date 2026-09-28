# nsl images

nsl builds its images from pinned [nspawn/mkosi-definitions](https://github.com/nspawn/mkosi-definitions) recipes with mkosi v27, inside a disposable Lima builder. There are two kinds ([ADR-0017](../docs/adr/0017-shared-vm-and-machine-images.md)):

- **The nsl VM image** (`vm/`): the Debian trixie disk that every nsl VM boots. It hosts machines as systemd-nspawn containers and runs the nsl agent. Contract: [VM image](../docs/specs/vm-image.md).
- **Machine images** (`machines/`): distro root filesystems that run as machines: Debian 13, Fedora 44, Arch and openSUSE Tumbleweed. Contract: [machine images](../docs/specs/machine-images.md).

## Build the VM image

```sh
source build/poc/env.sh  # NSL_LIMACTL, when using the locally downloaded Lima
scripts/build-image.sh --role vm
python3 scripts/probe-vm.py --nsl build/nsl --image build/image/share/nsl-vm-trixie-x86-64-r6.raw \
  --evidence build/image/evidence/nsl-vm-trixie-x86-64-r6-probe.json
```

The script builds the agent (`make agent`), composes the build input with `scripts/compose-image.py`, and builds `nsl-vm-trixie-x86-64-rN.raw` under `build/image/share/`. Next to it are the mkosi package manifest (`.manifest`) and the descriptor as built (`.json`), and the raw SHA256 is in `build/image/evidence/`. Bump `vm/profile.json`'s revision when inputs change; the script refuses to overwrite an existing artifact.

The builder needs Lima 2.2.0, Python 3, Git, Go and `flock`. It lives in `${XDG_DATA_HOME:-~/.local/share}/nsl-image-build` (override with `NSL_IMAGE_LIMA_HOME`), outside the checkout, because Lima's socket paths must stay under 108 bytes. It has 4 CPUs, 4 GiB of memory and a 64 GiB sparse disk, shares only `build/image/share`, and stops when the build ends. Build packages stay inside it.

To run a local build, select it and start the VM:

```sh
nsl update --image build/image/share/nsl-vm-trixie-x86-64-r6.raw --digest sha256:HEX
nsl recover
```

Live package repositories mean builds are not bit-for-bit reproducible. Checksums and manifests do not authenticate a publisher; published images are authenticated through the [signed delivery contract](../docs/specs/image-delivery.md).

## The VM layer

`vm/` holds the mkosi configuration, the post-install and finalize scripts and an overlay:

- **Data disk:** `nsl-data.service` runs `nsl-agent storage`. It formats a blank second disk as btrfs `nsl-data` with `machines` and `state` subvolumes, and refuses any disk with another signature. `var-lib-machines.mount` and `var-lib-nsl.mount` mount the subvolumes, and `nsl-grow.service` grows the filesystem to the disk at every boot.
- **Identity:** `nsl-setup.service` runs `nsl-agent setup` with the `nsl.vm` credential. It binds the VM on first boot and checks the binding later, keeps the SSH host key on the state subvolume, installs the host's key forced to the agent, and creates the `/mnt/host` alias symlinks. A refused disk or a mismatched credential powers the VM off at once.
- **Machines:** `nsl-machines.service` runs `nsl-agent boot`. It writes each machine's nspawn settings into `/run/systemd/nspawn` and starts every machine when autostart is on.
- **Idle stop:** `nsl-idle.service` runs `nsl-agent idle`. Every 10 s it powers off a machine idle for `idle_timeout`, and powers the VM off a minute after its last machine stops and its last request ends.
- **Transport:** `nsl-ssh.socket` listens on vsock port 22, with an inetd-style `nsl-ssh@.service`. The systemd SSH generator is masked, and sshd accepts only root with the forced agent command.
- **Boot:** the ESP mounts at `/efi` and `/boot` stays on the root filesystem ([ADR-0007](../docs/adr/0007-maintainable-guest-boot.md)). The root partition grows to the host's 16 GiB overlay through `systemd-repart`. The initramfs loads the vsock driver.

The composer copies the agent to `/usr/lib/nsl/nsl-agent` and writes `/usr/lib/nsl/image.json`. That descriptor's `integration_sha256` covers the composer, the layer and the agent binary, and the finalize script records the installed systemd and kernel versions. [ADR-0011](../docs/adr/0011-image-profiles-and-portable-vsock.md).

## Build machine images

```sh
scripts/build-image.sh --role machine --distribution debian     # also fedora, arch, opensuse
python3 scripts/probe-machines.py --nsl build/nsl --vm-image build/image/share/nsl-vm-trixie-x86-64-r6.raw \
  --machine-image build/image/share/nsl-machine-debian-trixie-x86-64-r2.tar.zst \
  --evidence build/image/evidence/machines-probe.json [--gui]
```

A machine build uses the recipe's container output without the disk profile, as a zstd tar: `nsl-machine-DISTRIBUTION-RELEASE-x86-64-rN.tar.zst`, with its manifest and the descriptor as built. Debian builds with the builder's own tools. Fedora and Arch use a Fedora 44 tools tree, and Tumbleweed an openSUSE one; the builder keeps mkosi's cache in `/var/cache/nsl-mkosi` between builds. Bump the profile's revision in `machines/profiles/NAME/profile.json` when inputs change.

`machines/` composes three layers:

1. `common/`: the machine layer. It holds the `nsl` PAM service, the nesting mount and its preset, the Podman drop-in and `nsl-path`. Its finalize script, which runs after the recipes' own scripts, masks networkd and resolved and disables SSH services. It also removes SSH host keys and the random seed, sets the machine ID to `uninitialized`, and fills in the descriptor.
2. `families/FAMILY/`: packages, the tools tree and family fixes, such as Arch's keyring deletion and first-boot `nsl-pacman-keyring.service`.
3. `profiles/NAME/`: the distribution, release and revision.

Create a machine from a local build with `nsl create NAME --image FILE --digest sha256:HEX`. `probe-machines.py` runs the agent's entry matrix, the hub-image tally checks and the workload checks through the CLI.

## Publication

The [publication design](../docs/design/image-publication.md) and `scripts/publish-images.py` still describe the retired per-distro disks. Phase 10 of the implementation plan rewrites them for the VM image and machine images, and until then publication is not runnable.
