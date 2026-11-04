#!/usr/bin/env python3
"""Persistent plane worker (ADR 0008): selective plane reads and WCS readouts for FITS cubes.

Protocol on stdin/stdout. One JSON request per line:
    {"op": "view"  | "plane" | "coord", "path": "/abs/file.fits", "channel": int, "x": int, "y": int}
Each request gets one JSON header line, then exactly header["nbytes"] raw bytes (0 unless op is "plane").
Pixel and channel indices are 0-based on the wire. The path is authorized by the caller; this process still
treats the file as untrusted. It never writes, and never prints anything but protocol lines to stdout.
"""
from __future__ import annotations

import json
import math
import os
import sys
import warnings

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))   # python -P does not add the script dir

import numpy as np
from astropy.io import fits
from astropy.wcs import WCS

from fits_header import axes_shape, clean, read_header

MAX_PLANE_ELEMENTS = 1 << 24      # 16.7M pixels = 64 MiB as float32
MAX_REQUEST_BYTES = 8192
MAX_CHANNELS = 1 << 24
MAX_SPECTRUM_CHANNELS = 1 << 16
MAX_REGION_WORK = 200_000_000     # region pixels x channels read for one spectrum
CUBES_KEPT = 4

# FITS standard default units, used only when CUNIT is absent and then reported as "inferred".
DEFAULT_UNIT = {"VRAD": "m/s", "VOPT": "m/s", "VELO": "m/s", "FREQ": "Hz", "WAVE": "m", "AWAV": "m"}
SPECTRAL_NAME = {"VRAD": "radio velocity", "VOPT": "optical velocity", "VELO": "relativistic velocity",
                 "FREQ": "frequency", "WAVE": "wavelength", "AWAV": "air wavelength"}
VELOCITY = {"VRAD", "VOPT", "VELO"}
# Legacy AIPS spellings such as VELO-LSR. Their radio/optical meaning and reference frame depend on VELREF,
# which many files omit; astropy would silently translate them (to VOPT and LSRK), and we do not.
LEGACY_SUFFIX = {"-LSR", "-HEL", "-OBS"}


class Refused(Exception):
    def __init__(self, code: str, message: str):
        super().__init__(message)
        self.code, self.message = code, message


def has_grid(h: fits.Header, i: int) -> bool:
    """True when axis i declares its own reference value, reference pixel and step.

    wcslib silently defaults missing CRVAL to 0, CRPIX to 0 and CDELT to 1. Using those would invent
    coordinates, so a missing keyword makes the axis unresolved instead.
    """
    return f"CRVAL{i}" in h and f"CRPIX{i}" in h and (f"CDELT{i}" in h or f"CD{i}_{i}" in h)


