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
import masks as mk
import orientation as ori
import orientation_profiles as oprof
import subset as sub
from srcopen import fits_open
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
    with fits_open(c.path, do_not_scale_image_data=True) as hdul:
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


def drop_producer_cards(h: fits.Header) -> None:
    """Remove the cards a pipeline wrote to describe its own product (VGT's PSIFRAME, PSIANGLE, VGTPROD, the VGT* parameters, INB*) from the
    header of a file Vesqira derives from it. They describe the angles and the resolution of the original; a smoothed or resampled map is
    neither, so keeping them would make the result re-declare itself from a header that is no longer true. The derivation says what it did."""
    producer = oprof.producer_cards()
    for k in [k for k in h.keys() if producer.fullmatch(k)]:
        del h[k]


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


def carry_masks(args, res: dict, src: Cube, fn, by: str, rule: str | None = None, dims: tuple[int, int, int] | None = None) -> dict:
    """Write the masks the researcher chose to carry (--carry-masks) and the result's recomputed no-value mask (--missing-out): masks.py."""
    return mk.carry_after(args, res, src, fn, by, rule, dims)


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
    res = _write_cube(c, out, h, "cutout", {"x": [x0, x1], "y": [y0, y1]}, args)
    return carry_masks(args, res, c, mk.cut_flags(slice(y0, y1 + 1), slice(x0, x1 + 1), None),
                       f"cutout x {x0}-{x1}, y {y0}-{y1} (0-based pixels of the source, inclusive)")


def op_subset(args) -> dict:
    """Materialise a virtual subset (ADR 0063): the same window the viewer reads through, written as an ordinary FITS file. The window
    is applied by Cube (shifted WCS, corrections in force, channels resolved by the stated rule), the planes are read through it one
    channel at a time like cutout and slab, and the result goes out through _write_cube."""
    try:
        window = json.loads(args.window) if args.window else None
    except ValueError:
        window = None
    if not isinstance(window, dict):
        fail("--window must be a JSON object")
    real = authorize(args.input)
    try:
        c = Cube(real, corr.CURRENT.get(real), window)
    except Refused as e:
        fail(e.message)
    if not (((c.chan, c.y, c.x) == (2, 1, 0) and len(c.shape) == 3) or (c.chan is None and (c.y, c.x) == (1, 0) and len(c.shape) == 2)):
        fail("a subset can be written as a file for an image or a cube with axes (x, y) or (x, y, channel) in that order; it can still be viewed")
    ny, nx = c.shape[c.y], c.shape[c.x]
    if nx * ny * c.channels > MAX_OUTPUT_ELEMENTS:
        fail("the subset is too large to write as a file")
    out = np.full((c.channels, ny, nx), np.nan, dtype="<f4")
    for ch, p in planes(c, 0, c.channels - 1):
        out[ch] = p
    rep = c.win_report()
    h = c.h.copy()
    note = (args.note or "").strip()
    if note:
        h["HISTORY"] = "Vesqira: " + note
    if rep["step"] > 1:
        # a strided window multiplies the pixel scale, so the VGT widths stated in input pixels no longer describe this grid: dropped, never left to be misread
        for k in ("VGTDN", "VGTKER", "VGTKFWHM"):
            if k in h:
                del h[k]
    h["HISTORY"] = f"Vesqira: window of the source, 0-based pixels x {rep['x0']}-{rep['x1']}, y {rep['y0']}-{rep['y1']}, every {rep['step']}, channels {rep['c0']}-{rep['c1']}"
    summary = {k: rep[k] for k in ("x0", "x1", "y0", "y1", "step", "c0", "c1", "world", "source")}
    res = _write_cube(c, out, h, "subset", summary, args)
    s = res["summary"]
    s["channel_rule"] = rep["channel_rule"]                       # in the result's summary, not repeated in every header card
    if rep["step"] > 1:
        s["policy"] += f" Every {rep['step']}th pixel of the source is kept (strided sampling, nothing averaged); the pixel step in the WCS is {rep['step']} times the source's."
    snx, sny, snc = c.src_shape_nx_ny()
    by = (f"subset window x {rep['x0']}-{rep['x1']}, y {rep['y0']}-{rep['y1']}, every {rep['step']}th pixel"
          + (f", channels {rep['c0']}-{rep['c1']}" if c.chan is not None else "") + " (0-based pixels and channels of the source, inclusive)")
    return carry_masks(args, res, c, mk.cut_flags(slice(rep["y0"], rep["y1"] + 1, rep["step"]), slice(rep["x0"], rep["x1"] + 1, rep["step"]),
                                                    slice(rep["c0"], rep["c1"] + 1) if c.chan is not None else None), by, dims=(snc, sny, snx))


