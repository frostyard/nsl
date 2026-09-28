# Spec: nsl agent protocol

Contract between the host CLI and the nsl agent, which runs in every nsl VM and acts on its machines under [ADR-0017](../adr/0017-shared-vm-and-machine-images.md). Both ends are built from this repository. The protocol version detects a host and VM built from different sources; a mismatch is rejected, never negotiated.

Callers: the [CLI](cli.md). Host: the [VM image](vm-image.md). Targets: [machine images](machine-images.md).

## Interface

### Transport

- The host reaches the VM over vsock SSH ([ADR-0011](../adr/0011-image-profiles-and-portable-vsock.md)) as VM `root`, with that VM's client key and pinned host key.
- The VM's sshd forces `/usr/lib/nsl/nsl-agent` for every session of that key and permits no password or interactive login. The key allows local TCP forwarding to VM loopback (`permitopen="127.0.0.1:*"`) and remote Unix-socket forwarding, which the host places under `/run/nsl/` for Waypipe. sshd grants a remote Unix-socket forward only when `AllowTcpForwarding` permits remote forwarding too. It allows no X11 or agent forwarding. The key is VM root in any case; the limits keep mistakes narrow rather than draw a trust boundary.
- The host sends exactly one request per SSH session as the session's command string. The agent reads it from `SSH_ORIGINAL_COMMAND`.
- The session's stdin, stdout and stderr belong to the operation: a command's streams for `run`, JSON results for the others. The SSH exit status is the result.

### Request

A request is the standard padded base64 encoding of one UTF-8 JSON object of at most 64 KiB. Duplicate keys, unknown fields, missing required fields and trailing data are rejected.

