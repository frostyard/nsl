"""Guard publication workflows against caches that lower-trust runs can write.

A job with write-valued permissions may have release credentials or write access.
Every cache-capable setup action in that job must set `cache: false`, and the job
must not restore an explicit cache. A cache written by a lower-trust workflow can
otherwise reach a publication job and execute code in a trusted context.
"""

import re
import unittest
from collections import namedtuple
from pathlib import Path


ROOT = Path(__file__).resolve().parent.parent
WORKFLOWS = ROOT / ".github" / "workflows"
SETUP_ACTIONS = (
    "actions/setup-go@",
    "jdx/mise-action@",
    "actions/setup-python@",
    "actions/setup-node@",
)
CACHE_ACTIONS = ("actions/cache@", "actions/cache/restore@")

CheckResult = namedtuple("CheckResult", "problems jobs_checked steps_checked")


def indentation(line):
    return len(line) - len(line.lstrip(" "))


def permission_is_write(value):
    """Report whether a scalar or mapping permission value grants write access."""
    if not value:
        return False
    value = value.strip()
    if value == "write-all":
        return True
    if value in ("{}", "read-all"):
        return False
    if value.startswith("{"):
        return bool(re.search(r":\s*[\"']?write(?:-all)?[\"']?(?:\s*[},]|$)", value))
    return any(re.fullmatch(r"write(?:-all)?", item) for item in value.split())


def check_workflow_caches(workflow_text):
    """Check one workflow text and return problems plus write-job/step counts."""
    lines = workflow_text.splitlines()
    jobs_start = None
    jobs_end = len(lines)

    for index, line in enumerate(lines):
        if indentation(line) == 0 and line.strip() == "jobs:":
            jobs_start = index + 1
            break
    if jobs_start is None:
        return CheckResult(["workflow has no jobs: block"], 0, 0)

    for index in range(jobs_start, len(lines)):
        if indentation(lines[index]) == 0 and lines[index].strip():
            jobs_end = index
            break

    workflow_permission = None
    workflow_permission_found = False
    for index, line in enumerate(lines[:jobs_start]):
        if indentation(line) != 0:
            continue
        match = re.match(r"^permissions:\s*(.*)$", line.strip())
        if match:
            workflow_permission_found = True
            workflow_permission = match.group(1).strip()
            workflow_permission_end = len(lines)
            for later in range(index + 1, len(lines)):
                if indentation(lines[later]) == 0 and lines[later].strip():
                    workflow_permission_end = later
                    break
            if workflow_permission:
                workflow_permission = workflow_permission.split("#", 1)[0].strip()
            else:
                mapping_values = []
                for later in range(index + 1, workflow_permission_end):
                    if indentation(lines[later]) == 2:
                        value_match = re.match(r"^\s*[A-Za-z0-9_-]+:\s*[\"']?([^\s#\"']+)", lines[later].strip())
                        if value_match:
                            mapping_values.append(value_match.group(1))
                workflow_permission = " " .join(mapping_values)
            break

    job_ranges = []
    job_name = None
    job_start = None
    for index in range(jobs_start, jobs_end):
        line = lines[index]
        if indentation(line) == 2:
            match = re.match(r"^  ([A-Za-z0-9_-]+):(?:\s*(?:#.*)?$)", line)
            if match:
                if job_name is not None:
                    job_ranges.append((job_name, job_start, index))
                job_name = match.group(1)
                job_start = index + 1
    if job_name is not None:
        job_ranges.append((job_name, job_start, jobs_end))

    problems = []
    jobs_checked = 0
    steps_checked = 0

    for name, start, end in job_ranges:
        write_permission = False
        job_permission_found = False
        job_permission = None

        for index in range(start, end):
            line = lines[index]
            stripped = line.strip()
            match = re.match(r"^permissions:\s*(.*)$", stripped)
            if indentation(line) == 4 and match:
                job_permission_found = True
                job_permission = match.group(1).strip().split("#", 1)[0].strip()
                if job_permission:
                    break
                mapping_values = []
                for later in range(index + 1, end):
                    if indentation(lines[later]) <= 4 and lines[later].strip():
                        break
                    value_match = re.match(r"^[A-Za-z0-9_-]+:\s*[\"']?([^\s#\"']+)", lines[later].strip())
                    if value_match:
                        mapping_values.append(value_match.group(1))
                job_permission = " ".join(mapping_values)
                break

        if job_permission_found:
            write_permission = permission_is_write(job_permission)
        elif workflow_permission_found:
            write_permission = permission_is_write(workflow_permission)
        else:
            write_permission = True

        if not write_permission:
            continue

        jobs_checked += 1
        steps_indent = None
        steps_start = end
        steps_end = end
        for index in range(start, end):
            line = lines[index]
            if indentation(line) == 4 and line.strip() == "steps:":
                steps_indent = indentation(line)
                steps_start = index + 1
                break
        if steps_indent is not None:
            for index in range(steps_start, end):
                line = lines[index]
                if line.strip() and indentation(line) <= steps_indent:
                    steps_end = index
                    break
            else:
                steps_end = end
        else:
            continue

        step_starts = []
        for index in range(steps_start, steps_end):
            line = lines[index]
            if indentation(line) == 6 and line.lstrip().startswith("-"):
                step_starts.append(index)

        for offset, step_start in enumerate(step_starts):
            step_end = step_starts[offset + 1] if offset + 1 < len(step_starts) else steps_end
            step_lines = lines[step_start:step_end]
            uses = None
            for line in step_lines:
                match = re.search(r"(?:^|\s)uses:\s*([^\s#]+)", line.strip())
                if match:
                    uses = match.group(1)
                    break

            if uses is None:
                continue

            setup_action = any(uses.startswith(action) for action in SETUP_ACTIONS)
            cache_action = any(uses.startswith(action) for action in CACHE_ACTIONS)
            if not setup_action and not cache_action:
                continue

            steps_checked += 1
            if cache_action:
                problems.append(f"job {name}: step uses {uses}; explicit cache actions are forbidden")
                continue

            with_indent = None
            with_end = step_end
            cache_value = None
            for relative_index, line in enumerate(step_lines):
                absolute_index = step_start + relative_index
                stripped = line.strip()
                if with_indent is None and re.match(r"^with:\s*(?:#.*)?$", stripped):
                    with_indent = indentation(line)
                elif with_indent is not None:
                    current_indent = indentation(line)
                    if current_indent <= with_indent and stripped:
                        with_end = absolute_index
                        break
                    match = re.match(r"^cache:\s*[\"']?([^\s#\"']+)[\"']?", stripped)
                    if match:
                        cache_value = match.group(1)

            if cache_value is None:
                problems.append(f"job {name}: {uses} has no cache key under with")
            elif cache_value != "false":
                problems.append(f"job {name}: {uses} has cache: {cache_value}, not cache: false")

    return CheckResult(problems, jobs_checked, steps_checked)


