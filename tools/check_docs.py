#!/usr/bin/env python3
"""Repository hygiene checks: relative markdown links, license identity, forbidden files."""
import re
import subprocess
import sys
from pathlib import Path

ROOT = Path(__file__).resolve().parent.parent
errors = []

lic = (ROOT / "LICENSE").read_text()
if "GNU AFFERO GENERAL PUBLIC LICENSE" not in lic or "Version 3, 19 November 2007" not in lic:
    errors.append("LICENSE is not the official AGPL v3 text")

link = re.compile(r"\[[^\]]*\]\(([^)\s]+)\)")
for md in ROOT.rglob("*.md"):
    if any(p in md.parts for p in (".git", "node_modules", "dist", "sources")):
        continue
    for target in link.findall(md.read_text(errors="ignore")):
        if re.match(r"^(https?:|mailto:|#)", target):
            continue
        path = (md.parent / target.split("#")[0]).resolve()
        if not path.exists():
            errors.append(f"{md.relative_to(ROOT)}: broken link {target}")

try:
    tracked = subprocess.run(["git", "ls-files"], cwd=ROOT, capture_output=True, text=True, check=True).stdout.split()
except Exception:
    tracked = []
bad = re.compile(r"(\.fits(\.gz)?$|\.fit$|\.tiff?$|\.czi$|\.parquet$|(^|/)\.env($|\.)|\.pem$|\.key$)")
for f in tracked:
    if bad.search(f) and not f.endswith(".env.example"):
        errors.append(f"forbidden tracked file: {f}")

# A tag can be moved after review; a commit cannot. Every third-party action is pinned to a full commit SHA.
use = re.compile(r"^\s*(?:-\s+)?uses:\s*([^\s#]+)", re.M)
for wf in sorted((ROOT / ".github" / "workflows").glob("*.yml")):
    for ref in use.findall(wf.read_text()):
        if not ref.startswith("./") and not re.search(r"@[0-9a-f]{40}$", ref):
            errors.append(f"{wf.relative_to(ROOT)}: {ref} is not pinned to a commit SHA")

# the HTTP contract must match the routes the server registers and the calls the web client makes
contracts = subprocess.run([sys.executable, str(ROOT / "tools" / "contracts.py"), "--check"], capture_output=True, text=True)
if contracts.returncode != 0:
    errors.extend(contracts.stdout.strip().splitlines() or ["tools/contracts.py --check failed"])

# every direct dependency must have an entry in THIRD_PARTY_NOTICES.md
notices = subprocess.run([sys.executable, str(ROOT / "tools" / "check_notices.py")], capture_output=True, text=True)
if notices.returncode != 0:
    errors.extend(notices.stdout.strip().splitlines() or ["tools/check_notices.py failed"])

for e in errors:
    print("FAIL", e)
print("ok" if not errors else f"{len(errors)} problem(s)")
sys.exit(1 if errors else 0)
