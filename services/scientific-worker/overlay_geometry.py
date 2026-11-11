"""Overlay geometry on a base map (ADR 0042, section 3): overlays from another dataset are placed by WCS, never resampled.

Every position this module returns is in BASE pixels: 0-based, pixel centres at integers, x along the base's first sky
axis and y along its second, y up (FITS order). The browser turns them into picture coordinates.

- contours: traced on the overlay's own grid by contourpy (matplotlib's contour engine), NaN as gaps; every vertex goes
  overlay pixel -> world (overlay frame) -> base frame -> base pixel with astropy.
- orientation: single pixels of the overlay's orientation map, picked on its own grid (never block means), converted to
  sky-frame B-field orientation from the declared frame and quantity (orientation.py), carried into the base's celestial
  frame and from there into the base's pixel frame.
- beam: an ellipse built on the sky from a header's BMAJ, BMIN and BPA (never a default), placed by the base's WCS.
- grid: lines of constant longitude and latitude in a chosen frame, generated on the sky and carried to base pixels.

A refusal (Refusal) carries a sentence for the person: frames that cannot be related, footprints that do not overlap, an
undeclared convention, a missing beam. The binary payload is little-endian float32; the head says how it is laid out.
"""
from __future__ import annotations

import math

import numpy as np

import orientation as ori

MAX_VERTICES = 200_000          # contour or grid vertices sent to the browser (1.6 MB of float32)
MAX_SEGMENTS = 20_000           # orientation samples
MAX_TRACE_WORK = 1 << 28        # overlay pixels x levels traced for one request (a few seconds of contourpy at most)
SIMPLIFY_TOLERANCES = (0.1, 0.25, 0.5, 1.0, 2.0, 4.0)   # base pixels, tried in order when over the vertex budget
FOOTPRINT_SAMPLES = 33          # per axis, when testing whether two footprints overlap
CLIP_MARGIN = 2.0               # base pixels kept outside the image so a line leaves the edge cleanly
COORDS = "base pixels: 0-based, pixel centres at integers, y up (FITS order)"
PLACED = "placed by WCS geometry (astropy): never resampled onto the base grid"

GRID_FRAMES = ("icrs", "fk5", "galactic")
# Grid spacings in degrees. Right ascension is labelled in time, so its steps are whole time units.
RA_STEPS = [s / 240.0 for s in (1, 2, 5, 10, 15, 30)] + [m / 4.0 for m in (1, 2, 5, 10, 15, 30)] + [h * 15.0 for h in (1, 2, 3, 6)]
DEG_STEPS = [s / 3600.0 for s in (1, 2, 5, 10, 15, 30)] + [m / 60.0 for m in (1, 2, 5, 10, 15, 30)] + [1.0, 2.0, 5.0, 10.0, 15.0, 30.0, 45.0, 90.0]
GRID_LINES_WANTED = 5
GRID_SAMPLES = 160              # vertices along one grid line before clipping


class Refusal(Exception):
    """The overlay cannot be drawn honestly; the message says why, for the person."""


# ---------- frames and placement ----------

def frame_label(f) -> str:
    name = getattr(f, "name", str(f))
    if name == "fk5":
        return f"FK5 (J{f.equinox.jyear:g})"
    if name == "fk4":
        return f"FK4 (B{f.equinox.byear:g})"
    return {"icrs": "ICRS", "galactic": "Galactic"}.get(name, name)


