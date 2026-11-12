"""WCS and unit corrections (ADR 0061): a researcher's declared override of specific header keywords, applied in memory.

The file is never written. A correction is applied to a copy of the header the worker read, and only to the keywords listed
in ALLOWED. Before it is accepted, astropy must build the corrected WCS for every axis the image has, and every axis must
keep its kind (celestial longitude, celestial latitude, spectral, other, none) and the image keep its roles (which axis is
x, y and the channel). A correction never adds or removes an axis: NAXIS is not a correctable keyword and an axis number
above NAXIS is refused.

The API sends, per file path, the state in force ({"hash", "bunit", "wcs"}); the worker echoes the hashes it accepted
(`corrections_applied`) so the API can refuse a reply from a worker that does not know this protocol.
"""
from __future__ import annotations

import contextlib
import math
import re
import warnings

from astropy.io import fits
from astropy.wcs import WCS

MAX_KEYS = 24
MAX_STRING = 20
# The keywords a correction may set. i and j are axis numbers (1..NAXIS).
ALLOWED = re.compile(r"^(?:(CRVAL|CRPIX|CDELT|CTYPE|CUNIT)([1-9])|(CD|PC)([1-9])_([1-9])|RESTFRQ|RESTFREQ|SPECSYS|EQUINOX|RADESYS)$")
NUMERIC = re.compile(r"^(?:CRVAL|CRPIX|CDELT|CD|PC)|^(?:RESTFRQ|RESTFREQ|EQUINOX)$")
CTYPE_TEXT = re.compile(r"^[A-Z0-9-]{1,8}$")
CUNIT_TEXT = re.compile(r"^[A-Za-z0-9/*^.()+ -]{1,20}$")
HASH_TEXT = re.compile(r"^[0-9a-f]{16,64}$")
SPECSYS_VALUES = {"TOPOCENT", "GEOCENTR", "BARYCENT", "HELIOCEN", "LSRK", "LSRD", "GALACTOC", "LOCALGRP", "CMBDIPOL", "SOURCE"}
RADESYS_VALUES = {"ICRS", "FK5", "FK4", "FK4-NO-E", "GAPPT"}
SPECTRAL_BASES = {"FREQ", "ENER", "WAVN", "VRAD", "WAVE", "VOPT", "ZOPT", "AWAV", "VELO", "BETA", "FELO"}
MAX_UNIT = 40


class Bad(ValueError):
    """A correction that is malformed or that astropy, or the image's axes, do not accept. The text is shown to the researcher."""


def _number(v) -> bool:
    return isinstance(v, (int, float)) and not isinstance(v, bool) and math.isfinite(v)


def check_unit(v):
    """The corrected brightness unit as text (kept as typed, never parsed), or None."""
    if v is None:
        return None
    if not isinstance(v, str) or not v or len(v) > MAX_UNIT or v != v.strip() or not v.isprintable():
        raise Bad("the corrected unit is 1 to 40 printable characters")
    return v


def check_wcs(wcs, naxis: int) -> dict:
    """Validate the keyword overrides against the allowed list and the image's axis count. Returns a new dict."""
    if wcs is None:
        return {}
    if not isinstance(wcs, dict) or len(wcs) > MAX_KEYS:
        raise Bad(f"a correction names at most {MAX_KEYS} keywords")
    out = {}
    for k, v in wcs.items():
        m = ALLOWED.match(k) if isinstance(k, str) else None
        if m is None:
            raise Bad(f"{str(k)[:16]!r} is not a keyword that can be corrected (CRVAL, CRPIX, CDELT, CD, PC, CTYPE, CUNIT, RESTFRQ, RESTFREQ, SPECSYS, EQUINOX, RADESYS)")
        axes = [int(a) for a in m.groups() if a is not None and a.isdigit()]
        if any(a > naxis for a in axes):
            raise Bad(f"{k} names an axis the image does not have (it has {naxis}); a correction never adds an axis")
        if NUMERIC.match(k):
            if not _number(v):
                raise Bad(f"{k} must be a finite number")
            if k.startswith("CDELT") and v == 0:
                raise Bad(f"{k} cannot be zero")
            if k in ("RESTFRQ", "RESTFREQ") and v <= 0:
                raise Bad(f"{k} must be a positive frequency in Hz")
            if k == "EQUINOX" and not 1800 <= v <= 2200:
                raise Bad("EQUINOX is a year between 1800 and 2200")
            out[k] = float(v)
            continue
        if not isinstance(v, str):
            raise Bad(f"{k} must be text")
        if k.startswith("CTYPE") and not CTYPE_TEXT.match(v):
            raise Bad(f"{k} is up to 8 characters of A-Z, 0-9 and '-' (for example RA---TAN or VRAD)")
        if k.startswith("CUNIT") and not CUNIT_TEXT.match(v):
            raise Bad(f"{k} is a unit of up to {MAX_STRING} plain characters (for example deg, km/s, Hz)")
        if k == "SPECSYS" and v not in SPECSYS_VALUES:
            raise Bad("SPECSYS is one of " + ", ".join(sorted(SPECSYS_VALUES)))
        if k == "RADESYS" and v not in RADESYS_VALUES:
            raise Bad("RADESYS is one of " + ", ".join(sorted(RADESYS_VALUES)))
        out[k] = v
    return out


