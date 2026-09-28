---
description: Write a stopped machine to an archive, and create a machine from one.
---

# Export and import

An archive holds a whole machine: its root filesystem, packages, services and guest home. Use archives for backups, and to move a machine to another host or another tier.

## Export

```sh
nsl stop dev
nsl export dev ~/backups/dev-2026-09-28.tar
```

The machine must be stopped. nsl writes a new file readable only by you, and refuses a path that already exists. The machine itself is unchanged.

## Import

```sh
nsl import dev2 ~/backups/dev-2026-09-28.tar
nsl import risky ~/downloads/risky.tar --isolated
```

The name must be unused. Before anything is published, nsl checks:

- that the archive is for x86-64 and its account has your UID and primary GID;
- the format version, names, sizes and checksum;
- every entry of the root filesystem: no absolute or `..` paths, no links that leave the tree, no duplicates, nothing unexpected after the end.

An archive that fails any check leaves no machine behind. The imported machine gets this host's time zone, the new hostname, a `sudo` rule and its own nspawn settings. The first machine imported into an empty nsl becomes the default, as with `create`.

The trust tier comes from the command line, defaulting to an ordinary machine. It is never read from the archive.

## What is in an archive

An archive is a tar file with two entries:

| Entry | Content |
| --- | --- |
| `manifest.json` | Format version, architecture, machine name, account, image build, creation time, and the digest and size of the root filesystem |
| `rootfs.tar.zst` | The root filesystem, with numeric owners, modes, extended attributes and ACLs |

!!! warning "Archives are not encrypted"

    An archive can contain SSH keys, tokens and anything else in the machine. Store it as you would those secrets. Its checksum detects damage; it does not prove where the archive came from. Import only archives you trust, or import them with `--isolated`.
