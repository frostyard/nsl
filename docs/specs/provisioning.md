# Spec: Optional creation-time provisioning

**Status: proposed; not implemented.** Phase 0 of the [plan](../plans/cloud-init-provisioning.md) re-validated cloud-init inside Debian and Fedora machines on 2026-11-10, as [ADR-0017](../adr/0017-shared-vm-and-machine-images.md) required. A NoCloud seed in the machine's tree provisions packages, files and a command under nspawn, user modules run after the nsl account exists, and the machine layer's masked networkd and resolved stay masked. The [image integration](#image-integration) rules and the [readiness](#status) rule below come from its findings. This contract is for CLI, agent and machine-image implementers under [ADR-0013](../adr/0013-optional-cloud-init-provisioning.md). Under [ADR-0016](../adr/0016-wsl-style-machines.md), provisioning is machine bootstrap, such as toolchains and dotfiles, not project definition.

## Interface

| Proposed command or option | Behavior |
| --- | --- |
| `create NAME … --cloud-init FILE` | Snapshot a local cloud-config file into owned state. Provisioning starts on the machine's first boot. |
| `create NAME … --cloud-init FILE --wait-provisioning [--provision-timeout DURATION]` | Create, start, wait for readiness, then wait for provisioning to succeed. |
| `provision status NAME [--json]` | Report the saved provisioning ID, input digest and last known status. Refresh from the machine only if it is running; never start it. |
| `provision wait NAME [--timeout DURATION]` | Start if necessary and wait for provisioning to finish. |
| `provision logs NAME [--follow]` | Read provisioning logs from a running machine; never start it. |

Wait durations use positive Go duration syntax such as `30s` or `10m`, defaulting to `10m`. `--wait-provisioning` requires `--cloud-init`, and `--provision-timeout` requires `--wait-provisioning`. The provisioning timer starts after readiness. A timeout or interruption stops waiting and leaves the machine and provisioning running.

```sh
nsl create dev --distro debian:13 --cloud-init dev.yaml --wait-provisioning
nsl provision status dev --json
```

Initial file format:

```yaml
#cloud-config
package_update: true
packages:
  - git
  - make
write_files:
  - path: /etc/profile.d/project.sh
    permissions: '0644'
    content: |
      export PROJECT_ENV=development
runcmd:
  - [sh, -c, 'printf "provisioned\n" > /var/lib/project-ready']
```

Package names and commands are distro-specific user configuration. nsl does not translate them between distributions.

## Rules

### Input and capability

- `FILE` MUST be a readable regular file of UTF-8 YAML beginning with `#cloud-config`, at most 1 MiB. Shell scripts, multipart, compressed and remote-include inputs are out of scope.
- The host MUST snapshot the file and record its SHA256 before publishing the machine. Later edits to the source MUST NOT affect the machine. The host MUST NOT interpolate or execute the content.
- The machine image MUST advertise the `provisioning.cloud_init` capability in its [descriptor](machine-images.md#descriptor). It declares the interface version, the cloud-init version, the `nocloud` datasource and the supported top-level keys. The CLI MUST check both the catalogue's descriptor and the running machine's. It MUST NOT install cloud-init into a machine as a fallback.
- Selectable keys are `package_update`, `package_upgrade`, `packages`, `write_files`, `runcmd`, `ca_certs`, `apt` and `yum_repos`, narrowed by each image's declared subset. Other keys and malformed values MUST be rejected before user configuration applies.
- nsl owns the NoCloud metadata and instance ID. Users/groups, SSH keys, networking, datasource selection and reboot or power-state directives are reserved, and images MUST disable cloud-init defaults that conflict with the machine layer.
- Accepted configuration runs as machine root. It is equivalent to explicit machine administration.

### Image integration

An image that advertises `provisioning.cloud_init` MUST ship cloud-init configured as follows; Phase 0 found each default below in Debian 13 (cloud-init 25.1.4) and Fedora 44 (cloud-init 26.1).

- **Off without a seed.** The image MUST set `datasource_list: [ NoCloud ]` and MUST ship `/etc/cloud/cloud-init.disabled`. cloud-init's `ds-identify` enables a list with a single datasource without looking for its seed. Without the marker, a machine with no seed runs every cloud-init stage, reports `error`, leaves failed units and starts about 5 s later. With it, cloud-init's generator adds no unit to the boot. The agent removes the marker when it writes a seed, and only then.
- **Modules.** The image MUST set its module lists to the modules of its selectable keys, and `final_message`, only, and MUST set `network: {config: disabled}`, `users: []`, `preserve_hostname: true` and `manage_etc_hosts: false`. With the distributions' defaults, cloud-init creates the distribution's default account with passwordless `sudo`, writes DHCP network configuration and generates SSH host keys.

  | Key | Module and stage |
  | --- | --- |
  | `package_update`, `package_upgrade`, `packages` | `package_update_upgrade_install`, final |
  | `write_files` | `write_files`, init; `write_files_deferred`, final, for entries with `defer: true` |
  | `runcmd` | `runcmd`, config, writes the script; `scripts_user`, final, runs it |
  | `ca_certs` | `ca_certs`, init |
  | `apt` | `apt_configure`, config |
  | `yum_repos` | `yum_add_repo`, config |

- **SSH and hotplug.** The image MUST mask the distribution's SSH service and host-key units (`ssh.service`, `sshd.service`, `sshd-keygen.service`, `sshd-keygen@.service`, those it installs) and `cloud-init-hotplugd.socket`. `cloud-init-network.service` wants `sshd.service` and `sshd-keygen.service`, which starts them although the machine layer disables them. nsl's own `sshd -i` uses its own host key and options and needs no unit.
- **Seed.** The agent writes `meta-data` and `user-data` to `/var/lib/cloud/seed/nocloud/` in the machine's tree before the machine's first boot, owned by root, through the offline nspawn of [per-machine data](machine-images.md#per-machine-data). Machines run with `PrivateUsers=no`, so no ownership mapping applies. `meta-data` holds only the `instance-id`; without `local-hostname`, cloud-init keeps the name nsl wrote.

### Status

Provisioning state is separate from machine state:

| State | Meaning |
| --- | --- |
| `disabled` | No cloud-init input was selected. |
| `pending` | Input is recorded; execution has not been observed. |
| `running` | cloud-init is executing the configuration. |
| `succeeded` | cloud-init finished with no provisioning errors. |
| `failed` | Validation, setup or execution reported errors, including degraded completion. |
| `unknown` | The machine's report is unavailable or inconsistent; success is never inferred. |

`status --json`:

```json
{
  "schema_version": 1,
  "machine": "dev",
  "machine_state": "running",
  "provisioning_state": "running",
  "provisioning_id": "0123456789abcdef0123456789abcdef",
  "input_sha256": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
  "observed_at": "2026-09-26T18:30:00Z",
  "freshness": "live",
  "diagnostic": null
}
```

`provisioning_id` is 32 lowercase hex digits and `input_sha256` 64; both are null when disabled. `observed_at` is RFC 3339 UTC or null. `freshness` is `live`, `cached` or `unavailable`, and cached status MUST be labeled. `status` exits zero for any valid report, including a provisioning failure.

`wait` and create-with-wait MUST exit zero only for `succeeded`. A timeout MUST NOT mark provisioning failed while it still runs. Ordinary commands MUST NOT wait for provisioning.

Machine readiness MUST NOT include provisioning. Today `start` waits up to 60 s for the machine's manager to report `running` or `degraded`, and cloud-init's units are part of the boot transaction. In Phase 0, a seed that ran for about 30 s held `start` for as long, and an ordinary command waited for it or failed with `busy` after the 30 s machine lock. The implementation MUST keep cloud-init's config and final stages out of what `start`, `run` and the machine lock wait for.

### Persistence

- The seed lives in the machine's tree at cloud-init's NoCloud seed directory, with the image's `cloud-init.disabled` marker removed, and the input copy in the host's machine state, readable only by the owner.
- Each machine gets a fresh provisioning ID at creation, used as the NoCloud `instance-id` `nsl-<provisioning_id>`. It MUST stay stable across stop, start, `recover`, export and import.
- The export archive MUST carry the provisioning ID and input digest in its manifest. An imported machine keeps cloud-init's execution markers, so completed once-per-instance modules do not run again. No exactly-once guarantee covers side effects interrupted before cloud-init recorded them.
- `recover` MUST NOT reset the instance ID or clean cloud-init state. A deliberate full rerun means creating a fresh machine.

## References

- Rationale: [ADR-0013](../adr/0013-optional-cloud-init-provisioning.md). Implementation plan: [cloud-init provisioning](../plans/cloud-init-provisioning.md).
- Related: [CLI](cli.md), [machine images](machine-images.md), [agent](agent.md).
