# 0001 — Record architecture decisions

- **Status:** Accepted
- **Date:** 2026-09-26 (retrospectively adopted)

## Context
This project is developed across sessions. Maintainers need durable rationale beyond conversational history.

## Decision
Record significant decisions in numbered ADRs with Context, Decision, Consequences, Alternatives and References. Accepted ADRs are immutable; reversals get a new ADR and mark the old one superseded.

## Consequences
Rationale is discoverable, at the cost of maintaining a linked documentation index.

## Alternatives considered
- Commit messages alone: not a navigable decision record.

## References
- Shapes the [documentation index](../README.md), [lifecycle design](../design/lifecycle.md), and [CLI spec](../specs/cli.md).