class Cube:
    """Parsed header facts for one file, keyed by (size, mtime) so a changed file is re-read."""

    def __init__(self, path: str):
        st = os.stat(path)
        self.path, self.sig = path, (st.st_size, st.st_mtime_ns)
        self.h = h = read_header(path)
        self.shape = axes_shape(h)                    # FITS order: shape[0] is axis 1
        n = len(self.shape)
        if n < 2 or all(s <= 1 for s in self.shape):
            raise Refused("unsupported", "the primary HDU holds no image of at least two axes")
        with warnings.catch_warnings():
            warnings.simplefilter("ignore")
            self.spectral_error = False
            try:
                # Only the axes the image really has: real VGT outputs are 2D files that kept the cube's CTYPE3.
                self.wcs = WCS(h, naxis=n)
                self.wcs.wcs.set()
            except Exception:                         # wcslib raises many types on hostile headers
                self.wcs = None
            if self.wcs is None:
                # One bad axis (for example a spectral type that needs a rest frequency) must not discard the
                # valid celestial axes: retry with the first two axes alone and mark the rest unresolved.
                self.spectral_error = True
                try:
                    self.wcs = WCS(h, naxis=[1, 2])
                    self.wcs.wcs.set()
                    if self.wcs.wcs.lng < 0 or self.wcs.wcs.lat < 0:
                        self.wcs = None
                except Exception:
                    self.wcs = None
        self._locate_axes()
        self._flags = None

    # ----- which FITS axis is what, decided from the WCS rather than assumed -----
    def _locate_axes(self):
        n, shape, w = len(self.shape), self.shape, self.wcs
        lng = lat = spec = -1
        if w is not None:
            lng, lat, spec = int(w.wcs.lng), int(w.wcs.lat), int(w.wcs.spec)
        nondeg = [i for i in range(n) if shape[i] > 1]
        if lng >= 0 and lat >= 0:
            x, y = sorted((lng, lat))
        else:
            rest = [i for i in nondeg if i != spec]
            if spec < 0 and len(rest) == 3:
                raise Refused("unsupported", "cannot tell which axis is spectral: no spectral CTYPE and no celestial WCS")
            if len(rest) < 2:
                raise Refused("unsupported", "fewer than two image axes")
            if len(rest) > 2:
                raise Refused("unsupported", "more than two non-spectral axes of length above 1")
            x, y = rest
        chan = spec if spec >= 0 else None
        extra = [i for i in nondeg if i not in (x, y, chan)]
        if chan is None and len(extra) == 1:
            chan, extra = extra[0], []
        if extra:
            raise Refused("unsupported", f"extra axis of length above 1 (axis {extra[0] + 1}); only one channel axis is supported")
        if shape[x] * shape[y] > MAX_PLANE_ELEMENTS:
            raise Refused("unsupported", "plane is larger than the viewer limit")
        if chan is not None and shape[chan] > MAX_CHANNELS:
            raise Refused("unsupported", "too many channels")
        self.x, self.y, self.chan = x, y, chan
        self.lng, self.lat, self.spec = lng, lat, spec
        self.channels = shape[chan] if chan is not None else 1
        self.celestial = lng >= 0 and lat >= 0 and has_grid(self.h, lng + 1) and has_grid(self.h, lat + 1)

    # ----- spectral axis -----
    def spectral_facts(self) -> dict | None:
        if self.chan is None:
            return None
        i = self.chan + 1
        ctype = clean(self.h.get(f"CTYPE{i}")) or None
        base = (ctype or "")[:4]
        legacy = (ctype or "")[4:8] in LEGACY_SUFFIX
        facts = {"fits_axis": i, "ctype": ctype, "name": (ctype if legacy else SPECTRAL_NAME.get(base, ctype or "channel")),
                 "is_spectral": self.spec >= 0}
        coupled = False
        if self.wcs is not None and self.spec >= 0:
            m = self.wcs.pixel_scale_matrix
            coupled = bool(np.any(np.delete(m[self.spec], self.spec) != 0) or np.any(np.delete(m[:, self.spec], self.spec) != 0))
        resolved = self.spec >= 0 and has_grid(self.h, i) and not coupled
        facts["resolved"] = resolved
        if not resolved:
            facts.update(unit=None, unit_status="unresolved", velocity="unresolved",
                         note="the header does not give a complete, separable grid for this axis" if self.spec >= 0 else "the axis is not declared spectral")
            return facts
        declared_unit = self.h.get(f"CUNIT{i}")
        unit = str(self.wcs.wcs.cunit[self.spec]).replace(" ", "") or DEFAULT_UNIT.get(base)
        facts["unit"], facts["unit_status"] = unit, "declared" if declared_unit else ("inferred" if unit else "unresolved")
        if legacy:
            facts["velocity"] = "unresolved"
            facts["note"] = f"{ctype} is a legacy spelling: its velocity convention and frame are not declared; the header values are shown as given"
        elif base in VELOCITY:
            facts["velocity"] = "declared"
        else:
            # A frequency or wavelength axis becomes a velocity only with a rest frequency AND a declared
            # velocity convention (radio, optical, relativistic). FITS headers do not carry the convention.
            facts["velocity"] = "unresolved"
            facts["note"] = "velocity needs a declared convention; none is in the header"
        facts["specsys"] = clean(self.h.get("SPECSYS")) or None
        facts["restfrq"] = clean(self.h.get("RESTFRQ"))
        return facts

    def spectral_value(self, channel: int):
        """World value of a channel in the unit from spectral_facts, or None when unresolved."""
        facts = self.spectral_facts()
        if facts is None or not facts["resolved"]:
            return None
        return self._world(0, 0, channel)[self.spec]

    # ----- world coordinates (astropy/wcslib; never hand-rolled) -----
    def _world(self, x: int, y: int, channel: int):
        pix = [0.0] * len(self.shape)
        pix[self.x], pix[self.y] = x, y
        if self.chan is not None:
            pix[self.chan] = channel
        with warnings.catch_warnings():
            warnings.simplefilter("ignore")
            return self.wcs.all_pix2world([pix], 0)[0]

    def sky_facts(self) -> dict:
        if not self.celestial or self.wcs is None:
            return {"kind": "pixel", "note": "no complete celestial WCS in the header; positions are image pixels only"}
        lon_t, lat_t = clean(self.h.get(f"CTYPE{self.lng + 1}")) or "", clean(self.h.get(f"CTYPE{self.lat + 1}")) or ""
        names = {"RA": ("RA", "Dec"), "GL": ("l", "b"), "EL": ("λ", "β")}.get(lon_t[:2], (lon_t[:4].strip("-"), lat_t[:4].strip("-")))
        frame = (clean(self.h.get("RADESYS")) if lon_t[:2] == "RA" else None) or {"GL": "Galactic", "EL": "Ecliptic"}.get(lon_t[:2])
        status = "declared" if frame else "unresolved"
        eq = self.h.get("EQUINOX")
        if not frame and lon_t[:2] == "RA" and isinstance(eq, (int, float)) and not isinstance(eq, bool):
            frame, status = ("FK5" if eq >= 1984 else "FK4"), "inferred"   # the FITS standard default for a given EQUINOX
        return {"kind": "celestial", "lon_name": names[0], "lat_name": names[1], "projection": lon_t[5:] or None,
                "frame": frame or None, "frame_status": status, "unit": "deg"}

    def coord(self, x: int, y: int, channel: int) -> dict:
        out = {"x": x, "y": y, "channel": channel, "sky": None, "spectral": None}
        if self.celestial and self.wcs is not None:
            world = self._world(x, y, channel)
            lon, lat = float(world[self.lng]), float(world[self.lat])
            if math.isfinite(lon) and math.isfinite(lat):
                out["sky"] = {"lon": lon, "lat": lat}
        facts = self.spectral_facts()
        if facts is not None and facts["resolved"]:
            v = float(self._world(x, y, channel)[self.spec])
            out["spectral"] = {"value": v if math.isfinite(v) else None}
        return out

    # ----- channel flags: a provider convention, never inferred -----
    def flags(self) -> dict:
        if self._flags is None:
            try:
                self._flags = self._read_flags()
            except Exception:
                self._flags = {"declared": False, "channels": [], "note": "the FLAGS extension could not be read; ignored"}
        return self._flags

    def _read_flags(self) -> dict:
        none = {"declared": False, "channels": []}
        if self.chan is None:
            return none
        with fits.open(self.path, memmap=True) as hdul:
            if "FLAGS" not in hdul:
                return none
            hdu = hdul["FLAGS"]
            hh = hdu.header
            if hh.get("NAXIS") != 1 or hh.get("NAXIS1") != self.channels:
                return {**none, "note": "a FLAGS extension exists but does not match the channel axis; ignored"}
            return {"declared": True, "source": "FLAGS extension (non-zero = flagged by the data provider)",
                    "channels": [int(i) for i in np.flatnonzero(np.asarray(hdu.data) != 0)]}

    # ----- one plane, read selectively -----
    def _index(self, channel: int, ys: slice, xs: slice) -> tuple:
        n = len(self.shape)
        idx = []
        for k in range(n):                                # numpy axis k is FITS axis n-k
            f = n - 1 - k
            idx.append(ys if f == self.y else xs if f == self.x else (channel if f == self.chan else 0))
        return tuple(idx)

    def _physical(self, raw: np.ndarray) -> np.ndarray:
        """float64 physical values: BSCALE/BZERO applied, BLANK (integer data) is NaN, never 0."""
        bscale, bzero = self.h.get("BSCALE", 1.0), self.h.get("BZERO", 0.0)
        if not all(isinstance(v, (int, float)) and math.isfinite(v) for v in (bscale, bzero)):
            raise Refused("unreadable", "BSCALE/BZERO are not finite numbers")
        with np.errstate(all="ignore"):
            phys = raw.astype("<f8") * bscale + bzero
            if raw.dtype.kind != "f":
                blank = self.h.get("BLANK")
                if blank is not None:
                    phys[raw == blank] = np.nan
        return phys

    def read_plane(self, channel: int) -> tuple[np.ndarray, int]:
        """(float32 plane, number of finite source values too large for float32, which became infinite)."""
        with fits.open(self.path, memmap=True, do_not_scale_image_data=True) as hdul:
            hdu = hdul[0]
            if hdu.data is None:
                raise Refused("unreadable", "the file holds no image data")
            raw = np.asarray(hdu.section[self._index(channel, slice(None), slice(None))])
        if raw.shape != (self.shape[self.y], self.shape[self.x]):
            raise Refused("unreadable", "unexpected plane shape")
        phys = self._physical(raw)
        with np.errstate(all="ignore"):
            out = np.ascontiguousarray(phys, dtype="<f4")
        return out, int(np.count_nonzero(np.isfinite(phys) & ~np.isfinite(out)))

    # ----- regions -----
    def region_mask(self, req: dict):
        """(shape, clipped bounding box, boolean mask over it, pixel count, unclipped pixel count). A pixel is
        inside when its centre is inside; the region is clipped to the image."""
        nx, ny = self.shape[self.x], self.shape[self.y]
        shape = req.get("shape")
        if shape == "rect":
            x0, y0, x1, y1 = (as_int(req, k, -(1 << 20), 1 << 20) for k in ("x0", "y0", "x1", "y1"))
            x0, x1, y0, y1 = min(x0, x1), max(x0, x1), min(y0, y1), max(y0, y1)
            bx0, bx1, by0, by1 = max(x0, 0), min(x1, nx - 1), max(y0, 0), min(y1, ny - 1)
            requested = (x1 - x0 + 1) * (y1 - y0 + 1)
            inside = None
        elif shape == "circle":
            cx, cy = (as_int(req, k, -(1 << 20), 1 << 20) for k in ("cx", "cy"))
            r = as_int(req, "r", 0, 4096)
            bx0, bx1, by0, by1 = max(cx - r, 0), min(cx + r, nx - 1), max(cy - r, 0), min(cy + r, ny - 1)
            x0, x1, y0, y1 = cx - r, cx + r, cy - r, cy + r
            d = np.arange(-r, r + 1)
            requested = int(np.count_nonzero(d[:, None] ** 2 + d[None, :] ** 2 <= r * r))
            inside = lambda yy, xx: (xx - cx) ** 2 + (yy - cy) ** 2 <= r * r   # pixel centres inside the circle (broadcast, no full grids)
        else:
            raise Refused("bad_request", "shape must be rect or circle")
        if bx0 > bx1 or by0 > by1:
            raise Refused("bad_request", "the region does not overlap the image")
        if inside is None:
            mask = np.ones((by1 - by0 + 1, bx1 - bx0 + 1), dtype=bool)
        else:
            yy, xx = np.ogrid[by0 : by1 + 1, bx0 : bx1 + 1]
            mask = inside(yy, xx)
        npix = int(mask.sum())
        if npix == 0:
            raise Refused("bad_request", "the region contains no pixel centre")
        return shape, (bx0, bx1, by0, by1), mask, npix, requested

    # ----- axial orientation statistics -----
    def axial_stats(self, req: dict, weights: "Cube | None") -> dict:
        """Mean orientation of axial angles via the doubled-angle method (astropy.stats circular statistics on 2*theta).

        theta and theta + 180 deg are the same orientation, so the ordinary mean is never used. Pixels whose angle is not
        finite are excluded. Weights come from a companion map: NaN weight = unknown, the pixel is excluded from the
        weighted result; weight 0 is a real value and keeps the pixel with no influence.
        """
        from astropy.stats import circmean, circvar
        unit = req.get("unit")
        if unit not in ("deg", "rad"):
            raise Refused("bad_request", "unit must be deg or rad")
        if self.channels != 1:
            raise Refused("unsupported", "an orientation map must be a single plane")
        nx, ny = self.shape[self.x], self.shape[self.y]
        if "shape" in req:
            shape, (bx0, bx1, by0, by1), mask, npix, requested = self.region_mask(req)
        else:
            shape, (bx0, bx1, by0, by1), mask, npix, requested = "map", (0, nx - 1, 0, ny - 1), np.ones((ny, nx), dtype=bool), nx * ny, nx * ny
        if npix > MAX_PLANE_ELEMENTS:
            raise Refused("unsupported", "the region is too large")
        ys, xs = slice(by0, by1 + 1), slice(bx0, bx1 + 1)

        def read(cube: "Cube") -> np.ndarray:
            with fits.open(cube.path, memmap=True, do_not_scale_image_data=True) as hdul:
                return cube._physical(np.asarray(hdul[0].section[cube._index(0, ys, xs)]))

        values = read(self)[mask]                                    # in the declared unit
        theta = np.radians(values) if unit == "deg" else values
        out_of_range = int(np.count_nonzero(np.isfinite(theta) & ((theta < -1e-9) | (theta >= np.pi + 1e-9))))
        ok = np.isfinite(theta)
        two = (2.0 * theta)[ok]
        fin = values[np.isfinite(values)]
        fits_radians = bool(unit == "deg" and fin.size and float(np.max(np.abs(fin))) <= math.pi + 1e-9)   # degrees would use a wider range
        result = {"values_fit_radians": fits_radians, "pixels": npix, "angles_finite": int(ok.sum()), "angles_excluded": int(npix - ok.sum()),
                  "outside_zero_to_180": out_of_range, "unweighted": axial_summary(two, None, circmean, circvar), "weighted": None}
        if weights is not None:
            if (weights.shape[weights.x], weights.shape[weights.y]) != (nx, ny) or weights.channels != 1:
                raise Refused("bad_request", "the weight map has a different shape")
            w = read(weights)[mask]
            if np.any(w[np.isfinite(w)] < 0):
                raise Refused("bad_request", "the weight map has negative values")
            both = ok & np.isfinite(w)
            result["weighted"] = axial_summary((2.0 * theta)[both], w[both], circmean, circvar)
            result["weighted"]["unknown_weight_excluded"] = int(np.count_nonzero(ok & ~np.isfinite(w)))
        return result

    # ----- region spectrum: mean of the finite pixels inside the region, per channel -----
    def spectrum(self, req: dict) -> tuple[dict, bytes]:
        nx, ny, nc = self.shape[self.x], self.shape[self.y], self.channels
        if nc > MAX_SPECTRUM_CHANNELS:
            raise Refused("unsupported", "too many channels for a region spectrum")
        shape, (bx0, bx1, by0, by1), mask, npix, requested = self.region_mask(req)
        if npix * nc > MAX_REGION_WORK:
            raise Refused("unsupported", "the region is too large for a spectrum; draw a smaller one")
        mean, count = np.full(nc, np.nan), np.zeros(nc, dtype="<u4")
        ys, xs = slice(by0, by1 + 1), slice(bx0, bx1 + 1)
        with fits.open(self.path, memmap=True, do_not_scale_image_data=True) as hdul:
            hdu = hdul[0]
            for c in range(nc):
                vals = self._physical(np.asarray(hdu.section[self._index(c, ys, xs)]))[mask]
                finite = vals[np.isfinite(vals)]
                count[c] = finite.size
                if finite.size:
                    mean[c] = finite.mean()          # a channel with no finite pixel is NaN (unknown), never 0
        facts = self.spectral_facts()
        resolved = facts is not None and facts["resolved"]
        if resolved:
            pix = np.zeros((nc, len(self.shape)))
            pix[:, self.chan] = np.arange(nc)
            with warnings.catch_warnings():
                warnings.simplefilter("ignore")
                axis = np.asarray(self.wcs.all_pix2world(pix, 0)[:, self.spec], dtype="<f8")
        else:
            axis = np.arange(nc, dtype="<f8")
        size = None
        if self.celestial and self.wcs is not None:
            from astropy.wcs.utils import proj_plane_pixel_scales
            sx, sy = (float(v) * 3600.0 for v in proj_plane_pixel_scales(self.wcs.celestial))
            pixel_area = abs(float(np.linalg.det(self.wcs.celestial.pixel_scale_matrix))) * 3600.0 ** 2   # arcsec^2, valid for skewed grids
            size = {"pixel_scale_arcsec": [sx, sy], "area_arcmin2": npix * pixel_area / 3600.0,
                    "note": "Pixel scale and area are taken at the reference pixel; projection distortion across the region is ignored"}
        payload = axis.astype("<f8").tobytes() + mean.astype("<f8").tobytes() + count.tobytes()
        head = {"ok": True, "op": "spectrum", "origin": 0, "channels": nc, "shape": shape, "nbytes": len(payload),
                "layout": "float64 axis[channels], float64 mean[channels] (NaN = no finite pixel), uint32 count[channels], little-endian",
                "region": {"pixels": npix, "requested_pixels": requested, "clipped": npix != requested,
                          "bbox": {"x0": bx0, "y0": by0, "x1": bx1, "y1": by1}},
                "size": size,
                "axis": {"kind": "spectral" if resolved else "channel", "name": None if facts is None else facts["name"],
                         "unit": facts["unit"] if resolved else None},
                "mask_policy": "Mean of the finite pixels inside the region, per channel. NaN, BLANK and infinite values are excluded and counted; "
                               "no weighting; flagged channels keep their data.",
                "bunit": clean(self.h.get("BUNIT")) if "BUNIT" in self.h else None}
        return head, payload

