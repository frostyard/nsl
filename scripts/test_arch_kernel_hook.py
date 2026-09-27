"""A failed new UKI must not remove the previous boot entry."""
import importlib.machinery
import importlib.util
import json
from pathlib import Path
import subprocess
import tempfile
import unittest

SOURCE = Path(__file__).resolve().parents[1]/'image/families/arch/overlay/usr/local/libexec/nsl-kernel-sync'
spec = importlib.util.spec_from_loader('kernel_sync', importlib.machinery.SourceFileLoader('kernel_sync', str(SOURCE)))
hook = importlib.util.module_from_spec(spec)
spec.loader.exec_module(hook)


class KernelHook(unittest.TestCase):
    def test_rejects_options_and_path_components_in_inventory(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            record = root/'var/lib/nsl/arch-kernels.json'
            record.parent.mkdir(parents=True)
            for version in ['--help', '..', '../kernel', '/kernel', '']:
                with self.subTest(version=version):
                    record.write_text(json.dumps([version]))
                    calls = []
                    with self.assertRaises(ValueError):
                        hook.synchronize(root, lambda args, **kwargs: calls.append(args))
                    self.assertEqual(calls, [])

    def test_failure_preserves_previous_inventory_and_entry(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            kernel = root/'usr/lib/modules/2-new/vmlinuz'
            kernel.parent.mkdir(parents=True); kernel.touch()
            record = root/'var/lib/nsl/arch-kernels.json'
            record.parent.mkdir(parents=True); record.write_text('["1-old"]')
            calls = []
            def fail(args, **kwargs):
                calls.append(args)
                raise subprocess.CalledProcessError(1, args)
            with self.assertRaises(subprocess.CalledProcessError):
                hook.synchronize(root, fail)
            self.assertEqual([c[1] for c in calls], ['add'])
            self.assertEqual(json.loads(record.read_text()), ['1-old'])
            calls.clear()
            hook.synchronize(root, lambda args, **kwargs: calls.append(args))
            self.assertEqual([c[1:3] for c in calls], [['add', '2-new'], ['remove', '1-old']])
            self.assertEqual(json.loads(record.read_text()), ['2-new'])
            kernel.unlink(); calls.clear()
            with self.assertRaises(ValueError):
                hook.synchronize(root, lambda args, **kwargs: calls.append(args))
            self.assertEqual(calls, [])
            self.assertEqual(json.loads(record.read_text()), ['2-new'])
