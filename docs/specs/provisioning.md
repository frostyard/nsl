# Spec: Optional creation-time provisioning

**Status: planned interface; none of the commands or flags below are implemented.** This contract is for CLI, image and backup implementers under [ADR-0013](../adr/0013-optional-cloud-init-provisioning.md).

## Interface

The following options extend either local-image creation or the separately planned catalogue selector. Existing image/resource/project flags keep their meanings.

| Proposed command or option | Behavior |
| --- | --- |
| `create NAME … --cloud-init FILE` | Snapshot a local cloud-config file into owned state. Prepare the VM; provisioning starts on its first boot. |
| `create NAME … --cloud-init FILE --wait-provisioning [--provision-timeout DURATION]` | Prepare, boot, wait for authenticated management access, then wait for provisioning success. |
| `provision status NAME [--json]` | Report saved provisioning identity/input digest and last known status; refresh from the authenticated guest if already reachable. Never start a VM. |
| `provision wait NAME [--timeout DURATION]` | Start if necessary and wait for provisioning to finish. |
| `provision logs NAME [--follow]` | Read provisioning logs through authenticated guest access. Require a running reachable VM; never start it implicitly. |

Wait durations use positive Go duration syntax such as `30s` or `10m`, defaulting to `10m`. Zero/negative values are invalid. `--wait-provisioning` requires `--cloud-init`; `--provision-timeout` also requires the wait option. The provisioning timer begins after management readiness; normal startup retains its separate timeout. Wait timeout or interruption stops waiting and leaves the VM/provisioning running.

Example, once both catalogue delivery and provisioning are implemented:

```sh
nsl create dev --distro ubuntu:24.04 --cloud-init dev.yaml --wait-provisioning
nsl provision status dev --json
nsl provision logs dev
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

Package names and shell commands remain distro-specific user configuration. nsl does not translate them between distributions.

## Input and capability rules

- `FILE` MUST resolve to a readable regular local file containing UTF-8 YAML beginning with `#cloud-config`, bounded to 1 MiB for the initial interface. Direct shell-script, multipart, compressed and remote-include inputs are outside this initial surface. Omission MUST leave provisioning disabled.
- The host MUST snapshot the file and record its SHA256 before publishing an environment; later edits or deletion of the source MUST NOT affect that environment. It MUST NOT interpolate shell variables or execute supplied content on the host.
- The proposed image capability `provisioning.cloud_init` MUST declare interface version, installed cloud-init version, `nocloud` datasource and supported top-level cloud-config keys. Both image selection metadata and the authenticated guest descriptor MUST be checked. Missing/incompatible capability MUST fail clearly; the CLI MUST NOT install cloud-init into an arbitrary guest as a fallback.
- Initial selectable keys are `package_update`, `package_upgrade`, `packages`, `write_files`, `runcmd`, `ca_certs`, `apt` and `yum_repos`, restricted further by each tested image's declared subset. Unknown/unsupported keys and malformed values MUST be rejected using the guest's cloud-init schema and the nsl allowlist before applying user configuration. Host-side checks cover input bounds/format; full guest validation can fail on first boot and MUST produce observable `failed` status.
- nsl controls NoCloud metadata and instance ID. Users MUST NOT supply replacement metadata, network configuration or vendor data in this interface. Users/groups, SSH authentication/host keys, partitioning, growth, datasource selection and reboot/power-state directives are reserved. Image adapters MUST disable conflicting cloud-init defaults as well as rejecting conflicting user keys.
- Approved configuration and commands run as guest root. This is equivalent in authority to explicit guest administration; scripts can still change nsl-managed files or services. Host shares remain limited to existing explicit grants.

## Boot and status rules

Images MUST expose only the local NoCloud datasource for this feature, with a read-only seed. Discovery may occur early; user modules MUST run after nsl account/identity/root-growth setup, and network-dependent modules after guest networking. Management SSH readiness MUST NOT wait for user provisioning to finish. Adapters MUST test these dependencies for boot cycles and avoid changing pinned SSH host keys.

