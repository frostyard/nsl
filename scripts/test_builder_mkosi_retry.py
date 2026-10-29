"""Root-free checks for the mkosi retry wrapper that guards the metadata sync and downloads."""
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

APT_STEP = '‣ Installing Ubuntu\n'
APT_FAILED = '‣ "apt-get -y -o Dir=/buildroot install --no-install-recommends foo" returned non-zero exit code 100.\n'
APT_DOWNLOAD = (
    'Get:1 http://archive.ubuntu.com/ubuntu noble/main amd64 foo amd64 1.0 [1,234 B]\n'
    'Err:1 http://archive.ubuntu.com/ubuntu noble/main amd64 foo amd64 1.0\n'
    'E: Failed to fetch http://archive.ubuntu.com/ubuntu/pool/main/f/foo/foo_1.0_amd64.deb  404  '
    'Not Found [IP: 1.2.3.4 80]\n'
    'E: Unable to fetch some archives, maybe run apt-get update or try with --fix-missing?\n')
APT_FIXTURE = APT_STEP + APT_DOWNLOAD + APT_FAILED + OMITTED
APT_MISSING = APT_STEP + 'Reading package lists...\nE: Unable to locate package foo\n' + APT_FAILED + OMITTED

DNF4_STEP = '‣ Installing Fedora Linux\n'
DNF4_FAILED = '‣ "dnf --assumeyes --best --installroot=/buildroot install foo" returned non-zero exit code 1.\n'
DNF4_DOWNLOAD = (
    'Foo                    1.2 MB/s | 3.0 MB     00:02\n'
    'Error: Error downloading packages:\n'
    '  foo-1.0-1.x86_64: Cannot download, all mirrors were already tried without success\n')
DNF4_FIXTURE = DNF4_STEP + DNF4_DOWNLOAD + DNF4_FAILED + OMITTED
DNF4_MISSING = (DNF4_STEP + 'No match for argument: foo\nError: Unable to find a match: foo\n'
                + DNF4_FAILED + OMITTED)

DNF5_STEP = '‣ Installing Fedora Linux\n'
DNF5_FAILED = '‣ "dnf5 --assumeyes --installroot=/buildroot install foo" returned non-zero exit code 1.\n'
DNF5_DOWNLOAD = (
    '>>> Status code: 404 for https://mirror.example/fedora/foo-1.0.x86_64.rpm (IP: 1.2.3.4)\n'
    '>>> Librepo error: Cannot download\n'
    'Failed to download packages\n'
    ' Librepo error: Cannot download, all mirrors were already tried without success\n')
DNF5_FIXTURE = DNF5_STEP + DNF5_DOWNLOAD + DNF5_FAILED + OMITTED
DNF5_MISSING = (DNF5_STEP + 'No match for argument: foo\nFailed to resolve the transaction:\n'
                'Problem: cannot install foo-1.0-1.x86_64\n' + DNF5_FAILED + OMITTED)

PACMAN_STEP = '‣ Installing Arch Linux\n'
PACMAN_FAILED = '‣ "pacman --root /buildroot --noconfirm --sync foo" returned non-zero exit code 1.\n'
PACMAN_DOWNLOAD = (
    'resolving dependencies...\n'
    "error: failed retrieving file 'foo.pkg.tar.zst' from mirror.example : "
    'The requested URL returned error: 404\n'
    'warning: failed to retrieve some files\n'
    'error: failed to commit transaction (failed to retrieve some files)\n'
    'Errors occurred, no packages were upgraded.\n')
PACMAN_FIXTURE = PACMAN_STEP + PACMAN_DOWNLOAD + PACMAN_FAILED + OMITTED
PACMAN_MISSING = PACMAN_STEP + 'error: target not found: foo\n' + PACMAN_FAILED + OMITTED

