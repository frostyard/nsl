# 0018 — User documentation site

- **Status:** Accepted
- **Date:** 2026-09-28

## Context

nsl's user instructions live in `README.md`, a single page. The system now has enough surface for one page to strain: seven machine images, host files, ports, the desktop, editors, isolated machines, archives, images, storage and a configuration file. `docs/` is taken by maintainer and agent documentation (ADRs, designs, specs and plans), written as contracts rather than guides.

Frostyard projects keep a documentation site in `site/`, next to the source, so that pages change in the same pull requests as behavior. The upstream MkDocs is no longer maintained, and Material for MkDocs is in maintenance mode.

## Decision

**A static site in `site/`.** User documentation lives in `site/content/` as Markdown and is built by [ProperDocs](https://properdocs.org), the maintained MkDocs 1.6 successor, with the [MaterialX](https://github.com/jaywhj/mkdocs-materialx) theme. `site/properdocs.yml` configures it; `site/content/stylesheets/frostyard.css` applies the Frostyard palette. The output goes to `site/dist/`, which is never committed.

**Pinned tools.** `site/requirements.in` names MaterialX; `site/requirements.txt` locks every dependency with hashes. `make site` installs them into `build/site-venv` and builds in strict mode, so a broken link or anchor fails. `make site-serve` previews the site.

**GitHub Pages.** `.github/workflows/site.yml` builds the site for pull requests that touch it, and builds and deploys it to GitHub Pages from `main`, at `https://frostyard.github.io/nsl/`. Actions are pinned by commit, as in the other workflows.

**Content boundary.** The site explains nsl to its users: installation, guides, concepts and reference. The specs remain the contract. A change to user-visible behavior updates the affected site pages in the same change. `README.md` stays the short entry on GitHub and links to the site.

**The icon.** `assets/nsl.svg` is the single source of the icon; `site/content/assets/nsl.svg` links to it.

## Consequences

- Documentation gets navigation, search (Pagefind) and a stable URL.
- The site needs Python and its own workflow. `make ci` does not build it, so Go-only changes do not need the Python toolchain.
- Pages that restate specs can drift from them. Reviewers check the site against the spec when behavior changes.
- GitHub Pages must be enabled for the repository, with GitHub Actions as its source.

## Alternatives considered

- **The Frostyard Astro docs shell:** the organization's own template deploys to Cloudflare Workers and needs Node. MaterialX gives navigation, search and theming with a smaller toolchain, and GitHub Pages needs no extra secrets.
- **Material for MkDocs with MkDocs:** both are in maintenance mode.
- **Zensical:** new, and incompatible with the MkDocs plugin ecosystem.
- **Publishing `docs/`:** those documents are contracts and history for maintainers, not user guides.
- **README only:** one page no longer fits the system.

## References

- Site: [`site/properdocs.yml`](../../site/properdocs.yml), [pages](../../site/content/), [workflow](../../.github/workflows/site.yml).
- Instructions: [AGENTS.md](../../AGENTS.md), [README.md](../../README.md). Contracts the site explains: [CLI](../specs/cli.md), [machine images](../specs/machine-images.md), [image delivery](../specs/image-delivery.md).
- Builds on [ADR-0001](0001-record-architecture-decisions.md) and [ADR-0002](0002-agent-portable-instruction-surface.md).
