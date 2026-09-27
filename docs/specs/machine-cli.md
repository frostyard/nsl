# Spec: nsl machine CLI

**Planned; not implemented.** This contract replaces the [current CLI contract](cli.md) under [ADR-0016](../adr/0016-wsl-style-machines.md). Items marked *topology* depend on the [shared-VM experiment](../plans/shared-vm-experiment.md). There is no compatibility or migration path from the environment CLI.

## Interface

| Command | Behavior |
| --- | --- |
| `nsl [-m NAME]` | Login shell in NAME or the default machine, in the translated host directory or the guest home. |
| `nsl run [-m NAME] [--root] [--cd PATH] COMMAND [ARGS...]` | Run argv in the machine. A PTY is used when stdin and stdout are terminals. |
| `nsl create NAME --distro DISTRO:RELEASE [--isolated] [--default] [--user NAME] [--offline]` | Prepare a machine from a verified catalogue selection. `--image FILE --digest sha256:HEX` selects a local image. |
| `nsl list` | Name, state, distro, trust tier and default marker for every owned machine. |
| `nsl default NAME` | Make NAME the default machine. |
| `nsl stop NAME` | Stop one machine and preserve all state. |
| `nsl shutdown` | Stop every running machine, and the shared VM if one exists. |
| `nsl export NAME FILE` | Write a whole-machine archive of a stopped machine; never overwrite. |
| `nsl import NAME FILE [--isolated]` | Verify and import an archive under an unused name. |
| `nsl remove NAME [--yes]` | Preview, then permanently remove a stopped machine. |
| `nsl ports [NAME]` | Forwarding status and conflicts for one machine or all machines. |
| `nsl logs [NAME]` | Recent host-side logs for one machine or all machines. |
| `nsl ssh-config NAME` | Start if needed and print an SSH configuration for remote editors. |
| `nsl images`, `nsl pull DISTRO:RELEASE` | Unchanged from the [delivery contract](image-delivery.md). |
| `nsl doctor`, `nsl version`, `nsl help` | Host checks, build version and usage. |
| `nsl recover NAME`, `nsl resize NAME --disk GiB` | *Topology:* per-machine disks keep today's behavior; a shared VM redefines both. |

Guest commands installed by every image:

| Command | Behavior |
| --- | --- |
| `nsl-open TARGET` | Open an `http`/`https` URL or a path under `/mnt/host` with the host's default handler. Also set as `BROWSER` and the `xdg-open` handler. |
| `nsl-path [--host \| --guest] PATH` | Translate between host and guest paths using the `/mnt/host` prefix. |

Machine names follow the current rules: a lowercase ASCII letter first, then lowercase letters, digits or interior hyphens, at most 24 characters.

## Rules

### Selection and entry

- There MUST be zero or one default machine. The first machine created MUST become the default; `--default` MUST select it explicitly.
- Removing the default MUST leave no default. Bare `nsl` without a default MUST fail and list machines; it MUST NOT pick one implicitly.
- The host working directory MUST be resolved through symlinks. When it lies in a shared tree and the machine is not isolated, the guest directory MUST be `/mnt/host` followed by the resolved path.
- A shell whose directory cannot be translated MUST start in the guest home and say so on stderr.
- `run` with an untranslatable directory and no `--cd` MUST fail without running anything. A command intended for the project directory must not run somewhere else.
- `--cd` MUST take an absolute guest path.

### Host files

- Machines that are not isolated MUST see the user's home, `/run/media/USER` and `/mnt`, read-write, at their canonical host paths under `/mnt/host`. Nothing else from the host filesystem MAY be shared.
- Top-level host symlinks that resolve into a shared tree SHOULD appear as matching relative symlinks under `/mnt/host`.
- Files created through `/mnt/host` MUST be owned by the host user. Guest root MUST NOT gain host permissions beyond the host user's.
- Unix sockets under shared trees MUST NOT be proxied.
- Isolated machines MUST have no `/mnt/host` content, desktop session or broker access.

### Account and execution

- The guest account MUST use the host username unless `--user` overrides it, together with the host numeric UID and primary GID. Its home is `/home/USERNAME` on machine storage.
- A username that conflicts with an existing guest account MUST fail creation clearly without changing that account.
- The guest hostname MUST be the machine name.
- Argv MUST be preserved literally. Binary streams, separate stdout/stderr, exit status and signals MUST survive execution.
- `--root` MUST select guest root and never host root.

### Desktop and host actions

- When the host has a Wayland session and the machine is not isolated, guest sessions MUST receive a `WAYLAND_DISPLAY` served by a persistent per-machine Waypipe session. Host display sockets MUST NOT be shared directly.
- The broker MUST authenticate each machine. It MUST accept only `http`/`https` URLs and translatable `/mnt/host` paths, and refuse every other target.
- Launcher entries and terminal profiles MUST be user-scoped, nsl-prefixed and removed with their machine. They MUST NOT overwrite unrelated files. This work belongs to a later phase.

### Lifecycle

- Commands MUST start a stopped machine and wait for authenticated readiness.
- A machine with no nsl command sessions and no connected GUI clients MUST stop after its idle timeout (proposed default 15 minutes; 0 disables). Services started inside the machine do not keep it running.
- `stop` and idle stop MUST preserve all machine state.
- Automatic forwarding MUST bind host loopback, report and retry conflicts, and never evict an existing listener. *Topology:* in a shared VM, machines share one network namespace, so the same port in two machines conflicts inside the VM, as in WSL.
- Import MUST choose the trust tier from its flags, defaulting to not isolated. It MUST NOT read the tier from the archive.
- Ownership validation, locking, safe removal and archive validation from the [current contract](cli.md) carry over.
- *Topology:* resource limits are per machine with separate VMs, or a single global budget with a shared VM.

### Open interface questions

- The configuration surface for idle timeout and resource budgets, whether flags, `nsl set` or a configuration file.
- Whether to translate absolute host symlinks that point into shared trees; see the [experiment plan](../plans/shared-vm-experiment.md).

## References

- Rationale: [ADR-0016](../adr/0016-wsl-style-machines.md). Current contract: [CLI](cli.md), [guest images](guest-images.md), [provisioning](provisioning.md).
- Topology: [shared-VM experiment](../plans/shared-vm-experiment.md), [ADR-0005](../adr/0005-vmspawn-and-nspawn-images.md).
