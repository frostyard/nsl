# Spec: Optional creation-time provisioning

**Status: deferred.** Nothing here is implemented, and it stays deferred until cloud-init is re-validated inside machines ([ADR-0017](../adr/0017-shared-vm-and-machine-images.md)). This contract is for CLI, agent and machine-image implementers under [ADR-0013](../adr/0013-optional-cloud-init-provisioning.md). Under [ADR-0016](../adr/0016-wsl-style-machines.md), provisioning is machine bootstrap, such as toolchains and dotfiles, not project definition.

Before this ships, re-validation must show that, in Debian and Fedora machines:

- cloud-init's NoCloud seed directory works under nspawn;
- user modules run after the nsl account exists;
- nothing conflicts with the machine layer's disabled network services.

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

### Persistence

- The seed lives in the machine's tree at cloud-init's NoCloud seed directory, and the input copy in the host's machine state, readable only by the owner.
- Each machine gets a fresh provisioning ID at creation, used as the NoCloud `instance-id` `nsl-<provisioning_id>`. It MUST stay stable across stop, start, `recover`, export and import.
- The export archive MUST carry the provisioning ID and input digest in its manifest. An imported machine keeps cloud-init's execution markers, so completed once-per-instance modules do not run again. No exactly-once guarantee covers side effects interrupted before cloud-init recorded them.
- `recover` MUST NOT reset the instance ID or clean cloud-init state. A deliberate full rerun means creating a fresh machine.

## References

- Rationale: [ADR-0013](../adr/0013-optional-cloud-init-provisioning.md). Implementation plan: [cloud-init provisioning](../plans/cloud-init-provisioning.md).
- Related: [CLI](cli.md), [machine images](machine-images.md), [agent](agent.md).