Provisioning state is independent of VM runtime state:

| State | Meaning |
| --- | --- |
| `disabled` | No cloud-init input was selected. |
| `pending` | Input is recorded; execution has not been observed starting. |
| `running` | Cloud-init is executing the selected configuration. |
| `succeeded` | Cloud-init reports completion with no provisioning errors. |
| `failed` | Input validation, required provisioning setup or cloud-init execution reported errors. |
| `unknown` | Provisioning was requested but the guest report is unavailable or inconsistent; never infer success. |

`status --json` uses these field names and types:

```json
{
  "schema_version": 1,
  "environment": "dev",
  "runtime_state": "Running",
  "provisioning_state": "running",
  "provisioning_id": "0123456789abcdef0123456789abcdef",
  "input_sha256": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
  "observed_at": "2026-09-26T18:30:00Z",
  "freshness": "live",
  "diagnostic": null
}
```

`provisioning_id` is 32 lowercase hexadecimal characters; `input_sha256` is 64. Both are null for `disabled`. `observed_at` is RFC 3339 UTC, or null without an observation. `freshness` is `live`, `cached` or `unavailable`; `diagnostic` is null or a bounded summary string. Cached status for a stopped/unreachable guest MUST be labeled. Human output MUST show whether the report is current. Status returns zero for a valid report, including a reported provisioning failure; command/query failures return nonzero.

Wait and create-with-wait MUST return zero only for `succeeded`. Failure, disabled provisioning, timeout, lost guest access or interruption return nonzero with an actionable reason. A timeout MUST NOT mark provisioning failed merely because it is still running. Cloud-init degraded/error completion counts as failure. Normal `start`, `shell` and `exec` continue to use management readiness and MUST NOT silently wait for provisioning.

Errors retain the environment and input. `recover` may resume unfinished work under cloud-init's normal module semantics but MUST NOT reset instance identity, clean cloud-init state or force a full rerun. There is no automatic retry/reset command in the initial interface. Recreate a fresh environment for a deliberate full run; partially executed scripts should be written to tolerate retries after interruption.

## Persistence, privacy and restore

- Private input, seed and provisioning metadata MUST live in owned environment state with access limited to the owner. Shared generic bases MUST contain no per-environment user data. Normal status/errors MUST NOT dump input or raw provisioning logs; explicit log access may expose user-supplied values.
- Generate a fresh provisioning ID at creation and use `nsl-<provisioning_id>` as NoCloud's `instance-id`. This ID MUST be stable across stop/start, recovery and backup/restore, regardless of the newly allocated runtime ID on restore.
- Planned archive version 2 MUST carry validated provisioning metadata and input sufficient to reconstruct the seed, including before first boot. The guest disk preserves cloud-init's execution markers. Restore MUST preserve the provisioning ID and input digest, recreate local seed paths, and retain existing explicit project/desktop opt-in behavior. Version-1 archives remain readable with no invented provisioning request; older readers must reject version 2.
- A completed restored guest MUST NOT rerun once-per-instance modules because it was restored. A pending or interrupted restored guest can execute unfinished work on next boot according to cloud-init semantics. No exactly-once guarantee is made for side effects interrupted before their completion marker was persisted.
- Export still requires a stopped VM. Seed restoration and archive validation MUST ship with the first provisioning implementation. Snapshot cloning with fresh identity and deliberate reprovisioning is a separate future operation.

## References

- Rationale: [ADR-0013](../adr/0013-optional-cloud-init-provisioning.md).
- Implementation and acceptance: [cloud-init plan](../plans/cloud-init-provisioning.md).
- Related: [current CLI](cli.md), [guest images](guest-images.md), [lifecycle](../design/lifecycle.md), [image delivery](../plans/image-distribution.md).
