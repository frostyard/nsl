# Plan: Optional cloud-init provisioning

**Status: Phase 0 complete, 2026-11-10. Phase 1 is next.** Deliver reusable machine bootstrap, such as toolchains and dotfiles, without weakening nsl's account, identity or readiness. [ADR-0013](../adr/0013-optional-cloud-init-provisioning.md) records the decision; the [provisioning contract](../specs/provisioning.md) defines flags, states and persistence. Start after Phase 7 of the [shared-VM implementation](https://github.com/frostyard/nsl/blob/e617966e80f6534aa6525f016def59ab58c1d864/docs/plans/shared-vm-implementation.md), when machines, exports and imports work through the CLI.

## Phase 0 — Re-validate cloud-init in machines

- In Debian and Fedora machine images, install cloud-init with only the NoCloud datasource and a seed directory in the machine's tree.
- Show that user modules run after the nsl account exists, and without the machine layer's disabled networkd and resolved.
- Show that a machine with no seed boots with no discovery delay and runs no user module.
- **Done when:** both distros provision packages, files and a command from a seed. The findings are recorded here and the contract is corrected, or the decision is revisited.

**Result, 2026-11-10: complete. The decision stands, and the contract is corrected.**

Local builds only, all discarded: VM image r11 built from `e480414`, and from the same tree Debian 13 r6 and Fedora 44 r5 baselines plus r7 and r6 variants. The variants add only the `cloud-init` package and `datasource_list: [ NoCloud ]` in `/etc/cloud/cloud.cfg.d/`. Each experiment ran 8 machines in a disposable state directory. The harness wrote the seed, and the guard file, into each machine's tree after `nsl create` and before the first start, with the agent's offline nspawn. The evidence is private.

| | Debian 13 | Fedora 44 |
| --- | --- | --- |
| cloud-init | 25.1.4 | 26.1 |
| Image growth (zstd) | 26.3 MB | 35.1 MB |
| First start, baseline / marker, no seed | 1.16 s / 1.61 s | 1.83 s / 1.87 s |
| Restart median of 3, baseline / marker, no seed | 0.50 s / 0.49 s | 0.55 s / 0.59 s |
| First start, no marker, no seed | 6.40 s, status `error` | 5.66 s, status `error` |
| First start, seed (`package_update`, 2 packages, 2 files, 1 command) | 7.82 s | 11.98 s |
| cloud-init stages, seed | 6.86 s | 10.94 s |

- **Provisioning works.** On both distros a seed installed `jq` and `tree`, wrote a root file with mode 0644 and a deferred file owned by the account with mode 0600, and ran a command. `cloud-init status` reported `done` with no errors and no `degraded`. A restart ran no once-per-instance work again. A seed added later also ran, after 4 boots without one: cloud-init does not need the seed before the first boot.
- **The account comes first.** The agent creates it offline at `create`, before anything boots. The deferred file resolved the account's owner, and the command recorded `id` of the account. The account's `/etc/passwd` change preceded cloud-init's start by 1.5 s on Debian and 1.6 s on Fedora.
- **networkd and resolved stay masked** and inactive, with and without a seed. With `network: {config: disabled}`, cloud-init writes no network configuration and leaves `/etc/resolv.conf`, `/etc/hostname` and `/etc/hosts` unchanged. No wait-online unit ran.
- **No seed needs a marker.** `ds-identify` enables a single-entry `datasource_list`, `[ NoCloud, None ]` too, without looking for the seed. So with `[ NoCloud ]` alone, a machine with no seed ran every stage, reported `error` with 4 failed units on Debian and 2 on Fedora, and started about 5 s later. No user module ran. With `/etc/cloud/cloud-init.disabled`, the generator added no cloud-init unit: no service ran, no instance directory appeared, and no unit failed. Restarts took as long as the baseline's, and the first start was within 0.5 s of it.
- **Defaults conflict with the machine layer.** With the stock configuration and a seed, both distros created their default account with UID 1002 and passwordless `sudo`, wrote DHCP network configuration and generated SSH host keys; Fedora also wrote `PasswordAuthentication no`. Explicit module lists for the selectable keys, `users: []`, `network: {config: disabled}`, `preserve_hostname: true` and `manage_etc_hosts: false` stopped all of it except the SSH units, with no cloud-init warning.
- **SSH units.** `cloud-init-network.service` wants `sshd.service` and `sshd-keygen.service`, so a seed pulls in the units the machine layer disables. Debian's `sshd-keygen.service` generated distribution host keys. Fedora's `sshd.service` started, exited for want of host keys and stayed in its restart loop. Nothing listened on TCP 22, and `ssh-config` worked on both, because nsl's `sshd -i` uses its own keys and options. Debian also enables `cloud-init-hotplugd.socket`, a FIFO in `/run`.
- **Readiness waits for provisioning.** `cloud-init.target` joins the boot transaction, and `start` returns only when the machine's manager reports `running`. With a 20 s command in the seed, `start` returned after 30.0 s on Debian and 34.7 s on Fedora. An ordinary `nsl run` sent 3 s after `start` waited 27.2 s on Debian; on Fedora it failed with `busy` after the agent's 30 s machine lock. Provisioning longer than 60 s would fail `start` itself.

## Phase 1 — Image capability

- Add the `provisioning.cloud_init` capability and its supported key set to the machine descriptor, for images that passed Phase 0.
- Ship cloud-init as the contract's [image integration](../specs/provisioning.md#image-integration) says: off through the marker, the module lists, the guard settings, and the distribution's SSH and hotplug units masked. Show that masking keeps the units from starting.
- Validate bounded `#cloud-config` input against the guest schema and the nsl allowlist before user modules run. Keep the machine usable when validation or provisioning fails.
- **Done when:** the capability appears only on accepted images; conflicting options fail clearly; failed configuration leaves the machine reachable for diagnosis.

## Phase 2 — Creation, seed and archives

- Add `--cloud-init FILE`, a private input copy, its digest and a persistent provisioning ID. The agent writes the seed into the machine's tree at creation and removes the image's `cloud-init.disabled` marker.
- Keep provisioning out of machine readiness, so `start`, `run` and the machine lock do not wait for cloud-init's config and final stages.
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

## Open questions

- How readiness leaves provisioning out (Phase 2): the agent could treat the boot as ready when only cloud-init's jobs remain, or the image could move the config and final stages out of the boot transaction and the agent start them after readiness. The choice also sets what `provision status` reads.

## Later / ideas

- Raw scripts, multipart user data and more modules, after defining their validation.
- Team templates chosen explicitly by the user; never run configuration merely found in a project directory.
- A clone operation with a fresh identity.

## References

- Implements: [ADR-0013](../adr/0013-optional-cloud-init-provisioning.md), [provisioning contract](../specs/provisioning.md).
- Related: [machine images](../specs/machine-images.md), [agent](../specs/agent.md), [shared-VM implementation](https://github.com/frostyard/nsl/blob/e617966e80f6534aa6525f016def59ab58c1d864/docs/plans/shared-vm-implementation.md).
- [NoCloud](https://docs.cloud-init.io/en/latest/reference/datasources/nocloud.html), [module reference](https://cloudinit.readthedocs.io/en/latest/reference/modules.html), [cloud-init status](https://cloudinit.readthedocs.io/en/24.1/howto/status.html).