_cubes: dict[str, Cube] = {}


def cube_for(path: str) -> Cube:
    if os.path.realpath(path) != path:          # the caller sends a resolved path; a swapped-in symlink is refused
        raise Refused("bad_request", "bad request")
    st = os.stat(path)
    c = _cubes.pop(path, None)
    if c is None or c.sig != (st.st_size, st.st_mtime_ns):
        c = Cube(path)
    _cubes[path] = c
    while len(_cubes) > CUBES_KEPT:
        _cubes.pop(next(iter(_cubes)))
    return c


def as_index(req: dict, key: str, upper: int) -> int:
    v = req.get(key)
    if isinstance(v, bool) or not isinstance(v, int) or not 0 <= v < upper:
        raise Refused("bad_request", f"{key} must be an integer in [0, {upper})")
    return v


DEGENERATE_RESULTANT = 1e-6   # mean resultant length of the doubled angles below which no orientation is defined


def axial_summary(two_theta: np.ndarray, weights: "np.ndarray | None", circmean, circvar) -> dict:
    """Mean orientation (degrees in [0, 180)), resultant length R, count and whether the mean is defined."""
    n = int(two_theta.size)
    if n == 0 or (weights is not None and not np.any(weights > 0)):
        return {"n": n, "resultant": None, "mean_deg": None, "defined": False, "reason": "no angle with a usable weight"}
    # R = 1 - circular variance of the doubled angles; the mean orientation is half the circular mean of 2*theta
    r = float(1.0 - circvar(two_theta, weights=weights))
    if not math.isfinite(r) or r < DEGENERATE_RESULTANT:
        return {"n": n, "resultant": r if math.isfinite(r) else None, "mean_deg": None, "defined": False,
                "reason": f"orientations cancel (resultant length below {DEGENERATE_RESULTANT:g})"}
    mean = float((np.degrees(circmean(two_theta, weights=weights)) / 2.0) % 180.0)
    return {"n": n, "resultant": r, "mean_deg": mean, "defined": True, "reason": None}


