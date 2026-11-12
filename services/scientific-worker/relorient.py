"""Relative orientation between two orientation tracers (ADR 0047): a map against a point catalog, or a map against a map.

What is computed, and nothing more:
- Both sides are turned into the B-field orientation (ADR 0042: gradient and dust polarization +90 degrees, starlight
  polarization and the B-field itself unrotated) in the map's own pixel frame, with the local north from astropy. A missing
  frame or quantity is a refusal that names it.
- The relative orientation of a sample is Δψ = ψ_map - ψ_other folded into [-90, 90) degrees (axial: 1 and 179 degrees are 2
  apart); |Δψ| is in [0, 90].
- AM = <cos 2Δψ>: 1 parallel, 0 no preferred relative orientation, -1 perpendicular. Weighted AM = Σ w cos 2Δψ / Σ w.
- The projected Rayleigh statistic (Jow et al. 2018, MNRAS 474, 1018, eqs. 6 and 8): V = Σ w cos 2Δψ / sqrt(Σ w² / 2), which
  is standard normal under uniform independent angles. Map pixels are not independent, so V is never turned into a p-value.
- A chance level from a shifted-pattern null, values kept and positions moved (see null_catalog and null_maps).

These are agreement statistics between two tracers. Neither side is a reference truth, and no number here says a method is
right, validated or successful (AGENTS.md invariant 9).

Determinism: every random draw comes from numpy.random.Generator(PCG64(seed)) with the seed recorded in the result; the same
inputs and seed give the same numbers.
"""
from __future__ import annotations

import csv
import math
import os
import tempfile

import numpy as np

import orientation as ori

DEGENERATE_RESULTANT = ori.DEGENERATE_RESULTANT
MAX_SHIFT_ATTEMPTS = 10_000          # per null shift; a minimum shift that can (almost) never be drawn is refused, not looped on
MAX_SEGMENTS = 5000                   # star segments kept in the summary for the figure (the table has every row)
MAX_TABLE_ROWS = 1_000_000            # per-pixel rows written for a map comparison; larger maps keep the FITS of |Δψ| only
MAX_DISK_WORK = 60_000_000            # pixels gathered per vectorised disk step (memory bound, not a result limit)
BOOT_SAMPLES = 2000                   # bootstrap resamples (star_vgt_scale_analysis.py:82)
SNR_DEFAULT = 3.0                     # P/σP >= 3: `keep = p / sp >= min_snr`, min_snr=3.0 (vgt_phase_vs_stars.py:83,87; main passes 3.0 for LMC, 0.0 otherwise, :141)
SIGMA_FLOOR_DEG = 1.0                 # catalog weights 1/max(σ, 1°)² (star_vgt_scale_analysis.py:269)
GAUSS_BEAM = math.pi / (4.0 * math.log(2.0))   # area of a Gaussian beam = GAUSS_BEAM * BMAJ * BMIN (1.1331)

LABEL = "agreement statistic"


class Refusal(Exception):
    """A controlled refusal; the message says what is missing or why."""


# ------------------------------------------------------------------------------------------------------------- statistics

def headless_deg(d):
    """Axial difference folded into [-90, 90) degrees (compare_vgt_polarization.py:44-45, in degrees)."""
    return (np.asarray(d, dtype=float) + 90.0) % 180.0 - 90.0


def cos2(d_deg):
    return np.cos(np.radians(2.0 * np.asarray(d_deg, dtype=float)))


def alignment_measure(d_deg, w=None):
    """AM = <cos 2Δψ>, or Σ w cos 2Δψ / Σ w with weights. None when there is no sample (or no positive weight)."""
    d = np.asarray(d_deg, dtype=float)
    ok = np.isfinite(d)
    if w is None:
        return float(np.mean(cos2(d[ok]))) if ok.any() else None
    w = np.asarray(w, dtype=float)
    ok &= np.isfinite(w)
    s = float(w[ok].sum())
    return float((w[ok] * cos2(d[ok])).sum() / s) if ok.any() and s > 0 else None


def projected_rayleigh(d_deg, w=None):
    """V = Σ w cos 2Δψ / sqrt(Σ w² / 2) (Jow et al. 2018 eq. 8; eq. 6 when every w is 1). θ = 2Δψ is the doubled relative angle,
    so positive V means preferentially parallel and negative V preferentially perpendicular."""
    d = np.asarray(d_deg, dtype=float)
    w = np.ones_like(d) if w is None else np.asarray(w, dtype=float)
    ok = np.isfinite(d) & np.isfinite(w)
    den = math.sqrt(float((w[ok] ** 2).sum()) / 2.0)
    return float((w[ok] * cos2(d[ok])).sum() / den) if ok.any() and den > 0 else None


def histogram(abs_deg, bins: int):
    """Counts of |Δψ| in `bins` equal-width bins over [0, 90] degrees (the last bin includes 90)."""
    a = np.asarray(abs_deg, dtype=float)
    a = a[np.isfinite(a)]
    idx = np.minimum((a / (90.0 / bins)).astype(int), bins - 1)
    return np.bincount(idx, minlength=bins)


def bootstrap_am(d_deg, rng, n=BOOT_SAMPLES):
    """16th and 84th percentiles of AM over resamples with replacement (star_vgt_scale_analysis.py:82-87). Assumes independent
    samples."""
    d = np.asarray(d_deg, dtype=float)
    d = d[np.isfinite(d)]
    if d.size < 2:
        return None
    c = cos2(d)
    s = [float(c[rng.integers(0, d.size, d.size)].mean()) for _ in range(n)]
    lo, hi = np.percentile(s, [16, 84])
    return [float(lo), float(hi)]


def null_rng(seed: int):
    """The null's stream: the same as numpy.random.default_rng(seed), which the owner's scripts use."""
    return np.random.Generator(np.random.PCG64(seed))


