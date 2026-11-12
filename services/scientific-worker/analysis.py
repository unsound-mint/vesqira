#!/usr/bin/env python3
"""Cube analysis that produces new, fully described files: statistics, moment maps, cutouts, spectral slabs and
alignment of one map or cube onto another's celestial grid.

Usage: analysis.py <op> --input FILE [--reference FILE] [options] --out OUT.fits
Prints one JSON object on stdout; exit 0 on success, 2 on a controlled failure.

Rules this program keeps:
- It never modifies an input and writes the output atomically (temporary file, then rename).
- Missing data (NaN, BLANK, outside the footprint) stays missing: a sum of nothing is NaN, never 0.
- Spectral values come from the file's own WCS (astropy). A spectral axis that is not fully declared is a refusal,
  never a guess. Velocities are not invented for frequency axes.
- Reprojection is not resolution matching: it changes pixel values by interpolation and says so in the output.
- Large cubes are processed one channel at a time, so memory stays bounded.
- A map declared as an axial orientation (--axial-unit) is never averaged as a scalar: align and convolve resample the doubled
  angle (Q = w cos 2θ, U = w sin 2θ) and moment and slab are refused (ADR 0042).
"""
from __future__ import annotations

import argparse
import json
import math
import os
import re
import sys
import tempfile
import warnings

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))

import numpy as np
from astropy.io import fits

import corrections as corr
from plane_worker import Cube, Refused, authorize, declared_unit as checked_declared_unit, fits_array
import orientation as ori
from memcap import limit_memory, over_cap_message

MAX_OUTPUT_ELEMENTS = 1 << 28


def fail(msg: str):
    raise Refused("failed", msg)


def open_cube(path: str) -> Cube:
    """The file as a Cube, with the researcher's corrections for it when the API sent any (--corrections, ADR 0061)."""
    real = authorize(path)
    return Cube(real, corr.CURRENT.get(real))


OWNED_UNIT = ("user", "corrected")     # a unit that is the researcher's, not the input file's: declared for a gap, or corrected


def planes(c: Cube, c0: int, c1: int, ys=slice(None), xs=slice(None)):
    """Yield (channel index, float64 plane with NaN for missing) for channels c0..c1 inclusive."""
    with fits.open(c.path, memmap=True, mode="denywrite", do_not_scale_image_data=True) as hdul:
        hdu = hdul[0]
        for ch in range(c0, c1 + 1):
            yield ch, c._physical(np.array(fits_array(hdul)[c._index(ch, ys, xs)]))


def spectral_axis(c: Cube):
    """(values in the axis' own unit, unit, name). Refuses unless the axis is fully declared."""
    facts = c.spectral_facts()
    if c.chan is None or facts is None or not facts["resolved"]:
        fail("the spectral axis is not fully declared in the header, so no spectral values can be computed")
    pix = np.zeros((c.channels, len(c.shape)))
    pix[:, c.chan] = np.arange(c.channels)
    with warnings.catch_warnings():
        warnings.simplefilter("ignore")
        vals = np.asarray(c.wcs.all_pix2world(pix, 0)[:, c.spec], dtype=float)
    return vals, facts["unit"], facts["name"]


def chan_range(c: Cube, args) -> tuple[int, int]:
    c0 = 0 if args.chan_start is None else args.chan_start
    c1 = c.channels - 1 if args.chan_end is None else args.chan_end
    if not (0 <= c0 <= c1 < c.channels):
        fail("the channel range is outside the cube")
    return c0, c1


def image_header(c: Cube, extra: dict) -> fits.Header:
    """A 2D header carrying the cube's spatial WCS (and nothing spectral)."""
    h = fits.Header()
    h["SIMPLE"], h["BITPIX"], h["NAXIS"] = True, -64, 2
    h["NAXIS1"], h["NAXIS2"] = c.shape[c.x], c.shape[c.y]
    for src, dst in ((c.x, 1), (c.y, 2)):
        for key in ("CTYPE", "CRVAL", "CRPIX", "CDELT", "CUNIT"):
            if f"{key}{src + 1}" in c.h:
                h[f"{key}{dst}"] = c.h[f"{key}{src + 1}"]
    for a, da in ((c.x, 1), (c.y, 2)):                  # PC and CD matrices, mapped onto the two image axes
        for b, db in ((c.x, 1), (c.y, 2)):
            for prefix in ("PC", "CD"):
                if f"{prefix}{a + 1}_{b + 1}" in c.h:
                    h[f"{prefix}{da}_{db}"] = c.h[f"{prefix}{a + 1}_{b + 1}"]
    for key in ("RADESYS", "EQUINOX", "LONPOLE", "LATPOLE"):
        if key in c.h:
            h[key] = c.h[key]
    for key, v in extra.items():
        h[key] = v
    return h


def unit_of(c: Cube, declared: str | None) -> tuple[str | None, str]:
    """The brightness unit of a file and where it comes from: ("file"), a unit its author declared for a file without one
    ("user"), or none ("unresolved"). What the file says always wins; a declared unit only fills a gap. It is text, carried
    as typed: nothing here parses or converts it. A unit the researcher corrected (ADR 0061) is "corrected"."""
    if c.unit_override is not None:
        if not c.unit_override.isascii():
            fail("the corrected brightness unit has characters a FITS header cannot hold (such as µ or °); "
                 "correct it in plain ASCII (for example uJy/beam), which a result file can carry")
        return c.unit_override, "corrected"
    if "BUNIT" in c.h:
        v = c.h.get("BUNIT")
        text = v.strip() if isinstance(v, str) else ""
        if not text:
            return None, "unresolved"                 # a blank card says nothing; it is not a unit, and a declaration does not replace it
        # A file made by an earlier analysis of a declared unit keeps saying whose the unit is, however many steps follow.
        return text, c.h.get("VESQBUNS") if c.h.get("VESQBUNS") in OWNED_UNIT else "file"
    if declared is not None:
        text = checked_declared_unit({"declared_bunit": declared})
        if not text.isascii():
            # a FITS header holds ASCII only: say so, instead of failing as if the input file were unreadable
            fail("the declared brightness unit has characters a FITS header cannot hold (such as µ or °); "
                 "declare it in plain ASCII (for example uJy/beam), which a result file can carry")
        return text, "user"
    return None, "unresolved"


