# Spec: Machine images

Contract for Frostyard machine images under [ADR-0017](../adr/0017-shared-vm-and-machine-images.md) and [ADR-0009](../adr/0009-distribution-neutral-guest-contract.md): distro root filesystems that run as systemd-nspawn machines in the [nsl VM](vm-image.md). Consumers: the machine image build in `image/machines/`, `scripts/probe-machines.py`, and the agent's `create` operation.

The host and the agent stay distribution-neutral. Everything distro-specific is built into the image, so creation applies only per-machine data and needs no network.

## Interface

### Artifact

A machine image is one root filesystem tree, packaged as `rootfs.tar.zst` in a signed artifact under the [delivery contract](image-delivery.md#artifacts). It is built from the pinned `nspawn/mkosi-definitions` recipes without the disk profile, as mkosi's zstd tar output, plus the nsl machine layer and one family adapter. It contains no kernel, bootloader, initramfs or partition table. Local builds are named `nsl-machine-DISTRIBUTION-RELEASE-x86-64-rN.tar.zst`.

### Initial catalogue

| Selectors | Family | Adapter |
| --- | --- | --- |
| `debian:trixie`, `debian:13` | `debian` | `libpam-systemd`, `dbus-user-session`, `tzdata`; native Debian build tools. |
| `fedora:44` | `rpm` | `systemd-pam`, `shadow-utils`, `tzdata`; Fedora 44 tools tree. |
| `arch:rolling` | `arch` | No `/etc/pacman.d/gnupg` in the image: the recipe's keyring, with its master private key, is deleted after the recipe's own scripts run. `nsl-pacman-keyring.service` runs `pacman-key --init` and `--populate` on first boot. Fedora 44 tools tree. |
| `opensuse:tumbleweed`, `opensuse-tumbleweed:rolling` | `suse` | `shadow`, `timezone`; openSUSE Tumbleweed tools tree. |

Each adapter also names its family's `sudo`, CA certificates, OpenSSH server, DejaVu font, Adwaita cursors and Wayland client libraries.

Other distros join after they pass acceptance.

### Machine layer

Every image supplies:

- **Accounts:** root locked with no usable password; shadow tools (`useradd`, `groupadd`, `usermod`); `sudo`, reading `/etc/sudoers.d`.
- **Identity:** `/etc/machine-id` absent, empty or `uninitialized`, so each machine gets its own on first boot. No SSH host key or package-keyring private key.
- **Sessions:** a PAM service `/etc/pam.d/nsl` that runs `pam_systemd`. It uses only modules every family ships (`pam_rootok`, `pam_unix`, `pam_keyinit`, `pam_limits`, `pam_env`, `pam_systemd`), so one file serves them all. It gives an nsl command a logind session, `XDG_RUNTIME_DIR` and a user manager. A system D-Bus and a user D-Bus session.
- **Network:** `systemd-networkd` and `systemd-resolved` masked, with their sockets. Machines use the VM's network namespace and its resolver. Masking survives the first boot's presets; the recipes enable both.
- **Nesting:** `run-nsl-proc.mount`, a fully visible procfs at `/run/nsl/proc`, and `/etc/containers/containers.conf.d/50-nsl-nspawn.conf` with `keyring = false` and `default_sysctls = []`.
- **Presets:** a preset for every integration unit. Images apply presets on first boot, and a distro's disable-all preset would otherwise undo an enable.
- **Desktop:** zone data, a font, a cursor theme and the Wayland client libraries, for the `gui` capability.
- **Guest commands:** `nsl-path`; `nsl-open`, set as `BROWSER` and the `xdg-open` handler, arrives with the broker (Phase 8).
- **Remote editors:** an OpenSSH server binary with no enabled service or socket, for `ssh-config`.
- **Descriptor:** `/usr/lib/nsl/machine.json`.

The layer is applied by a finalize script, which runs after the recipes' post-install scripts, so it overrides them. Files from the layer get mode 0755 or 0644 by their execute bit, whatever the checkout's umask.

### Per-machine data

The agent applies only these at `create` and `import`, in this order, before anything runs in the tree:

1. `/etc/localtime`, a relative link to the host's zone under `/usr/share/zoneinfo`;
2. `/etc/hostname`, the machine name, and `127.0.1.1 NAME` in `/etc/hosts`;
3. the account and its primary group: host username, UID and GID, home `/home/USER`, shell `/bin/bash`. An existing group with that GID is reused; an existing account with that name or UID fails creation;
4. `/etc/sudoers.d/nsl`, mode 0440, giving the account passwordless `sudo`;
5. the VM-side record from which the machine's nspawn settings are generated.

Offline nspawn runs for these steps use `--timezone=off`, `--register=no` and `--resolv-conf=off`, so they change nothing but the account.

### Descriptor

```json
{
  "schema": 1,
  "role": "machine",
  "build_id": "nsl-machine-debian-trixie-x86-64-r1",
  "distribution": "debian",
  "release": "trixie",
  "architecture": "x86-64",
  "family": "debian",
  "revision": 1,
  "machine_protocol": 1,
  "os_id": "debian",
  "os_version": "13",
  "systemd": "257.9",
  "capabilities": {"gui": {}, "nesting": {}},
  "integration_sha256": "…",
  "recipes_revision": "…",
  "mkosi_revision": "…"
}
```

`machine_protocol` versions the machine layer and per-machine data. It MUST equal the VM descriptor's `machine_protocol`; a mismatch is rejected. An image declares the capabilities its profile intends. Publication refuses an image unless the acceptance checks for every declared capability pass. The initial capabilities are `gui`, a Wayland client with fonts, and `nesting`, rootless Podman. `provisioning.cloud_init` is reserved for the [deferred provisioning contract](provisioning.md).

## Rules

- The host CLI and the agent MUST NOT call distro package managers or contain per-distro logic. A workaround for one family MUST stay in that family's adapter.
- Creation MUST work offline, from the verified image and the per-machine data alone.
- An image MUST boot to `running` or `degraded` under the VM's nspawn with `PrivateUsers=no`, without failed units, when its systemd is newer than the VM's.
- Images MUST NOT enable networkd or resolved, ship a machine ID, a usable root password, an SSH host key or a private key in any package keyring.
- Distro MAC policy does not apply inside machines; the VM kernel's security modules govern them. Images MUST NOT claim a distro's SELinux or AppArmor behavior.
- Image builds MUST pin their recipe and integration revisions and record package inventories, provenance and acceptance results.

### Acceptance

`scripts/probe-machines.py`, from the experiment's `workloads.py`, runs through the CLI on a fresh machine from each image. An image is accepted only when every check passes.

Workload checks:

| Check | Expected |
| --- | --- |
| System | systemd is PID 1; the system is `running` or `degraded`. |
| Packages | Install and remove Python, jq, Podman, `wayland-info` and a GUI application; `sudo` works. |
| Podman | Rootless build; `--userns=keep-id` volume ownership; HTTPS from a container; a published port reachable from the machine, the VM and a peer machine. |
| Files | `/mnt/host` spaces and Unicode, relative symlinks, executable bits, rename, delete and fsync; an edit seen by a peer machine; host ownership. |
| Ports | A user service's port forwarded to host loopback; the same port in a peer machine fails with `Address already in use`. |
| GUI | `wayland-info` lists `wl_compositor` and `xdg_wm_base`; a GUI application starts. |
| Translation | The working directory maps by device and inode, including a bind-mount alias; `/usr/share` is refused. |
| Persistence | Packages, home, an enabled system service and a Podman image survive a VM restart. |

Checks that an image does not repeat the [hub image tally](../plans/shared-vm-experiment.md#image-source-tally-hubnspawnorg-or-our-own):

| Check | Expected |
| --- | --- |
| Root password | root's shadow entry is locked or has no usable hash. |
| Keyrings | No private key under any package keyring, such as `/etc/pacman.d/gnupg`. |
| Network | networkd and resolved, and their sockets, are not enabled. |
| Machine ID | The image tree has no machine ID value; two machines from one image have different IDs. |
| SSH | No SSH host key in the image tree; no enabled SSH service or socket. |
| Session | `pam_systemd` in `/etc/pam.d/nsl`, `sudo`, a user bus, a font and the nesting mount are present. |
| Hostname | The machine resolves its own hostname without warnings. |
| Time zone | `/etc/localtime` links to the host's zone; installing or updating zone data succeeds. |

## References

- Rationale: [ADR-0017](../adr/0017-shared-vm-and-machine-images.md), [ADR-0009](../adr/0009-distribution-neutral-guest-contract.md), [ADR-0011](../adr/0011-image-profiles-and-portable-vsock.md).
- Evidence: Phases 2 and 3 and the image tally of the [shared-VM experiment](../plans/shared-vm-experiment.md). Implementation: Phase 4 of the [implementation plan](../plans/shared-vm-implementation.md).
- Related: [VM image](vm-image.md), [agent](agent.md), [CLI](cli.md), [image build](../../image/README.md).
