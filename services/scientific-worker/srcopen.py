"""The one way the worker opens a source file (ADR 0050, section 8: the authorise-then-open window).

A path is resolved to its real path and checked against the roots (honest symbolic links, such as a data folder reached
through a link on HPC scratch, keep working because only the resolved path is judged). The file is then opened by
walking that resolved path one component at a time from the root's directory, with O_NOFOLLOW on every component, so a
component swapped for a symbolic link after the check makes the open fail instead of reading somewhere else. The caller
gets an open, read-only file object; nothing reopens the path by name afterwards.

Python has no os.openat2 (RESOLVE_BENEATH | RESOLVE_NO_SYMLINKS would do this in one call), so the walk is explicit.
Every failure is the same refusal as an unauthorised path, so a caller cannot probe for files outside the roots.
"""
from __future__ import annotations

import contextlib
import io
import json
import os
import stat
import sys


class Refused(Exception):
    def __init__(self, code: str, message: str):
        super().__init__(message)
        self.code, self.message = code, message


def _load_roots():
    """Source roots enforced by this process itself (set by the remote launcher). None means the caller authorizes."""
    raw = os.environ.get("VESQIRA_ROOTS")
    if raw is None:
        return None
    try:
        roots = json.loads(raw)
        if not isinstance(roots, list) or not all(isinstance(r, str) and os.path.isabs(r) for r in roots):
            raise ValueError
    except ValueError:
        sys.exit("VESQIRA_ROOTS must be a JSON list of absolute paths")
    return [os.path.realpath(r) for r in roots]


ROOTS = _load_roots()      # canonicalised once, at startup

_U64 = (1 << 64) - 1


class OtherFile(OSError):
    """The file reached is a regular file, but not the one the API recorded (device and inode differ)."""


OTHER_FILE_MESSAGE = ("this name now belongs to a different file than the one that was registered (it was replaced, or its drive "
                      "was mounted again); check it is the file you mean, then register it again")


def parse_expect(raw) -> tuple[int, int] | None:
    """The (device, inode) the API recorded, from a request's `expect`; None when the request has none. The API sends signed 64-bit
    values, so both sides are compared modulo 2**64."""
    if raw is None:
        return None
    if (not isinstance(raw, (list, tuple)) or len(raw) != 2 or any(isinstance(v, bool) or not isinstance(v, int) for v in raw)):
        raise Refused("bad_request", "bad request")
    return raw[0] & _U64, raw[1] & _U64


def inside_roots(real: str) -> bool:
    if ROOTS is None:
        return True
    return any(real == r or real.startswith(r.rstrip(os.sep) + os.sep) for r in ROOTS)


def _base_for(real: str) -> str:
    """The directory the walk starts from: the deepest root holding `real`, or "/" when this process enforces no roots
    (the resolved path has no links, so walking it from "/" without following any is still exact)."""
    if ROOTS is None:
        return os.sep
    best = ""
    for r in ROOTS:
        if (real == r or real.startswith(r.rstrip(os.sep) + os.sep)) and len(r) > len(best):
            best = r
    return best


_DIR = os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW | os.O_CLOEXEC
_FILE = os.O_RDONLY | os.O_NOFOLLOW | os.O_CLOEXEC | os.O_NONBLOCK    # NONBLOCK: a FIFO in place of the file must not hang the open


def open_beneath(real: str, expect: tuple[int, int] | None = None) -> int:
    """A read-only fd for the regular file at the resolved path `real`, reached from its root without following a link.

    Raises OSError when a component is a link or missing, or the target is not a regular file; `expect` is an optional
    (device, inode) the caller saw, and a different file is an error too."""
    base = _base_for(real)
    if not base:
        raise OSError("outside the roots")
    parts = [p for p in real[len(base):].split(os.sep) if p]
    if not parts:
        raise OSError("not a file")
    fd = os.open(base, _DIR)
    try:
        for name in parts[:-1]:
            nxt = os.open(name, _DIR, dir_fd=fd)
            os.close(fd)
            fd = nxt
        # a FIFO or device is turned away before it is opened (opening a device can have effects); the fstat below is the check that counts
        if not stat.S_ISREG(os.stat(parts[-1], dir_fd=fd, follow_symlinks=False).st_mode):
            raise OSError("not a regular file")
        out = os.open(parts[-1], _FILE, dir_fd=fd)
    finally:
        os.close(fd)
    try:
        st = os.fstat(out)
        if not stat.S_ISREG(st.st_mode):
            raise OSError("not the file that was authorised")
        if expect is not None and (st.st_dev & _U64, st.st_ino & _U64) != tuple(v & _U64 for v in expect):
            raise OtherFile("not the file that was recorded")
    except BaseException:
        os.close(out)
        raise
    return out


class _Source(io.BufferedReader):
    # astropy accepts mode="denywrite" (a truly read-only mapping, which RLIMIT_DATA does not count) for a file object
    # only when the object reports that mode; a plain "rb" file would be mapped copy-on-write instead.
    fileobj_mode = "denywrite"


_hook = None     # tests only: called between the resolve and the open, to swap a component deterministically


def open_source(path: str, expect: tuple[int, int] | None = None) -> tuple[str, io.BufferedReader]:
    """(real path, open read-only file object) of the regular file `path`, which must resolve inside the roots."""
    refused = Refused("forbidden", "not available")
    if not isinstance(path, str) or not os.path.isabs(path) or len(path) > 4096 or "\0" in path:
        raise Refused("bad_request", "bad request")
    real = os.path.realpath(path)
    if not inside_roots(real):
        raise refused
    if _hook is not None:
        _hook(real)
    try:
        fd = open_beneath(real, expect)
    except OtherFile:
        raise Refused("changed", OTHER_FILE_MESSAGE) from None
    except OSError:
        raise refused from None
    return real, _Source(io.FileIO(fd, "rb"))


@contextlib.contextmanager
def source_file(path: str, expect: tuple[int, int] | None = None):
    """`with source_file(path) as (real, f):` the file is closed on exit."""
    real, f = open_source(path, expect)
    try:
        yield real, f
    finally:
        f.close()


def stat_source(path: str) -> os.stat_result:
    """The stat of the file as opened by open_source, never of whatever a link swapped in since the check."""
    with source_file(path) as (_, f):
        return os.fstat(f.fileno())


def open_fits(path: str, **kw):
    """astropy's HDUList of the source file, made from the open file object and never from the name. `memmap` and the other
    options pass through; the mode is read-only (denywrite) always. Closing the HDUList closes the file."""
    from astropy.io import fits
    kw.setdefault("memmap", True)
    _, f = open_source(path)
    try:
        return fits.open(f, mode="denywrite", **kw)
    except BaseException:
        f.close()
        raise


@contextlib.contextmanager
def fits_open(path: str, **kw):
    hdul = open_fits(path, **kw)
    try:
        yield hdul
    finally:
        hdul.close()