def unit_history(unit: str, source: str, what: str = "the input") -> str:
    if source == "corrected":
        return f"Vesqira: brightness unit {unit} is the author's correction for {what}; that file states another BUNIT"
    return f"Vesqira: brightness unit {unit} was declared by the author for {what}; the file has no BUNIT"


def stamp_user_unit(h: fits.Header, unit: str, what: str = "the input", source: str = "user"):
    """Give a result the unit its author declared (or corrected) for the input, marked as theirs (VESQBUNS) so it is never
    mistaken for a unit the input file stated."""
    if h.get("VESQBUNS") == source and h.get("BUNIT") == unit:
        return                                         # already marked by an earlier step, with its history
    h["BUNIT"] = unit
    h["VESQBUNS"] = source
    h["HISTORY"] = unit_history(unit, source, what)


def note_unit(summary: dict, source: str, unit: str | None, what: str = "the input"):
    """Say in the result's summary (which travels with figures and reviews) whose unit it is."""
    summary["unit_source"] = source
    if source == "corrected":
        prior = summary.get("policy", "").rstrip()
        summary["policy"] = (prior + ("" if prior.endswith(".") or not prior else ".") + f" The brightness unit ({unit}) is the author's correction for {what}; "
                             "the file states another BUNIT. It is carried as typed and was not checked against the data.").strip()
    if source == "user":
        prior = summary.get("policy", "").rstrip()
        summary["policy"] = (prior + ("" if prior.endswith(".") or not prior else ".") + f" The brightness unit ({unit}) was declared by the author for {what}; the file it comes from has no BUNIT. "
                             "It is carried as typed and was not checked against the data.").strip()


def stamp_corrections(header: fits.Header):
    """A result made from a file with corrected WCS keywords (ADR 0061) carries the corrected values as its own header: say so,
    so the result is never mistaken for one made from the file's own WCS."""
    keys = sorted({k for st in corr.CURRENT.by_path.values() for k in (st.get("wcs") or {})})
    if keys:
        header["VESQWCSC"] = ("corrected", "WCS of the input corrected by the author")
        header["HISTORY"] = "Vesqira: made with the author's WCS correction of the input, not the file's own WCS:"
        for i in range(0, len(keys), 6):
            header["HISTORY"] = "  corrected keywords: " + ", ".join(keys[i:i + 6])


def write_fits(path: str, data: np.ndarray, header: fits.Header):
    if data.size > MAX_OUTPUT_ELEMENTS:
        fail("the result is too large")
    stamp_corrections(header)
    d = os.path.dirname(os.path.abspath(path))
    os.makedirs(d, exist_ok=True)
    fd, tmp = tempfile.mkstemp(dir=d, suffix=".tmp")
    os.close(fd)
    try:
        fits.PrimaryHDU(data, header=header).writeto(tmp, overwrite=True)
        os.replace(tmp, path)
    finally:
        if os.path.exists(tmp):
            os.unlink(tmp)



# ---------------------------------------------------------------------------------------------------------------
# Axial orientation maps (ADR 0042). The API passes what the person declared; nothing here infers it.

def refuse_axial(args, op: str):
    if args.axial_unit:
        fail(f"{op} would average the angles of a map declared as an axial orientation map, which is not a valid operation "
             "(0 and 180 degrees are the same orientation). Use align or convolve, which resample the doubled angle")


def axial_inputs(args, c: Cube) -> tuple[np.ndarray, np.ndarray]:
    """(angle in degrees, weight) of a declared orientation map: both float64 planes with NaN for no value. The weight is the
    declared reliability map's values, or 1 when none is declared; it must not be negative (zero is a value: no weight)."""
    if c.channels != 1:
        fail("an orientation map is a single plane")
    _, p = next(planes(c, 0, 0))
    theta = p * (1.0 if args.axial_unit == "deg" else 180.0 / math.pi)
    if not args.weights:
        return theta, np.ones_like(theta)
    wc = open_cube(args.weights)
    if wc.channels != 1 or (wc.shape[wc.y], wc.shape[wc.x]) != (c.shape[c.y], c.shape[c.x]):
        fail("the reliability map does not have the shape of the orientation map")
    _, w = next(planes(wc, 0, 0))
    if np.any(w[np.isfinite(w)] < 0):
        fail("the reliability map has negative values, which cannot be weights")
    return theta, w


DOUBLED_ANGLE = ("Doubled-angle method: the angle θ was resampled as Q = w cos 2θ and U = w sin 2θ (w the declared reliability, or 1), "
                 "each by the same kernel as a scalar map, and the result is θ = 1/2 atan2(U, Q) in [0, 180) degrees; 1 and 179 degrees "
                 "average to 0, not 90. The companion reliability map is the resultant length |(Q, U)| / w in [0, 1]. Where the "
                 "contributing orientations cancel (resultant below 1e-6) the orientation is undefined and is NaN, counted in the summary.")


def axial_output(args, theta_deg: np.ndarray, rel: np.ndarray | None):
    """The angle in the input's unit (deg or rad), in [0, 180) degrees or [0, π) radians, as float32."""
    out = theta_deg if args.axial_unit == "deg" else np.radians(theta_deg)
    return out.astype("<f4"), (None if rel is None else rel.astype("<f4"))


def write_axial(args, theta_deg, rel, header, summary: dict, history: list[str]):
    """Write the angle map to --out and, when there is one, its reliability map to --rel-out."""
    ang, relf = axial_output(args, theta_deg, rel)
    h = header.copy()
    for line in history:
        h["HISTORY"] = line
    write_fits(args.out, ang, h)
    summary["reliability_file"] = False
    if relf is not None and args.rel_out:
        hr = header.copy()
        for k in ("BUNIT", "VESQBUNS"):
            if k in hr:
                del hr[k]
        hr["VESQOP"] = h.get("VESQOP", "")
        hr["HISTORY"] = "Vesqira: companion reliability map of an orientation result; the resultant length of the resampled doubled angles, 0 to 1"
        write_fits(args.rel_out, relf, hr)
        summary["reliability_file"] = True


# ---------------------------------------------------------------------------------------------------------------

def mask_policy(args, c: Cube, allow_weight: bool = False, which: str = "masks"):
    """The masks an operation was told to apply (the --masks option), opened for reading (close them with ap.__exit__), or None. Each
    has its own policy."""
    import masks as mk
    try:
        specs = mk.parse_specs(getattr(args, which, None))
        return mk.Applied(c, specs, allow_weight).__enter__() if specs else None
    except mk.MaskError as e:
        fail(str(e))


