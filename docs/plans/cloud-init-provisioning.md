# Plan: Optional cloud-init provisioning

**Status: deferred until cloud-init is re-validated inside machines.** Deliver reusable machine bootstrap, such as toolchains and dotfiles, without weakening nsl's account, identity or readiness. [ADR-0013](../adr/0013-optional-cloud-init-provisioning.md) records the decision; the [provisioning contract](../specs/provisioning.md) defines flags, states and persistence. Start after Phase 7 of the [shared-VM implementation](shared-vm-implementation.md), when machines, exports and imports work through the CLI.

## Phase 0 — Re-validate cloud-init in machines

- In Debian and Fedora machine images, install cloud-init with only the NoCloud datasource and a seed directory in the machine's tree.
- Show that user modules run after the nsl account exists, and without the machine layer's disabled networkd and resolved.
- Show that a machine with no seed boots with no discovery delay and runs no user module.
- **Done when:** both distros provision packages, files and a command from a seed. The findings are recorded here and the contract is corrected, or the decision is revisited.

## Phase 1 — Image capability

- Add the `provisioning.cloud_init` capability and its supported key set to the machine descriptor, for images that passed Phase 0.
- Disable cloud-init defaults that conflict with the machine layer or per-machine data.
- Validate bounded `#cloud-config` input against the guest schema and the nsl allowlist before user modules run. Keep the machine usable when validation or provisioning fails.
- **Done when:** the capability appears only on accepted images; conflicting options fail clearly; failed configuration leaves the machine reachable for diagnosis.

## Phase 2 — Creation, seed and archives

- Add `--cloud-init FILE`, a private input copy, its digest and a persistent provisioning ID. The agent writes the seed into the machine's tree at creation.
- Carry the provisioning ID and input digest in the export manifest; import keeps the seed and cloud-init's markers.
- **Done when:** changing the source file has no effect on a created machine. Machines exported before first boot, after success, after failure and after interruption import with the documented behavior.

## Phase 3 — Status, wait and diagnosis

- Add `provision status`, `provision wait`, `provision logs` and create-with-wait, reporting machine and provisioning state separately.
- Map cloud-init's status and errors to the contract's states, with timestamps and freshness. Keep startup and provisioning timeouts separate.
- **Done when:** scripts can tell success, failure and timeout apart; cached status is labeled; ordinary commands work while packages install.

## Phase 4 — Acceptance

Run on Debian and Fedora, then on each family that advertises the capability:

- fresh provisioning, including a service forwarded to host loopback;
- a restart and an import of a completed machine that do not rerun once-per-instance work;
- malformed input, a failing command, interruption and a wait timeout, each reported accurately without losing state;
- startup time with provisioning omitted, pending and complete.

**Done when:** this matrix passes and published descriptors advertise only tested capabilities.

## Later / ideas

- Raw scripts, multipart user data and more modules, after defining their validation.
- Team templates chosen explicitly by the user; never run configuration merely found in a project directory.
- A clone operation with a fresh identity.

## References

- Implements: [ADR-0013](../adr/0013-optional-cloud-init-provisioning.md), [provisioning contract](../specs/provisioning.md).
- Related: [machine images](../specs/machine-images.md), [agent](../specs/agent.md), [shared-VM implementation](shared-vm-implementation.md).
- [NoCloud](https://docs.cloud-init.io/en/latest/reference/datasources/nocloud.html), [module reference](https://cloudinit.readthedocs.io/en/latest/reference/modules.html), [cloud-init status](https://cloudinit.readthedocs.io/en/24.1/howto/status.html).
