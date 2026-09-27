# 0003 — Wrap nspawn for atomic-host development

- **Status:** Superseded by [ADR-0004](0004-managed-development-vms.md)
- **Date:** 2026-09-26 (retrospective record of v0.1 design)

## Context
The host has immutable `/usr`; packages needed for development should live in persistent guests. Direct nspawn commands make user and temporary mount handling cumbersome.

## Decision
Build a small Go CLI backed by nspawn.org's existing service and state. Default to one long-lived Debian 13 machine per name, run normal work as a guest user with host numeric UID/GID, and expose only one project or current Wayland socket at a time. Root package management is explicit.

## Consequences
No second daemon or host package installation. Switching mount contexts requires stopping the machine; GUI access grants a trusted program access to the graphical session. Additional distributions and desktop services remain future work.

## Alternatives considered
- Full home and desktop integration: rejected for initial release because it widens host access.
- Separate per-project environments: deferred; shared toolchain in one machine is simpler initially.

## References
- Built in [v0.1.0 plan](../plans/v0.1.0.md); mechanisms in [lifecycle design](../design/lifecycle.md); commands in [CLI spec](../specs/cli.md).
