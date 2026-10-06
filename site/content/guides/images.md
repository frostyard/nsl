---
description: The signed catalogue, machine images, VM image updates, offline use and how images are verified.
---

# Images and updates

Frostyard publishes two kinds of image for nsl. They have different jobs:

- **The VM image**: the small Debian 13 system every nsl VM boots. It holds no user state, so updates replace it as a whole.
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

`update` verifies and caches the current VM image from the catalogue, then selects it for every nsl VM, including isolated ones. Running VMs keep using their current image. At the next start, the new image replaces the VM's root; the data disk, machines and VM keys stay in place. Until then, `nsl list` and `nsl config` show the pending image.

To apply it now, stop everything and let the next command start the VM:

```sh
nsl shutdown
nsl
```

## Update machines

Update packages inside a machine with its own package manager, just as you would on an installed distro. Downloading a newer machine image only changes what you'll get when you create a new machine. Existing machines keep their installed software.

## Offline use

Add `--offline` to `images`, `pull`, `create --distro` or `update` to use the verified cache without downloading anything. Signature and digest checks still run, and the cached catalogue must not have expired.

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

Image developers can select local files with `nsl create NAME --image FILE --digest sha256:HEX` or `nsl update --image FILE --digest sha256:HEX`. The [build walkthrough](../contributing/build.md#the-images) shows how to build both images, select the output paths and calculate their digests.

The digest checks that the file matches the bytes you selected. It can't tell you who built the image.
