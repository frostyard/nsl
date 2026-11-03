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

nsl reads that file, its icon and its command line from the machine, and writes its own entry in `~/.local/share/applications`. It copies the keys your desktop can honour for a machine application, and the command line runs `nsl run` in the machine. Without `-m NAME`, the default machine is used.

The entry runs nsl by its full path, the `nsl` on your `PATH`, so it works when your desktop session has a shorter `PATH`. If you move or reinstall nsl elsewhere, the entry disappears from the menu; run `nsl app add` again to refresh it.

Icons come from the machine's `hicolor` icon theme. An application with an icon in another theme gets no icon in your menu, but the entry still works.

A menu entry starts its application without files. Opening a document from the menu, or a web link, is later work; start such a command with `nsl run` for now.

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
