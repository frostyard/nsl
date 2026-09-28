"""Public reports must prove every acceptance check passed and exclude private test state."""
import copy
import importlib.util
import json
from datetime import datetime, timezone
from pathlib import Path
import unittest

spec = importlib.util.spec_from_file_location('publication', Path(__file__).with_name('publish-images.py'))
pub = importlib.util.module_from_spec(spec)
spec.loader.exec_module(pub)

RAW = {'digest': 'sha256:'+'a'*64, 'size': 100}
COMPRESSED = {'digest': 'sha256:'+'b'*64, 'size': 50}
VM_IMAGE = {'build_id': 'nsl-vm-trixie-x86-64-r8', 'architecture': 'x86-64', 'agent_protocol': 1}
MACHINE_IMAGE = {'build_id': 'nsl-machine-debian-trixie-x86-64-r3', 'architecture': 'x86-64', 'machine_protocol': 1,
                 'capabilities': {'gui': {}, 'nesting': {}}}


def vm_probe():
    results = {name: {'pass': True, 'home': '/private/state'} for name in pub.VM_CHECKS}
    results['readiness']['first_boot_seconds'] = 7.9
    return {'digest': RAW['digest'], 'image': '/private/build/vm.raw', 'results': results}


def machine_probe():
    shared = {name: {'pass': True, 'stderr': '/home/example/secret'} for name in pub.MACHINE_CHECKS}
    shared['entry']['latency_ms'] = {'median': 69.0, 'p95': 120.0}
    isolated = {name: {'pass': True} for name in pub.ISOLATED_CHECKS}
    return {'vm': {'digest': RAW['digest'][7:], 'descriptor': {'build_id': VM_IMAGE['build_id']}},
            'machines': {'debian': {'build_id': MACHINE_IMAGE['build_id'], 'digest': COMPRESSED['digest'][7:], 'isolated': False, 'checks': shared},
                         'isolated': {'build_id': MACHINE_IMAGE['build_id'], 'digest': COMPRESSED['digest'][7:], 'isolated': True, 'checks': isolated}}}


