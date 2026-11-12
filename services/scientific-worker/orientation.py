"""Orientation angles on the sky (ADR 0042): what an axial angle means, and how to carry it between frames.

Definitions this module keeps:
- "pixel" frame: counter-clockwise from +axis 1 towards +axis 2, as stored in the file's pixel grid.
- "sky" frame: position angle east of north (IAU) in the file's own celestial frame (RA/Dec in its RADESYS, Galactic, ...),
  measured at the sample. North is the direction of increasing latitude of that frame, so a Galactic-frame file's "sky" angle
  is east of Galactic north, not of celestial north. Comparing two files in different celestial frames goes through
  rotate_pa_frame, which carries the direction between the frames.
- A comparison works on sky-frame B-field orientation in [0, 180). A gradient, and the E-vector of dust polarization, are
  perpendicular to B (+90 degrees); starlight polarization and the B-field itself are not rotated.

No angle here is derived by hand from the projection: the local north direction, the position angle and the frame rotation
all come from astropy (WCS.pixel_to_world / world_to_pixel and SkyCoord.position_angle / directional_offset_by), which apply
the file's projection, CD/PC matrix and any axis flip. The only trigonometry left is turning a pixel offset into an angle and
the doubled-angle algebra of axial statistics.
"""
from __future__ import annotations

import math

import numpy as np

FRAMES = ("pixel", "sky")
# What each declared quantity needs to become the B-field orientation.
QUANTITY_ROTATION = {"b_field": 0.0, "starlight_polarization": 0.0, "gradient": 90.0, "dust_polarization": 90.0}
QUANTITIES = tuple(QUANTITY_ROTATION) + ("unspecified",)

STEP_PIXELS = 0.01           # a probe this long (in pixels) gives the local direction; far below any projection curvature
STEP_DEG = 1e-4              # the same for a probe taken on the sky
DEGENERATE_RESULTANT = 1e-6  # below this the doubled-angle vectors cancel: the orientation is undefined (as in the statistics)


class Undeclared(Exception):
    """A declaration needed for this conversion is missing or unusable. The message names it."""


def check_frame(frame):
    if frame not in FRAMES:
        raise Undeclared("the angle's frame (pixel or sky) is not declared for this map, so it cannot be related to the sky; declare it first")
    return frame


def b_rotation_deg(quantity) -> float:
    """Degrees to add to an angle of this quantity to get the B-field orientation."""
    if quantity not in QUANTITY_ROTATION:
        if quantity == "unspecified":
            raise Undeclared("the angle's quantity is declared as 'unspecified': say whether it is the B-field, a gradient, dust polarization or starlight polarization")
        raise Undeclared("what the angle is (B-field, gradient, dust polarization or starlight polarization) is not declared for this map; declare it first")
    return QUANTITY_ROTATION[quantity]


def celestial_wcs(c):
    """The two-axis celestial WCS of a Cube whose sky axes are its image axes x and y in that order."""
    if not c.celestial or c.wcs is None:
        raise Undeclared("this file has no complete celestial WCS, so an angle cannot be related to the sky")
    if c.wcs.wcs.lng != c.x or c.wcs.wcs.lat != c.y:
        raise Undeclared("the sky axes of this file are not its first two image axes in the order (longitude, latitude)")
    return c.wcs.celestial


def sky_frame(w):
    """The astropy celestial frame the WCS describes (ICRS, FK5, Galactic, ...)."""
    from astropy.wcs.utils import wcs_to_celestial_frame
    try:
        return wcs_to_celestial_frame(w)
    except Exception as e:                       # astropy raises ValueError for a frame it cannot name
        raise Undeclared("the celestial frame of this file cannot be determined from its WCS") from e


def _pa_deg(origin, tip) -> np.ndarray:
    return np.asarray(origin.position_angle(tip).deg, dtype=float) % 360.0


def pixel_to_sky_pa(w, x, y, theta_deg):
    """Position angle (degrees east of north, in w's frame) of the pixel-grid direction theta (counter-clockwise from +x) at
    pixel (x, y). x, y are 0-based pixel coordinates (arrays or scalars); NaN in gives NaN out."""
    x, y, t = np.broadcast_arrays(*(np.asarray(v, dtype=float) for v in (x, y, theta_deg)))
    ok = np.isfinite(t)
    tr = np.radians(np.where(ok, t, 0.0))
    origin = w.pixel_to_world(x, y)
    tip = w.pixel_to_world(x + STEP_PIXELS * np.cos(tr), y + STEP_PIXELS * np.sin(tr))
    return np.where(ok, _pa_deg(origin, tip), np.nan)


