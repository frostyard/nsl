# nsl — WSL-style Linux machines for atomic Linux

`nsl` gives atomic Linux hosts persistent Linux machines, as WSL does for Windows. Machines are systemd-nspawn containers in one nsl-owned VM, launched with systemd-vmspawn and QEMU/KVM, and are trusted as your user: they see your home and removable media at `/mnt/host` ([ADR-0016](docs/adr/0016-wsl-style-machines.md), [ADR-0017](docs/adr/0017-shared-vm-and-machine-images.md)).

**Status: under construction.** The [implementation plan](docs/plans/shared-vm-implementation.md) is replacing the earlier one-VM-per-environment prototype phase by phase, and the [CLI contract](docs/specs/cli.md) describes the target. Today the binary builds and runs the nsl VM; machine commands arrive with the next phases. There are no published images for this design yet, and v0.3.0 and earlier releases are the retired prototype.

The tested host is **Snow Linux 13, x86_64, systemd 261.2**, QEMU 10.0.13, virtiofsd 1.13.2 and GNOME Wayland.

## Prerequisites

- systemd-vmspawn, a user systemd manager, systemd-ssh-proxy, QEMU/KVM, UEFI firmware, virtiofsd, OpenSSH, `sg` and util-linux `unshare`.
- Existing membership in `kvm`, with access to `/dev/kvm` and `/dev/vhost-vsock`; unprivileged user namespaces must work.
- Go 1.25.8+ to build nsl; Lima 2.2.0, Git and Python 3 to build images. Lima is used only by the image builder.

nsl does not install host packages or change device permissions, groups or sudoers. `nsl doctor` checks the prerequisites.

## Build and run the VM

```sh
make build
./scripts/bootstrap-poc.sh --waypipe   # optional: pinned Lima and Waypipe under build/poc
source build/poc/env.sh
build/nsl doctor

# Build the VM image inside a disposable Lima VM; dependencies stay inside it.
scripts/build-image.sh --role vm
image=build/image/share/nsl-vm-trixie-x86-64-r2.raw
build/nsl update --image "$image" --digest "sha256:$(sha256sum "$image" | cut -d' ' -f1)"
build/nsl recover     # start the VM from a fresh root
build/nsl list
build/nsl shutdown
```

`update` verifies and caches the image, then selects it for the VM's next start. The VM's root is replaceable and holds no user state; machines and the VM's identity live on its data disk. See the [image build](image/README.md).

| Command | Behavior |
| --- | --- |
| `list` | The VM's state, image, resources and data disk, and anything pending until its next start. |
| `update --image FILE --digest sha256:HEX` | Select a local VM image for the next start. |
| `recover` | Restart the VM from a fresh root, check its data disk and finish interrupted growth. |
| `resize --disk GiB` | Grow the stopped VM's data disk; it never shrinks. |
| `shutdown` | Stop the VM. |
| `config` | Show the effective configuration and its sources. |
| `doctor`, `version`, `help` | Host checks, build version and usage. |

## Configuration

Settings live in `~/.config/nsl/nsl.conf` (or under `$XDG_CONFIG_HOME`), separate from state in `NSL_HOME` (default `~/.local/share/nsl`). nsl reads the file and never writes it; an absent file means defaults.

```ini
[vm]
# GiB; the default is half the host's memory.
memory = 16
# The default is every host CPU.
cpus = 8

[machines]
autostart = true
# Minutes without sessions before a machine stops; 0 disables.
idle_timeout = 15
```

Comments take whole lines. Resource changes apply at the VM's next start, and `nsl config` and `nsl list` show them as pending. The [CLI contract](docs/specs/cli.md#configuration) has every key and rule.

## Current limits

- Machines, commands, files, ports and GUI are not wired into the CLI yet; see the plan's phases.
- Host file changes through virtiofs do not produce inotify events in the VM. Watched builds belong in machine storage.
- The whole home is visible to the VM, including nsl state and keys; this matches the trust model.

## Validate

```sh
make ci
python3 scripts/probe-vm.py --nsl build/nsl --image "$image" --evidence build/image/evidence/probe.json
```

Unit tests use fake tools and local processes and need neither root nor a VM. The probe boots disposable VMs in private state directories and removes them.

[Third-party license notices](THIRD_PARTY_NOTICES.txt) · [Documentation index](docs/README.md) · [CLI contract](docs/specs/cli.md) · [Architecture](docs/design/lifecycle.md)
