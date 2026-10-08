# Spec: nsl VM image

Contract for the nsl VM image under [ADR-0017](../adr/0017-shared-vm-and-machine-images.md): the Debian trixie system that hosts machines as systemd-nspawn containers. One image serves the shared VM and every isolated machine's VM. Consumers: the [image composer](../../image/README.md), `scripts/probe-vm.py`, and the CLI's launch and readiness code.

The [agent protocol](agent.md) covers commands and machine operations. [Machine images](machine-images.md) cover what runs inside machines. [Image delivery](image-delivery.md) covers publication.

## Interface

### Disks and state

| Disk | Content | Lifetime |
| --- | --- | --- |
| Root | A raw copy of the image, extended to 16 GiB, that the host makes for each VM image build; the root filesystem grows into it. | Replaced when the VM image changes and by `nsl recover`. Holds no user state. |
| Data | A sparse raw file holding a btrfs filesystem labelled `nsl-data`, with subvolumes `machines` at `/var/lib/machines` and `state` at `/var/lib/nsl`. | Kept for the VM's lifetime; grows offline and never shrinks. |

The `state` subvolume holds everything that must survive a root replacement:

- `identity.json`: the VM binding (`version`, `id`, `uid`, `gid`, `role` and, in an isolated VM, `machine`);
- `ssh/`: the SSH host keys;
- `machines/NAME.json`: the VM's record of each machine (`id`, `account`, `build_id`, `created`).

Machines are subvolumes `machines/NAME`. nspawn settings for each machine are generated at every boot into `/run/systemd/nspawn/NAME.nspawn` from the records, so nothing machine-specific lives on the root.

### Boot credential

vmspawn passes the credential `nsl.vm`, a JSON object:

| Field | Meaning |
| --- | --- |
| `version` | `1`. |
| `id` | The VM's 32-hex ID. |
| `role` | `shared` or `isolated`. |
| `machine` | The machine's name and ID in an isolated VM; absent in the shared VM. |
| `uid`, `gid` | The host user's numeric UID and primary GID. |
| `public_key` | The VM's SSH client public key. |
| `autostart` | Whether to start every machine at boot. |
| `idle_timeout` | The initial idle timeout in minutes, until a request carries a newer one. |
| `shares` | Each shared tree's canonical host path, mounted at `/mnt/host` plus that path. Empty in an isolated VM. |
| `aliases` | Top-level host aliases, as a path under `/mnt/host` and a relative symlink target, such as `/mnt/host/home` → `var/home`. |

### Launch