def sky_pa_to_pixel(w, x, y, pa_deg):
    """The inverse of pixel_to_sky_pa: the pixel-grid direction (counter-clockwise from +x, degrees in [0, 360)) of the sky
    position angle pa (east of north in w's frame) at pixel (x, y)."""
    import astropy.units as u
    x, y, pa = np.broadcast_arrays(*(np.asarray(v, dtype=float) for v in (x, y, pa_deg)))
    ok = np.isfinite(pa)
    origin = w.pixel_to_world(x, y)
    tip = origin.directional_offset_by(np.where(ok, pa, 0.0) * u.deg, STEP_DEG * u.deg)
    tx, ty = w.world_to_pixel(tip)
    return np.where(ok, np.degrees(np.arctan2(ty - y, tx - x)) % 360.0, np.nan)


def compass(w, x, y) -> dict:
    """Where north and east point in the pixel grid at pixel (x, y) (0-based): the pixel-grid direction of position angle 0 and
    90 degrees, counter-clockwise from +x with y up, [0, 360), by sky_pa_to_pixel (astropy's WCS: projection, CD/PC matrix, flips).
    North is increasing latitude and east increasing longitude of w's own frame. Never assumes north is up."""
    try:
        north, east = (float(sky_pa_to_pixel(w, x, y, pa)) for pa in (0.0, 90.0))
    except Exception:                            # a position off the projection raises or gives NaN; say so instead of drawing
        north = east = float("nan")
    if not (math.isfinite(north) and math.isfinite(east)):
        return {"available": False, "reason": "the file's WCS does not place north and east at the centre of the map"}
    try:
        frame = getattr(sky_frame(w), "name", None)
    except Undeclared:
        frame = None
    return {"available": True, "x": float(x), "y": float(y), "north_deg": north, "east_deg": east, "frame": frame,
            "note": "pixel-grid directions, counter-clockwise from +x with y up (FITS), from astropy's WCS at the centre of the map"}


def compass_of(c) -> dict:
    """compass() at the centre of a Cube's map, or `{"available": False, "reason": ...}` when it has no usable celestial WCS."""
    try:
        w = celestial_wcs(c)
        return compass(w, (c.shape[c.x] - 1) / 2.0, (c.shape[c.y] - 1) / 2.0)
    except Undeclared as e:
        return {"available": False, "reason": str(e)}


def rotate_pa_frame(origin, pa_deg, frame_to):
    """The position angle at `origin` (a SkyCoord, in the frame the angle is measured in) re-expressed east of north of
    `frame_to`. The direction is carried across by transforming the point and a nearby point on that direction."""
    import astropy.units as u
    pa = np.asarray(pa_deg, dtype=float)
    ok = np.isfinite(pa)
    tip = origin.directional_offset_by(np.where(ok, pa, 0.0) * u.deg, STEP_DEG * u.deg)
    return np.where(ok, _pa_deg(origin.transform_to(frame_to), tip.transform_to(frame_to)), np.nan)


def wrap_axial(deg):
    """Degrees into [0, 180). 0 and 180 are the same orientation, so exactly 180 is written as 0."""
    out = np.mod(np.asarray(deg, dtype=float), 180.0)
    return np.where(out >= 180.0, 0.0, out)


def to_sky_b_field(w, x, y, angle_deg, frame, quantity):
    """Sky-frame B-field orientation (degrees east of north in w's frame, in [0, 180)) of an axial angle given in `frame`.

    Refuses, naming the missing declaration, when the frame or the quantity is not declared."""
    check_frame(frame)
    rot = b_rotation_deg(quantity)
    a = np.asarray(angle_deg, dtype=float)
    pa = pixel_to_sky_pa(w, x, y, a) if frame == "pixel" else a
    return wrap_axial(pa + rot)


def doubled(theta_deg, weight):
    """The doubled-angle components Q = w cos 2θ and U = w sin 2θ. Where θ or w has no value, both are NaN."""
    th = np.asarray(theta_deg, dtype=float)
    w = np.asarray(weight, dtype=float)
    ok = np.isfinite(th) & np.isfinite(w)
    t2 = np.radians(2.0 * np.where(ok, th, 0.0))
    wv = np.where(ok, w, np.nan)
    return wv * np.cos(t2), wv * np.sin(t2)


def combine(q, u_, w):
    """(θ in degrees [0, 180), R) from resampled Q, U and the resampled weight w, with R = |(Q, U)| / w in [0, 1].

    R is the resultant length of the weighted unit-vector average: 1 when every sample that contributed had the same
    orientation, near 0 when they cancelled. θ is NaN where R is below DEGENERATE_RESULTANT (no mean orientation exists) and
    where w has no value or is not positive. R is clipped to [0, 1]: it can exceed 1 only by floating-point round-off in an
    interpolation or an FFT."""
    with np.errstate(all="ignore"):
        good = np.isfinite(q) & np.isfinite(u_) & np.isfinite(w) & (w > 0)
        r = np.where(good, np.clip(np.hypot(q, u_) / np.where(good, w, 1.0), 0.0, 1.0), np.nan)
        theta = np.where(good & (r >= DEGENERATE_RESULTANT), wrap_axial(0.5 * np.degrees(np.arctan2(u_, q))), np.nan)
    return theta, r
