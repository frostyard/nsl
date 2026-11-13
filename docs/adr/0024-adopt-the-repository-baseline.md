# 0024 — Adopt the frostyard repository baseline

- **Status:** Accepted
- **Date:** 2026-11-06

## Context

frostyard/core holds every code repository to 1 checklist, rows B1 to B17 of
[core's repository baseline spec](https://github.com/frostyard/core/blob/main/docs/specs/repository-baseline.md)
([core ADR-0071](https://github.com/frostyard/core/blob/main/docs/adr/0071-hold-every-code-repository-to-one-baseline.md)).
A weekly sweep checks each row. nsl already had the make gate triad, `mise.toml`
and `mise.lock`, pinned least-privilege workflows, svu releases, an MIT
license and the ProperDocs and MaterialX site. It did not have:

- the `.cursorrules` alias, `docs/org-adrs.md`, a risk-path list and its
  caller workflow, a PR template with a risk tier, a Dependabot configuration
  or a `SECURITY.md`;
- a check that `docs/README.md` indexes every document, or that
  `.memory/corrections.jsonl` stays append-only;
- a `README.md` in the shape of core's README spec (row B17,
  [core ADR-0072](https://github.com/frostyard/core/blob/main/docs/adr/0072-write-every-readme-to-one-standard.md)).

`docs/plans/` held 21 files, and 18 of them described finished work. A root
`PLAN.md` and `experiments/vmspawn/` described the retired prototype.

## Decision

- **Template gate.** `make verify` also runs `make verify-template`:
  `scripts/check-template.mjs` and `test/check-template.test.mjs`, byte-identical
  copies from bketelsen/agentic-template with `scripts/check-corrections.mjs`
  and `scripts/lib/risk-paths.mjs`. It checks the instruction aliases, the
  `docs/README.md` index, the risk-path list, the corrections file and the
  hash pins of the 2 files copied from core. `mise.toml` pins Node for it.
- **Corrections.** `make ci` runs `corrections-base` first: with `BASE` set,
  `.memory/corrections.jsonl` must only grow against `BASE`. CI sets `BASE`
  to the pull request's base. `ci.yml` (formerly `test.yml`) still runs
  `make ci` in 1 job, which [core ADR-0073](https://github.com/frostyard/core/blob/main/docs/adr/0073-let-ci-run-the-make-ci-targets-as-separate-jobs.md)
  allows; the job names stay.
- **Risk paths.** `.github/risk-paths.json` lists the governance files, the
  release configuration, the image build and publication scripts, the trust
  root, the image verification code, the image layers, the VM launch and
  recovery, the VM agent, the SSH identities, and machine removal, disk growth,
  image update, export and import; each `why` names a
  category of core's risk-paths spec. `.github/workflows/risk-paths.yml` calls
  core's labeller ([core ADR-0068](https://github.com/frostyard/core/blob/main/docs/adr/0068-label-pull-requests-that-touch-high-risk-paths.md)).
- **Plans hold open work only.** A finished plan is deleted when its lasting
  facts are in an ADR, a design or a spec; git history keeps it, and links to
  it point at commit `e617966`. The requirements that the shared-VM
  implementation carried moved to the [lifecycle design](../design/lifecycle.md#requirements-that-must-not-regress),
  and its open ideas to [unscheduled work](../plans/later-work.md).
- **README.** `README.md` is a `tool` README. Guides and reference stay on the
  site.

## Consequences

- A contributor runs `mise install`, then `make check` or `make ci`. The same
  targets run in CI, in the release workflow and in the image publication.
- A pull request that touches a listed risk path gets the `risk:high` label
  and needs its owner's review before it is ready.
- A new document under `docs/` fails `make verify` until `docs/README.md`
  links it.
- The fleet golangci-lint 2.15.0 reports 2 errcheck findings in
  `cmd/nsl-agent`, so the linter bump and Go 1.27 (row B11) are a separate
  change.

## Alternatives considered

- **Split `make ci` into separate CI jobs:** rejected. 1 job is allowed, and the
  ruleset requires the current job names.
- **Keep the finished plans as history in `docs/plans/`:** rejected. They
  describe the code as it was, the index mixed them with open work, and git
  history keeps them.

## References

- Shapes: [lifecycle design](../design/lifecycle.md), [unscheduled work](../plans/later-work.md), [org ADRs](../org-adrs.md)
- Builds on: [ADR-0002](0002-agent-portable-instruction-surface.md),
  [core ADR-0071](https://github.com/frostyard/core/blob/main/docs/adr/0071-hold-every-code-repository-to-one-baseline.md),
  [core ADR-0072](https://github.com/frostyard/core/blob/main/docs/adr/0072-write-every-readme-to-one-standard.md),
  [core ADR-0043](https://github.com/frostyard/core/blob/main/docs/adr/0043-pin-repository-tools-in-mise-and-name-the-verify-gate.md),
  [core ADR-0044](https://github.com/frostyard/core/blob/main/docs/adr/0044-expose-the-make-gate-triad-in-every-repository.md)
- Amended in part by: [core ADR-0081](https://github.com/frostyard/core/blob/main/docs/adr/0081-store-each-correction-in-a-file-of-its-own.md)
