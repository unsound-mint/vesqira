#!/usr/bin/env python3
"""Persistent plane worker (ADR 0008): selective plane reads and WCS readouts for FITS cubes.

Protocol on stdin/stdout. One JSON request per line:
    {"op": "view"  | "plane" | "coord" | "mask_plane", "path": "/abs/file.fits", "channel": int, "x": int, "y": int}
Each request gets one JSON header line, then exactly header["nbytes"] raw bytes (0 unless op is "plane").
Pixel and channel indices are 0-based on the wire. The path is authorized by the caller; this process still
treats the file as untrusted. It never writes (except the ROHSA index sidecar under a cache folder the caller names, ADR 0046), and never prints anything but protocol lines to stdout.
"""
from __future__ import annotations

import contextlib
import json
import math
import os
import re
import sys
import time
import warnings

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))   # python -P does not add the script dir

import numpy as np
from astropy.io import fits
from astropy.wcs import WCS

import convert
import corrections as corr
import rohsa
import subset
from memcap import limit_memory, over_cap_message
from fits_header import axes_shape, clean, read_header
from srcopen import ROOTS, Refused, fits_open, inside_roots, parse_expect, source_file, stat_source

MAX_PLANE_ELEMENTS = 1 << 24      # 16.7M pixels = 64 MiB as float32
MAX_PREVIEW_STEP = 64
MAX_REQUEST_BYTES = 16384
MAX_CHANNELS = 1 << 24
MAX_TEXT_BYTES = 256 << 10
WALK_MAX_ENTRIES = 5000           # entries in one page of a project walk
WALK_MAX_SECONDS = 10.0           # time spent on one page
WALK_MAX_LIST = 64                # names or patterns the caller may send per list
MAX_SPECTRUM_CHANNELS = 1 << 16
MAX_SKY_POINTS = 64
MAX_REGION_WORK = 1_000_000_000     # region pixels x channels read for one spectrum
SPECTRUM_BLOCK_ELEMENTS = 4_000_000   # pixels x channels per block read (32 MB as float64)
CUBES_KEPT = 4

# FITS standard default units, used only when CUNIT is absent and then reported as "inferred".
DEFAULT_UNIT = {"VRAD": "m/s", "VOPT": "m/s", "VELO": "m/s", "FREQ": "Hz", "WAVE": "m", "AWAV": "m"}
SPECTRAL_NAME = {"VRAD": "radio velocity", "VOPT": "optical velocity", "VELO": "relativistic velocity",
                 "FREQ": "frequency", "WAVE": "wavelength", "AWAV": "air wavelength"}
VELOCITY = {"VRAD", "VOPT", "VELO"}
# Legacy AIPS spellings such as VELO-LSR. Their radio/optical meaning and reference frame depend on VELREF,
# which many files omit; astropy would silently translate them (to VOPT and LSRK), and we do not.
LEGACY_SUFFIX = {"-LSR", "-HEL", "-OBS"}


def fits_array(hdul):
    """The primary data as something sliceable that reads only what is sliced.

    A memory-mapped file slices lazily and an order of magnitude faster than astropy's section reader; any file that is
    not mapped (compressed, or mapping refused) falls back to the section reader, which also reads only what is asked for.
    Callers copy what they slice (np.array) before the file is closed.
    """
    hdu = hdul[0]
    try:
        if getattr(hdul._file, "memmap", False) and getattr(hdul._file, "compression", None) is None:
            # a file shorter than its header says (copied part way, or still being written) cannot be mapped as an array;
            # the section reader still serves the planes that are there
            if hdul._file.size >= hdu._data_offset + hdu._data_size:
                return hdu.data
    except Exception:       # private attributes moved in some astropy version: the section reader is always correct
        pass
    return hdu.section


def has_grid(h: fits.Header, i: int) -> bool:
    """True when axis i declares its own reference value, reference pixel and step.

    wcslib silently defaults missing CRVAL to 0, CRPIX to 0 and CDELT to 1. Using those would invent
    coordinates, so a missing keyword makes the axis unresolved instead.
    """
    return f"CRVAL{i}" in h and f"CRPIX{i}" in h and (f"CDELT{i}" in h or f"CD{i}_{i}" in h)


