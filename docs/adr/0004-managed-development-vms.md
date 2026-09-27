# 0004 — Manage full development VMs with native host integration

- **Status:** Runtime and image choices superseded by [ADR-0005](0005-vmspawn-and-nspawn-images.md); explicit capability grants superseded by [ADR-0016](0016-wsl-style-machines.md)
- **Date:** 2026-09-26

## Context

The product goal is the WSL2 user experience on atomic Linux: persistent Linux environments with native command, file, network and desktop integration, backed by real VMs. The current nspawn.org wrapper follows [ADR-0003](0003-wrap-nspawn-for-development.md), but is a two-hour-old proof of concept with no users. It creates no compatibility or migration obligation.

WSL2's shared utility VM is one way to provide that experience. Reproducing its container topology is optional. nspawn.org's runtime manages containers, while its separate image recipe repository already includes a bootable disk profile. The [research and roadmap](../plans/wsl2-equivalent.md) distinguish image reuse, runtime reuse and launcher selection.

## Decision

Use **one full Linux VM per nsl environment**, with its distribution, systemd, package manager and applications running directly in the VM. Do not require nspawn inside it. The host application manages launch, command transport, files, networking, desktop integration and persistent storage.

Use **Lima 2.2.0 with QEMU/KVM** for the prototype. It demonstrated unprivileged lifecycle, SSH, project sharing and automatic localhost forwarding on the current host. A bounded vmspawn probe launched QEMU, but did not establish the same end-to-end workflow. Missing vhost-vsock access is a host permission issue, not an architectural blocker. Select Lima for its working integration, not because vmspawn cannot work.

Keep **Go** for the small host CLI and an embedded **Python 3** helper for structured guest command execution. This avoids implementing SSH, PTY and display protocols. Language changes remain possible if later requirements justify them.

Start with Debian's official generic cloud image, pinned by digest, and provision integration at boot. nspawn's bootable mkosi disk recipes remain a future image-building option. No upstream nspawn code or recipes are copied into this implementation; the MIT source remains separate from the downloaded tools. A derived image/signature pipeline is not yet implemented.

Replace the proof-of-concept CLI and implementation where useful. Do not build compatibility backends or migration machinery. Retain its ownership-validation and argument-array execution principles. Existing local machines and files remain untouched unless explicitly selected for cleanup.

The user explicitly authorizes a complete rewrite, including language and dependency changes. Select Go, Rust or another implementation according to demonstrated reuse and maintenance cost. Prefer established transport/display implementations over recreating their protocols. Update canonical instructions, builds and release tooling to reflect the selected implementation; no compatibility exception is needed to replace the current Go CLI.

This supersedes ADR-0003. The current design and CLI contract describe the new implementation. The [experiment report](../plans/vm-proof-of-concept.md) records evidence and open gates; acceptance here does not declare desktop parity or production readiness.

## Consequences

- One VM equals one user-facing environment, with its own kernel, disks and lifecycle. Host integrations cross a single guest boundary.
- Multiple environments repeat kernel/VM overhead. Benchmark one, two and four VMs before optimizing for density.
- nsl takes responsibility for persistent VM state and integration updates; ordinary distro package/kernel updates remain in the guest package manager. Updating nsl must not replace a user's customized guest root.
- The image catalogue and build recipes can be reused independently of the nspawn service. End users need VM runtime prerequisites but need not install nspawn.org or mkosi.
- GUI forwarding, file watchers, graphics acceleration and portals still require end-to-end validation. A real VM does not by itself provide native desktop behavior.
- VM isolation does not revoke access granted through shared projects, desktop services or host actions. Preserve explicit capability grants and user-scoped host processes.

## Alternatives considered

- **nspawn containers inside a utility VM:** shares a guest kernel across environments; deferred because its extra integration and capability boundaries have no demonstrated benefit for the intended workload yet.
- **Host-only nspawn:** useful proof of concept; does not supply kernel independence.
- **vmspawn or direct QEMU management:** viable alternatives, but require more transport and network integration than the demonstrated Lima workflow. Revisit if Lima blocks a measured requirement.
- **Fork nspawn immediately:** plausible image-code reuse, but namespace execution, mounts, bridge networking and lifecycle need VM replacements. Source reuse should earn its maintenance cost.
- **Incus/libvirt:** established VM systems; reconsider where their management capabilities outweigh additional host integration.

## References

- Measured implementation: [VM proof of concept](../plans/vm-proof-of-concept.md).
- Research, source links, alternatives and delivery gates: [WSL2-equivalent roadmap](../plans/wsl2-equivalent.md).
- Current proof of concept: [lifecycle design](../design/lifecycle.md), [CLI contract](../specs/cli.md).
- Existing decision: [ADR-0003](0003-wrap-nspawn-for-development.md).