def side_rng(seed: int, key: int):
    """An independent stream for other draws (bootstrap), so they never change the null's sequence."""
    return np.random.Generator(np.random.PCG64(np.random.SeedSequence(seed, spawn_key=(key,))))


def summarize_null(null: np.ndarray, am: float, n_hist: list, bins: int) -> dict:
    """Chance level and one-sided p from the kept null values (vgt_phase_vs_stars.py:115-117)."""
    out = {"kept": int(null.size), "chance95": None, "p_one_sided": None, "mean": None, "std": None,
           "histogram_band": None}
    if null.size == 0:
        return out
    out["chance95"] = float(np.percentile(null, 95))
    out["p_one_sided"] = float((np.sum(null >= am) + 1) / (null.size + 1))
    out["mean"] = float(null.mean())
    out["std"] = float(null.std())
    h = np.asarray(n_hist, dtype=float)
    lo, med, hi = np.percentile(h, [2.5, 50, 97.5], axis=0)
    out["histogram_band"] = {"lo": lo.tolist(), "median": med.tolist(), "hi": hi.tolist(),
                             "what": "2.5th, 50th and 97.5th percentiles over the kept shifts of the fraction of samples in each bin"}
    return out


# ------------------------------------------------------------------------------------------------------- map preparation

def pixel_scale_deg(w2) -> float:
    """Mean pixel scale at the reference pixel in degrees (vgt_phase_vs_stars.py:50-52)."""
    from astropy.wcs.utils import proj_plane_pixel_scales
    return float(np.mean(proj_plane_pixel_scales(w2)))


def pixel_b_field(w2, theta_deg: np.ndarray, frame, quantity) -> np.ndarray:
    """The B-field orientation in the map's pixel frame (counter-clockwise from +x, degrees in [0, 180)) of a declared axial map.
    A pixel-frame angle is only rotated by its quantity; a sky-frame one is carried into the pixel frame at each pixel by astropy."""
    ori.check_frame(frame)
    rot = ori.b_rotation_deg(quantity)
    t = np.asarray(theta_deg, dtype=float)
    if frame == "pixel":
        return ori.wrap_axial(t + rot)
    out = np.full(t.shape, np.nan)
    ok = np.isfinite(t)
    if ok.any():
        yy, xx = np.nonzero(ok)
        out[ok] = ori.wrap_axial(ori.sky_pa_to_pixel(w2, xx.astype(float), yy.astype(float), t[ok] + rot))
    return out


