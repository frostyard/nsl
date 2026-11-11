---
description: Build nsl and its images from source, run the tests, and edit this site.
---

# Build from source

## The CLI

You'll need Go 1.27.4 or newer to build nsl. To run the checks, install the golangci-lint version pinned in `mise.toml`. [mise ↗](https://mise.jdx.dev) installs it and verifies it against `mise.lock`. It also installs the pinned Node, which `make verify` needs for the template gate.

```sh
mise install  # the pinned golangci-lint, svu and Node
make build    # build/nsl
make check    # format, then verify
make verify   # tidiness, license notices, vet, formatting, lint, the template gate and tests; changes nothing
make ci       # what CI runs: verify, race-enabled tests with coverage, cross builds
```

You can run the unit tests without root or a VM. They use fake tools and local processes.

### Releases and the Homebrew tap

`make bump` tags the next stable version from a clean, verified `main`.
The release workflow uses GoReleaser Pro to publish the Linux archives and
generate `Casks/nsl.rb` for
[`frostyard/homebrew-tap` ↗](https://github.com/frostyard/homebrew-tap).
GoReleaser commits the cask to a `bump/nsl-VERSION` branch of the tap and opens
a pull request into the tap's `main`; the tap serves the new release once a
maintainer merges it. GoReleaser only warns when GitHub refuses the pull request,
so check the tap for it after each release. Snapshots and prerelease tags do not
update the stable cask.
Both workflows pin GoReleaser Pro to one exact version. The
release-config job renders the cask in snapshot mode, and
`scripts/check-cask.py` refuses a cask that Homebrew's
`Cask/ZapNotRequired` cop refuses. Bump the exact pin in both
workflows together.

The workflow needs the `GORELEASER_KEY` and `HOMEBREW_TAP_TOKEN` secrets.
`HOMEBREW_TAP_TOKEN` is a fine-grained token for the tap only, with Contents and
Pull requests write access; the workflow's `GITHUB_TOKEN` only publishes this
repository's release and provenance. Missing tap credentials fail the workflow
before publication.

Archive names carry no version, such as `nsl_linux_amd64.tar.gz`, because the
[install guide](../getting-started/install.md#download-a-release) downloads
them through `releases/latest/download/`. Keep them stable.

Run `make release-check` with GoReleaser Pro when editing `.goreleaser.yaml`.
The release-config CI job runs the same Pro configuration check.
A Dependabot or fork pull request does not get `GORELEASER_KEY`, so the job
skips the check there with a notice; the merge-group and main runs check it.
See [ADR-0020 ↗](https://github.com/frostyard/nsl/blob/main/docs/adr/0020-homebrew-tap-publication.md)
for the publication decision.

## The images

The image builder runs mkosi inside a disposable Lima VM using pinned [nspawn/mkosi-definitions ↗](https://github.com/nspawn/mkosi-definitions) recipes. Build dependencies stay in that VM. On the host, you'll need Lima 2.2.0, Python 3, Git, Go and `flock`.

```sh
make build
./scripts/bootstrap-poc.sh --waypipe   # optional: pinned Lima and Waypipe under build/poc
source build/poc/env.sh

# The VM image.
scripts/build-image.sh --role vm
image=build/image/share/nsl-vm-trixie-x86-64-r11.raw
build/nsl update --image "$image" --digest "sha256:$(sha256sum "$image" | cut -d' ' -f1)"
build/nsl recover

# A machine image, and a machine from it.
scripts/build-image.sh --role machine --distribution debian
machine=build/image/share/nsl-machine-debian-trixie-x86-64-r6.tar.zst
build/nsl create debian --image "$machine" --digest "sha256:$(sha256sum "$machine" | cut -d' ' -f1)"
```

Each build prints a `Built PATH` line. Use that path for `image` or `machine` if the profile revision differs from these examples.

`--distribution` also takes `ubuntu`, `fedora`, `centos`, `arch`, `opensuse` and `azure`; `--distribution opensuse --release 16.0` builds Leap, and `--release` also picks Debian `testing`, Fedora `rawhide` and Ubuntu `noble`. The [image build notes ↗](https://github.com/frostyard/nsl/blob/main/image/README.md) describe the layers and the builder.

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
