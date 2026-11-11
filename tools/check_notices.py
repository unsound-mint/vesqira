#!/usr/bin/env python3
"""Every direct dependency must be named in THIRD_PARTY_NOTICES.md.

Direct dependencies come from the manifests only (transitive ones are covered by the lock files).
A name counts as covered when it appears in backticks in the notices, e.g. `react-dom`, so that a
package mentioned only in prose (or as part of another name such as react-aria-components) does not pass.
"""
import json
import re
import sys
import tomllib
from pathlib import Path

ROOT = Path(__file__).resolve().parent.parent
NOTICES = ROOT / "THIRD_PARTY_NOTICES.md"


def go_direct(path):
    names, in_block = [], False
    for raw in path.read_text().splitlines():
        line = raw.strip()
        if line.startswith("require ("):
            in_block = True
            continue
        if in_block and line == ")":
            in_block = False
            continue
        if in_block:
            entry = line
        elif line.startswith("require "):
            entry = line[len("require "):]
        else:
            continue
        if not entry or entry.startswith("//") or "// indirect" in entry:
            continue
        names.append(entry.split()[0])
    return names


def web_direct(path):
    return list(json.loads(path.read_text()).get("dependencies", {}))


def python_direct(path):
    names = []
    for raw in path.read_text().splitlines():
        line = raw.split("#", 1)[0].strip()
        if not line or line.startswith("-"):
            continue
        match = re.match(r"[A-Za-z0-9][A-Za-z0-9._-]*", line)
        if match:
            names.append(match.group(0))
    return names


def rust_direct(path):
    return list(tomllib.loads(path.read_text()).get("dependencies", {}))


SOURCES = [
    ("services/api/go.mod", go_direct),
    ("apps/web/package.json", web_direct),
    ("services/scientific-worker/requirements.txt", python_direct),
    ("apps/desktop/src-tauri/Cargo.toml", rust_direct),  # optional: the desktop shell may not exist
]


def main():
    if not NOTICES.is_file():
        print("FAIL THIRD_PARTY_NOTICES.md is missing")
        return 1
    covered = set(re.findall(r"`([^`\n]+)`", NOTICES.read_text()))
    missing = []
    for rel, parse in SOURCES:
        path = ROOT / rel
        if not path.is_file():
            continue
        for name in parse(path):
            if name not in covered:
                missing.append((name, rel))
    for name, rel in missing:
        print(f"FAIL missing from THIRD_PARTY_NOTICES.md: {name} (direct dependency in {rel})")
    if missing:
        print(f"{len(missing)} direct dependenc{'y' if len(missing) == 1 else 'ies'} not in THIRD_PARTY_NOTICES.md")
        return 1
    print("notices ok")
    return 0


if __name__ == "__main__":
    sys.exit(main())