def op_stats(args) -> dict:
    c = open_cube(args.input)
    c0, c1 = chan_range(c, args)
    ap = mask_policy(args, c)
    try:
        return _stats(args, c, c0, c1, ap)
    finally:
        if ap is not None:
            ap.__exit__(None, None, None)


def _stats(args, c: Cube, c0: int, c1: int, ap) -> dict:
    n = nan = 0
    mean = m2 = 0.0
    lo, hi = math.inf, -math.inf
    peak = None
    per = []
    for ch, p in planes(c, c0, c1):
        nan += int(p.size - np.count_nonzero(np.isfinite(p)))        # no value in the file: counted before any mask, and apart from them
        if ap is not None:
            ex, _ = ap.plane(ch, p)
            p = np.where(ex, np.nan, p)                              # a masked pixel is left out; it is not turned into zero
        fin = p[np.isfinite(p)]
        row = {"channel": ch, "finite": int(fin.size), "rms": None, "mad_sigma": None, "min": None, "max": None, "mean": None}
        if fin.size:
            row.update(rms=float(fin.std()), mad_sigma=float(1.4826 * np.median(np.abs(fin - np.median(fin)))),
                       min=float(fin.min()), max=float(fin.max()), mean=float(fin.mean()))
            lo, hi = min(lo, row["min"]), max(hi, row["max"])
            # parallel (Chan) update of the running mean and sum of squares
            k = fin.size
            delta = row["mean"] - mean
            tot = n + k
            mean += delta * k / tot
            m2 += float(((fin - row["mean"]) ** 2).sum()) + delta * delta * n * k / tot
            n = tot
            if peak is None or row["max"] > peak["value"]:
                idx = np.unravel_index(int(np.nanargmax(p)), p.shape)
                peak = {"value": row["max"], "channel": ch, "x": int(idx[1]), "y": int(idx[0])}
        per.append(row)
    out = {"channels": [c0, c1], "finite": n, "no_value": nan, "min": None if n == 0 else lo, "max": None if n == 0 else hi,
           "mean": None if n == 0 else mean, "std": None if n < 2 else math.sqrt(m2 / n), "peak": peak, "per_channel": per,
           "policy": "NaN, BLANK and infinite values are excluded and counted; nothing is treated as zero"}
    if ap is not None:
        import masks as mk
        out["masks"] = ap.record()
        out["excluded_by_mask"] = ap.unique_excluded
        out["policy"] += ". " + mk.masks_policy_sentence(ap.specs)
    if peak is not None and c.celestial:
        w = c.coord(peak["x"], peak["y"], peak["channel"])
        out["peak"]["sky"] = w["sky"]
    return {"ok": True, "op": "stats", "summary": out}


def op_moment(args) -> dict:
    refuse_axial(args, "A moment")
    c = open_cube(args.input)
    order = args.order
    if order not in (0, 1, 2):
        fail("the moment order must be 0, 1 or 2")
    c0, c1 = chan_range(c, args)
    vals, unit, name = spectral_axis(c)
    width = np.abs(np.gradient(vals)) if c.channels > 1 else None
    if width is None:
        fail("a moment needs more than one channel")
    ny, nx = c.shape[c.y], c.shape[c.x]
    s0 = np.zeros((ny, nx))
    s1 = np.zeros((ny, nx))
    s2 = np.zeros((ny, nx))
    count = np.zeros((ny, nx), dtype=np.int64)
    thr = args.threshold
    ap = mask_policy(args, c)
    for ch, p in planes(c, c0, c1):
        ok = np.isfinite(p)
        if ap is not None:
            ok &= ~ap.plane(ch, p)[0]            # a masked pixel is not selected, exactly like one below the threshold; it is not zero
        if thr is not None:
            ok &= p >= thr                       # below the threshold counts as not selected, not as zero
        w = np.where(ok, p, 0.0) * width[ch]
        s0 += w
        s1 += w * vals[ch]
        count += ok
    none = count == 0
    bu, bu_src = unit_of(c, args.declared_unit)
    if order == 0:
        out = np.where(none, np.nan, s0)
        bunit = f"{bu} {unit}".strip() if bu else f"(brightness unit unresolved) {unit}"
    else:
        with np.errstate(all="ignore"):
            m1 = np.where((s0 > 0) & ~none, s1 / np.where(s0 > 0, s0, 1.0), np.nan)
        if order == 1:
            out, bunit = m1, unit
        else:
            for ch, p in planes(c, c0, c1):
                ok = np.isfinite(p)
                if ap is not None:
                    ok &= ~ap.plane_quiet(ch, p)[0]
                if thr is not None:
                    ok &= p >= thr
                w = np.where(ok, p, 0.0) * width[ch]
                s2 += w * (vals[ch] - np.where(np.isfinite(m1), m1, 0.0)) ** 2
            with np.errstate(all="ignore"):
                out = np.where(np.isfinite(m1), np.sqrt(s2 / np.where(s0 > 0, s0, 1.0)), np.nan)
            bunit = unit
    h = image_header(c, {"BUNIT": bunit, "VESQOP": f"moment{order}"})
    h["HISTORY"] = f"Vesqira moment {order} over channels {c0}-{c1}" + (f", values >= {thr}" if thr is not None else "")
    summary = {"order": order, "channels": [c0, c1], "threshold": thr, "unit": bunit,
               "axis": name, "pixels_without_data": int(np.isnan(out).sum()),
               "policy": "NaN, BLANK and values below the threshold are excluded; a pixel with no selected value is NaN, never 0; moments 1 and 2 need a positive total"}
    if order == 0:                                    # moments 1 and 2 are velocities: the brightness unit is not in them
        if bu_src in OWNED_UNIT:
            h["VESQBUNS"] = bu_src                              # the BUNIT written above is that brightness unit times the spectral one
            h["HISTORY"] = unit_history(bu, bu_src)
        note_unit(summary, bu_src, bu)
    if ap is not None:
        import masks as mk
        summary["masks"] = ap.record()
        summary["policy"] += ". " + mk.masks_policy_sentence(ap.specs)
        h["HISTORY"] = ("Vesqira: masks applied: " + "; ".join(f"{s['type']} {s['policy']}" for s in ap.specs))[:68]
        ap.__exit__(None, None, None)
    write_fits(args.out, out.astype("<f4"), h)
    return {"ok": True, "op": "moment", "summary": summary}


