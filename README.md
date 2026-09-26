# nsl — NSpawn Subsystem for Linux

`nsl` v0.1.0 is a small Linux CLI for persistent Debian development environments backed by [nspawn.org](https://nspawn.org). It runs package managers in a booted Debian machine while leaving an atomic host's `/usr` untouched. One binary, Go standard library, no additional daemon or database. Work as your own UID; ask explicitly for root. Maintainer architecture and decisions: [docs/README.md](docs/README.md). Licensed under [MIT](LICENSE).

## Requirements

- Linux with systemd-nspawn and a working nspawn.org **1.5.1** service (`nspawn ps -a`). Tested on Snow Linux, systemd 261 and nspawn.org 1.5.1.
- A non-root local user whose login name starts with a lowercase letter or `_` and then contains lowercase letters, digits, `_` or `-`; numeric UID and GID available inside Debian. The CLI currently uses `sudo -n nspawn` for management, so passwordless sudo must already be configured. It does **not** edit sudoers. `NSL_NSPAWN=/usr/local/bin/nspawn` chooses the installed binary if `nspawn` is not on sudo's PATH.
- Go 1.23+ to build; no Go runtime needed afterwards.
- An active Wayland login session for `gui`. No X11/audio/portals/GPU integration in v0.1.

## Build

```sh
make ci
make build
./build/nsl version
```

Put the resulting binary in a directory on your PATH if desired (for example `~/.local/bin`). Tagged releases publish Linux amd64/arm64 archives and checksums through GoReleaser Pro (the CI uses the Frostyard org secret). Do not install packages on the atomic host for this tool; only `nspawn` must already be functional.

## Workflows

```sh
nsl new debian                      # signed Debian 13 base, separate persistent machine
nsl ls
nsl run debian -- id                # your host's numeric UID/GID and guest user name
nsl run debian --root -- apt-get update
nsl run debian --root -- apt-get install -y build-essential
nsl enter debian                    # interactive guest shell as your user
nsl stop debian                     # stop first before changing mounts
nsl in debian ~/projects/myapp     # bind only this project at /work; interactive shell
nsl in debian ~/projects/myapp -- make
nsl stop debian
nsl gui debian -- galculator        # attach only current Wayland socket; returns on app exit
nsl stop debian                     # also removes saved session socket mount
```

The guest home `/home/<host-login>` persists in the machine; host `$HOME` is **never** mounted. For source trees, `in` canonicalizes and mounts one directory at `/work`; its idmapped bind mount preserves host file ownership when commands run as your UID. Explicit root commands from `run --root` can still create root-owned project files if manually given project access; normal project workflow is non-root. No host files are copied into the machine by `new`.

Mounts are mutually exclusive for v0.1. `enter` and `run` need an unmounted machine, `in` needs the same project mount, and `gui` needs the same Wayland socket mount. If already running with another mount, `nsl` refuses to switch: `nsl stop NAME` clears saved mounts, then try again. A GUI app uses a guest-owned private runtime directory for its socket, not the host's runtime directory. The socket is tied to the host's login; stop before logout so that a future session gets a fresh socket. The GUI command stays attached until the app exits, while the machine keeps running until stopped.

Machines are named `nsl-NAME` and labeled with the owner's UID. `nsl` lists/operates only its own labeled machines and never overwrites existing machines. The shared base image `nsl-base-debian-13` is pulled with signature verification. Inspect or manage everything directly with `sudo nspawn inspect nsl-NAME` or `sudo machinectl status nsl-NAME`.

## Boundaries and current limitations

- This is a system container sharing the host kernel, **not a VM or a sandbox for hostile packages**. GUI apps can interact with your Wayland compositor. Only grant graphical access to trusted applications.
- This initial release targets one user per environment and Debian 13 only. There is no automatic host-wide packaging, environment deletion, orchestration, command export, project-specific machines, or conflict resolution for concurrent projects. `nsl stop` stops all processes inside that machine.
- Existing numeric UID/GID collisions in the image, or unusual usernames, may cause `new` to fail during guest bootstrap. The partially created machine is deliberately retained for inspection, not automatically deleted.
- `nsl new` checks the base image's hub reference and signature metadata before cloning, but does not cryptographically re-verify a previously pulled local base on every run. Trust the local root-controlled nspawn store.
- This CLI uses nspawn's `exec` to run commands; the guest shell is `/bin/sh`. No terminal integration or guest systemd user session is set up. Apps needing desktop portals, audio, session D-Bus, GPU devices or SSH agent may need future explicit features.
- No `rm` subcommand: deletion is intentionally manual (`nsl stop NAME`, then `sudo nspawn rm nsl-NAME`) to avoid accidental loss of the persistent guest home. To remove the shared base too, ensure no environments still depend on it.

## Verification performed on framework

Unit tests (including `-race`) and `go vet` passed. Created `nsl-dev` with the final v0.1 binary; confirmed guest UID 1000, a real home, the bootstrap marker and a project mount showing host UID 1000; stopped with empty saved volumes. Earlier test machines `nsl-smoke` and `nsl-gui-smoke` also remain stopped with empty saved volume lists. `nsl-gui-smoke` has `galculator` installed and its attached process ran for a five-second smoke test using the Wayland mount (which is then cleared). The existing `debian-dev-test` machine was not modified by `nsl`.
