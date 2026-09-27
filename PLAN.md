# nsl v0.1.0 implementation plan

**Historical v0.1 container plan.** The current CLI manages full VMs with vmspawn. Start with the [current roadmap](docs/plans/wsl2-equivalent.md), [implementation results](docs/plans/vmspawn-implementation.md) and [next milestone](docs/plans/backup-and-reliability.md). The original plan below is retained as history.

Goal: a small compiled CLI for persistent Debian development machines on atomic Linux, using nspawn.org as the sole state/lifecycle backend. No host packages or automatic host-home sharing.

## Scope and decisions

- Go standard library, one binary, Linux only. Calls `sudo -n nspawn` (or direct nspawn when already root); no shell command interpolation. Requires existing nspawn.org installation and passwordless sudo for non-root CLI operations.
- Namespaced machines `nsl-<name>` and a reusable signed `nsl-base-debian-13` image. Validate names, never overwrite existing machines. Only operate on machines with an nsl label and matching owner UID. Initial image Debian 13 only; signed pull is the default.
- `new`, `ls`, `enter`, `run`, `in`, `gui`, `stop`, `version`, `help`. `run` executes as root only with explicit `--root`; normal work runs as a real named UID/GID user inside the machine. `new` configures user, home and shell in the guest.
- `in` mounts one canonical existing project directory at `/work`, runs user shell/command there. `gui` mounts the current session's Wayland socket under a private directory in the guest user's home and launches user command. No session bus, host home or devices shared.
- nspawn start remembers volumes, and rejects `-v none` combined with another `-v`. `enter`/`run` start without volumes; `in`/`gui` start with a single exact mount only when stopped. When already running, only identical mount sets are allowed; otherwise explain to stop first. `stop` stops and clears saved mounts by restarting with `-v none` and stopping again; clearly report failures. This avoids stale socket reuse.
- Integration check with a disposable `nsl` machine; do not remove user data automatically. Unit tests fake the runner and cover validation, argument building, mount mismatch and owner checks. Manual CLI build, help/version, and live package/file operation.

## Tasks

1. Scaffold Go module, CLI parser, nspawn runner, inspect/ownership validation and unit tests.
2. Implement lifecycle, user bootstrap, project and GUI workflows with tests.
3. Document installation, prerequisites, commands, limitations and test against real nspawn service.

## Non-goals

Automated host installation, X11, audio, GPU, portals, SSH agent, per-project machines, privilege-free nspawn administration, host command export, automatic switching of mounts on a running machine.
