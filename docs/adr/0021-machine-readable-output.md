# 0021 — Machine-readable output with `--json`

- **Status:** Accepted
- **Date:** 2026-10-05

## Context

Tools that integrate with nsl need its state: a terminal that opens machines
in tabs and manages them from its preferences, an editor extension, a shell
prompt. `nsl list`, `nsl images` and `nsl config` print tables for people:
`text/tabwriter` columns, empty cells for imported machines and machines
being removed, digests shortened to twelve characters, and notes such as
pending changes printed after a table without a separating line. Parsing
that text breaks when wording or layout changes, and cannot tell an empty
cell from a missing column without the header's offsets.

Reading nsl's state files instead would skip the ownership and permission
checks nsl applies to them, bind integrations to records nsl rewrites freely
before release, and miss everything nsl learns at run time: whether a VM and
its machines are running, where the configuration's values come from, which
catalogue images are cached.

nsl runs as the user with no daemon of its own
([ADR-0016](0016-wsl-style-machines.md)), so a command is the interface.

## Decision

`nsl list`, `nsl images` and `nsl config` accept `--json`. With it, a
successful command prints one JSON object on stdout and nothing else; it
reports the same facts as the table, with typed values (numbers, booleans,
arrays), full `sha256:` digests and source line numbers instead of the
table's abbreviations and phrases. Failures are unchanged: a message on
stderr, a non-zero exit, and nothing on stdout. Field names use
`snake_case`, as nsl's records do. The [CLI contract](../specs/cli.md)
defines each document.

The text and JSON forms are rendered from the same values, so they cannot
disagree. Other commands keep text output only: lifecycle commands report
through their exit status, and `run` passes a command's own streams through.

Adding a field is compatible, and consumers must ignore fields they do not
know. While nsl is pre-release, the contract may rename or remove fields in
place, as for every contract ([ADR-0001](0001-record-architecture-decisions.md)).

## Consequences

- Integrations get a contract they can test against instead of a layout.
- Every change to these three commands' output must update both forms and
  the spec's documents, and tests cover both.
- `--json` only reads state; it starts no VM or machine, as the table does
  not.

## Alternatives considered

- **Keep parsing tables:** fragile, and ambiguous for empty cells.
- **Read the state directory:** bypasses nsl's checks, couples tools to
  unstable records and cannot report run-time state.
- **A service API (socket, D-Bus, Varlink):** needs a daemon nsl does not
  run, and an access-control story for every caller.
- **`--format` with templates:** exposes Go struct names and template
  syntax as the contract, and a template still produces text to parse.

## References

- [CLI contract — machine-readable output](../specs/cli.md#machine-readable-output)
- [Command reference](../../site/content/reference/cli.md)
- [ADR-0016 — WSL-style machines](0016-wsl-style-machines.md)
