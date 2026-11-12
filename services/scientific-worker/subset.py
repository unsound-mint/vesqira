"""Virtual subsets (ADR 0063): a window onto a source cube, resolved here and read through the source file.

A window is a spatial box in the source's 0-based pixels (inclusive), an optional spatial step (every step-th pixel, values
untouched: strided sampling, never an average), and a spectral interval: a channel range, or a world interval with the unit and
frame it was stated in, resolved to channels by the rule in CHANNEL_RULE against the WCS in force (the researcher's corrections
included). Nothing is read or written here: plane_worker.Cube applies the resolved window, and analysis.py writes it to a file.

    window = {"x0": 3, "x1": 20, "y0": 2, "y1": 15, "step": 2, "c0": 10, "c1": 40}
    window = {"step": 1, "world": {"lo": -20.0, "hi": -5.0, "unit": "km/s", "frame": "LSRK"}}   # whole image, a velocity interval

Keys not given mean the whole axis. A box is all four of x0, x1, y0, y1 or none.
"""
from __future__ import annotations

import math

import numpy as np
from astropy.io import fits

MAX_STEP = 64
MAX_COORD = 1 << 30
CHANNEL_TOLERANCE = 1e-6      # of one channel width, so a bound written at a channel centre includes that channel

CHANNEL_RULE = ("A channel is in the interval when its centre, from the WCS of the file as it is read (with the author's corrections), "
                "lies in the closed interval, bounds compared to a millionth of a channel width. The unit must convert to the axis' own unit "
                "without a spectral convention, and a frame must be the one the file declares: no frame is converted or assumed.")


class Bad(Exception):
    """A window that cannot be applied to this file. The message is for the researcher."""


def _int(w: dict, key: str, lo: int = 0, hi: int = MAX_COORD) -> int:
    v = w[key]
    if isinstance(v, bool) or not isinstance(v, int) or not lo <= v <= hi:
        raise Bad(f"{key} must be a whole number from {lo} to {hi}")
    return v


KNOWN = {"x0", "x1", "y0", "y1", "step", "c0", "c1", "world"}


def source_slice(sl, n: int, off: int, step: int) -> slice:
    """The source-file slice for a slice (or the whole axis, slice(None)) of the n subset pixels along one axis: pixel i of the
    subset is source pixel off + i*step, so reading a window reads only those pixels."""
    if sl.step not in (None, 1):
        raise Bad("a strided read of a subset is not supported")
    a = 0 if sl.start is None else sl.start
    b = n if sl.stop is None else sl.stop
    a = max(0, min(a, n))
    b = max(a, min(b, n))
    if b == a:
        return slice(off + a * step, off + a * step, step)
    return slice(off + a * step, off + (b - 1) * step + 1, step)


def resolve(cube, window: dict) -> dict:
    """The window against this (unwindowed) cube, validated: every bound inside the source, the channel interval resolved."""
    if not isinstance(window, dict) or set(window) - KNOWN:
        raise Bad("the window names something that is not a box, a step or a spectral interval")
    nx, ny = cube.shape[cube.x], cube.shape[cube.y]
    box = [k in window for k in ("x0", "x1", "y0", "y1")]
    if any(box) and not all(box):
        raise Bad("a box needs x0, x1, y0 and y1 together")
    x0, x1, y0, y1 = (_int(window, k) for k in ("x0", "x1", "y0", "y1")) if all(box) else (0, nx - 1, 0, ny - 1)
    if not (0 <= x0 <= x1 < nx and 0 <= y0 <= y1 < ny):
        raise Bad(f"the box is not inside the source image (0 to {nx - 1} by 0 to {ny - 1})")
    step = _int(window, "step", 1, MAX_STEP) if "step" in window else 1
    sub_nx, sub_ny = (x1 - x0) // step + 1, (y1 - y0) // step + 1
    res = {"x0": x0, "x1": x0 + (sub_nx - 1) * step, "y0": y0, "y1": y0 + (sub_ny - 1) * step, "step": step, "nx": sub_nx, "ny": sub_ny,
           "src_nx": nx, "src_ny": ny, "requested": {"x1": x1, "y1": y1}, "c0": 0, "c1": 0, "nc": 1, "rule": None, "world": None}
    given = ("c0" in window, "c1" in window)
    if any(given) and not all(given):
        raise Bad("a channel range needs c0 and c1 together")
    if cube.chan is None:
        if all(given) or "world" in window:
            raise Bad("this file has no channel axis, so no channel or spectral interval can be chosen")
        return res
    nc = cube.channels
    if all(given) and "world" in window:
        raise Bad("choose a channel range or a spectral interval, not both")
    if all(given):
        c0, c1 = _int(window, "c0"), _int(window, "c1")
        if not 0 <= c0 <= c1 < nc:
            raise Bad(f"the channel range is not inside the source's channels (0 to {nc - 1})")
        res.update(c0=c0, c1=c1, nc=c1 - c0 + 1, rule="the channel range as stated")
    elif "world" in window:
        res.update(_resolve_world(cube, window["world"]))
    else:
        res.update(c0=0, c1=nc - 1, nc=nc, rule="all channels")
    return res


