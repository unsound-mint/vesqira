"""One bounded write on a host (ADR 0064): make a mask of a file there, or write a virtual subset of it as a file, without the bytes
crossing to the researcher's computer.

    VESQIRA_ROOTS='[...]' VESQIRA_DERIVED=/root/vesqira_derived python3 -P hostrun.py  < request.json

The request is one JSON object on stdin, never on the command line (a mask's name or a subset's note is free text, and the command
line goes through the account's login shell): {"op": "mask" | "subset", "name": "<file name>", "argv": [analysis.py options]}.

What may be written is fixed before the request is read:
- the folder is VESQIRA_DERIVED, set by the launcher from the host's first source root; it must resolve inside VESQIRA_ROOTS, may
  not be a symbolic link, and is created (one component, with its parent existing) when missing;
- the file name is one plain name of a fixed shape, and must not exist yet (an existing file is never replaced);
- analysis.py writes to a private temporary name in that folder, which is linked to the final name only once the program
  succeeded (link() fails if the name exists, so two runs never clobber each other) and is removed on every failure. A cut
  connection leaves either no file or a complete one, never half of one.
Options that would write anywhere else (--out, --carry-masks, --missing-out, --rel-out, --table-out) are refused. The inputs are read
through srcopen, which enforces VESQIRA_ROOTS on the resolved path like every other read of the host's worker.

The reply is analysis.py's JSON with "file": the resolved path of the file written. Nothing is printed before it.
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

OPS = ("mask", "subset")
NAME = re.compile(r"^(mask|subset)-[A-Za-z0-9_-]{1,100}\.fits$")
WRITES = ("--out", "--carry-masks", "--missing-out", "--rel-out", "--table-out")
MAX_REQUEST = 64 << 10


def refuse(msg: str) -> int:
    print(json.dumps({"ok": False, "error": msg}))
    return 2


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
    if not isinstance(argv, list) or len(argv) > 40 or not all(isinstance(a, str) and len(a) <= 16384 and "\0" not in a for a in argv):
        return refuse("bad request")
    if any(a.split("=", 1)[0] in WRITES for a in argv):
        return refuse("bad request")
    final = os.path.join(folder, name)
    if os.path.lexists(final):
        return refuse("a file of that name is already in the derived folder; it is left as it is")
    tmp = os.path.join(folder, f".part-{secrets.token_hex(8)}-{name}")

    import analysis
    buf = io.StringIO()
    try:
        with contextlib.redirect_stdout(buf):
            code = analysis.main([op, *argv, "--out", tmp])
        out = buf.getvalue().strip().splitlines()
        try:
            res = json.loads(out[-1]) if out else {}
        except ValueError:
            res = {}
        if code != 0 or not isinstance(res, dict) or not res.get("ok"):
            print(json.dumps(res if isinstance(res, dict) and res.get("error") else {"ok": False, "error": "the program failed on the host"}))
            return 2
        try:
            st = os.lstat(tmp)
            if not stat.S_ISREG(st.st_mode):
                raise OSError
            os.link(tmp, final)              # never replaces: fails if the name appeared meanwhile
        except FileExistsError:
            return refuse("a file of that name appeared in the derived folder meanwhile; it is left as it is")
        except OSError:
            return refuse("the file could not be put in place in the derived folder on the host")
        res["file"] = final
        print(json.dumps(res, allow_nan=False))
        return 0
    finally:
        with contextlib.suppress(OSError):
            os.unlink(tmp)


if __name__ == "__main__":
    sys.exit(main())
