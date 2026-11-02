"""Public reports must prove every acceptance check passed and exclude private test state."""
import base64
import contextlib
import copy
from datetime import datetime, timezone
import hashlib
import importlib.util
import io
import json
from pathlib import Path
import shutil
import subprocess
import tempfile
import unittest
from unittest import mock

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
        self.assertEqual(pub.measurements_passed(dict(good, desktop_overhead_mib=118.6)),
                         {'idle_4_pss_mib': 853.1, 'additional_machine_p95_seconds': 0.67, 'desktop_overhead_mib': 118.6})
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
        self.assertEqual(len(set(pub.MEASURED)), 4)
        self.assertLessEqual(set(pub.MEASURED), set(pub.compose.MACHINES))

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


UUID = '108e9186e8c5677a6e2e8f6cb158169ee8d1f33f928243cf53c1596d53ad681ab7473198ab288093'
CONFLICT = ('Error: signing /x/descriptor.json: signing bundle: error signing bundle: '
            '[POST /api/v1/log/entries][409] createLogEntryConflict {"code":409,"message":"an equivalent entry '
            f'already exists in the transparency log with UUID {UUID}"}}')
SIGNER_ERROR = 'Error: getting signer: getting GitHub token: no OIDC token available'
MISSING_BLOB = 'Error: signing /x/descriptor.json: open /x/descriptor.json: no such file or directory'
EMPTY_RETRIES = ('Error: signing x: POST https://fulcio.sigstore.dev/api/v2/signingCert '
                 'giving up after 4 attempt(s)')
# go-retryablehttp names the cause after ': ' when a transport error ended the retries.
CAUSED_RETRIES = ['Error: signing x: POST https://fulcio.sigstore.dev/api/v2/signingCert '
                  'giving up after 1 attempt(s): x509: certificate signed by unknown authority',
                  'Error: signing x: POST https://fulcio.sigstore.dev/api/v2/signingCert '
                  'giving up after 4 attempt(s): some unknown error']
TRANSIENT = ['Error: signing /x/descriptor.json: [POST /api/v1/log/entries][503] Service Unavailable',
             'Error: signing /x/descriptor.json: Post "https://rekor.sigstore.dev/api/v1/log/entries": '
             'giving up after 5 attempt(s): [POST /api/v1/log/entries][502] Bad Gateway',
             'Error: signing /x/descriptor.json: net/http: TLS handshake timeout']


def cosign(stderr='', returncode=0, stdout=''):
    return subprocess.CompletedProcess(['cosign'], returncode, stdout=stdout, stderr=stderr)


def steps(*actions):
    """A subprocess.run side effect: the actions run in turn, and no more may run than listed."""
    pending = list(actions)

    def effect(*args, **kwargs):
        if not pending:
            raise AssertionError('cosign ran more often than this test allows')
        action = pending.pop(0)
        return action(*args, **kwargs) if callable(action) else action
    return effect


def hashedrekord(digest, kind='hashedrekord', algorithm='sha256'):
    """The Rekor entry body cosign writes for a blob signature."""
    return dict(apiVersion='0.0.1', kind=kind,
                spec=dict(data=dict(hash=dict(algorithm=algorithm, value=digest)), signature=dict(content='c2ln')))


def entry(body, key=UUID):
    """A Rekor v1 log entry response holding one entry for this body."""
    return json.dumps({key: dict(body=base64.b64encode(json.dumps(body).encode()).decode(), integratedTime=1,
                                 logID='c0d23d6ad406973f9559f3ba2d1ca01f84147d8ffc5b8445c224f98b9591801d',
                                 logIndex=1, verification={})}).encode()


class Response:
    def __init__(self, raw):
        self.raw = raw

    def read(self, limit):
        return self.raw[:limit]

    def __enter__(self):
        return self

    def __exit__(self, *exception):
        return False