def shifted_header(c: Cube, drop: set[int], shifts: dict[int, float], shape_new: dict[int, int]) -> fits.Header:
    h = c.h.copy()
    for ax, new in shape_new.items():
        h[f"NAXIS{ax + 1}"] = new
    for ax, d in shifts.items():
        k = f"CRPIX{ax + 1}"
        if k in h:
            h[k] = float(h[k]) - d                # a window starting at pixel d moves the reference pixel by d
    return h


def op_cutout(args) -> dict:
    c = open_cube(args.input)
    nx, ny = c.shape[c.x], c.shape[c.y]
    x0, x1, y0, y1 = args.x0, args.x1, args.y0, args.y1
    if None in (x0, x1, y0, y1) or not (0 <= x0 <= x1 < nx and 0 <= y0 <= y1 < ny):
        fail("the cutout box is outside the image")
    if (x1 - x0 + 1) * (y1 - y0 + 1) * c.channels > MAX_OUTPUT_ELEMENTS:
        fail("the cutout is too large")
    out = np.full((c.channels, y1 - y0 + 1, x1 - x0 + 1), np.nan, dtype="<f4")
    for ch, p in planes(c, 0, c.channels - 1, slice(y0, y1 + 1), slice(x0, x1 + 1)):
        out[ch] = p
    h = shifted_header(c, set(), {c.x: x0, c.y: y0}, {c.x: x1 - x0 + 1, c.y: y1 - y0 + 1})
    return _write_cube(c, out, h, "cutout", {"x": [x0, x1], "y": [y0, y1]}, args)


def op_slab(args) -> dict:
    refuse_axial(args, "A slab")
    c = open_cube(args.input)
    c0, c1 = chan_range(c, args)
    out = np.stack([p.astype("<f4") for _, p in planes(c, c0, c1)])
    h = shifted_header(c, set(), {c.chan: c0}, {c.chan: c1 - c0 + 1})
    return _write_cube(c, out, h, "slab", {"channels": [c0, c1]}, args)


def _write_cube(c: Cube, data: np.ndarray, h: fits.Header, op: str, summary: dict, args) -> dict:
    # the data array is (channel, y, x); put the axes back in the file's own order
    n = len(c.shape)
    if n == 3 and (c.chan, c.y, c.x) == (2, 1, 0):
        arr = data
    else:
        fail("cutouts and slabs support cubes with axes (x, y, channel) in that order")
    h["BITPIX"] = -32
    for k in ("BSCALE", "BZERO", "BLANK"):
        if k in h:
            del h[k]
    h["VESQOP"] = op
    h["HISTORY"] = f"Vesqira {op} {json.dumps(summary)}"
    bu, bu_src = unit_of(c, args.declared_unit)
    if bu_src in OWNED_UNIT:
        stamp_user_unit(h, bu, source=bu_src)
    write_fits(args.out, arr, h)
    out = {**summary, "shape": list(arr.shape),
           "policy": "values are copied unchanged; the WCS reference pixel is shifted so coordinates stay exact"}
    note_unit(out, bu_src, bu)
    return {"ok": True, "op": op, "summary": out}


def aligned_header(src: Cube, w_ref, shape_out, cube: bool) -> fits.Header:
    """The header of an aligned result: the reference's celestial grid, plus the input's spectral axis for a cube."""
    h = fits.Header()
    h["SIMPLE"], h["BITPIX"] = True, -32
    h["NAXIS"] = 3 if cube else 2
    hdr_ref = w_ref.to_header()
    h["NAXIS1"], h["NAXIS2"] = shape_out[1], shape_out[0]
    if cube:
        h["NAXIS3"] = src.channels
    for k in hdr_ref:
        h[k] = hdr_ref[k]
    if cube:
        for key in ("CTYPE", "CRVAL", "CRPIX", "CDELT", "CUNIT"):
            if f"{key}{src.chan + 1}" in src.h:
                h[f"{key}3"] = src.h[f"{key}{src.chan + 1}"]
        for key in ("SPECSYS", "RESTFRQ", "RESTFREQ"):
            if key in src.h:
                h[key] = src.h[key]
    return h


