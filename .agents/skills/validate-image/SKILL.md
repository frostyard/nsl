---
name: validate-image
description: Build and validate an nsl distribution image in disposable VMs when adding a profile or changing guest boot, security, or maintenance integration.
---

# Validate a distribution image

Work from the repository root. Read [the guest contract](../../../docs/specs/guest-images.md), [image build instructions](../../../image/README.md) and the relevant report linked from [the distribution matrix](../../../docs/plans/distribution-support.md).

## Build and exercise

1. Keep package, security and boot differences in the image adapter. Host lifecycle, backup and storage commands remain distribution independent. Bump the profile revision when rebuilding changed inputs; the builder refuses existing artifacts.
2. Run `make build` and `scripts/build-image.sh --distribution DISTRO --release RELEASE`, using an existing Lima installation through `NSL_LIMACTL` if needed. Retain the build log under `build/image/evidence/`. The script owns its builder, serializes builds and stops it on exit. Do not edit the shell script while it is running.
3. Run the shared suite against the completed generic raw image:

   ```sh
   python3 scripts/probe-distribution.py \
     --image build/image/share/BUILD.raw \
     --home /absolute/unused/test-home \
     --project /absolute/unused/test-project \
     --evidence build/native/evidence/BUILD
   ```

   All three test paths must be unused. This creates disposable guests and removes them after success. Failed guests are stopped and retained for diagnosis. The evidence directory contains private backup archives; never publish it wholesale.
4. Inspect `results.json`, each lifecycle case's `pass` field, `maintenance.json` and `storage.json`. A built image, successful boot or one passing subtest is insufficient. Verify the raw SHA256 matches the tested artifact and record the package manifest, image descriptor and host versions.
5. Keep kernel reinstall and version-upgrade results distinct. If the transaction installs a new version, the reboot must run it. Native SELinux/AppArmor policy must remain enabled. Capture guest journal/audit output before stopping a failing guest; minimal images may lack diagnostic utilities such as `ps` or `strace`.
6. Apply fixes to the image source, then validate a fresh image with unused test paths. A manually repaired diagnostic VM is evidence for the fix, not acceptance of the published base. Remove obsolete diagnostic guests through `nsl stop` and `nsl remove --yes` within existing task authorization.
7. Run `make ci`, update the exact build/hash/coverage in the distribution report, and follow the user's commit/publication scope. Keep documentation in a separate commit when requested. Do not infer tag or registry-publication authorization from this procedure.

## Known distinctions

- SUSE's `getenforce` needs the root execution PATH. Its native policy can have permissive domains while global mode is enforcing; record both accurately.
- Leap needs `container-selinux` before container storage is created. SUSE kernel RPM hooks need `update-bootloader` and `LOADER_TYPE="systemd-boot"` to update UKIs.
- Arch uses its own pacman UKI hook; a failed generation must preserve the previous managed boot entry.
- A build may be slow while downloading or generating initrds. Diagnose process/log progress before terminating it. Failed build workspaces are retained inside the owned builder; successful ones are removed.
