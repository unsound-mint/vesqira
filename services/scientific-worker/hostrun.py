"""One bounded write on a host (ADR 0064): make a mask of a file there, write a virtual subset of it as a file, or compare a family of
maps there (ADR 0070), without the bytes crossing to the researcher's computer.

    VESQIRA_ROOTS='[...]' VESQIRA_DERIVED=/root/vesqira_derived python3 -P hostrun.py  < request.json

The request is one JSON object on stdin, never on the command line (a mask's name or a subset's note is free text, and the command
line goes through the account's login shell): {"op": "mask" | "subset" | "family", "name": "<file name>", "argv": [analysis.py options]}.
A family (name family-....fits, the spread map) also writes one comparison map per non-reference member beside it, <stem>-m<i>.fits, named by
hostrun.py from the program's summary and placed with the spread map, all or none; the reply's "extra_files" lists them.
A subset may also name the mask files its result carries (ADR 0064, masks carried through a subset, ADR 0055): "carry": [{"name":
"<file name>", "path": "<source mask>", "type": "<mask type>"}] and "missing": "<file name>" (the result's own no-value mask, computed
again). They are structured fields, not options: --carry-masks and --missing-out stay out of the allow-list, and hostrun.py builds
them itself from private temporary names.

What may be written is fixed before the request is read:
- the folder is VESQIRA_DERIVED, set by the launcher from the host's first source root; it must resolve inside VESQIRA_ROOTS, may
  not be a symbolic link, and is created (one component, with its parent existing) when missing;
- the file name is one plain name of a fixed shape, and must not exist yet (an existing file is never replaced);
- analysis.py writes to a private temporary name in that folder, which is linked to the final name only once the program
  succeeded (link() fails if the name exists, so two runs never clobber each other) and is removed on every failure. Just before
  the link a line is written to the caller: a connection already gone (the caller timed out, the VPN dropped) fails that write and
  the run stops there with no file. A connection lost in the last instant can leave a complete file nobody recorded, never half of one.
- several outputs (the result and its carried masks) are all-or-nothing: every final name must be new and distinct, every temporary is
  checked, and all are linked together; if any link fails, the ones just linked are removed and nothing stays.
Only the options each operation reads are accepted, by their exact names (an allow-list: argparse would also take an abbreviation such
as --miss for --missing-out); --out is added here. The inputs are read through srcopen, which enforces VESQIRA_ROOTS on the resolved
path like every other read of the host's worker.

The reply is analysis.py's JSON with "file": the resolved path of the file written, on the last line; with carried masks also
"carried_files" (resolved paths, in the order asked) and "missing_file" (or null).
"""
from __future__ import annotations

import contextlib
import io
import json
import os
import re
import secrets
import stat
import sys

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))   # python -P does not add the script dir

OPS = ("mask", "subset", "family")
NAME = re.compile(r"^(mask|subset|family)-[A-Za-z0-9_-]{1,100}\.fits$")
ALLOWED = {"mask": {"--input", "--params", "--corrections", "--declared-unit"},
           "subset": {"--input", "--window", "--note", "--declared-unit", "--corrections"},
           "family": {"--input", "--family", "--reference-index", "--spread", "--corrections"}}
ANALYSIS_OP = {"mask": "mask", "subset": "subset", "family": "family_agreement"}
MAX_FAMILY_MAPS = 12
MASK_TYPES = ("invalid", "low_snr", "saturated", "user_excluded", "other")   # a carried mask is never the no-value mask
MAX_CARRY = 16
MAX_REQUEST = 64 << 10


def refuse(msg: str) -> int:
    print(json.dumps({"ok": False, "error": msg}))
    return 2


def options_ok(op: str, argv: list[str]) -> bool:
    """Every token is an allowed option of op by its exact name, as --name=value or --name followed by a value that is not an option."""
    i = 0
    while i < len(argv):
        tok = argv[i]
        name, eq, _ = tok.partition("=")
        if not tok.startswith("--") or name not in ALLOWED[op]:
            return False
        if not eq:
            if i + 1 >= len(argv) or argv[i + 1].startswith("-"):
                return False
            i += 1
        i += 1
    return True


def mask_name_ok(name: object) -> bool:
    return isinstance(name, str) and bool(NAME.match(name)) and name.startswith("mask-")


def carry_plan(op: str, req: dict, name: str) -> tuple[list[dict], str | None] | None:
    """The carried masks ([{name, path, type}]) and the no-value mask's file name of a request, or None when they are not acceptable.
    Only a subset carries masks; every file name has the mask shape and all names (with the result's) are distinct."""
    carry, missing = req.get("carry", []), req.get("missing")
    if op != "subset":
        return ([], None) if not carry and missing is None else None
    if not isinstance(carry, list) or len(carry) > MAX_CARRY or (missing is not None and not mask_name_ok(missing)):
        return None
    for c in carry:
        if not (isinstance(c, dict) and set(c) == {"name", "path", "type"} and mask_name_ok(c["name"]) and c["type"] in MASK_TYPES
                and isinstance(c["path"], str) and os.path.isabs(c["path"]) and len(c["path"]) <= 16384 and "\0" not in c["path"]):
            return None
    names = [name] + [c["name"] for c in carry] + ([missing] if missing else [])
    if len(set(names)) != len(names):
        return None
    return carry, missing


def _unlink_all(paths: list[str]) -> None:
    for p in paths:
        with contextlib.suppress(OSError):
            os.unlink(p)