def align_axial(args, src: Cube, ref: Cube, func, kw: dict, method: str, shape_out) -> dict:
    """Align an orientation map declared axial onto the reference's celestial grid without ever averaging angles linearly.

    The physical orientation must survive the change of grid, so the angle is carried through the sky:
    1. a map in the pixel frame is turned into position angles east of north in the input's own celestial frame (astropy,
       from the WCS at each pixel); a map already in the sky frame is used as it is;
    2. bilinear and exact methods resample Q = w cos 2θ and U = w sin 2θ and the weight w independently with the scalar
       path's own kernel, and θ = 1/2 atan2(U, Q); nearest picks one sample and averages nothing, so it resamples θ itself;
    3. when the reference is in another celestial frame the position angle is rotated into it (astropy), and
    4. for a pixel-frame map the angle is turned back into the reference grid's own pixel frame.
    The frame must be declared (a refusal names it); the quantity is not needed, because the same quantity goes in and out."""
    if src.channels != 1:
        fail("an orientation map is a single plane")
    if args.axial_frame not in ori.FRAMES:
        fail("the angle's frame (pixel or sky) is not declared for this map, so it cannot be aligned onto another grid; declare it first")
    try:
        w_src, w_ref = ori.celestial_wcs(src), ori.celestial_wcs(ref)
        f_src, f_ref = ori.sky_frame(w_src), ori.sky_frame(w_ref)
    except ori.Undeclared as e:
        fail(str(e))
    theta, wgt = axial_inputs(args, src)
    ny, nx = theta.shape
    xs, ys = np.meshgrid(np.arange(nx), np.arange(ny))
    pa = ori.wrap_axial(ori.pixel_to_sky_pa(w_src, xs, ys, theta) if args.axial_frame == "pixel" else theta)
    ok = np.isfinite(pa) & np.isfinite(wgt)
    pa, wgt = np.where(ok, pa, np.nan), np.where(ok, wgt, np.nan)

    def resample(a):
        return np.asarray(func((a, w_src), w_ref, shape_out=shape_out, **kw)[0], dtype=float)

    if method == "nearest":
        th_ref = resample(pa)
        rel = resample(wgt) if args.weights else None
        rel_kind = "the declared reliability, picked with the same nearest sample" if args.weights else None
        degenerate = 0
    else:
        q, u_ = ori.doubled(pa, wgt)
        th_ref, rel = ori.combine(resample(q), resample(u_), resample(wgt))
        rel_kind = "resultant length of the resampled doubled angles, |(Q, U)| / w"
        degenerate = int((np.isfinite(rel) & np.isnan(th_ref)).sum())
    ry, rx = np.meshgrid(np.arange(shape_out[0]), np.arange(shape_out[1]), indexing="ij")
    if not f_src.is_equivalent_frame(f_ref):
        origin = w_ref.pixel_to_world(rx, ry)
        th_ref = ori.rotate_pa_frame(origin.transform_to(f_src), th_ref, f_ref)
    out = ori.wrap_axial(ori.sky_pa_to_pixel(w_ref, rx, ry, th_ref) if args.axial_frame == "pixel" else th_ref)
    h = aligned_header(src, w_ref, shape_out, False)
    for key in ("BUNIT",):
        if key in src.h:
            h[key] = src.h[key]
    h["VESQOP"] = "align"
    summary = {"method": method, "axial": True, "angle_unit": args.axial_unit, "angle_frame": args.axial_frame, "shape": list(out.shape),
               "source_frame": src.sky_facts().get("frame"), "reference_frame": ref.sky_facts().get("frame"),
               "pixels_without_data": int(np.isnan(out).sum()), "pixels_orientation_undefined": degenerate,
               "reliability_map": rel_kind, "declared_reliability_used": bool(args.weights)}
    how = (f"Vesqira align (axial): {method} resampling of the orientation, through the sky frame"
           + (", back to the reference grid's pixel frame" if args.axial_frame == "pixel" else "")
           + ("; the doubled angle (Q = w cos 2*theta, U = w sin 2*theta) was resampled, not the angle" if method != "nearest" else "; one sample was picked, nothing was averaged"))
    summary["policy"] = ((DOUBLED_ANGLE if method != "nearest" else "Nearest-neighbour: each output pixel takes one input sample, so no angles are averaged.")
                         + " The angle's physical orientation is kept across the change of grid"
                         + (": the pixel-frame angle was converted to east of north with the file's WCS, resampled, rotated into the reference's celestial frame if it differs, and converted back to the reference grid's pixel frame."
                            if args.axial_frame == "pixel" else " (position angle east of north in the file's celestial frame, rotated into the reference's if it differs).")
                         + " Resolution is NOT matched; pixels outside the input's footprint are NaN, never 0.")
    write_axial(args, out, rel, h, summary, [how])
    return {"ok": True, "op": "align", "summary": summary}


def op_align(args) -> dict:
    """Reproject the input onto the reference's celestial grid (validated provider: astropy reproject)."""
    try:
        from reproject import reproject_exact, reproject_interp
    except ImportError:
        fail("the reproject package is not installed on this machine")
    from astropy.wcs import WCS
    src = open_cube(args.input)
    ref = open_cube(args.reference)
    if not src.celestial or not ref.celestial:
        fail("both files need a fully declared celestial WCS to be aligned")
    method = args.method
    func = reproject_exact if method == "exact" else reproject_interp
    kw = {} if method == "exact" else {"order": 0 if method == "nearest" else 1}
    w_ref = ref.wcs.celestial
    shape_out = (ref.shape[ref.y], ref.shape[ref.x])
    w_src = src.wcs.celestial
    if args.axial_unit:
        return align_axial(args, src, ref, func, kw, method, shape_out)
    c0, c1 = (0, src.channels - 1)
    out_planes = []
    for ch, p in planes(src, c0, c1):
        r, _ = func((p, w_src), w_ref, shape_out=shape_out, **kw)
        out_planes.append(np.asarray(r, dtype="<f4"))          # outside the input's footprint reproject gives NaN
    cube = src.chan is not None and src.channels > 1
    data = np.stack(out_planes) if cube else out_planes[0]
    h = aligned_header(src, w_ref, shape_out, cube)
    for key in ("BUNIT",):
        if key in src.h:
            h[key] = src.h[key]
    h["VESQOP"] = "align"
    h["HISTORY"] = f"Vesqira align: {method} reprojection onto the grid of the reference; resolution is NOT matched"
    bu, bu_src = unit_of(src, args.declared_unit)
    if bu_src in OWNED_UNIT:
        stamp_user_unit(h, bu, source=bu_src)
    write_fits(args.out, data, h)
    summary = {"method": method, "shape": list(data.shape),
               "source_frame": src.sky_facts().get("frame"), "reference_frame": ref.sky_facts().get("frame"),
               "pixels_without_data": int(np.isnan(data).sum()),
               "policy": "Pixel values are interpolated onto the reference grid, which changes noise correlation and does not match angular resolution; "
                         "pixels outside the input's footprint are NaN, never 0"}
    note_unit(summary, bu_src, bu)
    return {"ok": True, "op": "align", "summary": summary}


def op_axial_diff(args) -> dict:
    """Signed difference between two axial orientation maps on the same grid, in degrees in (-90, 90].

    0 and 180 degrees are the same orientation, so the difference is taken modulo 180: 1 and 179 degrees differ by
    2 degrees, not 178. Pixels where either map has no value are NaN, never 0. Statistics use the doubled-angle method.
    """
    from astropy.stats import circmean, circvar
    a = open_cube(args.input)
    b = open_cube(args.reference)
    if a.channels != 1 or b.channels != 1 or (a.shape[a.y], a.shape[a.x]) != (b.shape[b.y], b.shape[b.x]):
        fail("both maps must be single planes of the same shape")
    fa = 1.0 if args.unit == "deg" else 180.0 / math.pi
    fb = 1.0 if args.unit_ref == "deg" else 180.0 / math.pi
    _, pa = next(planes(a, 0, 0))
    _, pb = next(planes(b, 0, 0))
    with np.errstate(all="ignore"):
        d = (((pa * fa - pb * fb) + 90.0) % 180.0) - 90.0
        d = np.where(d == -90.0, 90.0, d)                         # the interval is (-90, 90]
    ok = np.isfinite(d)
    n = int(ok.sum())
    summary = {"pixels_compared": n, "pixels_without_data": int((~ok).sum()), "mean_difference_deg": None, "resultant": None,
               "rms_deg": None, "within_10_deg": None, "mean_defined": False}
    if n:
        v = d[ok]
        summary["rms_deg"] = float(np.sqrt((v ** 2).mean()))
        summary["within_10_deg"] = float((np.abs(v) <= 10.0).mean())
        two = np.radians(2.0 * v)
        r = float(1.0 - circvar(two))
        summary["resultant"] = r
        if r >= 1e-6:                                              # below this the offsets cancel: no mean exists
            summary["mean_difference_deg"] = float((np.degrees(circmean(two)) / 2.0 + 90.0) % 180.0 - 90.0)
            summary["mean_defined"] = True
    h = image_header(a, {"BUNIT": "deg", "VESQOP": "axial_diff"})
    h["HISTORY"] = "Vesqira axial difference (input minus reference), modulo 180 degrees, in (-90, 90]"
    write_fits(args.out, d.astype("<f4"), h)
    summary["policy"] = ("Difference modulo 180 degrees: 1 and 179 degrees differ by 2, not 178. A pixel without a value in either map is NaN, never 0. "
                         "Mean and spread use the doubled-angle method; a mean is undefined when the offsets cancel.")
    return {"ok": True, "op": "axial_diff", "summary": summary}


