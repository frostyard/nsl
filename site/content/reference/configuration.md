---
description: The nsl.conf file, its keys and defaults, syntax and rules.
---

# Configuration

Settings live in one optional file, `$XDG_CONFIG_HOME/nsl/nsl.conf`, which is `~/.config/nsl/nsl.conf` when `XDG_CONFIG_HOME` is unset, empty or relative. It is separate from state in `NSL_HOME`. nsl reads the file and never writes it; without it, every setting has its default.

```ini
# ~/.config/nsl/nsl.conf
[vm]
# GiB; the default is half the host's memory.
memory = 16
# The default is every host CPU.
cpus = 8

[machines]
autostart = true
# Minutes without sessions before a machine stops; 0 disables.
idle_timeout = 15

[isolated]
memory = 4
cpus = 2
```

## Keys

| Section | Key | Value | Default |
| --- | --- | --- | --- |
| `vm` | `memory` | GiB ceiling for the shared VM, 1–128 | Half the host's memory, rounded down, from 2 to 128 |
| `vm` | `cpus` | vCPUs for the shared VM, 1–64 | Every host CPU, at most 64 |
| `machines` | `autostart` | `true` or `false`: start every machine when the VM starts | `true` |
| `machines` | `idle_timeout` | Minutes without sessions before a machine stops, 0–1440; `0` disables | `15` |
| `isolated` | `memory` | GiB for each isolated machine's VM, 1–128 | `2` |
| `isolated` | `cpus` | vCPUs for each isolated machine's VM, 1–64 | `2` |

## When changes apply

- `vm` and `isolated` resources apply at the next start of the affected VM. Until then, `nsl config` and `nsl list` report the pending restart.
- `autostart` applies at the next VM start.
- `idle_timeout` applies from the next nsl command.

## Show the effective configuration

`nsl config` prints each setting, its value and its source: `default`, or `file` with the line that set it.

```text
$ nsl config
Configuration file: /var/home/you/.config/nsl/nsl.conf (absent)

SETTING                VALUE  SOURCE
vm.memory              29     default (half of host memory)
vm.cpus                32     default (host CPUs)
machines.autostart     true   default
machines.idle_timeout  15     default
isolated.memory        2      default
isolated.cpus          2      default
```

## Syntax

- The file is UTF-8 without NUL bytes, at most 64 KiB.
- Lines are trimmed, including a carriage return before the line feed. Blank lines are ignored.
- A line starting with `#` is a comment. There are no comments after a value.
- `[section]` starts a section, and each section appears at most once.
- `key = value` splits at the first `=`, and both sides are trimmed. Names are case-sensitive.
- Numbers are plain decimal digits: no sign, unit or quotes. Booleans are exactly `true` or `false`.

## Errors

nsl refuses to guess. Unknown sections or keys, duplicates, keys outside a section, invalid values and out-of-range numbers are errors of the form `PATH:LINE: message`, and nsl starts no VM with a file it only partly understands. A symlink to a missing file, or anything other than a regular file, is an error too.
