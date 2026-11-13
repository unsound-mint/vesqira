"""Agreement of a family of maps on one grid (ADR 0070): maps that differ in one parameter, compared with a chosen reference.

Pure arithmetic on stacks of planes (member, row, column) with NaN for no value; analysis.py opens the files and writes the results.

Scalar maps: the difference to the reference (member minus reference), and per pixel the spread across the members, as a
standard deviation (ddof 1) or a median absolute deviation about the median (not rescaled), stated in the result.

Axial maps (orientations where 0 and 180 degrees are the same): never a plain difference. The pairwise axial difference is
|((a - b + 90) mod 180) - 90|, in [0, 90] degrees. The alignment measure is AM = <cos 2 delta> over the pixels both maps have, 1 for
parallel, 0 for unrelated, -1 for perpendicular. The per-pixel spread across members is half the circular standard deviation of
the doubled angle, sqrt(-2 ln R) / 2 with R the resultant length of the doubled angles; it is undefined (NaN) where R is below
1e-6 and 0 where the members agree exactly.
"""
from __future__ import annotations

import warnings

import numpy as np

MIN_RESULTANT = 1e-6
SPREADS = ("std", "mad")


def axial_difference(a: np.ndarray, b: np.ndarray) -> np.ndarray:
    """|a - b| as an axial distance in [0, 90] degrees; NaN where either map has no value."""
    with np.errstate(invalid="ignore"):
        return np.abs(((a - b) + 90.0) % 180.0 - 90.0)


def alignment_measure(a: np.ndarray, b: np.ndarray) -> tuple[float | None, int]:
    """(<cos 2 delta> over the pixels both maps have, how many)."""
    d = np.radians(axial_difference(a, b))
    ok = np.isfinite(d)
    n = int(ok.sum())
    return (float(np.cos(2.0 * d[ok]).mean()) if n else None), n


def scalar_spread(stack: np.ndarray, how: str) -> np.ndarray:
    """Per-pixel spread across members from at least two finite values; NaN elsewhere (never 0 for a lone value)."""
    if how not in SPREADS:
        raise ValueError("the spread is std or mad")
    n = np.isfinite(stack).sum(axis=0)
    with np.errstate(all="ignore"), warnings.catch_warnings():
        warnings.simplefilter("ignore")      # an all-NaN column is a pixel without a value, not a warning
        if how == "std":
            out = np.nanstd(stack, axis=0, ddof=1)
        else:
            med = np.nanmedian(stack, axis=0)
            out = np.nanmedian(np.abs(stack - med), axis=0)
    return np.where(n >= 2, out, np.nan)


def axial_spread(stack_deg: np.ndarray) -> tuple[np.ndarray, np.ndarray, int]:
    """(spread in degrees, resultant length R, pixels where the orientations cancel). Needs two finite members per pixel."""
    n = np.isfinite(stack_deg).sum(axis=0)
    t = np.radians(2.0 * stack_deg)
    with np.errstate(all="ignore"), warnings.catch_warnings():
        warnings.simplefilter("ignore")
        c = np.nanmean(np.cos(t), axis=0)
        s = np.nanmean(np.sin(t), axis=0)
    r = np.hypot(c, s)
    with np.errstate(all="ignore"):
        sp = np.degrees(np.sqrt(-2.0 * np.log(np.minimum(r, 1.0)))) / 2.0
    undefined = (n >= 2) & (r < MIN_RESULTANT)
    sp = np.where(n >= 2, sp, np.nan)
    sp = np.where(undefined, np.nan, sp)
    return sp, np.where(n >= 2, r, np.nan), int(undefined.sum())


def _stats(v: np.ndarray) -> dict:
    return {"mean": float(v.mean()), "median": float(np.median(v)), "rms": float(np.sqrt((v ** 2).mean())), "min": float(v.min()), "max": float(v.max())}


def scalar_agreement(stack: np.ndarray, ref: int, how: str) -> dict:
    """{diffs: [member minus reference, or None for the reference], spread, members: [stats], pairwise_rms}."""
    n = stack.shape[0]
    diffs, members = [], []
    for i in range(n):
        if i == ref:
            diffs.append(None)
            members.append({"index": i, "reference": True})
            continue
        with np.errstate(invalid="ignore"):
            d = stack[i] - stack[ref]
        ok = np.isfinite(d)
        m = {"index": i, "reference": False, "pixels_compared": int(ok.sum()), "pixels_without_data": int((~ok).sum())}
        m.update(_stats(d[ok]) if ok.any() else {"mean": None, "median": None, "rms": None, "min": None, "max": None})
        diffs.append(d)
        members.append(m)
    rms = [[None] * n for _ in range(n)]
    for i in range(n):
        for j in range(n):
            with np.errstate(invalid="ignore"):
                d = stack[i] - stack[j]
            ok = np.isfinite(d)
            rms[i][j] = float(np.sqrt((d[ok] ** 2).mean())) if ok.any() else None
    sp = scalar_spread(stack, how)
    fin = sp[np.isfinite(sp)]
    return {"diffs": diffs, "spread": sp, "members": members, "pairwise_rms": rms,
            "spread_stats": {"pixels": int(fin.size), "median": float(np.median(fin)) if fin.size else None, "max": float(fin.max()) if fin.size else None}}


def axial_agreement(stack_deg: np.ndarray, ref: int) -> dict:
    """{diffs: [|axial difference| to the reference, or None], spread, members: [stats with AM], pairwise_am, undefined}."""
    n = stack_deg.shape[0]
    diffs, members = [], []
    for i in range(n):
        if i == ref:
            diffs.append(None)
            members.append({"index": i, "reference": True})
            continue
        d = axial_difference(stack_deg[i], stack_deg[ref])
        ok = np.isfinite(d)
        m = {"index": i, "reference": False, "pixels_compared": int(ok.sum()), "pixels_without_data": int((~ok).sum()),
             "mean_abs_deg": None, "rms_deg": None, "within_10_deg": None, "am": None}
        if ok.any():
            v = d[ok]
            m.update(mean_abs_deg=float(v.mean()), rms_deg=float(np.sqrt((v ** 2).mean())), within_10_deg=float((v <= 10.0).mean()),
                     am=float(np.cos(2.0 * np.radians(v)).mean()))
        diffs.append(d)
        members.append(m)
    am = [[None] * n for _ in range(n)]
    for i in range(n):
        for j in range(n):
            am[i][j] = alignment_measure(stack_deg[i], stack_deg[j])[0]
    sp, r, undefined = axial_spread(stack_deg)
    fin = sp[np.isfinite(sp)]
    return {"diffs": diffs, "spread": sp, "resultant": r, "members": members, "pairwise_am": am, "orientations_cancel_pixels": undefined,
            "spread_stats": {"pixels": int(fin.size), "median": float(np.median(fin)) if fin.size else None, "max": float(fin.max()) if fin.size else None}}
