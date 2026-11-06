"""Test the lint-version-check target of the root Makefile.

Each case runs the target in a temporary directory with a copy of the Makefile,
a pinned golangci-lint version, a go.mod Go version, and a stub binary.  This
isolates the target's pin, installed-version, and built-Go checks from the
machine toolchain.
"""

import os
import shutil
import subprocess
import tempfile
import unittest
from pathlib import Path

ROOT = Path(__file__).resolve().parent.parent
MAKEFILE = ROOT / "Makefile"

GIT_ENVIRONMENT = ("GIT_DIR", "GIT_WORK_TREE", "GIT_INDEX_FILE")


def environment():
    cleaned = dict(os.environ)
    for name in GIT_ENVIRONMENT:
        cleaned.pop(name, None)
    return cleaned


class LintVersionCheck(unittest.TestCase):
    def setUp(self):
        self.require_tools()
        self.directory = Path(tempfile.mkdtemp(prefix="nsl-lint-version-check-"))
        self.addCleanup(shutil.rmtree, self.directory, ignore_errors=True)
        shutil.copyfile(MAKEFILE, self.directory / "Makefile")

    def require_tools(self):
        if shutil.which("make") is None:
            self.fail("make is not on PATH: this test runs lint-version-check")

    def run_check(self, pin="2.15.0", go_mod="1.27.3", installed="2.15.0", built="1.27.0"):
        (self.directory / "mise.toml").write_text(
            f'golangci-lint = "{pin}"\n',
            encoding="utf-8",
        )
        (self.directory / "go.mod").write_text(
            f"module example.test/nsl-lint-version-check\n\ngo {go_mod}\n",
            encoding="utf-8",
        )

        bin_directory = self.directory / "bin"
        bin_directory.mkdir(exist_ok=True)
        stub = bin_directory / "golangci-lint"
        stub.write_text(
            "#!/bin/sh\n"
            'if [ "$1" = "version" ] && [ "$2" = "--short" ]; then\n'
            f"    printf '%s\\n' '{installed}'\n"
            'elif [ "$1" = "version" ]; then\n'
            f"    printf '%s\\n' 'golangci-lint has version {installed} built with go{built} from test'\n"
            "else\n"
            "    exit 1\n"
            "fi\n",
            encoding="utf-8",
        )
        stub.chmod(0o755)

        env = environment()
        env["PATH"] = str(bin_directory) + os.pathsep + env.get("PATH", "")
        result = subprocess.run(
            ["make", "--no-print-directory", "lint-version-check"],
            cwd=self.directory,
            capture_output=True,
            text=True,
            env=env,
        )
        return result.returncode, result.stdout + result.stderr

    def test_built_go1_27_0_go_mod_1_27_3_passes(self):
        code, output = self.run_check(go_mod="1.27.3", installed="2.15.0", built="1.27.0")
        self.assertEqual(code, 0, f"go1.27.0 and go1.27.3 must pass:\n{output}")

    def test_built_go1_27_3_go_mod_1_27_3_passes(self):
        code, output = self.run_check(go_mod="1.27.3", installed="2.15.0", built="1.27.3")
        self.assertEqual(code, 0, f"matching Go patches must pass:\n{output}")

    def test_built_go1_26_9_go_mod_1_27_3_fails(self):
        code, output = self.run_check(go_mod="1.27.3", installed="2.15.0", built="1.26.9")
        self.assertNotEqual(code, 0, f"an older Go minor must fail:\n{output}")
        self.assertIn("go1.26.9", output, output)
        self.assertIn("go1.27.3", output, output)

    def test_installed_2_14_0_pin_2_15_0_fails(self):
        code, output = self.run_check(pin="2.15.0", go_mod="1.27.3", installed="2.14.0", built="1.27.3")
        self.assertNotEqual(code, 0, f"an installed version that differs from the pin must fail:\n{output}")
        self.assertIn("expected golangci-lint 2.15.0", output, output)

    def test_built_go1_28_0_go_mod_1_27_3_passes(self):
        code, output = self.run_check(go_mod="1.27.3", installed="2.15.0", built="1.28.0")
        self.assertEqual(code, 0, f"a newer Go minor must pass:\n{output}")


if __name__ == "__main__":
    unittest.main()
