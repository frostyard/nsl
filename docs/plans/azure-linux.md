# Plan: Azure Linux 4.0 machine image

**Status: Phase 1 complete, 2026-10-08. Phase 2 waits for a publication run.**

Add Azure Linux 4.0, Microsoft's Fedora-based cloud distribution, as a machine image in the `rpm` family. It joins the catalogue only after it passes the [machine-image acceptance](../specs/machine-images.md#acceptance), under [ADR-0009](../adr/0009-distribution-neutral-guest-contract.md).

Azure Linux 4.0 is a beta. Microsoft publishes it only under `packages.microsoft.com/azurelinux/4.0/beta/`, with `base`, `microsoft`, `nvidia` and `sdk` repositories; the image's own repository files enable `base` and `microsoft`. Packages and repository metadata are signed with the Mariner release key (`0CD9FED33135CE90`), the key mkosi already trusts for Azure Linux 3.0 through `distribution-gpg-keys`.

## Phase 1 — Profile and acceptance

- **Profile.** `image/machines/profiles/azurelinux/`, distribution `azure` (mkosi's name) and release `4.0`, in the `rpm` family with the Fedora 44 tools tree. Every rpm-family adapter package is in the beta `base` repository.
  - mkosi v27's Azure Linux support builds `4.0/prod/REPO/ARCH` URLs, which do not exist for 4.0. `LocalMirror=` reads `4.0/beta/base/x86_64/` for the build and leaves the image's repository files to the distro.
  - The pinned recipes (`68263d0`) have no Azure Linux recipe, so the profile names what Fedora's recipe adds that 4.0 packages: `dnf5`, `dbus-broker`, `glibc-langpack-en`, `iproute`, `iputils`, `pam`, `util-linux` and `vim-minimal`. A post-install script links `/etc/resolv.conf` to `/run/systemd/resolve/stub-resolv.conf`, as every recipe does.
  - `azurelinux-release-common` requires `azurelinux-repos(4.0)`, which both `azurelinux-repos` and `azurelinux-repos-dev` provide. The dev package points dnf at an internal development feed with `gpgcheck=0`, so the profile names `azurelinux-repos`.
- **Probe.** Azure Linux 4.0 packages no `wayland-utils`, and its enabled repositories have none of galculator, zenity or foot. On Azure Linux, `probe-machines.py` lists the compositor's globals with a Python client of the Wayland protocol, and opens `gtk-lshw` from `lshw-gui`, a GTK 3 window that waits for an answer when run without `pkexec`.
- **Done when:** the image builds, and every check of `probe-machines.py --gui` passes on it beside a Debian peer with VM image r10.

**Result, 2026-10-08: complete.**

| Image | systemd | Size (zstd) | Entry latency, median |
| --- | --- | --- | --- |
| `nsl-machine-azure-4.0-x86-64-r1` | 258.4 | 71 MB | 62 ms |

- **Build.** The image built on the first attempt in a fresh builder, from `azurelinux-release` 4.0-26 and 138 packages, with the signed `azurelinux-repos` and the basic release identity. Its os-release reads `Azure Linux 4.0 (Four Beta)`. It is the smallest image in the catalogue.
- **Acceptance** (`azure-probe-1.json`, VM image r10 `511a0431…` built from this branch, `--gui`): an Azure Linux machine and the published Debian r5 image ran 24 checks, and all passed. On Azure Linux, systemd reached `running` with no failed units, every tally check passed, and the packages check installed Python, jq, Podman 5.8 and `lshw-gui` from the beta repository in 44 s. The Python client found 33 globals, including `wl_compositor` and `xdg_wm_base`, and `gtk-lshw` held a window open until the timeout. Creation took 12 s and the first start 1.4 s.

## Phase 2 — Publication

- Add the selectors `azurelinux:4.0` and `azure-linux:4.0` to the publisher, and list the image as a beta in the user documentation.
- **Done when:** a signed catalogue lists the image, and `nsl create` works from `azurelinux:4.0` on a host with an empty cache.

## Later / ideas

- Move to Microsoft's production repositories when Azure Linux 4.0 leaves beta. The image's `azurelinux-repos` follows the distro; the profile's `LocalMirror=` changes with it, or goes once mkosi builds the right URLs for 4.0.
- An Azure Linux recipe upstream in `nspawn/mkosi-definitions` would replace the profile's package list and post-install script.

## Open questions

- Should the probe list Wayland globals with its Python client for every image, and drop `wayland-utils` from the workload? It stays limited to Azure Linux until another distro lacks `wayland-info`.

## References

- Implements: [machine images](../specs/machine-images.md), [image publication](../design/image-publication.md), [ADR-0009](../adr/0009-distribution-neutral-guest-contract.md).
- Follows: [Ubuntu, CentOS Stream and openSUSE Leap machine images](more-machine-images.md). Followed by: [Debian testing, Fedora Rawhide and Ubuntu 24.04](more-releases.md).
