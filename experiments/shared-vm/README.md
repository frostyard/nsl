# Shared-VM experiment

Experiment code for the [shared-VM plan](../../docs/plans/shared-vm-experiment.md): one nsl-owned VM that hosts machines as systemd-nspawn containers, compared against one VM per machine. It is separate from the nsl CLI and never reads or changes the CLI's state or units. Results and interpretation live in the plan; raw evidence is written to ignored `build/shared-vm/evidence/`.

## Components

- `build.sh`: builds `nsl-shared-vm-trixie-x86-64-v1.raw` from the Debian trixie profile plus `layer/`, inside a disposable Lima builder. Builder steps mirror `scripts/build-image.sh`; source pins are read from it.
- `layer/`: adds `systemd-container` and a machine-storage disk. On first boot, `nsl-machines-storage` formats the one blank non-root disk as btrfs labelled `nsl-machines`, and `var-lib-machines.mount` mounts it at `/var/lib/machines`. A disk with any other signature is refused.
- `driver.py`: lifecycle, authenticated readiness and acceptance checks for the shared VM. It shares the [ADR-0016](../../docs/adr/0016-wsl-style-machines.md) allowlist (home, `/run/media/USER`, `/mnt`, whichever exist) at canonical paths under `/mnt/host`.
- `machines.py`: Phase 2 machines as systemd-nspawn containers inside the VM, and the entry-method comparison.
- `nspawn-hub-cosign.pub`: the hub's project signing key, copied from `nspawn/mkosi-definitions` at `68263d05169784f44168ca65241d989865ed011b`, the commit the image recipes are pinned to.

## Requirements

The host prerequisites of the main CLI (`nsl doctor`), plus Lima 2.2.0 for the image build. No host packages, sudo, device-permission or sudoers changes are needed. Build dependencies stay inside the builder.

## Build and run

```sh
source build/poc/env.sh          # NSL_LIMACTL for a locally downloaded Lima
experiments/shared-vm/build.sh
experiments/shared-vm/driver.py init build/shared-vm/share/nsl-shared-vm-trixie-x86-64-v1.raw
experiments/shared-vm/driver.py check              # Phase 1 evidence
experiments/shared-vm/driver.py check --submounts  # also lists nested host mounts from the guest
experiments/shared-vm/driver.py exec -- findmnt /var/lib/machines
experiments/shared-vm/driver.py stop
```

`init` refuses an existing state directory. State defaults to `~/.local/share/nsl-shared-vm`; set `NSL_SHARED_VM_STATE` for a disposable copy. The builder's Lima home defaults to `~/.local/share/nsl-shared-vm-build` because Lima socket paths must stay under 108 bytes. The VM runs as user unit `nsl-shared-vm.service`, which the driver controls only when its description matches the state's ID. Launch refuses a vsock CID that already answers SSH.

`check` writes a host scratch directory under `~/.cache/nsl-shared-vm-probe/` and removes it afterwards. `--submounts` lists nested host mounts under shared trees from the guest and records only entry counts. Listing a network share can wait up to 15 seconds per mount.

The whole home is visible to the VM by design, including nsl state and this experiment's private key. That matches ADR-0016's trust model; do not run untrusted software in this VM.

## Machines

```sh
experiments/shared-vm/machines.py pull debian:13        # verify and cache only
experiments/shared-vm/machines.py create debian debian:13
experiments/shared-vm/machines.py create fedora fedora:44
experiments/shared-vm/machines.py start debian
experiments/shared-vm/machines.py exec debian -- id
experiments/shared-vm/machines.py exec --tty debian -- bash -l
experiments/shared-vm/machines.py exec --method nsenter --root fedora -- dnf --version
experiments/shared-vm/machines.py check                 # Phase 2 evidence for debian and fedora
experiments/shared-vm/machines.py remove fedora
```

`pull` fetches the manifest from hub.nspawn.org and accepts it only if a DSSE signature bundle verifies with `nspawn-hub-cosign.pub` and names the manifest digest. The keyless Sigstore signature is not checked. Layers are cached by digest in `~/.cache/nsl-shared-vm/blobs`. The VM reads them through `/mnt/host`; that shortcut is an experiment convenience, not a design.

`create` runs as VM root. It imports the layer into `/var/lib/machines/NAME`, adds your account with your UID and primary GID, locks root's password and sets the hostname. It masks the image's networkd and resolved, installs `pam_systemd` and `sudo` (Debian and Fedora only), and writes `/etc/systemd/nspawn/NAME.nspawn`. Machine records live in the state directory's `machines/`. Flags for `exec` go before the machine name. `--method run` is `systemd-run --machine` with the adjustments recorded in the [plan](../../docs/plans/shared-vm-experiment.md).

## Clean up

```sh
experiments/shared-vm/driver.py stop
rm -rf ~/.local/share/nsl-shared-vm ~/.cache/nsl-shared-vm
LIMA_HOME=~/.local/share/nsl-shared-vm-build "$NSL_LIMACTL" delete nsl-shared-vm-builder
```
