# Plan: Unscheduled work after the shared VM

The open items that the finished plans of the shared-VM design left behind, in 1 place. Each item becomes a plan of its own, from [TEMPLATE.md](TEMPLATE.md), when someone takes it up. The finished plans are in git history at [`e617966`](https://github.com/frostyard/nsl/tree/e617966e80f6534aa6525f016def59ab58c1d864/docs/plans); the [implementation plan](https://github.com/frostyard/nsl/blob/e617966e80f6534aa6525f016def59ab58c1d864/docs/plans/shared-vm-implementation.md) is the record of how the current design was built and proved.

Open work with a plan of its own: [Azure Linux 4.0, Phase 2](azure-linux.md) and [cloud-init provisioning](cloud-init-provisioning.md).

## Phase 1 — Evidence still missing

- Run the storage acceptance on an Ubuntu 26.04 host with an ext4 filesystem, and record the time of `recover` for the shared VM and for an isolated machine. Earlier runs had btrfs only.
- **Done when:** the times are recorded here, or in the plan that next changes storage.

## Phase 2 — Daily-work reliability

- Accept the shared VM and an isolated machine across a host reboot and a suspend and resume, network changes (VPN up and down, split DNS, a new network), a full data disk, an interrupted image download or `update`, and a killed VM or agent. The prototype's roadmap named these gates; the shared-VM design has not run them.
- **Done when:** each case has a recorded result, and each failure has an issue or a fix.

## Later / ideas

Performance:

- Size the VM to its running machines (virtio-mem or balloon targets); one machine costs about twice a single VM.
- Trim the desktop's memory: the first desktop session costs about 100 MiB, mostly ffmpeg and codec libraries that `waypipe --no-gpu` never uses. A Waypipe built without video support, or free-page reporting to the host, would recover it.
- Keep agent sessions warm, to recover the transport overhead that the experiment measured at 48 ms.
- Fold readiness and `start` into the first request: each command makes 3 SSH round trips (`identity`, `start`, `run`), which cost most of the 89 ms no-op median.
- Measure and bound virtiofsd memory under file load.
- virtiofs DAX, and KSM across machines.
- Per-machine cgroup limits inside a global VM budget.
- Find the 1.3–2 s Ctrl-C delay in the SSH transport.

Host integration:

- Translate absolute host symlinks that point into shared trees, handle `/run/media/USER` when it appears after the VM starts, and host automounts (the [CLI's open interface questions](../specs/cli.md#open-interface-questions)).
- Host menu entries ([ADR-0022](../adr/0022-desktop-menu-entries.md)): icon themes beyond `hicolor`, translation of `%f` and `%U` so that a menu entry can pass files and URLs, and terminal integration through OSC 3008 context markers.
- Desktop: clipboard, input methods, scaling and multiple monitors; X11 applications through Xwayland; audio, with a transport made for the protocol (a raw PipeWire socket tunnel is not enough) and the microphone as a separate grant.
- Through the host broker: notifications and file choosers. Full portal coverage, drag-and-drop, screen sharing, global shortcuts and accessibility.
- GPU rendering (virtio-gpu with virglrenderer or rutabaga), apart from window forwarding; Vulkan, video codecs and USB device forwarding.
- Private user namespaces with idmapped virtiofs mounts, if trusted machines ever need isolation from each other.

Images:

- More releases of the existing families, each a profile that passes acceptance: Debian 12, Fedora 43, CentOS Stream 9; AlmaLinux and Rocky Linux in the `rpm` family. The pinned recipes carry them.
- SUSE Linux Enterprise as a separate track, with its access and redistribution terms ([ADR-0009](../adr/0009-distribution-neutral-guest-contract.md)).
- A development branch (Debian testing, Fedora Rawhide) that fails acceptance blocks the whole catalogue until its profile is fixed; `operation=refresh` keeps the current catalogue alive meanwhile. If that happens often, the publisher could carry the last accepted image forward for that branch.
- arm64 VM and machine images: a separate image and hardware gate.
- Delivery: delta downloads, once measured against their complexity and recovery cost; mirrors, other registries and private authentication; offline bundles that carry enough metadata and signatures for the offline trust policy ([ADR-0015](../adr/0015-image-verification-and-catalogue-policy.md)).

## Open questions

- Should nsl's own state move out of the shared trees? A machine that is not isolated sees the whole home, as [ADR-0016](../adr/0016-wsl-style-machines.md) intends, and so nsl's state and private keys under the default `NSL_HOME` (`~/.local/share/nsl`). The shared-VM experiment left this open.
- Should the memory budget cover more than 4 idle machines, now that the catalogue has 11 images? The gate stays at 4, as the experiment measured, until a use case asks for more ([requirements that must not regress](../design/lifecycle.md#requirements-that-must-not-regress)).

## References

- Builds on: [machine lifecycle](../design/lifecycle.md), [image publication](../design/image-publication.md), [machine images](../specs/machine-images.md), [CLI](../specs/cli.md).
- Decisions: [ADR-0016](../adr/0016-wsl-style-machines.md), [ADR-0017](../adr/0017-shared-vm-and-machine-images.md), [ADR-0022](../adr/0022-desktop-menu-entries.md).
