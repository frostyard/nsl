---
description: Wayland windows from machines through Waypipe, and opening links and files on the host with nsl-open.
---

# Desktop applications

You can install a Wayland application in a machine and use it on your host desktop. When the machine asks to open a web link or shared file, your host's default application handles it.

## Requirements

- A Wayland session on the host.
- Waypipe on the host. nsl looks for `waypipe` on your `PATH`; set `NSL_WAYPIPE` to use another path.
- A machine that is not [isolated](isolated.md).

`nsl doctor` shows whether Waypipe was found.

## Run an application

Install it with the machine's package manager and run it:

```sh
nsl run sudo apt-get update
nsl run sudo apt-get install -y galculator  # pick any GUI application
nsl run galculator
```

You can also start applications from a shell inside the machine. A Wayland-compatible editor runs there with the machine's tools and opens its window on your desktop.

## Add an application to the menu

Put an application in your host's menu, so you can start it without a terminal:

```sh
nsl app add org.gnome.Calculator
```

Your desktop's application menu and search now show `Calculator (debian)`: the application's own name, and the machine it runs in. `nsl app add` starts the machine first, as `nsl run` does.

```sh
nsl app list
```

```
MACHINE  APP                   NAME
debian   org.gnome.Calculator  Calculator (debian)
```

Remove an entry with `nsl app remove`:

```sh
nsl app remove org.gnome.Calculator
```

APP is the application's desktop-file ID, without `.desktop`. To find it, list the machine's entries:

```sh
nsl run ls /usr/share/applications
```

nsl reads that file, its icon and its command line from the machine, and writes its own entry in `~/.local/share/applications` (`$XDG_DATA_HOME/applications` when you set `XDG_DATA_HOME`). It copies the keys your desktop can honour for a machine application, and the command line runs `nsl run` in the machine. Without `-m NAME`, `nsl app add` and `nsl app remove` use the default machine, and `nsl app list` lists the entries of every machine.

The entry runs nsl by its full path, the `nsl` on your `PATH`, so it works when your desktop session has a shorter `PATH`. If you move or reinstall nsl elsewhere, the entry disappears from the menu; run `nsl app add` again to refresh it.

The icon is the PNG or SVG file the application's entry names by its full path, or an icon of that name in the machine's icon themes: `hicolor` first, then the other themes in `/usr/share/icons` (such as `Adwaita` or `breeze`), then `/usr/share/pixmaps`. An application with no icon in any of them gets no icon in your menu, but the entry still works.

### Open files with a machine application

When the application takes files or links, its entry takes them too. Start it with `gio launch`, or choose it in a file manager that lets you open a file with any application:

```sh
gio launch ~/.local/share/applications/nsl-debian-org.gnome.Evince.desktop ~/Documents/report.pdf
```

nsl translates each file to its path in the machine, as it does for the working directory of `nsl run`: `~/Documents/report.pdf` opens as `/mnt/host/var/home/you/Documents/report.pdf`. Links (`https://…`) pass unchanged, and a `file://` link becomes a link to the machine path.

Only files in the trees the machine shares, your home, `/run/media/USER` and `/mnt`, can be opened. A file anywhere else stops the launch, and nothing runs; the reason is in your desktop's journal. The same works from a terminal:

```sh
nsl run --host-files F:1:0 -- evince ~/Documents/report.pdf
```

`F:1:0` says that the arguments after the first one (the command) are host files. See the [CLI reference](../reference/cli.md) for the other forms.

The entry does not register the application for any file type, so adding it never changes which application your desktop uses by default. Entries added by an earlier nsl pass no files; run `nsl app add` again to refresh them.

`nsl app remove` deletes the entry and its icon. `nsl remove NAME --yes` deletes the entries of that machine too, and the preview names how many. Isolated machines have no host desktop session, so they have no menu entries.

nsl never replaces a file it did not write: an entry of that name that another program created makes `nsl app add` stop and say so.

## The desktop session

When you enter or start a machine from a Wayland session, nsl starts its desktop session too, if needed. The user unit `nsl-UID-vm-ID-desktop-NAME.service` runs Waypipe between your desktop and the machine. It restarts after a failure, stays up when the machine stops, and ends when the VM stops.

Commands in the machine receive:

| Variable           | Value                                                  |
| ------------------ | ------------------------------------------------------ |
| `WAYLAND_DISPLAY`  | The machine's Waypipe socket                           |
| `XDG_SESSION_TYPE` | `wayland`, so Chromium, Electron and Qt choose Wayland |
| `BROWSER`          | `nsl-open`                                             |

Machines have no X server. Applications that support only X11 do not open windows.

A machine with a connected window is not idle, so it keeps running while the window is open.

## Open links and files on the host

```sh
nsl-open https://github.com/frostyard/nsl
nsl-open /mnt/host/var/home/you/report.pdf   # any path under /mnt/host
nsl-open report.pdf                          # relative, from a directory under /mnt/host
```

`nsl-open` asks the host to open an `http` or `https` URL, or a path under `/mnt/host`, with your default handler. It is the machine's `BROWSER` and its handler for web links, so tools that open a browser open yours.

A broker on the host handles these requests for each machine. It accepts only web URLs and `/mnt/host` paths that resolve to files in the shared trees. Everything else is refused. This keeps requests within the allowed paths, but remember that an ordinary machine can already write your files.

## Troubleshooting

- `nsl logs NAME` shows the desktop session's recent logs.
- A command started outside a Wayland session, such as over SSH, does not start a desktop session. The next command from your desktop does.
