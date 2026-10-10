"""Root-free checks for the acceptance probe's pure evidence logic."""
import importlib.util
from pathlib import Path
import subprocess
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


if __name__ == '__main__':
    unittest.main()