def check_state(state) -> dict:
    """{"hash", "bunit", "wcs"} as sent by the API, validated (the axis check comes later, against the file)."""
    if not isinstance(state, dict):
        raise Bad("bad corrections")
    h = state.get("hash")
    if not isinstance(h, str) or not HASH_TEXT.match(h):
        raise Bad("bad corrections")
    wcs = state.get("wcs") or {}
    if not isinstance(wcs, dict) or len(wcs) > MAX_KEYS:
        raise Bad(f"a correction names at most {MAX_KEYS} keywords")
    return {"hash": h, "bunit": check_unit(state.get("bunit")), "wcs": dict(wcs)}


def applied_header(file_h: fits.Header, wcs: dict) -> fits.Header:
    """A copy of the file's header with the overrides set. The file's own header object is not changed."""
    h = file_h.copy()
    for k, v in wcs.items():
        h[k] = v
    return h


def axis_kind(h: fits.Header, i: int) -> str:
    """What axis i (1-based) is by its CTYPE: lon, lat, spectral, other, or none (no CTYPE)."""
    t = h.get(f"CTYPE{i}")
    if not isinstance(t, str) or not t.strip():
        return "none"
    t = t.strip().upper()
    base = t[:4]
    if base in ("RA--", "RA") or t.startswith("RA-") or re.match(r"^[A-Z]LON", t) or re.match(r"^[A-Z]{2}LN", t):
        return "lon"
    if base == "DEC-" or t == "DEC" or re.match(r"^[A-Z]LAT", t) or re.match(r"^[A-Z]{2}LT", t):
        return "lat"
    if base.rstrip("-") in SPECTRAL_BASES or base in SPECTRAL_BASES:
        return "spectral"
    return "other"


def matrix_rules(file_h: fits.Header, wcs: dict, naxis: int):
    """A WCS keeps one form of linear transform. wcslib, given CD with PC or CDELT, silently uses one and ignores the other,
    so a correction that would mix them is refused instead of quietly not doing what it says."""
    has_cd = any(f"CD{i}_{j}" in file_h for i in range(1, naxis + 1) for j in range(1, naxis + 1))
    has_pc = any(f"PC{i}_{j}" in file_h for i in range(1, naxis + 1) for j in range(1, naxis + 1))
    sets_cd = any(k.startswith("CD") and not k.startswith("CDELT") for k in wcs)
    sets_pc_or_cdelt = any(k.startswith("PC") or k.startswith("CDELT") for k in wcs)
    if has_cd and sets_pc_or_cdelt:
        raise Bad("this file states its scale and rotation as a CD matrix: correct the CD keywords, not CDELT or PC (wcslib would ignore them)")
    if sets_cd and not has_cd:
        raise Bad("this file states its scale as CDELT" + (" and PC" if has_pc else "") + ": correct those, not CD (a CD matrix would replace them silently)")
    a, b = wcs.get("RESTFRQ", file_h.get("RESTFRQ")), wcs.get("RESTFREQ", file_h.get("RESTFREQ"))
    if _number(a) and _number(b) and float(a) != float(b):
        raise Bad("RESTFRQ and RESTFREQ would give two different rest frequencies: correct both to the same value")