class SignRetry(unittest.TestCase):
    """Signing retries only a transient HTTP error or a Rekor conflict that records this blob."""

    def setUp(self):
        self.directory = Path(tempfile.mkdtemp())
        self.addCleanup(shutil.rmtree, self.directory)
        self.blob = self.directory/'descriptor.json'
        self.blob.write_text('{"schema":1}\n')
        self.digest = hashlib.sha256(self.blob.read_bytes()).hexdigest()
        self.bundle = self.directory/'descriptor.sigstore.json'
        self.tools = Path('/tools')
        self.sleep = self.start(mock.patch.object(pub.time, 'sleep'))
        self.verify = self.start(mock.patch.object(pub, 'verify_signature'))
        self.out, self.err = io.StringIO(), io.StringIO()
        for redirect in (contextlib.redirect_stdout(self.out), contextlib.redirect_stderr(self.err)):
            redirect.__enter__()
            self.addCleanup(redirect.__exit__, None, None, None)

    def start(self, patch):
        self.addCleanup(patch.stop)
        return patch.start()

    def signing(self, *actions):
        return self.start(mock.patch.object(pub.subprocess, 'run', side_effect=steps(*actions)))

    def test_success_signs_once_and_verifies_without_a_retry(self):
        self.matches = self.start(mock.patch.object(pub, 'rekor_entry_matches', return_value=True))
        run = self.signing(cosign(stdout='tlog entry created with index 1'))
        pub.sign(self.tools, self.directory, 'descriptor')
        self.assertEqual(run.call_count, 1)
        self.sleep.assert_not_called()
        self.matches.assert_not_called()
        self.verify.assert_called_once_with(self.tools, self.directory, 'descriptor')
        argv = run.call_args.args[0]
        self.assertEqual(argv[:5], ['/tools/cosign', 'sign-blob', '--yes', '--oidc-provider', 'github-actions'])
        self.assertIn(str(self.bundle), argv)
        self.assertIn(str(self.blob), argv)
        self.assertEqual(argv[5], '--bundle')
        self.assertEqual(run.call_args.kwargs, dict(capture_output=True, text=True))

    def test_cosign_output_and_each_retry_stay_in_the_job_log(self):
        self.matches = self.start(mock.patch.object(pub, 'rekor_entry_matches', return_value=True))
        self.signing(cosign(CONFLICT + '\n', returncode=1, stdout='first\n'), cosign(stdout='second\n'))
        pub.sign(self.tools, self.directory, 'descriptor')
        self.assertIn('first\n', self.out.getvalue())
        self.assertIn('second\n', self.out.getvalue())
        self.assertIn(CONFLICT, self.err.getvalue())
        self.assertIn('cosign sign-blob failed (try 1 of 3): Rekor entry conflict for this blob; retrying in 15 s.', self.out.getvalue())

    def test_conflict_on_this_blob_retries_after_confirming_the_entry(self):
        self.matches = self.start(mock.patch.object(pub, 'rekor_entry_matches', return_value=True))
        run = self.signing(cosign(CONFLICT, returncode=1), cosign())
        pub.sign(self.tools, self.directory, 'descriptor')
        self.assertEqual(run.call_count, 2)
        self.matches.assert_called_once_with(UUID, self.blob)
        self.sleep.assert_called_once_with(15)
        self.verify.assert_called_once()

    def test_conflict_on_another_blob_stops_at_once(self):
        self.matches = self.start(mock.patch.object(pub, 'rekor_entry_matches', return_value=False))
        run = self.signing(cosign(CONFLICT, returncode=1), cosign())
        with self.assertRaises(subprocess.CalledProcessError):
            pub.sign(self.tools, self.directory, 'descriptor')
        self.assertEqual(run.call_count, 1)
        self.sleep.assert_not_called()
        self.verify.assert_not_called()

    def test_conflict_without_an_entry_uuid_stops_at_once(self):
        self.matches = self.start(mock.patch.object(pub, 'rekor_entry_matches', return_value=True))
        run = self.signing(cosign(CONFLICT.replace(UUID, ''), returncode=1), cosign())
        with self.assertRaises(subprocess.CalledProcessError):
            pub.sign(self.tools, self.directory, 'descriptor')
        self.assertEqual(run.call_count, 1)
        self.matches.assert_not_called()
        self.sleep.assert_not_called()

    def test_transient_errors_back_off_at_fifteen_then_forty_five_seconds(self):
        run = self.signing(cosign(TRANSIENT[0], returncode=1), cosign(TRANSIENT[1], returncode=1), cosign())
        pub.sign(self.tools, self.directory, 'descriptor')
        self.assertEqual(run.call_count, 3)
        self.assertEqual([call.args[0] for call in self.sleep.call_args_list], [15, 45])
        self.verify.assert_called_once()
        self.assertIn('cosign sign-blob failed (try 2 of 3): transient HTTP error; retrying in 45 s.', self.out.getvalue())

    def test_three_transient_failures_raise_the_last_error(self):
        run = self.signing(*(cosign(TRANSIENT[0], returncode=1) for _ in range(3)))
        with self.assertRaises(subprocess.CalledProcessError) as caught:
            pub.sign(self.tools, self.directory, 'descriptor')
        self.assertEqual(caught.exception.returncode, 1)
        self.assertEqual(caught.exception.stderr, TRANSIENT[0])
        self.assertEqual([call.args[0] for call in self.sleep.call_args_list], [15, 45])
        self.verify.assert_not_called()
        self.assertIn('cosign sign-blob failed (try 1 of 3): transient HTTP error; retrying in 15 s.', self.out.getvalue())
        self.assertIn('cosign sign-blob failed after 3 tries.', self.out.getvalue())

    def test_a_bare_giving_up_line_is_transient(self):
        run = self.signing(cosign(EMPTY_RETRIES, returncode=1), cosign())
        pub.sign(self.tools, self.directory, 'descriptor')
        self.assertEqual(run.call_count, 2)
        self.sleep.assert_called_once_with(15)
        self.verify.assert_called_once()

    def test_a_giving_up_line_with_another_cause_never_matches(self):
        for stderr in CAUSED_RETRIES:
            with self.subTest(stderr[-40:]):
                self.assertIsNone(pub.sign_retry_reason(stderr, self.blob))

    def test_other_errors_stop_at_once(self):
        for stderr in (SIGNER_ERROR, MISSING_BLOB, 'error: some other failure', *CAUSED_RETRIES):
            with self.subTest(stderr[:30]):
                with mock.patch.object(pub.subprocess, 'run', side_effect=steps(cosign(stderr, returncode=1), cosign())) as run:
                    with self.assertRaises(subprocess.CalledProcessError):
                        pub.sign(self.tools, self.directory, 'descriptor')
                    self.assertEqual(run.call_count, 1)
                self.sleep.assert_not_called()
                self.verify.assert_not_called()

    def test_a_bundle_left_by_a_failed_try_is_deleted_before_the_retry(self):
        seen = []

        def failing(*args, **kwargs):
            self.bundle.write_text('{}')
            return cosign(TRANSIENT[0], returncode=1)

        def succeeding(*args, **kwargs):
            seen.append(self.bundle.exists())
            return cosign()

        self.signing(failing, succeeding)
        pub.sign(self.tools, self.directory, 'descriptor')
        self.assertEqual(seen, [False])

    def test_a_bundle_that_existed_before_the_first_try_stays(self):
        self.bundle.write_text('{"old":true}')
        seen = []

        def failing(*args, **kwargs):
            seen.append(self.bundle.exists())
            return cosign(TRANSIENT[0], returncode=1)

        self.signing(failing, cosign())
        pub.sign(self.tools, self.directory, 'descriptor')
        self.assertEqual(seen, [True])

    def test_an_entry_named_by_its_tree_id_or_its_uuid_matches(self):
        short, long = UUID[16:], UUID
        for name, requested in ((short, short), (long, long), (long, short), (short, long)):
            with self.subTest(f'{len(name)} for {len(requested)}'):
                response = Response(entry(hashedrekord(self.digest), key=name))
                with mock.patch.object(pub.urllib.request, 'urlopen', return_value=response):
                    self.assertTrue(pub.rekor_entry_matches(requested, self.blob))

    def test_an_entry_for_another_uuid_never_matches(self):
        for name in ('f'*64, 'f'*80, UUID[:60] + 'f'*4, 'not a uuid'):
            with self.subTest(name[:16]):
                response = Response(entry(hashedrekord(self.digest), key=name))
                with mock.patch.object(pub.urllib.request, 'urlopen', return_value=response):
                    self.assertFalse(pub.rekor_entry_matches(UUID, self.blob))

    def test_an_entry_that_records_this_blob_matches(self):
        urlopen = self.start(mock.patch.object(pub.urllib.request, 'urlopen', return_value=Response(entry(hashedrekord(self.digest)))))
        self.assertTrue(pub.rekor_entry_matches(UUID, self.blob))
        urlopen.assert_called_once_with(pub.REKOR + '/api/v1/log/entries/' + UUID, timeout=30)
        self.assertEqual(pub.REKOR, 'https://rekor.sigstore.dev')

    def test_other_entries_never_match(self):
        cases = {'wrong digest': entry(hashedrekord('a'*64)),
                 'other kind': entry(hashedrekord(self.digest, kind='dsse')),
                 'another algorithm': entry(hashedrekord(self.digest, algorithm='sha512')),
                 'missing kind': entry(dict(spec=dict(data=dict(hash=dict(algorithm='sha256', value=self.digest))))),
                 'invalid json': b'{"not an entry"',
                 'no entry': json.dumps({}).encode(),
                 'two entries': json.dumps({UUID: json.loads(entry(hashedrekord(self.digest)))[UUID],
                                            'f'*64: json.loads(entry(hashedrekord(self.digest)))[UUID]}).encode()}
        for name, raw in cases.items():
            with self.subTest(name):
                with mock.patch.object(pub.urllib.request, 'urlopen', return_value=Response(raw)):
                    self.assertFalse(pub.rekor_entry_matches(UUID, self.blob))

    def test_a_response_over_one_mebibyte_never_matches(self):
        body = hashedrekord(self.digest)
        body['spec']['signature']['content'] = 'A' * (1 << 20)
        with mock.patch.object(pub.urllib.request, 'urlopen', return_value=Response(entry(body))):
            self.assertFalse(pub.rekor_entry_matches(UUID, self.blob))

    def test_a_failed_fetch_never_matches(self):
        for error in (OSError('connection refused'), TimeoutError('timed out'), ValueError('bad base64')):
            with self.subTest(type(error).__name__):
                with mock.patch.object(pub.urllib.request, 'urlopen', side_effect=error):
                    self.assertFalse(pub.rekor_entry_matches(UUID, self.blob))


if __name__ == '__main__':
    unittest.main()