The host launches the VM through the rootless device-descriptor path of [the lifecycle design](../design/lifecycle.md#launch-and-readiness):

- the root, and the data disk as an extra drive, both raw;
- each share read-write through virtiofs, at its `/mnt/host` path;
- the host's verified machine-image cache read-only at `/var/cache/nsl/images`;
- user-mode networking, vsock with the VM's CID, and the credential;
- memory and CPUs from the [configuration](cli.md#configuration).

### Descriptor

`/usr/lib/nsl/image.json` describes the image, and the agent returns it for readiness:

```json
{
  "schema": 1,
  "role": "vm",
  "build_id": "nsl-vm-trixie-x86-64-r1",
  "distribution": "debian",
  "release": "trixie",
  "architecture": "x86-64",
  "revision": 1,
  "agent_protocol": 1,
  "machine_protocol": 1,
  "transport": "nsl-vsock-ssh",
  "systemd": "257.9",
  "kernel": "6.12.48+deb13-amd64",
  "integration_sha256": "…",
  "recipes_revision": "…",
  "mkosi_revision": "…"
}
```

`agent_protocol` MUST equal the CLI's. `machine_protocol` is the machine-layer version the agent accepts; see [machine images](machine-images.md#descriptor). `systemd` is the version of nspawn and machined in the VM.

## Rules

### Boot and readiness

- The VM MUST become ready without network access. Readiness is the agent's `identity` answer over authenticated vsock SSH, which requires the data disk mounted, the binding checked and sshd running.
- On first boot, the VM MUST record the credential's `id`, `uid`, `gid`, `role` and `machine` in `identity.json`. On later boots, a credential that differs in any of them MUST fail readiness without changing state.
- A refused data disk or a mismatched credential MUST power the VM off at once, so the host reports the failure instead of waiting for readiness.
- sshd MUST listen only on the nsl-owned vsock socket ([ADR-0011](../adr/0011-image-profiles-and-portable-vsock.md)). It MUST accept only the credential's key, for `root`, forced to the agent as the [agent protocol](agent.md#transport) specifies.
- Files that must survive a crash, including the binding and host keys, MUST be flushed to the data disk before sshd starts.
- The VM runs on UTC; machines show the host's zone.
- The VM's resolver MUST NOT use LLMNR or mDNS. The user-mode network has no link-local peers, and sshd resolves the vsock peer name for every PTY session; LLMNR made that take seconds.

### Data disk

- The VM MUST use exactly one writable whole disk other than the root disk as its data disk.
- A disk with no signature MUST be formatted as btrfs `nsl-data` with its two subvolumes. A disk with any other signature, a second `nsl-data` filesystem, or a missing subvolume MUST fail readiness and leave the disk unchanged.
- The VM MUST grow the data filesystem to the disk's size at every boot before readiness ([ADR-0010](../adr/0010-explicit-guest-root-growth.md)).
- Machines are mounted with `compress=zstd:1` and `noatime`.

### Root replacement

- A new root from a different image build MUST keep the VM's binding, SSH host keys, machines and records, so the host's pinned host key still matches.
- The VM MUST NOT store user or machine state on the root. The host MAY discard the root whenever the VM is stopped.

### Host files

- In a shared VM, each share MUST be mounted read-write at `/mnt/host` plus its canonical host path, and each alias MUST be a relative symlink under `/mnt/host`, recreated at every boot. An isolated VM MUST have no `/mnt/host` content.
- Host files MUST appear with host ownership. Writes by guest root MUST land as the host user, and guest root MUST NOT write where the host user cannot.
- Host Unix sockets MUST NOT connect across the share.
- The image cache share MUST be read-only and MUST NOT be bound into machines.

### Machines

- The image MUST provide `systemd-container` (nspawn and machined), `btrfs-progs`, and tar with zstd, xattrs and ACLs. It also provides the agent at `/usr/lib/nsl/nsl-agent` and a Waypipe server.
- Before `machines.target`, a boot service MUST write each machine's nspawn settings from its record, and the agent MUST rewrite them before starting a machine: `Boot=yes`, `PrivateUsers=no`, `Timezone=off`, no virtual Ethernet, `BindReadOnly=/run/systemd/resolve` and, in a shared VM, `Bind=/mnt/host`. It MUST start every machine when `autostart` is true, and none otherwise.
- An idle monitor, `nsl-idle.service` running `nsl-agent idle`, MUST poll every 10 s:
  - It counts each running machine's `nsl-run-*` units whose command has not exited, and connections accepted on its Waypipe display socket, `/run/nsl/desktop/NAME/wayland-0`.
  - A machine is idle from the later of the moment it was last seen with either and its latest `start` or `run` request. After the latest `idle_timeout` (none when it is 0, or unreadable), the monitor powers it off as `stop` does. It skips a machine a lifecycle operation holds, and checks again under the machine's lock.
  - It powers the VM off once no machine has been running, and no request has been in flight or arrived, for 60 s. It decides under the exclusive request lock and keeps it, so a request that arrives meanwhile waits and the host starts the VM again.
- Runtime state for the monitor lives in `/run/nsl`: `idle-timeout`, `requests.lock`, `activity/NAME` and `locks/NAME.lock`.
- In a shared VM, each machine's desktop directory `/run/nsl/desktop/NAME` holds its Waypipe display socket and broker socket while its [desktop session](agent.md#display) lasts. The directory is bound into the running machine at `/run/nsl/desktop`; nothing else under `/run/nsl` is visible to machines.

### Acceptance

`scripts/probe-vm.py`, from the experiment's `driver.py check`, MUST pass on a locally built image before publication:

| Check | Expected |
| --- | --- |
| Readiness | `identity` matches the VM record and descriptor, on first and later boots. |
| Formatting | A blank data disk becomes `nsl-data` with both subvolumes. |
| Refusal | A data disk with an existing signature (the probe writes a swap header) fails readiness quickly and stays byte-identical. |
| Binding | A credential with a different `id` or `uid` fails readiness. |
| Root replacement | After a new root, the pinned host key, `identity.json` and a marker machine survive. |
| Growth | After the host grows the data disk, the filesystem reports the new size. |
| Allowlist | Each share is mounted at its `/mnt/host` path, and nothing else is. |
| Ownership | Host files show the host UID and GID; writes by the account and by root land as the host user; root cannot write a root-owned host directory. |
| Sockets | A host Unix socket under a share refuses connections, and the host listener is never reached. |
| Isolation | An isolated VM's credential names no shares and its only virtiofs mount is the image cache; checked by `scripts/probe-machines.py --isolated`, since an isolated VM needs a machine. |

## References

- Rationale: [ADR-0017](../adr/0017-shared-vm-and-machine-images.md), [ADR-0010](../adr/0010-explicit-guest-root-growth.md), [ADR-0011](../adr/0011-image-profiles-and-portable-vsock.md), [ADR-0007](../adr/0007-maintainable-guest-boot.md).
- Evidence: Phase 1 of the [shared-VM experiment](../plans/shared-vm-experiment.md#phase-1--shared-vm-baseline). Implementation: Phase 3 of the [implementation plan](../plans/shared-vm-implementation.md).
