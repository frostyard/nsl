---
name: validate-image
description: Build and validate the nsl VM image, and machine images once they exist, in disposable VMs when changing the image layers, the agent's boot services or machine integration.
---

# Validate an nsl image

Work from the repository root. Read [the VM image contract](../../../docs/specs/vm-image.md), [the machine-image contract](../../../docs/specs/machine-images.md), [image build instructions](../../../image/README.md) and the current phase of [the implementation plan](../../../docs/plans/shared-vm-implementation.md).

## The VM image

1. Keep distro and boot details in the image layer; the host CLI stays independent of them. Bump `image/vm/profile.json`'s revision when inputs change; the builder refuses existing artifacts.
2. Run `make build` and `scripts/build-image.sh --role vm`, using a local Lima through `NSL_LIMACTL` if needed. Keep the build log under `build/image/evidence/`. Do not edit the script while it runs.
3. Run the acceptance probe against the new raw image:

   ```sh
   python3 scripts/probe-vm.py --nsl build/nsl --image build/image/share/BUILD.raw \
     --evidence build/image/evidence/BUILD-probe.json
   ```

   It creates disposable state directories under `~/.local/share/nsl-probe-vm-*`, stops their VMs and removes them. It needs no root and changes nothing outside those directories and a scratch directory under `~/.cache/nsl-probe-vm/`.
4. Inspect every check's `pass` field. A booting image or one passing check is not acceptance. Record the raw SHA256, the descriptor's build ID and the host versions from the evidence.
5. When a check fails, read the VM's console through `journalctl --user -u nsl-UID-vm-ID.service`. Look inside a running VM through the agent's `vm` operation; the host has no other shell into it.
6. Fix the image source and validate a fresh build. A hand-repaired VM is evidence for a fix, not acceptance of an image.
7. Run `make ci` and record the build and results in the implementation plan. Follow the user's commit and publication scope; do not infer authorization to tag or publish.

## Machine images

Phase 4 of the implementation plan adds machine-image builds and `scripts/probe-machines.py`. Until then this skill covers only the VM image.
