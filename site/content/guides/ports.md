---
description: Automatic forwarding of machine ports to host loopback, conflicts, and networking.
---

# Ports

Start a development server in a machine on port 5173, then open `http://127.0.0.1:5173/` in your host browser. nsl forwards the port automatically, usually within a second or so. This works for TCP ports 1024 through 65535, except 5353 and 5355.

## How forwarding works

A forwarder runs beside each VM as a user unit. Once a second it asks the VM's agent which TCP ports machines are listening on, and binds each one on host `127.0.0.1`:

| The machine listens on | The host forwards to |
| --- | --- |
| `127.0.0.1`, `0.0.0.0` or `::` | The VM's `127.0.0.1` |
| Only `::1`, such as a dev server bound to `localhost` | The VM's `[::1]` |

Forwarding binds only to host loopback. Other computers on your network can't reach these forwarded ports.

## See what is forwarded

```sh
nsl ports          # every machine
nsl ports web      # one machine
```

The output lists the machine for each port. A working forward shows `forwarded`; a failed one shows `conflict` and the error.

## Conflicts

- **A host program already uses the port.** The forwarder reports a conflict and retries. It never displaces an existing listener.
- **Two machines use the same port.** Machines share the VM's network namespace, so the second one cannot listen at all, as in WSL. Give one of them another port.

## Outbound traffic

Machines reach the network through the VM's user-mode networking and use the VM's resolver. They have no network configuration of their own.

[Isolated machines](isolated.md) have their own VM and forwarder, and their ports reach host loopback the same way.
