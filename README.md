# nsl — WSL-style Linux machines for atomic Linux

`nsl` gives atomic Linux hosts persistent Linux machines, as WSL does for Windows. Machines are systemd-nspawn containers in one nsl-owned VM, launched with systemd-vmspawn and QEMU/KVM, and are trusted as your user: they see your home and removable media at `/mnt/host` ([ADR-0016](docs/adr/0016-wsl-style-machines.md), [ADR-0017](docs/adr/0017-shared-vm-and-machine-images.md)).

**Status: under construction.** The [implementation plan](docs/plans/shared-vm-implementation.md) is replacing the earlier one-VM-per-environment prototype phase by phase, and the [CLI contract](docs/specs/cli.md) describes the target. Today the binary runs the nsl VM and machines created from locally built images; publication, ports, GUI and export arrive with later phases. There are no published images for this design yet, and v0.3.0 and earlier releases are the retired prototype.

The tested host is **Snow Linux 13, x86_64, systemd 261.2**, QEMU 10.0.13, virtiofsd 1.13.2 and GNOME Wayland.

## Prerequisites

- systemd-vmspawn, a user systemd manager, systemd-ssh-proxy, QEMU/KVM, UEFI firmware, virtiofsd, OpenSSH, `sg` and util-linux `unshare`.
- Existing membership in `kvm`, with access to `/dev/kvm` and `/dev/vhost-vsock`; unprivileged user namespaces must work.
- Go 1.25.8+ to build nsl; Lima 2.2.0, Git and Python 3 to build images. Lima is used only by the image builder.

nsl does not install host packages or change device permissions, groups or sudoers. `nsl doctor` checks the prerequisites.

## Build and run

```sh
make build
./scripts/bootstrap-poc.sh --waypipe   # optional: pinned Lima and Waypipe under build/poc
source build/poc/env.sh
build/nsl doctor

# Build the VM image inside a disposable Lima VM; dependencies stay inside it.
scripts/build-image.sh --role vm
image=build/image/share/nsl-vm-trixie-x86-64-r5.raw
build/nsl update --image "$image" --digest "sha256:$(sha256sum "$image" | cut -d' ' -f1)"
build/nsl recover     # start the VM from a fresh root

# Build a machine image and create a machine from it; the first becomes the default.
scripts/build-image.sh --role machine --distribution debian
machine=build/image/share/nsl-machine-debian-trixie-x86-64-r2.tar.zst
build/nsl create debian --image "$machine" --digest "sha256:$(sha256sum "$machine" | cut -d' ' -f1)"
build/nsl             # a login shell in the default machine, in this directory
build/nsl run -m debian sudo apt-get install -y podman
build/nsl list
build/nsl shutdown
```

Machines are Debian 13, Fedora 44, Arch or openSUSE Tumbleweed (`--distribution debian|fedora|arch|opensuse`). Your account has your username, UID and GID, a home at `/home/USER` in the machine, and passwordless `sudo`. Your home, `/run/media/USER` and `/mnt` appear read-write at `/mnt/host` plus their host paths, and commands start in the matching directory. Each machine is its own distro with its own packages and services, all in one VM.

`update` verifies and caches the image, then selects it for the VM's next start. The VM's root is replaceable and holds no user state; machines and the VM's identity live on its data disk. See the [image build](image/README.md).

| Command | Behavior |
| --- | --- |
| `[-m NAME]` | Login shell in NAME or the default machine, in the translated current directory or the home. |
| `run [-m NAME] [--root] [--cd PATH] COMMAND [ARGS...]` | Run argv literally in the machine; exit status, streams and signals pass through. |
| `create NAME --image FILE --digest sha256:HEX [--default] [--user NAME]` | Create a machine from a local machine image, offline. |
| `start NAME`, `stop NAME`, `default NAME` | Start or stop a machine, or make it the default. |
| `remove NAME [--yes]` | Preview, then permanently remove a stopped machine. |
| `list` | The VM's state, image, resources and data disk, anything pending until its next start, and every machine. |
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

- Port forwarding, GUI sessions, `nsl-open`, export and import, isolated machines and idle stop are not wired into the CLI yet; see the plan's phases. A machine that is running keeps running until `stop` or `shutdown`.
- Host file changes through virtiofs do not produce inotify events in the VM. Watched builds belong in machine storage.
- The whole home is visible to the VM, including nsl state and keys; this matches the trust model.

## Validate

```sh
make ci
python3 scripts/probe-vm.py --nsl build/nsl --image "$image" --evidence build/image/evidence/probe.json
python3 scripts/probe-machines.py --nsl build/nsl --vm-image "$image" --machine-image "$machine" \
  --evidence build/image/evidence/machines.json
```

Unit tests use fake tools and local processes and need neither root nor a VM. The probes boot disposable VMs in private state directories and remove them.

[Third-party license notices](THIRD_PARTY_NOTICES.txt) · [Documentation index](docs/README.md) · [CLI contract](docs/specs/cli.md) · [Architecture](docs/design/lifecycle.md)
