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
