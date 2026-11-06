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


def normalize_yaml_line(line):
    """Remove a trailing YAML comment, without treating quoted # characters as comments."""
    quote = None
    escaped = False
    for index, char in enumerate(line):
        if escaped:
            escaped = False
            continue
        if char == "\\" and quote == '"':
            escaped = True
            continue
        if char in ("'", '"'):
            if quote is None:
                quote = char
            elif quote == char:
                if char == "'" and index + 1 < len(line) and line[index + 1] == "'":
                    continue
                quote = None
            continue
        if char == "#" and quote is None:
            if index == 0 or line[index - 1].isspace():
                line = line[:index]
                break
    return line.rstrip()


def normalized_yaml_lines(workflow_text):
    lines = []
    for line in workflow_text.splitlines():
        normalized = normalize_yaml_line(line)
        if normalized.strip():
            lines.append(normalized)
    return lines


def check_workflow_caches(workflow_text):
    """Check one workflow text and return problems plus write-job/step counts."""
    lines = normalized_yaml_lines(workflow_text)
    jobs_start = None
    jobs_indent = 0

    for index, line in enumerate(lines):
        match = re.match(r"^jobs:\s*$", line.strip())
        if indentation(line) == 0 and match:
            jobs_start = index + 1
            jobs_indent = indentation(line)
            break
    if jobs_start is None:
        return CheckResult(["workflow has no jobs: block"], 0, 0)

    jobs_end = len(lines)
    for index in range(jobs_start, len(lines)):
        if lines[index].strip() and indentation(lines[index]) <= jobs_indent:
            jobs_end = index
            break

    workflow_permission = None
    workflow_permission_found = False
    for index, line in enumerate(lines[:jobs_start]):
        match = re.match(r"^permissions:\s*(.*)$", line.strip())
        if indentation(line) == 0 and match:
            workflow_permission_found = True
            workflow_permission = match.group(1).strip()
            if not workflow_permission:
                values = []
                for later in range(index + 1, jobs_start):
                    if lines[later].strip() and indentation(lines[later]) == 0:
                        break
                    value_match = re.match(r"^[A-Za-z0-9_-]+:\s*[\"']?([^\s\"'#]+)", lines[later].strip())
                    if value_match:
                        values.append(value_match.group(1))
                workflow_permission = " ".join(values)
            break

    job_ranges = []
    job_name = None
    job_indent = None
    job_start = None
    for index in range(jobs_start, jobs_end):
        line = lines[index]
        current_indent = indentation(line)
        if not line.strip():
            continue
        if job_indent is None and current_indent > jobs_indent:
            job_indent = current_indent
        if job_indent is not None and current_indent == job_indent:
            match = re.match(r"^([A-Za-z0-9_-]+):\s*(.*)$", line.strip())
            if match:
                if job_name is not None:
                    job_ranges.append((job_name, job_start, index, job_indent))
                job_name = match.group(1)
                job_start = index + 1
    if job_name is not None:
        job_ranges.append((job_name, job_start, jobs_end, job_indent))

    problems = []
    jobs_checked = 0
    steps_checked = 0

    for name, start, end, indent in job_ranges:
        job_permission_found = False
        job_permission = None
        job_permission_indent = indent + 2
        reusable_workflow = False

        for index in range(start, end):
            line = lines[index]
            stripped = line.strip()
            if indentation(line) == job_permission_indent:
                permission_match = re.match(r"^permissions:\s*(.*)$", stripped)
                if permission_match:
                    job_permission_found = True
                    job_permission = permission_match.group(1).strip()
                    if not job_permission:
                        values = []
                        for later in range(index + 1, end):
                            if indentation(lines[later]) <= job_permission_indent and lines[later].strip():
                                break
                            value_match = re.match(r"^[A-Za-z0-9_-]+:\s*[\"']?([^\s\"'#]+)", lines[later].strip())
                            if value_match:
                                values.append(value_match.group(1))
                        job_permission = " ".join(values)
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

        for index in range(start, end):
            line = lines[index]
            if indentation(line) == job_permission_indent:
                uses_match = re.match(r"^uses:\s*([^\s]+)", line.strip())
                if uses_match:
                    reusable_workflow = True
                    break

        steps_key = None
        steps_indent = None
        steps_value = None
        for index in range(start, end):
            line = lines[index]
            if indentation(line) == job_permission_indent:
                steps_match = re.match(r"^steps:\s*(.*)$", line.strip())
                if steps_match:
                    steps_key = index
                    steps_indent = indentation(line)
                    steps_value = steps_match.group(1).strip()
                    break

        if reusable_workflow:
            continue
        if steps_key is None:
            problems.append(f"job {name}: could not find steps")
            continue
        if re.fullmatch(r"\[\]", steps_value or ""):
            continue

        item_indent = None
        for index in range(steps_key + 1, end):
            line = lines[index]
            stripped = line.strip()
            if not stripped:
                continue
            if indentation(line) >= steps_indent and stripped.startswith("-"):
                item_indent = indentation(line)
                break
            break

        if item_indent is None:
            problems.append(f"job {name}: could not parse steps")
            continue

        step_starts = []
        steps_end = end
        for index in range(steps_key + 1, end):
            line = lines[index]
            stripped = line.strip()
            if not stripped:
                continue
            current_indent = indentation(line)
            if current_indent < item_indent:
                steps_end = index
                break
            if current_indent == item_indent:
                if not stripped.startswith("-"):
                    steps_end = index
                    break
                step_starts.append(index)

        if not step_starts:
            problems.append(f"job {name}: could not parse steps")
            continue

        for offset, step_start in enumerate(step_starts):
            step_end = step_starts[offset + 1] if offset + 1 < len(step_starts) else steps_end
            step_lines = lines[step_start:step_end]
            uses = None
            for line in step_lines:
                stripped = line.strip()
                if stripped.startswith("-"):
                    stripped = stripped[1:].strip()
                match = re.match(r"^uses:\s*([^\s]+)", stripped)
                if match:
                    # A quoted value is the same action: compare it without its quotes.
                    uses = match.group(1).strip("\"'")
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
            cache_value = None
            for line in step_lines:
                stripped = line.strip()
                inline = stripped[1:].strip() if stripped.startswith("-") else stripped
                if re.match(r"^with:\s*(.*)$", inline):
                    with_indent = indentation(line) if not stripped.startswith("-") else item_indent
                    continue
                if with_indent is not None:
                    current_indent = indentation(line)
                    if stripped.startswith("-"):
                        current_indent = item_indent + 2
                    if current_indent > with_indent:
                        match = re.match(r"^cache:\s*[\"']?([^\s\"'#]+)", inline)
                        if match:
                            cache_value = match.group(1)
                    else:
                        with_indent = None

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
        self.assertGreater(total_steps, 0, "the repository guard checked 0 cache-sensitive steps")
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

    def test_same_indentation_mise_cache_true_fails(self):
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

    def test_same_indentation_setup_go_missing_cache_fails(self):
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

    def test_later_line_mise_cache_true_fails(self):
        sample = """jobs:
  release:
    permissions:
      contents: write
    steps:
      - name: x
        uses: jdx/mise-action@abc
        with:
          cache: true
"""
        result = check_workflow_caches(sample)
        self.assertTrue(result.problems)

    def test_unparseable_steps_fail_closed(self):
        sample = """jobs:
  release:
    permissions:
      contents: write
    steps:
      not-a-step: true
"""
        result = check_workflow_caches(sample)
        self.assertIn("could not parse steps", "; ".join(result.problems))

    def test_steps_trailing_comment_fails(self):
        sample = """jobs:
  release:
    permissions:
      contents: write
    steps: # publication
      - uses: jdx/mise-action@abc
        with:
          cache: true
"""
        result = check_workflow_caches(sample)
        self.assertTrue(result.problems)

    def test_permissions_trailing_comment_fails(self):
        sample = """jobs:
  release:
    permissions: # write
      contents: write
    steps:
      - uses: actions/setup-go@abc
        with:
          go-version-file: go.mod
"""
        result = check_workflow_caches(sample)
        self.assertTrue(result.problems)

    def test_with_trailing_comment_fails(self):
        sample = """jobs:
  release:
    permissions:
      contents: write
    steps:
      - uses: jdx/mise-action@abc
        with: # inputs
          cache: true
"""
        result = check_workflow_caches(sample)
        self.assertTrue(result.problems)

    def test_unknown_layout_fails_closed(self):
        sample = """jobs:
  release:
    permissions:
      contents: write
    stepz:
      - uses: jdx/mise-action@abc
"""
        result = check_workflow_caches(sample)
        self.assertIn("could not find steps", "; ".join(result.problems))

    def test_reusable_workflow_write_job_passes(self):
        sample = """jobs:
  call:
    permissions:
      contents: write
    uses: org/repo/.github/workflows/x.yml@sha
"""
        result = check_workflow_caches(sample)
        self.assertEqual(result.problems, [])

    def test_quoted_uses_value_fails(self):
        sample = """jobs:
  release:
    permissions:
      contents: write
    steps:
      - uses: "jdx/mise-action@abc"
        with:
          cache: true
      - uses: 'actions/setup-go@abc'
"""
        result = check_workflow_caches(sample)
        self.assertEqual(result.steps_checked, 2)
        self.assertEqual(len(result.problems), 2)


if __name__ == "__main__":
    unittest.main()
