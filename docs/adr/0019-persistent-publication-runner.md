# 0019 — Persistent publication runner

- **Status:** Accepted
- **Date:** 2026-09-28

## Context

`.github/workflows/images.yml` publishes the signed VM and machine images weekly, inside [ADR-0015](0015-image-verification-and-catalogue-policy.md)'s 30-day catalogue expiry. Its job needs a self-hosted runner with KVM and nested virtualization, a `systemd-vmspawn` from systemd 261, a user systemd manager, a Wayland compositor and a work directory under the account's home ([image publication](../design/image-publication.md)).

Every catalogue so far came from a manual dispatch to a temporary runner in the maintainer's desktop session, registered for the run and removed afterwards. The one scheduled run, [36417591932](https://github.com/frostyard/nsl/actions/runs/36417591932), waited about three hours for a runner and was cancelled. A weekly schedule that depends on the maintainer being logged in lets the catalogue expire unnoticed.

The job holds `packages: write` and signs with the workflow's GitHub OIDC identity, which the CLI trusts. Anything else that runs on its runner can reach that identity and the persistent build state. frostyard/nsl is public, and a repository-level runner accepts any workflow in the repository that names its labels, including one a pull request adds.

## Decision

**A dedicated, persistent runner VM.** The runner lives in its own Debian 14 virtual machine on the maintainer's lab host, `nsl-ci/nsl-builder` on Minideb, and runs nothing else. The private fleet repository owns it: OpenTofu creates the VM in a restricted Incus project, and Ansible configures the guest and registers the runner.

**An organization runner group.** The runner is registered to the frostyard organization in the `nsl-image-builder` runner group, labeled `nsl-image-builder`. The group admits only frostyard/nsl and, where the plan offers workflow restrictions, only `.github/workflows/images.yml` at `refs/heads/main`. The workflow's `runs-on` is unchanged.

**The runner account.** A normal account with a login shell and the `kvm` group, and no `docker` group. Linger keeps its user manager running. The runner and a headless sway, which advertises a `wl_seat` without input devices as the GUI acceptance needs, are user services, so jobs get the user manager, the account's primary group and `WAYLAND_DISPLAY`. A job-completed hook empties the work directory after every job, first keeping each publication's private evidence outside it for diagnosis; the Lima builder under `~/.local/share/nsl-publication-build` persists, as the workflow intends.

**Acceptance decides fitness.** The functional checks and the start-time gate do not change for nested virtualization. The memory budget is set from this runner's measurements: four idle machines at or below 1,200 MiB. The earlier 950 MiB was the shared-VM experiment's single measurement on the maintainer's workstation, adopted as a limit without headroom. On the builder, the same catalogue 9 images measured 974 MiB and fresh builds 1,047 MiB, about 120 MiB above that workstation. 1,200 MiB leaves room for package drift and is still about half of four separate VMs (2,328 MiB, [ADR-0017](0017-shared-vm-and-machine-images.md)). The first publication on the new runner is the evidence that it can publish; until then, a temporary runner in the same group remains the fallback.

## Consequences

- Scheduled runs find a runner, so weekly publication no longer depends on the maintainer's session.
- The runner persists state between runs: its home and the Lima builder. Only the trusted main-branch workflow can reach it, and the guest is disposable: rebuilding it means re-creating the VM and re-registering with a new token.
- Acceptance timings and memory are measured one virtualization layer deeper than before, which adds about 120 MiB of VM memory for four idle machines. A later failure of a gate on the builder is a regression to fix, not a reason to raise the budget again without new measurements.
- The runner updates itself. The VM, its image pin and its configuration change through reviewed fleet changes, not from this repository.

## Alternatives considered

- **Temporary runners in the maintainer's session:** the scheduled run waits for someone to start one.
- **The snosi runner's account on the same host:** Debian 13 ships systemd 257, the account is in the root-equivalent `docker` group, and it serves another repository.
- **A repository-level runner:** any workflow in the public repository that names the label, including one added by a pull request, would run beside the signing identity's state.
- **GitHub-hosted runners:** they provide KVM but no user systemd session or compositor, and every run would install vmspawn, QEMU and a compositor first.

## References

- Design: [image publication](../design/image-publication.md).
- Decisions: [ADR-0012](0012-signed-image-distribution.md), [ADR-0015](0015-image-verification-and-catalogue-policy.md).
- The fleet runbook: `docs/nsl-builder.md` in the private fleet repository.