def op_slab(args) -> dict:
    refuse_axial(args, "A slab")
    c = open_cube(args.input)
    c0, c1 = chan_range(c, args)
    out = np.stack([p.astype("<f4") for _, p in planes(c, c0, c1)])
    h = shifted_header(c, set(), {c.chan: c0}, {c.chan: c1 - c0 + 1})
    res = _write_cube(c, out, h, "slab", {"channels": [c0, c1]}, args)
    return carry_masks(args, res, c, mk.cut_flags(slice(None), slice(None), slice(c0, c1 + 1)),
                       f"slab of channels {c0}-{c1} (0-based, inclusive); a mask of one plane applies to every channel and is kept whole")


def _write_cube(c: Cube, data: np.ndarray, h: fits.Header, op: str, summary: dict, args) -> dict:
    # the data array is (channel, y, x); put the axes back in the file's own order
    n = len(c.shape)
    if n == 3 and (c.chan, c.y, c.x) == (2, 1, 0):
        arr = data
    elif op == "subset" and n == 2 and c.chan is None and (c.y, c.x) == (1, 0):
        arr = data[0]                                   # an image: one channel, written as a 2D file
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
    3. a sky-frame map whose header states its angles in another celestial frame than its grid's (--axial-sky-frame) is first carried
       into the grid's own frame at each pixel, and when the reference is in another celestial frame the position angle is rotated into it (astropy), and
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
    try:
        pa = ori.wrap_axial(ori.pixel_to_sky_pa(w_src, xs, ys, theta) if args.axial_frame == "pixel"
                            else ori.sky_pa_in_grid_frame(w_src, xs, ys, theta, args.axial_sky_frame))
    except ori.Undeclared as e:
        fail(str(e))
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
    return carry_masks(args, {"ok": True, "op": "align", "summary": summary}, src, mk.reproject_flags(w_src, w_ref, shape_out),
                       "align: reprojection onto the grid of the reference", mk.ALIGN_RULE)


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
    return carry_masks(args, {"ok": True, "op": "align", "summary": summary}, src, mk.reproject_flags(w_src, w_ref, shape_out),
                       "align: reprojection onto the grid of the reference", mk.ALIGN_RULE)


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
    if args.axial_sky_frame:
        # the angles are stated in another celestial frame than the grid's: carry them into the grid's own frame first, so the smoothed
        # map is a plain sky-frame map of its grid and its declaration needs no second frame
        try:
            w = ori.celestial_wcs(c)
            ys, xs = np.mgrid[0:theta.shape[0], 0:theta.shape[1]]
            theta = ori.wrap_axial(ori.sky_pa_in_grid_frame(w, xs, ys, theta, args.axial_sky_frame))
        except ori.Undeclared as e:
            fail(str(e))
    q, u_ = ori.doubled(theta, wgt)
    wm = np.where(np.isfinite(q), wgt, np.nan)                                 # the weight only where the angle has a value
    th, rel = ori.combine(smooth(q), smooth(u_), smooth(wm))
    nh = c.h.copy()
    drop_producer_cards(nh)
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
                         "are averaged over the finite pixels the kernel reaches. The kernel is applied in the frame the angles are stored in"
                         + (f"; they were first carried from the {ori.SKY_FRAMES[args.axial_sky_frame]} frame the file states them in into the grid's own frame, so the result is east of north in the grid's frame. "
                            if args.axial_sky_frame else ". ")
                         + "Noise becomes correlated over the new beam.")
    write_axial(args, th, rel, nh, summary, [f"Vesqira convolve (axial) {json.dumps({'from_beam_arcsec': list(from_beam), 'to_beam_arcsec': list(to_beam)})}: "
                                             "the doubled angle was smoothed, not the angle"])
    return carry_masks(args, {"ok": True, "op": "convolve", "summary": summary}, c, mk.dilate_flags(n_pix),
                       f"convolve to the target beam (kernel half-width {n_pix} pixels)", mk.CONVOLVE_RULE.format(n=n_pix))


