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
    return read_header_at(src, 0)[0]


def read_header_at(f, offset: int) -> tuple[fits.Header, int]:
    """(header, offset just past it) of the header that starts at byte `offset` of the open file `f`, bounded like the primary's."""
    f.seek(offset)
    raw = b""
    for _ in range(MAX_HEADER_BLOCKS):
        block = f.read(BLOCK)
        if len(block) < BLOCK:
            raise ValueError("file ends inside the header")
        raw += block
        if any(block[i : i + 80].rstrip() == b"END" for i in range(0, BLOCK, 80)):
            return fits.Header.fromstring(raw.decode("ascii", "replace")), offset + len(raw)
    raise ValueError("header exceeds the size limit")


def declared(header: fits.Header, key: str):
    """(value, provenance). A missing keyword is unresolved: never 0, never a default."""
    if key in header:
        return clean(header[key]), "declared"
    return None, "unresolved"


VGT_KEYS = re.compile(r"PSIFRAME|PSIANGLE|INB(MAJ|MIN|PA)|VGT[A-Z0-9]{1,5}")
ARC_UNITS = (("arcsec", 1.0), ("arcmin", 60.0), ("deg", 3600.0))   # token in the card comment -> arcseconds per unit


def _comment_unit(comment: str):
    """The angle unit a card's comment names (arcsec, arcmin or deg), or None when it names none or more than one.
    A FITS card has no unit field: the comment is the only place INBMAJ could say what it is in, so it is read, never guessed."""
    low = comment.lower()
    found = {name: f for name, f in ARC_UNITS if name in low}
    if "arcsecond" in low:
        found["arcsec"] = 1.0
    if "degree" in low:
        found["deg"] = 3600.0
    return next(iter(found.items())) if len(found) == 1 else None


def vgt_resolution(h: fits.Header, params: dict) -> dict:
    """What the VGT keywords establish about a map's resolution (ADR 0018 amendment). The map is the input cube's beam, widened by the
    Q/U Gaussian (VGTKFWHM, in input pixels) and by a dn x dn gradient window (a top-hat, not a Gaussian: not included). When every
    input is present and its unit is established the Gaussian part is the input beam and the kernel in quadrature along each axis,
    which is exact for Gaussians; the result is an approximation (declared, never verified) because of the window. Otherwise
    nothing is computed and `reasons` says what is missing: absence is never filled with a default."""
    out = {"status": "not_established", "reasons": [], "approximate": True}
    kfwhm = params.get("VGTKFWHM")
    dn = params.get("VGTDN")
    reasons = out["reasons"]
    if not (isinstance(kfwhm, (int, float)) and not isinstance(kfwhm, bool) and kfwhm > 0):
        reasons.append("VGTKFWHM (the smoothing kernel's FWHM in input pixels) is missing or not positive")
    scale = None
    try:
        from astropy.wcs import WCS
        w = WCS(h, naxis=2)
        if not w.has_celestial:
            raise ValueError("axes 1 and 2 are not a celestial pair")
        m = w.celestial.pixel_scale_matrix * 3600.0
        sx, sy = float((m[:, 0] ** 2).sum() ** 0.5), float((m[:, 1] ** 2).sum() ** 0.5)
        if not (sx > 0 and sy > 0):
            raise ValueError("the pixel scale is zero")
        if abs(sx - sy) > 1e-3 * max(sx, sy):
            reasons.append("the pixels are not square on the sky, so a kernel width in pixels is not one width in arcseconds")
        else:
            scale = (sx + sy) / 2.0
    except Exception:
        reasons.append("the pixel scale in arcseconds is not available (no usable celestial WCS on axes 1 and 2)")
    beam = {}
    unit = None
    if "INBMAJ" not in h:
        reasons.append("INBMAJ (the input cube's beam) is missing")
    else:
        unit = _comment_unit(str(h.comments["INBMAJ"]))
        if unit is None:
            reasons.append("the card INBMAJ does not state its unit (arcsec, arcmin or deg) in its comment, and Vesqira does not guess it")
        for k in ("INBMAJ", "INBMIN", "INBPA"):
            v = params.get(k)
            if isinstance(v, (int, float)) and not isinstance(v, bool):
                beam[k] = v
        if "INBMAJ" not in beam or beam["INBMAJ"] <= 0:
            reasons.append("INBMAJ is not a positive number")
        elif "INBMIN" not in beam or beam["INBMIN"] <= 0:
            reasons.append("INBMIN is missing or not positive")
        elif "INBPA" not in beam and abs(beam["INBMAJ"] - beam["INBMIN"]) > 1e-6 * beam["INBMAJ"]:
            reasons.append("the input beam is elliptical but INBPA is missing")
    if isinstance(dn, (int, float)) and not isinstance(dn, bool) and dn > 0 and scale is not None:
        out["window"] = {"side_pixels": dn, "side_arcsec": dn * scale,
                         "note": "a top-hat window of this side; it is not a Gaussian and is not included in the beam below"}
    if reasons:
        return out
    f = unit[1]
    k = kfwhm * scale
    bmaj, bmin = beam["INBMAJ"] * f, beam["INBMIN"] * f
    out.update({
        "status": "approximate",
        "kernel_fwhm_arcsec": k,
        "input_beam_arcsec": {"bmaj": bmaj, "bmin": bmin, "bpa": beam.get("INBPA", 0.0), "unit_stated_as": unit[0]},
        "gaussian_arcsec": {"bmaj": (bmaj ** 2 + k ** 2) ** 0.5, "bmin": (bmin ** 2 + k ** 2) ** 0.5, "bpa": beam.get("INBPA", 0.0)},
        "note": ("input beam and the smoothing Gaussian combined in quadrature along each axis; the dn window is a top-hat that is not "
                 "included, so this is declared by the file's keywords and approximate, never verified"),
    })
    return out


def vgt_keywords(h: fits.Header):
    """The keywords VGT.jl writes into its products, as the file states them (None when there are none): the angle's frame
    (PSIFRAME) and meaning (PSIANGLE), the product role (VGTPROD), the run parameters (VGT*) and the input cube's beam (INB*).
    Nothing is interpreted here; the API maps them onto Vesqira's declarations. Values are cleaned like every header value."""
    keys = [k for k in h.keys() if VGT_KEYS.fullmatch(k)]
    if not keys:
        return None
    params = {k: clean(h[k]) for k in keys if k not in ("PSIFRAME", "PSIANGLE", "VGTPROD")}
    params = {k: v for k, v in params.items() if v is not None}
    return {
        "psiframe": clean(h["PSIFRAME"]) if "PSIFRAME" in h else None,
        "psiangle": clean(h["PSIANGLE"]) if "PSIANGLE" in h else None,
        "vgtprod": clean(h["VGTPROD"]) if "VGTPROD" in h else None,
        "params": params,
        "resolution": vgt_resolution(h, params),
    }


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
    with source_file(path) as (_, f):
        h, end = read_header_at(f, 0)
        shape = axes_shape(h)
        if not shape and h.get("EXTEND") is True:
            # A HEALPix map (Planck HFI SkyMap) is a binary table after an empty primary HDU: a dataset of its own kind, never an image
            # (ADR 0067). healpix_pol refuses, with a sentence, a HEALPix table that does not state what Vesqira needs to use it.
            import healpix_pol
            try:
                ext, _ = read_header_at(f, end)
            except ValueError:
                ext = None
            if ext is not None and healpix_pol.is_healpix(ext):
                return healpix_pol.describe_header(ext)
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
        "vgt": vgt_keywords(h),
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
