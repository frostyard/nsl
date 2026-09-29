---
description: Known limits of nsl, and what to check when something goes wrong.
---

# Limits and troubleshooting

## Known limits

- **x86-64 only.** The VM and machine images are x86-64.
- **Idle stop ignores services.** A machine stops after `idle_timeout` minutes without nsl commands, editor sessions or windows, even if a service inside it is busy. Set `idle_timeout = 0` to keep machines running.
- **No file events from the host.** Host edits under `/mnt/host` produce no inotify events in machines. Keep watched builds in the guest home.
- **No automount triggers.** An unmounted host automount point appears empty in machines until the host mounts it.
- **Wayland only.** Machines have no X server, so X11-only applications open no windows.
- **Host loopback only.** Forwarded ports bind `127.0.0.1` on the host, and one port serves one machine at a time.
- **Archives are unencrypted** and can contain credentials. Import requires your UID and GID.
- **Your whole home is visible** to machines that are not isolated, including nsl's state and keys. This matches the [trust model](../concepts/trust.md).
- **Data disks only grow.** There is no shrinking.

## Troubleshooting

`nsl doctor` fails
:   A tool, the UEFI firmware, a device or a permission is missing. Install the tool or firmware with your host's own tools. If doctor reports `MISSING kvm group membership`, ask an administrator to run the displayed command to add you to `kvm`, then retry nsl; no new login is needed. nsl changes none of these itself.

Bare `nsl` fails and lists machines
:   There is no default machine, for example after removing it. Run `nsl default NAME`, or `nsl -m NAME`.

`nsl run` refuses to run
:   The current directory is not in a shared tree, or the machine is isolated. Pass `--cd` with an absolute guest path.

A port is not reachable from the host
:   Run `nsl ports`. A `conflict` means something else holds the port, on the host or in another machine. The server must listen on port 1024 or above, and not on 5353 or 5355.

A window does not appear
:   Check that `nsl doctor` finds Waypipe, that the command ran from a Wayland session, and that the machine is not isolated. `nsl logs NAME` shows the desktop session.

The VM does not become ready
:   Run `nsl logs` for the VM's messages, then `nsl recover`. Recovery starts the VM from a fresh root and keeps your machines.

A new setting has no effect
:   Resource changes apply at the VM's next start. `nsl config` shows what is pending; `nsl shutdown` applies it at the next command.

## Report a problem

Open an issue at [github.com/frostyard/nsl ↗](https://github.com/frostyard/nsl/issues) with the output of `nsl version`, `nsl doctor` and, where it helps, `nsl logs`.
