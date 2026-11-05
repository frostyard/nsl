#!/usr/bin/env python3
"""Check a rendered nsl Homebrew cask for the rejected zap stanza comment."""

import sys
from pathlib import Path


def problems(text: str) -> list[str]:
    """Return Homebrew cask validation problems found in text."""
    lines = [line.strip() for line in text.splitlines()]
    found = []
    if "# No zap stanza required" in lines:
        found.append("Cask/ZapNotRequired: found '# No zap stanza required'; use 'zap :not_required'")
    if not any(line.startswith("zap ") or line == "zap :not_required" for line in lines):
        found.append("Cask/ZapNotRequired: cask has no zap stanza; use 'zap :not_required'")
    return found


def main() -> int:
    if len(sys.argv) != 2:
        print("usage: check-cask.py PATH", file=sys.stderr)
        return 1

    path = Path(sys.argv[1])
    if not path.is_file():
        print(f"check-cask.py: 0 casks checked, missing file: {path}", file=sys.stderr)
        return 1

    found = problems(path.read_text(encoding="utf-8"))
    if found:
        print(f"check-cask.py: 1 cask checked, problems: {path}", file=sys.stderr)
        for problem in found:
            print(problem, file=sys.stderr)
        return 1
    print(f"check-cask.py: 1 cask checked, no problems: {path}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
