"""Root-free checks for the image input boundary and layer composition."""
import importlib.util
import json
import os
import re
import shutil
import subprocess
from pathlib import Path
import tempfile
import unittest

spec = importlib.util.spec_from_file_location('compose', Path(__file__).with_name('compose-image.py'))
compose = importlib.util.module_from_spec(spec)
spec.loader.exec_module(compose)
ROOT = Path(__file__).resolve().parents[1]


def agent(directory, content=b'\x7fELF agent'):
    path = Path(directory)/'nsl-agent'
    path.write_bytes(content)
    return path


class VMImage(unittest.TestCase):
    def test_unsupported_inputs_are_rejected(self):
        for args in [('machine',), ('../vm',), ('vm', 'debian'), ('vm', None, 'trixie'), ('vm', None, None, 'arm64'),
                     ('machine', 'ubuntu'), ('machine', 'debian', 'bookworm'), ('machine', '../vm')]:
            with self.subTest(args=args), self.assertRaises(ValueError):
                compose.select(ROOT, *args)

    def test_builder_rejects_unsupported_options_before_tools(self):
        for args in [(), ('--role', 'machine'), ('--distribution', 'debian'), ('--release', 'trixie'), ('--role', 'vm', '--distribution', 'debian'),
                     ('--role', 'machine', '--distribution', 'ubuntu'), ('--role', 'vm', '--destination', '/should-not-exist')]:
            with self.subTest(args=args):
                result = subprocess.run(['bash', str(ROOT/'scripts/build-image.sh'), *args],
                                        env=dict(os.environ, NSL_LIMACTL='/must-not-run'),
                                        capture_output=True, text=True)
                self.assertNotEqual(result.returncode, 0)
                self.assertNotIn('/must-not-run', result.stderr)

    def test_protocols_match_the_go_package(self):
        source = (ROOT/'internal/protocol/protocol.go').read_text()
        self.assertEqual(int(re.search(r'\bVersion\s+= (\d+)', source).group(1)), compose.AGENT_PROTOCOL)
        self.assertEqual(int(re.search(r'\bMachineVersion\s+= (\d+)', source).group(1)), compose.MACHINE_PROTOCOL)

    def test_composed_tree_and_no_overwrite(self):
        with tempfile.TemporaryDirectory() as tmp:
            destination = Path(tmp)/'tree'
            profile = compose.select(ROOT, 'vm')
            name = compose.compose(ROOT, destination, profile, 'recipes-pin', 'mkosi-pin', agent(tmp))
            self.assertEqual(name, 'nsl-vm-trixie-x86-64-r{revision}'.format(**profile))
            descriptor = json.loads((destination/'overlay/usr/lib/nsl/image.json').read_text())
            self.assertEqual((descriptor['role'], descriptor['build_id'], descriptor['agent_protocol'], descriptor['transport']),
                             ('vm', name, 1, 'nsl-vsock-ssh'))
            self.assertEqual((descriptor['systemd'], descriptor['kernel']), ('@SYSTEMD@', '@KERNEL@'))
            self.assertEqual(descriptor['recipes_revision'], 'recipes-pin')
            self.assertEqual(len(descriptor['integration_sha256']), 64)
            installed = destination/'overlay/usr/lib/nsl/nsl-agent'
            self.assertEqual((installed.read_bytes(), installed.stat().st_mode & 0o777), (b'\x7fELF agent', 0o755))
            config = (destination/'mkosi.local.conf').read_text()
            self.assertIn(f'Output={name}', config)
            self.assertIn('FinalizeScripts=nsl-finalize.chroot', config)
            self.assertEqual((destination/'overlay/etc/systemd/system-generators/systemd-ssh-generator').readlink(), Path('/dev/null'))
            units = destination/'overlay/etc/systemd/system'
            for unit in ('nsl-data.service', 'nsl-setup.service', 'nsl-machines.service', 'nsl-idle.service', 'nsl-ssh.socket', 'var-lib-nsl.mount', 'var-lib-machines.mount'):
                self.assertTrue((units/unit).is_file(), unit)
            self.assertIn('subvol=state', (units/'var-lib-nsl.mount').read_text())
            self.assertIn('subvol=machines', (units/'var-lib-machines.mount').read_text())
            self.assertIn('ImportCredential=nsl.vm', (units/'nsl-setup.service').read_text())
            sshd = (destination/'overlay/etc/ssh/sshd_config.d/90-nsl.conf').read_text()
            self.assertIn('PermitRootLogin forced-commands-only', sshd)
            self.assertIn('HostKey /var/lib/nsl/ssh/ssh_host_ed25519_key', sshd)
            # Failed retries cannot replace a prepared input tree.
            with self.assertRaises(FileExistsError):
                compose.compose(ROOT, destination, profile, 'other', 'other', agent(tmp))
            self.assertEqual(json.loads((destination/'overlay/usr/lib/nsl/image.json').read_text()), descriptor)

    def test_input_hash_tracks_inputs_not_docs(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)/'source'
            for name in ('image', 'scripts'):
                shutil.copytree(ROOT/name, root/name, symlinks=True)
            profile = compose.select(root, 'vm')
            def fingerprint(index, binary=b'agent'):
                destination = Path(tmp)/str(index)
                compose.compose(root, destination, profile, 'recipes', 'mkosi', agent(tmp, binary))
                return json.loads((destination/'overlay/usr/lib/nsl/image.json').read_text())['integration_sha256']
            first = fingerprint(1)
            (root/'image/README.md').write_text('Unrelated docs')
            self.assertEqual(first, fingerprint(2))
            self.assertNotEqual(first, fingerprint(3, b'another agent'))
            # A checkout's umask does not change the image, but an execute bit does.
            unit = root/'image/vm/overlay/etc/systemd/system/nsl-setup.service'
            unit.chmod(0o664)
            self.assertEqual(first, fingerprint(4))
            path = root/'image/vm/nsl-postinst.chroot'
            path.chmod(path.stat().st_mode ^ 0o100)
            self.assertNotEqual(first, fingerprint(5))

    def test_machine_images_compose_each_family(self):
        with tempfile.TemporaryDirectory() as tmp:
            for distribution, family in [('debian', 'debian'), ('fedora', 'rpm'), ('arch', 'arch'), ('opensuse', 'suse')]:
                destination = Path(tmp)/distribution
                profile = compose.select(ROOT, 'machine', distribution)
                name = compose.compose(ROOT, destination, profile, 'recipes-pin', 'mkosi-pin')
                descriptor = json.loads((destination/'overlay/usr/lib/nsl/machine.json').read_text())
                self.assertEqual((descriptor['role'], descriptor['family'], descriptor['build_id'], descriptor['machine_protocol']),
                                 ('machine', family, name, compose.MACHINE_PROTOCOL))
                self.assertEqual(descriptor['systemd'], '@SYSTEMD@')
                config = (destination/'mkosi.local.conf').read_text()
                self.assertIn('Format=tar', config)
                self.assertIn('CompressOutput=zstd', config)
                self.assertIn('FinalizeScripts=nsl-finalize.chroot', config)
                self.assertFalse((destination/'overlay/usr/lib/nsl/nsl-agent').exists())
                pam = destination/'overlay/etc/pam.d/nsl'
                self.assertIn('pam_systemd.so', pam.read_text())
                self.assertEqual(pam.stat().st_mode & 0o777, 0o644)
                self.assertEqual((destination/'overlay/usr/bin/nsl-path').stat().st_mode & 0o777, 0o755)
                self.assertEqual((destination/'mkosi.tools.conf').is_file(), family != 'debian')
            self.assertIn('nsl-arch-finalize.chroot', (Path(tmp)/'arch/mkosi.local.conf').read_text())

    def test_nsl_path_translates_both_ways(self):
        script = str(ROOT/'image/machines/common/overlay/usr/bin/nsl-path')
        run = lambda *args: subprocess.run(['sh', script, *args], capture_output=True, text=True)
        self.assertEqual(run('/mnt/host/var/home/u/x').stdout, '/var/home/u/x\n')
        self.assertEqual(run('/home/u/x').stdout, '/mnt/host/home/u/x\n')
        self.assertEqual(run('--guest', '/var/home/u').stdout, '/mnt/host/var/home/u\n')
        self.assertEqual(run('--host', '/etc/passwd').returncode, 1)
        self.assertEqual(run().returncode, 2)


if __name__ == '__main__':
    unittest.main()
