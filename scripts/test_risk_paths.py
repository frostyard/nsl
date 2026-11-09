"""Check .github/risk-paths.json against the repository it guards.

Core's PR triage workflow matches a pull request's paths against this list
(frostyard/core docs/specs/risk-paths.md, docs/specs/pr-triage.md), so a path
that the list misses leaves that change below the review level it needs. The
list is schema version 2: each entry has a `level`, and `default_level` applies
to a path that no glob matches. A version 1 list stays valid and reads as each
entry at L1 with a default of L3. This test holds the list to the repository:

- every `why` of an entry at L1 or L2 (rule B) starts with 1 category of the
  list's why column; an entry at L3 or L4 has free text;
- every tracked script is listed, except the license-notice renderer and the
  tests beside the scripts;
- every script that the publication or a workflow names is listed;
- every file that boots a machine, verifies an image, holds an identity or
  changes machine state is listed.

The glob semantics here are a subset of core's matcher (scripts/lib/risk-paths.mjs):
`**/` matches zero or more whole directories, `**` elsewhere matches any
characters including `/`, `*` matches any characters except `/`, and every other
character is literal. A glob with `?`, `[` or `{` is refused, so a list entry
that needs more than the subset fails here instead of matching differently. The
last tests check the translation itself.
"""

import json
import re
import subprocess
import unittest
from fnmatch import fnmatch
from functools import lru_cache
from pathlib import Path, PurePosixPath

ROOT = Path(__file__).resolve().parent.parent
LIST_FILE = ROOT / ".github" / "risk-paths.json"

# The why column's categories (frostyard/core docs/specs/risk-paths.md, rule B).
CATEGORIES = (
    "boot chain",
    "signing and keys",
    "update verification",
    "auth",
    "publication",
    "secrets and credentials",
    "rulesets and settings",
    "destructive operations",
    "governance",
)

# The levels whose `why` must name a category. An entry with no level is L1, as
# in a version 1 list; an entry at L3 or L4 has free text.
CATEGORY_LEVELS = ("L1", "L2")

# Tracked scripts that the list need not carry: the license-notice renderer and
# the tests beside the scripts.
UNLISTED_SCRIPTS = {"scripts/license-notices.py"}

# The Go files that boot, verify, hold an identity or change machine state. The
# agent and the protocol below are added to these from the index.
GO_FILES = (
    "image_verify.go",
    "image_registry.go",
    "image_cache.go",
    "image_contract.go",
    "vm.go",
    "machines.go",
    "storage.go",
    "archive.go",
    "state.go",
    "remote.go",
    "desktop.go",
)
GO_DIRECTORIES = ("cmd/nsl-agent", "internal/protocol")

# A script that the publication or a workflow names, such as
# scripts/probe-vm.py in a run line.
SCRIPT_REFERENCE = re.compile(r"scripts/[A-Za-z0-9_.-]+")


@lru_cache(maxsize=None)
def glob_regex(glob):
    """Translate a risk-path glob to a compiled regular expression."""
    parts = ["^"]
    index = 0
    unsupported = sorted(set(glob) & set("?[{"))
    if unsupported:
        raise ValueError(f"{glob}: unsupported glob characters {''.join(unsupported)}")
    while index < len(glob):
        if glob.startswith("**/", index):
            parts.append("(?:.*/)?")
            index += 3
        elif glob.startswith("**", index):
            parts.append(".*")
            index += 2
        elif glob[index] == "*":
            parts.append("[^/]*")
            index += 1
        else:
            parts.append(re.escape(glob[index]))
            index += 1
    parts.append("$")
    return re.compile("".join(parts))


def matches(glob, path):
    """Report whether the risk-path glob matches the whole repository path."""
    return glob_regex(glob).match(path) is not None


def risk_paths():
    with LIST_FILE.open(encoding="utf-8") as handle:
        return json.load(handle)["paths"]


def tracked(*paths):
    result = subprocess.run(
        ["git", "-C", str(ROOT), "ls-files", *paths],
        capture_output=True,
        text=True,
        check=True,
    )
    return [line for line in result.stdout.splitlines() if line]


def unlisted(paths, files):
    return [path for path in files if not any(matches(entry["glob"], path) for entry in paths)]


def needs_category(entry):
    """Report whether an entry's why must name a category (rule B).

    Only an entry at L1 or L2 does. An entry with no level is L1, as in a
    version 1 list; an entry at L3 or L4 has free text.
    """
    return entry.get("level", "L1") in CATEGORY_LEVELS


def category_problems(entries):
    """Return 1 line for each entry whose why names no required category."""
    return [
        f"{entry['glob']}: {entry.get('why', '')}"
        for entry in entries
        if needs_category(entry)
        and not any(entry.get("why", "").startswith(f"{category}:") for category in CATEGORIES)
    ]


