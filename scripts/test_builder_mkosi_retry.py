"""Root-free checks for the mkosi retry wrapper that guards the metadata sync."""
import json
import os
import subprocess
import sys
import tempfile
from pathlib import Path
import unittest

ROOT = Path(__file__).resolve().parents[1]
RETRY = ROOT/'scripts/builder-mkosi-retry'

FAKE = '''#!/usr/bin/env python3
import json, os, signal, sys
base = os.path.dirname(os.path.abspath(__file__))
counter = os.path.join(base, 'count')
n = int(open(counter).read()) if os.path.exists(counter) else 0
open(counter, 'w').write(str(n + 1))
attempts = json.load(open(os.path.join(base, 'script.json')))
text, code = attempts[min(n, len(attempts) - 1)]
sys.stdout.write(text)
sys.stdout.flush()
if code < 0:
    os.kill(os.getpid(), -code)
sys.exit(code)
'''

SYNC = '‣ Syncing package manager metadata\n'
DNF_OUTPUT = (
    "oss                                              89  B/s | 153  B     00:01    \n"
    "Errors during downloading metadata for repository 'oss':\n"
    "  - Status code: 404 for https://mirror.umd.edu/opensuse/tumbleweed/repo/oss/repodata/76b5cfdd\n"
    "Error: Failed to download metadata for repo 'oss': Yum repo downloading error\n")
FAILED = '‣ "dnf --assumeyes --best --releasever=tumbleweed --installroot=/buildroot makecache" returned non-zero exit code 1.\n'
OMITTED = '‣ (sandbox command omitted, rerun with --debug to see the full sandbox command.)\n'
INFO = '‣ Syncing package manager metadata: no incremental images, no caches to preserve\n'
FETCH = '‣ Fetching GPG key list from repository https://download.opensuse.org/tumbleweed/repo/oss\n'
FIXTURE = INFO + FETCH + SYNC + DNF_OUTPUT + FAILED + OMITTED
GOOD = '‣ Building image\n'


