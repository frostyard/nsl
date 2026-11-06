"""Test the corrections-base target of the root Makefile in a temporary repository.

`scripts/check-corrections.mjs --base` reads the working tree's
`.memory/corrections.jsonl` and compares it line by line with the same file at
the BASE commit (`git show BASE:./.memory/corrections.jsonl`). Each case here
commits a BASE and then changes the working tree, so it tests exactly what the
target promises:

- a BASE that does not resolve to a commit fails, never passes as an old base;
- a BASE that predates the gate prints a notice and passes;
- an appended line passes;
- a changed line fails;
- a removed line fails;
- filling an empty promoted_to passes.

The target runs in the temporary repository's directory, so its `git rev-parse`,
`git cat-file` and `node scripts/check-corrections.mjs` all read that repository.
"""

import json
import os
import shutil
import subprocess
import tempfile
import unittest
from pathlib import Path

ROOT = Path(__file__).resolve().parent.parent
MAKEFILE = ROOT / "Makefile"
CHECKER = ROOT / "scripts" / "check-corrections.mjs"

# A commit needs an identity, and no case may depend on the machine's git config.
GIT_CONFIG = ["-c", "user.name=t", "-c", "user.email=t@example.org", "-c", "commit.gpgsign=false"]

# A commit must not pick up the ambient repository.
GIT_ENVIRONMENT = ("GIT_DIR", "GIT_WORK_TREE", "GIT_INDEX_FILE")

LINES = [
    {
        "date": "2026-11-04",
        "scope": "tests",
        "correction": "the first correction",
        "evidence": "this test",
        "promoted_to": "AGENTS.md",
    },
    {
        "date": "2026-11-04",
        "scope": "tests",
        "correction": "the second correction, promoted later",
        "evidence": "this test",
        "promoted_to": "",
    },
]
APPENDED = {
    "date": "2026-11-05",
    "scope": "release",
    "correction": "a correction the base does not have",
    "evidence": "this test",
    "promoted_to": "",
}


def environment():
    cleaned = dict(os.environ)
    for name in GIT_ENVIRONMENT:
        cleaned.pop(name, None)
    return cleaned


class CorrectionsBase(unittest.TestCase):
    def setUp(self):
        self.require_tools()
        self.directory = Path(tempfile.mkdtemp(prefix="nsl-corrections-base-"))
        self.addCleanup(shutil.rmtree, self.directory, ignore_errors=True)
        self.git("init", "-q")
        self.without_gate = self.commit("no corrections gate", allow_empty=True)

    def require_tools(self):
        if shutil.which("node") is None:
            self.fail(
                "node is not on PATH, so the corrections gate cannot run: "
                "run `mise install` to install the Node release pinned in mise.toml"
            )
        if shutil.which("make") is None:
            self.fail("make is not on PATH: this test runs the corrections-base target of the root Makefile")

    def git(self, *arguments):
        result = subprocess.run(
            ["git", *GIT_CONFIG, *arguments],
            cwd=self.directory,
            capture_output=True,
            text=True,
            env=environment(),
        )
        self.assertEqual(result.returncode, 0, f"git {' '.join(arguments)}: {result.stderr}")
        return result.stdout.strip()

    def commit(self, message, allow_empty=False):
        self.git("add", "-A")
        arguments = ["commit", "-q", "-m", message]
        if allow_empty:
            arguments.insert(2, "--allow-empty")
        self.git(*arguments)
        return self.git("rev-parse", "HEAD")

    def make(self, base):
        result = subprocess.run(
            ["make", "--no-print-directory", "-f", str(MAKEFILE), "corrections-base", f"BASE={base}"],
            cwd=self.directory,
            capture_output=True,
            text=True,
            env=environment(),
        )
        return result.returncode, result.stdout + result.stderr

    def write_lines(self, lines):
        path = self.directory / ".memory" / "corrections.jsonl"
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_text("".join(json.dumps(line) + "\n" for line in lines), encoding="utf-8")

    def install_gate(self):
        """Commit the gate and 2 correction lines; return that commit as BASE."""
        scripts = self.directory / "scripts"
        scripts.mkdir(parents=True, exist_ok=True)
        shutil.copyfile(CHECKER, scripts / "check-corrections.mjs")
        self.write_lines(LINES)
        return self.commit("corrections gate and 2 lines")

    def test_a_base_that_does_not_resolve_fails(self):
        code, output = self.make("nope")
        self.assertNotEqual(code, 0, f"BASE=nope must fail:\n{output}")
        self.assertIn("does not resolve", output, output)

    def test_a_base_without_the_gate_skips_the_comparison(self):
        code, output = self.make(self.without_gate)
        self.assertEqual(code, 0, f"a BASE without the gate must pass:\n{output}")
        self.assertIn("predates the corrections gate", output, output)

    def test_an_appended_line_passes(self):
        base = self.install_gate()
        self.write_lines([*LINES, APPENDED])
        code, output = self.make(base)
        self.assertEqual(code, 0, f"an appended line must pass:\n{output}")

    def test_a_changed_line_fails(self):
        base = self.install_gate()
        changed = [dict(LINES[0], correction="a rewritten correction"), LINES[1]]
        self.write_lines(changed)
        code, output = self.make(base)
        self.assertNotEqual(code, 0, f"a changed line must fail:\n{output}")
        self.assertIn("FAIL corrections", output, output)

    def test_a_removed_line_fails(self):
        base = self.install_gate()
        self.write_lines(LINES[1:])
        code, output = self.make(base)
        self.assertNotEqual(code, 0, f"a removed line must fail:\n{output}")
        self.assertIn("FAIL corrections", output, output)

    def test_filling_an_empty_promoted_to_passes(self):
        base = self.install_gate()
        self.write_lines([LINES[0], dict(LINES[1], promoted_to="AGENTS.md")])
        code, output = self.make(base)
        self.assertEqual(code, 0, f"filling an empty promoted_to must pass:\n{output}")


if __name__ == "__main__":
    unittest.main()
