# 0025 — Migration notes for breaking changes

- **Status:** Accepted
- **Date:** 2026-11-18

## Context

nsl started with no users. [ADR-0004](0004-managed-development-vms.md),
[ADR-0016](0016-wsl-style-machines.md) and
[ADR-0017](0017-shared-vm-and-machine-images.md) record that fact on their
dates, and replaced the CLI, its state and its images with no compatibility
or migration path. `AGENTS.md` kept that rule for all later work: it called
nsl pre-release with no users and forbade compatibility bridges, migrations
and version shims.

That is no longer true. nsl publishes tagged releases (v0.4.0 to v0.13.2 on
this date), a Homebrew cask ([ADR-0020](0020-homebrew-tap-publication.md)) and
a weekly signed image catalogue ([ADR-0012](0012-signed-image-distribution.md)).
On 2026-11-18 the repository had 101 stars and forks by 5 outside accounts,
and frostyard/igloo calls the `nsl` CLI and reads its `--json` output
([ADR-0021](0021-machine-readable-output.md)). A user who upgrades has
machines, state, archives and configuration from the earlier release.

nsl is still pre-release: it has no stable release series and makes no
stability promise ([SECURITY.md](../../SECURITY.md)).

## Decision

- **Breaking changes are allowed, and announced.** A change is breaking when
  data or usage from the previous release stops working: machine and VM state,
  `nsl.conf`, export archives, the catalogue a released CLI reads, the CLI
  grammar, the `--json` documents, the agent protocol between a released CLI
  and a published VM image, or the machine-image contract.
- **Migration note.** A breaking change marks its Conventional Commit subject
  with `!` (for example `feat!:`), so the release changelog shows it. The
  pull request body has a `Migration` section: what stops working, which
  users it affects, and the steps they take. A change to user-visible
  behavior also updates its site page, as every behavior change does.
- **Short compatibility path where it is cheap.** When the old form can be
  accepted at a small cost, accept it for 1 minor release with a warning that
  names the new form: an old flag or field name, an old state or
  configuration key that nsl reads and rewrites. The change that adds the path
  adds a test for it and an entry in [unscheduled work](../plans/later-work.md)
  to remove it after that release. When the path is not cheap, the migration
  note alone is enough.
- **Security checks are not compatibility paths.** A compatibility path never
  skips or weakens verification, trust, ownership or permission checks, and
  never takes a trust tier or host share from an archive. A host and VM with
  different agent protocol versions stay rejected, never negotiated
  ([agent protocol](../specs/agent.md)); the migration note names the update
  step instead.
- Documents still change in place: ADRs, specs, designs and plans follow
  [ADR-0001](0001-record-architecture-decisions.md).

## Consequences

- Users find what to do in the changelog and the pull request before they
  upgrade, and many renames do not break them for 1 release.
- A compatibility path is code to test and to remove later; the
  `later-work.md` entry keeps the removal from being forgotten.
- [ADR-0021](0021-machine-readable-output.md)'s rule that the `--json`
  contract may rename or remove fields in place now needs the migration note,
  and an old field name kept for 1 release where that is cheap.
- igloo follows a breaking CLI change in its own repository; the migration
  note tells its maintainers what to change.

## Alternatives considered

- **Keep no bridges and no migrations:** rejected. Outside users upgrade
  from Homebrew and the releases, and an unannounced break loses their
  workflow or their machines.
- **A stable 1.0 contract with deprecation periods for every interface:**
  rejected for now. nsl is pre-release, and a full guarantee costs more than
  its users need today.

## References

- Shapes: [`AGENTS.md`](../../AGENTS.md), the
  [pull request template](../../.github/pull_request_template.md),
  [CLI spec](../specs/cli.md)
- Narrows: [ADR-0021](0021-machine-readable-output.md)
- Follows: [ADR-0004](0004-managed-development-vms.md),
  [ADR-0016](0016-wsl-style-machines.md),
  [ADR-0017](0017-shared-vm-and-machine-images.md)
- Related: [ADR-0001](0001-record-architecture-decisions.md),
  [ADR-0006](0006-stopped-vm-backups.md),
  [ADR-0008](0008-offline-storage-management.md)
