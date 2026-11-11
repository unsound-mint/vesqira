#!/usr/bin/env python3
"""Fails when services/scientific-worker/DEPLOY and the worker's own imports disagree.

DEPLOY is the one list of worker modules shipped to remote hosts and the RPM. Every module a listed file imports (at any
depth, lazy imports included) that is a sibling .py file must be listed too, and every listed name must be a file.
"""
import ast
import sys
from pathlib import Path

DIR = Path(__file__).resolve().parent.parent / "services" / "scientific-worker"


def manifest(path: Path = DIR / "DEPLOY") -> list[str]:
    return [ln.strip() for ln in path.read_text().splitlines() if ln.strip() and not ln.lstrip().startswith("#")]


def local_imports(path: Path, siblings: set[str]) -> set[str]:
    found = set()
    for node in ast.walk(ast.parse(path.read_text(), str(path))):
        names = [a.name for a in node.names] if isinstance(node, ast.Import) else [node.module or ""] if isinstance(node, ast.ImportFrom) and node.level == 0 else []
        found |= {n.split(".")[0] for n in names} & siblings
    return found


def problems(directory: Path = DIR) -> list[str]:
    listed = manifest(directory / "DEPLOY")
    out = [f"DEPLOY names {n}, which is not a file in {directory.name}/" for n in listed if not (directory / n).is_file()]
    if "plane_worker.py" not in listed:
        out.append("DEPLOY must list plane_worker.py, the entry point started on remote hosts")
    siblings = {p.stem for p in directory.glob("*.py")}
    for n in listed:
        if (directory / n).is_file():
            out += [f"{n} imports {m}, which DEPLOY does not list ({m}.py would be missing on a remote host)"
                    for m in sorted(local_imports(directory / n, siblings - {Path(n).stem})) if f"{m}.py" not in listed]
    return out


if __name__ == "__main__":
    bad = problems()
    print("\n".join(bad) if bad else f"worker manifest ok: {len(manifest())} modules")
    sys.exit(1 if bad else 0)