ZYPPER_STEP = '‣ Installing openSUSE\n'
ZYPPER_FAILED = '‣ "zypper --root /buildroot --non-interactive install foo" returned non-zero exit code 106.\n'
ZYPPER_DOWNLOAD = (
    'Retrieving: foo-1.0.x86_64.rpm .............................[error]\n'
    'Failed to provide Package foo-1.0.x86_64. Do you want to retry retrieval?\n'
    '\n'
    '[1/1] package foo-1.0.x86_64 requires libbar.so.1, but this requirement cannot be provided\n'
    'Abort, retry, ignore? [a/r/i] (a): a\n'
    'Problem occurred during or after installation or removal of packages:\n'
    'Installation has been aborted as directed.\n')
ZYPPER_FIXTURE = ZYPPER_STEP + ZYPPER_DOWNLOAD + ZYPPER_FAILED + OMITTED
ZYPPER_MISSING = (ZYPPER_STEP + "No provider of 'foo' found.\n"
                  "'foo' not found in package names. Trying capabilities.\n" + ZYPPER_FAILED + OMITTED)

KEYRING = (
    '‣ [tools] Downloading Arch Linux keyring\n'
    "error: failed retrieving file 'archlinux-keyring-20240101-1-any.pkg.tar.zst' from mirror.example "
    ': The requested URL returned error: 404\n'
    'warning: failed to retrieve some files\n'
    'error: failed to commit transaction (failed to retrieve some files)\n'
    '‣ [tools] "pacman --root /buildroot --noconfirm --sync --downloadonly archlinux-keyring" '
    'returned non-zero exit code 1.\n'
    '‣ [tools] (sandbox command omitted, rerun with --debug to see the full sandbox command.)\n')

DOWNLOADS = (('apt-get', APT_FIXTURE), ('dnf 4', DNF4_FIXTURE), ('dnf5', DNF5_FIXTURE),
             ('pacman', PACMAN_FIXTURE), ('zypper', ZYPPER_FIXTURE), ('arch keyring', KEYRING))
MISSING = (('apt-get', APT_MISSING), ('dnf 4', DNF4_MISSING), ('dnf5', DNF5_MISSING),
           ('pacman', PACMAN_MISSING), ('zypper', ZYPPER_MISSING))
MISSING_LINES = (('apt-get', APT_FIXTURE, 'E: Unable to locate package bar'),
                 ('dnf 4', DNF4_FIXTURE, 'No match for argument: bar'),
                 ('dnf5', DNF5_FIXTURE, 'Failed to resolve the transaction:'),
                 ('pacman', PACMAN_FIXTURE, 'error: target not found: bar'),
                 ('zypper', ZYPPER_FIXTURE, "No provider of 'bar' found."))


def before_failure(text, line):
    """Insert a plain output line just before the command failure line of a fixture."""
    lines = text.splitlines(keepends=True)
    for index in range(len(lines) - 1, -1, -1):
        if lines[index].startswith('‣ ') and 'returned non-zero exit code' in lines[index]:
            lines.insert(index, line + '\n')
            return ''.join(lines)
    raise AssertionError('the fixture has no failure line')