class Cube:
    """Parsed header facts for one file, keyed by (size, mtime) so a changed file is re-read.

    `corrections` is the researcher's correction state for this file (ADR 0061), already validated by corrections.check_state:
    the WCS keywords are applied to a copy of the header (self.h) and the file's own header stays in self.file_h; a corrected
    brightness unit is kept apart (self.unit_override) and only unit_of reads it. Nothing is written to the file.
    """

    def __init__(self, path: str, corrections: dict | None = None, window: dict | None = None, _plane_limit: bool = True):
        with source_file(path) as (_, f):             # the size and the header come from one open file
            st = os.fstat(f.fileno())
            self.path, self.sig = path, (st.st_size, st.st_mtime_ns)
            self.file_h = read_header(f)
        self.shape = axes_shape(self.file_h)          # FITS order: shape[0] is axis 1
        n = len(self.shape)
        if n < 2 or all(s <= 1 for s in self.shape):
            raise Refused("unsupported", "the primary HDU holds no image of at least two axes")
        self.corrections = corrections
        self.unit_override = corrections.get("bunit") if corrections else None
        self.corrected = set((corrections or {}).get("wcs") or {})
        if self.unit_override is not None and "BUNIT" not in self.file_h:
            raise Refused("unsupported", "a unit correction is for a file that states a BUNIT; this file has none (declare its unit instead)")
        try:
            self.h = h = corr.validate(self.file_h, (corrections or {}).get("wcs") or {}, n) if self.corrected else self.file_h
        except corr.Bad as e:
            raise Refused("unsupported", "the WCS correction is not applied: " + str(e))
        self._build_wcs(h, n)
        self.win = None                               # the resolved window of a virtual subset (subset.py), or None
        self._locate_axes(check_plane=window is None and _plane_limit)
        self.src_channels = self.channels
        self._flags = None
        if window is not None:
            self._apply_window(window)
        if self.corrected:
            # the roles of the axes (x, y, channel) come from the file; a correction that would change them is refused
            plain = Cube(path, _plane_limit=False)
            if (plain.x, plain.y, plain.chan) != (self.x, self.y, self.chan):
                raise Refused("unsupported", "the WCS correction is not applied: it would change which axes are the image and the channel axis")

    def _build_wcs(self, h: fits.Header, n: int):
        """self.wcs for header h (the axes it really has), or None; spectral_error marks a spectral axis wcslib could not set up."""
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

    def _apply_window(self, window: dict):
        """Turn this cube into the subset the window names (subset.py): the shape, channel count, header and WCS become the
        subset's, and every read goes through _index, which maps subset pixels back to the source file's. Nothing is written."""
        try:
            res = subset.resolve(self, window)
        except subset.Bad as e:
            raise Refused("unsupported", str(e))
        wh = subset.window_header(self.h, self, res)
        self.win = res
        self.h = wh
        self.shape = list(self.shape)
        self.shape[self.x], self.shape[self.y] = res["nx"], res["ny"]
        if self.chan is not None:
            self.shape[self.chan] = res["nc"]
            self.channels = res["nc"]
        if res["nx"] * res["ny"] > MAX_PLANE_ELEMENTS:
            raise Refused("unsupported", "plane is larger than the viewer limit")
        self._build_wcs(wh, len(self.shape))
        if self.wcs is None:
            raise Refused("unsupported", "the subset's coordinates could not be set up from the source's WCS")

    # ----- which FITS axis is what, decided from the WCS rather than assumed -----
    def _locate_axes(self, check_plane: bool = True):
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
        if check_plane and shape[x] * shape[y] > MAX_PLANE_ELEMENTS:     # a window onto a large plane is checked at its own size
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
        if f"CUNIT{i}" in self.corrected:
            facts["unit_status"] = "corrected"                 # the researcher's correction (ADR 0061), never the file's
        if legacy:
            facts["velocity"] = "unresolved"
            facts["note"] = f"{ctype} is a legacy spelling: its velocity convention and frame are not declared; the header values are shown as given"
        elif base in VELOCITY:
            facts["velocity"] = "corrected" if f"CTYPE{i}" in self.corrected else "declared"
        else:
            # A frequency or wavelength axis becomes a velocity only with a rest frequency AND a declared
            # velocity convention (radio, optical, relativistic). FITS headers do not carry the convention.
            facts["velocity"] = "unresolved"
            facts["note"] = "velocity needs a declared convention; none is in the header"
        facts["specsys"] = clean(self.h.get("SPECSYS")) or None
        facts["restfrq"] = clean(self.h.get("RESTFRQ"))
        corrected = sorted(k for k in self.corrected if k in (f"CTYPE{i}", f"CUNIT{i}", f"CRVAL{i}", f"CRPIX{i}", f"CDELT{i}", "SPECSYS", "RESTFRQ", "RESTFREQ"))
        if corrected:
            facts["corrected"] = corrected                     # which of the facts above are the researcher's corrections
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
        corrected = sorted(k for k in self.corrected if k in ("RADESYS", "EQUINOX", f"CTYPE{self.lng + 1}", f"CTYPE{self.lat + 1}"))
        if frame and any(k in corrected for k in ("RADESYS", "EQUINOX", f"CTYPE{self.lng + 1}")):
            status = "corrected"                               # the frame comes from the researcher's correction (ADR 0061)
        out = {"kind": "celestial", "lon_name": names[0], "lat_name": names[1], "projection": lon_t[5:] or None,
               "frame": frame or None, "frame_status": status, "unit": "deg"}
        if corrected:
            out["corrected"] = corrected
        return out

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

    def sky_to_pixels(self, points) -> dict:
        """0-based image pixel positions of sky positions given in the file's own celestial frame (degrees), by the file's WCS.

        Also returns the pixel scale and where north points in the image, so a caller can turn an angular size into pixels and
        know whether a box on the sky is aligned with the pixel grid. A position that does not map to a finite pixel is null.
        """
        if not self.celestial or self.wcs is None:
            raise Refused("unsupported", "this file has no complete celestial WCS, so sky positions cannot be placed on it")
        if not isinstance(points, list) or not 1 <= len(points) <= MAX_SKY_POINTS:
            raise Refused("bad_request", f"give between 1 and {MAX_SKY_POINTS} positions")
        for p in points:
            if (not isinstance(p, list) or len(p) != 2 or any(isinstance(v, bool) or not isinstance(v, (int, float)) or not math.isfinite(v) for v in p)
                    or not -360.0 <= p[0] <= 720.0 or not -90.0 <= p[1] <= 90.0):
                raise Refused("bad_request", "each position is [longitude, latitude] in degrees")
        base = np.asarray(self._world(0, 0, 0), dtype=float)       # the other axes stay at the first channel

        def pix(lon: float, lat: float):
            w = base.copy()
            w[self.lng], w[self.lat] = lon, lat
            with warnings.catch_warnings():
                warnings.simplefilter("ignore")
                p = np.asarray(self.wcs.all_world2pix([w], 0)[0], dtype=float)
            x, y = float(p[self.x]), float(p[self.y])
            return (x, y) if math.isfinite(x) and math.isfinite(y) else None

        out = [pix(float(p[0]), float(p[1])) for p in points]
        from astropy.wcs.utils import proj_plane_pixel_scales
        sx, sy = (float(v) * 3600.0 for v in proj_plane_pixel_scales(self.wcs.celestial))
        lon0, lat0 = float(points[0][0]), float(points[0][1])
        step = -1.0 / 60.0 if lat0 > 89.0 else 1.0 / 60.0
        a, b = out[0], pix(lon0, lat0 + step)
        north = None
        if a is not None and b is not None and (b[0] != a[0] or b[1] != a[1]):
            # b lies a minute north of a, or a minute south of it near the pole, where a step north would cross it
            north = math.degrees(math.atan2(b[0] - a[0], b[1] - a[1]) if step > 0 else math.atan2(a[0] - b[0], a[1] - b[1]))
        return {"pixels": [None if q is None else [q[0], q[1]] for q in out], "pixel_scale_arcsec": [sx, sy],
                "north_angle_deg": north, "sky": self.sky_facts(),
                "note": "0-based image pixels, pixel centres at integers; the scale is taken at the reference pixel"}

    def win_report(self) -> dict:
        """What the window resolved to, in the source file's own 0-based pixels and channels, with the rule that chose the channels."""
        return subset.report(self.win, self.src_shape_nx_ny())

    def src_shape_nx_ny(self) -> tuple[int, int, int]:
        """(source width, source height, source channels) of a subset."""
        w = self.win
        return w["src_nx"], w["src_ny"], self.src_channels

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
        with fits_open(self.path) as hdul:
            if "FLAGS" not in hdul:
                return none
            hdu = hdul["FLAGS"]
            hh = hdu.header
            if hh.get("NAXIS") != 1 or hh.get("NAXIS1") != self.src_channels:
                return {**none, "note": "a FLAGS extension exists but does not match the channel axis; ignored"}
            flagged = [int(i) for i in np.flatnonzero(np.asarray(hdu.data) != 0)]
            if self.win is not None:                           # the source's flags, renumbered into the subset's channels
                flagged = [i - self.win["c0"] for i in flagged if self.win["c0"] <= i <= self.win["c1"]]
            return {"declared": True, "source": "FLAGS extension (non-zero = flagged by the data provider)", "channels": flagged}

    # ----- one plane, read selectively -----
    def _index(self, channel: int, ys: slice, xs: slice) -> tuple:
        """The numpy index into the source file of (channel, ys, xs). For a subset the arguments are in the subset's pixels and
        are mapped to the source's here, the only place that does: its offset and, for a spatial step, a strided slice."""
        if self.win is not None:
            w = self.win
            ys, xs = subset.source_slice(ys, w["ny"], w["y0"], w["step"]), subset.source_slice(xs, w["nx"], w["x0"], w["step"])
            if self.chan is not None:
                channel = subset.source_slice(channel, w["nc"], w["c0"], 1) if isinstance(channel, slice) else w["c0"] + channel
        n = len(self.shape)
        idx = []
        for k in range(n):                                # numpy axis k is FITS axis n-k
            f = n - 1 - k
            idx.append(ys if f == self.y else xs if f == self.x else (channel if f == self.chan else 0))
        return tuple(idx)

    def _channel_block(self, hdul, c0: int, c1: int, ys: slice, xs: slice) -> np.ndarray:
        """Physical values of channels [c0, c1) over a region, as an array (channels, y, x)."""
        raw = np.array(fits_array(hdul)[self._index(slice(c0, c1), ys, xs)])
        if self.chan is None:                                  # a map without a channel axis is one channel
            raw = raw[np.newaxis]
            return self._physical(np.swapaxes(raw, 1, 2) if self.x > self.y else raw)
        n = len(self.shape)
        kept = [n - 1 - k for k in range(n) if n - 1 - k in (self.chan, self.y, self.x)]   # FITS axes that survive, in numpy order
        raw = np.moveaxis(raw, kept.index(self.chan), 0)
        if kept.index(self.x) < kept.index(self.y):                                          # file stores x before y
            raw = np.swapaxes(raw, 1, 2)
        return self._physical(raw)

    @contextlib.contextmanager
    def block_reader(self):
        """Yields read(c0, c1, ys, xs): physical float64 values of channels [c0, c1) over a pixel box, shape (channels, y, x),
        from one memory-mapped open of the file (a bounded read, never the whole cube)."""
        with fits_open(self.path, do_not_scale_image_data=True) as hdul:
            if hdul[0].header.get("NAXIS", 0) == 0:
                raise Refused("unreadable", "the file holds no image data")
            yield lambda c0, c1, ys, xs: self._channel_block(hdul, c0, c1, ys, xs)

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
        with fits_open(self.path, do_not_scale_image_data=True) as hdul:
            hdu = hdul[0]
            if hdu.header.get("NAXIS", 0) == 0:     # not hdu.data: touching it loads or maps the whole array, which a short file refuses
                raise Refused("unreadable", "the file holds no image data")
            raw = np.array(fits_array(hdul)[self._index(channel, slice(None), slice(None))])
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
            if shape == "line":
                raise Refused("bad_request", "a line has no area: it cannot be averaged over; use it for a position-velocity diagram")
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
            with fits_open(cube.path, do_not_scale_image_data=True) as hdul:
                return cube._physical(np.array(fits_array(hdul)[cube._index(0, ys, xs)]))

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

    # ----- masks applied to a region (ADR 0055) -----
    def region_masks(self, req: dict):
        """The masks the request names, opened (close with __exit__), or None. The caller (the API) has already checked that they belong to
        the dataset and are fresh; the policy rules (no default, `missing` exclude-only, no weights here) are the shared ones in masks.py."""
        specs = req.get("masks")
        if not specs:
            return None
        import masks as mk
        try:
            return mk.Applied(self, mk.check_specs(specs), False).__enter__()
        except mk.MaskError as e:
            raise Refused("unsupported", str(e))

    # ----- region spectrum: mean of the finite pixels inside the region, per channel -----
    def spectrum(self, req: dict) -> tuple[dict, bytes]:
        ap = self.region_masks(req)
        try:
            return self._spectrum(req, ap)
        finally:
            if ap is not None:
                ap.__exit__(None, None, None)

    def region_histogram(self, req: dict) -> dict:
        """Counts of the finite values of one channel inside a region, in `bins` equal steps from `min` to `max` (the largest value falls in
        the last bin; the same rule as the browser's count of the plane it holds), with the masks the request names applied per mask."""
        channel = as_index(req, "channel", self.channels)
        lo, hi, bins = req.get("min"), req.get("max"), req.get("bins")
        if any(isinstance(v, bool) or not isinstance(v, (int, float)) or not math.isfinite(v) for v in (lo, hi)):
            raise Refused("bad_request", "min and max must be finite numbers")
        if isinstance(bins, bool) or not isinstance(bins, int) or not 1 <= bins <= 1024:
            raise Refused("bad_request", "bins must be an integer from 1 to 1024")
        shape, (bx0, bx1, by0, by1), mask, npix, requested = self.region_mask(req)
        plane, _ = self.read_plane(channel)
        ys, xs = slice(by0, by1 + 1), slice(bx0, bx1 + 1)
        vals = plane[ys, xs][mask]
        no_value = int(vals.size - np.count_nonzero(np.isfinite(vals)))      # counted before any mask, and apart from them
        ap = self.region_masks(req)
        try:
            if ap is not None:
                ex, _ = ap.plane(channel, vals, (ys, xs, mask))
                vals = vals[~ex]                                              # a masked pixel is left out; it is never turned into zero
        finally:
            if ap is not None:
                ap.__exit__(None, None, None)
        fin = vals[np.isfinite(vals)].astype(np.float64)
        counts = np.zeros(bins, dtype=np.int64)
        if not hi > lo:
            counts[0] = fin.size
        else:
            inside = fin[(fin >= lo) & (fin <= hi)]
            np.add.at(counts, np.minimum(bins - 1, np.floor((inside - lo) * (bins / (hi - lo))).astype(np.int64)), 1)
        out = {"channel": channel, "min": lo, "max": hi, "bins": bins, "counts": counts.tolist(), "pixels": npix, "no_value": no_value,
               "region": {"pixels": npix, "requested_pixels": requested, "clipped": npix != requested}}
        if ap is not None:
            import masks as mk
            out["masks"] = ap.record()
            out["excluded_by_mask"] = ap.unique_excluded
            out["policy"] = mk.masks_policy_sentence(ap.specs)
        return out

    def _spectrum(self, req: dict, ap) -> tuple[dict, bytes]:
        nx, ny, nc = self.shape[self.x], self.shape[self.y], self.channels
        if nc > MAX_SPECTRUM_CHANNELS:
            raise Refused("unsupported", "too many channels for a region spectrum")
        shape, (bx0, bx1, by0, by1), mask, npix, requested = self.region_mask(req)
        if npix * nc > MAX_REGION_WORK:
            raise Refused("unsupported", "the region is too large for a spectrum; draw a smaller one")
        mean, count = np.full(nc, np.nan), np.zeros(nc, dtype="<u4")
        no_value = 0
        ys, xs = slice(by0, by1 + 1), slice(bx0, bx1 + 1)
        # Read a block of channels per call: one strided read of the region over K channels instead of K separate reads,
        # with the per-channel mean taken in one vectorised step. K is bounded so a block stays a few tens of MB.
        block = max(1, min(nc, SPECTRUM_BLOCK_ELEMENTS // ((bx1 - bx0 + 1) * (by1 - by0 + 1))))
        with fits_open(self.path, do_not_scale_image_data=True) as hdul:
            hdu = hdul[0]
            for c0 in range(0, nc, block):
                c1 = min(nc, c0 + block)
                vals = self._channel_block(hdul, c0, c1, ys, xs)[:, mask]          # (channels, region pixels)
                if ap is not None:
                    no_value += int(vals.size - np.count_nonzero(np.isfinite(vals)))   # counted before any mask, and apart from them
                    ex = np.stack([ap.plane(c0 + i, vals[i], (ys, xs, mask))[0] for i in range(c1 - c0)])
                    vals = np.where(ex, np.nan, vals)                      # a masked pixel is left out; it is never turned into zero
                finite = np.isfinite(vals)
                n = finite.sum(axis=1)
                count[c0:c1] = n
                total = np.where(finite, vals, 0.0).sum(axis=1)
                with np.errstate(invalid="ignore", divide="ignore"):
                    mean[c0:c1] = np.where(n > 0, total / np.maximum(n, 1), np.nan)   # a channel with no finite pixel is NaN (unknown), never 0
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
        if ap is not None:
            import masks as mk
            head["masks"] = ap.record()
            head["no_value"] = no_value
            head["excluded_by_mask"] = ap.unique_excluded
            head["mask_policy"] += " " + mk.masks_policy_sentence(ap.specs)
        return head, payload

_cubes: dict[tuple, Cube] = {}


def cube_for(path: str, window: dict | None = None) -> Cube:
    """The Cube of a file, with the corrections the current request carries for it (ADR 0061). The cache key includes the
    corrections' hash, so a corrected, an uncorrected and a reverted reading of one file are never served for each other."""
    if os.path.realpath(path) != path:          # the caller sends a resolved path; a swapped-in symlink is refused
        raise Refused("bad_request", "bad request")
    st = stat_source(path)
    cs = corr.CURRENT.get(path)
    key = (path, cs["hash"] if cs else None, json.dumps(window, sort_keys=True) if window is not None else None)   # a subset is not its source
    c = _cubes.pop(key, None)
    if c is None or c.sig != (st.st_size, st.st_mtime_ns):
        c = Cube(path, cs, window)
    _cubes[key] = c
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


def block_mean(a: np.ndarray, step: int) -> np.ndarray:
    """Float32 plane reduced by `step` in each direction: the mean of the finite pixels of each block, NaN where a block has
    none (never 0). Edge blocks that are smaller than the rest average only the pixels they have."""
    h, w = a.shape
    ph, pw = -h % step, -w % step
    x = np.pad(a.astype("<f8"), ((0, ph), (0, pw)), constant_values=np.nan)
    blocks = x.reshape(x.shape[0] // step, step, x.shape[1] // step, step)
    finite = np.isfinite(blocks)
    n = finite.sum(axis=(1, 3))
    total = np.where(finite, blocks, 0.0).sum(axis=(1, 3))
    with np.errstate(all="ignore"):
        out = np.where(n > 0, total / np.maximum(n, 1), np.nan)
    return np.ascontiguousarray(out, dtype="<f4")


def stats(a: np.ndarray) -> dict:
    finite = a[np.isfinite(a)]
    out = {"finite_count": int(finite.size), "nonfinite_count": int(a.size - finite.size),
           "min": None, "max": None, "p01": None, "p99": None}
    if finite.size:
        p01, p99 = np.percentile(finite, [1, 99])
        out.update(min=float(finite.min()), max=float(finite.max()), p01=float(p01), p99=float(p99))
    return out


def authorize(path: str, want: str = "file") -> str:
    """The real path of `path` if it lies inside the roots and is a regular file (or a directory when want="dir").

    Containment is checked on the resolved path, so a symlink pointing out of the roots is refused. Every refusal is
    the same message, whatever the reason, so callers cannot probe for files outside the roots. This is the check, not the
    open: a source file is read only through srcopen (open_source, source_file, fits_open), which opens the resolved path
    component by component without following a link, so a component swapped after this check is refused at the open.
    """
    refused = Refused("forbidden", "not available")
    if not isinstance(path, str) or not os.path.isabs(path) or len(path) > 4096 or "\0" in path:
        raise Refused("bad_request", "bad request")
    real = os.path.realpath(path)
    if not inside_roots(real) or not (os.path.isdir(real) if want == "dir" else os.path.isfile(real)):
        raise refused
    return real


def _name_patterns(req: dict, key: str) -> list:
    v = req.get(key, [])
    if not isinstance(v, list) or len(v) > WALK_MAX_LIST or not all(isinstance(x, str) and 0 < len(x) <= 200 for x in v):
        raise Refused("bad_request", "bad request")
    return v


def _match_any(patterns: list, name: str) -> bool:
    import fnmatch
    n = name.lower()
    return any(fnmatch.fnmatchcase(n, p.lower()) for p in patterns)


def _skip_glob(globs: list, rel: str) -> bool:
    """The simple project.toml skip patterns (`dir/**`, `**/name`, a bare name pattern). Anything else is left to the
    caller, which applies every pattern again; skipping here only saves walking."""
    import fnmatch
    for g in globs:
        g = g.removeprefix("./").rstrip("/")
        if g.endswith("/**"):
            base = g[:-3]
            if rel == base or rel.startswith(base + "/"):
                return True
        elif g.startswith("**/"):
            if fnmatch.fnmatchcase(rel.rsplit("/", 1)[-1], g[3:]):
                return True
        elif "/" not in g and fnmatch.fnmatchcase(rel.rsplit("/", 1)[-1], g):
            return True
    return False


def walk_page(req: dict) -> dict:
    """One page of a depth-first, name-sorted walk of a project folder, for the caller's discovery.

    The folder must lie inside the roots (the caller adds it to them when the project is opened). Symbolic links are
    never followed: a link is reported as inside, outside the folder, or dangling. The caller's rules arrive with the
    request and are applied before anything is statted: names in skip_names and folders matching skip_globs are
    reported but not entered; names matching never_files / never_dirs are reported by name only (not statted, not
    read, not entered); folders ending in a leaf_dir_suffixes entry (such as .zarr) are reported and not entered.
    `after` is the path of the last entry of the previous page; the walk resumes right after it."""
    root = authorize(req.get("path"), "dir")
    skip_names = set(_name_patterns(req, "skip_names"))
    never_files, never_dirs = _name_patterns(req, "never_files"), _name_patterns(req, "never_dirs")
    skip_globs, leaf = _name_patterns(req, "skip_globs"), [x.lower() for x in _name_patterns(req, "leaf_dir_suffixes")]
    after = req.get("after", "")
    if not isinstance(after, str) or len(after) > 4096 or "\0" in after or after.startswith("/") or ".." in after.split("/"):
        raise Refused("bad_request", "bad request")
    limit = req.get("limit", WALK_MAX_ENTRIES)
    if isinstance(limit, bool) or not isinstance(limit, int) or not 1 <= limit <= WALK_MAX_ENTRIES:
        raise Refused("bad_request", f"limit must be an integer in [1, {WALK_MAX_ENTRIES}]")
    import time
    deadline = time.monotonic() + WALK_MAX_SECONDS
    out: list = []
    state = {"last": None, "stopped": False}

    def emit(rel: str, rec: dict) -> bool:
        out.append({"p": rel, **rec})
        state["last"] = rel
        if len(out) >= limit or time.monotonic() > deadline:
            state["stopped"] = True
        return state["stopped"]

    def rec(dir_abs: str, rel_dir: str, resume: list) -> None:
        try:
            with os.scandir(dir_abs) as it:
                entries = sorted(it, key=lambda e: e.name)
        except OSError:
            return
        for e in entries:
            name = e.name
            if resume:
                if name < resume[0]:
                    continue
                if name == resume[0]:                            # emitted on an earlier page: only look inside it
                    below, resume = resume[1:], []
                    if _enterable(e, rel_dir, name):
                        rec(e.path, rel_dir + "/" + name if rel_dir else name, below)
                        if state["stopped"]:
                            return
                    continue
                resume = []
            rel = rel_dir + "/" + name if rel_dir else name
            try:
                is_link = e.is_symlink()
                is_dir = e.is_dir(follow_symlinks=False)
            except OSError:
                continue
            if is_link:
                if _match_any(never_dirs, name) or _match_any(never_files, name):
                    stop = emit(rel, {"link": "never"})        # by name only; where it points is not looked at
                else:
                    real = os.path.realpath(e.path)
                    state_ = "dangling" if not os.path.lexists(real) else ("inside" if real == root or real.startswith(root.rstrip(os.sep) + os.sep) else "outside")
                    stop = emit(rel, {"link": state_})
                if stop:
                    return
                continue
            if is_dir:
                never = _match_any(never_dirs, name)
                if never or name in skip_names or _skip_glob(skip_globs, rel) or name.lower().endswith(tuple(leaf)):
                    if emit(rel, {"dir": True, "leaf": True}):
                        return
                    continue
                if emit(rel, {"dir": True}):
                    return
                rec(e.path, rel, [])
                if state["stopped"]:
                    return
                continue
            if _match_any(never_files, name):
                if emit(rel, {"never": True}):                  # no stat: its size and time are not looked at
                    return
                continue
            try:
                st = e.stat(follow_symlinks=False)
            except OSError:
                continue
            if emit(rel, {"size": st.st_size, "mtime_ns": st.st_mtime_ns}):
                return

    def _enterable(e, rel_dir: str, name: str) -> bool:
        try:
            if e.is_symlink() or not e.is_dir(follow_symlinks=False):
                return False
        except OSError:
            return False
        rel = rel_dir + "/" + name if rel_dir else name
        return not (_match_any(never_dirs, name) or name in skip_names or _skip_glob(skip_globs, rel) or name.lower().endswith(tuple(leaf)))

    rec(root, "", [x for x in after.split("/") if x])
    return {"ok": True, "op": "walk", "nbytes": 0, "root": root, "entries": out,
            "next": state["last"] if state["stopped"] else None}


def canonical_dir(path: str) -> dict:
    """The real path of a folder the owner is about to open as a project, and this account's home. It is the one
    request that is answered outside the roots: opening a folder is what adds it to them, and the answer is only
    whether the path is a folder and where it really is."""
    if not isinstance(path, str) or not os.path.isabs(path) or len(path) > 4096 or "\0" in path:
        raise Refused("bad_request", "bad request")
    real = os.path.realpath(path)
    return {"ok": True, "op": "canon", "nbytes": 0, "real": real, "is_dir": os.path.isdir(real),
            "home": os.path.realpath(os.path.expanduser("~"))}
COMPLETE_MAX_ENTRIES = 50
COMPLETE_MAX_READ = 2000
COMPLETE_BUDGET_S = 0.2


def complete_names(req: dict) -> dict:
    """Names in one folder, for path completion. Same rules as the local API route.

    Answers names and whether each is a folder; never contents, sizes or anything outside the one folder asked for.
    Deliberately not limited to ROOTS (choosing a folder to open needs to see folders that are not readable yet);
    reading file contents stays limited to ROOTS in every other op.
    """
    import time
    prefix, kind, exts = req.get("prefix"), req.get("kind") or "any", req.get("ext") or []
    if (not isinstance(prefix, str) or len(prefix) > 4096 or "\0" in prefix or kind not in ("dir", "file", "any")
            or not isinstance(exts, list) or len(exts) > 8
            or not all(isinstance(e, str) and 2 <= len(e) <= 16 and e.startswith(".") and "/" not in e and "\0" not in e for e in exts)):
        raise Refused("bad_request", "bad request")
    exts = [e.lower() for e in exts]
    if prefix == "~" or prefix.startswith("~/"):
        rest = prefix[1:].lstrip("/")
        prefix = os.path.join(os.path.expanduser("~"), rest) + ("/" if not rest or prefix.endswith("/") else "")
    if not os.path.isabs(prefix):
        raise Refused("bad_request", "bad request")
    if prefix.endswith("/"):
        base, last = os.path.normpath(prefix), ""
    else:
        base, last = os.path.normpath(os.path.dirname(prefix)), os.path.basename(prefix)
    if base.startswith("//"):
        base = base[1:]                                          # normpath keeps a POSIX double slash
    out = {"ok": True, "op": "complete", "nbytes": 0, "base": base, "entries": [], "truncated": False}
    deadline = time.monotonic() + COMPLETE_BUDGET_S
    names = []
    try:
        with os.scandir(base) as it:
            for e in it:
                names.append(e)
                if len(names) >= COMPLETE_MAX_READ or time.monotonic() > deadline:
                    out["truncated"] = True
                    break
    except OSError:
        return out                                               # missing or unreadable: an empty answer, not an error
    hidden = last.startswith(".")

    def match(fold: bool):
        for e in names:
            n = e.name
            if not hidden and n.startswith("."):
                continue
            if (n.lower().startswith(last.lower())) if fold else n.startswith(last):
                yield e
    m = list(match(False))
    if not m and last:
        m = list(match(True))
    entries = []
    for e in m:
        try:
            is_dir = e.is_dir(follow_symlinks=False)
            if e.is_symlink():
                if time.monotonic() > deadline:
                    out["truncated"] = True
                else:
                    is_dir = os.path.isdir(e.path)               # one stat of the entry itself, never a walk into it
        except OSError:
            is_dir = False
        if not is_dir and (kind == "dir" or (exts and not e.name.lower().endswith(tuple(exts)))):
            continue
        entries.append({"name": e.name[:255], "path": os.path.join(base, e.name)[:4096], "dir": is_dir})
    entries.sort(key=lambda x: (not x["dir"], x["name"]))
    if len(entries) > COMPLETE_MAX_ENTRIES:
        entries, out["truncated"] = entries[:COMPLETE_MAX_ENTRIES], True
    out["entries"] = entries
    return out


def host_op(op: str, req: dict) -> tuple[dict, bytes]:
    """Operations used when this worker runs on a remote host (and by the local API for registration)."""
    if op == "ping":
        import astropy
        return {"ok": True, "op": op, "nbytes": 0, "python": sys.version.split()[0], "astropy": astropy.__version__,
                "numpy": np.__version__, "roots": ROOTS, "enforces_roots": ROOTS is not None}, b""
    if op == "complete":
        return complete_names(req), b""
    path = req.get("path")
    if op == "walk":
        return walk_page(req), b""
    if op == "canon":
        return canonical_dir(path), b""
    if op == "list":
        real = authorize(path, "dir")
        entries = []
        with os.scandir(real) as it:
            for e in sorted(it, key=lambda e: e.name)[:2000]:
                try:
                    target = os.path.realpath(e.path)
                    if not inside_roots(target):
                        continue                                 # a link out of the roots is not listed
                    if os.path.isdir(target):
                        entries.append({"name": e.name[:255], "kind": "dir"})
                    elif os.path.isfile(target):
                        entries.append({"name": e.name[:255], "kind": "file", "size": os.stat(target).st_size})
                except OSError:
                    continue
                if len(entries) >= 500:
                    break
        return {"ok": True, "op": op, "nbytes": 0, "path": real, "entries": entries}, b""
    real = authorize(path)
    st = stat_source(real)                         # size and time of the file as opened, never of a link swapped in since the check
    if op == "resolve":
        return {"ok": True, "op": op, "nbytes": 0, "real": real, "size": st.st_size, "mtime_ns": st.st_mtime_ns}, b""
    if op == "stat":
        return {"ok": True, "op": op, "nbytes": 0, "size": st.st_size, "mtime_ns": st.st_mtime_ns}, b""
    if op == "readtext":
        # small text files only (run metadata, configs): bounded, and only inside the roots
        # with "max" the first max bytes are returned and `truncated` says whether the file was longer (a job script's header)
        limit = req.get("max")
        if limit is not None and (isinstance(limit, bool) or not isinstance(limit, int) or not 1 <= limit <= MAX_TEXT_BYTES):
            raise Refused("bad_request", "bad request")
        if limit is None and st.st_size > MAX_TEXT_BYTES:
            raise Refused("unsupported", "the file is too large to read as text")
        with source_file(real) as (_, f):
            data = f.read((limit or MAX_TEXT_BYTES) + 1)
        truncated = limit is not None and len(data) > limit
        if truncated:
            data = data[:limit]
        return {"ok": True, "op": op, "nbytes": 0, "text": data.decode("utf-8", "replace"), "truncated": truncated, "size": st.st_size, "mtime_ns": st.st_mtime_ns}, b""
    if op == "fingerprint":
        # the sampled fingerprint (ADR 0053): about 1 MiB read however large the file is; never called a full hash
        import fingerprint as fp
        return {"ok": True, "op": op, "nbytes": 0, "algorithm": fp.ALGORITHM, "sampled": fp.sampled(real), "size": st.st_size, "mtime_ns": st.st_mtime_ns}, b""
    if op == "fingerprint_full":
        # the SHA-256 of every byte, refused above a cap; the API kills this (heavy) lane's process when the caller cancels
        import fingerprint as fp
        cap = req.get("max_bytes", fp.DEFAULT_FULL_CAP)
        if isinstance(cap, bool) or not isinstance(cap, int) or not 1 <= cap <= fp.MAX_FULL_CAP:
            raise Refused("bad_request", "bad request")
        got = fp.full(real, cap)
        if got is None:
            raise Refused("unsupported", "the file is larger than the cap for a full hash")
        return {"ok": True, "op": op, "nbytes": 0, "sha256": got[0], "bytes": got[1], "size": st.st_size, "mtime_ns": st.st_mtime_ns}, b""
    if op == "describe":
        # Local registration: the head is exactly what `fits_header.py <path>` prints (no op/size fields), and a refusal
        # carries the same short sanitised message, so the catalog row and the error text do not depend on the transport.
        from fits_header import describe
        from healpix_pol import HealpixRefused
        try:
            return describe(real), b""
        except HealpixRefused as e:
            # a HEALPix map that does not state its convention or coordinate system (ADR 0067): the sentence is the answer, whole
            raise Refused("refused", clean(str(e), 300))
        except (OSError, ValueError, KeyError, TypeError) as e:
            raise Refused("unreadable", clean(str(e), 120))
    if op == "header":
        from fits_header import describe
        from healpix_pol import HealpixRefused
        try:
            return {**describe(real), "nbytes": 0, "op": op, "size": st.st_size, "mtime_ns": st.st_mtime_ns}, b""
        except HealpixRefused as e:
            raise Refused("refused", clean(str(e), 300))
    raise Refused("bad_request", "bad request")


MAX_CARDS = 3000


def header_cards(path: str, window: dict | None = None) -> dict:
    """The primary header exactly as the file states it, one 80-column card per entry, for reading and export.

    Nothing is interpreted: this is the file's own text (control characters replaced, count bounded). For a virtual subset
    (a window onto the file) it is the header the subset would have if written: the shifted WCS, with corrections applied and
    a COMMENT saying it is a window, not a file."""
    from fits_header import SAFE, read_header
    if window is not None:
        c = cube_for(path, window)
        h = c.h.copy()
        h.add_comment("Vesqira virtual subset: a window onto a source file, shown with the WCS it would have as a file. Not a file on disk.")
    else:
        h = read_header(path)
    cards, truncated = [], False
    for i, card in enumerate(h.cards):
        if i >= MAX_CARDS:
            truncated = True
            break
        try:
            text = card.image
        except Exception:
            text = f"{card.keyword} = <unreadable card>"
        cards.append(SAFE.sub("?", text)[:80].rstrip())
    return {"ok": True, "op": "cards", "nbytes": 0, "cards": cards, "truncated": truncated}


MAX_DECLARED_UNIT = 40


def unit_of(c: "Cube", declared) -> tuple:
    """(unit, status) of a file's values: a unit the researcher corrected (ADR 0061) is "corrected"; otherwise what the file
    says always wins and a declared unit only fills a gap."""
    if c.unit_override is not None:
        return c.unit_override, "corrected"
    if "BUNIT" in c.h:
        return clean(c.h.get("BUNIT")), "declared"
    if declared is not None:
        return declared, "user"                              # declared by the person, not by the file
    return None, "unresolved"


def abs_path(v) -> str:
    if not isinstance(v, str) or not os.path.isabs(v) or len(v) > 4096:
        raise Refused("bad_request", "bad request")
    return v


def link(a: "Cube", req: dict) -> dict:
    """What linking map A to map B needs, in two independent parts so a refused one never blocks the other (ADR 0042,
    section 4). `sky`: an affine approximation of A pixel -> B pixel over the visible region of A, with its measured error, and
    exact positions of a few points (only when a region or points are sent). `spectral`: A channel -> B's nearest channel by
    world value (only when `channels` is true). Each part is null when not asked for, or carries `refused` and the reason.
    The browser never computes WCS: it applies the affine the worker states, and says its error."""
    import overlay_geometry as og
    b = cube_for(abs_path(req.get("other_path")))
    out = {"sky": None, "spectral": None}
    region, points = req.get("region"), req.get("points")
    if region is not None or points is not None:
        def num(v):
            return isinstance(v, (int, float)) and not isinstance(v, bool) and math.isfinite(v)
        if region is None:
            region = {"x0": -0.5, "x1": a.shape[a.x] - 0.5, "y0": -0.5, "y1": a.shape[a.y] - 0.5}
        if not (isinstance(region, dict) and all(num(region.get(k)) for k in ("x0", "x1", "y0", "y1"))):
            raise Refused("bad_request", "region must have finite x0, x1, y0 and y1")
        if points is None:
            points = []
        if not (isinstance(points, list) and len(points) <= og.LINK_MAX_POINTS
                and all(isinstance(p, list) and len(p) == 2 and num(p[0]) and num(p[1]) for p in points)):
            raise Refused("bad_request", f"points must be at most {og.LINK_MAX_POINTS} pairs of finite numbers")
        try:
            out["sky"] = og.link_sky(a, b, region, points)
        except og.Refusal as e:
            out["sky"] = {"refused": str(e)}
    if req.get("channels") is True:
        out["spectral"] = convert.link_channels(a, b)
    elif req.get("channels") not in (None, False):
        raise Refused("bad_request", "channels must be true or false")
    return out


def overlay_geometry(base: "Cube", req: dict) -> tuple[dict, bytes]:
    """Geometry of one overlay on `base`, in base pixels (overlay_geometry.py). The caller (the API) sends the overlay as the
    scene states it, already validated, plus what the catalog declares about the overlay dataset: its unit for a file without
    BUNIT, whether it is declared axial, and for an orientation map its unit, frame and quantity. Nothing is taken from the
    browser that the catalog declares."""
    import overlay_geometry as og
    spec = req.get("overlay")
    if not isinstance(spec, dict) or spec.get("kind") not in ("contours", "orientation", "beam", "grid"):
        raise Refused("bad_request", "bad request")
    kind = spec["kind"]
    ov_path = abs_path(req.get("overlay_path"))
    ov = cube_for(ov_path)
    same = ov_path == base.path
    who = "the base map" if same else "the overlay map"
    try:
        if kind == "contours":
            if req.get("overlay_axial"):
                raise og.Refusal("the overlay is declared as an orientation map: contours of angles are not meaningful (0 and 180 degrees are the same)")
            head, payload = og.contours(base, ov, spec, unit_of(ov, declared_unit({"declared_bunit": req.get("overlay_declared_bunit")})), same)
        elif kind == "orientation":
            interp = req.get("overlay_interp")
            if not isinstance(interp, dict):
                raise og.Refusal("the overlay is not declared as an orientation map")
            rp = req.get("reliability_path")
            head, payload = og.orientation_samples(base, ov, cube_for(abs_path(rp)) if rp is not None else None, spec, interp, req.get("spacing"), same)
        elif kind == "beam":
            head, payload = og.beam(base, ov, spec, same, who)
        else:
            if not same:
                raise og.Refusal("a coordinate grid is drawn from the base map's own WCS")
            head, payload = og.grid(base, spec)
    except og.Refusal as e:
        raise Refused("unsupported", str(e))
    return {**head, "nbytes": len(payload), "dtype": "<f4", "coords": og.COORDS}, payload


def overlay_catalog(req: dict) -> tuple[dict, bytes]:
    """Markers of a point catalog on the base map (overlay_geometry.catalog). A separate op from overlay_geometry because the table
    is read on the heavy lane (ADR 0044), and the declaration that says which columns are the position and the angle comes from the
    API's stored declaration, never from the browser."""
    import overlay_geometry as og
    import catalog_table as ct
    spec = req.get("overlay")
    if not isinstance(spec, dict) or spec.get("kind") != "catalog" or not isinstance(req.get("declaration"), dict):
        raise Refused("bad_request", "bad request")
    base_path, table_path = abs_path(req.get("path")), abs_path(req.get("overlay_path"))
    base = cube_for(authorize(base_path))
    try:
        head, payload = og.catalog(base, ct.load(authorize(table_path)), req["declaration"], spec)
    except og.Refusal as e:
        raise Refused("unsupported", str(e))
    except ct.Bad as e:
        raise Refused("unreadable", str(e))
    return {"ok": True, "op": "overlay_catalog", **head, "nbytes": len(payload), "dtype": "<f4", "coords": og.COORDS,
            "bunit": None, "bunit_status": None}, payload


def declared_unit(req: dict):
    """The unit the person declared for a file without BUNIT, sent with the request, or None. Text only: it is shown and
    compared as typed and never parsed or converted here."""
    v = req.get("declared_bunit")
    if v is None:
        return None
    if not isinstance(v, str) or not v or len(v) > MAX_DECLARED_UNIT or v != v.strip() or not v.isprintable():
        raise Refused("bad_request", "bad request")
    return v


def catalog_op(op: str, path, req: dict) -> tuple[dict, bytes]:
    """Point catalogs (ADR 0044): tables read by astropy, bounded, never executed. The reply's payload is a JSON document."""
    import catalog_table as ct
    if op not in ct.OPS or not isinstance(path, str) or not os.path.isabs(path) or len(path) > 4096:
        raise Refused("bad_request", "bad request")
    real = authorize(path)
    try:
        return ct.handle(op, real, req)
    except ct.Bad as e:
        raise Refused("unreadable", str(e))


def remote_cache_dir(real_dat: str) -> str:
    """Where a worker that enforces roots (a remote host) keeps its ROHSA index sidecars: the folder the launcher names in
    VESQIRA_CACHE (the account's own cache, `~/.cache/vesqira/rohsa`), never one a request names. It is created private
    (0700), must be a real folder owned by this account (not a link), and must not be inside the folder of the .dat: an
    index is a rebuildable cache and is never written next to research data or into a project tree.
    """
    raw = os.environ.get("VESQIRA_CACHE")
    if not raw or not os.path.isabs(raw) or "\0" in raw:
        raise Refused("unsupported", "this host has no cache folder for the ROHSA index")
    data_dir = os.path.dirname(real_dat)
    planned = os.path.realpath(raw)
    if planned == data_dir or planned.startswith(data_dir.rstrip(os.sep) + os.sep):
        raise Refused("unsupported", "the ROHSA index is never written next to the data")   # checked before anything is created
    try:
        if os.path.islink(raw):
            raise Refused("unsupported", "the host's ROHSA cache folder is a link; it is not used")
        os.makedirs(raw, mode=0o700, exist_ok=True)
        st = os.lstat(raw)
    except OSError:
        raise Refused("unsupported", "the host's ROHSA cache folder cannot be created")
    import stat as _stat
    if not _stat.S_ISDIR(st.st_mode) or st.st_uid != os.getuid():
        raise Refused("unsupported", "the host's ROHSA cache folder is not a folder of this account")
    return planned


def rohsa_velocity_axis(c):
    """(world, dv, axis) of a crop's spectral axis for the ROHSA reads: world(channels) in km/s and dv = |km/s per channel| when the axis is
    a declared (or corrected) velocity axis with a resolved, uniform grid, else (None, None, an axis record that says why centres and widths
    stay in channels). The one place this is decided, for the per-pixel browser and for the decomposition review."""
    from astropy import units as u
    nc = c.channels
    facts = c.spectral_facts()
    world, dv = None, None
    axis = {"kind": "channel", "name": None, "unit": None, "source_unit": None, "unit_status": None,
            "note": "the spectral axis is not a resolved velocity axis, so centres and widths stay in channels"}
    if facts and facts["resolved"] and facts.get("velocity") in ("declared", "corrected"):
        try:
            factor = float(u.Unit(facts["unit"]).to(u.km / u.s))
        except Exception:
            factor = None
        if factor is not None:
            def world(ch, factor=factor):
                pix = np.zeros((len(ch), len(c.shape)))
                pix[:, c.chan] = ch
                with warnings.catch_warnings():
                    warnings.simplefilter("ignore")
                    return np.asarray(c.wcs.all_pix2world(pix, 0)[:, c.spec], dtype=np.float64) * factor
            w = world(np.array([0.0, 1.0, nc - 1.0]))
            dv = abs(float(w[1] - w[0]))
            span = float(w[2] - w[0])
            if dv > 0 and abs(span - (nc - 1) * float(w[1] - w[0])) <= 1e-6 * max(abs(span), dv):
                axis = {"kind": "velocity", "name": facts["name"], "unit": "km/s", "source_unit": facts["unit"],
                        "unit_status": facts["unit_status"], "note": None}
            else:
                world = dv = None
                axis["note"] = "the spectral axis is not uniformly spaced, so widths in km/s are not defined"
    return world, dv, axis


def rohsa_op(op: str, req: dict) -> tuple[dict, bytes]:
    """ROHSA decomposition (ADR 0046): index the raw .dat (resumable), then read one pixel's Gaussians as curves.

    The crop FITS (`path`) gives the pixel grid and the spectral axis; the .dat (`dat_path`) holds the fit. The sidecar
    index goes under a cache folder, so this is the one operation that writes, and only there. Locally the API names the
    folder (`cache_dir`). A worker that enforces roots (a host) takes none from the request: its own cache folder is fixed
    by the launcher (remote_cache_dir), the index is built on the machine that owns the data, and the .dat never travels.
    """
    for key in ("path", "dat_path") + (("cache_dir",) if ROOTS is None else ()):
        v = req.get(key)
        if not isinstance(v, str) or not os.path.isabs(v) or len(v) > 4096 or "\0" in v:
            raise Refused("bad_request", "bad request")
    real_crop, real_dat = authorize(req["path"]), authorize(req["dat_path"])
    cache_dir = req["cache_dir"] if ROOTS is None else remote_cache_dir(real_dat)
    c = cube_for(real_crop)
    if c.chan is None or c.channels < 2:
        raise Refused("unsupported", "the crop has no spectral axis of at least two channels")
    nx, ny, nc = c.shape[c.x], c.shape[c.y], c.channels
    n = req.get("n_gauss")
    try:
        if op == "rohsa_index":
            budget = req.get("budget_s", 15)
            if isinstance(budget, bool) or not isinstance(budget, (int, float)) or not 0 < budget <= 20:
                raise Refused("bad_request", "bad request")
            return {"ok": True, "op": op, "nbytes": 0, "width": nx, "height": ny,
                    **rohsa.index_step(real_dat, cache_dir, nx, ny, n, float(budget))}, b""
        x, y = as_index(req, "x", nx), as_index(req, "y", ny)
        if nc * (n + 2 if isinstance(n, int) and not isinstance(n, bool) else 2) > rohsa.MAX_CURVE_ELEMENTS:
            raise Refused("unsupported", "too many channels for the curves")
        amp, mean_raw, sig = rohsa.read_pixel(real_dat, cache_dir, nx, ny, n, x, y)
    except rohsa.Bad as e:
        raise Refused(e.code, e.message)
    except OSError:
        raise Refused("unsupported", "the .dat cannot be read")
    world, dv, axis = rohsa_velocity_axis(c)
    comps, curves, model = rohsa.components_at(amp, mean_raw, sig, nc, world, dv)
    chan = np.arange(nc, dtype=np.float64)
    ax = world(chan) if world is not None else chan
    payload = (np.asarray(ax, dtype="<f8").tobytes() + model.astype("<f8").tobytes() + curves.astype("<f8").tobytes())
    head = {"ok": True, "op": op, "nbytes": len(payload), "x": x, "y": y, "channels": nc, "n_gauss": n, "components": comps,
            "axis": axis, "bunit": unit_of(c, None)[0],
            "layout": "float64 axis[channels], float64 model[channels], float64 component[n_gauss][channels], little-endian",
            "conventions": {
                "file_columns": "i j amp mean sigma; i = zero-based row (FITS axis 2), j = zero-based column (FITS axis 1)",
                "mean": "1-based channel number in the file; mean_channel = file mean - 1 is the 0-based channel index",
                "sigma": "channels; centre and width in km/s use the crop's spectral axis (astropy WCS), no hand-rolled conversion",
                "model": "sum of the valid components, each amp * exp(-(channel - mean_channel)^2 / (2 sigma^2)) at every channel",
                "invalid": "a component whose sigma is not positive and finite is listed as invalid and is not drawn; no floor is applied; amplitude 0 is a real value",
                "fwhm": "2 sqrt(2 ln 2) sigma",
                "t_doppler": f"T_D = {rohsa.DOPPLER_K_PER_KMS2} K (km/s)^-2 x sigma^2: the pipeline's constant, m_H sigma^2 / k_B for atomic hydrogen; an upper limit on the kinetic temperature (thermal broadening only)"}}
    return head, payload


def handle(req: dict) -> tuple[dict, bytes]:
    """One request. The corrections it carries (ADR 0061) are in force for its duration only; every reply that read a file
    says which correction states it accepted (`corrections_applied`), so the API can refuse a worker that ignored them."""
    try:
        with corr.CURRENT.using(req.get("corrections")) as applied:
            head, payload = _handle(req)
    except corr.Bad as e:
        raise Refused("bad_request", str(e))
    if applied:
        head = {**head, "corrections_applied": sorted(st["hash"] for st in applied.values())}
    return head, payload


WINDOW_OPS = ("view", "plane", "coord", "spectrum")


def _handle(req: dict) -> tuple[dict, bytes]:
    op, path = req.get("op"), req.get("path")
    if op == "wcs_preview":
        return wcs_preview(req)
    if op == "convert_values":                                   # numbers with units and no dataset (ADR 0062): plain dimensional conversion
        import unit_values as uv
        try:
            return {"ok": True, "op": op, "nbytes": 0, **uv.convert_values(req)}, b""
        except uv.Bad as e:
            raise Refused("bad_request", str(e))
    if op in ("rohsa_index", "rohsa_pixel"):
        return rohsa_op(op, req)
    if op == "cards":
        if not isinstance(path, str) or not os.path.isabs(path) or len(path) > 4096:
            raise Refused("bad_request", "bad request")
        authorize(path)
        window = req.get("window")
        if window is not None and not isinstance(window, dict):
            raise Refused("bad_request", "bad request")
        head = header_cards(path, window)
        return ({**head, "window": cube_for(path, window).win_report()} if window is not None else head), b""
    if op == "mask_plane":                                       # a plane of a typed mask file (ADR 0055), for the viewer's hatch overlay
        if not isinstance(path, str) or not os.path.isabs(path) or len(path) > 4096:
            raise Refused("bad_request", "bad request")
        ch = req.get("channel")
        if isinstance(ch, bool) or not isinstance(ch, int) or ch < 0:
            raise Refused("bad_request", "channel must be a non-negative integer")
        import masks as mk
        return mk.mask_plane(path, ch)
    if op in ("ping", "list", "resolve", "stat", "header", "describe", "readtext", "complete", "walk", "canon", "fingerprint", "fingerprint_full"):
        return host_op(op, req)
    if isinstance(op, str) and op.startswith("catalog_"):
        return catalog_op(op, path, req)
    if op == "overlay_catalog":
        return overlay_catalog(req)
    file_ops = ("view", "plane", "coord", "spectrum", "axial", "sky2pix", "convert", "conversions", "orientation_sky", "overlay_geometry",
                "slab", "channel_grid", "pv", "link", "region_histogram")
    if op in file_ops:
        authorize(path)                                          # also enforces the roots on a remote host
        expect = parse_expect(req.get("expect"))
        if expect is not None:                                   # the API checked this file's device and inode; the file opened now must be that one
            with source_file(path, expect):
                pass
        if op == "axial" and req.get("weights_path") is not None:
            authorize(req.get("weights_path"))
        if op == "link":                                         # the other map of a linked pair
            authorize(req.get("other_path"))
        if op == "overlay_geometry":                             # every file the overlay reads, not only the base
            authorize(req.get("overlay_path"))
            if req.get("reliability_path") is not None:
                authorize(req.get("reliability_path"))
    if op not in file_ops or not isinstance(path, str) or not os.path.isabs(path) or len(path) > 4096:
        raise Refused("bad_request", "bad request")
    window = req.get("window")
    if window is not None and (op not in WINDOW_OPS or not isinstance(window, dict)):
        # a window changes what every pixel index means; an operation that does not read through it must refuse, not answer for the source
        raise Refused("bad_request", "a virtual subset can be viewed, read for a plane, a spectrum or a coordinate; this operation is not available on it")
    c = cube_for(path, window)
    bunit, status = unit_of(c, declared_unit(req))
    base = {"ok": True, "op": op, "origin": 0, "bunit": bunit, "bunit_status": status}
    if c.win is not None:
        base["window"] = c.win_report()                    # the API refuses a reply that does not say it applied the window it sent
    if c.corrections:
        # what the file itself says, beside the corrected values, so every surface can say "the file says K; you declared ..."
        base["file_bunit"] = clean(c.file_h.get("BUNIT")) if "BUNIT" in c.file_h else None
        base["wcs_corrected"] = sorted(c.corrected)
    if op == "overlay_geometry":
        head, payload = overlay_geometry(c, req)
        return {**base, **head}, payload
    if op == "link":
        return {**base, "nbytes": 0, **link(c, req)}, b""
    if op in ("slab", "channel_grid", "pv"):
        import cube_slices as cs
        try:
            if op == "slab":
                head, payload = cs.slab(c, req, block_mean, stats)
            elif op == "channel_grid":
                head, payload = cs.channel_grid(c, req, block_mean)
            else:
                head, payload = cs.pv(c, req, stats)
        except cs.Refusal as e:
            raise Refused(e.code, str(e))
        except convert.Bad as e:
            raise Refused("bad_request", str(e))
        return {**base, **head}, payload
    if op == "view":
        import orientation as ori
        fl = c.flags()
        return {**base, "compass": ori.compass_of(c), "nbytes": 0, "width": c.shape[c.x], "height": c.shape[c.y], "channels": c.channels,
                "fits_axes": {"x": c.x + 1, "y": c.y + 1, "channel": None if c.chan is None else c.chan + 1},
                "sky": c.sky_facts(), "spectral": c.spectral_facts(), "flags": fl,
                "provenance_note": "axis roles come from the WCS; values are declared by the file, not verified"}, b""
    if op == "axial":
        wp = req.get("weights_path")
        if wp is not None and (not isinstance(wp, str) or not os.path.isabs(wp) or len(wp) > 4096):
            raise Refused("bad_request", "bad request")
        return {**base, "nbytes": 0, "stats": c.axial_stats(req, cube_for(wp) if wp else None),
                "method": "doubled-angle circular statistics (astropy.stats); 0 and 180 degrees are the same orientation"}, b""
    if op == "sky2pix":
        return {**base, "nbytes": 0, **c.sky_to_pixels(req.get("points"))}, b""
    if op == "orientation_sky":
        return {**base, "nbytes": 0, **orientation_sky(c, req)}, b""
    if op == "conversions":
        return {**base, "nbytes": 0, **convert.offers(c, bunit, status)}, b""
    if op == "region_histogram":
        return {**base, "nbytes": 0, **c.region_histogram(req)}, b""
    if op == "spectrum":
        head, payload = c.spectrum(req)
        return {**base, **head, "bunit": bunit}, payload     # the spectrum head knows only the file's unit
    channel = as_index(req, "channel", c.channels)
    if op == "convert":
        x, y = as_index(req, "x", c.shape[c.x]), as_index(req, "y", c.shape[c.y])
        try:
            return {**base, "nbytes": 0, **convert.convert(c, req, x, y, channel, bunit, status)}, b""
        except convert.Bad as e:
            raise Refused("bad_request", str(e))
    if op == "coord":
        x, y = as_index(req, "x", c.shape[c.x]), as_index(req, "y", c.shape[c.y])
        return {**base, "nbytes": 0, **c.coord(x, y, channel)}, b""
    plane, overflow = c.read_plane(channel)
    step = req.get("step", 1)
    if isinstance(step, bool) or not isinstance(step, int) or not 1 <= step <= MAX_PREVIEW_STEP:
        raise Refused("bad_request", f"step must be an integer in [1, {MAX_PREVIEW_STEP}]")
    preview = None
    if step > 1:
        preview = {"step": step, "source_width": int(plane.shape[1]), "source_height": int(plane.shape[0]),
                   "method": f"mean of the finite pixels in each {step} x {step} block (NaN where a block has none); a display preview, not for measurement"}
        plane = block_mean(plane, step)
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
            "flagged": (channel in fl["channels"]) if fl["declared"] else None, "preview": preview}, payload


def wcs_preview(req: dict) -> tuple[dict, bytes]:
    """What a proposed correction does, before it is saved (ADR 0061): it is validated exactly as it would be applied, and the
    coordinates at the image centre and corners and of the first, middle and last channel are given before (the corrections in
    force now, `current`) and after (`proposed`). A refused proposal answers with the reason, not an error, so the form can show
    it. Nothing is written."""
    path = req.get("path")
    if not isinstance(path, str) or not os.path.isabs(path) or len(path) > 4096:
        raise Refused("bad_request", "bad request")
    authorize(path)
    if os.path.realpath(path) != path:
        raise Refused("bad_request", "bad request")
    try:
        cur = corr.check_state(req["current"]) if req.get("current") is not None else None
        prop = corr.check_state(req.get("proposed"))
    except (corr.Bad, KeyError) as e:
        raise Refused("bad_request", str(e) or "bad request")
    before = Cube(path, cur)
    out = {"ok": True, "op": "wcs_preview", "nbytes": 0, "before": corr.coords(before), "after": None, "refused": None,
           "file_bunit": clean(before.file_h.get("BUNIT")) if "BUNIT" in before.file_h else None,
           "changes": corr.changes(before.file_h, (cur or {}).get("wcs") or {}, prop["wcs"]),
           "note": "coordinates by astropy from the file's header with the corrections applied in memory; the file is not changed"}
    try:
        after = Cube(path, prop)
    except Refused as e:
        out["refused"] = e.message
        return out, b""
    out["after"] = corr.coords(after)
    out["bunit_after"] = unit_of(after, None)[0]
    return out, b""


def orientation_sky(c: Cube, req: dict) -> dict:
    """One pixel of an orientation map as sky-frame B-field orientation (ADR 0042).

    The caller supplies what the person declared: the angle's unit (deg or rad), its frame (pixel or sky) and its quantity.
    A missing or unusable declaration is a refusal that names it; nothing is assumed. The stored value is read from the
    file's single plane at (x, y); a pixel with no value gives null, never 0."""
    import orientation as ori
    unit = req.get("unit")
    if unit not in ("deg", "rad"):
        raise Refused("bad_request", "unit must be deg or rad")
    try:
        ori.check_frame(req.get("frame"))
        ori.b_rotation_deg(req.get("quantity"))
        if c.channels != 1:
            raise ori.Undeclared("an orientation map is a single plane")
        w = ori.celestial_wcs(c) if (req["frame"] == "pixel" or req.get("sky_frame")) else None
    except ori.Undeclared as e:
        raise Refused("unsupported", str(e))
    x, y = as_index(req, "x", c.shape[c.x]), as_index(req, "y", c.shape[c.y])
    plane, _ = c.read_plane(0)
    v = float(plane[y, x])
    out = {"x": x, "y": y, "unit": unit, "frame": req["frame"], "quantity": req["quantity"], "stored": None, "sky_b_field_deg": None,
           "north_pixel_angle_deg": None, "sky_frame": c.sky_facts().get("frame") if c.celestial else None,
           "method": ("sky frame: position angle east of north in the file's own celestial frame; a gradient and dust polarization are "
                      "perpendicular to B (+90 degrees); the pixel frame is related to the sky by astropy's WCS at this pixel; axial, in [0, 180)")}
    if not math.isfinite(v):
        out["note"] = "this pixel has no value (NaN or BLANK); it is not zero"
        return out
    deg = v if unit == "deg" else math.degrees(v)
    out["stored"] = v
    try:
        sky = float(ori.to_sky_b_field(w, x, y, deg, req["frame"], req["quantity"], req.get("sky_frame")))
        if w is not None:
            out["angle_stated_in"] = req.get("sky_frame")
            out["north_pixel_angle_deg"] = float(ori.sky_pa_to_pixel(w, x, y, 0.0))
    except ori.Undeclared as e:
        raise Refused("unsupported", str(e))
    out["sky_b_field_deg"] = sky
    return out




SLOW_REQUEST_S = 0.5
_REQUEST_ID = re.compile(r"[A-Za-z0-9._-]{8,64}")


def request_id(req) -> str:
    """The API's correlation id for this request, for log lines only; "-" when absent or not a plain token.

    It is never echoed in a reply and never reaches scientific outputs or provenance.
    """
    rid = req.get("requestId") if isinstance(req, dict) else None
    return rid if isinstance(rid, str) and _REQUEST_ID.fullmatch(rid) else "-"


def log_line(rid: str, op, what: str) -> None:
    """One stderr line (stdout is the protocol), only for a request that carries an API id: background header reads
    (project scans) have none and routinely refuse non-FITS files. The op is logged only when it is a short identifier; never a path."""
    if rid == "-":
        return
    op = op if isinstance(op, str) and len(op) <= 16 and op.isidentifier() else "?"
    print(f"worker request={rid} op={op} {what}", file=sys.stderr, flush=True)


def main() -> int:
    limit_memory()
    out = sys.stdout.buffer
    sys.stdout = sys.stderr                                 # stray prints must never corrupt the protocol
    for line in sys.stdin.buffer:
        t0, rid, op = time.perf_counter(), "-", None
        try:
            if len(line) > MAX_REQUEST_BYTES:
                raise Refused("bad_request", "request too large")
            req = json.loads(line)
            if not isinstance(req, dict):
                raise Refused("bad_request", "bad request")
            rid, op = request_id(req), req.get("op")
            head, payload = handle(req)
        except Refused as e:
            head, payload = {"ok": False, "code": e.code, "error": e.message, "nbytes": 0}, b""
            log_line(rid, op, f"refused code={e.code}")
        except MemoryError:
            head, payload = {"ok": False, "code": "unreadable", "error": over_cap_message(), "nbytes": 0}, b""
            log_line(rid, op, "failed MemoryError")
        except (OSError, ValueError, KeyError, TypeError, IndexError) as e:
            head, payload = {"ok": False, "code": "unreadable", "error": "cannot read this file (truncated, corrupt or unsupported)", "nbytes": 0}, b""
            log_line(rid, op, f"failed {type(e).__name__}")
        except Exception as e:                               # keep the worker alive on anything astropy raises
            head, payload = {"ok": False, "code": "unreadable", "error": "cannot read file", "nbytes": 0}, b""
            log_line(rid, op, f"failed {type(e).__name__}")
        elapsed = time.perf_counter() - t0
        if elapsed > SLOW_REQUEST_S:
            log_line(rid, op, f"slow ms={int(elapsed * 1000)}")
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
