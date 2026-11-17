"""Root-free checks for the acceptance probe's pure evidence logic and state script."""
import importlib.util
import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest

spec = importlib.util.spec_from_file_location('probe', Path(__file__).with_name('probe-machines.py'))
probe = importlib.util.module_from_spec(spec)
spec.loader.exec_module(probe)


def read(stdout, returncode=0, stderr=b''):
    return probe.Result(subprocess.CompletedProcess([], returncode, stdout, stderr), 0.25)


class MachineIdTally(unittest.TestCase):
    def test_distinct_ids_pass(self):
        result = probe.machine_id_tally('uninitialized', {'a': read(b'1'*32 + b'\n'), 'b': read(b'2'*32 + b'\n')})
        self.assertEqual(result, {'pass': True, 'image': 'uninitialized', 'machines': 2, 'failed_reads': {}, 'shared': []})

    def test_an_image_id_fails(self):
        self.assertFalse(probe.machine_id_tally('3'*32, {'a': read(b'1'*32)})['pass'])
        self.assertTrue(probe.machine_id_tally('', {'a': read(b'1'*32)})['pass'])

    def test_a_failed_read_names_the_machine_without_ids(self):
        result = probe.machine_id_tally('uninitialized', {'a': read(b'1'*32), 'b': read(b'', 255, b'nsl: the VM is stopping'),
                                                          'c': read(b'2'*32, 1)})
        self.assertFalse(result['pass'])
        self.assertEqual(result['failed_reads'], {
            'b': {'returncode': 255, 'length': 0, 'output': '', 'stderr': 'nsl: the VM is stopping', 'seconds': 0.25},
            'c': {'returncode': 1, 'length': 32, 'output': '', 'stderr': '', 'seconds': 0.25}})
        self.assertEqual(result['shared'], [])
        self.assertEqual(probe.machine_id_tally('', {'a': read(b'uninitialized')})['failed_reads']['a']['output'], 'uninitialized')

    def test_a_shared_id_names_the_machines(self):
        result = probe.machine_id_tally('', {'a': read(b'1'*32), 'b': read(b'2'*32), 'c': read(b'1'*32)})
        self.assertEqual((result['pass'], result['shared'], result['failed_reads']), (False, [['a', 'c']], {}))


CLOUD_INIT_KEYS = ['apt', 'ca_certs', 'package_update', 'package_upgrade', 'packages', 'runcmd', 'write_files']
CLOUD_INIT_CAPABILITY = {'interface': 1, 'cloud_init': '25.1.4', 'datasource': 'nocloud', 'keys': CLOUD_INIT_KEYS}


def cloud_init_state(**overrides):
    """What the state script prints for a machine that ships cloud-init switched off."""
    state = dict(version='25.1.4',
                 config=dict(datasource_list=['NoCloud'], network={'config': 'disabled'}, users=[], preserve_hostname=True,
                             manage_etc_hosts=False, **probe.compose.cloud_init_modules(CLOUD_INIT_KEYS)),
                 marker=True, status='status: disabled', active=[],
                 masked={name: dict({'enabled': 'masked'}, **({'active': 'inactive'} if '@' not in name else {}))
                         for name in probe.CLOUD_INIT_MASKED},
                 host_keys=[], ran=False, generator=False)
    state.update(overrides)
    return state


class CloudInitResult(unittest.TestCase):
    """The cloud_init check accepts an image only when cloud-init stays off and runs its keys' modules."""

    def test_a_good_state_passes(self):
        result = probe.cloud_init_result(CLOUD_INIT_CAPABILITY, cloud_init_state())
        self.assertTrue(result['pass'], result['checks'])
        self.assertEqual(result['version'], '25.1.4')
        self.assertEqual(result['checks']['config'], True)

    def test_each_deviation_fails(self):
        state = cloud_init_state()
        cases = {
            'wrong version': (CLOUD_INIT_CAPABILITY, dict(version='26.1')),
            'extra module': (CLOUD_INIT_CAPABILITY, dict(config=dict(state['config'], cloud_config_modules=['apt_configure', 'runcmd', 'bootcmd']))),
            'a default account': (CLOUD_INIT_CAPABILITY, dict(config=dict(state['config'], users=[{'name': 'debian'}]))),
            'no marker': (CLOUD_INIT_CAPABILITY, dict(marker=False)),
            'an active unit': (CLOUD_INIT_CAPABILITY, dict(active=['cloud-init-hotplugd.socket'])),
            'a name not masked': (CLOUD_INIT_CAPABILITY, dict(masked=dict(state['masked'], **{'sshd.service': {'enabled': 'enabled', 'active': 'active'}}))),
            # A masked unit must be inactive, not on its way in or out, and not failed.
            'a masked unit activating': (CLOUD_INIT_CAPABILITY, dict(masked=dict(state['masked'], **{'ssh.service': {'enabled': 'masked', 'active': 'activating'}}))),
            'a masked unit failed': (CLOUD_INIT_CAPABILITY, dict(masked=dict(state['masked'], **{'ssh.service': {'enabled': 'masked', 'active': 'failed'}}))),
            'a masked unit with no state': (CLOUD_INIT_CAPABILITY, dict(masked=dict(state['masked'], **{'ssh.service': {'enabled': 'masked', 'active': ''}}))),
            'a host key': (CLOUD_INIT_CAPABILITY, dict(host_keys=['/etc/ssh/ssh_host_ed25519_key'])),
            'cloud-init ran': (CLOUD_INIT_CAPABILITY, dict(ran=True)),
            'a generator link': (CLOUD_INIT_CAPABILITY, dict(generator=True)),
            'an unknown key': (dict(CLOUD_INIT_CAPABILITY, keys=CLOUD_INIT_KEYS + ['hostname']), {}),
            'another interface': (dict(CLOUD_INIT_CAPABILITY, interface=2), {}),
        }
        for name, (capability, overrides) in cases.items():
            with self.subTest(name):
                self.assertFalse(probe.cloud_init_result(capability, cloud_init_state(**overrides))['pass'])


