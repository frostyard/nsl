# 0023 — Local problem records

- **Status:** Accepted
- **Date:** 2026-11-04

## Context

A panic of the host CLI, or a state that nsl's own code should never reach,
leaves only the stderr of 1 terminal, which the user often closes. A panic in
a helper unit (`_forward`, `_desktop`, `_devices`, `_launch`) is in the user
journal as unstructured text, and one in `_ssh` (ssh's `ProxyCommand`) is in
the editor's SSH log. Nobody can count these problems, and
chairlift cannot show them.

Frostyard decided in [core ADR-0069](https://github.com/frostyard/core/blob/main/docs/adr/0069-report-problems-local-first-opt-in-with-one-sender-per-host.md)
that apps record panics and coded errors as journal entries with 1 fixed
`MESSAGE_ID`, through the `problems` Go package of `github.com/frostyard/std`.
Apps never send: chairlift (on desktops) and `snosi-health` (on Floe) are the
only senders, and only after the user or admin turns reporting on. The package
writes 1 datagram to the local journal socket, drops a record that does not
fit or that the journal cannot take at once, caps a process at 10 records a
minute, and writes nothing when the version is not a release version.

nsl is pre-release and runs on hosts that are not snosi images too. The VM
agent runs inside the VM, whose journal is not the host's.

## Decision

- The host CLI records problems in the local systemd journal with
  `github.com/frostyard/std/problems`, app ID `org.frostyard.nsl`, its
  release version (`main.version`, which GoReleaser stamps without `v`) and
  component `daemon` for the internal helper commands (arguments that start
  with `_`) or `cli` for the others.
- `main` makes 1 recorder. It turns on the crash output (a crash file under
  `$STATE_DIRECTORY`, else `$XDG_STATE_HOME`, else `~/.local/state`, in
  `frostyard-problems/org.frostyard.nsl`; after a crash it holds Go's raw
  crash report), so that the next start records a crash that no `recover`
  saw and removes the file. It records a panic and panics again: a panic
  still exits 2, and Go's panic output adds `[recovered, repanicked]`. Each long-lived
  goroutine of the host CLI (the desktop session's and the `nsl-open`
  broker's) records its own panic the same way.
- nsl records exactly 4 coded errors, at the point where it returns them,
  with the returned error unchanged:
  - `state.vm_record_invalid`: a VM record that does not decode, does not
    validate, or is in the wrong place.
  - `state.machine_record_invalid`: a machine record that does not decode or
    does not match its name and the user's identity.
  - `vm.exited_on_start`: the VM unit ended while nsl started it.
  - `vm.readiness_timeout`: the started VM did not become ready in time.
  A user mistake, a missing tool or a permission error gets no record.
- nsl sends nothing over a network for problems, and adds no flag or setting.
  With no journal socket, a record is dropped silently; a dev build (version
  `vmspawn-dev` or a `git describe` string) records nothing.
- The agent (`cmd/nsl-agent`) records nothing.

## Consequences

- chairlift's Problems page can list nsl's panics and internal errors on a
  host, and a user who turns reporting on can send their counts.
- A record keeps nsl's error text or panic message in the local journal; it
  can name nsl's own state path or a unit. Panic frames are function names
  only, with no file path, line or argument.
- `make` builds stamp `git describe` and record nothing; only release builds
  do. A new code is a decision: add it here, in the README and in the site's
  limits page.
- nsl depends on `github.com/frostyard/std`, a Frostyard module with no
  further dependencies.

## Alternatives considered

- **Record every error nsl returns:** most are user mistakes or host setup,
  which say nothing about nsl's code and would fill the rate cap.
- **Record in the agent too:** the VM's journal is not the host's, and no
  reader looks there.
- **Send reports from nsl:** core ADR-0069 allows 1 sender per host, after
  consent; nsl is not it.

## References

- [frostyard/core ADR-0069](https://github.com/frostyard/core/blob/main/docs/adr/0069-report-problems-local-first-opt-in-with-one-sender-per-host.md) and its problem report spec.
- [CLI spec](../specs/cli.md), [ADR-0016](0016-wsl-style-machines.md), [ADR-0017](0017-shared-vm-and-machine-images.md).
- User documentation: [limits and troubleshooting](../../site/content/reference/limits.md).
