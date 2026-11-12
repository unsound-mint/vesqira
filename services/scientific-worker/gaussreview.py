"""Review of a ROHSA Gaussian decomposition as a whole (ADR 0046, "Reviewing the decomposition"): a sigma-v diagram, the pixels
behind one of its cells, and one map per width class.

The decomposition is the raw `rohsa_*.dat` (see rohsa.py for the row layout and the conventions: `mean` is 1-based in channels,
`sigma` in channels, `amp` in the crop's brightness unit). Every operation here reads every row once, in chunks, so memory does
not depend on the file's size; the work is bounded by a time budget and by the caps below, and runs as an analysis program in the
heavy workload class.

Rules this module keeps:
- One binning function (`bin_index`) serves the diagram, the cell mask and the class maps. Bins and classes are half-open,
  `[lo, hi)`, so a component whose width is exactly a class edge is in the class above it ("narrow" is `sigma < cut`, the
  pipeline's own rule and the viewer's `classify`). A value on the last edge of the diagram is outside it, never clipped in.
- Components outside the diagram, and components whose sigma is not positive and finite, are counted and reported, never
  folded into an edge bin. An amplitude of 0 is a fitted value and is a contributor with weight 0.
- Widths and centres are in km/s only when the crop's spectral axis is a resolved, uniform velocity axis; otherwise they stay in
  channels, and the weights that need km/s are refused with the reason (no unit is assumed).
- A pixel with no valid component has no class value (NaN); a pixel with valid components and none in a class has 0 there.
"""
from __future__ import annotations

import base64
import os
import time
import zlib

import numpy as np

from srcopen import source_file, stat_source

CHUNK_BYTES = 8 << 20
MAX_BINS = 128                       # per axis of the diagram: keeps the reply and the plot bounded
MAX_CLASSES = 8
MAX_GAUSSIANS = 64
MAX_SECONDS = 25 * 60                # inside the analysis program's 30-minute budget
MAX_MASK_REPLY = 700_000             # bytes of base64 on stdout (the API reads at most 1 MiB)
SQRT_2PI = float(np.sqrt(2.0 * np.pi))
# Optically thin 21 cm: N(H I) = 1.8224e18 cm^-2 x integral of T_B dv over (K km/s) (Dickey and Lockman 1990). Not read from the
# pipeline (its own nhi maps are products); named here because the result's meaning depends on it, and said in the result.
NHI_PER_K_KMS = 1.8224e18

WEIGHTS = ("integrated", "column_thin", "count")


class Refusal(Exception):
    """A request this module will not carry out, with the sentence the person reads."""


# ---- binning

def bin_index(values: np.ndarray, edges: np.ndarray) -> np.ndarray:
    """Index of the half-open bin [edges[i], edges[i+1]) each value is in; -1 for below the first edge, on or above the last edge, or not finite."""
    idx = np.searchsorted(edges, values, side="right") - 1
    ok = (idx >= 0) & (idx < len(edges) - 1) & np.isfinite(values)
    return np.where(ok, idx, -1)


def class_index(sigma: np.ndarray, cuts: np.ndarray) -> np.ndarray:
    """Width class 0..len(cuts): 0 is sigma < cuts[0]; class i is cuts[i-1] <= sigma < cuts[i]; the last is sigma >= cuts[-1]."""
    return np.searchsorted(cuts, sigma, side="right")


def linear_edges(spec: dict, name: str) -> np.ndarray:
    """Edges from {min, max, n}: n equal bins, validated."""
    try:
        lo, hi, n = float(spec["min"]), float(spec["max"]), spec["n"]
    except (KeyError, TypeError, ValueError):
        raise Refusal(f"the {name} bins need min, max and n")
    if isinstance(n, bool) or not isinstance(n, int) or not 1 <= n <= MAX_BINS:
        raise Refusal(f"the {name} bins: n must be a whole number from 1 to {MAX_BINS}")
    if not (np.isfinite(lo) and np.isfinite(hi)) or not hi > lo:
        raise Refusal(f"the {name} bins need finite limits with max above min")
    return np.linspace(lo, hi, n + 1)


