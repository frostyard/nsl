# Spec: nsl CLI v0.1

Contract for the binary and its unit tests; rationale [ADR-0003](../adr/0003-wrap-nspawn-for-development.md), implementation [lifecycle design](../design/lifecycle.md).

## Interface
`new NAME`, `ls`, `enter NAME`, `run NAME [--root] -- COMMAND`, `in NAME DIRECTORY [-- COMMAND]`, `gui NAME -- COMMAND`, `stop NAME`, `version`, `help`. See [README](../../README.md) for examples. Names contain lowercase letters, digits and interior hyphens; length <= 40. `run` propagates guest exit status.

## Rules
- `new` MUST refuse a preexisting machine and use a signed Debian 13 base.
- Commands MUST refuse machines without the matching owner UID label, boot mode and create origin.
- Guest work MUST run at the owner's numeric UID/GID by default; root is explicit with `run --root`.
- `in` MUST refuse `/`, nonexistent/non-directory paths and unsupported volume path characters; it MUST mount only the selected canonical directory at `/work`.
- `gui` MUST require a current Wayland socket and attach only that socket; it is not detached, so an absent program fails visibly.
- Switching a running machine's mounts MUST fail with stop guidance; `stop` MUST clear saved mounts.

## References
- Rationale: [ADR-0003](../adr/0003-wrap-nspawn-for-development.md)
- Context: [lifecycle design](../design/lifecycle.md)
- Delivered in: [v0.1.0 plan](../plans/v0.1.0.md)