def op_scalar_diff(args) -> dict:
    """Difference (input minus reference) of two scalar maps on the same grid, in the maps' common brightness unit.

    Refused unless both maps declare the same BUNIT: nothing is converted or assumed. A pixel without a value in either map
    is NaN in the result, never 0; a recorded 0 is a value. A difference of orientation maps is a different operation
    (axial_diff), because angles are periodic.
    """
    a = open_cube(args.input)
    b = open_cube(args.reference)
    if a.channels != 1 or b.channels != 1 or (a.shape[a.y], a.shape[a.x]) != (b.shape[b.y], b.shape[b.x]):
        fail("both maps must be single planes of the same shape")
    ua, sa = unit_of(a, args.declared_unit)
    ub, sb = unit_of(b, args.declared_unit_ref)
    if not ua or not ub:
        fail("both maps must have a brightness unit, from their BUNIT or declared by you; none is assumed")
    if ua != ub:
        fail("the maps have different brightness units; convert one first, nothing is converted here")
    _, pa = next(planes(a, 0, 0))
    _, pb = next(planes(b, 0, 0))
    with np.errstate(all="ignore"):
        d = pa - pb
    ok = np.isfinite(d)
    n = int(ok.sum())
    summary = {"pixels_compared": n, "pixels_without_data": int((~ok).sum()), "unit": ua,
               "mean": None, "median": None, "rms": None, "min": None, "max": None}
    if n:
        v = d[ok]
        summary.update(mean=float(v.mean()), median=float(np.median(v)), rms=float(np.sqrt((v ** 2).mean())), min=float(v.min()), max=float(v.max()))
    h = image_header(a, {"BUNIT": ua, "VESQOP": "scalar_diff"})
    h["HISTORY"] = "Vesqira scalar difference (input minus reference), same grid, same declared unit"
    by = [w for w, sr in (("the input", sa), ("the reference", sb)) if sr in OWNED_UNIT]
    src = "corrected" if "corrected" in (sa, sb) else "user"
    if by:
        h["VESQBUNS"] = src
        h["HISTORY"] = unit_history(ua, src, " and ".join(by))
    write_fits(args.out, d.astype("<f4"), h)
    summary["policy"] = ("Input minus reference. A pixel without a value in either map is NaN, never 0; a recorded 0 is a value. The grids were checked "
                         "for agreement before this ran, but different resolution (beam) between the maps is not corrected: a difference of maps with "
                         "different beams mixes resolution with signal. The mean is a plain arithmetic mean of the finite differences.")
    note_unit(summary, src if by else "file", ua, " and ".join(by))
    return {"ok": True, "op": "scalar_diff", "summary": summary}


FWHM_TO_SIGMA = 1.0 / (2.0 * math.sqrt(2.0 * math.log(2.0)))


def _sky_cov(bmaj: float, bmin: float, bpa_deg: float) -> np.ndarray:
    """Covariance (east, north) of an elliptical Gaussian beam; the position angle runs from north towards east."""
    t = math.radians(bpa_deg)
    u = np.array([math.sin(t), math.cos(t)])       # major axis direction as (east, north)
    v = np.array([math.cos(t), -math.sin(t)])
    return (bmaj * FWHM_TO_SIGMA) ** 2 * np.outer(u, u) + (bmin * FWHM_TO_SIGMA) ** 2 * np.outer(v, v)


def convolve_axial(args, c: Cube, chans, smooth, from_beam, to_beam, n_pix) -> dict:
    """Convolve an orientation map declared axial: Q = w cos 2θ, U = w sin 2θ and w are each smoothed by the scalar path's
    kernel (NaN handled the same way) and θ = 1/2 atan2(U, Q). The kernel is applied in the frame the map is stored in (the
    declared frame is not converted: over a kernel the direction of north changes by far less than the kernel is wide, and
    nothing is resampled onto another grid). Values are angles, so the beam-area rescaling of a flux unit does not apply."""
    if len(chans) != 1:
        fail("an orientation map is a single plane")
    theta, wgt = axial_inputs(args, c)
    q, u_ = ori.doubled(theta, wgt)
    wm = np.where(np.isfinite(q), wgt, np.nan)                                 # the weight only where the angle has a value
    th, rel = ori.combine(smooth(q), smooth(u_), smooth(wm))
    nh = c.h.copy()
    nh["BMAJ"], nh["BMIN"], nh["BPA"] = to_beam[0] / 3600.0, to_beam[1] / 3600.0, to_beam[2]
    nh["BITPIX"] = -32
    for k in ("BSCALE", "BZERO", "BLANK"):
        if k in nh:
            del nh[k]
    nh["VESQOP"] = "convolve"
    summary = {"axial": True, "angle_unit": args.axial_unit, "angle_frame": args.axial_frame or None,
               "from_beam_arcsec": list(from_beam), "to_beam_arcsec": list(to_beam), "kernel_half_width_pixels": n_pix,
               "shape": list(th.shape), "pixels_without_data": int(np.isnan(th).sum()),
               "pixels_orientation_undefined": int((np.isfinite(rel) & np.isnan(th)).sum()),
               "reliability_map": "resultant length of the smoothed doubled angles, |(Q, U)| / w", "declared_reliability_used": bool(args.weights)}
    summary["policy"] = (DOUBLED_ANGLE + " Smoothed with a Gaussian kernel to the target beam; pixels without a value stay without a value and neighbours "
                         "are averaged over the finite pixels the kernel reaches. The kernel is applied in the frame the angles are stored in. "
                         "Noise becomes correlated over the new beam.")
    write_axial(args, th, rel, nh, summary, [f"Vesqira convolve (axial) {json.dumps({'from_beam_arcsec': list(from_beam), 'to_beam_arcsec': list(to_beam)})}: "
                                             "the doubled angle was smoothed, not the angle"])
    return {"ok": True, "op": "convolve", "summary": summary}


