# frostyard/nsl

nsl is a Go CLI for persistent systemd-vmspawn/QEMU development VMs on atomic Linux hosts. Each environment runs a full distro directly in one VM; see ADR-0005. Start at [docs/README.md](docs/README.md); user setup is in [README.md](README.md).

This is the canonical agent instruction file. `CLAUDE.md`, `GEMINI.md`, and `.github/copilot-instructions.md` link here; `.claude/skills` links to `.agents/skills` ([ADR-0002](docs/adr/0002-agent-portable-instruction-surface.md)). Edit the canonical targets only.

## Skills

Procedures belong in [.agents/skills/](.agents/skills/). Add a skill based on its template when a multi-step procedure repeats.

- [validate-image](.agents/skills/validate-image/SKILL.md): build and exercise distribution images, record real VM evidence and diagnose guest integration failures.

## Live code conventions

- The root `main.go` owns command parsing; `state.go`, `vm.go`, `ports.go`, `backup.go` and `storage.go` manage storage, lifecycle, forwarding and archives using the Go standard library. The `runner` interface abstracts local tools. The image pipeline installs `guest/exec.py` and `guest/setup.py`; the command helper is also embedded for local protocol tests. Tests use a fake runner and local Python processes; do not require root or a VM for unit tests. Language/dependency changes are allowed when justified.
- Signed image delivery uses `sigstore-go` and bounded zstd under [ADR-0015](docs/adr/0015-image-verification-and-catalogue-policy.md). Keep publisher identity, trust root, rollback/freshness and cache checks intact. Public image artifacts must exclude private VM evidence/backups; follow [the publication design](docs/design/image-publication.md).
- Run guest commands with argument arrays (`exec.Command`), not a host shell. Validate machine ownership (`app.owned`) before changing state. Never overwrite preexisting images or environments. Reserve `--root` for explicit administrative commands.
- Direction: [ADR-0016](docs/adr/0016-wsl-style-machines.md) replaces environments, project shares and desktop opt-in with WSL-style machines; [ADR-0017](docs/adr/0017-shared-vm-and-machine-images.md) runs them as systemd-nspawn containers in one shared VM, from Frostyard machine images. The [machine CLI](docs/specs/machine-cli.md) is planned; follow the [implementation plan](docs/plans/shared-vm-implementation.md) and its carried-over requirements. Until implemented, the rules below describe the live code.
- Project shares are fixed at creation and persist across stop/start. GUI uses Waypipe independently of project mounts. `stop` preserves disks and configuration. Review [the lifecycle design](docs/design/lifecycle.md) and [the CLI contract](docs/specs/cli.md) before changing this.
- Backups follow [ADR-0006](docs/adr/0006-stopped-vm-backups.md): stopped VMs only; restore preserves guest identity and keys but allocates new runtime identity. Validate archives before publishing; never inherit a host share implicitly.
- Storage operations follow [ADR-0008](docs/adr/0008-offline-storage-management.md): stopped owned units only, resumable removal and growth, no shrinking. Lifecycle calls must reject a replacement environment ID after waiting for a lock.
- Keep host lifecycle independent of guest distribution. Image adapters own packages, boot hooks, filesystem growth and security policy; follow the [guest contract](docs/specs/guest-images.md) and [distribution plan](docs/plans/distribution-support.md). Debian, Ubuntu, Fedora, CentOS Stream, openSUSE Leap/Tumbleweed and Arch profiles passed the common suite on Snow x86-64. Keep `scripts/probe-distribution.py` common and package/kernel command arrays in profile maintenance files. Native SUSE bootloader and Arch pacman adapters own their UKI maintenance.
- Regenerate `THIRD_PARTY_NOTICES.txt` with `python3 scripts/license-notices.py` when Go dependencies change; CI checks it against shipped packages.
- Run `make ci` before claiming a change is complete. CI runs the same recipe. GoReleaser Pro (not OSS) validates `.goreleaser.yaml` in CI using the org secret; run Pro's `goreleaser check` when changing release configuration.

## Repository boundary

Never commit binaries, `build/`, `dist/`, coverage artifacts, local agent state, credentials or a host-specific VM image. The CLI does not install host packages, change device permissions or sudoers, or share D-Bus, GPU or SSH sockets. Host storage is shared only through the `/mnt/host` allowlist in [ADR-0016](docs/adr/0016-wsl-style-machines.md). Releases are built from `v*` tags by `.github/workflows/release.yml` using GoReleaser Pro and GitHub provenance attestations. Conventional commit subjects make the release changelog readable.

## Documentation

Docs follow [the index](docs/README.md): `adr/` for immutable rationale, `design/` for living mechanisms, `specs/` for testable contracts, `plans/` for phased work. Start new documents from the category's `TEMPLATE.md`; update the index and link related docs in both directions. Keep user instructions in `README.md`. Significant decisions get a new ADR before changing the related design/spec. Do not rewrite an accepted ADR except to mark supersession or repair links.
