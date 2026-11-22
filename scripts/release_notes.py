#!/usr/bin/env python3
"""Print the CHANGELOG.md section for one version, for the GitHub release notes.

    python scripts/release_notes.py 0.2.0

Exits with an error when the changelog has no section for that version, so the release
workflow stops before anything is published.
"""

from __future__ import annotations

import re
import sys
from pathlib import Path

CHANGELOG = Path(__file__).resolve().parent.parent / "CHANGELOG.md"
CHANGELOG_URL = "https://github.com/vinaycodeaxe/hkcli/blob/main/CHANGELOG.md"


def section(version: str, text: str) -> str:
    heading = re.compile(rf"^## \[?{re.escape(version)}\]?(?=\s|$).*$", re.M)
    match = heading.search(text)
    if not match:
        return ""
    rest = text[match.end():]
    following = re.search(r"^## ", rest, re.M)
    return (rest[: following.start()] if following else rest).strip()


def main() -> int:
    if len(sys.argv) != 2:
        print("usage: release_notes.py VERSION", file=sys.stderr)
        return 2
    version = sys.argv[1].removeprefix("v")
    body = section(version, CHANGELOG.read_text(encoding="utf-8"))
    if not body:
        print(f"CHANGELOG.md has no section for {version}; add '## [{version}] - YYYY-MM-DD'", file=sys.stderr)
        return 1
    print(f"{body}\n\nFull changelog: {CHANGELOG_URL}")
    return 0


if __name__ == "__main__":
    sys.exit(main())