def op_convolve(args) -> dict:
    """Smooth to a larger target beam: convolve with the Gaussian that, combined with the current beam, gives it.

    Needs a declared beam (BMAJ, BMIN, BPA), a target that is at least as large in every direction, and a celestial
    WCS. Pixels without a value stay without a value; a pixel next to missing ones is the average of the finite
    neighbours the kernel reaches (weights renormalised), never a zero. In surface-brightness units (K) values are
    smoothed as they are; in Jy/beam they are also rescaled by the ratio of beam areas."""
    from astropy.convolution import convolve_fft
    c = open_cube(args.input)
    if not c.celestial or c.wcs is None or c.wcs.wcs.lng != c.x or c.wcs.wcs.lat != c.y:
        fail("the file needs a fully declared celestial WCS with the sky axes as its first two")
    h = c.h
    try:
        smaj, smin = float(h["BMAJ"]) * 3600.0, float(h["BMIN"]) * 3600.0
    except (KeyError, TypeError, ValueError):
        fail("the file does not declare its beam (BMAJ and BMIN), so the target cannot be reached by convolution")
    if not (smaj > 0 and smin > 0):
        fail("the declared beam is not positive")
    spa = float(h["BPA"]) if "BPA" in h else None
    if spa is None and abs(smaj - smin) > 1e-6 * smaj:
        fail("the beam is elliptical but its position angle (BPA) is not declared")
    spa = 0.0 if spa is None else spa
    tmaj, tmin, tpa = args.target_bmaj, args.target_bmin, args.target_bpa
    if tmaj is None or tmin is None or not (tmaj >= tmin > 0):
        fail("give the target beam as major and minor FWHM in arcseconds, major at least minor")
    cov_k = _sky_cov(tmaj, tmin, tpa) - _sky_cov(smaj, smin, spa)       # arcsec^2 in (east, north)
    w, _ = np.linalg.eigh(cov_k)
    if w[0] < -1e-9 * max(1.0, abs(w[1])):
        fail("the target beam is smaller than the current beam in some direction; convolution can only make it larger")
    m = c.wcs.celestial.pixel_scale_matrix * 3600.0                        # arcsec per pixel: (east, north) = m @ (dx, dy)
    minv = np.linalg.inv(m)
    cov_pix = minv @ cov_k @ minv.T
    ev, evec = np.linalg.eigh(cov_pix)
    n_pix = 0
    kernel = None
    if ev[1] > 1e-4:
        ev = np.maximum(ev, 1e-3)                                           # a kernel narrower than a pixel in one direction
        half = int(min(100, math.ceil(4.0 * math.sqrt(ev[1]))))
        yy, xx = np.mgrid[-half:half + 1, -half:half + 1]
        d = np.stack([xx.ravel(), yy.ravel()])
        q = np.einsum("ij,ij->j", d, np.linalg.solve(evec @ np.diag(ev) @ evec.T, d))
        kernel = np.exp(-0.5 * q).reshape(xx.shape)
        kernel /= kernel.sum()
        n_pix = half

    def smooth(p: np.ndarray) -> np.ndarray:
        """One plane smoothed by the kernel; NaN stays NaN and the finite neighbours are averaged, never a zero."""
        if kernel is None:
            return p.copy()
        padded = np.pad(p, n_pix, constant_values=np.nan)                   # outside the map is missing, not zero
        return convolve_fft(padded, kernel, nan_treatment="interpolate", preserve_nan=True, normalize_kernel=True,
                            boundary="fill", fill_value=0.0, allow_huge=True)[n_pix:-n_pix, n_pix:-n_pix]

    area = 1.0
    bunit, bu_src = unit_of(c, args.declared_unit)
    per_beam = "beam" in (bunit or "").lower()
    if per_beam:
        area = (tmaj * tmin) / (smaj * smin)
    n = len(c.shape)
    if n == 3 and (c.chan, c.y, c.x) == (2, 1, 0):
        chans = list(planes(c, 0, c.channels - 1))
    elif n == 2 and (c.y, c.x) == (1, 0):
        chans = [(0, np.asarray(next(planes(c, 0, 0))[1]))] if c.chan is None else None
    else:
        chans = None
    if chans is None:
        fail("convolution supports an image or a cube with axes (x, y[, channel]) in that order")
    if args.axial_unit:
        return convolve_axial(args, c, chans, smooth, (smaj, smin, spa), (tmaj, tmin, tpa), n_pix)
    out = []
    for _, p in chans:
        out.append((smooth(np.asarray(p, dtype=float)) * area).astype("<f4"))
    data = np.stack(out) if n == 3 else out[0]
    nh = h.copy()
    nh["BMAJ"], nh["BMIN"], nh["BPA"] = tmaj / 3600.0, tmin / 3600.0, tpa
    nh["BITPIX"] = -32
    for k in ("BSCALE", "BZERO", "BLANK"):
        if k in nh:
            del nh[k]
    nh["VESQOP"] = "convolve"
    summary = {"from_beam_arcsec": [smaj, smin, spa], "to_beam_arcsec": [tmaj, tmin, tpa], "kernel_half_width_pixels": n_pix,
               "rescaled_by_beam_area": per_beam, "area_ratio": area}
    nh["HISTORY"] = f"Vesqira convolve {json.dumps(summary)}"
    if bu_src in OWNED_UNIT:
        stamp_user_unit(nh, bunit, source=bu_src)
    write_fits(args.out, data, nh)
    out = {**summary, "shape": list(data.shape),
           "pixels_without_data": int(np.isnan(data).sum()),
           "policy": "Smoothed with a Gaussian kernel to the target beam; pixels without a value stay without a value and neighbours are "
                     "averaged over the finite pixels the kernel reaches. Noise becomes correlated over the new beam. "
                     "Units in K are smoothed as they are; Jy/beam values are rescaled by the beam-area ratio."}
    if bu_src == "unresolved":
        # not rescaled is an assumption of surface brightness, and nothing says so: the result says it
        out["policy"] += (" The file has no BUNIT and none was declared, so the values were smoothed as they are, as if in a surface-brightness unit; "
                          "if they are per beam, they need rescaling by the beam-area ratio.")
    note_unit(out, bu_src, bunit)
    if bu_src in OWNED_UNIT:
        out["policy"] += (" Whether the values are per beam was read from that text (a unit containing \"beam\" is rescaled by the beam-area ratio): "
                          f"{'rescaled' if per_beam else 'not rescaled'}.")
    return {"ok": True, "op": "convolve", "summary": out}