SOURCE_BEAM_POLICY = {
    "vgt_approximate": ("The source resolution is the approximation the VGT keywords give (input beam and smoothing Gaussian in quadrature; the "
                        "gradient window is a top-hat and is not included), accepted by the researcher: declared by the file and approximate, "
                        "never verified, so the target resolution is reached only approximately."),
    "user": "The source resolution was entered by the researcher; the file declares none.",
}


def note_source_beam(res: dict, source_beam) -> dict:
    """Record on a convolution's summary which source resolution it started from when the file declared none."""
    if source_beam:
        res["summary"]["source_beam"] = source_beam
        res["summary"]["policy"] += " " + SOURCE_BEAM_POLICY.get(source_beam["basis"], "")
    return res


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
    supplied = args.source_bmaj is not None or args.source_bmin is not None
    source_beam = None
    if supplied:
        # A product that states no beam of its own (VGT's maps: their resolution is the input beam widened by a kernel and a window) can be
        # convolved from a resolution the person accepted or entered. A file that does declare a beam is never overridden.
        if "BMAJ" in h or "BMIN" in h:
            fail("the file declares its own beam (BMAJ, BMIN); that one is used, so a resolution given for it is refused rather than silently ignored")
        if args.source_bmaj is None or args.source_bmin is None or not (args.source_bmaj > 0 and args.source_bmin > 0):
            fail("give the source resolution as positive major and minor FWHM in arcseconds")
        smaj, smin, spa = args.source_bmaj, args.source_bmin, args.source_bpa
        basis = args.source_beam_basis or "user"
        source_beam = {"basis": basis, "approximate": basis != "user"}
    else:
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
        return note_source_beam(convolve_axial(args, c, chans, smooth, (smaj, smin, spa), (tmaj, tmin, tpa), n_pix), source_beam)
    out = []
    for _, p in chans:
        out.append((smooth(np.asarray(p, dtype=float)) * area).astype("<f4"))
    data = np.stack(out) if n == 3 else out[0]
    nh = h.copy()
    drop_producer_cards(nh)
    nh["BMAJ"], nh["BMIN"], nh["BPA"] = tmaj / 3600.0, tmin / 3600.0, tpa
    nh["BITPIX"] = -32
    for k in ("BSCALE", "BZERO", "BLANK"):
        if k in nh:
            del nh[k]
    nh["VESQOP"] = "convolve"
    summary = {"from_beam_arcsec": [smaj, smin, spa], "to_beam_arcsec": [tmaj, tmin, tpa], "kernel_half_width_pixels": n_pix,
               "rescaled_by_beam_area": per_beam, "area_ratio": area}
    if source_beam:
        summary["source_beam"] = source_beam
    nh["HISTORY"] = f"Vesqira convolve {json.dumps(summary)}"
    if bu_src in OWNED_UNIT:
        stamp_user_unit(nh, bunit, source=bu_src)
    write_fits(args.out, data, nh)
    out = {**summary, "shape": list(data.shape),
           "pixels_without_data": int(np.isnan(data).sum()),
           "policy": "Smoothed with a Gaussian kernel to the target beam; pixels without a value stay without a value and neighbours are "
                     "averaged over the finite pixels the kernel reaches. Noise becomes correlated over the new beam. "
                     "Units in K are smoothed as they are; Jy/beam values are rescaled by the beam-area ratio."}
    if source_beam:
        out["policy"] += " " + SOURCE_BEAM_POLICY.get(source_beam["basis"], "")
    if bu_src == "unresolved":
        # not rescaled is an assumption of surface brightness, and nothing says so: the result says it
        out["policy"] += (" The file has no BUNIT and none was declared, so the values were smoothed as they are, as if in a surface-brightness unit; "
                          "if they are per beam, they need rescaling by the beam-area ratio.")
    note_unit(out, bu_src, bunit)
    if bu_src in OWNED_UNIT:
        out["policy"] += (" Whether the values are per beam was read from that text (a unit containing \"beam\" is rescaled by the beam-area ratio): "
                          f"{'rescaled' if per_beam else 'not rescaled'}.")
    return carry_masks(args, {"ok": True, "op": "convolve", "summary": out}, c, mk.dilate_flags(n_pix),
                       f"convolve to the target beam (kernel half-width {n_pix} pixels)", mk.CONVOLVE_RULE.format(n=n_pix))


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


