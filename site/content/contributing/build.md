---
description: Build nsl and its images from source, run the tests, and edit this site.
---

# Build from source

## The CLI

You'll need Go 1.25.8 or newer to build nsl. To run the checks, install the golangci-lint version pinned in `mise.toml`. [mise ↗](https://mise.jdx.dev) installs it and verifies it against `mise.lock`.

```sh
mise install  # the pinned golangci-lint and svu
make build    # build/nsl
make check    # format, then verify
make verify   # tidiness, license notices, vet, formatting, lint and tests; changes nothing
make ci       # what CI runs: verify, race-enabled tests with coverage, cross builds
```

You can run the unit tests without root or a VM. They use fake tools and local processes.

## The images

The image builder runs mkosi inside a disposable Lima VM using pinned [nspawn/mkosi-definitions ↗](https://github.com/nspawn/mkosi-definitions) recipes. Build dependencies stay in that VM. On the host, you'll need Lima 2.2.0, Python 3, Git, Go and `flock`.

```sh
make build
./scripts/bootstrap-poc.sh --waypipe   # optional: pinned Lima and Waypipe under build/poc
source build/poc/env.sh

# The VM image.
scripts/build-image.sh --role vm
image=build/image/share/nsl-vm-trixie-x86-64-r9.raw
build/nsl update --image "$image" --digest "sha256:$(sha256sum "$image" | cut -d' ' -f1)"
build/nsl recover

# A machine image, and a machine from it.
scripts/build-image.sh --role machine --distribution debian
machine=build/image/share/nsl-machine-debian-trixie-x86-64-r4.tar.zst
build/nsl create debian --image "$machine" --digest "sha256:$(sha256sum "$machine" | cut -d' ' -f1)"
```

`--distribution` also takes `ubuntu`, `fedora`, `centos`, `arch` and `opensuse`; `--distribution opensuse --release 16.0` builds Leap. The [image build notes ↗](https://github.com/frostyard/nsl/blob/main/image/README.md) describe the layers and the builder.

## Accept images in disposable VMs

```sh
python3 scripts/probe-vm.py --nsl build/nsl --image "$image" --evidence build/image/evidence/probe.json
python3 scripts/probe-machines.py --nsl build/nsl --vm-image "$image" --machine-image "$machine" \
  --evidence build/image/evidence/machines.json
```

The probes boot VMs in private state directories and remove them afterwards.

## This site

The site lives in `site/`: pages in `site/content/`, configuration in `site/properdocs.yml`. It is built with [ProperDocs ↗](https://properdocs.org) and the [MaterialX ↗](https://jaywhj.github.io/mkdocs-materialx/) theme, and published to GitHub Pages from `main`.

```sh
make site-serve   # preview at http://127.0.0.1:8000/nsl/
make site         # build into site/dist, failing on any warning
```

Both targets install the pinned tools into `build/site-venv` first. When you change how nsl behaves, update the relevant pages in the same pull request.

## Design documents

Decisions, designs, contracts and plans live in [`docs/` ↗](https://github.com/frostyard/nsl/tree/main/docs). Start at the [documentation index ↗](https://github.com/frostyard/nsl/blob/main/docs/README.md).