def as_int(req: dict, key: str, lo: int, hi: int) -> int:
    v = req.get(key)
    if isinstance(v, bool) or not isinstance(v, int) or not lo <= v <= hi:
        raise Refused("bad_request", f"{key} must be an integer")
    return v


def stats(a: np.ndarray) -> dict:
    finite = a[np.isfinite(a)]
    out = {"finite_count": int(finite.size), "nonfinite_count": int(a.size - finite.size),
           "min": None, "max": None, "p01": None, "p99": None}
    if finite.size:
        p01, p99 = np.percentile(finite, [1, 99])
        out.update(min=float(finite.min()), max=float(finite.max()), p01=float(p01), p99=float(p99))
    return out


def handle(req: dict) -> tuple[dict, bytes]:
    op, path = req.get("op"), req.get("path")
    if op not in ("view", "plane", "coord", "spectrum", "axial") or not isinstance(path, str) or not os.path.isabs(path) or len(path) > 4096:
        raise Refused("bad_request", "bad request")
    c = cube_for(path)
    bunit = clean(c.h.get("BUNIT")) if "BUNIT" in c.h else None
    base = {"ok": True, "op": op, "origin": 0, "bunit": bunit, "bunit_status": "declared" if bunit is not None else "unresolved"}
    if op == "view":
        fl = c.flags()
        return {**base, "nbytes": 0, "width": c.shape[c.x], "height": c.shape[c.y], "channels": c.channels,
                "fits_axes": {"x": c.x + 1, "y": c.y + 1, "channel": None if c.chan is None else c.chan + 1},
                "sky": c.sky_facts(), "spectral": c.spectral_facts(), "flags": fl,
                "provenance_note": "axis roles come from the WCS; values are declared by the file, not verified"}, b""
    if op == "axial":
        wp = req.get("weights_path")
        if wp is not None and (not isinstance(wp, str) or not os.path.isabs(wp) or len(wp) > 4096):
            raise Refused("bad_request", "bad request")
        return {**base, "nbytes": 0, "stats": c.axial_stats(req, cube_for(wp) if wp else None),
                "method": "doubled-angle circular statistics (astropy.stats); 0 and 180 degrees are the same orientation"}, b""
    if op == "spectrum":
        head, payload = c.spectrum(req)
        return {**base, **head}, payload
    channel = as_index(req, "channel", c.channels)
    if op == "coord":
        x, y = as_index(req, "x", c.shape[c.x]), as_index(req, "y", c.shape[c.y])
        return {**base, "nbytes": 0, **c.coord(x, y, channel)}, b""
    plane, overflow = c.read_plane(channel)
    fl = c.flags()
    payload = plane.tobytes()
    spectral = c.spectral_facts()
    value = c.spectral_value(channel)
    return {**base, "nbytes": len(payload), "dtype": "<f4", "width": plane.shape[1], "height": plane.shape[0],
            "row0": "FITS y=1 (the bottom row when north is up)", "channel": channel, "channels": c.channels,
            "nan_meaning": "no value: NaN or BLANK in the source (or non-finite after scaling); it is not zero",
            "source_bitpix": clean(c.h.get("BITPIX")), "scaled": "BSCALE" in c.h or "BZERO" in c.h,
            "stats": {**stats(plane), "overflow_count": overflow},
            "spectral_value": None if value is None else float(value),
            "spectral_unit": None if spectral is None else spectral.get("unit"),
            "flagged": (channel in fl["channels"]) if fl["declared"] else None}, payload