def op_healpix_sample(args) -> dict:
    """A HEALPix polarization map sampled onto a dataset's celestial grid as Q and U, then turned into an angle (ADR 0067)."""
    import healpix_pol as hpp
    try:
        params = json.loads(args.params) if args.params else {}
        if not isinstance(params, dict):
            raise ValueError
    except ValueError:
        fail("the parameters are not a JSON object")
    try:
        import astropy_healpix  # noqa: F401  (only machines that sample HEALPix maps need it, as only alignment needs reproject)
    except ImportError:
        fail("sampling a HEALPix map needs the astropy-healpix package, which is not installed for this program's Python")
    try:
        return hpp.op_sample(args, params)
    except hpp.HealpixRefused as e:
        fail(str(e))


def review_inputs(args):
    """The crop, the fit and what the crop says about channels and units, for the decomposition review (ADR 0046): (params, crop, dat, nx, ny, n, scales)."""
    import gaussreview as gr
    from plane_worker import rohsa_velocity_axis
    try:
        params = json.loads(args.params) if args.params else {}
        if not isinstance(params, dict):
            raise ValueError
    except ValueError:
        fail("the parameters are not a JSON object")
    n = params.get("n_gauss")
    dat = params.get("dat_path")
    if isinstance(n, bool) or not isinstance(n, int) or not 1 <= n <= gr.MAX_GAUSSIANS or not isinstance(dat, str) or not os.path.isabs(dat) or "\0" in dat:
        fail("the decomposition review needs the .dat's path and the number of Gaussians")
    dat = authorize(dat)
    c = open_cube(args.input)
    if c.chan is None or c.channels < 2:
        fail("the crop has no spectral axis of at least two channels")
    world, dv, axis = rohsa_velocity_axis(c)
    w0 = step = None
    if world is not None:
        w = world(np.array([0.0, 1.0]))
        w0, step = float(w[0]), float(w[1] - w[0])
    bunit, src = unit_of(c, args.declared_unit)
    sc = gr.Scales(c.channels, w0, step, axis, bunit, src)
    return params, c, dat, c.shape[c.x], c.shape[c.y], n, sc


def review_cache(params: dict) -> str | None:
    """The caller's cache folder for the diagram's pixel index (the API's cache folder, never beside the data), or None."""
    d = params.get("cache_dir")
    if d is None:
        return None
    if not isinstance(d, str) or not os.path.isabs(d) or "\0" in d:
        fail("the cache folder must be an absolute path")
    return d


def review_common(sc, dat, extra: dict) -> dict:
    import gaussreview as gr
    out = {"dat": gr.dat_identity(dat), "convention": "centre = the crop's velocity axis at (file mean - 1) channels; sigma in km/s = sigma in channels x |km/s per channel|; "
           "a component whose sigma is not positive and finite is invalid and left out; an amplitude of 0 is a value"}
    if sc.bunit and sc.bunit_source in OWNED_UNIT:
        note_unit(out, sc.bunit_source, sc.bunit, "the crop")
    elif sc.bunit:
        out["unit_source"] = sc.bunit_source
    out.update(extra)
    return out


def op_rohsa_sigmav(args) -> dict:
    """The sigma-v diagram of a decomposition: widths against centres, weighted, with each cell's contributor count (gaussreview.sigmav)."""
    import gaussreview as gr
    params, c, dat, nx, ny, n, sc = review_inputs(args)
    dv_default, ds_default = gr.default_bins(sc)
    try:
        v_edges = gr.linear_edges(params.get("velocity_bins") or dv_default, "centre")
        s_edges = gr.linear_edges(params.get("sigma_bins") or ds_default, "width")
        res = gr.sigmav(dat, nx, ny, n, sc, params.get("weight") or "integrated", v_edges, s_edges, cache_dir=review_cache(params))
    except gr.Refusal as e:
        fail(str(e))
    res["width"], res["height"] = nx, ny
    return {"ok": True, "op": "rohsa_sigmav", "summary": review_common(sc, dat, res)}


