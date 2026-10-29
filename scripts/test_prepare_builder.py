"""Root-free checks for the builder preparation step and its place in a build."""
import os
import subprocess
import tempfile
from pathlib import Path
import unittest

ROOT = Path(__file__).resolve().parents[1]
SCRIPT = ROOT/'scripts/prepare-builder.sh'
HOOK = ROOT/'scripts/builder-esp-prune'
RUNNING = '6.12.107+deb13-amd64'
NEWEST = 'linux-image-6.12.113+deb13-amd64'
OLD = 'linux-image-6.12.99+deb13-amd64'
PACKAGES = '\n'.join([f'ii  linux-image-{RUNNING}', f'ii  {NEWEST}', f'ii  {OLD}'])

FAKES = {
    'uname': '''#!/bin/sh
printf 'uname %s\\n' "$*" >> "$FAKE_LOG"
printf '%s\\n' "$FAKE_KERNEL"
''',
    'dpkg-query': '''#!/bin/sh
printf 'dpkg-query %s\\n' "$*" >> "$FAKE_LOG"
for arg in "$@"; do last=$arg; done
if [ "$last" = linux-image-amd64 ]; then
  [ -z "$FAKE_NEWEST_RC" ] || exit "$FAKE_NEWEST_RC"
  printf '%s\\n' "$FAKE_NEWEST"
  exit 0
fi
[ -z "$FAKE_PACKAGES_RC" ] || exit "$FAKE_PACKAGES_RC"
printf '%s\\n' "$FAKE_PACKAGES"
''',
    'dpkg': '''#!/bin/sh
printf 'dpkg %s\\n' "$*" >> "$FAKE_LOG"
case "$*" in
  *--configure*) exit "${FAKE_CONFIGURE_RC:-0}" ;;
  *--purge*) exit "${FAKE_PURGE_RC:-0}" ;;
esac
''',
    'df': '''#!/bin/sh
printf 'df %s\\n' "$*" >> "$FAKE_LOG"
case "$*" in
  *-h*) printf 'Filesystem      Size  Used Avail Use%% Mounted on\\n/dev/vda1       100M   99M  1.0M  99%% /boot/efi\\n' ;;
  *) printf 'Filesystem     1024-blocks  Used Available Capacity Mounted on\\n/dev/vda1 100000 50000 %s 50%% /boot/efi\\n' "$FAKE_ESP_FREE" ;;
esac
''',
    'du': '''#!/bin/sh
printf 'du %s\\n' "$*" >> "$FAKE_LOG"
case "$*" in
  *vmlinuz-*|*initrd.img-*) printf '%s\\t%s\\n' "$FAKE_FILE_KIB" "$2" ;;
  *) printf '%s\\t%s\\n' "$FAKE_ESP_KIB" "$2" ;;
esac
''',
}