class Retry(unittest.TestCase):
    def run_retry(self, attempts, env=None):
        with tempfile.TemporaryDirectory() as tmp:
            tmp = Path(tmp)
            fake = tmp/'fake'
            fake.write_text(FAKE)
            fake.chmod(0o755)
            (tmp/'script.json').write_text(json.dumps(attempts))
            environment = dict(os.environ, NSL_MKOSI_RETRY_DELAY='0')
            environment.pop('NSL_MKOSI_TRIES', None)
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
        result, _ = self.run_retry([[FIXTURE, 1]], {'NSL_MKOSI_RETRY_DELAY': '0', 'NSL_MKOSI_TRIES': '3'})
        self.assertIn('retrying in 0 s', result.stderr)
        result, count = self.run_retry([[FIXTURE, 1], [FIXTURE, 1], [GOOD, 0]], {'NSL_MKOSI_RETRY_DELAY': '1'})
        self.assertEqual((result.returncode, count), (0, 3))
        self.assertIn('(try 1 of 3); retrying in 1 s', result.stderr)
        self.assertIn('(try 2 of 3); retrying in 3 s', result.stderr)

    def test_a_failure_after_a_later_step_does_not_retry(self):
        later = FIXTURE + '‣ Installing openSUSE\n' + FAILED + OMITTED
        result, count = self.run_retry([[later, 1], [GOOD, 0]])
        self.assertEqual((result.returncode, count), (1, 1))
        self.assertNotIn('retrying', result.stderr)

    def test_an_interrupt_after_the_sync_does_not_retry(self):
        text = SYNC + FAILED + OMITTED + '‣ Interrupted by SIGINT signal\n'
        result, count = self.run_retry([[text, 1], [GOOD, 0]])
        self.assertEqual((result.returncode, count), (1, 1))

    def test_a_traceback_after_the_failure_does_not_retry(self):
        text = FIXTURE + ('Traceback (most recent call last):\n'
                          '  File "/root/mkosi/run.py", line 1, in run\n'
                          'OSError: [Errno 28] No space left on device\n')
        result, count = self.run_retry([[text, 1], [GOOD, 0]])
        self.assertEqual((result.returncode, count), (1, 1))

    def test_a_traceback_during_a_download_does_not_retry(self):
        text = DNF5_STEP + DNF5_DOWNLOAD + 'Traceback (most recent call last):\n' + DNF5_FAILED
        result, count = self.run_retry([[text, 1], [GOOD, 0]])
        self.assertEqual((result.returncode, count), (1, 1))

    def test_a_failed_cleanup_command_after_the_sync_does_not_retry(self):
        text = SYNC + '‣ "rm -rf -- /var/tmp/mkosi-workspace" returned non-zero exit code 1.\n' + OMITTED
        result, count = self.run_retry([[text, 1], [GOOD, 0]])
        self.assertEqual((result.returncode, count), (1, 1))

    def test_an_install_command_after_the_sync_does_not_retry(self):
        text = SYNC + '‣ "dnf --assumeyes install foo" returned non-zero exit code 1.\n' + OMITTED
        result, count = self.run_retry([[text, 1], [GOOD, 0]])
        self.assertEqual(count, 1)

    def test_other_package_manager_sync_failures_retry(self):
        for command in ('/usr/bin/apt-get -o Dir=/x update', 'pacman --config /x --sync --refresh',
                        'zypper --root /x refresh', 'dnf5 --refresh makecache'):
            with self.subTest(command=command):
                text = SYNC + f'‣ "{command}" returned non-zero exit code 100.\n' + OMITTED
                result, count = self.run_retry([[text, 100], [GOOD, 0]])
                self.assertEqual((result.returncode, count), (0, 2))

    def test_an_unparsable_command_does_not_retry(self):
        text = SYNC + '‣ "dnf makecache \'unclosed" returned non-zero exit code 1.\n'
        result, count = self.run_retry([[text, 1], [GOOD, 0]])
        self.assertEqual(count, 1)

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
        for env in ({'NSL_MKOSI_TRIES': '0'}, {'NSL_MKOSI_TRIES': 'x'}, {'NSL_MKOSI_TRIES': '-1'},
                    {'NSL_MKOSI_RETRY_DELAY': '-1'}, {'NSL_MKOSI_RETRY_DELAY': '1.5'},
                    {'NSL_MKOSI_RETRY_DELAY': ''}):
            with self.subTest(env=env):
                result, count = self.run_retry([[GOOD, 0]], env)
                self.assertEqual((result.returncode, count), (2, 0))
                self.assertIn(next(iter(env)), result.stderr)

    def test_the_tries_variable_limits_the_attempts(self):
        result, count = self.run_retry([[FIXTURE, 1]], {'NSL_MKOSI_TRIES': '2'})
        self.assertEqual((result.returncode, count), (1, 2))
        self.assertIn('failed after 2 tries', result.stderr)

    def test_a_download_failure_retries_and_succeeds_on_try_two(self):
        for tool, text in DOWNLOADS:
            with self.subTest(tool=tool):
                result, count = self.run_retry([[text, 1], [GOOD, 0]])
                self.assertEqual((result.returncode, count), (0, 2))
                self.assertIn('Package download failed (try 1 of 3); retrying in 0 s.', result.stderr)
                self.assertEqual(result.stdout, text + GOOD)

    def test_a_missing_package_fails_at_once(self):
        for tool, text in MISSING:
            with self.subTest(tool=tool):
                result, count = self.run_retry([[text, 1], [GOOD, 0]])
                self.assertEqual((result.returncode, count), (1, 1))
                self.assertNotIn('retrying', result.stderr)

    def test_a_download_failure_mixed_with_a_missing_package_does_not_retry(self):
        for tool, text, line in MISSING_LINES:
            with self.subTest(tool=tool):
                result, count = self.run_retry([[before_failure(text, line), 1], [GOOD, 0]])
                self.assertEqual((result.returncode, count), (1, 1))
                self.assertNotIn('retrying', result.stderr)

    def test_a_download_failure_outside_an_install_step_does_not_retry(self):
        postinstall = '‣ Running postinstall script /usr/lib/pm-utils/power.d/95anacron…\n'
        text = postinstall + APT_FIXTURE[len(APT_STEP):]
        result, count = self.run_retry([[text, 1], [GOOD, 0]])
        self.assertEqual((result.returncode, count), (1, 1))

    def test_other_installing_steps_do_not_retry(self):
        for header in ('Installing cache copies', 'Installing systemd-boot…',
                       'Installing prebuilt UKI at /efi/nsl.efi…'):
            with self.subTest(step=header):
                text = f'‣ {header}\n' + PACMAN_DOWNLOAD + PACMAN_FAILED + OMITTED
                result, count = self.run_retry([[text, 1], [GOOD, 0]])
                self.assertEqual((result.returncode, count), (1, 1))

    def test_a_remove_command_in_an_install_step_does_not_retry(self):
        failed = '‣ "dnf5 --assumeyes remove foo" returned non-zero exit code 1.\n'
        result, count = self.run_retry([[DNF5_STEP + DNF5_DOWNLOAD + failed + OMITTED, 1], [GOOD, 0]])
        self.assertEqual((result.returncode, count), (1, 1))

    def test_an_apt_package_script_error_does_not_retry(self):
        text = (APT_STEP + APT_DOWNLOAD
                + 'E: Sub-process /usr/bin/dpkg returned an error code (1)\n'
                + APT_FAILED + OMITTED)
        result, count = self.run_retry([[text, 1], [GOOD, 0]])
        self.assertEqual((result.returncode, count), (1, 1))

    def test_three_download_failures_exit_with_the_child_code(self):
        result, count = self.run_retry([[DNF5_FIXTURE, 1], [DNF5_FIXTURE, 1], [DNF5_FIXTURE, 7]])
        self.assertEqual((result.returncode, count), (7, 3))
        self.assertIn('(try 2 of 3)', result.stderr)
        self.assertIn('Package download failed after 3 tries.', result.stderr)

    def test_a_sync_failure_and_a_download_failure_share_the_tries(self):
        result, count = self.run_retry([[FIXTURE, 1], [DNF5_FIXTURE, 1], [GOOD, 0]])
        self.assertEqual((result.returncode, count), (0, 3))
        self.assertIn('Package metadata sync failed (try 1 of 3); retrying in 0 s.', result.stderr)
        self.assertIn('Package download failed (try 2 of 3); retrying in 0 s.', result.stderr)


if __name__ == '__main__':
    unittest.main()
