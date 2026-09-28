---
description: Automatic forwarding of machine ports to host loopback, conflicts, and networking.
---

# Ports

A server listening in a machine on port 1024 or above is reachable at the same port on host `127.0.0.1`. There is nothing to configure: a development server started in a machine on port 5173 answers at `http://127.0.0.1:5173/` in your host browser within a second or so.

## How forwarding works

A forwarder runs beside each VM as a user unit. Once a second it asks the VM's agent which TCP ports machines are listening on, and binds each one on host `127.0.0.1`:

| The machine listens on | The host forwards to |
| --- | --- |
| `127.0.0.1`, `0.0.0.0` or `::` | The VM's `127.0.0.1` |
| Only `::1`, such as a dev server bound to `localhost` | The VM's `[::1]` |

Ports 1024 to 65535 are forwarded, except 5353 and 5355. Forwarding binds host loopback only; nothing is exposed on your network.

## See what is forwarded

```sh
nsl ports          # every machine
nsl ports web      # one machine
```

Each port shows its machine and its state: `forwarded`, or `conflict` with the error.

## Conflicts

- **A host program already uses the port.** The forwarder reports a conflict and retries. It never displaces an existing listener.
- **Two machines use the same port.** Machines share the VM's network namespace, so the second one cannot listen at all, as in WSL. Give one of them another port.

## Outbound traffic

Machines reach the network through the VM's user-mode networking and use the VM's resolver. They have no network configuration of their own.

[Isolated machines](isolated.md) have their own VM and forwarder, and their ports reach host loopback the same way.