class Builder(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.dir = Path(self.tmp.name)
        self.bin = self.dir/'bin'
        self.bin.mkdir()
        for name, content in FAKES.items():
            path = self.bin/name
            path.write_text(content)
            path.chmod(0o755)
        self.log = self.dir/'log'
        self.log.write_text('')
        self.boot = self.dir/'boot'
        self.esp = self.dir/'esp'
        self.boot.mkdir()
        self.esp.mkdir()
        version = NEWEST.removeprefix('linux-image-')
        for name in (f'vmlinuz-{version}', f'initrd.img-{version}'):
            (self.boot/name).write_text('kernel')
        self.env = dict(os.environ, PATH=f'{self.bin}:{os.environ["PATH"]}', FAKE_LOG=str(self.log),
                        FAKE_KERNEL=RUNNING, FAKE_NEWEST=NEWEST, FAKE_PACKAGES=PACKAGES,
                        FAKE_NEWEST_RC='', FAKE_PACKAGES_RC='', FAKE_CONFIGURE_RC='', FAKE_PURGE_RC='',
                        FAKE_ESP_FREE='500000', FAKE_FILE_KIB='1000', FAKE_ESP_KIB='0',
                        NSL_BOOT=str(self.boot), NSL_ESP=str(self.esp), NSL_ETC=str(self.dir/'etc'))

    def run_script(self, **overrides):
        return subprocess.run(['sh', str(SCRIPT)], capture_output=True, text=True,
                              env={**self.env, **overrides})

    def purged(self):
        return [line.split()[-1] for line in self.log.read_text().splitlines() if line.startswith('dpkg --purge')]

    def test_purges_old_kernels_only(self):
        result = self.run_script()
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(self.purged(), [OLD])
        self.assertIn(f'Purging old builder kernel {OLD}', result.stdout)
        # Finishes pending configuration before the space check.
        self.assertIn('dpkg --configure -a', self.log.read_text())

    def test_skips_not_installed_packages(self):
        # dpkg cannot purge what it does not list as installed; leftovers it
        # only keeps configuration for are still removed.
        leftover = 'linux-image-6.12.90+deb13-amd64'
        packages = f'un  linux-image-6.12.80+deb13-amd64\nrc  {leftover}\n{PACKAGES}'
        result = self.run_script(FAKE_PACKAGES=packages)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(self.purged(), [leftover, OLD])

    def test_missing_metapackage_keeps_every_kernel(self):
        result = self.run_script(FAKE_NEWEST='', FAKE_NEWEST_RC='1')
        self.assertEqual(result.returncode, 0, result.stderr)
        # No kernel is known to be the newest, so none may be purged.
        self.assertEqual(self.purged(), [])
        self.assertIn('linux-image-amd64 is not installed; keeping all builder kernels.', result.stdout)
        # Without a newest kernel there is nothing to reserve space for.
        self.assertNotIn('df -P', self.log.read_text())

    def test_no_matching_packages_is_not_a_failure(self):
        result = self.run_script(FAKE_PACKAGES='', FAKE_PACKAGES_RC='1')
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(self.purged(), [])

    def test_failed_configuration_explains_the_builder(self):
        result = self.run_script(FAKE_CONFIGURE_RC='1')
        self.assertEqual(result.returncode, 1)
        self.assertIn('df -h', self.log.read_text())
        self.assertIn('EFI system partition', result.stderr)
        self.assertIn('can be full', result.stderr)
        self.assertIn('limactl delete nsl-image-builder', result.stderr)
        self.assertIn('LIMA_HOME', result.stderr)

    def test_insufficient_esp_space_stops_the_build(self):
        result = self.run_script(FAKE_ESP_FREE='1000', FAKE_FILE_KIB='2000')
        self.assertEqual(result.returncode, 1)
        # Only the ESP is reported, and configuration never starts.
        self.assertIn(f'df -h {self.esp}', self.log.read_text())
        self.assertNotIn('--configure', self.log.read_text())
        self.assertIn('too full', result.stderr)
        self.assertIn('1000 KiB', result.stderr)
        self.assertIn('limactl delete nsl-image-builder', result.stderr)

    def test_space_check_runs_before_configuration(self):
        result = self.run_script(FAKE_ESP_FREE='1000', FAKE_FILE_KIB='2000')
        self.assertEqual(result.returncode, 1)
        log = self.log.read_text()
        self.assertLess(log.index('dpkg --purge'), log.index('df -Pk'))
        # The failing check stops the script before configuration.
        self.assertNotIn('--configure', log)
        # With enough space, configuration is what follows the check.
        self.log.write_text('')
        result = self.run_script()
        self.assertEqual(result.returncode, 0, result.stderr)
        log = self.log.read_text()
        self.assertLess(log.index('df -Pk'), log.index('dpkg --configure -a'))

    def test_existing_copy_on_the_esp_counts_as_space(self):
        # 1000 KiB free plus the 5000 KiB the old copy of the newest kernel takes.
        (self.esp/'systemd'/NEWEST.removeprefix('linux-image-')).mkdir(parents=True)
        result = self.run_script(FAKE_ESP_FREE='1000', FAKE_FILE_KIB='2000', FAKE_ESP_KIB='5000')
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn('has 5 MiB free', result.stdout)

    def test_installs_the_kernel_hook(self):
        for _ in range(2):
            result = self.run_script()
            self.assertEqual(result.returncode, 0, result.stderr)
        installed = Path(self.env['NSL_ETC'])/'kernel'/'postinst.d'/'00-nsl-esp-prune'
        self.assertEqual(installed.read_bytes(), HOOK.read_bytes())
        self.assertEqual(installed.stat().st_mode & 0o777, 0o755)

    def test_missing_kernel_files_count_as_nothing(self):
        version = NEWEST.removeprefix('linux-image-')
        (self.boot/f'vmlinuz-{version}').unlink()
        (self.boot/f'initrd.img-{version}').unlink()
        result = self.run_script(FAKE_ESP_FREE='1000', FAKE_FILE_KIB='999999')
        self.assertEqual(result.returncode, 0, result.stderr)


class Hook(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.dir = Path(self.tmp.name)
        self.bin = self.dir/'bin'
        self.bin.mkdir()
        uname = self.bin/'uname'
        uname.write_text('#!/bin/sh\nprintf "%s\\n" "$FAKE_KERNEL"\n')
        uname.chmod(0o755)
        self.esp = self.dir/'esp'
        self.etc = self.dir/'etc'
        (self.esp/'loader'/'entries').mkdir(parents=True)
        self.etc.mkdir()
        self.env = dict(os.environ, PATH=f'{self.bin}:{os.environ["PATH"]}', FAKE_KERNEL=RUNNING,
                        NSL_ESP=str(self.esp), NSL_ETC=str(self.etc))

    def run_hook(self, *args, **overrides):
        return subprocess.run(['sh', str(HOOK), *args], capture_output=True, text=True,
                              env={**self.env, **overrides})

    def kernel(self, token, version, suffix=''):
        (self.esp/'loader'/'entries'/f'{token}-{version}{suffix}.conf').write_text('title')
        directory = self.esp/token/version
        directory.mkdir(parents=True)
        return directory

    def entries(self):
        return sorted(path.name for path in (self.esp/'loader'/'entries').iterdir())

    def test_prunes_old_copies_and_their_entries(self):
        incoming = '6.12.113+deb13-amd64'
        (self.etc/'kernel').mkdir()
        (self.etc/'kernel'/'entry-token').write_text('systemd\n')
        keep = self.kernel('systemd', incoming)
        running = self.kernel('systemd', RUNNING)
        old = self.kernel('systemd', '6.12.99+deb13-amd64')
        older = self.kernel('systemd', '6.12.90+deb13-amd64', '+3-0')
        # Another token's tree, and a stray loader entry, stay untouched.
        other = self.kernel('othertoken', '6.12.50+deb13-amd64')
        result = self.run_hook(incoming)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertTrue(keep.is_dir())
        self.assertTrue(running.is_dir())
        self.assertFalse(old.exists())
        self.assertFalse(older.exists())
        self.assertTrue(other.is_dir())
        self.assertEqual(self.entries(), sorted([f'systemd-{incoming}.conf', f'systemd-{RUNNING}.conf',
                                                 'othertoken-6.12.50+deb13-amd64.conf']))
        self.assertIn('Pruned the ESP copy of kernel 6.12.99+deb13-amd64', result.stdout)

    def test_entry_token_wins_over_machine_id(self):
        (self.etc/'kernel').mkdir()
        (self.etc/'kernel'/'entry-token').write_text('nsl\n')
        (self.etc/'machine-id').write_text('abc123\n')
        from_token = self.kernel('nsl', '6.12.99+deb13-amd64')
        from_machine_id = self.kernel('abc123', '6.12.99+deb13-amd64')
        result = self.run_hook('6.12.113+deb13-amd64')
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertFalse(from_token.exists())
        self.assertTrue(from_machine_id.is_dir())

    def test_machine_id_is_the_fallback_token(self):
        (self.etc/'machine-id').write_text('abc123\n')
        old = self.kernel('abc123', '6.12.99+deb13-amd64')
        result = self.run_hook('6.12.113+deb13-amd64')
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertFalse(old.exists())

    def test_nothing_happens_without_a_target(self):
        (self.etc/'machine-id').write_text('abc\n')
        kept = self.kernel('abc', '6.12.99+deb13-amd64')
        slash = self.dir/'slash'
        (slash/'kernel').mkdir(parents=True)
        (slash/'kernel'/'entry-token').write_text('a/b\n')
        for label, args, env in [('no argument', (), {}),
                                 ('no ESP', ('6.12.113+deb13-amd64',), {'NSL_ESP': str(self.dir/'missing')}),
                                 ('no token', ('6.12.113+deb13-amd64',), {'NSL_ETC': str(self.dir/'empty')}),
                                 ('token with a slash', ('6.12.113+deb13-amd64',), {'NSL_ETC': str(slash)})]:
            with self.subTest(label):
                result = self.run_hook(*args, **env)
                self.assertEqual((result.returncode, result.stdout, result.stderr), (0, '', ''))
                self.assertTrue(kept.is_dir())


class BuildOrder(unittest.TestCase):
    def test_the_build_prepares_the_builder_before_apt(self):
        text = (ROOT/'scripts/build-image.sh').read_text()
        self.assertIn('cp scripts/prepare-builder.sh "$staging/prepare-builder.sh"', text)
        self.assertIn('cp scripts/builder-esp-prune "$staging/builder-esp-prune"', text)
        self.assertIn('cp scripts/builder-mkosi-retry "$staging/builder-mkosi-retry"', text)
        wrapped = 'PYTHONPATH="$build_dir/mkosi" python3 "$(dirname "$1")/builder-mkosi-retry" python3 -m mkosi'
        self.assertEqual(text.count(wrapped), 2)
        self.assertEqual(text.count('python3 -m mkosi'), 2)
        prepare = text.index('sh "$(dirname "$1")/prepare-builder.sh"')
        self.assertLess(prepare, text.index('apt-get update'))
        self.assertLess(text.index('apt-get install'), text.index('apt-get autoremove -y --purge'))


if __name__ == '__main__':
    unittest.main()
