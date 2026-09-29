---
description: Reach a machine from VS Code or any SSH client with nsl ssh-config, without a network listener.
---

# Editors over SSH

Remote editors such as VS Code's Remote - SSH connect to a machine through an SSH host alias that nsl prints. Nothing listens on the network, in the machine or on the host.

Use this workflow to operate an editor on your host against a running machine, allowing you to use the machine's installed tools.

## Add the alias

```sh
nsl ssh-config dev >> ~/.ssh/config
```

This starts the machine if needed and prints an entry like this:

```text
Host nsl-dev
    HostName dev
    User you
    IdentityFile /var/home/you/.local/share/nsl/machines/dev.ssh/id_ed25519
    IdentitiesOnly yes
    UserKnownHostsFile /var/home/you/.local/share/nsl/machines/dev.ssh/known_hosts
    HostKeyAlias nsl-dev-…
    StrictHostKeyChecking accept-new
    ProxyCommand env NSL_HOME=/var/home/you/.local/share/nsl /usr/local/bin/nsl _ssh dev
```

Then connect to `nsl-dev`:

```sh
ssh nsl-dev
```

In VS Code, run **Remote-SSH: Connect to Host…** and pick `nsl-dev`.

## How it works

The proxy command, `nsl _ssh NAME`, starts the machine and asks the VM's agent to run `sshd -i` in it, with its standard input and output as the connection. The key is generated for that machine and lives with its host key in `NSL_HOME/machines/NAME.ssh/`. Both are removed with the machine.

Editor sessions count as nsl command sessions, so a machine stays running while an editor is connected.

## Keep watched projects in the guest home

Host edits to files under `/mnt/host` produce no file events in the machine. Language servers, file watchers and hot reload work best on a checkout in the guest home, such as `/home/you/src/project`, opened over this SSH connection.
