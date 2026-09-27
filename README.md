# nsl — development VMs for atomic Linux

`nsl` manages persistent Linux development VMs with terminal, project-file, localhost and Wayland integration. Each environment runs its distribution and applications directly inside one VM. The Go CLI uses systemd-vmspawn and QEMU/KVM, with Debian, Ubuntu, Fedora, CentOS Stream, openSUSE Leap/Tumbleweed and Arch images built from nspawn's mkosi recipes.

This is a development prototype. The tested host is **Snow Linux 13, x86_64, systemd 261.2**, QEMU 10.0.13, virtiofsd 1.13.2 and GNOME Wayland. Other atomic distributions remain to be tested. [Implementation and validation](docs/plans/vmspawn-implementation.md).

## Prerequisites

- systemd-vmspawn, a user systemd manager, systemd-ssh-proxy, QEMU/KVM, UEFI firmware, virtiofsd, OpenSSH, `sg` and util-linux `unshare`.
- Existing membership in `kvm`, with access to `/dev/kvm` and `/dev/vhost-vsock`; unprivileged user namespaces must work.
- Go 1.25.8+ to build the CLI. Waypipe and a Wayland session for GUI applications.
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
scripts/build-image.sh --distribution debian
# Ubuntu alternative: scripts/build-image.sh --distribution ubuntu --release noble
image_sha=$(sha256sum build/image/share/nsl-debian-trixie-x86-64-v7.raw)
build/nsl create dev \
  --image "$PWD/build/image/share/nsl-debian-trixie-x86-64-v7.raw" \
  --digest "sha256:${image_sha%% *}" \
  --project "$PWD" --desktop --cpus 2 --memory 2 --disk 16
build/nsl shell dev
```

The image builder refuses to overwrite an existing output. See [image build details](image/README.md). `create` verifies the image and prepares an independent disk and keypair. First entry boots and configures the guest; later commands start a stopped VM automatically.

The guest user is `nsl`, with your numeric UID/primary GID, persistent `/home/nsl`, and guest sudo. The selected host project is at `/work`. It is fixed at creation; no host home, agent, D-Bus or GPU socket is implicitly shared. Omitting `--project` creates a VM with no host project share.

## Prebuilt images (in development)

The development CLI implements signed catalogue selection. Public GHCR image
publication is still pending; use the local build above until it is available.
The planned download path requires the host VM prerequisites, but no image builder:

```sh
build/nsl images
build/nsl pull debian:trixie
build/nsl create dev --distro debian:trixie --project "$PWD"
```

Use the selectors advertised by `images`. `pull` verifies and caches a base without
creating a VM. Interrupted transfers resume on retry. `--offline` on `images`,
`pull` or `create --distro` requires a still-valid signed catalogue and the complete
verified cache. Online errors never silently select cached metadata. The optional
`DISTRO:RELEASE@sha256:HEX` suffix pins the catalogue's current OCI manifest.

Catalogue changes affect new creations; existing VMs keep their own disks.
Local `--image`/`--digest` checks bytes without authenticating a publisher.
[Trust policy and limits](docs/specs/image-delivery.md).

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

`recover` restarts the environment and resumes interrupted preparation while preserving an existing disk and its keys. It checks qcow2 metadata without automatic repair. Lost keys, filesystem corruption and directories without valid metadata require manual diagnosis. It also completes pending disk growth. `list` reports interrupted storage work as `Resizing` or `Removing`.

## Grow or remove an environment

```sh
build/nsl stop dev
build/nsl resize dev --disk 24
build/nsl exec dev -- df -h /
# Preview deletion, then explicitly remove the stopped VM:
build/nsl stop dev
build/nsl remove dev
build/nsl remove dev --yes
```

Resize grows virtual capacity only; the image grows its root filesystem on the next boot. Use a current validated profile image for this workflow; maintained v4 guests can require a guest integration update. Shrinking is refused. If growth is interrupted, repeat the same resize command or use `recover`. Take a stopped backup before changing important storage.

Removal permanently deletes the guest disk, credentials and configuration. It preserves external host projects, cached images and exported backups. A running VM must be stopped explicitly. Interrupted deletion reserves the name and resumes with `remove NAME --yes`.

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

[Backup/restore and guest maintenance checks passed](docs/plans/backup-and-reliability.md), including rootless Podman and kernel reinstallation. [Safe removal and disk growth](docs/plans/storage-management.md) are implemented. [Image profiles and Ubuntu 24.04 LTS](docs/plans/image-profiles-and-ubuntu.md) now add a second distro through common integration and explicit boot/package adapters. [Fedora 44 and CentOS Stream 10](docs/plans/rpm-guests.md) also pass the full suite with SELinux enforcing. [openSUSE Leap/Tumbleweed and Arch](docs/plans/suse-and-arch.md) also pass the full suite; Arch passed an actual kernel-version upgrade. SUSE Linux Enterprise needs separate source/entitlement research. [Distribution plan and support matrix](docs/plans/distribution-support.md). Broader reliability, defaults/cwd/editor conveniences, signed images and desktop integration follow. See the [prioritized roadmap](docs/plans/wsl2-equivalent.md) for current evidence and release gates.

## Existing prototype VMs

Use the current validated image for each profile: Debian/Ubuntu v6, Fedora v4, CentOS Stream v3, both openSUSE profiles v5 and Arch v2. Revisions are per profile. All use explicit root growth and an nsl-owned vsock SSH service. Earlier v3 guests have a FAT `/boot` layout that fails Debian kernel reinstalls; the failed operation can remove their boot entry. Updating nsl does not change existing guest disks. Keep a stopped backup and use a fresh validated profile for kernel maintenance until a tested migration is available. [Image details](image/README.md).

## Current limits

- Host file changes through virtiofs do not produce reliable guest inotify events. Use polling for live reload, or keep source in the guest home and use a remote editor.
- Clipboard, audio, accelerated graphics, portals and application launcher export are unfinished. One working Wayland application is not full desktop integration.
- Host suspend/reboot, newer-kernel upgrades beyond Arch and signed image delivery remain release gates. All seven profiles passed kernel reinstallation, reboot and rootless Podman checks.
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

[Third-party license notices](THIRD_PARTY_NOTICES.txt) · [Documentation index](docs/README.md) · [CLI contract](docs/specs/cli.md) · [Architecture](docs/design/lifecycle.md) · [Earlier runtime comparison](docs/plans/vmspawn-comparison.md)