def reference_sources():
    workflows = ROOT / ".github" / "workflows"
    return [ROOT / "scripts" / "publish-images.py", *(sorted(workflows.glob("*.yml")))]


class RiskPaths(unittest.TestCase):
    def test_every_level_1_and_2_why_names_a_category(self):
        paths = risk_paths()
        graded = [entry for entry in paths if needs_category(entry)]
        problems = category_problems(paths)
        print(f"checked {len(graded)} risk-path categories of {len(paths)} entries")
        self.assertEqual(
            problems,
            [],
            "the why of an entry at L1 or L2 must start with 1 of these categories and a colon "
            f"({', '.join(CATEGORIES)}): " + "; ".join(problems),
        )

    def test_the_category_rule_follows_the_level(self):
        free_text = [
            {"glob": "docs/**", "level": "L3", "why": "how-to documentation"},
            {"glob": "testdata/**", "level": "L4", "why": "test data"},
        ]
        self.assertEqual(category_problems(free_text), [], "the why of an L3 or L4 entry is free text")
        for entry in (
            {"glob": "Makefile", "level": "L1", "why": "the verify gate"},
            {"glob": ".svu.yml", "why": "the release tags"},
        ):
            problems = category_problems([entry])
            self.assertEqual(len(problems), 1, f"{entry} must be refused: {problems}")
            self.assertIn(entry["glob"], problems[0])

    def test_every_tracked_script_is_listed(self):
        paths = risk_paths()
        files = [
            path
            for path in tracked("scripts")
            if path not in UNLISTED_SCRIPTS and not fnmatch(PurePosixPath(path).name, "test_*.py")
        ]
        missing = unlisted(paths, files)
        print(f"checked {len(files)} tracked scripts against {len(paths)} globs")
        self.assertEqual(
            missing,
            [],
            "these tracked scripts match no risk path, so a change to them carries no risk tier: "
            + ", ".join(missing),
        )

    def test_every_named_script_is_listed(self):
        paths = risk_paths()
        names = set()
        for source in reference_sources():
            self.assertTrue(source.is_file(), f"{source} does not exist")
            names.update(SCRIPT_REFERENCE.findall(source.read_text(encoding="utf-8")))
        files = sorted(name for name in names if (ROOT / name).is_file())
        missing = unlisted(paths, files)
        print(f"checked {len(files)} scripts that the publication and the workflows name")
        self.assertEqual(
            missing,
            [],
            "these scripts are named by scripts/publish-images.py or a workflow but match no risk path: "
            + ", ".join(missing),
        )

    def test_every_sensitive_go_file_is_listed(self):
        paths = risk_paths()
        files = [path for path in GO_FILES if (ROOT / path).is_file()]
        self.assertEqual(
            files, list(GO_FILES), "a Go file of the list above is missing from the repository"
        )
        for directory in GO_DIRECTORIES:
            files.extend(tracked(directory))
            self.assertTrue((ROOT / directory).is_dir(), f"{directory} does not exist")
        missing = unlisted(paths, files)
        print(f"checked {len(files)} Go files against {len(paths)} globs")
        self.assertEqual(
            missing,
            [],
            "these Go files boot, verify, hold an identity or change machine state but match no risk path: "
            + ", ".join(missing),
        )

    def test_glob_translation(self):
        cases = [
            ("trust/**", "trust/a/b", True),
            ("trust/**", "trust/sigstore-root.json", True),
            ("scripts/*.py", "scripts/probe-vm.py", True),
            ("scripts/*.py", "scripts/a/b.py", False),
            ("Makefile", "Makefile", True),
            ("Makefile", "sub/Makefile", False),
            ("Makefile", "Makefile.bak", False),
            ("image/**", "image/snapshots/apparmor.txt", True),
            (".github/workflows/**", ".github/workflows/ci.yml", True),
            (".github/workflows/**", ".github/workflows", False),
            ("scripts/**/probe-vm.py", "scripts/probe-vm.py", True),
            ("scripts/**/probe-vm.py", "scripts/a/b/probe-vm.py", True),
            ("scripts/**/probe-vm.py", "scripts/xprobe-vm.py", False),
        ]
        problems = [
            f"{glob} must {'match' if expected else 'not match'} {path}, but it "
            f"{'does' if matches(glob, path) else 'does not'}"
            for glob, path, expected in cases
            if matches(glob, path) != expected
        ]
        print(f"checked {len(cases)} glob translations")
        self.assertEqual(problems, [], "; ".join(problems))

    def test_unsupported_glob_is_refused(self):
        for glob in ("scripts/probe-?.py", "scripts/[ab].py", "scripts/{a,b}.py"):
            with self.assertRaises(ValueError, msg=glob):
                glob_regex(glob)


if __name__ == "__main__":
    unittest.main()
