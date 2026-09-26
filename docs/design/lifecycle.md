# Lifecycle and mounts

Living design. Rationale: [ADR-0003](../adr/0003-wrap-nspawn-for-development.md), [ADR-0002](../adr/0002-agent-portable-instruction-surface.md). Contract: [CLI spec](../specs/cli.md).

## Overview
`nsl` is a CLI adapter over nspawn.org. A signed Debian image is cloned into an `nsl-NAME` boot machine labeled with the host owner's UID. nspawn owns images, machine state and networking.

## Design
The Go `runner` invokes `nspawn` through `sudo -n` when non-root. `app.owned` checks name, origin, mode and owner label before operations. Guest bootstrap creates a named user with the invoking user's numeric UID/GID, persistent home, config/runtime directories and readiness marker. Normal commands execute as that UID; root is explicit.

`in` binds the canonical project directory to `/work`. `gui` binds only the current Wayland socket under the guest user's private runtime path. An already-running machine accepts only the identical mount set. nspawn remembers `start -v` settings: `stop` stops, starts briefly with `-v none`, then stops again to forget saved mounts. nspawn 1.5.1 rejects mixing `-v none` with a new mount in one start.

## Operational notes
Guest `exec -e HOME=...` does not override nspawn's user lookup; the CLI runs `env` in the guest instead. Guest bootstrap can leave an incomplete named machine; readiness is checked before use, and data is never silently deleted. Wayland socket changes after logout, so clear volumes before rebinding. CI uses a fake runner; manual host integration requires a working nspawn service.

## References
- Rationale: [ADR-0003](../adr/0003-wrap-nspawn-for-development.md)
- Contract: [CLI spec](../specs/cli.md)
- Built in: [v0.1.0 plan](../plans/v0.1.0.md)
