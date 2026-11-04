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
"""
from __future__ import annotations

import argparse
import json
import math
import os
import sys
import tempfile
import warnings

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))

import numpy as np
from astropy.io import fits

from plane_worker import Cube, Refused, authorize, fits_array

MAX_OUTPUT_ELEMENTS = 1 << 28


def fail(msg: str):
    raise Refused("failed", msg)


def open_cube(path: str) -> Cube:
    real = authorize(path)
    return Cube(real)


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


def write_fits(path: str, data: np.ndarray, header: fits.Header):
    if data.size > MAX_OUTPUT_ELEMENTS:
        fail("the result is too large")
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

def op_stats(args) -> dict:
    c = open_cube(args.input)
    c0, c1 = chan_range(c, args)
    n = nan = 0
    mean = m2 = 0.0
    lo, hi = math.inf, -math.inf
    peak = None
    per = []
    for ch, p in planes(c, c0, c1):
        fin = p[np.isfinite(p)]
        nan += p.size - fin.size
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
    if peak is not None and c.celestial:
        w = c.coord(peak["x"], peak["y"], peak["channel"])
        out["peak"]["sky"] = w["sky"]
    return {"ok": True, "op": "stats", "summary": out}


def op_moment(args) -> dict:
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
    for ch, p in planes(c, c0, c1):
        ok = np.isfinite(p)
        if thr is not None:
            ok &= p >= thr                       # below the threshold counts as not selected, not as zero
        w = np.where(ok, p, 0.0) * width[ch]
        s0 += w
        s1 += w * vals[ch]
        count += ok
    none = count == 0
    if order == 0:
        out = np.where(none, np.nan, s0)
        bunit = f"{c.h.get('BUNIT')} {unit}".strip() if "BUNIT" in c.h else f"(brightness unit unresolved) {unit}"
    else:
        with np.errstate(all="ignore"):
            m1 = np.where((s0 > 0) & ~none, s1 / np.where(s0 > 0, s0, 1.0), np.nan)
        if order == 1:
            out, bunit = m1, unit
        else:
            for ch, p in planes(c, c0, c1):
                ok = np.isfinite(p)
                if thr is not None:
                    ok &= p >= thr
                w = np.where(ok, p, 0.0) * width[ch]
                s2 += w * (vals[ch] - np.where(np.isfinite(m1), m1, 0.0)) ** 2
            with np.errstate(all="ignore"):
                out = np.where(np.isfinite(m1), np.sqrt(s2 / np.where(s0 > 0, s0, 1.0)), np.nan)
            bunit = unit
    h = image_header(c, {"BUNIT": bunit, "VESQOP": f"moment{order}"})
    h["HISTORY"] = f"Vesqira moment {order} over channels {c0}-{c1}" + (f", values >= {thr}" if thr is not None else "")
    write_fits(args.out, out.astype("<f4"), h)
    return {"ok": True, "op": "moment", "summary": {"order": order, "channels": [c0, c1], "threshold": thr, "unit": bunit,
            "axis": name, "pixels_without_data": int(np.isnan(out).sum()),
            "policy": "NaN, BLANK and values below the threshold are excluded; a pixel with no selected value is NaN, never 0; moments 1 and 2 need a positive total"}}


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
    write_fits(args.out, arr, h)
    return {"ok": True, "op": op, "summary": {**summary, "shape": list(arr.shape),
            "policy": "values are copied unchanged; the WCS reference pixel is shifted so coordinates stay exact"}}


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
    c0, c1 = (0, src.channels - 1)
    out_planes = []
    for ch, p in planes(src, c0, c1):
        r, _ = func((p, w_src), w_ref, shape_out=shape_out, **kw)
        out_planes.append(np.asarray(r, dtype="<f4"))          # outside the input's footprint reproject gives NaN
    cube = src.chan is not None and src.channels > 1
    data = np.stack(out_planes) if cube else out_planes[0]
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
    for key in ("BUNIT",):
        if key in src.h:
            h[key] = src.h[key]
    h["VESQOP"] = "align"
    h["HISTORY"] = f"Vesqira align: {method} reprojection onto the grid of the reference; resolution is NOT matched"
    write_fits(args.out, data, h)
    return {"ok": True, "op": "align", "summary": {"method": method, "shape": list(data.shape),
            "source_frame": src.sky_facts().get("frame"), "reference_frame": ref.sky_facts().get("frame"),
            "pixels_without_data": int(np.isnan(data).sum()),
            "policy": "Pixel values are interpolated onto the reference grid, which changes noise correlation and does not match angular resolution; "
                      "pixels outside the input's footprint are NaN, never 0"}}


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
    ua, ub = a.h.get("BUNIT"), b.h.get("BUNIT")
    if not isinstance(ua, str) or not isinstance(ub, str) or not ua.strip() or not ub.strip():
        fail("both maps must declare their brightness unit (BUNIT); none is assumed")
    if ua.strip() != ub.strip():
        fail("the maps declare different brightness units; convert one first, nothing is converted here")
    _, pa = next(planes(a, 0, 0))
    _, pb = next(planes(b, 0, 0))
    with np.errstate(all="ignore"):
        d = pa - pb
    ok = np.isfinite(d)
    n = int(ok.sum())
    summary = {"pixels_compared": n, "pixels_without_data": int((~ok).sum()), "unit": ua.strip(),
               "mean": None, "median": None, "rms": None, "min": None, "max": None}
    if n:
        v = d[ok]
        summary.update(mean=float(v.mean()), median=float(np.median(v)), rms=float(np.sqrt((v ** 2).mean())), min=float(v.min()), max=float(v.max()))
    h = image_header(a, {"BUNIT": ua.strip(), "VESQOP": "scalar_diff"})
    h["HISTORY"] = "Vesqira scalar difference (input minus reference), same grid, same declared unit"
    write_fits(args.out, d.astype("<f4"), h)
    summary["policy"] = ("Input minus reference. A pixel without a value in either map is NaN, never 0; a recorded 0 is a value. The grids were checked "
                         "for agreement before this ran, but different resolution (beam) between the maps is not corrected: a difference of maps with "
                         "different beams mixes resolution with signal. The mean is a plain arithmetic mean of the finite differences.")
    return {"ok": True, "op": "scalar_diff", "summary": summary}


FWHM_TO_SIGMA = 1.0 / (2.0 * math.sqrt(2.0 * math.log(2.0)))


def _sky_cov(bmaj: float, bmin: float, bpa_deg: float) -> np.ndarray:
    """Covariance (east, north) of an elliptical Gaussian beam; the position angle runs from north towards east."""
    t = math.radians(bpa_deg)
    u = np.array([math.sin(t), math.cos(t)])       # major axis direction as (east, north)
    v = np.array([math.cos(t), -math.sin(t)])
    return (bmaj * FWHM_TO_SIGMA) ** 2 * np.outer(u, u) + (bmin * FWHM_TO_SIGMA) ** 2 * np.outer(v, v)


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
    area = 1.0
    bunit = str(h.get("BUNIT", ""))
    per_beam = "beam" in bunit.lower()
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
    out = []
    for _, p in chans:
        p = np.asarray(p, dtype=float)
        if kernel is None:
            r = p.copy()
        else:
            padded = np.pad(p, n_pix, constant_values=np.nan)               # outside the map is missing, not zero
            r = convolve_fft(padded, kernel, nan_treatment="interpolate", preserve_nan=True, normalize_kernel=True,
                             boundary="fill", fill_value=0.0, allow_huge=True)[n_pix:-n_pix, n_pix:-n_pix]
        out.append((r * area).astype("<f4"))
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
    write_fits(args.out, data, nh)
    return {"ok": True, "op": "convolve", "summary": {**summary, "shape": list(data.shape),
            "pixels_without_data": int(np.isnan(data).sum()),
            "policy": "Smoothed with a Gaussian kernel to the target beam; pixels without a value stay without a value and neighbours are "
                      "averaged over the finite pixels the kernel reaches. Noise becomes correlated over the new beam. "
                      "Units in K are smoothed as they are; Jy/beam values are rescaled by the beam-area ratio."}}


OPS = {"convolve": op_convolve, "axial_diff": op_axial_diff, "scalar_diff": op_scalar_diff, "stats": op_stats, "moment": op_moment, "cutout": op_cutout, "slab": op_slab, "align": op_align}


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
    args = ap.parse_args(argv)
    try:
        if args.op != "stats" and not args.out:
            fail("--out is required")
        if args.op in ("align", "axial_diff", "scalar_diff") and not args.reference:
            fail("--reference is required")
        result = OPS[args.op](args)
        import astropy
        result["software"] = {"astropy": astropy.__version__, "numpy": np.__version__, "python": sys.version.split()[0]}
        if args.op == "align":
            import reproject
            result["software"]["reproject"] = reproject.__version__
        print(json.dumps(result, allow_nan=False))
        return 0
    except Refused as e:
        print(json.dumps({"ok": False, "error": e.message}))
        return 2
    except (OSError, ValueError, KeyError, TypeError, IndexError, MemoryError):
        print(json.dumps({"ok": False, "error": "cannot read or process this file"}))
        return 2


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
