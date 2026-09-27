# Plan: Optional cloud-init provisioning

**Status: planned; interface documented, implementation not started.** Deliver reusable creation-time project setup while retaining nsl management access and persistent guest identity. [ADR-0013](../adr/0013-optional-cloud-init-provisioning.md) records the decision; the [provisioning contract](../specs/provisioning.md) defines exact flags, states and restore behavior.

Sequence this after image capability negotiation and, for the public user workflow, [verified image delivery](image-distribution.md). Local-image development can proceed without a registry. Debian and Ubuntu are the first acceptance targets; Fedora and other families need separate evidence before advertising the capability.

## Phase 1 — Image capability and guest boundary

- Add the optional versioned cloud-init capability and per-image supported key set. Install and configure cloud-init within image profiles, without host package installation.
- Use local NoCloud only. Separate nsl-managed account, SSH, networking and root growth from allowed user modules; disable conflicting cloud-init defaults, including host-key replacement.
- Validate bounded `#cloud-config` input through the guest's schema and nsl key allowlist before user modules execute. Keep management access available on validation or provisioning failure.
- Define systemd ordering so account/root-growth setup and required networking precede user configuration without gating SSH readiness on cloud-init completion. An image with no selected configuration must avoid metadata discovery delays and user module execution.
- **Done when:** Debian and Ubuntu expose the capability, an unprovisioned VM keeps normal startup behavior, conflicting options fail clearly, and failed configuration leaves authenticated diagnosis available.

## Phase 2 — Creation, seed and backup

- Add `--cloud-init FILE`, immutable private input storage, input digest and a persistent provisioning ID. Generate a read-only NoCloud seed using a tested launcher path without assuming extra packages exist on atomic hosts.
- Preserve lazy creation; first start executes the selected configuration. Reject unsupported image capabilities before publishing state when known, and verify the capability again over authenticated guest access.
- Implement archive version 2 with validated provisioning input/metadata and seed reconstruction. Preserve instance ID and guest execution markers across restore; retain version-1 read support without introducing a new provisioning request.
- **Done when:** editing/deleting the original file has no effect on an environment, source secrets never enter the shared base, and backups taken before first boot, after success, after failure and after interruption restore with the documented behavior.

## Phase 3 — Status, wait and diagnosis

- Add `provision status`, `provision wait`, `provision logs`, and create-with-wait. Report management readiness and provisioning state independently.
- Translate supported cloud-init status/error reports into stable nsl states with timestamps and freshness; preserve useful diagnostic summaries and explicit logs.
- Apply separate startup and provisioning timeouts. Timeout/interruption leaves guest work running; query failures never become success. Preserve failed guests for inspection without automatically resetting their state.
- **Done when:** scripts can distinguish success, failure and timeout; cached status is labeled, logs are available after failure, and ordinary shell/exec remains usable while package installation runs.

## Phase 4 — Acceptance and release

Run the following on both Debian and Ubuntu, then repeat for each additional advertised family:

- Fresh provisioning installs packages, writes files and starts a test service reachable through host localhost. Keys, UID/GID and explicit share ownership remain correct.
- A second ordinary boot and restore of a completed guest preserve once-per-instance completion without rerunning its marker command. A pending restored guest provisions on first boot with its original input and ID.
- A malformed/unsupported config fails before user changes; a command/package failure reports `failed` and retains management access. Network loss and wait timeout report accurately without destroying guest state.
- Interrupted creation, concurrent waits, guest restart during provisioning and stopped backups of partial work retain identity and valid state. Tests must not assume exactly-once arbitrary script side effects.
- Cloud-init capability mismatch, changed host key, invalid archive fields, input tampering and missing seed fail clearly. User data never appears in ordinary status output or shared base artifacts.
- Compare startup with provisioning omitted, pending and completed. Keep actual measurements and per-distro limitations in the acceptance report; run `make ci` and root-free contract tests.
- **Done when:** this matrix passes, public image metadata advertises only tested capabilities, and user documentation labels the supported cloud-config subset and explicit root authority.

## Later / ideas

- Raw scripts, multipart user data and a wider module set after defining validation and compatibility behavior.
- Team templates and explicit user-selected defaults; no automatic execution of configuration merely discovered in a project directory.
- A deliberate clone/reprovision operation with new identity and clear treatment of previous side effects.
- Enterprise policy and secret-delivery integrations with their own scope and lifecycle.

## Open questions

- Seed encoding/attachment tooling on minimum supported atomic hosts: phase 2 prototype.
- Per-distro cloud-init versions, schema behavior and status API differences: phase 1 acceptance.
- Exact version-2 archive fields and input integrity checks: phase 2, before enabling creation flags.
- Additional modules and user-facing retries: later work; the first interface uses recreation for a deliberate full run.

## References

- Implements: [ADR-0013](../adr/0013-optional-cloud-init-provisioning.md), [provisioning interface](../specs/provisioning.md).
- Related: [distribution matrix](distribution-support.md), [guest image contract](../specs/guest-images.md), [main roadmap](wsl2-equivalent.md), [image pipeline](../../image/README.md).
- Primary references: [NoCloud](https://docs.cloud-init.io/en/latest/reference/datasources/nocloud.html), [module reference](https://cloudinit.readthedocs.io/en/latest/reference/modules.html), [cloud-init status](https://cloudinit.readthedocs.io/en/24.1/howto/status.html).