class Sky:
    """A file's two-axis celestial WCS and the celestial frame it is in, refused when either is not established."""

    def __init__(self, c, who: str):
        try:
            self.w = ori.celestial_wcs(c)
        except ori.Undeclared as e:
            raise Refusal(f"{who}: {e}") from e
        facts = c.sky_facts()
        self.status = facts.get("frame_status")
        if self.status not in ("declared", "inferred"):
            # wcslib would assume ICRS (or FK4/FK5 from EQUINOX); relating two files on an assumed frame is not done
            raise Refusal(f"{who}: the celestial frame is not declared (RADESYS, or EQUINOX for FK4/FK5), so it cannot be related to another file")
        try:
            self.frame = ori.sky_frame(self.w)
        except ori.Undeclared as e:
            raise Refusal(f"{who}: {e}") from e
        self.label = frame_label(self.frame)
        self.shape = (c.shape[c.x], c.shape[c.y])   # (width, height)

    def same_frame(self, other: "Sky") -> bool:
        return bool(self.frame.is_equivalent_frame(other.frame))

    def scale_deg(self) -> float:
        """Mean pixel size in degrees at the reference pixel (only used to choose spacings and sizes, never to place)."""
        from astropy.wcs.utils import proj_plane_pixel_scales
        return float(np.mean(proj_plane_pixel_scales(self.w)))


def placement(src: Sky, base: Sky, same_dataset: bool) -> dict:
    same = src.same_frame(base)
    if same_dataset:
        method = "drawn on its own grid"
    elif same:
        method = f"{PLACED}; both in {base.label}"
    else:
        method = f"{PLACED}; converted from {src.label} to the base's {base.label} frame"
    return {"method": method, "from_frame": src.label, "to_frame": base.label, "from_status": src.status, "to_status": base.status,
            "same_frame": same, "same_dataset": same_dataset}


def to_base(src: Sky, base: Sky, x, y):
    """Base pixel positions of src pixel positions, through the sky (astropy). Non-finite where a point has no sky position."""
    sc = src.w.pixel_to_world(np.asarray(x, dtype=float), np.asarray(y, dtype=float))
    if not src.same_frame(base):
        sc = sc.transform_to(base.frame)
    xb, yb = base.w.world_to_pixel(sc)
    return np.asarray(xb, dtype=float), np.asarray(yb, dtype=float)


def _inside(x, y, shape, margin=0.0):
    w, h = shape
    return np.isfinite(x) & np.isfinite(y) & (x >= -0.5 - margin) & (x <= w - 0.5 + margin) & (y >= -0.5 - margin) & (y <= h - 0.5 + margin)


def _edge_grid(shape):
    w, h = shape
    xs = np.linspace(-0.5, w - 0.5, FOOTPRINT_SAMPLES)
    ys = np.linspace(-0.5, h - 0.5, FOOTPRINT_SAMPLES)
    gx, gy = np.meshgrid(xs, ys)
    return gx.ravel(), gy.ravel()


def check_overlap(src: Sky, base: Sky, who: str) -> None:
    """Refuse when the two footprints do not overlap: no sampled point of either lies inside the other."""
    with np.errstate(all="ignore"):
        x, y = _edge_grid(src.shape)
        xb, yb = to_base(src, base, x, y)
        if np.any(_inside(xb, yb, base.shape)):
            return
        x, y = _edge_grid(base.shape)
        xo, yo = to_base(base, src, x, y)
        if np.any(_inside(xo, yo, src.shape)):
            return
    raise Refusal(f"{who} does not overlap the base map on the sky (compared by WCS in {base.label}), so there is nothing to draw")


# ---------- polylines ----------

def split_runs(x, y, keep):
    """Split one polyline into runs of consecutive kept vertices. A vertex is kept when it, or a neighbour, is kept, so a
    line that leaves the image still reaches its edge."""
    k = np.asarray(keep, dtype=bool)
    if k.size == 0:
        return []
    grown = k.copy()
    grown[1:] |= k[:-1]
    grown[:-1] |= k[1:]
    grown &= np.isfinite(x) & np.isfinite(y)
    out, start = [], None
    for i, g in enumerate(np.append(grown, False)):
        if g and start is None:
            start = i
        elif not g and start is not None:
            if i - start >= 2:
                out.append(np.column_stack([x[start:i], y[start:i]]))
            start = None
    return out