| Field | Type | Operations | Meaning |
| --- | --- | --- | --- |
| `protocol` | integer | all | Agent protocol version. MUST equal the agent's, which is `1`. |
| `op` | string | all | One of the operations below. |
| `machine` | string | machine operations | Machine name, as in the [CLI](cli.md#interface). |
| `id` | string | machine operations | The machine's 32 lowercase hex ID recorded by the host. |
| `argv` | array of strings | `run`, `vm` | At least one argument; no NUL bytes. Passed literally. |
| `directory` | string | `run` | Absolute path in the machine, or empty for the user's home (`/root` with `root`). |
| `root` | boolean | `run` | Run as machine root instead of the machine account. |
| `tty` | boolean | `run` | Run on a PTY. Requires a terminal on the session's stdin. |
| `env` | object | `run` | Environment from the host. Names are `TERM`, `COLORTERM`, `LANG`, `LANGUAGE` or `LC_` followed by capital letters; values are at most 256 bytes without NUL or newline. Any other name is rejected. |
| `idle_timeout` | integer | `start`, `run` | Minutes, 0–1440, from the [configuration](cli.md#configuration). The VM's idle monitor uses the latest value it receives. |
| `account` | object | `create`, `import` | `user`, `group`, `uid`, `gid` of the host user, as in [account rules](cli.md#account-and-execution). |
| `image` | object | `create` | `path` under the read-only image share, `digest` and `size` of the compressed root filesystem, and `build_id`. An empty `build_id` accepts the image's own; otherwise the descriptor must match it. |
| `time_zone` | string | `create`, `import` | The host's IANA zone name, or `Etc/UTC`. |

Example `run` request, before base64 encoding:

```json
{"protocol": 1, "op": "run", "machine": "debian", "id": "4f0c6a1e9b2d4c7f8e3a5b6c7d8e9f01",
 "argv": ["printf", "%s\\n", "$HOME", "with space"], "directory": "/mnt/host/var/home/bjk/projects",
 "root": false, "tty": false, "env": {"TERM": "xterm-256color", "LANG": "en_US.UTF-8"}, "idle_timeout": 15}
```

### Operations

| Operation | Input | Output on success |
| --- | --- | --- |
| `identity` | none | JSON: `protocol`, `vm` (the binding: `version`, `id`, `role`, `uid`, `gid`, and `machine` in an isolated VM) and `image`, the VM descriptor from [`/usr/lib/nsl/image.json`](vm-image.md#descriptor). Readiness uses this. |
| `vm` | `argv` | Runs argv as VM root, with the session's streams and the same exit mapping as `run`, for diagnostics, `shutdown` and acceptance probes. |
| `machines` | none | JSON array: `machine`, `id`, `state` (`running`, `starting`, `stopping` or `stopped`), `sessions`, `build_id`. |
| `start` | `machine`, `id`, `idle_timeout` | Rewrites the machine's nspawn settings, starts it if needed and waits up to 60 s for its manager to report `running` or `degraded`. JSON: `state`, `seconds`. |
| `stop` | `machine`, `id` | Powers the machine off, waiting up to 30 s before terminating it. |
| `run` | see below | The command's streams and exit status. |
| `create` | `machine`, `id`, `account`, `image`, `time_zone` | Imports the image into a new subvolume and applies per-machine data. JSON: `build_id`, from the image's descriptor. |
| `export` | `machine`, `id` | `rootfs.tar.zst` of a stopped machine on stdout. |
| `import` | `machine`, `id`, `account`, `time_zone`; `rootfs.tar.zst` on stdin | Validates and extracts into a new subvolume, then applies per-machine data. |
| `remove` | `machine`, `id` | Deletes a stopped machine's subvolume, settings and record. Resumable. |

Phase 8 adds `listeners`, for port discovery, and `display`, for the per-machine Waypipe session, to this table before implementing them.

### `run`

The agent starts a transient service named `nsl-run-` followed by 32 random hex digits and `.service` in the machine's service manager, reached as VM root through D-Bus. Machines run with `PrivateUsers=no`, so VM root is machine root.

- **Argv:** `ExecStartEx` with the `no-env-expand` flag, so `$VAR`, `${VAR}`, `$$` and `%` reach the program literally. A command name without a slash is found through `ExecSearchPath`: `~/.local/bin`, `/usr/local/sbin`, `/usr/local/bin`, `/usr/sbin`, `/usr/bin`, `/sbin` and `/bin`, which also becomes the command's `PATH`.
- **Identity:** `User=` is the machine account, or root with `root`. Account sessions use `PAMName=nsl`, the image's [`nsl` PAM service](machine-images.md#machine-layer), which supplies a logind session, `XDG_RUNTIME_DIR` and the user manager. Root commands have no PAM session.
- **Directory and environment:** `WorkingDirectory=` is the requested directory; a missing directory fails the command without running it. `Environment=` holds the allowlisted `env`. The agent adds `WAYLAND_DISPLAY` when the machine has a live display session (Phase 8).
- **Streams without a PTY:** the agent passes its own stdin, stdout and stderr to the unit as file descriptors. It copies no bytes, so binary data and separate streams survive.
- **PTY:** the agent opens a PTY in the machine through machined's `OpenMachinePTY` and runs the unit on it. It copies bytes between its stdio and the PTY unchanged. It adds no terminal-title or color sequences; OSC 3008 context sequences from the machine pass. The PTY takes the session terminal's size at start and follows its changes. Ctrl-C reaches the command through the PTY's line discipline.
- **Lifetime:** the unit has `RemainAfterExit=yes`, so its result stays readable until the agent collects it by stopping and resetting the unit. If the SSH session ends first, the agent stops the unit, delivering SIGTERM and SIGHUP and then SIGKILL after 10 s. Commands see SIGPIPE as they would in a shell (`IgnoreSIGPIPE=no`).
- **Exit status:** read from `ExecMainCode` and `ExecMainStatus`:

| Outcome | Exit status |
| --- | --- |
| The command exited with status N | N |
| The command was killed by signal N, with or without a core dump | 128 + N |
| systemd could not start the command, for example `200/CHDIR`, `203/EXEC`, `217/USER` or `224/PAM` | systemd's status, and the agent SHOULD name the step on stderr |
| The agent refused or failed before starting the command | 255, with an error line on stderr |

A running `nsl-run-*` unit whose command has not exited is an nsl command session for idle accounting.

### Errors

An agent error writes one line to stderr, `nsl-agent: CODE: message`, and exits 255. For `run`, 255 can also be the command's own status; the host relies on the message, not the status, to tell them apart.

| Code | Meaning |
| --- | --- |
| `bad-request` | Framing, JSON, a field or a value is invalid. |
| `protocol` | The request's protocol differs from the agent's. |
| `unknown-machine` | No machine with that name exists in this VM. |
| `machine-id` | The machine exists, but its recorded ID differs from the request's. |
| `not-running` | `run` targets a stopped machine; the host starts it first. |
| `busy` | Another lifecycle operation holds the machine, or `remove` or `export` found it running. |
| `refused` | The VM's role forbids the request, such as a second machine in an isolated VM. |
| `failed` | The operation failed; the message explains. |

## Rules

- The agent MUST reject a request before acting on it if any field fails validation. Names MUST match the CLI's machine-name rule, IDs MUST be 32 lowercase hex digits, account names MUST match the CLI's `--user` rule, and the UID and GID MUST be from 1 to 2147483646 and not 65534.
- Every machine operation MUST compare the request's `id` with the VM's record for that machine, and fail with `machine-id` on a mismatch.
- The agent MUST enforce the VM's trust tier from its boot identity:
  - A `shared` VM hosts any number of machines, each with `Bind=/mnt/host`.
  - An `isolated` VM hosts only the machine named in its identity, with no `/mnt/host` binding, display session or broker.
- Lifecycle operations on one machine MUST serialize in the VM. `run` MAY proceed concurrently with other sessions.
- `create` and `import` MUST build the machine in a staging subvolume and publish it under its name only after every step succeeds. On failure they MUST delete the staging subvolume, even if the SSH session ended, and leave the name free.
- `create` MUST verify the image's compressed digest and size while reading it. `create` and `import` MUST refuse archive entries with absolute or `..` paths, links leaving the tree, OCI whiteouts and duplicates. They MUST preserve numeric owners, modes, xattrs (including file capabilities) and ACLs.
- The agent MUST NOT run commands through a shell, `machinectl shell` or `nsenter`.
- The agent MUST NOT change `/mnt/host` content or host files except as the command it runs directs.

## Implementation

The agent is written in Go (`cmd/nsl-agent`) and uses a pinned `github.com/godbus/dbus/v5` for systemd and machined. It reaches a machine's service manager through the machine's system bus, at the leader's `/run/dbus/system_bus_socket`, as root. The manager's private socket refuses peers from another PID namespace. It reaches machined through the VM's system bus. It is statically linked and installed in the VM image, and it shares the request types and their validation with the CLI through `internal/protocol`. The same binary provides the VM's boot services: `nsl-agent storage`, `setup` and `boot`. A Python agent using `jeepney` was the alternative. Go avoids a second implementation of the framing and validation, and avoids interpreter startup on every command.

## Test cases

Phase 6 turns these into the agent's acceptance matrix, run through the CLI on every machine image. Protocol decoding and validation also have unit tests on both ends.

| Case | Expected |
| --- | --- |
| `printf '%s\0'` with `plain`, `with space`, `single'quote`, `double"quote`, `$HOME`, `${HOME}`, `$$`, `%h`, `%%`, `*`, an empty string, `new\nline`, `tab\there`, `ünïcødé`, `--flag`, `;`, `\|`, `&&`, `\backslash` | Output is the same arguments, NUL-separated, byte for byte. |
| `sh -c 'exit 42'` | 42 |
| `sh -c 'kill -TERM $$'`, and the same with `-INT`, `-HUP`, `-PIPE`, `-KILL`, `-SEGV` | 143, 130, 129, 141, 137, 139 |
| `sh -c 'printf out; printf err >&2'` | stdout is `out`; stderr is `err`. |
| `cat` with 1 MiB of random bytes followed by `\0\r\n\x03\x04` repeated 64 times | Output identical to input. |
| Identity script as the account, in a `/mnt/host` directory | UID, GID, user, hostname = machine name, `HOME=/home/USER`, working directory, and `/mnt/host` owner = host UID:GID. |
| Session probe | `XDG_RUNTIME_DIR=/run/user/UID`; `systemctl --user is-system-running` is `running` or `degraded`. |
| `tty` with `test -t 0 && test -t 1 && tty` | `/dev/pts/N`, with no escape sequences other than OSC 3008. |
| `sleep 30` on a PTY, then Ctrl-C from a host terminal | Exits within 3 s. |
| `run` on a stopped machine | `not-running`; nothing runs. |
| A wrong `id`, a wrong `protocol`, an unknown field, a duplicate key, `env` with `PATH`, a relative `directory`, NUL in argv, empty argv | 255 with the matching error code; nothing runs. |
| `root: true` with `id -u` | `0`, with no PAM session. |
| No-op `true`, 50 times | Median latency recorded against the experiment's 65 ms. |

## References

- Rationale: [ADR-0017](../adr/0017-shared-vm-and-machine-images.md). Evidence: the entry-method comparison in the [shared-VM experiment](../plans/shared-vm-experiment.md#phase-2--machines-as-containers).
- [org.freedesktop.systemd1](https://www.freedesktop.org/software/systemd/man/latest/org.freedesktop.systemd1.html), [org.freedesktop.machine1](https://www.freedesktop.org/software/systemd/man/latest/org.freedesktop.machine1.html), [sshd forced commands](https://man.openbsd.org/sshd#AUTHORIZED_KEYS_FILE_FORMAT).