class Disks:
    """Axial means in disks of a fixed radius on the sky, centred on the pixel nearest a position (vgt_phase_vs_stars.py:57).

    Membership is by angular distance between pixel centres (unit vectors from astropy's pixel_to_world), not pixel distance.
    The search box is (2 ri + 1) pixels wide with ri = ceil(radius / mean pixel scale), and a disk whose box leaves the map is
    not used (vgt_phase_vs_stars.py:58-60). A disk with fewer than `min_pixels` usable pixels is not used (line 64).
    Pixels count when their angle is finite and their weight (the declared reliability, or 1) is finite; the mean is the
    weighted doubled-angle mean θ = ½ atan2(Σ w sin 2φ, Σ w cos 2φ) with resultant R = |Σ w e^{2iφ}| / Σ w.
    """

    def __init__(self, w2, phi_pix_deg: np.ndarray, weight: np.ndarray, radius_deg: float, min_pixels: int):
        ny, nx = phi_pix_deg.shape
        self.nx, self.ny = nx, ny
        self.ri = int(math.ceil(radius_deg / pixel_scale_deg(w2)))
        self.cos_r = math.cos(math.radians(radius_deg))
        self.min_pixels = min_pixels
        yy, xx = np.mgrid[0:ny, 0:nx]
        xyz = w2.pixel_to_world(xx.ravel(), yy.ravel()).cartesian.xyz.value       # (3, ny*nx), unit vectors
        self.xyz = np.ascontiguousarray(xyz.reshape(3, ny, nx))
        ok = np.isfinite(phi_pix_deg) & np.isfinite(weight)
        t2 = np.radians(2.0 * np.where(ok, phi_pix_deg, 0.0))
        wv = np.where(ok, weight, 0.0)
        self.C, self.S, self.W = wv * np.cos(t2), wv * np.sin(t2), wv
        self.F = ok.astype(np.float64)
        oy, ox = np.mgrid[-self.ri:self.ri + 1, -self.ri:self.ri + 1]
        self.oy, self.ox = oy.ravel(), ox.ravel()
        self.border = (np.abs(self.oy) == self.ri) | (np.abs(self.ox) == self.ri)

    def sample(self, x, y):
        """For positions (x, y) in 0-based pixels: (θ degrees, usable pixels, R, status, box_truncated). Status: "" used,
        "edge", "few", "no_weight", "cancel"."""
        x = np.asarray(x, dtype=float)
        y = np.asarray(y, dtype=float)
        n = x.size
        theta = np.full(n, np.nan)
        npx = np.zeros(n, dtype=int)
        R = np.full(n, np.nan)
        status = np.full(n, "edge", dtype=object)
        trunc = np.zeros(n, dtype=bool)
        fin = np.isfinite(x) & np.isfinite(y)
        xi = np.where(fin, np.rint(np.where(fin, x, 0)), -10 ** 9).astype(np.int64)
        yi = np.where(fin, np.rint(np.where(fin, y, 0)), -10 ** 9).astype(np.int64)
        ri = self.ri
        inside = fin & (xi - ri >= 0) & (yi - ri >= 0) & (xi + ri < self.nx) & (yi + ri < self.ny)
        idx = np.flatnonzero(inside)
        step = max(1, MAX_DISK_WORK // self.oy.size)
        for k0 in range(0, idx.size, step):
            k = idx[k0:k0 + step]
            cy, cx = yi[k], xi[k]
            by = cy[:, None] + self.oy[None, :]
            bx = cx[:, None] + self.ox[None, :]
            c = self.xyz[:, cy, cx]                                            # (3, m)
            dot = (self.xyz[0][by, bx] * c[0][:, None] + self.xyz[1][by, bx] * c[1][:, None] + self.xyz[2][by, bx] * c[2][:, None])
            m = dot >= self.cos_r
            trunc[k] = (m & self.border[None, :]).any(axis=1)
            cs = np.where(m, self.C[by, bx], 0.0).sum(axis=1)
            ss = np.where(m, self.S[by, bx], 0.0).sum(axis=1)
            ws = np.where(m, self.W[by, bx], 0.0).sum(axis=1)
            nn = np.where(m, self.F[by, bx], 0.0).sum(axis=1).astype(int)
            npx[k] = nn
            with np.errstate(all="ignore"):
                rr = np.where(ws > 0, np.hypot(cs, ss) / np.where(ws > 0, ws, 1.0), np.nan)
            st = np.where(nn < self.min_pixels, "few", np.where(~(ws > 0), "no_weight", np.where(rr < DEGENERATE_RESULTANT, "cancel", "")))
            status[k] = st
            R[k] = rr
            good = st == ""
            theta[k[good]] = ori.wrap_axial(0.5 * np.degrees(np.arctan2(ss[good], cs[good])))
        return theta, npx, R, status, trunc


# ------------------------------------------------------------------------------------------------------- nulls

def null_catalog(disks: Disks, x, y, phi_star_pix, am_real: float, n_real: int, *, n_shifts: int, min_shift_px: float,
                 keep: float, rng, bins: int):
    """The shifted-pattern null of vgt_phase_vs_stars.py:97-117, ported step by step.

    The whole star pattern moves by (dx, dy), each uniform in (-nx, nx) and (-ny, ny), redrawn until hypot(dx, dy) is at least
    the minimum shift; positions wrap modulo the map (x + dx) % nx; each star keeps its pixel-frame angle from its true
    position. A shift is kept when at least n_sub = int(keep * n_real) shifted stars have a usable disk; it is then scored on
    exactly n_sub of them drawn without replacement. Rejected shifts are not redrawn. Every star with a position and an angle
    takes part, including stars outside the map at their true position (the wrap brings them in), as in the script.
    The length test is on the unwrapped offset, so an offset near ±nx is close to no shift after wrapping; the number of kept
    shifts whose wrapped displacement is below the minimum is reported, not changed."""
    nx, ny = disks.nx, disks.ny
    n_sub = int(keep * n_real)
    null, hists = [], []
    attempts = wrapped_short = 0
    for _ in range(n_shifts):
        tries = 0
        while True:
            dx, dy = rng.uniform(-nx, nx), rng.uniform(-ny, ny)
            tries += 1
            if np.hypot(dx, dy) >= min_shift_px:
                break
            if tries >= MAX_SHIFT_ATTEMPTS:
                raise Refusal("the minimum shift is too large for this map: almost no random offset is that long; lower it")
        attempts += 1
        xs, ys = (x + dx) % nx, (y + dy) % ny
        vs, _, _, st, _ = disks.sample(xs, ys)
        ds = headless_deg(vs - phi_star_pix)
        oks = np.isfinite(ds)
        if oks.sum() >= n_sub:
            pick = rng.choice(np.where(oks)[0], n_sub, replace=False)
            null.append(float(np.mean(cos2(ds[pick]))))
            hists.append(histogram(np.abs(ds[pick]), bins) / n_sub)
            wdx = (dx + nx / 2.0) % nx - nx / 2.0
            wdy = (dy + ny / 2.0) % ny - ny / 2.0
            if np.hypot(wdx, wdy) < min_shift_px:
                wrapped_short += 1
    out = summarize_null(np.asarray(null), am_real, hists, bins)
    out.update(attempted=attempts, n_sub=n_sub, wrapped_below_min_shift=wrapped_short)
    return out


def null_maps(ca, sa, cb, sb, valid_a, valid_b, am_real: float, n_real: int, *, n_shifts: int, min_shift_px: float,
              keep: float, rng, bins: int):
    """Shifted-map null: the second map moves by a whole number of pixels (dx, dy) with wrap (numpy.roll), values kept; the first
    stays. Offsets are drawn as in the catalog null (uniform in (-nx, nx) and (-ny, ny)) and rounded to whole pixels, but the
    length test is on the wrapped displacement, so a near-identity shift is never accepted. A shift is kept when the overlap of
    usable pixels is at least n_sub = int(keep * n_real), and is scored on every overlap pixel. Unlike the catalog null it does not
    draw exactly n_sub of them: with thousands of correlated pixels the overlap changes the spread of the null by a negligible
    amount, and drawing millions of pixels per shift would make the null many times slower (measured on a 2048 x 2048 map:
    about 0.6 s per shift with the draw, under 0.05 s without)."""
    ny, nx = valid_a.shape
    if min_shift_px >= math.hypot(nx / 2.0, ny / 2.0):
        raise Refusal("the minimum shift is longer than any shift of this map can be after wrapping; lower it")
    n_sub = int(keep * n_real)
    null, hists = [], []
    attempts = 0
    # |Δψ| bin edges as values of cos 2|Δψ|, which decreases from 1 (0°) to -1 (90°): a sample falls in bin j when it is below j
    # inner edges. This avoids an arccos per pixel.
    neg_edges = -np.cos(np.radians(2.0 * 90.0 * np.arange(1, bins) / bins))
    ca, sa, cb, sb = (np.where(v, a, 0.0).astype(np.float32) for a, v in ((ca, valid_a), (sa, valid_a), (cb, valid_b), (sb, valid_b)))
    for _ in range(n_shifts):
        tries = 0
        while True:
            dx, dy = int(np.rint(rng.uniform(-nx, nx))), int(np.rint(rng.uniform(-ny, ny)))
            wdx = (dx + nx // 2) % nx - nx // 2
            wdy = (dy + ny // 2) % ny - ny // 2
            tries += 1
            if math.hypot(wdx, wdy) >= min_shift_px:
                break
            if tries >= MAX_SHIFT_ATTEMPTS:
                raise Refusal("the minimum shift is too large for this map; lower it")
        attempts += 1
        ok = valid_a & np.roll(valid_b, (wdy, wdx), axis=(0, 1))
        cnt = int(ok.sum())
        if cnt >= n_sub and n_sub > 0:
            c = (ca * np.roll(cb, (wdy, wdx), axis=(0, 1)) + sa * np.roll(sb, (wdy, wdx), axis=(0, 1)))[ok].astype(np.float64)   # cos 2(φa - φb)
            null.append(float(c.mean()))
            hists.append(np.bincount(np.searchsorted(neg_edges, -c, side="left"), minlength=bins) / cnt)
    out = summarize_null(np.asarray(null), am_real, hists, bins)
    out.update(attempted=attempts, n_sub=n_sub, scored_on="every pixel of the overlap (at least n_sub)")
    return out


# ------------------------------------------------------------------------------------------------------- output helpers

def _f(v, nd=6):
    if v is None:
        return ""
    v = float(v)
    return "" if not math.isfinite(v) else f"{v:.{nd}f}"


def write_csv(path: str, header: list, rows) -> int:
    """Write rows atomically (temporary file, then rename). Missing values are empty cells, never 0."""
    d = os.path.dirname(os.path.abspath(path))
    os.makedirs(d, exist_ok=True)
    fd, tmp = tempfile.mkstemp(dir=d, suffix=".tmp")
    n = 0
    try:
        with os.fdopen(fd, "w", newline="") as fh:
            w = csv.writer(fh)
            w.writerow(header)
            for r in rows:
                w.writerow(r)
                n += 1
        os.replace(tmp, path)
    finally:
        if os.path.exists(tmp):
            os.unlink(tmp)
    return n


def histogram_block(abs_used, bins: int) -> dict:
    counts = histogram(abs_used, bins)
    total = int(counts.sum())
    edges = [90.0 * i / bins for i in range(bins + 1)]
    return {"edges_deg": edges, "counts": counts.tolist(), "fractions": (counts / total).tolist() if total else [0.0] * bins,
            "bins": "equal-width bins of |Δψ| over [0, 90] degrees; the number of bins is a parameter, every bin is the same width"}


def check_params(p: dict, mode: str) -> dict:
    """Validated parameters, each with its default (ADR 0047)."""
    def num(key, default, lo, hi, integer=False):
        v = p.get(key, default)
        if v is None:
            v = default
        try:
            v = int(v) if integer else float(v)
        except (TypeError, ValueError):
            raise Refusal(f"{key} must be a number")
        if integer and float(p.get(key, default)) != v:
            raise Refusal(f"{key} must be a whole number")
        if not (lo <= v <= hi) or (not integer and not math.isfinite(v)):
            raise Refusal(f"{key} must be between {lo} and {hi}")
        return v
    out = {"n_shifts": num("n_shifts", 3000 if mode == "catalog" else 1000, 0, 20000, True),
           "min_shift_deg": num("min_shift_deg", 1.0, 0.0, 180.0),
           "keep_fraction": num("keep_fraction", 0.9, 0.05, 1.0),
           "seed": num("seed", 20261020, 0, 2 ** 63 - 1, True),
           "bins": num("bins", 9, 2, 90, True)}
    if mode == "catalog":
        out["radius_arcmin"] = num("radius_arcmin", 10.0, 1e-3, 600.0)
        out["min_pixels"] = num("min_pixels", 5, 1, 10 ** 6, True)
        # Optional row filter P/σP >= k, off unless both columns are chosen; the three keys are recorded only when it is on.
        cols = [p.get(k) for k in ("snr_column", "snr_sigma_column")]
        if any(c is not None and c != "" for c in cols) or p.get("snr_min") is not None:
            if not all(isinstance(c, str) and c for c in cols):
                raise Refusal("the P/σP filter needs both columns: choose the column holding P and the column holding σP")
            if cols[0] == cols[1]:
                raise Refusal("the P/σP filter needs two different columns")
            out["snr_column"], out["snr_sigma_column"] = cols
            out["snr_min"] = num("snr_min", SNR_DEFAULT, 0.0, 1e6)
    return out


# ------------------------------------------------------------------------------------------------------- the two operations

REASONS = {
    "position": "position missing or unreadable in the catalog",
    "angle": "angle missing or unreadable in the catalog",
    "no_pixel": "the position has no pixel coordinates in this map's projection",
    "outside": "outside the map",
    "edge": "the disk crosses the map edge",
    "few": "too few pixels with a value in the disk",
    "no_weight": "every reliability weight in the disk is zero",
    "cancel": "the orientations in the disk cancel (no mean orientation)",
    "snr_missing": "P or σP missing, unreadable, or σP not positive (the P/σP filter needs both)",
    "snr": "P/σP below the chosen threshold",
}

DEFINITIONS = {
    "delta_psi": "Δψ = ψ_map − ψ_other, both as B-field orientations, folded into [−90°, 90°); |Δψ| in [0°, 90°]. 1° and 179° are 2° apart.",
    "am": "alignment measure AM = mean of cos 2Δψ over the samples used: 1 parallel, 0 no preferred relative orientation, −1 perpendicular.",
    "am_weighted": "Σ w cos 2Δψ / Σ w with the weights named under weighting.",
    "chance95": "95th percentile of AM over the kept shifts of the null.",
    "p_one_sided": "(number of kept shifts with AM ≥ the observed AM + 1) / (kept shifts + 1).",
}


def _undeclared(fn, *a, what=""):
    try:
        return fn(*a)
    except ori.Undeclared as e:
        raise Refusal(f"{what}{e}") from None


def _frame_name(f) -> str:
    name = f.name
    eq = getattr(f, "equinox", None)
    return f"{name} ({eq.value})" if eq is not None and name in ("fk5", "fk4") else name


def _map_side(args, cube, prefix=""):
    """(pixel-frame B angle, weight, wcs, declaration facts) of a declared axial map."""
    from analysis import axial_inputs
    unit = getattr(args, f"{prefix}axial_unit")
    which = "the second map: " if prefix else "the map: "
    if not unit:
        raise Refusal(f"{which}it is not declared as an axial orientation map")
    w2 = _undeclared(ori.celestial_wcs, cube, what=which)
    ns = type("A", (), {"axial_unit": unit, "weights": getattr(args, f"{prefix}weights")})
    theta, weight = axial_inputs(ns, cube)
    frame, quantity = getattr(args, f"{prefix}axial_frame"), getattr(args, f"{prefix}axial_quantity")
    phi = _undeclared(pixel_b_field, w2, theta, frame, quantity, what=which)
    facts = {"unit": unit, "frame": frame, "quantity": quantity, "rotation_to_b_deg": ori.QUANTITY_ROTATION[quantity],
             "celestial_frame": _frame_name(_undeclared(ori.sky_frame, w2, what=which)), "reliability": bool(getattr(args, f"{prefix}weights"))}
    return phi, weight, w2, facts


def op_catalog(args, params: dict) -> dict:
    """Map against a point catalog (mode A)."""
    import json as _json
    from analysis import open_cube
    from plane_worker import authorize
    import catalog_table as ct
    from astropy.coordinates import SkyCoord
    import astropy.units as u

    p = check_params(params, "catalog")
    cube = open_cube(args.input)
    phi, weight, w2, mfacts = _map_side(args, cube)
    ny, nx = phi.shape
    if not args.catalog or not args.declaration:
        raise Refusal("a catalog and its declaration are required")
    try:
        L = ct.load(authorize(args.catalog))
        decl = ct.validate_declaration(_json.loads(args.declaration), L)
    except ct.Bad as e:
        raise Refusal(f"the catalog: {e}") from None
    if not decl["angle"]:
        raise Refusal("the catalog has no angle column declared: declare which column is the position angle, its unit and what it measures")
    q = decl["angle"]["quantity"]
    reasons = dict(REASONS)
    snr_filter = None
    if "snr_column" in p:
        for c in (p["snr_column"], p["snr_sigma_column"]):
            if c not in decl["values"]:
                raise Refusal(f"the P/σP filter: {c!r} is not one of the catalog's declared value columns")
        reasons["snr"] = f"P/σP below {p['snr_min']:g}"
    rot = _undeclared(ori.b_rotation_deg, q, what="the catalog's angle: ")
    try:
        conv = ct.convert(L, decl)
    except ct.Bad as e:
        raise Refusal(f"the catalog: {e}") from None
    n = len(L.table)
    lon, lat, ang = conv.lon_deg, conv.lat_deg, conv.angle_deg
    sigma = conv.sigma_deg
    reason = np.full(n, "", dtype=object)
    reason[~np.isfinite(lon)] = "position"
    reason[(reason == "") & ~np.isfinite(ang)] = "angle"
    if "snr_column" in p:
        # The script drops these rows first, so they also leave the shifted pattern (vgt_phase_vs_stars.py:87-88).
        # A missing or unreadable P or σP is its own reason and is never read as 0; σP <= 0 has no ratio.
        pv = ct.number_field(L, p["snr_column"], "").values
        sv = ct.number_field(L, p["snr_sigma_column"], "").values
        todo = reason == ""
        _, st = ct.snr_filter(pv, sv, p["snr_min"])      # the one shared P/σP rule (catalog_table.snr_filter)
        reason[todo & (st == "missing")] = "snr_missing"
        reason[todo & (st == "below")] = "snr"
        snr_filter = {"p_column": p["snr_column"], "sigma_column": p["snr_sigma_column"], "min": p["snr_min"],
                      "rule": f"keep a row when P/σP >= {p['snr_min']:g} (the script's `keep = p / sp >= min_snr`, vgt_phase_vs_stars.py:87)",
                      "excluded_below": int((reason == "snr").sum()), "excluded_missing": int((reason == "snr_missing").sum())}
    have = reason == ""
    x = np.full(n, np.nan)
    y = np.full(n, np.nan)
    lon_m = np.full(n, np.nan)
    lat_m = np.full(n, np.nan)
    star_sky = np.full(n, np.nan)
    star_pix = np.full(n, np.nan)
    mframe = ori.sky_frame(w2)
    if have.any():
        sc = SkyCoord(lon[have] * u.deg, lat[have] * u.deg, frame=ct.astropy_frame(decl["frame"], decl["equinox"]))
        with np.errstate(all="ignore"):
            xx, yy = w2.world_to_pixel(sc)
        x[have], y[have] = xx, yy
        tm = sc.transform_to(mframe).spherical
        lon_m[have], lat_m[have] = tm.lon.deg % 360.0, tm.lat.deg
        star_sky[have] = ori.wrap_axial(ori.rotate_pa_frame(sc, ang[have] + rot, mframe))
        with np.errstate(all="ignore"):
            star_pix[have] = ori.wrap_axial(ori.sky_pa_to_pixel(w2, x[have], y[have], star_sky[have]))
    reason[have & ~(np.isfinite(x) & np.isfinite(y) & np.isfinite(star_pix))] = "no_pixel"
    placed = reason == ""
    inmap = placed & (x >= -0.5) & (x < nx - 0.5) & (y >= -0.5) & (y < ny - 0.5)
    reason[placed & ~inmap] = "outside"
    radius_deg = p["radius_arcmin"] / 60.0
    disks = Disks(w2, phi, weight, radius_deg, p["min_pixels"])
    v = np.full(n, np.nan)
    npx = np.zeros(n, dtype=int)
    R = np.full(n, np.nan)
    trunc = np.zeros(n, dtype=bool)
    k = np.flatnonzero(inmap)
    if k.size:
        vk, nk, rk, sk, tk = disks.sample(x[k], y[k])
        v[k], npx[k], R[k], trunc[k] = vk, nk, rk, tk
        reason[k] = sk
    used = reason == ""
    d = np.where(used, headless_deg(v - star_pix), np.nan)
    n_real = int(used.sum())
    counts = {key: int((reason == key).sum()) for key in reasons}
    if n_real == 0:
        why = "; ".join(f"{reasons[kk]}: {cnt}" for kk, cnt in counts.items() if cnt)
        raise Refusal(f"no source could be compared ({why or 'the catalog is empty'})")
    am = alignment_measure(d[used])
    wts = None
    weighting_star = "none: no uncertainty column is declared for the catalog angle"
    if sigma is not None:
        wts = 1.0 / np.radians(np.maximum(sigma, SIGMA_FLOOR_DEG)) ** 2
        weighting_star = (f"weighted AM uses w = 1 / max(σ, {SIGMA_FLOOR_DEG:g}°)² (σ in radians) from the declared uncertainty column "
                          f"{decl['angle']['sigmaColumn']!r}, as star_vgt_scale_analysis.py:269; a source with no σ is left out of the weighted AM only")
    am_w = alignment_measure(d[used], None if wts is None else wts[used])
    absd = np.abs(d)
    hist = histogram_block(absd[used], p["bins"])
    # The null shifts every source with a pixel position and an angle, as the script does (it wraps sources outside the map in).
    part = placed
    nul = {"method": "shifted source pattern, values kept (vgt_phase_vs_stars.py:97-117)", "skipped": None}
    n_sub = int(p["keep_fraction"] * n_real)
    min_shift_px = p["min_shift_deg"] / pixel_scale_deg(w2)
    if p["n_shifts"] == 0:
        nul["skipped"] = "no shifts were asked for"
    elif n_sub < 2:
        nul["skipped"] = f"too few sources ({n_real}) for a null that keeps {p['keep_fraction']:g} of them"
    elif min_shift_px >= math.hypot(nx, ny):
        raise Refusal(f"the minimum shift ({p['min_shift_deg']:g}°) is longer than the map's diagonal; lower it")
    else:
        nul.update(null_catalog(disks, x[part], y[part], star_pix[part], am, n_real, n_shifts=p["n_shifts"], min_shift_px=min_shift_px,
                                keep=p["keep_fraction"], rng=null_rng(p["seed"]), bins=p["bins"]))
        nul["sources_shifted"] = int(part.sum())
    boot = bootstrap_am(d[used], side_rng(p["seed"], 1))
    map_sky = np.full(n, np.nan)
    map_sky[used] = ori.wrap_axial(ori.pixel_to_sky_pa(w2, x[used], y[used], v[used]))
    dsky = np.where(used, headless_deg(map_sky - star_sky), np.nan)
    header = ["row", "lon_deg", "lat_deg", "frame", "lon_map_deg", "lat_map_deg", "x_fits", "y_fits", "catalog_b_deg", "map_b_deg",
              "dpsi_deg", "abs_dpsi_deg", "cos_2dpsi", "disk_pixels", "disk_resultant", "weight", "used", "reason"]
    frame_txt = decl["frame"] + (f" {decl['equinox']}" if decl["equinox"] else "")

    def rows():
        for i in range(n):
            yield [i + 1, _f(lon[i]), _f(lat[i]), frame_txt, _f(lon_m[i]), _f(lat_m[i]), _f(x[i] + 1, 3), _f(y[i] + 1, 3),
                   _f(star_sky[i], 3), _f(map_sky[i], 3), _f(dsky[i], 3), _f(absd[i], 3), _f(cos2(d[i]) if used[i] else None),
                   int(npx[i]), _f(R[i], 4), _f(None if wts is None else wts[i], 4), 1 if used[i] else 0, reasons.get(reason[i], "")]
    write_csv(args.out, header, rows())
    seg_idx = np.flatnonzero(inmap)[:MAX_SEGMENTS]
    summary = {
        "label": LABEL, "mode": "map_catalog", "definitions": DEFINITIONS, "parameters": p,
        "map": {**mfacts, "nx": nx, "ny": ny, "pixel_scale_arcmin": pixel_scale_deg(w2) * 60.0, "compass": ori.compass(w2, (nx - 1) / 2, (ny - 1) / 2)},
        "catalog": {"frame": frame_txt, "quantity": q, "rotation_to_b_deg": rot, "rows": n, "uncertainty_column": decl["angle"]["sigmaColumn"]},
        "snr_filter": snr_filter,
        "conversion": f"Both sides as B-field orientation. The catalog position angle (east of north in {frame_txt}) is rotated by {rot:g}° for its quantity, "
                      f"carried into the map's celestial frame ({mfacts['celestial_frame']}) at each position and then into the map's pixel frame with astropy; "
                      "the map is rotated by its quantity and, if declared on the sky, carried into its pixel frame. Δψ is taken in the map's pixel frame; "
                      "the table also gives both angles east of north in the map's celestial frame.",
        "sampling": f"The map's orientation at a source is the doubled-angle mean over the pixels whose centres lie within {p['radius_arcmin']:g} arcmin "
                    "(angular distance) of the centre of the pixel nearest the source; a disk whose search box leaves the map, or with fewer than "
                    f"{p['min_pixels']} usable pixels, is not used. NaN pixels are left out.",
        "weighting": {"disk": "reliability-weighted: each pixel by the declared reliability map (a NaN reliability leaves the pixel out)" if mfacts["reliability"]
                      else "unweighted: no reliability map is declared for the map", "sources": weighting_star},
        "counts": {"rows": n, "used": n_real, "excluded": {reasons[kk]: cnt for kk, cnt in counts.items() if cnt},
                   "share_used": n_real / n if n else None, "disks_truncated_by_search_box": int((trunc & used).sum())},
        "am": am, "am_weighted": am_w if wts is not None else None,
        "median_abs_dpsi_deg": float(np.median(absd[used])),
        "bootstrap_am_16_84": boot,
        "bootstrap_note": "16th to 84th percentile of AM over 2000 resamples of the sources with replacement (star_vgt_scale_analysis.py:82-87); it assumes independent sources, which overlapping disks are not",
        "histogram": hist, "null": nul,
        "segments": {"x": [float(x[i]) for i in seg_idx], "y": [float(y[i]) for i in seg_idx],
                     "map_pix_deg": [None if not math.isfinite(v[i]) else float(v[i]) for i in seg_idx],
                     "other_pix_deg": [float(star_pix[i]) for i in seg_idx], "used": [bool(used[i]) for i in seg_idx],
                     "truncated": int(inmap.sum()) > MAX_SEGMENTS,
                     "frame": "0-based pixel coordinates and pixel-frame angles (counter-clockwise from +x) of the map"},
        "table": {"file": os.path.basename(args.out), "rows": n, "columns": header,
                  "note": "x_fits, y_fits are 1-based FITS pixel coordinates in the map; catalog_b_deg and map_b_deg are B-field orientations east of north in the map's celestial frame; dpsi_deg is map minus catalog in that frame"},
        "policy": ("Agreement statistic between two tracers, not a validation of either. Pixels in a disk and overlapping disks are correlated, so the "
                   "bootstrap range is optimistic; the shifted-pattern null keeps the map's own structure. No catalog row was dropped silently: each "
                   "excluded row is counted with its reason, and listed in the table."),
    }
    return {"ok": True, "op": "relorient_catalog", "summary": summary}


def beam_count(cubes, n_used: int, pix_area_deg2: float) -> dict:
    """Number of beams in the samples used: n × pixel area / beam area with the larger of the maps' declared Gaussian beams
    (BMAJ, BMIN in degrees), as fig_fg_template_planck.py:102,110. An approximation, offered only when both maps declare a beam."""
    areas = []
    for c, name in cubes:
        bmaj, bmin = c.h.get("BMAJ"), c.h.get("BMIN")
        if not all(isinstance(b, (int, float)) and not isinstance(b, bool) and math.isfinite(b) and b > 0 for b in (bmaj, bmin)):
            return {"value": None, "reason": f"{name} declares no beam (BMAJ and BMIN), so no count of independent beams is offered"}
        areas.append(GAUSS_BEAM * float(bmaj) * float(bmin))
    beam = max(areas)
    return {"value": n_used * pix_area_deg2 / beam, "beam_area_arcmin2": beam * 3600.0,
            "method": "pixels used × pixel area / area of the larger declared Gaussian beam (π/(4 ln 2) BMAJ BMIN), as fig_fg_template_planck.py:102,110",
            "caveat": "An approximation: it ignores correlated noise, and a VGT map is correlated over its sub-block window, which is usually larger "
                      "than the beam, so it overstates the number of independent samples."}


def op_maps(args, params: dict) -> dict:
    """Map against map on a common grid (mode B)."""
    from analysis import open_cube, image_header, write_fits, mask_policy
    p = check_params(params, "maps")
    a = open_cube(args.input)
    b = open_cube(args.reference)
    if a.channels != 1 or b.channels != 1 or (a.shape[a.y], a.shape[a.x]) != (b.shape[b.y], b.shape[b.x]):
        raise Refusal("both maps must be single planes on the same grid; align one onto the other first (align is axial-aware)")
    phi_a, w_a, wa2, fa = _map_side(args, a)
    phi_b, w_b, wb2, fb = _map_side(args, b, prefix="ref_")
    ny, nx = phi_a.shape
    wgt = w_a * w_b
    d = headless_deg(phi_a - phi_b)
    # Masks of either map (ADR 0055), each with its own policy: an excluded pixel is left out (counted apart from "no orientation"),
    # a kept one is untouched, a weighted one counts with its factor on top of the declared reliability (weights are what this
    # statistic already has). A pixel with no value is never "masked" here: it is already excluded, and zero is an angle.
    ap_a, ap_b = mask_policy(args, a, True), mask_policy(args, b, True, "ref_masks")
    ex_a = np.zeros(phi_a.shape, dtype=bool)
    ex_b = np.zeros(phi_a.shape, dtype=bool)
    mask_weighted = False
    for ap, phi, ex in ((ap_a, phi_a, ex_a), (ap_b, phi_b, ex_b)):
        if ap is not None:
            e, wm = ap.plane(0, phi)
            ex |= e
            wgt = wgt * wm
            mask_weighted = mask_weighted or any(s["policy"] == "weight" for s in ap.specs)
    ex_any = ex_a | ex_b
    used = np.isfinite(d) & np.isfinite(wgt) & ~ex_any
    excluded_by_mask = int((np.isfinite(d) & np.isfinite(wgt) & ex_any).sum())
    for ap in (ap_a, ap_b):
        if ap is not None:
            ap.__exit__(None, None, None)
    n_real = int(used.sum())
    missing = int((~(np.isfinite(phi_a) & np.isfinite(phi_b))).sum())
    unknown_w = int((np.isfinite(d) & ~np.isfinite(wgt)).sum())
    if n_real == 0:
        raise Refusal("no pixel has an orientation in both maps")
    weighted = fa["reliability"] or fb["reliability"] or mask_weighted
    dv = d[used]
    am = alignment_measure(dv)
    absd = np.abs(d)
    pix_area = abs(float(np.linalg.det(wa2.pixel_scale_matrix)))
    va = np.isfinite(phi_a) & np.isfinite(w_a) & ~ex_a
    vb = np.isfinite(phi_b) & np.isfinite(w_b) & ~ex_b
    nul = {"method": "shifted map: the second map moved by whole pixels with wrap, values kept", "skipped": None}
    n_sub = int(p["keep_fraction"] * n_real)
    min_shift_px = p["min_shift_deg"] / pixel_scale_deg(wa2)
    if p["n_shifts"] == 0:
        nul["skipped"] = "no shifts were asked for"
    elif n_sub < 2:
        nul["skipped"] = f"too few pixels ({n_real}) for a null"
    else:
        ta, tb = np.radians(2.0 * np.where(va, phi_a, 0.0)), np.radians(2.0 * np.where(vb, phi_b, 0.0))
        nul.update(null_maps(np.cos(ta), np.sin(ta), np.cos(tb), np.sin(tb), va, vb, am, n_real, n_shifts=p["n_shifts"],
                             min_shift_px=min_shift_px, keep=p["keep_fraction"], rng=null_rng(p["seed"]), bins=p["bins"]))
    h = image_header(a, {"BUNIT": "deg", "VESQOP": "relorient_maps"})
    h["HISTORY"] = "Vesqira relative orientation |psi_1 - psi_2| of two B-field orientation maps, axial, in [0, 90] degrees"
    write_fits(args.out, np.where(used, absd, np.nan).astype("<f4"), h)
    table = {"file": None, "rows": n_real, "reason": None}
    if args.table_out:
        if n_real > MAX_TABLE_ROWS:
            table["reason"] = f"{n_real} pixels is more than the {MAX_TABLE_ROWS} rows a table is written for; the FITS map holds every |Δψ|"
        else:
            yy, xx = np.nonzero(used)
            sky = wa2.pixel_to_world(xx, yy).spherical
            sa = ori.wrap_axial(ori.pixel_to_sky_pa(wa2, xx.astype(float), yy.astype(float), phi_a[used]))
            sb = ori.wrap_axial(ori.pixel_to_sky_pa(wa2, xx.astype(float), yy.astype(float), phi_b[used]))
            ds = headless_deg(sa - sb)
            header = ["x_fits", "y_fits", "lon_deg", "lat_deg", "map_b_deg", "other_b_deg", "dpsi_deg", "abs_dpsi_deg", "weight"]
            lo, la, wu, au = sky.lon.deg % 360.0, sky.lat.deg, wgt[used], absd[used]
            write_csv(args.table_out, header, ([int(xx[i]) + 1, int(yy[i]) + 1, _f(lo[i]), _f(la[i]), _f(sa[i], 3), _f(sb[i], 3), _f(ds[i], 3),
                                                _f(au[i], 3), _f(wu[i], 4)] for i in range(n_real)))
            table.update(file=os.path.basename(args.table_out), columns=header,
                         note="1-based FITS pixels; lon, lat in the map's celestial frame; both angles are B-field orientations east of north there; dpsi_deg is first minus second")
    summary = {
        "label": LABEL, "mode": "map_map",
        "definitions": {**DEFINITIONS, "prs": "projected Rayleigh statistic V = Σ w cos 2Δψ / sqrt(Σ w² / 2) (Jow et al. 2018, MNRAS 474, 1018, eq. 8; "
                                              "eq. 6 when every w is 1; written Z_x there, the V statistic of Durand & Greenwood 1958). Positive means preferentially parallel, negative perpendicular."},
        "parameters": p,
        "map": {**fa, "nx": nx, "ny": ny, "pixel_scale_arcmin": pixel_scale_deg(wa2) * 60.0, "compass": ori.compass(wa2, (nx - 1) / 2, (ny - 1) / 2)}, "other": fb,
        "conversion": "Both maps as B-field orientation in their common pixel frame (each rotated by its quantity; a sky-frame map carried into the "
                      "pixel frame with astropy); per-pixel Δψ = first − second.",
        "weighting": (("w = product of the declared reliabilities (1 for a map without one)" + (" and the factors of the masks applied with the weight policy" if mask_weighted else "")
                       + "; a pixel with an unknown (NaN) reliability is left out")
                      if weighted else "unweighted: neither map declares a reliability map"),
        "counts": {"pixels": nx * ny, "used": n_real, "share_used": n_real / (nx * ny),
                   "excluded": {k: c for k, c in (("no orientation in one or both maps", missing), ("unknown reliability", unknown_w),
                                                  ("excluded by a mask", excluded_by_mask)) if c}},
        "masks": {"map": ap_a.record() if ap_a else [], "other": ap_b.record() if ap_b else []},
        "am": am, "am_weighted": alignment_measure(dv, wgt[used]) if weighted else None,
        "median_abs_dpsi_deg": float(np.median(absd[used])),
        "prs": {"V": projected_rayleigh(dv), "V_weighted": projected_rayleigh(dv, wgt[used]) if weighted else None, "n": n_real,
                "note": "V is standard normal only for independent angles. Neighbouring pixels are correlated over the beam and, for VGT, over its "
                        "window, so V overstates significance and no p-value is given for it; the shifted-map null gives the chance level."},
        "independent_samples": beam_count([(a, "the first map"), (b, "the second map")], n_real, pix_area),
        "histogram": histogram_block(absd[used], p["bins"]), "null": nul, "table": table,
        "policy": ("Agreement statistic between two tracers, not a validation of either. The maps share a grid; their resolutions are not matched "
                   "here (convolve first if they differ). The FITS result holds |Δψ| in degrees, NaN where a pixel was not used."),
    }
    return {"ok": True, "op": "relorient_maps", "summary": summary}