def rdp(pts: np.ndarray, tol: float) -> np.ndarray:
    """Ramer-Douglas-Peucker simplification of one polyline to within tol (base pixels); display only, endpoints kept."""
    n = len(pts)
    if n < 3:
        return pts
    keep = np.zeros(n, dtype=bool)
    keep[0] = keep[-1] = True
    stack = [(0, n - 1)]
    while stack:
        i, j = stack.pop()
        if j <= i + 1:
            continue
        seg = pts[j] - pts[i]
        d = pts[i + 1:j] - pts[i]
        length = math.hypot(seg[0], seg[1])
        dist = np.hypot(d[:, 0], d[:, 1]) if length == 0 else np.abs(seg[0] * d[:, 1] - seg[1] * d[:, 0]) / length
        k = int(np.argmax(dist))
        if dist[k] > tol:
            m = i + 1 + k
            keep[m] = True
            stack.append((i, m))
            stack.append((m, j))
    return pts[keep]


def within_budget(paths: list, budget: int = MAX_VERTICES):
    """(paths, simplified): paths as they are when within the budget, otherwise simplified with the smallest tolerance that
    fits. Refused when even the largest tolerance does not."""
    before = sum(len(p) for _, p in paths)
    if before <= budget:
        return paths, None
    for tol in SIMPLIFY_TOLERANCES:
        out = [(g, rdp(p, tol)) for g, p in paths]
        after = sum(len(p) for _, p in out)
        if after <= budget:
            return out, {"tolerance_px": tol, "vertices_before": before, "vertices_after": after}
    raise Refusal(f"the overlay has {before} vertices, more than {budget} even simplified to {SIMPLIFY_TOLERANCES[-1]:g} base pixels; use fewer levels")


def paths_payload(paths: list) -> tuple[dict, bytes]:
    table = np.array([[g, len(p)] for g, p in paths], dtype="<f4").reshape(-1, 2)
    verts = np.concatenate([p for _, p in paths]).astype("<f4") if paths else np.zeros((0, 2), dtype="<f4")
    return {"paths": len(paths), "vertices": int(len(verts))}, table.tobytes() + verts.tobytes()


# ---------- contours ----------

def contour_levels(spec: dict, plane: np.ndarray, channel: int):
    """(levels, sigma) for a levels spec. sigma is {value, method} for k x sigma levels and None otherwise."""
    mode = spec.get("mode")
    if mode == "list":
        return [float(v) for v in spec["values"]], None
    if mode == "linear":
        return [float(spec["start"]) + i * float(spec["step"]) for i in range(int(spec["count"]))], None
    if mode == "geometric":
        return [float(spec["start"]) * float(spec["factor"]) ** i for i in range(int(spec["count"]))], None
    if mode == "sigma":
        from astropy.stats import mad_std
        finite = plane[np.isfinite(plane)].astype("<f8")
        sigma = float(mad_std(finite)) if finite.size else float("nan")
        if not math.isfinite(sigma) or sigma <= 0:
            raise Refusal(f"the robust scatter (1.4826 x MAD) of channel {channel + 1} is zero or undefined (too few distinct finite values), so levels in sigma cannot be set; give the levels instead")
        recorded = spec.get("sigma")
        if recorded is not None and abs(sigma - float(recorded)) > 1e-6 * float(recorded):
            raise Refusal(f"sigma recomputed from the data is {sigma:.6g}, not the {float(recorded):.6g} these levels were drawn with: the data changed")
        method = (f"1.4826 x MAD of the {finite.size} finite pixels of channel {channel + 1} (astropy.stats.mad_std): a robust scatter of the "
                  "whole plane, signal included, not a noise measurement from an emission-free region")
        return [float(k) * sigma for k in spec["multiples"]], {"value": sigma, "method": method}
    raise Refusal("unknown contour level mode")