FAKE_CLOUDINIT_CONFIG = '''def read_conf_with_confd(path):
    return {'datasource_list': ['NoCloud'], 'network': {'config': 'disabled'}, 'users': [],
            'preserve_hostname': True, 'manage_etc_hosts': False}
'''


def systemctl(list_units='', is_active='printf \'inactive\\n\''):
    """A fake systemctl body; systemctl exits nonzero for masked and inactive units."""
    return ('#!/bin/sh\n'
            'case $1 in\n'
            f'  list-units) {list_units} ;;\n'
            "  is-enabled) printf 'masked\\n' ;;\n"
            f'  is-active) {is_active} ;;\n'
            'esac\n')


class CloudInitStateScript(unittest.TestCase):
    """The state script reads cloud-init's own state, and records a failed read as an error."""

    def state(self, tmp, systemctl_body):
        """Run the script on the host with fakes first on PATH: systemctl, cloud-init, the cloudinit package."""
        binaries = Path(tmp)/'bin'
        binaries.mkdir()
        for name, body in (('systemctl', systemctl_body), ('cloud-init', '#!/bin/sh\nprintf \'status: disabled\\n\'\n')):
            (binaries/name).write_text(body)
            (binaries/name).chmod(0o755)
        package = Path(tmp)/'python/cloudinit'
        package.mkdir(parents=True)
        (package/'__init__.py').write_text('')
        (package/'version.py').write_text('def version_string():\n    return "25.1.4"\n')
        (package/'util.py').write_text(FAKE_CLOUDINIT_CONFIG)
        run = subprocess.run(['python3', '-c', probe.CLOUD_INIT_STATE], capture_output=True, text=True,
                             env=dict(os.environ, PATH=f'{binaries}:{os.environ["PATH"]}', PYTHONPATH=str(package.parent)))
        self.assertEqual(run.returncode, 0, run.stderr)
        return json.loads(run.stdout)

    def test_a_failed_unit_list_is_an_error_not_an_empty_list(self):
        with tempfile.TemporaryDirectory() as tmp:
            state = self.state(tmp, systemctl(list_units="printf 'cloud-init.target loaded active active x\\n'; exit 1"))
            self.assertIsInstance(state['active'], str)
            self.assertFalse(probe.cloud_init_result(CLOUD_INIT_CAPABILITY, state)['checks']['active'])

    def test_an_instance_of_a_masked_template_unit_is_listed(self):
        with tempfile.TemporaryDirectory() as tmp:
            state = self.state(tmp, systemctl(list_units='case "$*" in *sshd-keygen@*) '
                                              "printf 'sshd-keygen@rsa.service loaded activating start x\\n' ;; esac"))
            self.assertEqual(state['active'], ['sshd-keygen@rsa.service'])

    def test_a_masked_unit_that_is_activating_fails(self):
        with tempfile.TemporaryDirectory() as tmp:
            state = self.state(tmp, systemctl(is_active="case $2 in ssh.service) printf 'activating\\n' ;; "
                                                       "*) printf 'inactive\\n' ;; esac"))
            self.assertEqual(state['masked']['ssh.service'], {'enabled': 'masked', 'active': 'activating'})
            self.assertFalse(probe.cloud_init_result(CLOUD_INIT_CAPABILITY, state)['checks']['masked'])


if __name__ == '__main__':
    unittest.main()