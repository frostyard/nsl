# 0022 — Desktop menu entries for machine applications

- **Status:** Accepted
- **Date:** 2026-11-02

## Context

GUI applications in a machine that is not isolated open their windows on the
host desktop through the machine's Waypipe session
([ADR-0016](0016-wsl-style-machines.md)). The user must still start each one
from a terminal: `nsl run galculator`. WSLg puts the Linux applications of a
distribution in the Windows Start menu; the [WSL2 plan](../plans/wsl2-equivalent.md)
asks for the same on Linux hosts, and the [CLI spec](../specs/cli.md) already
requires that launcher entries are user-scoped, nsl-prefixed, removed with
their machine, and never overwrite unrelated files.

The application's own desktop entry, its icon and its command line are in
the machine's root filesystem, inside the VM's data disk. The host cannot read
them directly. The agent's `run` operation can, as the machine account, with
argv only; no shell command line is sent to the guest.

Host desktops (GNOME, KDE and others) read user entries from
`$XDG_DATA_HOME/applications` and watch that directory. Desktop-entry `Exec`
lines have their own quoting rules and field codes (`%f`, `%U`) that name
files and URLs. Those names are host paths, and a machine needs them
translated to `/mnt/host` paths first.

`nsl export` is the archive command, so the name is not free for launchers.

## Decision

A new command group, `nsl app`, manages host menu entries for applications
in a machine:

- `nsl app add [-m NAME] APP` reads the machine's desktop entry `APP.desktop`
  (from the account's `~/.local/share/applications`, then
  `/usr/local/share/applications`, then `/usr/share/applications`) through
  the agent's `run` operation with `cat`, and writes the host entry
  `$XDG_DATA_HOME/applications/nsl-NAME-APP.desktop`.
- `nsl app remove [-m NAME] APP` deletes that entry and its icon.
- `nsl app list [-m NAME]` lists the entries nsl owns, for one machine or all.
- `nsl remove NAME --yes` deletes every entry and icon of the machine once the
  machine is gone, before it finishes removal; an interrupted removal deletes
  them when it resumes.

The host entry is written by nsl from selected keys, not copied: `Name` with
the suffix ` (NAME)`, `GenericName`, `Comment`, `Categories`, `Keywords`,
`StartupWMClass` and `Terminal`, and the markers `X-Nsl-Machine`,
`X-Nsl-Machine-ID` and `X-Nsl-App`. `Exec` runs the guest command through nsl:
`NSL run -m NAME --cd DIR -- ARGV`, where NSL is the absolute path of the `nsl`
found on `PATH` (or of the running binary), DIR is the entry's absolute `Path`
or the account's home, and ARGV is the guest `Exec` parsed into arguments with
its field codes removed. `TryExec` names the same NSL, so a desktop hides the
entry when nsl is gone. Unsupported shell syntax in the guest `Exec` is refused
rather than guessed. Isolated machines have no desktop session, so they get no
entries.

The icon comes from the guest entry's `Icon`: an absolute PNG or SVG path, or
a name found in the `hicolor` theme under `/usr/share/icons/hicolor`, scalable
first and then the largest PNG. nsl copies it to
`NSL_HOME/machines/NAME.apps/` and names that absolute path in the entry. An
entry without a found icon has no `Icon` key.

Ownership is the marker triple: a host entry is nsl's only when its markers
name this machine, its current ID and the application. nsl creates a new entry
without replacing any path (a hard link from a temporary file), replaces only
an entry it owns, and refuses every other existing file. `nsl app add` writes
under the machine lock and rechecks the machine's ID, so it cannot race
`nsl remove`.

Theme lookup beyond `hicolor`, and translation of `%f`, `%F`, `%u` and `%U`
into machine paths so the host can pass files and URLs, are later work. Until
then the host entry declares no `MimeType` and passes no files.

## Consequences

- Machine applications appear in the host's menu and search with their own
  name, a machine suffix and, for most packaged applications, their icon.
- The change is host-only. The agent protocol, the VM image and the machine
  images are unchanged; reading uses `cat` from coreutils or busybox, present
  in every machine image.
- `nsl app add` starts the machine, as `nsl run` does.
- The entry depends on the path of `nsl`. Moving the binary hides the entry
  through `TryExec`; `nsl app add` again rewrites it.
- An application opened from the menu starts without files until field-code
  translation lands; an entry for a document viewer opens it empty.
- Localized names are not copied; the entry shows the unlocalized `Name`.

## Alternatives considered

- **`nsl export`:** the archive command already uses it.
- **An agent operation that reads entries and icons:** one round trip instead
  of several, but it changes the agent protocol and needs a new VM image. `cat`
  through `run` is enough for one application at a time.
- **Copying the guest entry and rewriting `Exec`:** keeps keys such as
  `MimeType`, `Actions` and `DBusActivatable` that the host cannot honor for a
  machine application, so desktops would offer actions that fail.
- **`Exec=nsl …` by name:** desktop sessions do not always have the user's
  shell `PATH`, for example a Homebrew prefix; the absolute path works without
  it.
- **Icons in `$XDG_DATA_HOME/icons/hicolor`:** standard, but it needs the icon
  cache to notice new files, and removal would need its own ownership checks
  in a shared tree. An absolute path inside nsl's own state needs neither.
- **Automatic discovery of every application:** clutters the menu with every
  tool a package brings; explicit adding comes first, as the WSL2 plan says.

## References

- Contract: [CLI spec](../specs/cli.md#desktop-and-host-actions).
- Rationale: [ADR-0016](0016-wsl-style-machines.md) (desktop integration and
  host-file ownership), [ADR-0017](0017-shared-vm-and-machine-images.md).
- Plans: [WSL2 equivalent](../plans/wsl2-equivalent.md),
  [implementation plan](../plans/shared-vm-implementation.md).
- User guide: [Desktop applications](../../site/content/guides/desktop.md#add-an-application-to-the-menu).
