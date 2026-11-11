"""Channel maps and position-velocity diagrams (ADR 0049): views that cut a cube along its spectral axis.

- slab: the per-pixel mean of channels first..last of one plane (NaN, BLANK and infinite values excluded and counted, a pixel
  with no finite value is NaN, never 0), optionally reduced to a block-mean preview afterwards (slab mean first, then block mean).
- channel_grid: the plan of an N x M grid of panels (which channels each shows, its spectral label through the readout
  conversions of ADR 0040) and the ONE colour scale all panels share, as numbers.
- pv: a position-velocity slice. The cube is sampled along a polyline at about one-pixel spacing, with several samples across the
  stated width averaged (non-finite samples excluded and counted), for every channel of a range. The offset of each sample is the
  cumulative great-circle separation (astropy) of the sky positions of the samples along the path, never a pixel count times a scale.

Everything reads through Cube.block_reader (memory-mapped blocks, bounded), never the whole cube. A Refusal carries a code
("bad_request" for a malformed request, "unsupported" for a legitimate request this file or the caps cannot serve) and a sentence.
"""
from __future__ import annotations

import math
import warnings

import numpy as np

import convert

MAX_SLAB_CHANNELS = 256
MAX_SLAB_WORK = 1 << 28                # plane pixels x channels read for one slab
MAX_PANELS = 64
MAX_PANEL_PIXELS = 1 << 20             # one panel as sent (after the block-mean preview)
DEFAULT_PANEL_PIXELS = 1 << 18         # 512 x 512
MAX_GRID_PIXELS = 1 << 24              # every panel together: 64 MiB of float32
MAX_PREVIEW_STEP = 64
SCALE_SAMPLE_PANELS = 6
SCALE_SAMPLE_WORK = 1 << 29            # plane pixels x channels read to choose the shared scale
READ_BLOCK_ELEMENTS = 4_000_000        # pixels x channels per block read

MAX_PV_SAMPLES = 16384                 # positions along the path
MAX_PV_CROSS = 64                      # samples across the width
MAX_PV_CELLS = 1 << 22                 # offsets x channels returned (16 MiB of float32)
MAX_PV_WORK = 1 << 28                  # path samples x cross samples x channels interpolated
MAX_PV_VERTICES = 64
PV_RUN = 128                           # path samples handled per read (keeps the pixels read per run small for any path)
PV_RUN_ELEMENTS = 1 << 21              # gathered values per run and channel block

NAN_MEANING = "no value: NaN, BLANK or infinite in the source; it is not zero"


class Refusal(Exception):
    def __init__(self, code: str, message: str):
        super().__init__(message)
        self.code = code


def _int(req: dict, key: str, lo: int, hi: int, default=None) -> int:
    v = req.get(key, default)
    if isinstance(v, bool) or not isinstance(v, int) or not lo <= v <= hi:
        raise Refusal("bad_request", f"{key} must be an integer in [{lo}, {hi}]")
    return v


def _num(v, what: str) -> float:
    if isinstance(v, bool) or not isinstance(v, (int, float)) or not math.isfinite(v):
        raise Refusal("bad_request", f"{what} must be a finite number")
    return float(v)


def _need_channels(c) -> None:
    if c.chan is None or c.channels < 2:
        raise Refusal("unsupported", "this file has no channel axis of more than one channel")


def _step(req: dict) -> int:
    return _int(req, "step", 1, MAX_PREVIEW_STEP, 1)


# ---------- slab ----------

