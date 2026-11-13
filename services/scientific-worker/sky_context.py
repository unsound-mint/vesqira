"""Sky context (ADR 0072): where a dataset lies on the sky, for the sky panel, in the panel's own frame.

Every position comes from the dataset's celestial WCS as the worker holds it (the corrections in force are already applied to
that WCS), through astropy, and is carried into the panel's frame (ICRS or Galactic) by astropy's frame transformation. A
dataset without a celestial WCS, or in a frame astropy cannot name or this app does not transform, has no footprint: the
reply says why and no position is guessed. Nothing here reads a pixel value.

- footprint: the outline of the image, sampled along its four edges (the pixel edges, -0.5 to n-0.5), so a curved edge stays
  curved when the frames differ. Longitudes are unwrapped around the footprint's centre, so a footprint across 0/360 is one
  continuous outline; the browser wraps them for display.
- point: one pixel's position, for the cursor link.
"""
from __future__ import annotations

import math

import numpy as np

import overlay_geometry as og

PANEL_FRAMES = ("icrs", "galactic")
EDGE_SAMPLES = 24               # per side; an edge of a distorted or rotated frame is drawn as 4 x 24 segments
MAX_EXTENT_DEG = 80.0           # a map reaching further than this from its centre is a survey, not a field: it has no outline here
PLACED = "placed by the dataset's celestial WCS (astropy), carried into the panel's frame by astropy; context only, never a measurement"


class Bad(Exception):
    """The request is malformed (a frame name, a pixel that is not a number)."""


def panel_frame(name):
    from astropy.coordinates import ICRS, Galactic
    if name not in PANEL_FRAMES:
        raise Bad("frame must be icrs or galactic")
    return ICRS() if name == "icrs" else Galactic()


def _sky(c):
    """The dataset's Sky (overlay_geometry.Sky), or the sentence saying why it has no usable celestial frame."""
    try:
        return og.Sky(c, "this dataset"), None
    except og.Refusal as e:
        return None, str(e)


def _facts(sky, frame_name: str) -> dict:
    return {"frame": frame_name, "from_frame": sky.label, "from_status": sky.status, "from_basis": sky.basis,
            "same_frame": bool(sky.frame.is_equivalent_frame(panel_frame(frame_name)))}


def _into_panel(sky, coords, frame_name: str):
    target = panel_frame(frame_name)
    if sky.frame.is_equivalent_frame(target):
        return coords                                     # already in the panel's frame: no transformation, no rounding noise
    return coords.transform_to(target)


def _lonlat(coords):
    return np.asarray(coords.spherical.lon.deg, dtype=float), np.asarray(coords.spherical.lat.deg, dtype=float)


def _unwrap(lon, around: float):
    """Longitudes within 180 degrees of `around`, so an outline that crosses 0/360 is continuous."""
    return around + (np.asarray(lon, dtype=float) - around + 180.0) % 360.0 - 180.0


def _separation_deg(lon0, lat0, lon, lat):
    a0, b0, a, b = map(np.radians, (lon0, lat0, lon, lat))
    s = np.sin(b0) * np.sin(b) + np.cos(b0) * np.cos(b) * np.cos(a - a0)
    return np.degrees(np.arccos(np.clip(s, -1.0, 1.0)))


def footprint(c, frame_name: str) -> dict:
    panel_frame(frame_name)
    sky, why = _sky(c)
    if sky is None:
        return {"footprint": None, "reason": why, "frame": frame_name}
    w, h = sky.shape
    t = np.linspace(-0.5, w - 0.5, EDGE_SAMPLES + 1)[:-1]
    u = np.linspace(-0.5, h - 0.5, EDGE_SAMPLES + 1)[:-1]
    # counter-clockwise on the pixel grid: bottom edge, right edge, top edge (reversed), left edge (reversed)
    xs = np.concatenate([t, np.full(EDGE_SAMPLES, w - 0.5), (w - 0.5) - (t + 0.5), np.full(EDGE_SAMPLES, -0.5)])
    ys = np.concatenate([np.full(EDGE_SAMPLES, -0.5), u, np.full(EDGE_SAMPLES, h - 0.5), (h - 0.5) - (u + 0.5)])
    out = _facts(sky, frame_name)
    out["method"] = f"{PLACED}; {sky.basis}"
    with np.errstate(all="ignore"):
        centre = _into_panel(sky, sky.w.pixel_to_world(np.array([(w - 1) / 2.0]), np.array([(h - 1) / 2.0])), frame_name)
        clon, clat = (float(v[0]) for v in _lonlat(centre))
        lon, lat = _lonlat(_into_panel(sky, sky.w.pixel_to_world(xs, ys), frame_name))
    if not (math.isfinite(clon) and math.isfinite(clat)):
        return {**out, "footprint": None, "reason": "the centre of the image has no position in this WCS"}
    keep = np.isfinite(lon) & np.isfinite(lat)
    if keep.sum() < 3:
        return {**out, "footprint": None, "reason": "the image edge has no position in this WCS"}
    lon, lat = _unwrap(lon[keep], clon), lat[keep]
    extent = float(np.max(_separation_deg(clon, clat, lon, lat)))
    if extent >= MAX_EXTENT_DEG:
        return {**out, "footprint": None, "reason": f"the image reaches {extent:.0f} degrees from its centre: it is a survey, not a field, and has no outline here"}
    out["footprint"] = {"polygon": [[float(a), float(b)] for a, b in zip(lon, lat)], "center": {"lon": clon % 360.0, "lat": clat},
                        "extent_deg": extent, "dropped": int((~keep).sum())}
    out["reason"] = None
    return out


def point(c, frame_name: str, x, y) -> dict:
    panel_frame(frame_name)
    for v in (x, y):
        if isinstance(v, bool) or not isinstance(v, (int, float)) or not math.isfinite(v):
            raise Bad("x and y are pixel positions")
    sky, why = _sky(c)
    if sky is None:
        return {"position": None, "reason": why, "frame": frame_name}
    out = _facts(sky, frame_name)
    with np.errstate(all="ignore"):
        lon, lat = _lonlat(_into_panel(sky, sky.w.pixel_to_world(np.array([float(x)]), np.array([float(y)])), frame_name))
    if not (math.isfinite(lon[0]) and math.isfinite(lat[0])):
        return {**out, "position": None, "reason": "this pixel has no position in the WCS"}
    return {**out, "position": {"lon": float(lon[0]) % 360.0, "lat": float(lat[0])}, "reason": None}
