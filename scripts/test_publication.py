"""Public reports must prove every suite passed and exclude private test state."""
import importlib.util
import json
from pathlib import Path
import tempfile
import unittest

spec = importlib.util.spec_from_file_location('publication', Path(__file__).with_name('publish-images.py'))
pub = importlib.util.module_from_spec(spec)
spec.loader.exec_module(pub)


class Publication(unittest.TestCase):
    def reports(self, directory):
        image = {'build_id': 'test', 'distribution': 'debian'}
        raw = {'digest': 'sha256:'+'a'*64, 'size': 100}
        cases = ('independent_vms_and_grown_roots', 'separate_homes', 'host_port_not_displaced',
                 'conflict_retry', 'cross_vm_conflict_and_handoff',
                 'forced_exit_recovery_preserves_data_and_peer', 'missing_forwarder_restarted')
        reports = {
            'results': dict(passed=True, image_sha256='a'*64, image=image, systemd='systemd 257',
                            root_filesystem='btrfs', argv_binary_streams_exit_and_pty=True, share_ownership=True),
            'lifecycle': {**{key: {'pass': True, 'private': '/home/example/key'} for key in cases},
                          'stop_dev': {'exit': 0}, 'stop_peer': {'exit': 0}},
            'maintenance': dict(passed=True, image=image, home='/private/vm', kernel_before='1', kernel_after='1',
                                podman_version='5', rootless=True, storage_driver='overlay', container_base_digest='sha256:test',
                                build_bind_volume_dns_https=True, host_localhost_http=True, kernel_reinstall_and_reboot=True,
                                newer_kernel_upgrade_tested=False, container_and_volume_survive_vm_restart=True),
            'storage': dict(passed=True, root_bytes_before=10, root_bytes_after=20, running_mutations_refused=True,
                            growth_preserved_identity_and_data=True, grown_backup_restores_without_cache=True,
                            removal_preserved_project_cache_backups=True, name_reused_with_new_identity=True, peer_uninterrupted=True),
        }
        for name, value in reports.items():
            (directory/f'{name}.json').write_text(json.dumps(value))
        (directory/'backup.nsl').write_text('PRIVATE BACKUP')
        return image, raw, reports

    def test_public_report_is_explicit_and_preserves_upgrade_distinction(self):
        with tempfile.TemporaryDirectory() as tmp:
            directory = Path(tmp)
            image, raw, _ = self.reports(directory)
            report = pub.acceptance(directory, image, raw)
            data = json.dumps(report)
            for private in ('/private', '/home/example', 'PRIVATE BACKUP'):
                self.assertNotIn(private, data)
            self.assertFalse(report['maintenance']['newer_kernel_upgrade_tested'])
            self.assertTrue(report['passed'])

    def test_incomplete_failed_or_mismatched_reports_cannot_promote(self):
        for mode in ('hash', 'image', 'lifecycle', 'shutdown', 'maintenance', 'storage'):
            with self.subTest(mode=mode), tempfile.TemporaryDirectory() as tmp:
                directory = Path(tmp)
                image, raw, reports = self.reports(directory)
                if mode == 'hash': reports['results']['image_sha256'] = 'b'*64
                if mode == 'image': reports['results']['image'] = {'build_id': 'different'}
                if mode == 'lifecycle': del reports['lifecycle']['separate_homes']
                if mode == 'shutdown': reports['lifecycle']['stop_peer']['exit'] = 1
                if mode in ('maintenance', 'storage'): reports[mode]['passed'] = False
                for name, value in reports.items():
                    (directory/f'{name}.json').write_text(json.dumps(value))
                with self.assertRaises(ValueError):
                    pub.acceptance(directory, image, raw)


if __name__ == '__main__':
    unittest.main()
