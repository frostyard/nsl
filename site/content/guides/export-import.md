---
description: Save a stopped machine to an archive, and create a machine from one.
---

# Export and import

Export a machine to save its root filesystem, packages, services and guest home in one archive. You can use it as a backup, move it to another host, or import it with a different isolation tier.

## Export

```sh
nsl stop dev
nsl export dev ~/backups/dev-2026-09-28.tar
```

The machine must be stopped. `nsl` saves a new file readable only by you, and refuses to write a path that already exists. The machine itself is unchanged.

## Import

```sh
nsl import dev2 ~/backups/dev-2026-09-28.tar
nsl import risky ~/downloads/risky.tar --isolated
```

Choose a name that isn't already in use. Before making the imported machine available, nsl checks:

- that the archive matches your host architecture and its account has your UID and primary GID;
- the format version, names, sizes and checksum;
- every entry of the root filesystem: no absolute or `..` paths, no links that leave the tree, no duplicates, nothing unexpected after the end. These checks are best effort; they cannot make an untrusted archive safe.

`nsl` refuses to import an archive that fails any check. The imported machine gets this host's time zone, the new hostname, a `sudo` rule and its own nspawn settings. If this is your first machine, it becomes the default, just as it would with `create`.

If the connection fails during import and cleanup cannot be confirmed, the name stays reserved and `nsl list` shows an incomplete machine. Run `nsl remove NAME --yes` to retry cleanup before importing again.

Import creates an ordinary machine unless you pass `--isolated`. The archive doesn't choose its own trust tier.

## What is in an archive

An archive is a tar file with two entries:

| Entry            | Content                                                                                                                         |
| ---------------- | ------------------------------------------------------------------------------------------------------------------------------- |
| `manifest.json`  | Format version, architecture, machine name, account, image build, creation time, and the digest and size of the root filesystem |
| `rootfs.tar.zst` | The root filesystem, with numeric owners, modes, extended attributes and ACLs                                                   |

!!! warning "Archives are not encrypted"

    An archive can contain SSH keys, tokens and anything else in the machine. Store it as you would those secrets. Its checksum detects damage; it does not prove where the archive came from. Import only archives you trust, or import them with `--isolated`.