def _resolve_world(cube, world) -> dict:
    from astropy import units as u
    if not isinstance(world, dict) or set(world) - {"lo", "hi", "unit", "frame"}:
        raise Bad("a spectral interval is lo, hi, unit and optionally frame")
    facts = cube.spectral_facts()
    if facts is None or not facts["resolved"]:
        raise Bad("the spectral axis is not fully declared in the header, so an interval in world units cannot be resolved to channels")
    lo, hi = world.get("lo"), world.get("hi")
    if any(isinstance(v, bool) or not isinstance(v, (int, float)) or not math.isfinite(v) for v in (lo, hi)):
        raise Bad("the interval bounds must be finite numbers")
    unit_text = world.get("unit")
    if not isinstance(unit_text, str) or not unit_text or len(unit_text) > 20:
        raise Bad("the interval needs its unit")
    frame = world.get("frame") or ""
    if not isinstance(frame, str) or len(frame) > 20:
        raise Bad("the frame must be short text")
    declared = facts.get("specsys")
    if frame and not declared:
        raise Bad(f"the interval is stated in {frame} but the file declares no spectral frame: no frame is assumed or converted")
    if frame and declared and frame.strip().upper() != str(declared).strip().upper():
        raise Bad(f"the interval is stated in {frame} but the file declares {declared}: no frame is converted")
    axis_unit = facts["unit"]
    try:
        scale = (1.0 * u.Unit(unit_text)).to(u.Unit(axis_unit)).value
    except (ValueError, TypeError, u.UnitsError):
        raise Bad(f"{unit_text} cannot be converted to the axis unit {axis_unit} without a spectral convention, which is not assumed")
    a, b = sorted((float(lo) * scale, float(hi) * scale))
    pix = np.zeros((cube.channels, len(cube.shape)))
    pix[:, cube.chan] = np.arange(cube.channels)
    import warnings
    with warnings.catch_warnings():
        warnings.simplefilter("ignore")
        vals = np.asarray(cube.wcs.all_pix2world(pix, 0)[:, cube.spec], dtype=float)
    width = float(np.nanmedian(np.abs(np.diff(vals)))) if len(vals) > 1 else 0.0
    tol = CHANNEL_TOLERANCE * width
    inside = np.flatnonzero(np.isfinite(vals) & (vals >= a - tol) & (vals <= b + tol))
    if inside.size == 0:
        raise Bad(f"no channel centre lies between {lo} and {hi} {unit_text} (the axis runs {vals[0]:g} to {vals[-1]:g} {axis_unit})")
    if inside[-1] - inside[0] + 1 != inside.size:
        raise Bad("the spectral axis is not monotonic over this interval, so it does not select a range of channels")
    c0, c1 = int(inside[0]), int(inside[-1])
    return {"c0": c0, "c1": c1, "nc": c1 - c0 + 1, "rule": CHANNEL_RULE,
            "world": {"lo": lo, "hi": hi, "unit": unit_text, "frame": frame or None, "axis_unit": axis_unit, "frame_declared": declared,
                      "first_centre": float(vals[c0]), "last_centre": float(vals[c1])}}


def window_header(h: fits.Header, cube, res: dict) -> fits.Header:
    """h (the header in force, corrections applied) with the window's shape and its WCS shifted: for the image axes
    CRPIX' = (CRPIX - 1 - first)/step + 1 and the pixel step scaled, for the channel axis CRPIX' = CRPIX - first. The step multiplies
    the image-axis columns of CD, or of PC when the header has one, and CDELT only when it has neither: a rotation or skew in the
    matrix stays exact, which scaling CDELT alone would break. Every other card is kept as it is."""
    out = h.copy()
    n = len(cube.shape)
    axes = {cube.x: (res["x0"], res["nx"]), cube.y: (res["y0"], res["ny"])}
    if cube.chan is not None:
        axes[cube.chan] = (res["c0"], res["nc"])
    step = res["step"]
    for ax, (first, count) in axes.items():
        k = ax + 1
        out[f"NAXIS{k}"] = count
        key = f"CRPIX{k}"
        if key in out:
            s = step if ax != cube.chan else 1
            out[key] = (float(out[key]) - 1.0 - first) / s + 1.0
    if step > 1:
        cols = (cube.x + 1, cube.y + 1)
        has_cd = any(f"CD{i}_{j}" in out for i in range(1, n + 1) for j in range(1, n + 1))
        has_pc = any(f"PC{i}_{j}" in out for i in range(1, n + 1) for j in range(1, n + 1))
        for j in cols:
            if has_cd:
                for i in range(1, n + 1):
                    if f"CD{i}_{j}" in out:
                        out[f"CD{i}_{j}"] = float(out[f"CD{i}_{j}"]) * step
            elif has_pc:
                for i in range(1, n + 1):
                    key = f"PC{i}_{j}"
                    value = float(out[key]) if key in out else (1.0 if i == j else 0.0)
                    if key in out or value != 0.0:
                        out[key] = value * step
            elif f"CDELT{j}" in out:
                out[f"CDELT{j}"] = float(out[f"CDELT{j}"]) * step
    return out


def report(res: dict, src: tuple[int, int, int]) -> dict:
    """The resolved window as it is echoed in every reply and recorded with a materialised file: 0-based source pixels and channels."""
    return {"x0": res["x0"], "x1": res["x1"], "y0": res["y0"], "y1": res["y1"], "step": res["step"],
            "c0": res["c0"], "c1": res["c1"], "width": res["nx"], "height": res["ny"], "channels": res["nc"],
            "source": {"width": src[0], "height": src[1], "channels": src[2]},
            "channel_rule": res["rule"], "world": res["world"]}