class Retry(unittest.TestCase):
    def run_retry(self, attempts, env=None):
        with tempfile.TemporaryDirectory() as tmp:
            tmp = Path(tmp)
            fake = tmp/'fake'
            fake.write_text(FAKE)
            fake.chmod(0o755)
            (tmp/'script.json').write_text(json.dumps(attempts))
            environment = dict(os.environ, NSL_SYNC_RETRY_DELAY='0')
            environment.pop('NSL_SYNC_TRIES', None)
            environment.update(env or {})
            result = subprocess.run([sys.executable, str(RETRY), sys.executable, str(fake)],
                                    capture_output=True, text=True, env=environment, timeout=60)
            count = int((tmp/'count').read_text()) if (tmp/'count').exists() else 0
            return result, count

    def test_the_real_fixture_retries_and_success_on_try_two_exits_zero(self):
        result, count = self.run_retry([[FIXTURE, 1], [GOOD, 0]])
        self.assertEqual((result.returncode, count), (0, 2))
        self.assertIn('Package metadata sync failed (try 1 of 3); retrying in 0 s.', result.stderr)
        self.assertEqual(result.stdout, FIXTURE + GOOD)

    def test_three_sync_failures_exit_with_the_child_code(self):
        result, count = self.run_retry([[FIXTURE, 1], [FIXTURE, 1], [FIXTURE, 7]])
        self.assertEqual((result.returncode, count), (7, 3))
        self.assertIn('(try 2 of 3)', result.stderr)
        self.assertIn('Package metadata sync failed after 3 tries.', result.stderr)
        self.assertEqual(result.stdout, FIXTURE * 2 + FIXTURE)

    def test_backoff_grows_by_three(self):
        result, _ = self.run_retry([[FIXTURE, 1]], {'NSL_SYNC_RETRY_DELAY': '0', 'NSL_SYNC_TRIES': '3'})
        self.assertIn('retrying in 0 s', result.stderr)
        result, count = self.run_retry([[FIXTURE, 1], [FIXTURE, 1], [GOOD, 0]], {'NSL_SYNC_RETRY_DELAY': '1'})
        self.assertEqual((result.returncode, count), (0, 3))
        self.assertIn('(try 1 of 3); retrying in 1 s', result.stderr)
        self.assertIn('(try 2 of 3); retrying in 3 s', result.stderr)

    def test_a_failure_after_a_later_step_does_not_retry(self):
        later = FIXTURE + '‣ Installing openSUSE\n' + FAILED + OMITTED
        result, count = self.run_retry([[later, 1], [GOOD, 0]])
        self.assertEqual((result.returncode, count), (1, 1))
        self.assertNotIn('retrying', result.stderr)

    def test_a_python_error_after_the_sync_does_not_retry(self):
        text = SYNC + FAILED + OMITTED + '‣ Interrupted by SIGINT signal\n'
        result, count = self.run_retry([[text, 1], [GOOD, 0]])
        self.assertEqual((result.returncode, count), (1, 1))

    def test_two_command_failures_do_not_retry(self):
        result, count = self.run_retry([[SYNC + FAILED + FAILED + OMITTED, 1], [GOOD, 0]])
        self.assertEqual(count, 1)

    def test_a_prefixed_and_ansi_sync_failure_retries(self):
        blue = '\x1b[34m[tools]\x1b[0m '
        text = (f'‣ {blue}Syncing package manager metadata\n' + DNF_OUTPUT
                + f'‣ {blue}\x1b[31m"dnf makecache" returned non-zero exit code 1.\x1b[0m\n'
                + f'‣ {blue}Sandbox command: bwrap --unshare-pid\n')
        result, count = self.run_retry([[text, 1], [GOOD, 0]])
        self.assertEqual((result.returncode, count), (0, 2))

    def test_plain_prefix_and_nesting_retry(self):
        text = '‣ [tools] Syncing package manager metadata\n' + '‣ [tools] ' + FAILED[2:]
        result, count = self.run_retry([[text, 1], [GOOD, 0]])
        self.assertEqual(count, 2)

    def test_a_quoted_argument_in_the_failed_command_retries(self):
        failed = '‣ "dnf --setopt=\'x="y"\' makecache" returned non-zero exit code 1.\n'
        result, count = self.run_retry([[SYNC + DNF_OUTPUT + failed + OMITTED, 1], [GOOD, 0]])
        self.assertEqual((result.returncode, count), (0, 2))

    def test_the_informational_line_alone_does_not_count(self):
        text = INFO + FAILED + OMITTED
        result, count = self.run_retry([[text, 1], [GOOD, 0]])
        self.assertEqual((result.returncode, count), (1, 1))

    def test_a_killed_child_does_not_retry(self):
        result, count = self.run_retry([[SYNC + DNF_OUTPUT, -9], [GOOD, 0]])
        self.assertEqual((result.returncode, count), (128 + 9, 1))

    def test_success_and_other_failures_pass_through_unchanged(self):
        result, count = self.run_retry([[GOOD + 'plain line\n', 0]])
        self.assertEqual((result.returncode, count, result.stdout), (0, 1, GOOD + 'plain line\n'))
        result, count = self.run_retry([['no step\n', 3], [GOOD, 0]])
        self.assertEqual((result.returncode, count), (3, 1))

    def test_bad_environment_values_exit_two(self):
        for env in ({'NSL_SYNC_TRIES': '0'}, {'NSL_SYNC_TRIES': 'x'}, {'NSL_SYNC_TRIES': '-1'},
                    {'NSL_SYNC_RETRY_DELAY': '-1'}, {'NSL_SYNC_RETRY_DELAY': '1.5'}, {'NSL_SYNC_RETRY_DELAY': ''}):
            with self.subTest(env=env):
                result, count = self.run_retry([[GOOD, 0]], env)
                self.assertEqual((result.returncode, count), (2, 0))
                self.assertIn(next(iter(env)), result.stderr)

    def test_the_tries_variable_limits_the_attempts(self):
        result, count = self.run_retry([[FIXTURE, 1]], {'NSL_SYNC_TRIES': '2'})
        self.assertEqual((result.returncode, count), (1, 2))
        self.assertIn('failed after 2 tries', result.stderr)


if __name__ == '__main__':
    unittest.main()
