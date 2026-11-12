#!/usr/bin/env python3
"""Read a FITS primary header and report what it *declares*. Never reads the data unit.

Usage: fits_header.py <path>    Prints one JSON object on stdout. Exit 0 on success, 2 on a controlled failure
(the JSON then has "error"). Diagnostics are never echoed from file content beyond short, sanitised fields.
"""
from __future__ import annotations

import json
import os
import re
import sys

from astropy.io import fits

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))   # python -P does not add the script dir
from memcap import limit_memory, over_cap_message
from srcopen import Refused, source_file

BLOCK = 2880
MAX_HEADER_BLOCKS = 64            # 184 kB of header: far beyond any real primary header
MAX_AXES = 8
MAX_ELEMENTS = 1 << 40            # refuse absurd NAXIS products before anything allocates
SAFE = re.compile(r"[^\x20-\x7e]")


def clean(v, limit=80):
    """Header values are untrusted text: strip control characters and bound the length."""
    if isinstance(v, str):
        return SAFE.sub("?", v)[:limit]
    if isinstance(v, bool) or v is None:
        return v
    if isinstance(v, (int, float)):
        return v if v == v and abs(v) != float("inf") else None
    return None


def read_header(src) -> fits.Header:
    """The primary header of `src`: an open binary file (already authorised and opened without following links), or a path,
    which is opened that way here."""
    if isinstance(src, str):
        with source_file(src) as (_, f):
            return read_header(f)
    f = src
    f.seek(0)
    raw = b""
    for _ in range(MAX_HEADER_BLOCKS):
        block = f.read(BLOCK)
        if len(block) < BLOCK:
            raise ValueError("file ends inside the header")
        raw += block
        if any(block[i : i + 80].rstrip() == b"END" for i in range(0, BLOCK, 80)):
            return fits.Header.fromstring(raw.decode("ascii", "replace"))
    raise ValueError("header exceeds the size limit")


def declared(header: fits.Header, key: str):
    """(value, provenance). A missing keyword is unresolved: never 0, never a default."""
    if key in header:
        return clean(header[key]), "declared"
    return None, "unresolved"


def axes_shape(h: fits.Header) -> list[int]:
    """Validated NAXISn list in native FITS order; rejects absurd sizes before anything allocates."""
    if h.get("SIMPLE") is not True:
        raise ValueError("not a FITS primary header")
    naxis = int(h.get("NAXIS", 0))
    if not 0 <= naxis <= MAX_AXES:
        raise ValueError(f"NAXIS {naxis} outside the supported range 0-{MAX_AXES}")
    shape, elements = [], 1
    for i in range(1, naxis + 1):
        n = int(h.get(f"NAXIS{i}", -1))
        if n < 0:
            raise ValueError(f"NAXIS{i} missing or negative")
        shape.append(n)
        elements *= max(n, 1)
        if elements > MAX_ELEMENTS:
            raise ValueError("data size exceeds the supported limit")
    return shape


def describe(path: str) -> dict:
    h = read_header(path)
    shape = axes_shape(h)
    naxis = len(shape)
    axes = []
    for i in range(1, naxis + 1):   # native FITS order: axis 1 is the fastest-varying
        ax = {"axis": i, "length": shape[i - 1]}
        for field, key in (("ctype", "CTYPE"), ("cunit", "CUNIT"), ("crval", "CRVAL"), ("crpix", "CRPIX"), ("cdelt", "CDELT")):
            ax[field], ax[f"{field}_status"] = declared(h, f"{key}{i}")
        axes.append(ax)
    bunit, bunit_status = declared(h, "BUNIT")
    # linear-transform and frame keywords that decide whether two grids are really the same grid
    extra = {}
    for key in h.keys():
        if re.fullmatch(r"(PC|CD)\d{1,2}_\d{1,2}|CROTA\d|LONPOLE|LATPOLE|EQUINOX", key):
            v = clean(h[key])
            if isinstance(v, (int, float)) and not isinstance(v, bool):
                extra[key] = v
    beam = {k.lower(): clean(h[k]) for k in ("BMAJ", "BMIN", "BPA") if k in h and isinstance(clean(h[k]), (int, float))}
    return {
        "beam": {**beam, "status": "declared" if "bmaj" in beam and "bmin" in beam else "unresolved",
                 "note": "FITS gives BMAJ, BMIN and BPA in degrees; the beam is the resolution and is not the pixel scale"},
        "wcs_extra": extra,
        "ok": True,
        "naxis": naxis,
        "bitpix": clean(h.get("BITPIX")),
        "axes": axes,
        "bunit": bunit,
        "bunit_status": bunit_status,
        "specsys": declared(h, "SPECSYS")[0],
        "radesys": declared(h, "RADESYS")[0],
        "provenance_note": "values are declared by the file header; nothing here is verified",
    }


def main(argv: list[str]) -> int:
    if len(argv) != 2:
        print(json.dumps({"ok": False, "error": "usage: fits_header.py <path>"}))
        return 2
    limit_memory()  # its own process, like plane_worker and analysis: a crafted header must not take the machine down
    try:
        print(json.dumps(describe(argv[1])))
        return 0
    except MemoryError:
        print(json.dumps({"ok": False, "error": over_cap_message()}))
        return 2
    except (OSError, ValueError, KeyError, TypeError, Refused) as e:
        print(json.dumps({"ok": False, "error": clean(str(e), 120)}))
        return 2


if __name__ == "__main__":
    sys.exit(main(sys.argv))