def full_wcs(h: fits.Header, naxis: int) -> WCS:
    """The WCS of every axis, as astropy builds it, or Bad with astropy's reason. Unlike the viewer's reading of a file, there
    is no fallback to the celestial axes alone: a correction must leave a WCS astropy accepts whole."""
    with warnings.catch_warnings():
        warnings.simplefilter("ignore")
        try:
            w = WCS(h, naxis=naxis)
            w.wcs.set()
        except Exception as e:      # wcslib raises many types; its message is short and says what is wrong
            raise Bad("astropy does not accept the corrected WCS: " + (str(e).splitlines() or ["invalid"])[0][:160])
    return w


def validate(file_h: fits.Header, wcs: dict, naxis: int) -> fits.Header:
    """The corrected header, after checking that astropy accepts it and that every axis keeps its kind."""
    wcs = check_wcs(wcs, naxis)
    matrix_rules(file_h, wcs, naxis)
    h = applied_header(file_h, wcs)
    for i in range(1, naxis + 1):
        before, after = axis_kind(file_h, i), axis_kind(h, i)
        if before != after:
            raise Bad(f"axis {i} would change kind ({before} to {after}); a correction keeps the kind of every axis")
    if wcs:
        full_wcs(h, naxis)
    return h


class Context:
    """The corrections of the request being handled, by resolved file path. One request at a time per worker process."""

    def __init__(self):
        self.by_path: dict[str, dict] = {}

    @contextlib.contextmanager
    def using(self, mapping):
        prev = self.by_path
        self.by_path = parse_mapping(mapping)
        try:
            yield self.by_path
        finally:
            self.by_path = prev

    def get(self, path: str):
        return self.by_path.get(path)


def parse_mapping(mapping) -> dict:
    if mapping is None:
        return {}
    if not isinstance(mapping, dict) or len(mapping) > 8:
        raise Bad("bad corrections")
    out = {}
    for p, st in mapping.items():
        if not isinstance(p, str) or len(p) > 4096:
            raise Bad("bad corrections")
        out[p] = check_state(st)
    return out


CURRENT = Context()


def sample_points(c) -> list[dict]:
    """The image centre and its four corners (0-based pixel centres), named."""
    nx, ny = c.shape[c.x], c.shape[c.y]
    return [{"name": "centre", "x": (nx - 1) / 2, "y": (ny - 1) / 2}, {"name": "bottom left", "x": 0.0, "y": 0.0},
            {"name": "bottom right", "x": float(nx - 1), "y": 0.0}, {"name": "top left", "x": 0.0, "y": float(ny - 1)},
            {"name": "top right", "x": float(nx - 1), "y": float(ny - 1)}]


def coords(c) -> dict:
    """World coordinates of the sample points (sky) and of the first, middle and last channel (spectral), by astropy."""
    out = {"sky": c.sky_facts(), "points": [], "spectral": None}
    for p in sample_points(c):
        q = {"name": p["name"], "x": p["x"], "y": p["y"], "lon": None, "lat": None}
        if c.celestial and c.wcs is not None:
            pix = [0.0] * len(c.shape)
            pix[c.x], pix[c.y] = p["x"], p["y"]
            with warnings.catch_warnings():
                warnings.simplefilter("ignore")
                w = c.wcs.all_pix2world([pix], 0)[0]
            lon, lat = float(w[c.lng]), float(w[c.lat])
            if math.isfinite(lon) and math.isfinite(lat):
                q["lon"], q["lat"] = lon, lat
        out["points"].append(q)
    facts = c.spectral_facts()
    if facts is not None:
        sp = {"facts": facts, "channels": []}
        if facts.get("resolved"):
            for ch in sorted({0, (c.channels - 1) // 2, c.channels - 1}):
                v = c.spectral_value(ch)
                sp["channels"].append({"channel": ch, "value": None if v is None or not math.isfinite(float(v)) else float(v)})
        out["spectral"] = sp
    return out


def changes(file_h: fits.Header, before: dict, after: dict) -> list[dict]:
    """Keyword by keyword: what the file says, what is in force now, what is proposed."""
    from fits_header import clean
    out = []
    for k in sorted(set(before) | set(after)):
        out.append({"keyword": k, "file": clean(file_h.get(k)) if k in file_h else None, "in_file": k in file_h,
                    "before": before.get(k), "after": after.get(k)})
    return out
