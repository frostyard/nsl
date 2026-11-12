"""Guard the single Go pin (frostyard/core ADR-0043).

go.mod is the only Go pin. mise reads it through
idiomatic_version_file_enable_tools = ["go"], and setup-go reads it in CI, so
mise.toml must never pin Go a second time. mise.lock keeps the Go checksums
that `mise lock` derives from go.mod; they must describe that same release.
"""

import re
import tomllib
import unittest
from pathlib import Path

ROOT = Path(__file__).resolve().parent.parent


def go_mod_directive(text, name):
    """Return the argument of a single-line go.mod directive, without a // comment."""
    for line in text.splitlines():
        fields = line.split("//", 1)[0].split()
        if len(fields) == 2 and fields[0] == name:
            return fields[1]
    return None


def go_mod_release(text):
    """Return the Go release go.mod selects: a toolchain goX line, else the go line.

    `toolchain default` names no release, so the go line selects it then.
    """
    toolchain = go_mod_directive(text, "toolchain")
    if toolchain and toolchain.startswith("go"):
        return toolchain.removeprefix("go")
    return go_mod_directive(text, "go")


def is_go_tool(name):
    """Report whether a mise [tools] key selects Go, with any backend or version suffix."""
    tool = name.split("@", 1)[0]
    return tool == "go" or tool.endswith(":go") or tool.endswith("/go")


def go_pin_problems(mise_toml, mise_lock, go_mod):
    """Return each way the three files break the single Go pin."""
    problems = []
    config = tomllib.loads(mise_toml)
    tools = config.get("tools", {})
    for name in tools:
        if is_go_tool(name):
            problems.append(f"mise.toml pins Go as [tools] {name}; go.mod is the only Go pin")
    idiomatic = config.get("settings", {}).get("idiomatic_version_file_enable_tools", [])
    if "go" not in idiomatic:
        problems.append('mise.toml must set [settings] idiomatic_version_file_enable_tools = ["go"]')

    directive = go_mod_directive(go_mod, "go")
    if not directive:
        problems.append("go.mod has no go directive")
    elif not re.fullmatch(r"1\.\d+\.\d+", directive):
        problems.append(f"go.mod's go directive {directive} is not a full 1.x.y release")
    release = go_mod_release(go_mod)

    locked = [entry.get("version") for entry in tomllib.loads(mise_lock).get("tools", {}).get("go", [])]
    if not locked:
        problems.append("mise.lock records no Go checksums; run `mise lock`")
    for version in locked:
        if release and version != release:
            problems.append(
                f"mise.lock records Go {version} but go.mod selects {release}; run `mise lock`"
            )
    return problems


MISE_TOML = """[settings]
idiomatic_version_file_enable_tools = ["go"]
lockfile = true

[tools]
golangci-lint = "2.15.0"
"""

MISE_LOCK = """lockfile_version = 1

[[tools.go]]
version = "1.27.4"
backend = "core:go"
"""

GO_MOD = "module example.test/go-pin\n\ngo 1.27.4\n"


class RepositoryGoPin(unittest.TestCase):
    def test_repository_pins_go_only_in_go_mod(self):
        problems = go_pin_problems(
            (ROOT / "mise.toml").read_text(encoding="utf-8"),
            (ROOT / "mise.lock").read_text(encoding="utf-8"),
            (ROOT / "go.mod").read_text(encoding="utf-8"),
        )
        self.assertEqual(problems, [])


class GoPinProblems(unittest.TestCase):
    def test_single_pin_passes(self):
        self.assertEqual(go_pin_problems(MISE_TOML, MISE_LOCK, GO_MOD), [])

    def test_mise_go_pin_fails(self):
        toml = MISE_TOML + 'go = "1.27.4"\n'
        problems = go_pin_problems(toml, MISE_LOCK, GO_MOD)
        self.assertTrue(any("pins Go" in problem for problem in problems), problems)

    def test_mise_go_pin_with_backend_fails(self):
        toml = MISE_TOML + '"core:go" = "1.27.4"\n'
        problems = go_pin_problems(toml, MISE_LOCK, GO_MOD)
        self.assertTrue(any("pins Go" in problem for problem in problems), problems)

    def test_mise_go_pin_with_version_suffix_fails(self):
        for key in ('"go@1.27.4"', '"core:go@1.27.4"'):
            toml = MISE_TOML + f'{key} = "latest"\n'
            problems = go_pin_problems(toml, MISE_LOCK, GO_MOD)
            self.assertTrue(any("pins Go" in problem for problem in problems), (key, problems))

    def test_mise_go_table_fails(self):
        toml = MISE_TOML + '\n[tools.go]\nversion = "1.27.4"\n'
        problems = go_pin_problems(toml, MISE_LOCK, GO_MOD)
        self.assertTrue(any("pins Go" in problem for problem in problems), problems)

    def test_missing_idiomatic_setting_fails(self):
        toml = MISE_TOML.replace('idiomatic_version_file_enable_tools = ["go"]\n', "")
        problems = go_pin_problems(toml, MISE_LOCK, GO_MOD)
        self.assertTrue(any("idiomatic_version_file_enable_tools" in problem for problem in problems), problems)

    def test_floating_go_directive_fails(self):
        problems = go_pin_problems(MISE_TOML, MISE_LOCK, "module example.test/go-pin\n\ngo 1.27\n")
        self.assertTrue(any("full 1.x.y" in problem for problem in problems), problems)

    def test_stale_lock_fails(self):
        go_mod = GO_MOD.replace("go 1.27.4", "go 1.27.5")
        problems = go_pin_problems(MISE_TOML, MISE_LOCK, go_mod)
        self.assertTrue(any("run `mise lock`" in problem for problem in problems), problems)

    def test_lock_without_go_fails(self):
        for lock in ("lockfile_version = 1\n", "lockfile_version = 1\n\n[tools]\ngo = []\n"):
            problems = go_pin_problems(MISE_TOML, lock, GO_MOD)
            self.assertTrue(any("records no Go" in problem for problem in problems), (lock, problems))

    def test_commented_toolchain_line_is_read(self):
        go_mod = GO_MOD + "\ntoolchain go1.28.1 // preferred toolchain\n"
        problems = go_pin_problems(MISE_TOML, MISE_LOCK, go_mod)
        self.assertTrue(any("go.mod selects 1.28.1" in problem for problem in problems), problems)

    def test_commented_go_directive_is_read(self):
        go_mod = GO_MOD.replace("go 1.27.4", "go 1.27.4 // minimum")
        self.assertEqual(go_pin_problems(MISE_TOML, MISE_LOCK, go_mod), [])

    def test_toolchain_default_uses_go_line(self):
        for line in ("toolchain default", "toolchain default // the go line"):
            go_mod = GO_MOD + f"\n{line}\n"
            self.assertEqual(go_pin_problems(MISE_TOML, MISE_LOCK, go_mod), [], line)

    def test_lock_follows_toolchain_line(self):
        go_mod = GO_MOD.replace("go 1.27.4", "go 1.27.0\n\ntoolchain go1.27.4")
        self.assertEqual(go_pin_problems(MISE_TOML, MISE_LOCK, go_mod), [])


if __name__ == "__main__":
    unittest.main()