def contours(base_cube, ov_cube, spec: dict, ov_unit: tuple, same_dataset: bool) -> tuple[dict, bytes]:
    if ov_cube.channels is None:
        raise Refusal("the overlay has no channels")
    channel = spec.get("channel")
    if isinstance(channel, bool) or not isinstance(channel, int) or not 0 <= channel < ov_cube.channels:
        raise Refusal(f"channel {channel} is not in the overlay (it has {ov_cube.channels})")
    unit, unit_status = ov_unit
    want = spec.get("unit")
    if (want or None) != (unit or None):
        raise Refusal(f"the levels are written in {want or 'no unit'}, but the overlay's unit is {unit or 'not stated'}; set the levels again")
    base = Sky(base_cube, "the base map")
    src = Sky(ov_cube, "the contoured map")
    check_overlap(src, base, "the contoured map")
    try:
        import contourpy
    except ImportError as e:
        raise Refusal("contours need the Python package contourpy, which is not installed where this file is read") from e
    plane, _ = ov_cube.read_plane(channel)
    levels, sigma = contour_levels(spec["levels"], plane, channel)
    if plane.size * len(levels) > MAX_TRACE_WORK:
        raise Refusal(f"tracing {len(levels)} levels over {plane.size} pixels is more than this request allows; use fewer levels")
    z = np.ma.masked_invalid(plane.astype("<f8"))            # no value is a gap in every contour, never zero
    gen = contourpy.contour_generator(z=z, line_type=contourpy.LineType.Separate)
    traced, per_level = [], []
    for i, lev in enumerate(levels):
        lines = gen.lines(lev)
        per_level.append(len(lines))
        traced.extend((i, np.asarray(ln, dtype=float)) for ln in lines if len(ln) >= 2)
    paths = []
    if traced:
        allpts = np.concatenate([p for _, p in traced])
        with np.errstate(all="ignore"):
            xb, yb = to_base(src, base, allpts[:, 0], allpts[:, 1])
        at = 0
        for g, p in traced:
            n = len(p)
            sx, sy = xb[at:at + n], yb[at:at + n]
            at += n
            paths.extend((g, run) for run in split_runs(sx, sy, _inside(sx, sy, base.shape, CLIP_MARGIN)))
    paths, simplified = within_budget(paths)
    layout, payload = paths_payload(paths)
    spectral = ov_cube.spectral_facts()
    value = ov_cube.spectral_value(channel)
    head = {"kind": "contours", **layout, "levels": levels, "level_paths": per_level, "unit": unit, "unit_status": unit_status,
            "sigma": sigma, "channel": channel, "channels": ov_cube.channels,
            "spectral_value": None if value is None or not math.isfinite(float(value)) else float(value),
            "spectral_unit": None if spectral is None else spectral.get("unit"),
            "simplified": simplified, "placement": placement(src, base, same_dataset),
            "method": "contourpy on the overlay's own grid; pixels with no value (NaN, BLANK) are gaps"}
    return head, payload


# ---------- orientation ----------

