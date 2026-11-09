"""Tests for the release-config job's decision on release validation.

The job must pass with a notice when GORELEASER_KEY is absent on a Dependabot
or fork pull request, and it must fail on every other run without the key.
The test reads the job's `Decide on release validation` step from
.github/workflows/ci.yml and runs its script with a clean environment.
"""

import os
import re
import subprocess
import tempfile
import textwrap
import unittest
from pathlib import Path


ROOT = Path(__file__).resolve().parents[1]
WORKFLOW = ROOT / ".github/workflows/ci.yml"
JOB = "release-config"
STEP = "Decide on release validation"
KEYED_STEPS = (
    "- uses: actions/setup-go@",
    "- uses: goreleaser/goreleaser-action@",
    "- name: Render the Homebrew cask",
    "- name: Check the Homebrew cask",
)


def block_of(lines, index, indent):
    """Return the lines nested under lines[index], which has the given indent."""
    block = []
    for line in lines[index + 1:]:
        if line.strip() and len(line) - len(line.lstrip()) <= indent:
            break
        block.append(line)
    return block


def job_lines(text):
    lines = text.splitlines()
    for index, line in enumerate(lines):
        match = re.match(r"^(\s*)" + re.escape(JOB) + r":\s*$", line)
        if match:
            return block_of(lines, index, len(match.group(1)))
    raise AssertionError(f"no {JOB}: job")


def step_blocks(job):
    """Split the job's `steps:` list into blocks, each starting at a `- ` line."""
    steps = []
    current = None
    for line in job:
        if re.match(r"^\s*- ", line) or re.match(r"^\s*-$", line.rstrip()) or line.strip() == "-":
            current = [line]
            steps.append(current)
        elif current is not None:
            current.append(line)
    return steps


def step_text(job, name):
    marker = f"- name: {name}"
    for block in step_blocks(job):
        if block[0].strip() == marker:
            return "\n".join(block)
    raise AssertionError(f"no step {name!r}")


def run_script(job):
    """Return the dedented `run: |` script of the decision step."""
    step = step_blocks(job)
    target = None
    for block in step:
        if block[0].strip() == f"- name: {STEP}":
            target = block
            break
    if target is None:
        raise AssertionError(f"no step {STEP!r}")
    for index, line in enumerate(target):
        match = re.match(r"^(\s*)run:\s*\|\s*$", line)
        if not match:
            continue
        return textwrap.dedent("\n".join(block_of(target, index, len(match.group(1)))))
    raise AssertionError(f"step {STEP!r} has no run: | block")


class ReleaseConfig(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.text = WORKFLOW.read_text(encoding="utf-8")
        cls.job = job_lines(cls.text)
        cls.script = run_script(cls.job)

    def run_case(self, case):
        env = {
            "PATH": os.environ.get("PATH", "/usr/bin:/bin"),
            "HAS_GORELEASER_KEY": case["key"],
            "EVENT_NAME": case["event"],
            "HEAD_REPOSITORY": case.get("head", ""),
            "REPOSITORY": case.get("repo", ""),
            "ACTOR": case.get("actor", ""),
        }
        with tempfile.TemporaryDirectory() as directory:
            summary = Path(directory) / "summary.md"
            if case.get("summary"):
                env["GITHUB_STEP_SUMMARY"] = str(summary)
            else:
                env.pop("GITHUB_STEP_SUMMARY", None)
            done = subprocess.run(
                ["bash", "-c", self.script],
                capture_output=True,
                text=True,
                env=env,
                cwd=directory,
            )
            written = summary.read_text(encoding="utf-8") if summary.exists() else ""
        return done, written

    def test_cases(self):
        nothing = {
            "key": "true",
            "event": "push",
            "repo": "frostyard/nsl",
            "actor": "bketelsen",
        }
        fork = {
            "key": "false",
            "event": "pull_request",
            "head": "other/nsl",
            "repo": "frostyard/nsl",
            "actor": "bketelsen",
            "summary": True,
        }
        dependabot = {
            "key": "false",
            "event": "pull_request",
            "head": "frostyard/nsl",
            "repo": "frostyard/nsl",
            "actor": "dependabot[bot]",
            "summary": True,
        }
        cases = [
            (nothing, 0, None, None),
            (fork, 0, "::notice", "fork"),
            (dependabot, 0, "::notice", "Dependabot"),
            ({**dependabot, "actor": "bketelsen", "summary": False}, 1, "::error", None),
            ({**nothing, "key": "false", "actor": "dependabot[bot]"}, 1, "::error", None),
            ({**nothing, "key": "false", "event": "merge_group"}, 1, "::error", None),
            ({**nothing, "key": "false"}, 1, "::error", None),
        ]
        checked = 0
        for case, code, marker, text in cases:
            with self.subTest(case=case):
                done, written = self.run_case(case)
                output = done.stdout + done.stderr
                self.assertEqual(done.returncode, code, output)
                if marker is None:
                    self.assertNotIn("::notice", output)
                    self.assertNotIn("::error", output)
                    self.assertEqual(written, "")
                else:
                    self.assertIn(marker, output)
                    self.assertIn(text or "", output)
                    if case.get("summary"):
                        self.assertIn("GORELEASER_KEY is absent because", written)
                        if text:
                            self.assertIn(text, written)
                    else:
                        self.assertEqual(written, "")
                checked += 1
        print(f"checked {checked} cases")

    def test_decision_step_is_first_and_has_no_if(self):
        first = step_blocks(self.job)[0]
        self.assertEqual(first[0].strip(), f"- name: {STEP}")
        step = step_text(self.job, STEP)
        for line in step.splitlines():
            self.assertIsNone(re.match(r"^\s*if:", line), line)

    def test_decision_step_passes_inputs_through_env(self):
        step = step_text(self.job, STEP)
        for line in (
            "EVENT_NAME: ${{ github.event_name }}",
            "HEAD_REPOSITORY: ${{ github.event.pull_request.head.repo.full_name }}",
            "REPOSITORY: ${{ github.repository }}",
            "ACTOR: ${{ github.actor }}",
        ):
            self.assertIn(line, step)
        self.assertNotIn("${{", self.script)

    def test_old_steps_are_gone(self):
        self.assertNotIn("Require release validation on trusted runs", self.text)
        self.assertNotIn("Explain fork PR validation gap", self.text)

    def test_keyed_steps_keep_their_guard(self):
        steps = [block[0].strip() for block in step_blocks(self.job)]
        guarded = [
            block[0].strip() for block in step_blocks(self.job)
            if "if: env.HAS_GORELEASER_KEY == 'true'" in "\n".join(block)
        ]
        self.assertEqual(len(guarded), len(KEYED_STEPS), steps)
        for marker in KEYED_STEPS:
            self.assertTrue(any(marker in line for line in guarded), (marker, guarded))


if __name__ == "__main__":
    unittest.main()