class Publication(unittest.TestCase):
    def test_public_reports_are_explicit(self):
        vm = pub.vm_acceptance(vm_probe(), VM_IMAGE, RAW, machine_probe())
        machine = pub.machine_acceptance(machine_probe(), MACHINE_IMAGE, COMPRESSED, {'idle_4_pss_mib': 853.1})
        for report in (vm, machine):
            self.assertTrue(report['passed'])
            data = json.dumps(report)
            for private in ('/private', '/home/example'):
                self.assertNotIn(private, data)
        self.assertEqual(set(machine['checks']), pub.MACHINE_CHECKS)
        self.assertEqual(set(machine['isolated_checks']), pub.ISOLATED_CHECKS)
        self.assertEqual(machine['capabilities'], ['gui', 'nesting'])
        self.assertEqual(vm['machine_images'], [MACHINE_IMAGE['build_id']] * 2)

    def test_failed_skipped_missing_or_mismatched_checks_cannot_promote(self):
        cases = {
            'vm failed': lambda v, m: v['results']['refusal'].update({'pass': False}),
            'vm missing': lambda v, m: v['results'].pop('binding'),
            'vm bytes': lambda v, m: v.update({'digest': 'sha256:'+'c'*64}),
            'machines on another VM': lambda v, m: m['vm'].update({'digest': 'c'*64}),
            'machine failed': lambda v, m: m['machines']['debian']['checks']['gui'].update({'pass': False}),
            'gui skipped': lambda v, m: m['machines']['debian']['checks']['broker'].update({'skipped': 'no compositor'}),
            'machine missing': lambda v, m: m['machines']['debian']['checks'].pop('ssh'),
            'isolated failed': lambda v, m: m['machines']['isolated']['checks']['isolation'].update({'pass': False}),
            'other build': lambda v, m: m['machines']['debian'].update({'build_id': 'nsl-machine-debian-trixie-x86-64-r2'}),
            'not probed': lambda v, m: m['machines'].pop('debian'),
        }
        for name, change in cases.items():
            with self.subTest(name):
                vm, machines = vm_probe(), machine_probe()
                change(vm, machines)
                with self.assertRaises(ValueError):
                    pub.vm_acceptance(vm, VM_IMAGE, RAW, machines)
                    pub.machine_acceptance(machines, MACHINE_IMAGE, COMPRESSED, {})

    def test_measurements_gate_publication(self):
        good = {'criteria': {'idle_machines': {'pass': True, 'pss_mib': 853.1},
                             'additional_machine_p95': {'pass': True, 'seconds': 0.67}}}
        self.assertEqual(pub.measurements_passed(good), {'idle_4_pss_mib': 853.1, 'additional_machine_p95_seconds': 0.67})
        bad = copy.deepcopy(good)
        bad['criteria']['idle_machines']['pass'] = False
        for evidence in (bad, {}):
            with self.assertRaises(ValueError):
                pub.measurements_passed(evidence)

    def test_catalogue_entries_by_kind(self):
        vm = pub.entry(dict(kind='vm', architecture='x86-64', agent_protocol=1, build_id='v'), 'sha256:'+'a'*64)
        machine = pub.entry(dict(kind='machine', selectors=['debian:13'], architecture='x86-64', machine_protocol=1, build_id='m'), 'sha256:'+'b'*64)
        self.assertEqual(set(vm), {'kind', 'architecture', 'agent_protocol', 'manifest', 'build_id'})
        self.assertEqual(set(machine), {'kind', 'selectors', 'architecture', 'machine_protocol', 'manifest', 'build_id'})
        built = [dict(kind='vm')] + [dict(kind='machine', selectors=s) for s in pub.SELECTORS.values()]
        pub.complete(built)
        for broken in (built[1:], built + [dict(kind='vm')], built[:-1]):
            with self.assertRaises(ValueError):
                pub.complete(broken)

    def test_selectors_cover_every_machine_profile(self):
        self.assertEqual(set(pub.SELECTORS), set(pub.compose.MACHINES))

    def test_refresh_and_withdrawal_preserve_history(self):
        now = datetime(2026, 9, 27, tzinfo=timezone.utc)
        first = {'kind': 'machine', 'manifest': 'sha256:'+'a'*64, 'selectors': ['debian:13']}
        second = {'kind': 'vm', 'manifest': 'sha256:'+'b'*64}
        old_revocation = 'sha256:'+'c'*64
        prior = dict(sequence=10, images=[first, second], revoked=[old_revocation])
        fresh = pub.next_catalogue(prior, 11, now)
        self.assertEqual(fresh['images'], prior['images'])
        self.assertEqual(fresh['revoked'], [old_revocation])
        self.assertEqual(fresh['expires'], '2026-10-27T00:00:00Z')
        withdrawn = pub.next_catalogue(prior, 12, now, revoke=[first['manifest']])
        self.assertEqual(withdrawn['images'], [second])
        self.assertEqual(set(withdrawn['revoked']), {first['manifest'], old_revocation})
        with self.assertRaises(ValueError):
            pub.next_catalogue(prior, 10, now)
        with self.assertRaises(ValueError):
            pub.next_catalogue(prior, 11, now, revoke=['sha256:'+'d'*64])
        rebuilt = pub.next_catalogue(withdrawn, 13, now, entries=[first, second])
        self.assertEqual(rebuilt['images'], [second])

    def test_disk_catalogues_are_replaced_only_by_publication(self):
        now = datetime(2026, 9, 27, tzinfo=timezone.utc)
        disks = dict(sequence=3, images=[{'selectors': ['debian:13'], 'manifest': 'sha256:'+'a'*64}], revoked=[])
        with self.assertRaises(ValueError):
            pub.next_catalogue(disks, 4, now)
        entry = {'kind': 'vm', 'manifest': 'sha256:'+'b'*64}
        self.assertEqual(pub.next_catalogue(disks, 4, now, entries=[entry])['images'], [entry])


if __name__ == '__main__':
    unittest.main()