def class_cuts(raw) -> np.ndarray:
    if not isinstance(raw, list) or not 1 <= len(raw) <= MAX_CLASSES - 1:
        raise Refusal(f"give from 1 to {MAX_CLASSES - 1} width edges")
    try:
        cuts = np.array([float(x) for x in raw])
    except (TypeError, ValueError):
        raise Refusal("the width edges must be numbers")
    if not np.all(np.isfinite(cuts)) or np.any(cuts <= 0) or np.any(np.diff(cuts) <= 0):
        raise Refusal("the width edges must be positive and strictly increasing")
    return cuts


# ---- the axis and the units

class Scales:
    """What the crop says about channels: the velocity of channel 0 and the step (signed) when its axis is a resolved, uniform velocity
    axis, else None (centres and widths stay in channels), and the brightness unit with whose it is."""

    def __init__(self, nc: int, w0: float | None, step: float | None, axis: dict, bunit: str | None, bunit_source: str):
        self.nc, self.w0, self.step, self.axis = nc, w0, step, axis
        self.bunit, self.bunit_source = bunit, bunit_source

    @property
    def velocity(self) -> bool:
        return self.step is not None

    @property
    def dv(self) -> float | None:
        return abs(self.step) if self.step is not None else None

    @property
    def unit(self) -> str:
        return "km/s" if self.velocity else "channels"

    def centre(self, mean0: np.ndarray) -> np.ndarray:
        """Centre on the diagram's axis: the verified-uniform velocity axis evaluated at the 0-based fractional channel, else the channel."""
        return self.w0 + mean0 * self.step if self.velocity else mean0

    def width(self, sigma_ch: np.ndarray) -> np.ndarray:
        return sigma_ch * self.dv if self.velocity else sigma_ch


def weight_unit(weight: str, sc: Scales) -> tuple[str, str]:
    """(unit, sentence) of a weight, or a Refusal when the crop does not give what it needs."""
    if weight not in WEIGHTS:
        raise Refusal("weight must be integrated, column_thin or count")
    if weight == "count":
        return "components", "each component counts 1"
    if not sc.velocity:
        raise Refusal("the crop's spectral axis is not a resolved velocity axis, so a flux in K km/s is not defined; weight by count, or fix the axis first. " + (sc.axis.get("note") or ""))
    if not sc.bunit:
        raise Refusal("the crop has no brightness unit (BUNIT), so the integrated flux has no unit; declare or correct the unit, or weight by count")
    if weight == "integrated":
        return f"{sc.bunit} km/s", f"integrated intensity amp x sigma x sqrt(2 pi) of each component, in {sc.bunit} km/s"
    from astropy import units as u
    try:
        is_kelvin = u.Unit(sc.bunit) == u.K
    except Exception:
        is_kelvin = False
    if not is_kelvin:
        raise Refusal(f"column density needs a brightness temperature in K; this crop's unit is {sc.bunit}")
    return "cm^-2", (f"optically thin H I column density N = {NHI_PER_K_KMS:.4g} cm^-2 per K km/s x the integrated intensity of each component; "
                     "a lower limit where the gas is not optically thin (cold gas)")


def weights_of(weight: str, amp: np.ndarray, sigma_ch: np.ndarray, sc: Scales) -> np.ndarray:
    if weight == "count":
        return np.ones_like(amp)
    integ = amp * sigma_ch * SQRT_2PI * sc.dv
    return integ * NHI_PER_K_KMS if weight == "column_thin" else integ


# ---- the streaming reader

