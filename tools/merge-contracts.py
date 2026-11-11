#!/usr/bin/env python3
"""Resolve the contract files of a conflicted merge or rebase: three-way JSON merge of packages/contracts/bodies.json,
regeneration of openapi.yaml and the web schema types, and the README status line. Run from the repository root.
Parallel branches each add routes and bodies, so these files conflict on almost every merge; code conflicts are
not touched and must be resolved by hand. Refuses to run if any other file is still conflicted, and never stages
a file that still holds conflict markers."""
import json
import subprocess
import sys

P = "packages/contracts/bodies.json"
CONTRACT = {P, "packages/contracts/openapi.yaml", "apps/web/src/api/schema.d.ts", "packages/contracts/README.md", "packages/contracts/undescribed.txt"}
unmerged = set(subprocess.check_output(["git", "diff", "--name-only", "--diff-filter=U"], text=True).split())
if unmerged - CONTRACT:
    sys.exit("other files are still conflicted (resolve them by hand first): " + " ".join(sorted(unmerged - CONTRACT)))


def show(stage):
    return json.loads(subprocess.check_output(["git", "show", f":{stage}:{P}"]))


def merge(a, t, b, path, conflicts):
    for k, v in t.items():
        if k not in a:
            a[k] = v
        elif isinstance(a[k], dict) and isinstance(v, dict):
            merge(a[k], v, b.get(k, {}) if isinstance(b, dict) else {}, path + "/" + k, conflicts)
        elif a[k] != v:
            base = b.get(k) if isinstance(b, dict) else None
            if a[k] == base:
                a[k] = v
            elif v != base:
                conflicts.append(path + "/" + k)


base, ours, theirs = show(1), show(2), show(3)
conflicts = []
merge(ours, theirs, base, "", conflicts)
open(P, "w").write(json.dumps(ours, indent=1, ensure_ascii=False) + "\n")
for c in conflicts:
    print("CONFLICT (kept ours):", c)
subprocess.check_call([sys.executable, "tools/contracts.py"], stdout=subprocess.DEVNULL)
subprocess.check_call(["npm", "run", "gen:api"], cwd="apps/web", stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
subprocess.check_call(["git", "add", P, "packages/contracts/openapi.yaml", "apps/web/src/api/schema.d.ts", "packages/contracts/undescribed.txt"])
print("contracts merged and regenerated" + (f"; {len(conflicts)} conflicts" if conflicts else ""))

# README status line: keep ours and append sentences only theirs has (not in base).
import re
R = "packages/contracts/README.md"
s = open(R).read()
m = re.search(r"<<<<<<< [^\n]*\n(.*?)\|\|\|\|\|\|\| [^\n]*\n(.*?)=======\n(.*?)>>>>>>> [^\n]*\n", s, flags=re.S)
while m:
    ours, base_t, theirs = m.group(1).strip(), m.group(2).strip(), m.group(3).strip()
    split = lambda x: [y.strip() for y in re.split(r'(?<=\.)\s+(?=[A-Z`(])', x)]
    merged = ours
    for a in [x for x in split(theirs) if x not in split(ours) and x not in split(base_t)]:
        merged = merged.replace("All other operations are routes only.", a + " All other operations are routes only.")
    s = s[:m.start()] + merged + "\n" + s[m.end():]
    m = re.search(r"<<<<<<< [^\n]*\n(.*?)\|\|\|\|\|\|\| [^\n]*\n(.*?)=======\n(.*?)>>>>>>> [^\n]*\n", s, flags=re.S)
open(R, "w").write(s)
if re.search(r"^(<<<<<<<|>>>>>>>)", s, flags=re.M):
    sys.exit("README.md still holds conflict markers; fix it by hand")
subprocess.check_call(["git", "add", R])
print("README merged")