class WorkflowCaches(unittest.TestCase):
    def test_repository_write_jobs_disable_caches(self):
        total_jobs = 0
        total_steps = 0
        problems = []
        for path in sorted(WORKFLOWS.glob("*.yml")):
            result = check_workflow_caches(path.read_text(encoding="utf-8"))
            total_jobs += result.jobs_checked
            total_steps += result.steps_checked
            problems.extend(f"{path.relative_to(ROOT)}: {problem}" for problem in result.problems)
        print(f"checked {total_jobs} write-permission jobs and {total_steps} cache-sensitive steps")
        self.assertGreater(total_jobs, 0, "the repository guard checked 0 write-permission jobs")
        self.assertEqual(problems, [], "\n".join(problems))

    def test_mise_cache_true_fails(self):
        sample = """jobs:
  release:
    permissions:
      contents: write
    steps:
      - uses: jdx/mise-action@abc
        with:
          cache: true
"""
        result = check_workflow_caches(sample)
        self.assertTrue(result.problems)

    def test_setup_go_missing_cache_fails(self):
        sample = """jobs:
  release:
    permissions:
      contents: write
    steps:
      - uses: actions/setup-go@abc
        with:
          go-version-file: go.mod
"""
        result = check_workflow_caches(sample)
        self.assertTrue(result.problems)

    def test_explicit_cache_action_fails(self):
        sample = """jobs:
  release:
    permissions:
      contents: write
    steps:
      - uses: actions/cache@abc
"""
        result = check_workflow_caches(sample)
        self.assertTrue(result.problems)

    def test_read_only_job_with_cache_true_passes(self):
        sample = """jobs:
  test:
    permissions:
      contents: read
    steps:
      - uses: jdx/mise-action@abc
        with:
          cache: true
"""
        result = check_workflow_caches(sample)
        self.assertEqual(result.problems, [])

    def test_job_write_all_mise_cache_true_fails(self):
        sample = """jobs:
  release:
    permissions: write-all
    steps:
      - uses: jdx/mise-action@abc
        with:
          cache: true
"""
        result = check_workflow_caches(sample)
        self.assertTrue(result.problems)

    def test_workflow_write_mapping_inherited_by_job_fails(self):
        sample = """permissions:
  contents: write
jobs:
  release:
    steps:
      - uses: actions/setup-go@abc
        with:
          go-version-file: go.mod
"""
        result = check_workflow_caches(sample)
        self.assertTrue(result.problems)

    def test_empty_job_permissions_cache_true_passes(self):
        sample = """permissions: write-all
jobs:
  test:
    permissions: {}
    steps:
      - uses: jdx/mise-action@abc
        with:
          cache: true
"""
        result = check_workflow_caches(sample)
        self.assertEqual(result.problems, [])


if __name__ == "__main__":
    unittest.main()