def op_rohsa_cellmask(args) -> dict:
    """The pixels that have a component in one cell of the diagram, as a packed mask (gaussreview.cell_mask)."""
    import gaussreview as gr
    params, c, dat, nx, ny, n, sc = review_inputs(args)
    dv_default, ds_default = gr.default_bins(sc)
    cell = params.get("cell")
    if not isinstance(cell, dict) or any(isinstance(cell.get(k), bool) or not isinstance(cell.get(k), int) for k in ("s", "v")):
        fail("name the cell as whole bin numbers s and v")
    try:
        v_edges = gr.linear_edges(params.get("velocity_bins") or dv_default, "centre")
        s_edges = gr.linear_edges(params.get("sigma_bins") or ds_default, "width")
        res = gr.cell_mask(dat, nx, ny, n, sc, v_edges, s_edges, cell["s"], cell["v"], cache_dir=review_cache(params) or fail("the cache folder for the pixel index is not set"))
    except gr.Refusal as e:
        fail(str(e))
    res["cell"] = {"s": cell["s"], "v": cell["v"]}
    return {"ok": True, "op": "rohsa_cellmask", "summary": review_common(sc, dat, res)}


def op_rohsa_classmaps(args) -> dict:
    """One map per width class, summed over the decomposition, written as <out stem>-class<i>.fits (gaussreview.class_maps)."""
    import gaussreview as gr
    params, c, dat, nx, ny, n, sc = review_inputs(args)
    try:
        res = gr.class_maps(dat, nx, ny, n, sc, params.get("weight") or "integrated", gr.class_cuts(params.get("edges")))
    except gr.Refusal as e:
        fail(str(e))
    maps = res.pop("maps")
    stem = args.out[:-5] if args.out.endswith(".fits") else args.out
    for cl in res["classes"]:
        i = cl["index"]
        h = image_header(c, {"BUNIT": res["weight_unit"], "VESQOP": "rohsa_class"})
        h["VESQCLS"] = (i, "width class index, 0 = narrowest")
        h["HISTORY"] = f"Vesqira ROHSA width class: {cl['label']}"
        h["HISTORY"] = f"weight: {res['weight']}"
        if res["weight"] != "count" and sc.bunit_source in OWNED_UNIT:
            h["VESQBUNS"] = sc.bunit_source
            h["HISTORY"] = unit_history(sc.bunit, sc.bunit_source, "the crop")
        write_fits(f"{stem}-class{i}.fits", maps[i].astype("<f4"), h)
    res["policy"] = ("Each pixel holds the summed weight of its valid components in the class. A pixel with no valid component is NaN, never 0; "
                     "a pixel with valid components and none in the class is 0. Classes are half-open, [lo, hi): a width on an edge is in the class above.")
    return {"ok": True, "op": "rohsa_classmaps", "summary": review_common(sc, dat, res)}


