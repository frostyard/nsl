# Shared-VM experiment

Experiment code for the [shared-VM plan](../../docs/plans/shared-vm-experiment.md): one nsl-owned VM that hosts machines as systemd-nspawn containers, compared against one VM per machine. It is separate from the nsl CLI and never reads or changes the CLI's state or units. Results and interpretation live in the plan; raw evidence is written to ignored `build/shared-vm/evidence/`.

## Components

- `build.sh`: builds `nsl-shared-vm-trixie-x86-64-v1.raw` from the Debian trixie profile plus `layer/`, inside a disposable Lima builder. Builder steps mirror `scripts/build-image.sh`; source pins are read from it.
- `layer/`: adds `systemd-container` and a machine-storage disk. On first boot, `nsl-machines-storage` formats the one blank non-root disk as btrfs labelled `nsl-machines`, and `var-lib-machines.mount` mounts it at `/var/lib/machines`. A disk with any other signature is refused.
- `driver.py`: lifecycle, authenticated readiness and acceptance checks for the shared VM. It shares the [ADR-0016](../../docs/adr/0016-wsl-style-machines.md) allowlist (home, `/run/media/USER`, `/mnt`, whichever exist) at canonical paths under `/mnt/host`.

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

## Clean up

```sh
experiments/shared-vm/driver.py stop
rm -rf ~/.local/share/nsl-shared-vm
LIMA_HOME=~/.local/share/nsl-shared-vm-build "$NSL_LIMACTL" delete nsl-shared-vm-builder
```
