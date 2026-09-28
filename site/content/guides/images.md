---
description: The signed catalogue, machine images, VM image updates, offline use and how images are verified.
---

# Images and updates

nsl uses two kinds of image, both published by Frostyard:

- **The VM image**: the small Debian 13 system every nsl VM boots. It holds no user state and is replaced whole.
- **Machine images**: distro root filesystems that become machines. See the [list](../reference/machine-images.md).

## Browse and cache

```sh
nsl images                 # machine images in the catalogue, and the VM image in effect
nsl pull ubuntu:26.04      # verify and cache one without creating a machine
```

`create --distro` pulls what it needs, including the VM image the first time.

## Update the VM

```sh
nsl update
```

`update` verifies the catalogue's current VM image, caches it and selects it for the next start of each nsl VM, isolated ones included. It never touches a running VM. `nsl list` and `nsl config` show the pending image until then. The new image replaces the VM's root at its next start; the data disk, the machines and the VM's keys stay.

To apply it now, stop everything and let the next command start the VM:

```sh
nsl shutdown
nsl
```

## Update machines

Machines update through their own package managers, like any installed distro. A newer machine image affects machines created after it, never existing ones.

## Offline use

`--offline` on `images`, `pull`, `create --distro` and `update` uses only the verified cache and never downloads. It still re-verifies signatures and digests, and needs a catalogue that has not expired.

## Pin an exact image

A selector can carry the OCI manifest digest of one image:

```sh
nsl create pinned --distro debian:13@sha256:HEX
```

The digest must still be in the current catalogue and not revoked.

## How images are verified

Images are signed with Sigstore by the image workflow in this repository, and published to `ghcr.io/frostyard/nsl-images`. Before using a catalogue or an image, nsl checks:

- that the signing certificate was issued by GitHub Actions to exactly `https://github.com/frostyard/nsl/.github/workflows/images.yml@refs/heads/main`;
- the certificate transparency, Rekor inclusion and timestamp against the Sigstore public-good root compiled into nsl;
- that the catalogue has not expired, is valid for at most 30 days, and is not older than the newest one this host has seen;
- every file's digest and size, and bounded decompression, before anything enters the cache.

No flag or environment variable turns these checks off. Images are rebuilt at least weekly for security updates, and a withdrawn image is listed as revoked in a newer catalogue.

## Local images

Image developers can select files they built:

```sh
nsl create test --image nsl-machine-debian-trixie-x86-64-r4.tar.zst --digest sha256:HEX
nsl update --image nsl-vm-trixie-x86-64-r9.raw --digest sha256:HEX
```

The digest proves the bytes are the ones you meant; it does not prove who built them. See [build from source](../contributing/build.md).
