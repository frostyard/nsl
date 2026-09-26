# 0002 — Agent-portable instruction surface

- **Status:** Accepted
- **Date:** 2026-09-26 (retrospectively adopted)

## Context
Different coding agents look for different instruction and skill paths. Duplicated rules drift.

## Decision
`AGENTS.md` is canonical; `CLAUDE.md`, `GEMINI.md` and `.github/copilot-instructions.md` are symlinks to it. `.agents/skills/` is canonical; `.claude/skills` is a symlink. Instructions remain tool-agnostic.

## Consequences
One source of truth; Windows checkouts need symlink support or WSL.

## Alternatives considered
- Duplicated copies: drift between agents.

## References
- Builds on [ADR-0001](0001-record-architecture-decisions.md); shapes [AGENTS.md](../../AGENTS.md), [lifecycle design](../design/lifecycle.md), and [CLI spec](../specs/cli.md).
