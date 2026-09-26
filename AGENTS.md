# frostyard/nsl

nsl is a Go CLI for persistent systemd-nspawn development machines on atomic Linux hosts. Start at [docs/README.md](docs/README.md); user setup is in [README.md](README.md).

This is the canonical agent instruction file. `CLAUDE.md`, `GEMINI.md`, and `.github/copilot-instructions.md` link here; `.claude/skills` links to `.agents/skills` ([ADR-0002](docs/adr/0002-agent-portable-instruction-surface.md)). Edit the canonical targets only.

## Skills

Procedures belong in [.agents/skills/](.agents/skills/). Add a skill based on its template when a multi-step procedure repeats; none are defined yet.

## Live code conventions

- Keep the CLI standard-library-only. The root `main.go` owns command parsing, the `runner` interface abstracts calls to nspawn, and `main_test.go` uses a fake runner; do not require root or a machine for unit tests.
- Run guest commands with argument arrays (`exec.Command`), not a host shell. Validate machine ownership (`app.owned`) before changing state. Never overwrite preexisting images or environments. Reserve `--root` for explicit administrative commands.
- nspawn remembers mount settings; switching mounts on a running machine is forbidden. `stop` clears saved volumes before returning. Review [the lifecycle design](docs/design/lifecycle.md) and [the CLI contract](docs/specs/cli.md) before changing this.
- Run `make ci` before claiming a change is complete. CI runs the same recipe. GoReleaser Pro (not OSS) validates `.goreleaser.yaml` in CI using the org secret; run Pro's `goreleaser check` when changing release configuration.

## Repository boundary

Never commit binaries, `build/`, `dist/`, coverage artifacts, local agent state, credentials or a host-specific nspawn machine image. The CLI does not install nspawn.org on the host, change sudoers or implicitly mount host home, D-Bus, GPU or SSH sockets. Releases are built from `v*` tags by `.github/workflows/release.yml` using GoReleaser Pro and GitHub provenance attestations. Conventional commit subjects make the release changelog readable.

## Documentation

Docs follow [the index](docs/README.md): `adr/` for immutable rationale, `design/` for living mechanisms, `specs/` for testable contracts, `plans/` for phased work. Start new documents from the category's `TEMPLATE.md`; update the index and link related docs in both directions. Keep user instructions in `README.md`. Significant decisions get a new ADR before changing the related design/spec. Do not rewrite an accepted ADR except to mark supersession or repair links.
