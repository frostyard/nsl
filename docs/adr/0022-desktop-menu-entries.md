# 0022 — Desktop menu entries for machine applications

- **Status:** Accepted
- **Date:** 2026-11-02; revised 2026-11-04 (icon themes, files and URLs; opt-in file types)

## Context

GUI applications in a machine that is not isolated open their windows on the
host desktop through the machine's Waypipe session
([ADR-0016](0016-wsl-style-machines.md)). The user must still start each one
from a terminal: `nsl run galculator`. WSLg puts the Linux applications of a
distribution in the Windows Start menu; the [WSL2 plan](https://github.com/frostyard/nsl/blob/e617966e80f6534aa6525f016def59ab58c1d864/docs/plans/wsl2-equivalent.md)
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

- `nsl app add [-m NAME] [--mime-types] APP` reads the machine's desktop entry `APP.desktop`
  (from the account's `~/.local/share/applications`, then
  `/usr/local/share/applications`, then `/usr/share/applications`) through
  the agent's `run` operation with `cat`, and writes the host entry
  `$XDG_DATA_HOME/applications/nsl-NAME-APP.desktop`.
- `nsl app remove [-m NAME] APP` deletes that entry and its icon.
- `nsl app list [-m NAME]` lists the entries nsl owns, for one machine or all,
  with the number of file types each one declares.
- `nsl remove NAME --yes` deletes every entry and icon of the machine once the
  machine is gone, before it finishes removal; an interrupted removal deletes
  them when it resumes.

The host entry is written by nsl from selected keys, not copied: `Name` with
the suffix ` (NAME)`, `GenericName`, `Comment`, `Categories`, `Keywords`,
`StartupWMClass` and `Terminal`, and the markers `X-Nsl-Machine`,
`X-Nsl-Machine-ID` and `X-Nsl-App`. `Exec` runs the guest command through nsl:
`NSL run -m NAME --cd DIR -- ARGV`, where NSL is the absolute path of the `nsl`
found on `PATH` (or of the running binary), DIR is the entry's absolute `Path`
or the account's home, and ARGV is the guest `Exec` parsed into arguments.
`TryExec` names the same NSL, so a desktop hides the entry when nsl is gone.
Unsupported shell syntax in the guest `Exec` is refused rather than guessed.
Isolated machines have no desktop session, so they get no entries.

### Files and URLs

The first `%f`, `%F`, `%u` or `%U` that stands as an argument of its own after
the command in the guest `Exec` stays in the host `Exec`, at the same place among ARGV, and `run`
gets a new option that says where its expansion lies:
`NSL run -m NAME --cd DIR --host-files CODE:BEFORE:AFTER -- ARGV`. CODE is
`f`, `F`, `u` or `U`; BEFORE is the number of guest arguments before the field
code and AFTER the number after it. The desktop expands the field code into
host paths or URLs; `run` takes the arguments between the first BEFORE and the
last AFTER as those, translates each one, and runs the guest command with the
results in their place. Every other field code, and a second file field code,
is dropped as before.

Translation is the one `run` already uses for its working directory: a host
path that lies in a shared tree, matched by the device and inode of its
ancestors, becomes `/mnt/host` followed by the tree's canonical path and the
rest. A relative path is taken from the host working directory. Symlinks are
resolved on the host first, before `..` is applied, because a symlink can point
somewhere the machine sees differently; a file that does not exist yet
resolves through its parent directory, which must exist. For `%u` and
`%U`, a `file:` URL (with no host, or `localhost`) is translated to a `file:`
URL of the machine path, a path is translated as a path, and a URL of any
other scheme is passed unchanged. `%f` and `%u` take at most one argument. A
file outside the shared trees, a `file:` URL of another host, or any file for
an isolated machine stops the command before anything runs, as an
untranslatable working directory does; nsl never passes a host path that
means something else in the machine.

By default the host entry declares no `MimeType`. A user entry with
`MimeType` ranks before the system's applications, so adding an application
would make a machine's viewer the host's default for its file types without
asking. Files reach the application when the user chooses it for them, for
example with `gio launch` or a file manager's list of all applications.

### File types (opt-in)

`nsl app add --mime-types` copies the guest entry's `MimeType` into the host
entry, so the application appears in the desktop's "Open With" lists for those
types. The user asks for it per application, knowing that the desktop can then
make it the default handler for a type that has no other user choice. The flag
is off by default for the reason above.

With the flag, nsl refuses the application, before it writes anything, when:

- the guest entry has no `MimeType`, or only empty items;
- the guest `Exec` has no `%f`, `%F`, `%u` or `%U` that nsl keeps, because the
  desktop would offer to open files with an application that cannot receive
  them;
- an item is not a media type `TYPE/SUBTYPE` whose parts are RFC 6838
  restricted names (a letter or digit, then up to 126 letters, digits or
  `!#$&^_.+-`), for example a parameter or a wildcard.

The host `MimeType` is the guest's items in order, without empty or repeated
items, each followed by `;`. nsl writes only its own entry: it does not edit
`mimeapps.list`, run `update-desktop-database` or write any cache. The flag is
not remembered. Each `nsl app add` writes the entry as its own flags say, so
adding the application again without `--mime-types` takes the types away, and
`nsl app remove` does too. `nsl app list` shows the number of types of each
entry, so a refresh that dropped them is visible.

### Icons

The icon comes from the guest entry's `Icon`: an absolute PNG or SVG path, or
a name. A name is looked up first in the `hicolor` theme under
`/usr/share/icons/hicolor`, scalable first and then the largest PNG, as
before. When `hicolor` has none, nsl lists the other themes under
`/usr/share/icons`, reads each one's `index.theme`, and looks in its
`Applications` directories: themes in name order, scalable directories first,
then fixed ones by size, largest first, SVG before PNG. Then
`/usr/share/pixmaps`. `ls -1d` with many candidate paths at once finds which
exist, in batches that fit a request, so the lookup is a few round trips
rather than one per candidate. nsl copies
the icon to `NSL_HOME/machines/NAME.apps/` and names that absolute path in the
entry. An entry without a found icon has no `Icon` key.

Ownership is the marker triple: a host entry is nsl's only when its markers
name this machine, its current ID and the application. nsl creates a new entry
without replacing any path (a hard link from a temporary file), replaces only
an entry it owns, and refuses every other existing file. `nsl app add` writes
under the machine lock and rechecks the machine's ID, so it cannot race
`nsl remove`.

## Consequences

- Machine applications appear in the host's menu and search with their own
  name, a machine suffix and, for most packaged applications, their icon.
- The change is host-only. The agent protocol, the VM image and the machine
  images are unchanged; reading uses `cat` and `ls` from coreutils or busybox,
  present in every machine image.
- `nsl app add` starts the machine, as `nsl run` does.
- The entry depends on the path of `nsl`. Moving the binary hides the entry
  through `TryExec`; `nsl app add` again rewrites it.
- An application started with files, for example with `gio launch`, opens
  the files from the shared trees at their machine paths. A
  file outside them makes the launch fail, and the reason is in the desktop
  session's journal.
- `run` without `--host-files` behaves as before, so entries written by an
  earlier nsl keep working; they pass no files until `nsl app add` refreshes
  them.
- A machine application never becomes the host's default handler by being
  added without `--mime-types`: the entry has no `MimeType`. With the flag, it
  appears in "Open With" lists and can become the default for a type the user
  has not chosen an application for; a default the user picks is in
  `mimeapps.list`, which nsl never edits, and a desktop skips it once the entry
  is gone.
- Refreshing an entry with `nsl app add` and without `--mime-types` drops its
  file types; `nsl app list` shows it.
- `nsl app list` has a `FILE TYPES` column, the number of `MimeType` items of
  each entry, `-` for none.
- Most icons come from `hicolor`; the theme lookup finds icons that an
  application or a distribution ships only in another theme, at the cost of a
  few more guest reads when `hicolor` has none.
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
- **Translating every absolute argument after `--`:** shorter, but it guesses
  which arguments are files; a fixed guest argument such as `/usr/share/x`
  would be translated too. The positions in `--host-files` are exact.
- **A separate command that reads the guest command from the host entry at
  launch:** no positions on the command line, but launching would depend on
  reading and trusting a file the user may have edited, and `run` would not
  gain the ability for other callers.
- **Copying `MimeType` by default:** puts the application in "Open With"
  lists without a flag, but it can take over the host's default handlers
  silently (see above). The opt-in flag keeps that a choice.
- **Remembering `--mime-types` in the entry:** a refresh would keep the types
  without the flag, but turning them off would need a second flag, and the
  command line would no longer say what the entry holds.
- **Writing the entry without types when the guest has none or cannot take
  files:** quieter, but the user asked for "Open With" and would not get it, or
  would get an entry that opens the application without the file. A refusal
  says why and changes nothing.
- **Validating only the characters of `MimeType`:** a looser check passes
  wildcards and parameters that desktops read differently; the media-type
  grammar is exact.
- **The host's current icon theme:** the host's theme says nothing about which
  themes the machine has, and changes would leave entries stale. A fixed order
  over the machine's own themes gives the same icon each time.
- **Automatic discovery of every application:** clutters the menu with every
  tool a package brings; explicit adding comes first, as the WSL2 plan says.

## References

- Contract: [CLI spec](../specs/cli.md#desktop-and-host-actions).
- Rationale: [ADR-0016](0016-wsl-style-machines.md) (desktop integration and
  host-file ownership), [ADR-0017](0017-shared-vm-and-machine-images.md).
- Plans: [WSL2 equivalent](https://github.com/frostyard/nsl/blob/e617966e80f6534aa6525f016def59ab58c1d864/docs/plans/wsl2-equivalent.md),
  [implementation plan](https://github.com/frostyard/nsl/blob/e617966e80f6534aa6525f016def59ab58c1d864/docs/plans/shared-vm-implementation.md).
- User guide: [Desktop applications](../../site/content/guides/desktop.md#add-an-application-to-the-menu).
