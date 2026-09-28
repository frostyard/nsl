---
description: Wayland windows from machines through Waypipe, and opening links and files on the host with nsl-open.
---

# Desktop applications

Wayland applications in a machine open windows on your desktop. Links and shared files opened in a machine open with your host's handlers.

## Requirements

- A Wayland session on the host.
- Waypipe on the host. nsl looks for `waypipe` on your `PATH`; set `NSL_WAYPIPE` to use another path.
- A machine that is not [isolated](isolated.md).

`nsl doctor` shows whether Waypipe was found.

## Run an application

Install it with the machine's package manager and run it:

```sh
nsl run sudo apt-get update
nsl run sudo apt-get install -y galculator
nsl run galculator
```

Applications started from a shell in the machine work the same way.

## The desktop session

When a command enters or starts a machine from a Wayland session, nsl starts that machine's desktop session if it is not running: a user unit, `nsl-UID-vm-ID-desktop-NAME.service`, that runs Waypipe between your desktop and the machine. The session restarts after a failure, outlives a stopped machine and ends with the VM.

Commands in the machine receive:

| Variable | Value |
| --- | --- |
| `WAYLAND_DISPLAY` | The machine's Waypipe socket |
| `XDG_SESSION_TYPE` | `wayland`, so Chromium, Electron and Qt choose Wayland |
| `BROWSER` | `nsl-open` |

Machines have no X server. Applications that support only X11 do not open windows.

A machine with a connected window is not idle, so it keeps running while the window is open.

## Open links and files on the host

```sh
nsl-open https://github.com/frostyard/nsl
nsl-open /mnt/host/var/home/you/report.pdf   # any path under /mnt/host
nsl-open report.pdf                          # relative, from a directory under /mnt/host
```

`nsl-open` asks the host to open an `http` or `https` URL, or a path under `/mnt/host`, with your default handler. It is the machine's `BROWSER` and its handler for web links, so tools that open a browser open yours.

The host side is a per-machine broker. It accepts only web URLs and `/mnt/host` paths whose host paths lie in the shared trees, and refuses every other target. These checks keep a machine from naming host files outside the shares. They are not a security boundary: a machine can already write your files.

## Troubleshooting

- `nsl logs NAME` shows the desktop session's recent logs.
- A command started outside a Wayland session, such as over SSH, does not start a desktop session. The next command from your desktop does.