OPS = {"rohsa_sigmav": op_rohsa_sigmav, "rohsa_cellmask": op_rohsa_cellmask, "rohsa_classmaps": op_rohsa_classmaps, "healpix_sample": op_healpix_sample, "mask": op_mask, "relorient_catalog": op_relorient, "relorient_maps": op_relorient, "convolve": op_convolve, "axial_diff": op_axial_diff, "scalar_diff": op_scalar_diff, "stats": op_stats, "moment": op_moment, "cutout": op_cutout, "subset": op_subset, "slab": op_slab, "align": op_align}


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
    ap.add_argument("--source-bmaj", type=float)    # arcsec: the resolution of a file that declares no beam, accepted or entered by the researcher
    ap.add_argument("--source-bmin", type=float)
    ap.add_argument("--source-bpa", type=float)
    ap.add_argument("--source-beam-basis", choices=["vgt_approximate", "user"])
    ap.add_argument("--axial-sky-frame", choices=list(ori.SKY_FRAMES))   # the celestial frame a sky-frame map states its angles in, when not its grid's
    ap.add_argument("--ref-axial-sky-frame", choices=list(ori.SKY_FRAMES))
    ap.add_argument("--axial-frame", choices=["pixel", "sky"])   # its declared frame; absent when not declared
    ap.add_argument("--weights")                                 # its declared reliability map (same grid), a path
    ap.add_argument("--rel-out")                                 # where an orientation result's companion reliability map goes
    ap.add_argument("--declared-unit")        # the brightness unit its author declared for the input, only for a file without BUNIT
    ap.add_argument("--declared-unit-ref")    # the same for the reference
    ap.add_argument("--third")                # relorient_maps: a scalar map to bin the comparison by (same grid)
    ap.add_argument("--declared-unit-third")  # the brightness unit its author declared for the third map, only for a file without BUNIT
    ap.add_argument("--axial-quantity")       # what the declared angle describes (ADR 0042); relative orientation needs it
    ap.add_argument("--ref-axial-unit", choices=["deg", "rad"])  # the reference map's declaration, for relorient_maps
    ap.add_argument("--ref-axial-frame", choices=["pixel", "sky"])
    ap.add_argument("--ref-axial-quantity")
    ap.add_argument("--ref-weights")
    ap.add_argument("--catalog")              # relorient_catalog: the catalog file, and its declaration as JSON
    ap.add_argument("--declaration")
    ap.add_argument("--params")               # relorient_*: parameters as a JSON object (relorient.check_params)
    ap.add_argument("--masks")                # masks to apply, a JSON list of {path, type, policy[, weight, id, name, tier, rule, fingerprint]} (ADR 0055)
    ap.add_argument("--carry-masks")          # masks to carry into the result of cutout, slab, subset, align, convolve: JSON list of {path, type, out}
    ap.add_argument("--missing-out")          # where the result's own no-value mask goes (computed again from its values, never carried)
    ap.add_argument("--ref-masks")            # the same for the reference of relorient_maps
    ap.add_argument("--table-out")            # relorient_maps: where the per-pixel CSV goes
    ap.add_argument("--p-out")                # healpix_sample: where the companion P map goes
    ap.add_argument("--sigma-out")            # healpix_sample: where the companion sigma_P map goes
    ap.add_argument("--window")               # subset: the window as a JSON object (subset.py)
    ap.add_argument("--note")                 # subset: one line of provenance for the file's HISTORY
    ap.add_argument("--corrections")          # the researcher's corrections, a JSON object {resolved path: {hash, bunit, wcs}} (ADR 0061)
    args = ap.parse_args(argv)
    limit_memory()
    try:
        if args.corrections:
            try:
                corr.CURRENT.by_path = corr.parse_mapping(json.loads(args.corrections))
            except (corr.Bad, ValueError):
                fail("the corrections sent with this analysis are malformed")
        if args.op not in ("stats", "rohsa_sigmav", "rohsa_cellmask") and not args.out:
            fail("--out is required")
        if args.op in ("align", "axial_diff", "scalar_diff", "relorient_maps", "healpix_sample") and not args.reference:
            fail("--reference is required")
        result = OPS[args.op](args)
        if corr.CURRENT.by_path:
            # the API checks this before it records the result: a program that did not apply the corrections must not pass for one that did
            result["corrections_applied"] = sorted(st["hash"] for st in corr.CURRENT.by_path.values())
            keys = sorted({k for st in corr.CURRENT.by_path.values() for k in (st.get("wcs") or {})})
            summary = result.get("summary")
            if keys and isinstance(summary, dict):
                # the summary is what travels with a figure to the reviewer; the FITS stamp alone would not reach them
                prior = str(summary.get("policy") or "").rstrip()
                summary["policy"] = (prior + ("" if prior.endswith(".") or not prior else ".") + " The WCS of the input was the author's correction ("
                                     + ", ".join(keys) + "), not the file's own; the file was not changed, and a result file carries the corrected values, marked VESQWCSC.").strip()
        import astropy
        result["software"] = {"astropy": astropy.__version__, "numpy": np.__version__, "python": sys.version.split()[0]}
        if args.op == "align":
            import reproject
            result["software"]["reproject"] = reproject.__version__
        if args.op == "healpix_sample":
            import astropy_healpix
            result["software"]["astropy_healpix"] = astropy_healpix.__version__
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
