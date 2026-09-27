# Spec: nsl vmspawn CLI

Contract for the binary and tests. Rationale: [ADR-0005](../adr/0005-vmspawn-and-nspawn-images.md). Mechanisms: [lifecycle](../design/lifecycle.md).

## Interface

| Command | Behavior |
| --- | --- |
| `create NAME --image FILE --digest sha256:HEX [--project DIR] [--desktop] [--cpus N] [--memory GiB] [--disk GiB]` | Verify a local raw image and prepare an independent persistent VM. |
| `list` | List owned environments and runtime state. |
| `start NAME` | Launch if stopped; verify authenticated guest identity and restore forwarding. |
| `shell NAME` | Start if needed; interactive Bash login shell in guest home. |
| `exec NAME [--root] [--tty] [--workdir /PATH] -- COMMAND ...` | Execute argv as the guest user or explicit guest root. |
| `gui NAME -- COMMAND ...` | Run an attached software-rendered Waypipe session. |
| `stop NAME` | Shut down the owned VM and forwarding; preserve persistent state. |
| `recover NAME` | Stop, resume preparation/check existing disk and restart; preserve identity and data. |
| `export NAME FILE.nsl` | Export a stopped prepared VM as a private self-contained backup; never overwrite. |
| `restore NAME FILE.nsl [--project DIR] [--desktop]` | Verify and restore under an unused name; new runtime identity, preserved guest identity, no image-cache dependency. |
| `remove NAME [--yes]` | Preview permanent deletion; `--yes` removes a stopped owned VM and resumes interrupted deletion. |
| `resize NAME --disk GiB` | Grow a stopped prepared disk; retain identity, refuse shrinking, resume pending growth. |
| `ports NAME` | Show timestamped loopback forwarding status and bind conflicts. |
| `logs NAME` | Show the last 100 journal entries for the VM and forwarding units. |
| `ssh-config NAME` | Start if needed; print the owned SSH config path (alias `guest`). |
| `doctor` | Check executables, group-based device access, user namespaces and the user systemd manager. |
| `version`, `help` | Print build version or usage. |

Names start with a lowercase ASCII letter, contain lowercase letters/digits/interior hyphens and have at most 24 characters. Flags follow the name. CPUs: 1–64 (default 2); RAM: 1–128 GiB (default 2); disk: 4–4096 GiB (default 16), never smaller than the raw image. Creation requires x86_64. Images must implement the nsl boot-credential/command protocol; a digest verifies selected bytes, not publisher identity.

## Rules

- Host commands MUST run as the normal user, without implicit sudo or host configuration changes.
- State and runtime unit identity MUST be validated before mutation. Existing names, foreign units and unsupported metadata MUST be rejected.
- Lifecycle changes MUST serialize per environment; command sessions MAY run concurrently after readiness.
- Freshly created environments MUST have independent disks and client keys. All environments MUST have distinct runtime units and CIDs within a state directory. Restored copies preserve their backup's guest identity/keypair and have independent disk files ([ADR-0006](../adr/0006-stopped-vm-backups.md)).
- Launch MUST request the vsock SSH listener explicitly, so readiness does not depend on automatic detection before modules load.
- Readiness MUST verify the guest ID, UID, GID and protocol even for an already running unit.
- Interrupted preparation MUST retain state. Recovery MUST preserve an existing disk, private key and host-key trust; it MUST NOT silently repair corruption or regenerate lost keys.
- Normal guest work MUST use the host numeric UID and primary GID. `--root` selects guest root, never host root.
- Guest argv MUST retain spaces, quotes and metacharacters literally. Binary streams and exit status MUST survive non-PTY execution.
- Only an explicit canonical project directory other than `/` MAY be shared. Unsupported path delimiters/control characters MUST be rejected.
- GUI MUST require desktop opt-in and a host Wayland session. It MUST NOT change the persistent share.
- Automatic service forwards MUST bind host loopback, report/retry conflicts and never evict an existing host listener.
- Stop MUST preserve guest data and configuration and terminate that environment's forwarding service.

## Offline storage management

Removal MUST require stopped owned VM and forwarding units, explicit `--yes`, and manager/environment locks. It MUST preserve external host projects, cached images and external archives. Projects inside the environment state MUST block removal. Interrupted removal MUST reserve the name under `removing/NAME`, appear as `Removing` in `list`, and resume only from validated metadata or an empty final directory. Waiting lifecycle calls MUST reject a newly allocated environment under the same name.

Resize MUST require a prepared stopped standalone disk, refuse shrinking and invalid capacities, and persist the pending target before disk mutation. It MUST sync and validate the resulting disk before committing capacity. Pending growth MUST appear as `Resizing`, block start/export, and resume through the same resize target or `recover`. Capacity inconsistent with both committed and pending sizes MUST be refused. Guest filesystem growth is a separate boot operation supplied by the image; Debian v5 explicitly requires it before readiness. See [ADR-0008](../adr/0008-offline-storage-management.md), [ADR-0010](../adr/0010-explicit-guest-root-growth.md), and the [guest contract](guest-images.md).

## Backup version 1

`export` MUST hold the environment lock and refuse active, activating or stopping VMs. It MUST check a standalone qcow2 disk, write a compact independent copy and publish a mode-0600 archive without replacing an existing path. It MUST preserve the source. The archive contains `manifest.json`, `disk.qcow2`, `keys/identity`, `keys/identity.pub` and optional `known_hosts`; host shares are excluded.

`restore` MUST require x86_64 and matching numeric UID/primary GID. It MUST validate format version, names, entry types, resource/size bounds, file completeness, SHA256 checksums, keypair correspondence and a standalone qcow2 disk before publishing an environment. Links, duplicates, traversal, unexpected entries, trailing data and external disk references MUST be refused. Invalid archives MUST leave no named environment. Verified state interrupted during final preparation MAY be retained for `recover`.

Restore MUST allocate new runtime identity and reconstruct local configuration. Guest binding, SSH keys, pinned host trust, packages and disk contents MUST be preserved. Host project and desktop access MUST default to absent and require explicit restore flags. The original base-image cache MUST NOT be required. Checksums are not publisher authentication; archives contain unencrypted guest data and credentials.

## Validation boundaries

Unit tests use fake tools and local Python helper processes. Integration tests need disposable real VMs. They exercise first boot, growth, multiple identities, port conflicts and recovery; they do not establish arbitrary crash recovery or full desktop parity. Exact results: [implementation report](../plans/vmspawn-implementation.md).

## References

- [Lifecycle](../design/lifecycle.md), [image build](../../image/README.md).
- [ADR-0005](../adr/0005-vmspawn-and-nspawn-images.md), [roadmap](../plans/wsl2-equivalent.md).
