# nsl — development VMs for atomic Linux

`nsl` manages persistent Linux development VMs with terminal, project-file, localhost and Wayland integration. Each environment runs its distribution and applications directly inside one VM. The Go CLI uses systemd-vmspawn and QEMU/KVM, with a Debian image built from nspawn's mkosi recipes.

This is a development prototype. The tested host is **Snow Linux 13, x86_64, systemd 261.2**, QEMU 10.0.13, virtiofsd 1.13.2 and GNOME Wayland. Other atomic distributions remain to be tested. [Implementation and validation](docs/plans/vmspawn-implementation.md).

## Prerequisites

- systemd-vmspawn, a user systemd manager, systemd-ssh-proxy, QEMU/KVM, UEFI firmware, virtiofsd, OpenSSH, `sg` and util-linux `unshare`.
- Existing membership in `kvm`, with access to `/dev/kvm` and `/dev/vhost-vsock`; unprivileged user namespaces must work.
- Go 1.23+ to build the CLI. Waypipe and a Wayland session for GUI applications.
- Lima 2.2.0, Git and Python 3 to build the guest image locally. Lima is used only by the image builder.

nsl does not install host packages or change device permissions, groups or sudoers. `doctor` checks the local prerequisites. arm64 cross-compiles; creating arm64 guests is not implemented.

## Build and create

```sh
make build
# Optional: download pinned Lima and extract Waypipe locally for development.
./scripts/bootstrap-poc.sh --waypipe
source build/poc/env.sh
build/nsl doctor

# Build once inside a disposable VM; dependencies stay inside that VM.
scripts/build-image.sh
image_sha=$(sha256sum build/image/share/nsl-debian-v4.raw)
build/nsl create dev \
  --image "$PWD/build/image/share/nsl-debian-v4.raw" \
  --digest "sha256:${image_sha%% *}" \
  --project "$PWD" --desktop --cpus 2 --memory 2 --disk 16
build/nsl shell dev
```

The image builder refuses to overwrite an existing output. See [image build details](image/README.md). `create` verifies the image and prepares an independent disk and keypair. First entry boots and configures the guest; later commands start a stopped VM automatically.

The guest user is `nsl`, with your numeric UID/primary GID, persistent `/home/nsl`, and guest sudo. The selected host project is at `/work`. It is fixed at creation; no host home, agent, D-Bus or GPU socket is implicitly shared. Omitting `--project` creates a VM with no host project share.

## Use

```sh
build/nsl exec dev --workdir /work -- git status
build/nsl exec dev --root -- apt-get update
build/nsl exec dev --root -- apt-get install -y golang-go
build/nsl gui dev -- galculator
build/nsl ports dev
build/nsl list
build/nsl stop dev
```

`exec --tty` supports interactive commands. Guest arguments retain their original boundaries, streams and exit status. GUI sessions use software-rendered Waypipe and stay attached until the app exits. Exiting a shell leaves the VM running; `stop` preserves its disk and project definition.

Guest IPv4 TCP listeners on ports 1024–65535 appear on host `127.0.0.1` at the same port, excluding 5353/5355. Discovery polls once per second. `ports` reports conflicts and retries when a port becomes free. Existing listeners are never displaced. IPv6-only listeners and UDP are not supported.

For remote editor access, `build/nsl ssh-config dev` prints the SSH configuration path; the alias is `guest`. The guest host key is trusted on first use and pinned for later connections.

## State and recovery

State defaults to `$XDG_DATA_HOME/nsl` or `~/.local/share/nsl`. `NSL_HOME` selects a separate directory; `NSL_WAYPIPE` overrides the GUI tool. Fresh environments have independent disks and client keys. Every environment gets its own systemd units and vsock address; restored copies retain the backup's guest authentication identity. Older prototype metadata is rejected; no migration or adoption is performed.

```sh
build/nsl logs dev
build/nsl recover dev
```

`recover` restarts the environment and resumes interrupted preparation while preserving an existing disk and its keys. It checks qcow2 metadata without automatic repair. Lost keys, filesystem corruption and directories without valid metadata require manual diagnosis. Safe deletion and resizing existing disks remain planned.

## Back up and restore

```sh
build/nsl stop dev
build/nsl export dev "$HOME/dev-backup.nsl"
build/nsl restore recovered "$HOME/dev-backup.nsl"
build/nsl shell recovered
# To reattach a host project or enable GUI, select those at restore time:
# build/nsl restore another "$HOME/dev-backup.nsl" --project "$PWD" --desktop
```

Export requires a stopped VM, temporary disk space, and a destination filesystem with hard-link support for atomic publication. It never replaces an existing backup. The archive includes the full guest disk, packages, home, settings and SSH credentials. Protect it as private data: it is not encrypted. Shared host project contents are **not included** and need their own backup.

Restore checks the archive and creates a new independent disk without the original image cache. It requires x86_64 and the same numeric UID/primary GID. It preserves the guest machine/SSH identity, while allocating new VM units and addresses. Source and restored VMs can run together, but services with their own machine identity may need application-specific changes. Host shares and desktop access require explicit restore flags. Only restore archives from a trusted source; checksums detect damage, not publisher identity.

## Roadmap

[Backup/restore and guest maintenance checks passed](docs/plans/backup-and-reliability.md), including rootless Podman and kernel reinstallation. Next are safe removal/disk growth, broader reliability tests, defaults/cwd/editor conveniences, signed prebuilt images and desktop integration. See the [prioritized roadmap](docs/plans/wsl2-equivalent.md) for current evidence and release gates.

## Existing prototype VMs

Use image v4 for new environments. Earlier v3 guests have a FAT `/boot` layout that fails Debian kernel reinstalls; the failed operation can remove their boot entry. Updating nsl does not change existing guest disks. Keep a stopped backup and use a fresh v4 environment for kernel maintenance until a tested migration is available. [Image details](image/README.md).

## Current limits

- Host file changes through virtiofs do not produce reliable guest inotify events. Use polling for live reload, or keep source in the guest home and use a remote editor.
- Clipboard, audio, accelerated graphics, portals and application launcher export are unfinished. One working Wayland application is not full desktop integration.
- Host suspend/reboot, upgrades to a newer kernel, alternate distributions and signed image delivery remain release gates. Kernel reinstallation and rootless Podman passed on image v4.
- Writable shares are accessible to guest processes, including guest root. `--root` is a convenience for administration inside the VM.

## Validate

```sh
make ci
python3 scripts/measure-poc.py --nsl build/nsl \
  --environment dev --project "$PWD" \
  --warm-trials 50 --cold-trials 20 \
  --output build/native/evidence/measurement.json
```

The measurement harness writes uniquely named test files, starts a temporary HTTP server, briefly opens a calculator and cycles the VM. It leaves the measured VM stopped. Unit tests use fake tools and local helper processes and need neither root nor a VM. Additional recovery and multi-VM checks are described in the [implementation report](docs/plans/vmspawn-implementation.md).

[Documentation index](docs/README.md) · [CLI contract](docs/specs/cli.md) · [Architecture](docs/design/lifecycle.md) · [Earlier runtime comparison](docs/plans/vmspawn-comparison.md)
