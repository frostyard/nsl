"""Root-free checks for the image input boundary and layer composition."""
import importlib.util
import json
import os
import subprocess
from pathlib import Path
import tempfile
import unittest

spec = importlib.util.spec_from_file_location('compose', Path(__file__).with_name('compose-image.py'))
compose = importlib.util.module_from_spec(spec)
spec.loader.exec_module(compose)
ROOT = Path(__file__).resolve().parents[1]


class ImageProfiles(unittest.TestCase):
    def test_unsupported_inputs_are_rejected(self):
        for args in [('unknown', None, 'x86-64'), ('../../common', None, 'x86-64'),
                     ('ubuntu', 'trixie', 'x86-64'), ('opensuse', '../../common', 'x86-64'), ('debian', None, 'arm64')]:
            with self.subTest(args=args), self.assertRaises(ValueError):
                compose.select(ROOT, *args)

    def test_builder_rejects_unsupported_options_before_tools(self):
        for args in [('--distribution', 'unknown'), ('--release', 'noble'),
                     ('--architecture', 'arm64'), ('--destination', '/should-not-exist')]:
            with self.subTest(args=args):
                result = subprocess.run(['bash', str(ROOT/'scripts/build-image.sh'), *args],
                                        env=dict(os.environ, NSL_LIMACTL='/must-not-run'),
                                        capture_output=True, text=True)
                self.assertNotEqual(result.returncode, 0)
                self.assertNotIn('/must-not-run', result.stderr)

    def test_multiple_releases_select_distinct_layers(self):
        with tempfile.TemporaryDirectory() as tmp:
            for release in ('16.0', 'tumbleweed'):
                profile = compose.select(ROOT, 'opensuse', release)
                destination = Path(tmp)/release
                compose.compose(ROOT, destination, profile, 'recipes', 'mkosi')
                config = (destination/'mkosi.local.conf').read_text()
                self.assertEqual(profile['release'], release)
                self.assertEqual('Snapshot=20260923' in config.splitlines(), release == 'tumbleweed')
                descriptor = json.loads((destination/'overlay/usr/lib/nsl/image.json').read_text())
                self.assertEqual(descriptor['os_id'], 'opensuse-tumbleweed' if release == 'tumbleweed' else 'opensuse-leap')

    def test_complete_separate_images_and_no_overwrite(self):
        with tempfile.TemporaryDirectory() as tmp:
            for distribution, filesystem in [('debian', 'btrfs'), ('ubuntu', 'ext4'), ('fedora', 'btrfs'), ('centos', 'ext4'), ('arch', 'btrfs')]:
                destination = Path(tmp)/distribution
                profile = compose.select(ROOT, distribution)
                name = compose.compose(ROOT, destination, profile, 'recipes-pin', 'mkosi-pin')
                descriptor = json.loads((destination/'overlay/usr/lib/nsl/image.json').read_text())
                self.assertEqual(descriptor['root_filesystem'], filesystem)
                self.assertEqual(descriptor['build_id'], name)
                self.assertEqual(descriptor['recipes_revision'], 'recipes-pin')
                self.assertEqual(len(descriptor['integration_sha256']), 64)
                for target in ['nsl-setup', 'nsl-exec', 'nsl-platform-setup']:
                    self.assertTrue((destination/'overlay/usr/local/libexec'/target).is_file())
                self.assertTrue((destination/'overlay/etc/systemd/system/nsl-ssh.socket').is_file())
                self.assertEqual((destination/'overlay/etc/systemd/system-generators/systemd-ssh-generator').readlink(), Path('/dev/null'))
                self.assertIn('root=UUID=', (destination/'overlay/usr/local/libexec/nsl-platform-setup').read_text())
                self.assertNotIn('root=UUID=', (destination/'overlay/usr/local/libexec/nsl-setup').read_text())
                if filesystem == 'ext4':
                    self.assertIn('Format=ext4', (destination/'mkosi.repart/10-root.conf').read_text())
                # Failed retries cannot replace a prepared input tree.
                with self.assertRaises(FileExistsError):
                    compose.compose(ROOT, destination, profile, 'other', 'other')
                self.assertEqual(json.loads((destination/'overlay/usr/lib/nsl/image.json').read_text()), descriptor)


if __name__ == '__main__':
    unittest.main()