def orientation_samples(base_cube, ov_cube, rel_cube, spec: dict, interp: dict, spacing, same_dataset: bool) -> tuple[dict, bytes]:
    unit = interp.get("unit")
    if unit not in ("deg", "rad"):
        raise Refusal("the orientation map's unit (deg or rad) is not declared")
    try:
        ori.check_frame(interp.get("frame"))
        rot = ori.b_rotation_deg(interp.get("quantity"))
    except ori.Undeclared as e:
        raise Refusal(f"orientation map: {e}") from e
    if ov_cube.channels != 1:
        raise Refusal("an orientation map is a single plane")
    base = Sky(base_cube, "the base map")
    src = Sky(ov_cube, "the orientation map")
    check_overlap(src, base, "the orientation map")
    needs_rel = spec.get("opacity") == "reliability" or spec.get("length") == "reliability" or spec.get("threshold") is not None
    if needs_rel and rel_cube is None and (spec.get("length") == "reliability" or spec.get("threshold") is not None):
        raise Refusal("length scaled by reliability, or a reliability threshold, needs a reliability map declared with the orientation map")
    plane, _ = ov_cube.read_plane(0)
    h, w = plane.shape
    ratio = src.scale_deg() / base.scale_deg()             # overlay pixel size in base pixels
    sampling = spec.get("sampling") or {}
    if sampling.get("mode") == "step":
        step = int(sampling["step"])
    else:
        if spacing is None or isinstance(spacing, bool) or not isinstance(spacing, (int, float)) or not 1 <= spacing <= 4096:
            raise Refusal("automatic sampling needs the wanted spacing in base pixels")
        step = max(1, int(round(float(spacing) / ratio)))
    capped = False
    while math.ceil(h / step) * math.ceil(w / step) > MAX_SEGMENTS:
        step += max(1, step // 4)
        capped = True
    ys, xs = np.mgrid[step // 2:h:step, step // 2:w:step]
    xs, ys = xs.ravel().astype(float), ys.ravel().astype(float)
    vals = plane[ys.astype(int), xs.astype(int)].astype("<f8")
    deg = vals if unit == "deg" else np.degrees(vals)
    weight = np.ones_like(deg)
    rel_info = None
    if rel_cube is not None and needs_rel:
        if (rel_cube.shape[rel_cube.x], rel_cube.shape[rel_cube.y]) != (w, h):
            raise Refusal("the declared reliability map is not on the orientation map's grid")
        rel, _ = rel_cube.read_plane(0)
        finite = rel[np.isfinite(rel)]
        p99 = float(np.percentile(finite, 99)) if finite.size else float("nan")
        r = rel[ys.astype(int), xs.astype(int)].astype("<f8")
        with np.errstate(all="ignore"):
            weight = np.where(np.isfinite(r) & (p99 > 0), np.clip(r / p99, 0.0, 1.0), np.nan)
        dropped = 0
        thr = spec.get("threshold")
        if thr is not None:
            low = ~(np.isfinite(r) & (r >= float(thr)))     # unknown reliability does not pass a threshold
            dropped = int(np.count_nonzero(low & np.isfinite(deg)))
            deg = np.where(low, np.nan, deg)
        rel_info = {"p99": p99 if math.isfinite(p99) else None, "threshold": thr, "dropped_below_threshold": dropped,
                    "scale": "weight = reliability / its 99th percentile, clipped to [0, 1]; NaN where the reliability is unknown"}
    ok = np.isfinite(deg)
    xs, ys, deg, weight = xs[ok], ys[ok], deg[ok], weight[ok]
    with np.errstate(all="ignore"):
        pa = ori.to_sky_b_field(src.w, xs, ys, deg, interp["frame"], interp["quantity"])   # east of north in the overlay's frame
        origin = src.w.pixel_to_world(xs, ys)
        if not src.same_frame(base):
            pa = ori.rotate_pa_frame(origin, pa, base.frame)
            origin = origin.transform_to(base.frame)
        xb, yb = base.w.world_to_pixel(origin)
        xb, yb = np.asarray(xb, dtype=float), np.asarray(yb, dtype=float)
        ang = ori.wrap_axial(ori.sky_pa_to_pixel(base.w, xb, yb, pa))
    keep = _inside(xb, yb, base.shape) & np.isfinite(ang)
    out = np.column_stack([xb[keep], yb[keep], ang[keep], weight[keep]]).astype("<f4")
    how = {"pixel": f"angles counter-clockwise from the map's +x axis, turned into east of north with its WCS in {src.label}",
           "sky": f"angles east of north in {src.label}"}[interp["frame"]]
    what = {"b_field": "declared as the B-field", "starlight_polarization": "declared as starlight polarization, parallel to B",
            "gradient": f"declared as a gradient, rotated by {rot:g} degrees to B", "dust_polarization": f"declared as dust polarization, rotated by {rot:g} degrees to B"}[interp["quantity"]]
    conversion = f"{how}; {what}" + ("" if src.same_frame(base) else f"; rotated into {base.label}") + "; then into the base's pixel frame with its WCS"
    head = {"kind": "orientation", "segments": int(len(out)), "step": step, "spacing_px": step * ratio, "capped": capped,
            "conversion": conversion, "reliability": rel_info, "placement": placement(src, base, same_dataset),
            "method": "single pixels of the orientation map on its own grid (no averaging of angles)"}
    return head, out.tobytes()


# ---------- beam ----------

CORNERS = {"bottom-left": (0, 0), "bottom-right": (1, 0), "top-left": (0, 1), "top-right": (1, 1)}


def _header_number(h, key):
    v = h.get(key)
    return float(v) if isinstance(v, (int, float)) and not isinstance(v, bool) and math.isfinite(v) else None


def beam(base_cube, src_cube, spec: dict, same_dataset: bool, who: str) -> tuple[dict, bytes]:
    import astropy.units as u
    h = src_cube.h
    bmaj, bmin, bpa = _header_number(h, "BMAJ"), _header_number(h, "BMIN"), _header_number(h, "BPA")
    if bmaj is None or bmin is None:
        raise Refusal(f"the header of {who} has no BMAJ and BMIN, so there is no beam to draw (a beam is never assumed)")
    if bmaj <= 0 or bmin <= 0 or bmin > bmaj:
        raise Refusal(f"the header of {who} gives BMAJ {bmaj:g} and BMIN {bmin:g}; a beam needs 0 < BMIN <= BMAJ")
    note = None
    if bpa is None:
        if bmaj != bmin:
            raise Refusal(f"the header of {who} gives an elliptical beam without BPA, so its orientation is unknown")
        bpa, note = 0.0, "circular beam: the header has no BPA and none is needed"
    base = Sky(base_cube, "the base map")
    pa = bpa
    if not same_dataset and bmaj != bmin:
        src = Sky(src_cube, who)
    else:
        src = base
    corner = spec.get("corner") or "bottom-left"
    fx, fy = CORNERS[corner]
    W, H = base.shape
    a_px = (bmaj / 2.0) / base.scale_deg()
    margin = max(2.0, 0.04 * min(W, H))
    if 2.0 * (a_px + margin) > min(W, H):
        raise Refusal(f"the beam ({bmaj * 3600:.3g} arcsec) is larger than the base image can show in a corner")
    cx = (a_px + margin - 0.5) if fx == 0 else (W - 0.5 - margin - a_px)
    cy = (a_px + margin - 0.5) if fy == 0 else (H - 0.5 - margin - a_px)
    centre = base.w.pixel_to_world(cx, cy)
    if src is not base and not src.same_frame(base):
        pa = float(ori.rotate_pa_frame(centre.transform_to(src.frame), bpa, base.frame))
    phi = np.radians(np.linspace(0.0, 360.0, 73))
    a, b = bmaj / 2.0, bmin / 2.0
    r = a * b / np.sqrt((b * np.cos(phi)) ** 2 + (a * np.sin(phi)) ** 2)      # polar form of an ellipse from its major axis
    pts = centre.directional_offset_by((pa + np.degrees(phi)) * u.deg, r * u.deg)
    xb, yb = base.w.world_to_pixel(pts)
    poly = np.column_stack([np.asarray(xb, dtype=float), np.asarray(yb, dtype=float)])
    if not np.all(np.isfinite(poly)):
        raise Refusal("the beam cannot be placed on the base map's WCS")
    layout, payload = paths_payload([(0, poly)])
    head = {"kind": "beam", **layout, "corner": corner, "centre": [cx, cy],
            "beam": {"bmaj_arcsec": bmaj * 3600.0, "bmin_arcsec": bmin * 3600.0, "bpa_deg": None if note else bpa,
                     "bpa_frame": src.label, "source": f"BMAJ, BMIN{'' if note else ' and BPA'} of the header of {who} (degrees, FITS standard)",
                     **({"note": note} if note else {})},
            "placement": {"method": "an ellipse built on the sky around a corner of the base and placed by its WCS"
                          + ("" if src is base or src.same_frame(base) else f"; position angle rotated from {src.label} to {base.label}"),
                          "from_frame": src.label, "to_frame": base.label, "from_status": src.status, "to_status": base.status,
                          "same_frame": src is base or src.same_frame(base), "same_dataset": same_dataset}}
    return head, payload


# ---------- coordinate grid ----------

def _grid_frame(name):
    from astropy.coordinates import FK5, ICRS, Galactic
    return {"icrs": ICRS(), "fk5": FK5(equinox="J2000"), "galactic": Galactic()}[name]


def _choose(span: float, steps: list) -> float:
    for s in steps:
        if span / s <= GRID_LINES_WANTED + 1:
            return s
    return steps[-1]


def _label(value: float, axis: str, frame: str, step: float) -> str:
    from astropy.coordinates import Angle
    import astropy.units as u
    if frame == "galactic":
        d = 0 if step >= 1 else 1 if step >= 0.1 else 2 if step >= 0.01 else 3 if step >= 0.001 else 4
        return f"{(value % 360.0) if axis == 'lon' else value:.{d}f}°"
    if axis == "lon":
        sec = step * 240.0                                   # step in seconds of time
        fields = 1 if sec >= 3600 else 2 if sec >= 60 else 3
        return Angle(value % 360.0, u.deg).to_string(unit=u.hourangle, sep="hms", fields=fields, precision=0, pad=True)
    sec = step * 3600.0
    fields = 1 if sec >= 3600 else 2 if sec >= 60 else 3
    return Angle(value, u.deg).to_string(unit=u.deg, sep="dms", fields=fields, precision=0, alwayssign=True, pad=True)


EDGE_PREFERENCE = {"lon": ("bottom", "top", "left", "right"), "lat": ("left", "right", "bottom", "top")}
LABEL_GAP = 0.15                # fraction of an edge's length kept between two labels on it


def _label_point(runs, shape, axis, placed):
    """Where a grid line's label goes: where the line leaves the image, on the edge such lines are labelled on (longitude at
    the bottom, latitude at the left, else the opposite edge, else any), and not within LABEL_GAP of a label already on that
    edge. None when no such place is free."""
    W, H = shape
    ends = []
    for r in runs:
        ok = _inside(r[:, 0], r[:, 1], shape)
        idx = np.flatnonzero(ok)
        if len(idx):
            ends += [r[idx[0]], r[idx[-1]]]
    def edge_of(p):
        d = {"left": p[0] + 0.5, "right": W - 0.5 - p[0], "bottom": p[1] + 0.5, "top": H - 0.5 - p[1]}
        e = min(d, key=d.get)
        return e if d[e] <= 1.0 else None
    for want in EDGE_PREFERENCE[axis]:
        for p in ends:
            if edge_of(p) != want:
                continue
            along, size = (p[0], W) if want in ("bottom", "top") else (p[1], H)
            if all(lab["edge"] != want or abs((lab["x"] if want in ("bottom", "top") else lab["y"]) - along) > LABEL_GAP * size for lab in placed):
                return float(p[0]), float(p[1]), want
    return None


def grid(base_cube, spec: dict) -> tuple[dict, bytes]:
    from astropy.coordinates import SkyCoord
    import astropy.units as u
    name = spec.get("frame")
    if name not in GRID_FRAMES:
        raise Refusal("the grid frame is icrs, fk5 or galactic")
    base = Sky(base_cube, "the base map")
    gf = _grid_frame(name)
    W, H = base.shape
    gx, gy = _edge_grid(base.shape)
    with np.errstate(all="ignore"):
        sc = base.w.pixel_to_world(gx, gy).transform_to(gf)
        centre = base.w.pixel_to_world((W - 1) / 2.0, (H - 1) / 2.0).transform_to(gf)
    lon = np.asarray(sc.spherical.lon.deg, dtype=float)
    lat = np.asarray(sc.spherical.lat.deg, dtype=float)
    ok = np.isfinite(lon) & np.isfinite(lat)
    if not np.any(ok):
        raise Refusal("the base map's footprint has no sky positions")
    c_lon = float(centre.spherical.lon.deg)
    rel = (lon[ok] - c_lon + 180.0) % 360.0 - 180.0            # longitudes around the centre, so 0/360 does not split the range
    lon_lo, lon_hi = c_lon + rel.min(), c_lon + rel.max()
    lat_lo, lat_hi = float(lat[ok].min()), float(lat[ok].max())
    for pole in (90.0, -90.0):                                 # a pole inside the image: every longitude meets it
        p = SkyCoord(0.0 * u.deg, pole * u.deg, frame=gf).transform_to(base.frame)
        px, py = base.w.world_to_pixel(p)
        if _inside(np.asarray(px), np.asarray(py), base.shape):
            lon_lo, lon_hi = c_lon - 180.0, c_lon + 180.0
            lat_lo, lat_hi = (lat_lo, 90.0) if pole > 0 else (-90.0, lat_hi)
    lon_steps = RA_STEPS if name in ("icrs", "fk5") else DEG_STEPS
    s_lon = _choose(lon_hi - lon_lo, lon_steps)
    s_lat = _choose(max(lat_hi - lat_lo, 1e-9), DEG_STEPS)
    paths, labels = [], []

    def add(group, lo, la, axis, value, step):
        s = SkyCoord(lo * u.deg, la * u.deg, frame=gf).transform_to(base.frame)
        with np.errstate(all="ignore"):
            x, y = base.w.world_to_pixel(s)
        x, y = np.asarray(x, dtype=float), np.asarray(y, dtype=float)
        runs = split_runs(x, y, _inside(x, y, base.shape, CLIP_MARGIN))
        if not runs:
            return
        paths.extend((group, r) for r in runs)
        at = _label_point(runs, base.shape, axis, labels)
        if at is not None:
            labels.append({"text": _label(value, axis, name, step), "x": at[0], "y": at[1], "axis": axis, "edge": at[2]})

    pad_lat = 0.05 * max(lat_hi - lat_lo, s_lat)
    lat_samples = np.linspace(max(-90.0, lat_lo - pad_lat), min(90.0, lat_hi + pad_lat), GRID_SAMPLES)
    k0, k1 = math.floor(lon_lo / s_lon), math.ceil(lon_hi / s_lon)
    if k1 - k0 > 400:
        raise Refusal("the grid would need too many lines")
    seen = set()
    for k in range(k0, k1 + 1):
        v = k * s_lon
        key = round((v % 360.0) / s_lon)
        if key in seen:
            continue
        seen.add(key)
        add(0, np.full(GRID_SAMPLES, v), lat_samples, "lon", v, s_lon)
    pad_lon = 0.05 * max(lon_hi - lon_lo, s_lon)
    lon_samples = np.linspace(lon_lo - pad_lon, lon_hi + pad_lon, GRID_SAMPLES)
    for k in range(math.floor(lat_lo / s_lat), math.ceil(lat_hi / s_lat) + 1):
        v = k * s_lat
        if abs(v) >= 90.0:
            continue
        add(1, lon_samples, np.full(GRID_SAMPLES, v), "lat", v, s_lat)
    paths, simplified = within_budget(paths)
    layout, payload = paths_payload(paths)
    head = {"kind": "grid", **layout, "frame": frame_label(gf), "lon_step_deg": s_lon, "lat_step_deg": s_lat, "labels": labels,
            "simplified": simplified,
            "placement": {"method": f"lines of constant longitude and latitude in {frame_label(gf)}, generated on the sky and placed by the base's WCS"
                          + ("" if base.frame.is_equivalent_frame(gf) else f" after conversion to its {base.label} frame (astropy)"),
                          "from_frame": frame_label(gf), "to_frame": base.label, "from_status": "declared", "to_status": base.status,
                          "same_frame": bool(base.frame.is_equivalent_frame(gf)), "same_dataset": True}}
    return head, payload
