"""Test the corrections-base target of the root Makefile in a temporary repository.

`scripts/check-corrections.mjs --base` reads the record files in the working
tree's `.memory/corrections/` and compares them with the same folder at the
BASE commit (frostyard/core ADR-0081). Each case here commits a BASE and then
changes the working tree, so it tests exactly what the target promises:

- a BASE that does not resolve to a commit fails, never passes as an old base;
- a BASE that predates the gate prints a notice and passes;
- an added record file passes;
- a changed record fails;
- a removed record fails;
- filling an empty promoted_to passes;
- the old single corrections file fails when BASE has the folder, beside
  the folder or in its place.

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

RECORDS = [
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
ADDED = {
    "date": "2026-11-05",
    "scope": "release",
    "correction": "a correction the base does not have",
    "evidence": "this test",
    "promoted_to": "",
}


# The records at BASE, as (slug, record) pairs: each becomes 1 file.
BASE_RECORDS = [("legacy-0001", RECORDS[0]), ("legacy-0002", RECORDS[1])]

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

    @staticmethod
    def record_name(record, slug):
        return f"{record['date']}-{slug}.json"

    def write_records(self, records):
        """Write each (slug, record) pair as 1 file in canonical form; remove the others."""
        folder = self.directory / ".memory" / "corrections"
        folder.mkdir(parents=True, exist_ok=True)
        for old in folder.glob("*.json"):
            old.unlink()
        for slug, record in records:
            text = json.dumps(record, indent=2, ensure_ascii=False) + "\n"
            (folder / self.record_name(record, slug)).write_text(text, encoding="utf-8")

    def install_gate(self):
        """Commit the gate and 2 record files; return that commit as BASE."""
        scripts = self.directory / "scripts"
        scripts.mkdir(parents=True, exist_ok=True)
        shutil.copyfile(CHECKER, scripts / "check-corrections.mjs")
        self.write_records(BASE_RECORDS)
        return self.commit("corrections gate and 2 records")

    def test_a_base_that_does_not_resolve_fails(self):
        code, output = self.make("nope")
        self.assertNotEqual(code, 0, f"BASE=nope must fail:\n{output}")
        self.assertIn("does not resolve", output, output)

    def test_a_base_without_the_gate_skips_the_comparison(self):
        code, output = self.make(self.without_gate)
        self.assertEqual(code, 0, f"a BASE without the gate must pass:\n{output}")
        self.assertIn("predates the corrections gate", output, output)

    def test_an_added_record_passes(self):
        base = self.install_gate()
        self.write_records([*BASE_RECORDS, ("pr-9-added", ADDED)])
        code, output = self.make(base)
        self.assertEqual(code, 0, f"an added record must pass:\n{output}")

    def test_a_changed_record_fails(self):
        base = self.install_gate()
        first, second = BASE_RECORDS
        changed = [(first[0], dict(first[1], correction="a rewritten correction")), second]
        self.write_records(changed)
        code, output = self.make(base)
        self.assertNotEqual(code, 0, f"a changed record must fail:\n{output}")
        self.assertIn("FAIL corrections", output, output)

    def test_a_removed_record_fails(self):
        base = self.install_gate()
        self.write_records(BASE_RECORDS[1:])
        code, output = self.make(base)
        self.assertNotEqual(code, 0, f"a removed record must fail:\n{output}")
        self.assertIn("record removed", output, output)

    def test_filling_an_empty_promoted_to_passes(self):
        base = self.install_gate()
        first, second = BASE_RECORDS
        self.write_records([first, (second[0], dict(second[1], promoted_to="AGENTS.md"))])
        code, output = self.make(base)
        self.assertEqual(code, 0, f"filling an empty promoted_to must pass:\n{output}")

    def test_the_old_corrections_file_beside_the_folder_fails(self):
        base = self.install_gate()
        old = self.directory / ".memory" / "corrections.jsonl"
        old.write_text(json.dumps(ADDED) + "\n", encoding="utf-8")
        code, output = self.make(base)
        self.assertNotEqual(code, 0, f"the old corrections file beside the folder must fail:\n{output}")
        self.assertIn("both .memory/corrections.jsonl and .memory/corrections/ exist", output, output)

    def test_the_old_corrections_file_instead_of_the_folder_fails(self):
        base = self.install_gate()
        shutil.rmtree(self.directory / ".memory" / "corrections")
        old = self.directory / ".memory" / "corrections.jsonl"
        old.write_text(json.dumps(ADDED) + "\n", encoding="utf-8")
        code, output = self.make(base)
        self.assertNotEqual(code, 0, f"the old corrections file must not come back:\n{output}")
        self.assertIn("is retired in this repository", output, output)

if __name__ == "__main__":
    unittest.main()
