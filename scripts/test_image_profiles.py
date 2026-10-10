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
                     ('machine', 'gentoo'), ('machine', 'debian', 'bookworm'), ('machine', 'ubuntu', 'jammy'), ('machine', 'fedora', '43'), ('machine', 'debian', 'sid'), ('machine', '../vm')]:
            with self.subTest(args=args), self.assertRaises(ValueError):
                compose.select(ROOT, *args)

    def test_default_release_is_the_stable_one(self):
        for distribution, release in [('debian', 'trixie'), ('fedora', '44'), ('ubuntu', 'resolute'), ('opensuse', 'tumbleweed')]:
            with self.subTest(distribution=distribution):
                self.assertEqual(compose.select(ROOT, 'machine', distribution)['release'], release)

    def test_builder_rejects_unsupported_options_before_tools(self):
        for args in [(), ('--role', 'machine'), ('--distribution', 'debian'), ('--release', 'trixie'), ('--role', 'vm', '--distribution', 'debian'),
                     ('--role', 'machine', '--distribution', 'gentoo'), ('--role', 'vm', '--destination', '/should-not-exist')]:
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
            for distribution, release, family in [('debian', 'trixie', 'debian'), ('ubuntu', 'resolute', 'debian'), ('fedora', '44', 'rpm'),
                                                  ('centos', '10', 'rpm'), ('arch', 'rolling', 'arch'), ('opensuse', 'tumbleweed', 'suse'),
                                                  ('opensuse', '16.0', 'suse'), ('azure', '4.0', 'rpm'), ('debian', 'testing', 'debian'),
                                                  ('fedora', 'rawhide', 'rpm'), ('ubuntu', 'noble', 'debian')]:
                destination = Path(tmp)/f'{distribution}-{release}'
                profile = compose.select(ROOT, 'machine', distribution, release)
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
                self.assertEqual((destination/'overlay/etc/profile.d/nsl-osc7.sh').stat().st_mode & 0o777, 0o644)
                # Only Debian builds with the builder's own tools.
                self.assertEqual((destination/'mkosi.tools.conf').is_file(), distribution != 'debian')
                self.assertEqual(config.rsplit('ToolsTree=', 1)[1].split()[0], 'no' if distribution == 'debian' else 'default')
            self.assertIn('nsl-arch-finalize.chroot', (Path(tmp)/'arch-rolling/mkosi.local.conf').read_text())
            # Rawhide changes signing keys at each branch point, so the build fetches the current one.
            self.assertIn('RepositoryKeyFetch=yes', (Path(tmp)/'fedora-rawhide/mkosi.local.conf').read_text())
            # Azure Linux 4.0 builds from its beta repository and keeps the signed repository package, not the dev feed.
            azure = (Path(tmp)/'azure-4.0/mkosi.local.conf').read_text()
            self.assertIn('LocalMirror=https://packages.microsoft.com/azurelinux/4.0/beta/base/x86_64/', azure)
            self.assertIn('Packages=azurelinux-repos\n', azure)
            self.assertEqual((Path(tmp)/'azure-4.0/nsl-azure-postinst.chroot').stat().st_mode & 0o777, 0o755)

    def test_osc7_reports_the_directory_in_vte_terminals(self):
        script = str(ROOT/'image/machines/common/overlay/etc/profile.d/nsl-osc7.sh')
        with tempfile.TemporaryDirectory() as tmp:
            directory = Path(tmp)/'a b'/'ü~x'
            directory.mkdir(parents=True)
            uri = 'file://box' + str(directory).replace(' ', '%20').replace('ü', '%C3%BC')
            expected = f'\033]7;{uri}\033\\'
            env = {'PATH': os.environ['PATH'], 'HOME': tmp, 'VTE_VERSION': '8401', 'HOSTNAME': 'box', 'HOST': 'box'}

            def shell(*argv, body, **overrides):
                return subprocess.run([*argv, f'. "{script}"; {body}'], cwd=directory, capture_output=True, text=True,
                                      env={**env, **overrides})

            bash = ['bash', '--noprofile', '--norc', '-i', '-c']
            self.assertEqual(shell(*bash, body='__nsl_osc7').stdout, expected)
            # Registered once, ahead of an existing prompt command.
            run = shell(*bash, body=f'. "{script}"; echo "$PROMPT_COMMAND"', PROMPT_COMMAND='history -a')
            self.assertEqual(run.stdout, '__nsl_osc7;history -a\n')
            # vte.sh, when loaded, reports the directory instead.
            self.assertEqual(shell(*bash, body='__vte_osc7() { :; }; __nsl_osc7').stdout, '')
            # Only VTE terminals and interactive shells.
            self.assertEqual(shell(*bash, body='echo "[$PROMPT_COMMAND]"', VTE_VERSION='').stdout, '[]\n')
            run = subprocess.run(['bash', '--noprofile', '--norc', '-c', f'. "{script}"; echo "[$PROMPT_COMMAND]"'],
                                 capture_output=True, text=True, env=env)
            self.assertEqual(run.stdout, '[]\n')
            # POSIX shells read profile.d too and must return cleanly.
            run = subprocess.run(['sh', '-c', f'. "{script}"; echo "rc=$?"'], capture_output=True, text=True, env=env)
            self.assertEqual((run.returncode, run.stdout), (0, 'rc=0\n'))
            if shutil.which('zsh'):
                zsh = ['zsh', '-f', '-i', '-c']
                self.assertEqual(shell(*zsh, body='__nsl_osc7').stdout, expected)
                run = shell(*zsh, body=f'. "{script}"; print -r -- "${{precmd_functions[*]}}"')
                self.assertEqual(run.stdout, '__nsl_osc7\n')

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
