---
description: The nsl.conf file, its keys and defaults, syntax and rules.
---

# Configuration

nsl works without a configuration file. To change the defaults, create `$XDG_CONFIG_HOME/nsl/nsl.conf`. If `XDG_CONFIG_HOME` is unset, empty or relative, use `~/.config/nsl/nsl.conf`. This file is separate from the state in `NSL_HOME`; nsl reads it but never writes it.

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

If the file contains an unknown section or key, a duplicate, a key outside a section, an invalid value or an out-of-range number, nsl reports `PATH:LINE: message` and won't start a VM. Fix the reported line before trying again. A symlink to a missing file, or anything other than a regular file, is also an error.