def op_relorient(args) -> dict:
    """Relative orientation (ADR 0047): a declared orientation map against a point catalog or another map. The statistics live in
    relorient.py; this only parses the parameters and turns its refusals into this program's."""
    import relorient
    try:
        params = json.loads(args.params) if args.params else {}
        if not isinstance(params, dict):
            raise ValueError
    except ValueError:
        fail("the parameters are not a JSON object")
    try:
        return relorient.op_catalog(args, params) if args.op == "relorient_catalog" else relorient.op_maps(args, params)
    except relorient.Refusal as e:
        fail(str(e))


def op_mask(args) -> dict:
    import masks as mk
    return mk.op_mask(args)


OPS = {"mask": op_mask, "relorient_catalog": op_relorient, "relorient_maps": op_relorient, "convolve": op_convolve, "axial_diff": op_axial_diff, "scalar_diff": op_scalar_diff, "stats": op_stats, "moment": op_moment, "cutout": op_cutout, "slab": op_slab, "align": op_align}


def log_failure(op: str, what: str) -> None:
    """A stderr line naming the API request (VESQIRA_REQUEST_ID, set by the API from its validated id) for a failed run."""
    rid = os.environ.get("VESQIRA_REQUEST_ID", "")
    rid = rid if re.fullmatch(r"[A-Za-z0-9._-]{8,64}", rid) else "-"
    print(f"analysis request={rid} op={op} failed {what}", file=sys.stderr, flush=True)


def main(argv: list[str]) -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("op", choices=sorted(OPS))
    ap.add_argument("--input", required=True)
    ap.add_argument("--reference")
    ap.add_argument("--out")
    ap.add_argument("--order", type=int, default=0)
    ap.add_argument("--chan-start", type=int)
    ap.add_argument("--chan-end", type=int)
    ap.add_argument("--threshold", type=float)
    ap.add_argument("--x0", type=int)
    ap.add_argument("--x1", type=int)
    ap.add_argument("--y0", type=int)
    ap.add_argument("--y1", type=int)
    ap.add_argument("--unit", choices=["deg", "rad"], default="deg")
    ap.add_argument("--unit-ref", choices=["deg", "rad"], default="deg")
    ap.add_argument("--target-bmaj", type=float)
    ap.add_argument("--target-bmin", type=float)
    ap.add_argument("--target-bpa", type=float, default=0.0)
    ap.add_argument("--method", choices=["interp", "nearest", "exact"], default="interp")
    ap.add_argument("--axial-unit", choices=["deg", "rad"])      # the map is declared an axial orientation map, in this unit (ADR 0042)
    ap.add_argument("--axial-frame", choices=["pixel", "sky"])   # its declared frame; absent when not declared
    ap.add_argument("--weights")                                 # its declared reliability map (same grid), a path
    ap.add_argument("--rel-out")                                 # where an orientation result's companion reliability map goes
    ap.add_argument("--declared-unit")        # the brightness unit its author declared for the input, only for a file without BUNIT
    ap.add_argument("--declared-unit-ref")    # the same for the reference
    ap.add_argument("--axial-quantity")       # what the declared angle describes (ADR 0042); relative orientation needs it
    ap.add_argument("--ref-axial-unit", choices=["deg", "rad"])  # the reference map's declaration, for relorient_maps
    ap.add_argument("--ref-axial-frame", choices=["pixel", "sky"])
    ap.add_argument("--ref-axial-quantity")
    ap.add_argument("--ref-weights")
    ap.add_argument("--catalog")              # relorient_catalog: the catalog file, and its declaration as JSON
    ap.add_argument("--declaration")
    ap.add_argument("--params")               # relorient_*: parameters as a JSON object (relorient.check_params)
    ap.add_argument("--masks")                # masks to apply, a JSON list of {path, type, policy[, weight, id, name, tier, rule, fingerprint]} (ADR 0055)
    ap.add_argument("--ref-masks")            # the same for the reference of relorient_maps
    ap.add_argument("--table-out")            # relorient_maps: where the per-pixel CSV goes
    ap.add_argument("--corrections")          # the researcher's corrections, a JSON object {resolved path: {hash, bunit, wcs}} (ADR 0061)
    args = ap.parse_args(argv)
    limit_memory()
    try:
        if args.corrections:
            try:
                corr.CURRENT.by_path = corr.parse_mapping(json.loads(args.corrections))
            except (corr.Bad, ValueError):
                fail("the corrections sent with this analysis are malformed")
        if args.op != "stats" and not args.out:
            fail("--out is required")
        if args.op in ("align", "axial_diff", "scalar_diff", "relorient_maps") and not args.reference:
            fail("--reference is required")
        result = OPS[args.op](args)
        if corr.CURRENT.by_path:
            # the API checks this before it records the result: a program that did not apply the corrections must not pass for one that did
            result["corrections_applied"] = sorted(st["hash"] for st in corr.CURRENT.by_path.values())
        import astropy
        result["software"] = {"astropy": astropy.__version__, "numpy": np.__version__, "python": sys.version.split()[0]}
        if args.op == "align":
            import reproject
            result["software"]["reproject"] = reproject.__version__
        print(json.dumps(result, allow_nan=False))
        return 0
    except Refused as e:
        log_failure(args.op, "refused")
        print(json.dumps({"ok": False, "error": e.message}))
        return 2
    except MemoryError:
        log_failure(args.op, "MemoryError")
        print(json.dumps({"ok": False, "error": over_cap_message()}))
        return 2
    except (OSError, ValueError, KeyError, TypeError, IndexError) as e:
        log_failure(args.op, type(e).__name__)
        print(json.dumps({"ok": False, "error": "cannot read or process this file"}))
        return 2


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