def stream_components(dat_path: str, nx: int, ny: int, n: int, max_seconds: float = MAX_SECONDS):
    """Yield (x, y, amp, mean_raw, sigma_ch) arrays for consecutive chunks of whole rows of a ROHSA .dat.

    The layout is verified as the rows pass (rohsa.py): five finite columns, and the row at position r is the (i, j) of pixel r // n
    (so the pixel of a component is read from the verified columns, never inferred); the total must be nx x ny x n rows. The file is
    never loaded: a chunk is CHUNK_BYTES, and a call that outlives its time budget stops with a Refusal.
    """
    if n < 1 or n > MAX_GAUSSIANS:
        raise Refusal(f"n_gauss must be from 1 to {MAX_GAUSSIANS}")
    t0 = time.monotonic()
    row = 0
    expected = nx * ny * n

    def parse(buf: bytes):
        nonlocal row
        if b"#" in buf:
            buf = b"\n".join(ln for ln in buf.split(b"\n") if not ln.lstrip().startswith(b"#"))
        toks = buf.split()
        if not toks:
            return None
        if len(toks) % 5:
            raise Refusal("a row of the .dat does not have five columns; it is not the ROHSA row layout")
        try:
            v = np.array(toks, dtype="S").astype(np.float64).reshape(-1, 5)
        except ValueError:
            raise Refusal("a value in the .dat is not a number; it is not the ROHSA row layout")
        if not np.isfinite(v).all():
            raise Refusal("a value in the .dat is not finite; it is not the ROHSA row layout")
        r = row + np.arange(len(v), dtype=np.int64)
        p = r // n
        if not (np.array_equal(v[:, 0], p // nx) and np.array_equal(v[:, 1], p % nx)):
            raise Refusal(f"the .dat is not in y, x, Gaussian order for a {nx} x {ny} crop with {n} Gaussians (row {row + 1} onwards)")
        row += len(v)
        if row > expected:
            raise Refusal(f"the .dat has more rows than {nx} x {ny} pixels x {n} Gaussians = {expected}")
        return v[:, 1].astype(np.int64), v[:, 0].astype(np.int64), v[:, 2].copy(), v[:, 3].copy(), v[:, 4].copy()

    with source_file(dat_path) as (_, f):
        tail = b""
        while True:
            if time.monotonic() - t0 > max_seconds:
                raise Refusal(f"reading the .dat took more than {int(max_seconds)} s and was stopped; this fit is too large for one pass here")
            buf = f.read(CHUNK_BYTES)
            if not buf:
                break
            buf = tail + buf
            cut = buf.rfind(b"\n")
            if cut < 0:
                tail = buf
                if len(tail) > CHUNK_BYTES:
                    raise Refusal("a line of the .dat is longer than the chunk size; it is not the ROHSA row layout")
                continue
            tail = buf[cut + 1:]
            out = parse(buf[:cut + 1])
            if out is not None:
                yield out
        out = parse(tail) if tail.strip() else None
        if out is not None:
            yield out
    if row != expected:
        raise Refusal(f"the .dat has {row} data rows; a {nx} x {ny} crop with {n} Gaussians needs {expected}")


def valid_mask(amp: np.ndarray, sigma_ch: np.ndarray) -> np.ndarray:
    """A component is drawn and counted when its sigma is positive and finite (no floor is invented); amp 0 is a value."""
    return np.isfinite(amp) & np.isfinite(sigma_ch) & (sigma_ch > 0)


# ---- the diagram

def default_bins(sc: Scales) -> tuple[dict, dict]:
    """Sensible starting bins from the crop alone (no pass over the fit): the whole spectral range with one bin per channel, up to 64,
    half a channel of margin so a centre on the first or last channel is inside; widths from 0 to 12 km/s (or to half the axis)."""
    nc = sc.nc
    if sc.velocity:
        a, b = sc.w0, sc.w0 + (nc - 1) * sc.step
        lo, hi = min(a, b) - sc.dv / 2, max(a, b) + sc.dv / 2
        smax = min(12.0, 0.5 * (hi - lo))
    else:
        lo, hi = -0.5, nc - 0.5
        smax = max(nc / 4.0, 1.0)
    nv = int(min(64, nc))
    return {"min": float(lo), "max": float(hi), "n": nv}, {"min": 0.0, "max": float(smax), "n": 48}


def sigmav(dat_path: str, nx: int, ny: int, n: int, sc: Scales, weight: str, v_edges: np.ndarray, s_edges: np.ndarray,
           max_seconds: float = MAX_SECONDS) -> dict:
    """One pass: the 2D histogram of width (rows, `s_edges`) against centre (columns, `v_edges`), weighted, with the contributor count of each cell."""
    unit, how = weight_unit(weight, sc)
    nv, ns = len(v_edges) - 1, len(s_edges) - 1
    counts = np.zeros(ns * nv, dtype=np.int64)
    sums = np.zeros(ns * nv, dtype=np.float64)
    total = invalid = outside = negative = 0
    w_total = w_outside = 0.0
    lo_s = hi_s = lo_c = hi_c = None
    for x, y, amp, mean_raw, sig in stream_components(dat_path, nx, ny, n, max_seconds):
        ok = valid_mask(amp, sig)
        total += len(amp)
        invalid += int((~ok).sum())
        amp, mean_raw, sig = amp[ok], mean_raw[ok], sig[ok]
        if not len(amp):
            continue
        w = weights_of(weight, amp, sig, sc)
        negative += int((w < 0).sum())
        cen, wid = sc.centre(mean_raw - 1.0), sc.width(sig)
        lo_s = float(wid.min()) if lo_s is None else min(lo_s, float(wid.min()))
        hi_s = float(wid.max()) if hi_s is None else max(hi_s, float(wid.max()))
        lo_c = float(cen.min()) if lo_c is None else min(lo_c, float(cen.min()))
        hi_c = float(cen.max()) if hi_c is None else max(hi_c, float(cen.max()))
        iv, js = bin_index(cen, v_edges), bin_index(wid, s_edges)
        inside = (iv >= 0) & (js >= 0)
        outside += int((~inside).sum())
        w_total += float(w.sum())
        w_outside += float(w[~inside].sum())
        flat = js[inside] * nv + iv[inside]
        counts += np.bincount(flat, minlength=ns * nv)
        sums += np.bincount(flat, weights=w[inside], minlength=ns * nv)
    nz = np.flatnonzero(counts)
    return {
        "weight": weight, "weight_unit": unit, "weight_how": how,
        "axes": {"centre": {"unit": sc.unit, "name": sc.axis.get("name")}, "sigma": {"unit": sc.unit}},
        "velocity_axis": sc.axis,
        "brightness_unit": sc.bunit, "brightness_unit_source": sc.bunit_source,
        "v_edges": [float(e) for e in v_edges], "sigma_edges": [float(e) for e in s_edges],
        "binning": "half-open bins [lo, hi): a value on a bin's upper edge is in the next bin, and a value on the last edge is outside the diagram",
        "cells": [[int(i // nv), int(i % nv), int(counts[i]), float(sums[i])] for i in nz],
        "components": total, "invalid_sigma": invalid, "outside": outside, "in_diagram": int(counts.sum()),
        "weight_total": w_total, "weight_outside": w_outside, "negative_weight_components": negative,
        "observed": {"sigma": [lo_s, hi_s], "centre": [lo_c, hi_c]},
    }


# ---- the pixels behind a cell

def cell_mask(dat_path: str, nx: int, ny: int, n: int, sc: Scales, v_edges: np.ndarray, s_edges: np.ndarray, cell_s: int, cell_v: int,
              max_seconds: float = MAX_SECONDS) -> dict:
    """The pixels that have at least one component in the diagram cell (sigma bin `cell_s`, centre bin `cell_v`), as a packed mask."""
    nv, ns = len(v_edges) - 1, len(s_edges) - 1
    if not (0 <= cell_s < ns and 0 <= cell_v < nv):
        raise Refusal("that cell is outside the diagram")
    hit = np.zeros(nx * ny, dtype=bool)
    ncomp = 0
    for x, y, amp, mean_raw, sig in stream_components(dat_path, nx, ny, n, max_seconds):
        ok = valid_mask(amp, sig)
        iv = bin_index(sc.centre(mean_raw - 1.0), v_edges)
        js = bin_index(sc.width(sig), s_edges)
        sel = ok & (iv == cell_v) & (js == cell_s)
        ncomp += int(sel.sum())
        hit[y[sel] * nx + x[sel]] = True
    return {"mask": pack_mask(hit, nx, ny), "pixels": int(hit.sum()), "components": ncomp}


def pack_mask(hit: np.ndarray, nx: int, ny: int) -> dict:
    """A mask as {width, height, encoding, data}: the nx x ny booleans in row-major order (row y = 0 is the file's first row, x fastest),
    8 to a byte most significant bit first (numpy packbits), deflated (zlib), then base64."""
    data = base64.b64encode(zlib.compress(np.packbits(hit).tobytes(), 6)).decode("ascii")
    if len(data) > MAX_MASK_REPLY:
        raise Refusal("the pixel mask for this crop is too large to send; use a smaller crop")
    return {"width": nx, "height": ny, "encoding": "packbits-msb-row-major+zlib+base64", "data": data}


def unpack_mask(m: dict) -> np.ndarray:
    raw = np.frombuffer(zlib.decompress(base64.b64decode(m["data"])), dtype=np.uint8)
    return np.unpackbits(raw)[: m["width"] * m["height"]].astype(bool).reshape(m["height"], m["width"])


# ---- one map per width class

def class_maps(dat_path: str, nx: int, ny: int, n: int, sc: Scales, weight: str, cuts: np.ndarray, max_seconds: float = MAX_SECONDS) -> dict:
    """The decomposition summed per width class: for every pixel, the weight of its valid components of each class.

    A pixel with no valid component has no value (NaN) in any class; a pixel with valid components and none in a class has 0 in it.
    """
    if not sc.velocity:
        raise Refusal("width classes are cut in km/s, and this crop's spectral axis is not a resolved velocity axis. " + (sc.axis.get("note") or ""))
    unit, how = weight_unit(weight, sc)
    k = len(cuts) + 1
    npix = nx * ny
    sums = np.zeros((k, npix), dtype=np.float64)
    comps = np.zeros(k, dtype=np.int64)
    has = np.zeros(npix, dtype=bool)
    invalid = total = 0
    for x, y, amp, mean_raw, sig in stream_components(dat_path, nx, ny, n, max_seconds):
        ok = valid_mask(amp, sig)
        total += len(amp)
        invalid += int((~ok).sum())
        if not ok.any():
            continue
        pix = y[ok] * nx + x[ok]
        w = weights_of(weight, amp[ok], sig[ok], sc)
        cls = class_index(sc.width(sig[ok]), cuts)
        has[pix] = True
        for c in range(k):
            sel = cls == c
            if sel.any():
                sums[c] += np.bincount(pix[sel], weights=w[sel], minlength=npix)
                comps[c] += int(sel.sum())
    maps = np.where(has[None, :], sums, np.nan).reshape(k, ny, nx)
    edges = [0.0, *[float(c) for c in cuts], float("inf")]
    classes = []
    for c in range(k):
        lo, hi = edges[c], edges[c + 1]
        label = f"sigma < {hi:g} km/s" if c == 0 else (f"sigma >= {lo:g} km/s" if c == k - 1 else f"{lo:g} <= sigma < {hi:g} km/s")
        classes.append({"index": c, "label": label, "lo": lo, "hi": None if hi == float("inf") else hi, "components": int(comps[c]),
                        "weight_total": float(sums[c].sum())})
    return {"maps": maps, "weight": weight, "weight_unit": unit, "weight_how": how, "classes": classes, "components": total,
            "invalid_sigma": invalid, "pixels_without_valid_component": int((~has).sum())}


def dat_identity(dat_path: str) -> dict:
    """The fit as the result names it: file name, size and mtime (never the folder: the run record keeps no location)."""
    st = stat_source(dat_path)
    return {"name": os.path.basename(dat_path), "size_bytes": int(st.st_size), "mtime_ns": int(st.st_mtime_ns)}
