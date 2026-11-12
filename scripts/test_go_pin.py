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


def go_mod_release(text):
    """Return the Go release go.mod selects: the toolchain line, else the go line."""
    toolchain = re.search(r"^toolchain go(\S+)\s*$", text, re.MULTILINE)
    if toolchain:
        return toolchain.group(1)
    directive = re.search(r"^go (\S+)\s*$", text, re.MULTILINE)
    return directive.group(1) if directive else None


def go_pin_problems(mise_toml, mise_lock, go_mod):
    """Return each way the three files break the single Go pin."""
    problems = []
    config = tomllib.loads(mise_toml)
    tools = config.get("tools", {})
    for name in tools:
        if name == "go" or name.endswith(":go") or name.endswith("/go"):
            problems.append(f"mise.toml pins Go as [tools] {name}; go.mod is the only Go pin")
    idiomatic = config.get("settings", {}).get("idiomatic_version_file_enable_tools", [])
    if "go" not in idiomatic:
        problems.append('mise.toml must set [settings] idiomatic_version_file_enable_tools = ["go"]')

    directive = re.search(r"^go (\S+)\s*$", go_mod, re.MULTILINE)
    if not directive:
        problems.append("go.mod has no go directive")
    elif not re.fullmatch(r"1\.\d+\.\d+", directive.group(1)):
        problems.append(f"go.mod's go directive {directive.group(1)} is not a full 1.x.y release")
    release = go_mod_release(go_mod)

    locked = [entry.get("version") for entry in tomllib.loads(mise_lock).get("tools", {}).get("go", [])]
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

    def test_lock_follows_toolchain_line(self):
        go_mod = GO_MOD.replace("go 1.27.4", "go 1.27.0\n\ntoolchain go1.27.4")
        self.assertEqual(go_pin_problems(MISE_TOML, MISE_LOCK, go_mod), [])


if __name__ == "__main__":
    unittest.main()
