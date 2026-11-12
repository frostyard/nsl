# Org-wide decisions (frostyard/core ADRs)

Conventions this repository follows that are decided at the org level are
recorded as ADRs in
[frostyard/core](https://github.com/frostyard/core/tree/main/docs/adr).
The ones that bind nsl:

- [ADR-0012 — svu-derived versions and make bump](https://github.com/frostyard/core/blob/main/docs/adr/0012-svu-versioning-and-rolling-dev-prerelease.md) — `.svu.yml` and `make bump` tag releases from conventional commits; nsl publishes no rolling `dev` prerelease
- [ADR-0016 — Reverse-DNS org.frostyard.* identifiers](https://github.com/frostyard/core/blob/main/docs/adr/0016-reverse-dns-org-frostyard-identifiers.md) — the app ID `org.frostyard.nsl` in the problem records (`problems.go`)
- [ADR-0018 — Org-wide agent instruction and knowledge surfaces](https://github.com/frostyard/core/blob/main/docs/adr/0018-org-wide-agent-instruction-and-knowledge-surfaces.md) — `AGENTS.md` is canonical; `CLAUDE.md`, `GEMINI.md`, `.cursorrules` and `.github/copilot-instructions.md` are symlinks to it, and `.memory/corrections.jsonl` is the append-only corrections inbox
- [ADR-0019 — Governance as code and risk tiers](https://github.com/frostyard/core/blob/main/docs/adr/0019-governance-as-code-and-risk-tiers.md) — `.github/pull_request_template.md` has a `Risk tier` section
- [ADR-0021 — SHA-pinned actions and least-privilege CI workflows](https://github.com/frostyard/core/blob/main/docs/adr/0021-sha-pinned-actions-and-least-privilege-ci.md) — every workflow is SHA-pinned, with top-level `permissions: {}` and job-level grants, and every checkout sets `persist-credentials: false`
- [ADR-0023 — External downloads are version-pinned and checksum-verified](https://github.com/frostyard/core/blob/main/docs/adr/0023-verified-pinned-downloads.md) — `scripts/bootstrap-poc.sh` and `scripts/bootstrap-publisher.py` check a pinned SHA-256 for each tool; `mise.lock` holds the checksums of the mise tools
- [ADR-0025 — One docs/ tree per repository, in core's four-category shape](https://github.com/frostyard/core/blob/main/docs/adr/0025-consolidate-repository-docs-into-docs.md) — this `docs/` tree, indexed in [README.md](README.md)
- [ADR-0029 — ACMM conformance via canonical aliases](https://github.com/frostyard/core/blob/main/docs/adr/0029-acmm-conformance-via-canonical-aliases.md) — the alias symlinks above and `.claude/skills` → `.agents/skills`
- [ADR-0038 — make ci stays canonical](https://github.com/frostyard/core/blob/main/docs/adr/0038-scope-the-test-name-filter-to-chairlift.md) — `make ci` runs every test; the test-name filter is chairlift's only
- [ADR-0040 — Publish the repository settings contract](https://github.com/frostyard/core/blob/main/docs/adr/0040-publish-the-repository-settings-contract.md) — `SECURITY.md` names GitHub private vulnerability reporting
- [ADR-0043 — Pin repository tools in mise and name the verify gate](https://github.com/frostyard/core/blob/main/docs/adr/0043-pin-repository-tools-in-mise-and-name-the-verify-gate.md) — `mise.toml` and `mise.lock` pin golangci-lint, Node and svu; `go.mod` is the only Go pin, which mise reads, and `mise.lock` keeps its checksums; `make verify` changes no file
- [ADR-0044 — Expose the make gate triad in every repository](https://github.com/frostyard/core/blob/main/docs/adr/0044-expose-the-make-gate-triad-in-every-repository.md) — `make verify`, `make check` and `make ci`; `ci` runs `verify`
- [ADR-0053 — Name the svu config .svu.yml](https://github.com/frostyard/core/blob/main/docs/adr/0053-name-the-svu-config-svu-yml.md) — `.svu.yml`
- [ADR-0068 — Label pull requests that touch high-risk paths](https://github.com/frostyard/core/blob/main/docs/adr/0068-label-pull-requests-that-touch-high-risk-paths.md) — `.github/risk-paths.json` and the `risk-paths.yml` caller; [ADR-0078](https://github.com/frostyard/core/blob/main/docs/adr/0078-sort-pull-requests-by-levels-l1-to-l4.md) amends it in part: the list carries levels L1 to L4, and `risk:high` stays as the alias of L1 and L2
- [ADR-0069 — Report problems local first, opt-in, with one sender per host](https://github.com/frostyard/core/blob/main/docs/adr/0069-report-problems-local-first-opt-in-with-one-sender-per-host.md) — nsl writes panics and coded internal errors to the local journal and sends nothing; see [ADR-0023](adr/0023-local-problem-records.md)
- [ADR-0070 — Build tool docs sites with ProperDocs and MaterialX on GitHub Pages](https://github.com/frostyard/core/blob/main/docs/adr/0070-build-tool-docs-sites-with-materialx-on-github-pages.md) — the site in `site/`, built by `make site` and deployed by `site.yml`; see [ADR-0018](adr/0018-user-documentation-site.md)
- [ADR-0071 — Hold every code repository to one baseline](https://github.com/frostyard/core/blob/main/docs/adr/0071-hold-every-code-repository-to-one-baseline.md) — rows B1 to B17; see [ADR-0024](adr/0024-adopt-the-repository-baseline.md)
- [ADR-0072 — Write every README to one standard](https://github.com/frostyard/core/blob/main/docs/adr/0072-write-every-readme-to-one-standard.md) — `README.md` is a `tool` README
- [ADR-0073 — Let CI run the make ci targets as separate jobs](https://github.com/frostyard/core/blob/main/docs/adr/0073-let-ci-run-the-make-ci-targets-as-separate-jobs.md) — `ci.yml` runs `make ci` in 1 job, which the ADR keeps allowed
- [ADR-0076 — Require status checks on every default branch](https://github.com/frostyard/core/blob/main/docs/adr/0076-require-status-checks-on-every-default-branch.md) — the required status checks of `main` are `verify` and `vulncheck` (the owner's comment for this repository on [frostyard/core#222](https://github.com/frostyard/core/issues/222)); `release-config` reads the GoReleaser key, which a Dependabot or fork pull request does not get, so it is not a required check: it stays in CI, and on such a pull request it skips the release validation with a notice
- [ADR-0077 — Bind ready-for-human to the head commit](https://github.com/frostyard/core/blob/main/docs/adr/0077-bind-ready-for-human-to-the-head-commit.md) — the `PR triage` caller removes `ready-for-human` when the head moves after the label, or when CI at the head is not green when the label comes
- [ADR-0078 — Sort pull requests by levels L1 to L4](https://github.com/frostyard/core/blob/main/docs/adr/0078-sort-pull-requests-by-levels-l1-to-l4.md) — `.github/risk-paths.json` is schema version 2, with a `level` for each glob and `default_level` for a path that no glob matches; the caller adds `level:L1` to `level:L4`, and `risk:high` for L1 and L2

Not applicable: the package-publication ADRs
([ADR-0009](https://github.com/frostyard/core/blob/main/docs/adr/0009-single-artifact-origin-repository-frostyard-org.md),
[ADR-0011](https://github.com/frostyard/core/blob/main/docs/adr/0011-frostyard-prefixed-package-names.md),
[ADR-0055](https://github.com/frostyard/core/blob/main/docs/adr/0055-publish-debian-packages-through-the-apt-publisher.md))
— nsl ships release tarballs and a Homebrew cask, and its images through its own
signed catalogue on GHCR ([ADR-0012](adr/0012-signed-image-distribution.md)), not
distro packages; and the sysext ADRs
([ADR-0007](https://github.com/frostyard/core/blob/main/docs/adr/0007-frostyard-sysext-filename-pattern.md),
[ADR-0008](https://github.com/frostyard/core/blob/main/docs/adr/0008-sysext-distribution-and-update-contract.md))
— nsl ships no sysext.

When changing behavior covered by one of these, update or supersede the ADR
in frostyard/core first, then change this repository in the same effort.