def main() -> int:
    out = sys.stdout.buffer
    sys.stdout = sys.stderr                                 # stray prints must never corrupt the protocol
    for line in sys.stdin.buffer:
        try:
            if len(line) > MAX_REQUEST_BYTES:
                raise Refused("bad_request", "request too large")
            req = json.loads(line)
            if not isinstance(req, dict):
                raise Refused("bad_request", "bad request")
            head, payload = handle(req)
        except Refused as e:
            head, payload = {"ok": False, "code": e.code, "error": e.message, "nbytes": 0}, b""
        except (OSError, ValueError, KeyError, TypeError, IndexError, MemoryError) as e:
            head, payload = {"ok": False, "code": "unreadable", "error": "cannot read this file (truncated, corrupt or unsupported)", "nbytes": 0}, b""
        except Exception:                                    # keep the worker alive on anything astropy raises
            head, payload = {"ok": False, "code": "unreadable", "error": "cannot read file", "nbytes": 0}, b""
        try:
            line_out = json.dumps(head, allow_nan=False).encode()
        except ValueError:                                   # a non-finite number leaked into the header
            line_out = json.dumps({"ok": False, "code": "unreadable", "error": "non-finite value in result", "nbytes": 0}).encode()
            payload = b""
        out.write(line_out + b"\n" + payload)
        out.flush()
    return 0


if __name__ == "__main__":
    sys.exit(main())