def slab_mean(c, first: int, last: int) -> tuple[np.ndarray, dict]:
    """(float32 plane, counts): mean of the finite values of channels first..last per pixel at full resolution."""
    ny, nx = c.shape[c.y], c.shape[c.x]
    nch = last - first + 1
    total = np.zeros((ny, nx))
    count = np.zeros((ny, nx), dtype=np.int32)
    block = max(1, READ_BLOCK_ELEMENTS // (ny * nx))
    with c.block_reader() as read:
        for c0 in range(first, last + 1, block):
            v = read(c0, min(last + 1, c0 + block), slice(None), slice(None))
            fin = np.isfinite(v)
            total += np.where(fin, v, 0.0).sum(axis=0)
            count += fin.sum(axis=0, dtype=np.int32)
    with np.errstate(all="ignore"):
        mean = np.where(count > 0, total / np.maximum(count, 1), np.nan)
        out = np.ascontiguousarray(mean, dtype="<f4")
    return out, {"channels": nch, "samples": int(nch * ny * nx), "finite_samples": int(count.sum()),
                 "excluded_samples": int(nch * ny * nx - count.sum()), "pixels_without_value": int(np.count_nonzero(count == 0))}


def slab(c, req: dict, reduce, stats) -> tuple[dict, bytes]:
    _need_channels(c)
    first, last = _int(req, "first", 0, c.channels - 1), _int(req, "last", 0, c.channels - 1)
    if last < first:
        raise Refusal("bad_request", "last must not be before first")
    if last - first + 1 > MAX_SLAB_CHANNELS:
        raise Refusal("unsupported", f"a slab averages at most {MAX_SLAB_CHANNELS} channels")
    ny, nx = c.shape[c.y], c.shape[c.x]
    if (last - first + 1) * ny * nx > MAX_SLAB_WORK:
        raise Refusal("unsupported", "the slab reads too much of the cube; average fewer channels")
    step = _step(req)
    plane, info = slab_mean(c, first, last)
    preview = None
    if step > 1:
        preview = {"step": step, "source_width": nx, "source_height": ny,
                   "method": f"mean of the finite pixels in each {step} x {step} block of the slab mean (NaN where a block has none); a display preview, not for measurement"}
        plane = reduce(plane, step)
    payload = plane.tobytes()
    vals = convert.native_values(c, [first, last]) if _resolved(c) else None
    return {"nbytes": len(payload), "dtype": "<f4", "width": plane.shape[1], "height": plane.shape[0],
            "row0": "FITS y=1 (the bottom row when north is up)", "channel": first, "channels": c.channels,
            "nan_meaning": NAN_MEANING, "stats": stats(plane), "preview": preview,
            "slab": {"first": first, "last": last, **info,
                     "method": f"mean of the finite values of channels {first}-{last} at each pixel (full resolution); non-finite values are excluded and counted, "
                               "a pixel with no finite value is NaN, flagged channels keep their data"},
            "spectral_first": None if vals is None or not math.isfinite(vals[0]) else float(vals[0]),
            "spectral_last": None if vals is None or not math.isfinite(vals[1]) else float(vals[1])}, payload


def _resolved(c) -> bool:
    f = c.spectral_facts()
    return f is not None and f["resolved"]


# ---------- channel grid plan ----------

def panels_of(first: int, last: int, stride: int, average: int) -> list[tuple[int, int]]:
    """[(first channel, last channel)] of each panel. Panels start every `stride` channels from `first`; a panel covers `average`
    channels (fewer at the end of the range, never beyond `last`)."""
    out, s = [], first
    while s <= last:
        out.append((s, min(s + average - 1, last)))
        s += stride
    return out


def channel_grid(c, req: dict, reduce) -> tuple[dict, bytes]:
    _need_channels(c)
    first, last = _int(req, "first", 0, c.channels - 1), _int(req, "last", 0, c.channels - 1)
    if last < first:
        raise Refusal("bad_request", "last must not be before first")
    stride = _int(req, "stride", 1, c.channels, 1)
    average = _int(req, "average", 1, MAX_SLAB_CHANNELS, 1)
    if average > stride:
        raise Refusal("bad_request", "channels averaged per panel cannot exceed the stride (panels would share channels)")
    panels = panels_of(first, last, stride, average)
    if len(panels) > MAX_PANELS:
        raise Refusal("unsupported", f"{len(panels)} panels are more than the {MAX_PANELS} a grid holds; raise the stride or narrow the range")
    ny, nx = c.shape[c.y], c.shape[c.x]
    if average * ny * nx > MAX_SLAB_WORK:
        raise Refusal("unsupported", "a panel would read too much of the cube; average fewer channels")
    budget = min(MAX_PANEL_PIXELS, DEFAULT_PANEL_PIXELS, MAX_GRID_PIXELS // len(panels))
    step = 1
    while math.ceil(ny / step) * math.ceil(nx / step) > budget:
        step += 1
    if step > MAX_PREVIEW_STEP:
        raise Refusal("unsupported", "the image is too large for a channel grid")
    pw, ph = math.ceil(nx / step), math.ceil(ny / step)

    # ----- the shared scale: numbers, stated, the same for every panel -----
    limits = req.get("limits")
    if limits is not None:
        if (not isinstance(limits, list) or len(limits) != 2 or not all(isinstance(v, (int, float)) and not isinstance(v, bool) and math.isfinite(v) for v in limits)
                or limits[0] >= limits[1]):
            raise Refusal("bad_request", "limits must be two finite numbers, the lower below the upper")
        scale = {"lo": float(limits[0]), "hi": float(limits[1]), "source": "custom limits given with the request (the pane's own), the same for every panel", "sampled_panels": []}
    else:
        scale = _sampled_scale(c, panels, average, step, reduce)
    flags = c.flags()
    flagged = set(flags["channels"]) if flags["declared"] else None
    want = [p[0] for p in panels] + [p[1] for p in panels]
    spectral = _spectral_labels(c, want, req.get("unit"), req.get("convention"))
    n = len(panels)
    out_panels = []
    for i, (a, b) in enumerate(panels):
        sv = spectral["values"]
        out_panels.append({"index": i, "first": a, "last": b, "channels": b - a + 1, "mean": b > a,
                           "spectral": None if sv is None else {"first": sv["first"][i], "last": sv["last"][i]},
                           "flagged": None if flagged is None else sum(1 for k in range(a, b + 1) if k in flagged)})
    spectral = {k: v for k, v in spectral.items() if k != "values"}
    return {"nbytes": 0, "channels": c.channels, "width": nx, "height": ny, "panel_width": pw, "panel_height": ph, "step": step,
            "panels": out_panels, "panel_count": n, "pixels_per_panel": pw * ph, "pixels_total": pw * ph * n,
            "caps": {"panels": MAX_PANELS, "pixels_per_panel": MAX_PANEL_PIXELS, "pixels_total": MAX_GRID_PIXELS},
            "preview": None if step == 1 else {"step": step, "method": f"each panel is the mean of the finite pixels in {step} x {step} blocks (NaN where a block has none); a display preview, not for measurement"},
            "averaging": None if average == 1 else {"channels": average, "order": "per-pixel mean of the finite values of the panel's channels at full resolution, then the block mean"},
            "scale": scale, "spectral": spectral, "nan_meaning": NAN_MEANING}, b""


def _sampled_scale(c, panels, average, step, reduce) -> dict:
    ny, nx = c.shape[c.y], c.shape[c.x]
    n = len(panels)
    k = min(n, SCALE_SAMPLE_PANELS)
    k = max(1, min(k, SCALE_SAMPLE_WORK // max(1, average * ny * nx)))
    idx = sorted({int(round(v)) for v in np.linspace(0, n - 1, k)})
    pooled = []
    for i in idx:
        a, b = panels[i]
        if a == b:
            with c.block_reader() as read:
                m = read(a, a + 1, slice(None), slice(None))[0].astype("<f4")
        else:
            m, _ = slab_mean(c, a, b)
        m = reduce(m, step) if step > 1 else m
        pooled.append(m[np.isfinite(m)].ravel())
    v = np.concatenate(pooled) if pooled else np.empty(0)
    if v.size == 0:
        return {"lo": None, "hi": None, "source": "the sampled panels hold no finite pixel", "sampled_panels": idx}
    lo, hi = (float(x) for x in np.percentile(v, [1, 99]))
    if not hi > lo:
        lo, hi = float(v.min()), float(v.max())
    return {"lo": lo, "hi": hi, "sampled_panels": idx,
            "source": f"1st to 99th percentile of the finite pixels pooled over panels {', '.join(str(i + 1) for i in idx)} of {n} "
                      f"(evenly spaced, at the preview resolution); one scale for every panel"}


def _spectral_labels(c, channels: list[int], unit, convention) -> dict:
    """The spectral axis of the panels' first and last channels: native values, and converted ones when a unit is asked for.
    A conversion that is refused leaves the native values in place and says why (the grid still draws)."""
    f = c.spectral_facts()
    half = len(channels) // 2
    out = {"axis": None if f is None else f.get("name"), "ctype": None if f is None else f.get("ctype"), "values": None,
           "unit": None, "unit_status": None, "native_unit": None, "convention": None, "convention_source": None, "specsys": None,
           "rest": None, "basis": None, "refused": None, "reference_frame_note": convert.SPECSYS_NOTE}
    if f is None or not f["resolved"]:
        out["refused"] = (f or {}).get("note") or "the file has no spectral axis; panels are labelled by channel index only"
        return out
    native = convert.native_values(c, channels)
    fin = lambda a: [float(v) if math.isfinite(v) else None for v in a]
    out.update(native_unit=f["unit"], unit=f["unit"], unit_status=f["unit_status"], specsys=f.get("specsys"),
               values={"first": fin(native[:half]), "last": fin(native[half:])})
    if unit is None or unit == f["unit"]:
        return out
    many = convert.convert_spectral_many(c, channels, unit, convention)
    out.update(convention=many.get("convention"), convention_source=many.get("convention_source"), rest=many.get("rest"),
               basis=many.get("basis"), specsys=many.get("specsys") or out["specsys"])
    if many["refused"] is not None or many["converted"] is None:
        out["refused"] = many["refused"] or "the conversion is not defined for this axis"
        return out
    out.update(unit=unit, unit_status="converted", values={"first": many["converted"]["values"][:half], "last": many["converted"]["values"][half:]})
    return out


# ---------- position-velocity ----------

def _path(req: dict, nx: int, ny: int) -> np.ndarray:
    pts = req.get("points")
    if not isinstance(pts, list) or not 2 <= len(pts) <= MAX_PV_VERTICES:
        raise Refusal("bad_request", f"the path needs 2 to {MAX_PV_VERTICES} vertices")
    out = []
    for p in pts:
        if not isinstance(p, list) or len(p) != 2:
            raise Refusal("bad_request", "each vertex is [x, y] in image pixels")
        out.append([_num(p[0], "a vertex coordinate"), _num(p[1], "a vertex coordinate")])
    a = np.asarray(out, dtype=float)
    if np.any(np.abs(a) > (1 << 20)):
        raise Refusal("bad_request", "a vertex is far outside the image")
    return a


def _local_scale_arcsec(wcs_cel, x: float, y: float) -> float:
    """Arcsec per pixel at (x, y): the geometric mean of the separations of the neighbours half a pixel away along each axis."""
    px = wcs_cel.pixel_to_world([x - 0.5, x + 0.5, x, x], [y, y, y - 0.5, y + 0.5])
    sx, sy = px[0].separation(px[1]).arcsec, px[2].separation(px[3]).arcsec
    return float(math.sqrt(sx * sy))


def sample_path(points: np.ndarray, width_px: float):
    """Samples along a polyline: n along the path at spacing <= 1 pixel, m across at 1-pixel spacing centred on the path.
    Returns (x, y) of shape (n, m), the centre line (cx, cy) of length n, the path length in pixels and m. The cross direction
    at a sample is the normal of the segment it lies on. Repeated vertices are dropped."""
    pts = [points[0]]
    for p in points[1:]:
        if p[0] != pts[-1][0] or p[1] != pts[-1][1]:
            pts.append(p)
    pts = np.asarray(pts)
    if len(pts) < 2:
        raise Refusal("bad_request", "the path has no length")
    seg = np.diff(pts, axis=0)
    seglen = np.hypot(seg[:, 0], seg[:, 1])
    length = float(seglen.sum())
    n = max(2, int(math.ceil(length)) + 1)
    if n > MAX_PV_SAMPLES:
        raise Refusal("unsupported", f"the path is {length:.0f} pixels long: more than {MAX_PV_SAMPLES} samples; draw a shorter line")
    m = max(1, int(round(width_px)))
    if m > MAX_PV_CROSS:
        raise Refusal("unsupported", f"a width of {m} pixels is more than the {MAX_PV_CROSS} samples across that are supported")
    s = np.linspace(0.0, length, n)
    cum = np.concatenate([[0.0], np.cumsum(seglen)])
    k = np.clip(np.searchsorted(cum, s, side="right") - 1, 0, len(seglen) - 1)
    t = (s - cum[k]) / seglen[k]
    cx = pts[k, 0] + t * seg[k, 0]
    cy = pts[k, 1] + t * seg[k, 1]
    ux, uy = seg[k, 0] / seglen[k], seg[k, 1] / seglen[k]
    off = np.arange(m) - (m - 1) / 2.0
    x = cx[:, None] + (-uy)[:, None] * off[None, :]
    y = cy[:, None] + ux[:, None] * off[None, :]
    return x, y, cx, cy, length, m


def interpolate(block: np.ndarray, x0: int, y0: int, x: np.ndarray, y: np.ndarray, nx: int, ny: int, mode: str) -> np.ndarray:
    """Values of `block` (channels, h, w), whose pixel [0, 0] is image pixel (x0, y0), at image positions x, y (any shape)
    -> (channels, *x.shape). linear: bilinear from the four surrounding pixels; the result is NaN if any neighbour with a
    non-zero weight is not finite, or the position lies outside [0, n-1]. nearest: the pixel whose centre is nearest, no
    interpolation; outside [-0.5, n-0.5) is NaN. An exact pixel position gives that pixel under both."""
    shape = x.shape
    xf, yf = x.ravel(), y.ravel()
    if mode == "nearest":
        xi, yi = np.floor(xf + 0.5).astype(np.int64), np.floor(yf + 0.5).astype(np.int64)
        ok = (xi >= 0) & (xi < nx) & (yi >= 0) & (yi < ny)
        out = block[:, np.clip(yi, 0, ny - 1) - y0, np.clip(xi, 0, nx - 1) - x0].astype(np.float64)
        out[:, ~ok] = np.nan
        return out.reshape((block.shape[0],) + shape)
    ok = (xf >= 0) & (xf <= nx - 1) & (yf >= 0) & (yf <= ny - 1)
    xc, yc = np.clip(xf, 0, nx - 1), np.clip(yf, 0, ny - 1)
    xa, ya = np.floor(xc).astype(np.int64), np.floor(yc).astype(np.int64)
    xb, yb = np.minimum(xa + 1, nx - 1), np.minimum(ya + 1, ny - 1)
    fx, fy = xc - xa, yc - ya
    acc = np.zeros((block.shape[0], xf.size))
    bad = np.zeros((block.shape[0], xf.size), dtype=bool)
    for xi, yi, w in ((xa, ya, (1 - fx) * (1 - fy)), (xb, ya, fx * (1 - fy)), (xa, yb, (1 - fx) * fy), (xb, yb, fx * fy)):
        v = block[:, yi - y0, xi - x0].astype(np.float64)
        used = w > 0
        fin = np.isfinite(v)
        bad |= used[None, :] & ~fin
        acc += np.where(fin, v, 0.0) * w[None, :]
    acc[bad] = np.nan
    acc[:, ~ok] = np.nan
    return acc.reshape((block.shape[0],) + shape)


def offsets_arcsec(c, x: np.ndarray, y: np.ndarray) -> tuple[np.ndarray, str, dict]:
    """Offset of each centre-line sample from the first, along the path: cumulative great-circle separation (astropy) of the
    samples' sky positions, in arcsec. Without a complete celestial WCS the offset is in pixels and says so."""
    if not c.celestial or c.wcs is None:
        d = np.concatenate([[0.0], np.cumsum(np.hypot(np.diff(x), np.diff(y)))])
        return d, "pixel", {"kind": "pixel", "note": "no complete celestial WCS in the header; offsets are image pixels along the path"}
    with warnings.catch_warnings():
        warnings.simplefilter("ignore")
        sky = c.wcs.celestial.pixel_to_world(x, y)
    sep = sky[:-1].separation(sky[1:]).arcsec
    if not np.all(np.isfinite(sep)):
        raise Refusal("unsupported", "part of the path has no sky position (outside the projection)")
    return np.concatenate([[0.0], np.cumsum(sep)]), "arcsec", c.sky_facts()


def pv(c, req: dict, stats) -> tuple[dict, bytes]:
    _need_channels(c)
    nx, ny = c.shape[c.x], c.shape[c.y]
    points = _path(req, nx, ny)
    first = _int(req, "first", 0, c.channels - 1, 0)
    last = _int(req, "last", 0, c.channels - 1, c.channels - 1)
    if last < first:
        raise Refusal("bad_request", "last must not be before first")
    nch = last - first + 1
    mode = req.get("interpolation", "linear")
    if mode not in ("linear", "nearest"):
        raise Refusal("bad_request", "interpolation must be linear or nearest")
    width = _num(req.get("width", 1), "width")
    width_unit = req.get("width_unit", "pixel")
    if width_unit not in ("pixel", "arcsec") or not 0 < width <= 1e6:
        raise Refusal("bad_request", "width must be positive, in pixel or arcsec")
    if width_unit == "arcsec":
        if not c.celestial or c.wcs is None:
            raise Refusal("unsupported", "a width in arcsec needs a celestial WCS; this file has none, give the width in pixels")
        mid = points.mean(axis=0)
        with warnings.catch_warnings():
            warnings.simplefilter("ignore")
            scale = _local_scale_arcsec(c.wcs.celestial, float(mid[0]), float(mid[1]))
        width_px = width / scale
        width_note = f"{width:g} arcsec = {width_px:.2f} pixels at the path's midpoint ({scale:.4g} arcsec per pixel, from astropy separations)"
    else:
        width_px, width_note = width, f"{width:g} pixels"
    x, y, cx, cy, length, m = sample_path(points, width_px)
    n = x.shape[0]
    if n * nch > MAX_PV_CELLS:
        raise Refusal("unsupported", f"{n} offsets x {nch} channels is more than the {MAX_PV_CELLS} cells a diagram holds; use a shorter line or fewer channels")
    if n * m * nch > MAX_PV_WORK:
        raise Refusal("unsupported", "the path, width and channel range ask for too much sampling; narrow one of them")
    off, off_unit, sky = offsets_arcsec(c, cx, cy)
    data = np.full((nch, n), np.nan, dtype="<f4")
    counts = np.zeros((nch, n), dtype=np.uint8)
    outside = 0
    inside = lambda a, b: (a >= 0) & (a <= nx - 1) & (b >= 0) & (b <= ny - 1) if mode == "linear" else (a >= -0.5) & (a < nx - 0.5) & (b >= -0.5) & (b < ny - 0.5)
    with c.block_reader() as read:
        for r0 in range(0, n, PV_RUN):
            xs, ys = x[r0:r0 + PV_RUN], y[r0:r0 + PV_RUN]
            ok = inside(xs, ys)
            outside += int(ok.size - ok.sum())
            if not ok.any():
                continue
            bx0, bx1 = int(max(0, math.floor(xs[ok].min()))), int(min(nx - 1, math.ceil(xs[ok].max()) + 1))
            by0, by1 = int(max(0, math.floor(ys[ok].min()))), int(min(ny - 1, math.ceil(ys[ok].max()) + 1))
            bh, bw = by1 - by0 + 1, bx1 - bx0 + 1
            k = max(1, min(nch, READ_BLOCK_ELEMENTS // (bh * bw), PV_RUN_ELEMENTS // max(1, xs.size)))
            for c0 in range(first, last + 1, k):
                c1 = min(last + 1, c0 + k)
                block = read(c0, c1, slice(by0, by1 + 1), slice(bx0, bx1 + 1))
                v = interpolate(block, bx0, by0, xs, ys, nx, ny, mode)       # (channels, run, cross)
                fin = np.isfinite(v)
                cnt = fin.sum(axis=2)
                with np.errstate(all="ignore"):
                    mean = np.where(cnt > 0, np.where(fin, v, 0.0).sum(axis=2) / np.maximum(cnt, 1), np.nan)
                data[c0 - first:c1 - first, r0:r0 + xs.shape[0]] = mean
                counts[c0 - first:c1 - first, r0:r0 + xs.shape[0]] = cnt
    chans = list(range(first, last + 1))
    spectral = _pv_axis(c, chans, req.get("unit"), req.get("convention"))
    axis = np.asarray(spectral.pop("values"), dtype="<f8")
    payload = off.astype("<f8").tobytes() + axis.tobytes() + data.tobytes() + counts.tobytes()
    interp_text = ("bilinear interpolation of the four surrounding pixels; a sample with a non-finite neighbour is excluded"
                   if mode == "linear" else "nearest pixel, no interpolation")
    return {"nbytes": len(payload), "offsets": n, "channels_returned": nch, "first_channel": first, "last_channel": last,
            "layout": "float64 offset[offsets], float64 axis[channels] (NaN = undefined), float32 value[channels][offsets] (NaN = no value), "
                      "uint8 count[channels][offsets] (finite samples averaged), little-endian",
            "offset": {"unit": off_unit, "origin": "the first vertex; positive along the path",
                       "method": "cumulative great-circle separation of the sky positions of the samples along the path (astropy)" if off_unit == "arcsec"
                       else "cumulative pixel distance along the path"},
            "path": {"vertices": points.tolist(), "length_pixels": length, "spacing_pixels": length / (n - 1), "samples": n},
            "width": {"requested": width, "unit": width_unit, "cross_samples": m, "note": width_note,
                      "method": f"{m} samples across at 1-pixel spacing, perpendicular to the segment, averaged over the finite ones (non-finite excluded, counted)"},
            "interpolation": {"mode": mode, "method": interp_text}, "outside_samples": outside,
            "spectral": spectral, "stats": stats(data), "nan_meaning": NAN_MEANING, "sky": sky,
            "caps": {"offsets": MAX_PV_SAMPLES, "cells": MAX_PV_CELLS, "cross_samples": MAX_PV_CROSS, "work": MAX_PV_WORK}}, payload


def _pv_axis(c, chans: list[int], unit, convention) -> dict:
    f = c.spectral_facts()
    n = len(chans)
    out = {"axis": None, "unit": None, "unit_status": None, "native_unit": None, "convention": None, "convention_source": None, "specsys": None,
           "rest": None, "basis": None, "refused": None, "reference_frame_note": convert.SPECSYS_NOTE, "values": [math.nan] * n}
    if f is None or not f["resolved"]:
        out.update(axis="channel", unit=None, refused=(f or {}).get("note") or "the spectral axis is not resolved; the axis is the channel index",
                   values=[float(i) for i in chans])
        return out
    native = convert.native_values(c, chans)
    out.update(axis=f.get("name"), unit=f["unit"], native_unit=f["unit"], unit_status=f["unit_status"], specsys=f.get("specsys"), values=native.tolist())
    if unit is None or unit == f["unit"]:
        return out
    many = convert.convert_spectral_many(c, chans, unit, convention)
    out.update(convention=many.get("convention"), convention_source=many.get("convention_source"), rest=many.get("rest"), basis=many.get("basis"))
    if many["refused"] is not None or many["converted"] is None:
        out["refused"] = many["refused"] or "the conversion is not defined for this axis"
        return out
    out.update(unit=unit, unit_status="converted", values=[math.nan if v is None else v for v in many["converted"]["values"]])
    return out