def derived_folder(roots: list[str] | None, raw: str | None) -> str:
    """The resolved derived folder, created when missing. Raises ValueError with a sentence when it is not acceptable."""
    if roots is None or not raw or not os.path.isabs(raw) or "\0" in raw:
        raise ValueError("this host's worker was started without its roots or derived folder")
    parent = os.path.realpath(os.path.dirname(raw.rstrip(os.sep)))
    if not any(parent == r or parent.startswith(r.rstrip(os.sep) + os.sep) for r in roots):
        raise ValueError("the derived folder is not inside the host's source roots")
    target = os.path.join(parent, os.path.basename(raw.rstrip(os.sep)))
    try:
        st = os.lstat(target)
    except FileNotFoundError:
        try:
            os.mkdir(target, 0o755)          # the umask applies; one component, under a parent inside the roots
        except FileExistsError:
            pass
        st = os.lstat(target)
    if stat.S_ISLNK(st.st_mode) or not stat.S_ISDIR(st.st_mode):
        raise ValueError("the derived folder on the host is a symbolic link or not a folder; Vesqira does not write through links")
    return target


def main() -> int:
    from srcopen import ROOTS
    try:
        folder = derived_folder(ROOTS, os.environ.get("VESQIRA_DERIVED"))
    except (ValueError, OSError) as e:
        return refuse(str(e) if isinstance(e, ValueError) else "the derived folder on the host could not be made")
    raw = sys.stdin.read(MAX_REQUEST + 1)
    if len(raw) > MAX_REQUEST:
        return refuse("the request is too large")
    try:
        req = json.loads(raw)
    except ValueError:
        return refuse("the request is not JSON")
    if not isinstance(req, dict):
        return refuse("the request is not a JSON object")
    op, name, argv = req.get("op"), req.get("name"), req.get("argv")
    if op not in OPS or not isinstance(name, str) or not NAME.match(name) or not name.startswith(op + "-"):
        return refuse("bad request")
    if op == "family" and not name.endswith(".fits"):
        return refuse("bad request")
    if not isinstance(argv, list) or len(argv) > 40 or not all(isinstance(a, str) and len(a) <= 16384 and "\0" not in a for a in argv):
        return refuse("bad request")
    if not options_ok(op, argv):
        return refuse("bad request")
    plan = carry_plan(op, req, name)
    if plan is None:
        return refuse("bad request")
    carry, missing = plan
    finals = [name] + [c["name"] for c in carry] + ([missing] if missing else [])
    if any(os.path.lexists(os.path.join(folder, n)) for n in finals):
        return refuse("a file of that name is already in the derived folder; it is left as it is")
    tag = secrets.token_hex(8)
    tmps = [os.path.join(folder, f".part-{tag}-{n}") for n in finals]   # private names; the first is the result's own
    extra = []
    if carry:
        extra.append("--carry-masks=" + json.dumps([{"path": c["path"], "type": c["type"], "out": tmps[1 + i]} for i, c in enumerate(carry)]))
    if missing:
        extra.append("--missing-out=" + tmps[-1])

    import analysis
    buf = io.StringIO()
    linked: list[str] = []
    try:
        with contextlib.redirect_stdout(buf):
            code = analysis.main([ANALYSIS_OP[op], *argv, "--out", tmps[0], *extra])
        out = buf.getvalue().strip().splitlines()
        try:
            res = json.loads(out[-1]) if out else {}
        except ValueError:
            res = {}
        if code != 0 or not isinstance(res, dict) or not res.get("ok"):
            print(json.dumps(res if isinstance(res, dict) and res.get("error") else {"ok": False, "error": "the program failed on the host"}))
            return 2
        if op == "family":
            # the comparison maps are written beside the spread map as <stem>-m<i>.fits (analysis.py); they are placed with it, all or none
            idx = res.get("summary", {}).get("comparison_maps") if isinstance(res.get("summary"), dict) else None
            if not isinstance(idx, list) or len(idx) >= MAX_FAMILY_MAPS or len(set(idx)) != len(idx) or not all(isinstance(i, int) and not isinstance(i, bool) and 0 <= i < MAX_FAMILY_MAPS for i in idx):
                print(json.dumps({"ok": False, "error": "the program on the host answered unexpectedly about the comparison maps"}))
                return 2
            stem_tmp, stem_final = tmps[0][:-5], name[:-5]
            for i in idx:
                tmps.append(f"{stem_tmp}-m{i}.fits")
                finals.append(f"{stem_final}-m{i}.fits")
        try:
            print(json.dumps({"stage": "placing"}), flush=True)   # the caller is still there? (raises when the connection is gone)
        except OSError:
            return 2
        try:
            for t in tmps:                   # every output must exist as a plain file before any is put in place
                if not stat.S_ISREG(os.lstat(t).st_mode):
                    raise OSError
            for t, n in zip(tmps, finals):
                os.link(t, os.path.join(folder, n))   # never replaces: fails if the name appeared meanwhile
                linked.append(os.path.join(folder, n))
        except FileExistsError:
            _unlink_all(linked)
            return refuse("a file of that name appeared in the derived folder meanwhile; it is left as it is")
        except OSError:
            _unlink_all(linked)
            return refuse("the file could not be put in place in the derived folder on the host")
        res["file"] = linked[0]
        if op == "family":
            res["extra_files"] = linked[1:]       # the comparison maps, in the order of summary.comparison_maps
        if carry or missing:
            res["carried_files"] = linked[1:1 + len(carry)]
            res["missing_file"] = linked[-1] if missing else None
        print(json.dumps(res, allow_nan=False))
        return 0
    finally:
        _unlink_all(tmps)
        if op == "family":                   # a failed run may have left some comparison maps' temporaries, which are not in tmps yet
            _unlink_all([os.path.join(folder, n) for n in os.listdir(folder) if n.startswith(f".part-{tag}-")])


if __name__ == "__main__":
    sys.exit(main())
