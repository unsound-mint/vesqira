"""A format-neutral record of per-pixel Gaussian components (ADR 0068, item 3).

A decomposition tool writes its fit in some format; its profile declares what the values mean. This module reads the declaration
and the source, and hands the rest of the worker (the components panel, the sigma-v diagram, the width-class maps) one thing: for
every (pixel, component slot), an amplitude, a centre as a 0-based fractional channel of the grid, and a sigma in channels, or a
reason why that is not defined. Nothing here knows a tool's name.

The record, per component:
  amplitude  a number, in the grid's brightness unit ("grid") or in a declared unit, or the BUNIT of the file that holds it
  centre     a number with a unit ("channel" with an index base, or a velocity unit with a spectral frame)
  width      a number with a convention (sigma, dispersion = sigma, or fwhm) and a unit ("channel" or a velocity unit)
  order      the slot number, in the source's own numbering (never reordered here)
  source     where the numbers come from (a text table, or FITS maps)

What a declaration does not state is `unresolved`: it is reported as such, and no physical quantity is computed from it. A unit, an
index base or a width convention is never assumed. FWHM becomes sigma in one place (`width_to_channel`) by 2 sqrt(2 ln 2).

Two sources:
  table      rows `pixel row, pixel column, ... one row per (pixel, component)` in pixel row-major order with the component fastest
             (ROHSA's .dat). Read through rohsa.py (verified layout, an offset index, one seek per pixel); the declaration gives
             the column numbers and the pixel index base. The file can be tens of GB and is never loaded.
  fits_maps  one FITS map per parameter (a cube with a declared component axis, or one 2D map per component, as the profile says).
             Headers are validated before anything is read; the data is read per pixel or per tile of rows with FITS sections, so
             memory does not depend on the size of the maps. A NaN in any of the three parameters of a slot means "not fitted
             here" (a tool with a variable number of components per pixel), counted apart from an invalid width and never zero.

Derived quantities (a Doppler temperature, a CNM cut) are not part of the record; the callers add them, labelled.
"""
from __future__ import annotations

import hashlib
import json
import os
import sys

import numpy as np

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))   # python -P does not add the script dir
import rohsa
from rohsa import Bad
from srcopen import source_file, stat_source

FWHM_PER_SIGMA = float(2.0 * np.sqrt(2.0 * np.log(2.0)))
MAX_GAUSSIANS = rohsa.MAX_GAUSSIANS
TILE_ELEMENTS = 1 << 21              # values per parameter in one tile of rows read from FITS maps
MAX_MAP_FILES = 3 * MAX_GAUSSIANS
MAX_MAP_AXIS = 1 << 20
MAX_HDUS = 4096                      # HDUs whose headers are looked at when an HDU is named
MAX_EXTVER = 1 << 20
WIDTH_KINDS = ("sigma", "dispersion", "fwhm")
CHANNEL = "channel"
GRID = "grid"


class Refusal(Exception):
    """A request the worker will not carry out, with the sentence the person reads (the review's refusals are this class)."""


# ---- the declaration

# What the ROHSA profile declares (services/api/internal/runs/run_profiles.json, ROHSA@1: the public .dat rows `i, j, A, mean, sigma`,
# i and j 0-based, mean 1-based in channels, sigma in channels, no units). A request that carries no declaration is read this way, for
# callers that predate profiles; a test keeps this equal to the shipped profile.
ROHSA_DECLARATION = {
    "source": {"kind": "table", "columns": {"row": 0, "col": 1, "amplitude": 2, "centre": 3, "width": 4}, "ncols": 5, "pixelBase": 0},
    "amplitude": {"unit": GRID},
    "centre": {"unit": CHANNEL, "base": 1, "frame": GRID},
    "width": {"kind": "sigma", "unit": CHANNEL},
    "order": "the source's own numbering, 1 to n",
}

_TOP = {"source", "amplitude", "centre", "width", "order", "as"}


def _bad(msg: str):
    raise Bad("bad_request", f"the decomposition declaration is not valid: {msg}")


def _text(v, what: str, limit: int = 60) -> str | None:
    if v is None:
        return None
    if not isinstance(v, str) or not v.strip() or len(v) > limit or not v.isprintable():
        _bad(f"{what} must be a short text")
    return v.strip()


def _only(d, allowed: set, what: str) -> dict:
    if not isinstance(d, dict):
        _bad(f"{what} must be an object")
    extra = set(d) - allowed
    if extra:
        _bad(f"{what} has unknown field(s) {sorted(extra)}")
    return d


def parse(decl) -> dict:
    """The declaration, validated and copied. A field that is absent stays absent (unresolved); a field that is wrong is refused."""
    d = _only(decl, _TOP, "the declaration")
    src = _only(d.get("source"), {"kind", "columns", "ncols", "pixelBase", "maps", "componentAxis"}, "source")
    kind = src.get("kind")
    out: dict = {"source": {"kind": kind}}
    if kind == "table":
        cols = _only(src.get("columns"), {"row", "col", "amplitude", "centre", "width"}, "columns")
        ncols = src.get("ncols")
        if isinstance(ncols, bool) or not isinstance(ncols, int) or not 5 <= ncols <= 32:
            _bad("ncols must be a whole number from 5 to 32")
        idx = {}
        for k in ("row", "col", "amplitude", "centre", "width"):
            v = cols.get(k)
            if isinstance(v, bool) or not isinstance(v, int) or not 0 <= v < ncols:
                _bad(f"column {k} must be a column number below {ncols}")
            idx[k] = v
        if len(set(idx.values())) != 5:
            _bad("the five columns must be different")
        base = src.get("pixelBase", 0)
        if base not in (0, 1) or isinstance(base, bool):
            _bad("pixelBase must be 0 or 1")
        out["source"].update(columns=idx, ncols=ncols, pixelBase=base)
    elif kind == "fits_maps":
        maps = _only(src.get("maps"), {"amplitude", "centre", "width"}, "maps")
        if set(maps) != {"amplitude", "centre", "width"}:
            _bad("maps must say where the amplitude, centre and width are")
        m_out = {}
        for k, m in maps.items():
            m = _only(m, {"hdu", "extver"}, f"maps.{k}")
            h = m.get("hdu", 0)
            if isinstance(h, bool) or not (isinstance(h, int) and 0 <= h < 1000 or isinstance(h, str) and 0 < len(h.strip()) <= 68 and h.isprintable()):
                _bad(f"maps.{k}.hdu must be an HDU number or name")
            m_out[k] = {"hdu": h.strip() if isinstance(h, str) else h}
            v = m.get("extver")
            if v is not None:
                if isinstance(v, bool) or not isinstance(v, int) or not 1 <= v <= MAX_EXTVER or not isinstance(h, str):
                    _bad(f"maps.{k}.extver is the EXTVER (a whole number from 1) of an HDU named by hdu; a numbered HDU has none")
                m_out[k]["extver"] = v
        axis = src.get("componentAxis")
        if axis is not None and (isinstance(axis, bool) or axis not in (1, 2, 3)):
            _bad("componentAxis must be the FITS axis 1, 2 or 3 that counts components, or absent when each component has its own map")
        out["source"].update(maps=m_out, componentAxis=axis)
    else:
        _bad("source.kind must be table or fits_maps")
    a = _only(d.get("amplitude", {}), {"unit"}, "amplitude")
    out["amplitude"] = {"unit": _text(a.get("unit"), "amplitude.unit")}
    c = _only(d.get("centre", {}), {"unit", "base", "frame"}, "centre")
    base = c.get("base")
    if base is not None and (isinstance(base, bool) or base not in (0, 1)):
        _bad("centre.base must be 0 or 1")
    out["centre"] = {"unit": _text(c.get("unit"), "centre.unit"), "base": base, "frame": _text(c.get("frame"), "centre.frame")}
    w = _only(d.get("width", {}), {"kind", "unit"}, "width")
    wk = w.get("kind")
    if wk is not None and wk not in WIDTH_KINDS:
        _bad(f"width.kind must be one of {', '.join(WIDTH_KINDS)}")
    out["width"] = {"kind": wk, "unit": _text(w.get("unit"), "width.unit")}
    out["order"] = _text(d.get("order"), "order", 200)
    return out


# ---- the grid the components are read against

class Grid:
    """What the crop says about its spectral axis and unit: the velocity of channel 0 (km/s) and the signed step when its axis is a
    resolved, uniform velocity axis (else None), its spectral frame (SPECSYS), the brightness unit and whose it is."""

    def __init__(self, nc: int, w0, step, specsys=None, bunit=None, bunit_source="unresolved", header=None):
        self.nc, self.w0, self.step, self.specsys = nc, w0, step, specsys
        self.bunit, self.bunit_source, self.header = bunit, bunit_source, header

    @property
    def velocity(self) -> bool:
        return self.step is not None

    @property
    def dv(self):
        return abs(self.step) if self.step is not None else None


def _kms_factor(unit: str, what: str) -> float:
    from astropy import units as u
    try:
        return float(u.Unit(unit).to(u.km / u.s))
    except Exception:
        raise Refusal(f"the {what} is declared in {unit!r}, which is not a velocity unit, so it cannot be placed on the cube's velocity axis") from None


def units_equal(a: str | None, b: str | None) -> bool | None:
    """Whether two unit spellings are the same unit; None when either is unknown."""
    if not a or not b:
        return None
    if a.strip() == b.strip():
        return True
    try:
        from astropy import units as u
        return bool(u.Unit(a) == u.Unit(b))
    except Exception:
        return False


def centre_to_channel(decl: dict, centre, g: Grid):
    """Centres as 0-based fractional channel numbers of the grid. A refusal says what the declaration lacks or what the grid cannot give."""
    c = decl["centre"]
    unit = c.get("unit")
    if unit is None:
        raise Refusal("the centre's unit is not declared (unresolved), so the components cannot be placed on the spectrum")
    if unit == CHANNEL:
        if c.get("base") is None:
            raise Refusal("the centre is in channels but the index base (first channel is 0 or 1) is not declared (unresolved); it is not guessed")
        return centre - float(c["base"])
    factor = _kms_factor(unit, "centre")
    if not g.velocity:
        raise Refusal(f"the centre is in {unit}, but the cube has no resolved velocity axis to place it on")
    frame = c.get("frame")
    if frame is None:
        raise Refusal("the centre is a velocity but its spectral frame is not declared (unresolved), so it is not compared with the cube's axis")
    specsys = getattr(g, "specsys", None)
    if not specsys:
        raise Refusal(f"the centre is in the {frame} frame, but the cube states no SPECSYS, so the two frames cannot be compared")
    if frame.upper() != str(specsys).upper():
        raise Refusal(f"the centre is in the {frame} frame and the cube's spectral axis is {specsys}; they are not the same frame, so the components are not drawn over this cube")
    return (centre * factor - g.w0) / g.step


def width_to_channel(decl: dict, width, g: Grid):
    """Sigma in channels. A FWHM is turned into sigma here, by 2 sqrt(2 ln 2); `dispersion` is the standard deviation, as sigma."""
    w = decl["width"]
    kind, unit = w.get("kind"), w.get("unit")
    if kind is None:
        raise Refusal("the width convention (sigma, FWHM or dispersion) is not declared (unresolved); it is not guessed")
    if unit is None:
        raise Refusal("the width's unit is not declared (unresolved), so it is not read as channels or km/s")
    sigma = width if kind in ("sigma", "dispersion") else width / FWHM_PER_SIGMA
    if unit == CHANNEL:
        return sigma
    factor = _kms_factor(unit, "width")
    if not g.velocity:
        raise Refusal(f"the width is in {unit}, but the cube has no resolved velocity axis to convert it with")
    return sigma * factor / g.dv


def describe(decl: dict, g: Grid, amp_unit: str | None, amp_basis: str) -> dict:
    """The record's fields as the reader resolved them, for the reply: what each one is, and `unresolved` with the reason when it is not."""
    def probe(fn, *a):
        try:
            fn(*a)
            return {"status": "resolved"}
        except Refusal as e:
            return {"status": "unresolved", "reason": str(e)}
    c, w = decl["centre"], decl["width"]
    out = {
        "source": decl["source"]["kind"],
        "amplitude": {"unit": amp_unit, "basis": amp_basis, "status": "resolved" if amp_unit else "unresolved"},
        "centre": {"unit": c.get("unit"), "base": c.get("base") if c.get("unit") == CHANNEL else None, "frame": c.get("frame"), **probe(centre_to_channel, decl, 0.0, g)},
        "width": {"kind": w.get("kind"), "unit": w.get("unit"), **probe(width_to_channel, decl, 1.0, g)},
        "order": decl.get("order"),
    }
    if w.get("kind") == "fwhm":
        out["width"]["to_sigma"] = "sigma = FWHM / (2 sqrt(2 ln 2)) = FWHM / 2.3548"
    if not amp_unit:
        out["amplitude"]["reason"] = "no unit is declared for the amplitudes and the grid has none"
    return out


# ---- sources

class TableSource:
    kind = "table"

    def __init__(self, decl: dict, dat_path: str):
        s = decl["source"]
        c = s["columns"]
        self.layout = rohsa.Layout(s["ncols"], c["row"], c["col"], c["amplitude"], c["centre"], c["width"], s["pixelBase"])
        self.dat = dat_path
        self.notes: list[str] = []

    def cache_key(self) -> str:
        return rohsa._key(self.dat, stat_source(self.dat), self.layout)

    def source_dirs(self) -> list:
        return [os.path.dirname(os.path.realpath(self.dat))]

    def identity(self) -> dict:
        st = stat_source(self.dat)
        return {"name": os.path.basename(self.dat), "size_bytes": int(st.st_size), "mtime_ns": int(st.st_mtime_ns)}

    def amp_file_unit(self):
        return None

    def effective(self, decl: dict) -> dict:
        return decl

    def index_step(self, cache_dir, nx, ny, n, budget_s):
        return rohsa.index_step(self.dat, cache_dir, nx, ny, n, budget_s, self.layout)

    def read_pixel(self, cache_dir, nx, ny, n, x, y):
        a, c, w = rohsa.read_pixel(self.dat, cache_dir, nx, ny, n, x, y, self.layout)
        return a, c, w, np.ones(len(a), dtype=bool)

    def stream(self, nx, ny, n, max_seconds):
        import gaussreview as gr
        for x, y, a, c, w in gr.stream_components(self.dat, nx, ny, n, max_seconds, self.layout):
            yield x, y, a, c, w, np.ones(len(a), dtype=bool)


PARAMS = ("amplitude", "centre", "width")


class MapsSource:
    """One FITS map (or cube) per parameter. `files` is {parameter: [absolute, authorized paths]}: one cube per parameter with a
    declared component axis, or one 2D map per component, in the component order of the profile."""
    kind = "fits_maps"

    def __init__(self, decl: dict, files: dict, nx: int, ny: int, n: int, grid_header=None):
        self.decl, self.nx, self.ny, self.n = decl, nx, ny, n
        self.axis = decl["source"].get("componentAxis")
        self.files = self._check_files(files)
        self.notes: list[str] = []
        self.units: dict[str, str | None] = {}
        self.dims: dict[str, tuple] = {}
        for p in PARAMS:
            self._validate(p, grid_header)

    def _check_files(self, files) -> dict:
        if not isinstance(files, dict) or set(files) != set(PARAMS):
            raise Bad("bad_request", "the FITS maps need files for the amplitude, centre and width")
        out = {}
        for p in PARAMS:
            fl = files[p]
            want = 1 if self.axis else self.n
            if (not isinstance(fl, list) or len(fl) != want or any(not isinstance(f, str) or not os.path.isabs(f) or "\0" in f for f in fl)):
                raise Bad("bad_request", f"the {p} map needs {'one file' if self.axis else f'{self.n} files, one per component'}")
            out[p] = list(fl)
        if sum(len(v) for v in out.values()) > MAX_MAP_FILES:
            raise Bad("unsupported", "too many map files")
        return out

    def _open(self, p: str, k: int):
        """Context manager yielding the HDU of map k of parameter p, opened through the one safe-open path (no reopening by name)."""
        import contextlib
        from astropy.io import fits

        @contextlib.contextmanager
        def cm():
            with source_file(self.files[p][k]) as (_, f):
                try:
                    hl = fits.open(f, mode="denywrite", memmap=False, lazy_load_hdus=True)
                except Exception:
                    raise Bad("unsupported", f"the {p} map is not a readable FITS file") from None
                try:
                    yield find_hdu(hl, self.decl["source"]["maps"][p], p)
                finally:
                    hl.close()
        return cm()

    def _validate(self, p: str, grid_header):
        """Header checks before any data is read: dimensions, the unit, and that the sky grid is the cube's."""
        for k in range(len(self.files[p])):
            with self._open(p, k) as hdu:
                h = hdu.header
                nax = h.get("NAXIS")
                dims = tuple(int(h.get(f"NAXIS{i}", 0)) for i in range(1, (nax or 0) + 1))
                if self.axis:
                    want = {1: (self.n, self.nx, self.ny), 2: (self.nx, self.n, self.ny), 3: (self.nx, self.ny, self.n)}[self.axis]
                else:
                    want = (self.nx, self.ny)
                if dims != want:
                    raise Bad("unsupported", f"the {p} map has axes {dims} (FITS order) where {want} is expected for {self.nx} x {self.ny} pixels"
                              + (f" and {self.n} components on axis {self.axis}" if self.axis else ""))
                if any(d < 1 or d > MAX_MAP_AXIS for d in dims) or int(h.get("BITPIX", 0)) not in (8, 16, 32, 64, -32, -64):
                    raise Bad("unsupported", f"the {p} map has an unusable shape or data type")
                unit = h.get("BUNIT")
                unit = unit.strip() if isinstance(unit, str) and unit.strip() else None
                if k == 0:
                    self.units[p] = unit
                    self.dims[p] = dims
                elif unit != self.units[p]:
                    raise Bad("unsupported", f"the {p} maps of different components do not agree on BUNIT ({self.units[p]!r} and {unit!r})")
                note = _check_sky_grid(h, grid_header, p)
                if note and note not in self.notes:
                    self.notes.append(note)

    def source_dirs(self) -> list:
        return sorted({os.path.dirname(os.path.realpath(f)) for p in PARAMS for f in self.files[p]})

    def cache_key(self) -> str:
        h = hashlib.sha256()
        for p in PARAMS:
            for f in self.files[p]:
                st = stat_source(f)
                h.update(f"{f}\0{st.st_size}\0{st.st_mtime_ns}\0".encode())
        h.update(json.dumps(self.decl, sort_keys=True).encode())
        return h.hexdigest()[:32]

    def identity(self) -> dict:
        names, size, mt = [], 0, 0
        for p in PARAMS:
            for f in self.files[p]:
                st = stat_source(f)
                size += int(st.st_size)
                mt = max(mt, int(st.st_mtime_ns))
                names.append(os.path.basename(f))
        shown = ", ".join(names[:3]) + (f" and {len(names) - 3} more" if len(names) > 3 else "")
        return {"name": shown, "size_bytes": size, "mtime_ns": mt}

    def amp_file_unit(self):
        return self.units.get("amplitude")

    def effective(self, decl: dict) -> dict:
        """The declaration with units a map's own BUNIT states: the file wins over a declared physical unit, and a disagreement is a note.
        A centre declared in channels is a position, not a physical unit, and BUNIT is not asked."""
        out = json.loads(json.dumps(decl))
        for p, field in (("centre", "centre"), ("width", "width")):
            bu, declared = self.units.get(p), out[field].get("unit")
            if declared == CHANNEL or bu is None:
                continue
            if declared is not None and units_equal(declared, bu) is False:
                note = f"the {p} map says BUNIT {bu!r} and the profile declares {declared!r}; the file's unit is used"
                if note not in self.notes:
                    self.notes.append(note)
            out[field]["unit"] = bu
        return out

    def index_step(self, cache_dir, nx, ny, n, budget_s):
        # nothing to index: the maps are addressed by pixel. The headers were checked when the source was opened.
        size = sum(os.path.getsize(f) for p in PARAMS for f in self.files[p])
        return {"state": "ready", "done_bytes": size, "size_bytes": size, "rows": nx * ny * n, "reason": None, "malformed": 0,
                "examples": [], "index_bytes": 0, "cache_file": "", "created": False, "settled": False}

    def _section(self, p: str, k: int, y0: int, y1: int, x0: int, x1: int):
        """Values of parameter p for pixel rows y0..y1-1 and columns x0..x1-1 as an array (rows, columns, components) in float64."""
        with self._open(p, k) as hdu:
            sec = hdu.section
            if self.axis:
                nd = 3
                idx = [None] * nd
                idx[nd - self.axis] = slice(None)
                rest = [i for i in range(nd) if i != nd - self.axis]       # numpy axes of the two sky axes, slowest first: y then x
                idx[rest[0]], idx[rest[1]] = slice(y0, y1), slice(x0, x1)
                a = np.asarray(sec[tuple(idx)], dtype=np.float64)
                return np.moveaxis(a, nd - self.axis, -1)
            return np.asarray(sec[y0:y1, x0:x1], dtype=np.float64)[..., None]

    def _read(self, p: str, y0, y1, x0, x1) -> np.ndarray:
        if self.axis:
            return self._section(p, 0, y0, y1, x0, x1)
        return np.concatenate([self._section(p, k, y0, y1, x0, x1) for k in range(self.n)], axis=-1)

    def read_pixel(self, cache_dir, nx, ny, n, x, y):
        a, c, w = (self._read(p, y, y + 1, x, x + 1)[0, 0] for p in PARAMS)
        return a, c, w, ~(np.isnan(a) | np.isnan(c) | np.isnan(w))

    def stream(self, nx, ny, n, max_seconds):
        import time
        t0 = time.monotonic()
        rows = max(1, TILE_ELEMENTS // (nx * n))
        for y0 in range(0, ny, rows):
            if time.monotonic() - t0 > max_seconds:
                raise Refusal(f"reading the maps took more than {int(max_seconds)} s and was stopped; this decomposition is too large for one pass here")
            y1 = min(ny, y0 + rows)
            a, c, w = (self._read(p, y0, y1, 0, nx) for p in PARAMS)
            present = ~(np.isnan(a) | np.isnan(c) | np.isnan(w))
            yy, xx = np.meshgrid(np.arange(y0, y1, dtype=np.int64), np.arange(nx, dtype=np.int64), indexing="ij")
            yy, xx = np.repeat(yy.ravel(), n), np.repeat(xx.ravel(), n)       # pixel-major, component fastest
            yield xx, yy, a.ravel(), c.ravel(), w.ravel(), present.ravel()


def find_hdu(hl, spec: dict, p: str):
    """The image HDU a map declaration names. A number is an index into the file. A name is matched on EXTNAME (case-insensitively, as
    astropy does) and, when `extver` is declared, on EXTVER too (a card that is absent counts as 1, the FITS default). A name that
    several HDUs carry, or none does, is a refusal that says so: the first match is never taken on a guess. Only headers are looked
    at, up to MAX_HDUS."""
    from astropy.io import fits
    h, ver = spec["hdu"], spec.get("extver")
    if isinstance(h, int):
        try:
            hdu = hl[h]
        except IndexError:
            raise Bad("unsupported", f"the {p} map has no HDU number {h}") from None
    else:
        want, found = h.strip().upper(), []
        for i in range(MAX_HDUS):
            try:
                cand = hl[i]
            except IndexError:
                break
            name = cand.header.get("EXTNAME")
            if not isinstance(name, str) or name.strip().upper() != want:
                continue
            raw = cand.header.get("EXTVER", 1)
            found.append((i, raw if isinstance(raw, int) and not isinstance(raw, bool) else None))
        else:
            raise Bad("unsupported", f"the {p} map's file has more than {MAX_HDUS} HDUs; HDU {h!r} is not looked for in it")
        shown = ", ".join(f"HDU {i} (EXTVER {v if v is not None else 'not a number'})" for i, v in found)
        if ver is not None:
            hit = [i for i, v in found if v == ver]
            if not hit:
                raise Bad("unsupported", f"the {p} map's file has no HDU named {h!r} with EXTVER {ver}" + (f"; it has {shown}" if found else ""))
            if len(hit) > 1:
                raise Bad("unsupported", f"the {p} map's file has {len(hit)} HDUs named {h!r} with EXTVER {ver} ({shown}); the name does not pick one")
            idx = hit[0]
        else:
            if not found:
                raise Bad("unsupported", f"the {p} map's file has no HDU named {h!r}")
            if len(found) > 1:
                raise Bad("unsupported", f"the {p} map's file has {len(found)} HDUs named {h!r} ({shown}); declare which with extver, the first is not taken")
            idx = found[0][0]
        hdu = hl[idx]
    if not isinstance(hdu, (fits.PrimaryHDU, fits.ImageHDU, fits.CompImageHDU)):
        raise Bad("unsupported", f"the {p} map's HDU is not an image")
    return hdu


def _check_sky_grid(h, grid_header, p: str) -> str | None:
    """The map must sit on the cube's sky grid. A map with its own celestial WCS is compared card by card; one without is taken on the
    declaration (said in a note), since pixels are then all there is."""
    if grid_header is None:
        return None
    if "CTYPE1" not in h and "CTYPE2" not in h:
        return "the maps carry no sky WCS: they are taken to be on the cube's pixel grid, as the profile declares"
    keys = ("CTYPE1", "CTYPE2", "CRVAL1", "CRVAL2", "CRPIX1", "CRPIX2", "CDELT1", "CDELT2", "CD1_1", "CD1_2", "CD2_1", "CD2_2")
    for k in keys:
        a, b = h.get(k), grid_header.get(k)
        if a is None and b is None:
            continue
        if isinstance(a, str) or isinstance(b, str):
            same = a == b
        else:
            same = a is not None and b is not None and bool(np.isclose(float(a), float(b), rtol=1e-9, atol=1e-12))
        if not same:
            raise Bad("unsupported", f"the {p} map is not on the cube's sky grid ({k} is {a!r} there and {b!r} in the cube)")
    return None


# ---- the record the callers use

class Decomposition:
    """A declaration with its source, against one grid."""

    def __init__(self, decl: dict, source, grid: Grid):
        self.source, self.grid = source, grid
        self.decl = source.effective(decl)
        self.amp_unit, self.amp_basis = self._amp()

    def _amp(self):
        unit = self.decl["amplitude"].get("unit")
        file_unit = self.source.amp_file_unit()
        if file_unit:
            if unit not in (None, GRID) and units_equal(unit, file_unit) is False:
                note = f"the amplitude map says BUNIT {file_unit!r} and the profile declares {unit!r}; the file's unit is used"
                if note not in self.source.notes:
                    self.source.notes.append(note)
            return file_unit, "file"
        if unit == GRID:
            return self.grid.bunit, GRID if self.grid.bunit else "unresolved"
        if unit:
            return unit, "declared"
        return None, "unresolved"

    @property
    def amp_matches_grid(self):
        return True if self.amp_basis == GRID else units_equal(self.amp_unit, self.grid.bunit)

    def record(self) -> dict:
        r = describe(self.decl, self.grid, self.amp_unit, self.amp_basis)
        r["amplitude"]["matches_grid"] = self.amp_matches_grid
        r["notes"] = list(self.source.notes)
        return r

    def normalise(self, amp, centre, width):
        """(amplitude, 0-based fractional channel of the centre, sigma in channels); a Refusal when the centre or the width is unresolved."""
        return amp, centre_to_channel(self.decl, centre, self.grid), width_to_channel(self.decl, width, self.grid)

    def stream(self, nx, ny, n, max_seconds=25 * 60):
        """Yield (x, y, amp, mean0, sigma_ch, present) for consecutive chunks of whole pixels: components in the grid's channel terms.
        An absent slot (present False) has NaN centre and width; callers count it apart and never read it as zero."""
        for x, y, a, c, w, present in self.source.stream(nx, ny, n, max_seconds):
            m0 = centre_to_channel(self.decl, c, self.grid)
            s = width_to_channel(self.decl, w, self.grid)
            yield x, y, a, m0, s, present

    def read_pixel(self, cache_dir, nx, ny, n, x, y):
        """(amp, raw centre, raw width, present) at one pixel, as the source states them."""
        return self.source.read_pixel(cache_dir, nx, ny, n, x, y)

    def source_dirs(self) -> list:
        return self.source.source_dirs()

    def cache_key(self) -> str:
        return self.source.cache_key()

    def identity(self) -> dict:
        return self.source.identity()


def coerce(x, grid):
    """A path (the ROHSA .dat, as callers before profiles passed it) or a Decomposition; `grid` is anything with the attributes of Grid
    (the review's Scales)."""
    if isinstance(x, Decomposition):
        return x
    d = parse(ROHSA_DECLARATION)
    return Decomposition(d, TableSource(d, x), grid)


def dat_want(req: dict) -> str:
    """What `dat_path` must be for this request: the run's folder for FITS maps, else the .dat file (a malformed declaration is left
    to open_decomposition to refuse)."""
    spec = req.get("decomposition")
    src = spec.get("declaration", {}).get("source") if isinstance(spec, dict) and isinstance(spec.get("declaration"), dict) else None
    return "dir" if isinstance(src, dict) and src.get("kind") == "fits_maps" else "file"


def open_decomposition(req: dict, grid, nx: int, ny: int, n: int, dat_path: str, authorize, grid_header=None) -> Decomposition:
    """The decomposition a request names: `decomposition` = {declaration, files} (the API sends the run profile's declaration), else ROHSA's
    reading of `dat_path`. `dat_path` is an authorized path: the .dat of a table, or the folder the map files sit in; `files` names
    them relative to that folder, and each must stay inside it after links are resolved and be authorized on its own."""
    spec = req.get("decomposition")
    if spec is None:
        return coerce(dat_path, grid)
    if not isinstance(spec, dict) or set(spec) - {"declaration", "files"}:
        raise Bad("bad_request", "bad request")
    decl = parse(spec.get("declaration"))
    if decl["source"]["kind"] == "table":
        return Decomposition(decl, TableSource(decl, dat_path), grid)
    folder, files = os.path.realpath(dat_path), spec.get("files")
    if not isinstance(files, dict) or set(files) != set(PARAMS):
        raise Bad("bad_request", "bad request")
    real: dict = {}
    for p in PARAMS:
        names = files[p]
        if not isinstance(names, list) or not 1 <= len(names) <= MAX_GAUSSIANS:
            raise Bad("bad_request", "bad request")
        real[p] = []
        for name in names:
            if not isinstance(name, str) or not name or len(name) > 255 or "\0" in name or os.path.isabs(name) or ".." in name.split("/"):
                raise Bad("bad_request", "bad request")
            path = os.path.realpath(os.path.join(folder, name))
            if not path.startswith(folder + os.sep):
                raise Bad("forbidden", "a map file is outside the run's folder")
            real[p].append(authorize(path))
    return Decomposition(decl, MapsSource(decl, real, nx, ny, n, grid_header), grid)


def conventions(dec: Decomposition, doppler_k_per_kms2: float) -> dict:
    """The reading conventions of a reply in words, generated from the declaration (so a tool's own values are described, not ROHSA's)."""
    d, c, w = dec.decl, dec.decl["centre"], dec.decl["width"]
    s = d["source"]
    if s["kind"] == "table":
        cols = s["columns"]
        source = (f"text table, one row per (pixel, component) in pixel row-major order with the component fastest; columns (from 0): pixel row {cols['row']}, "
                  f"pixel column {cols['col']}, amplitude {cols['amplitude']}, centre {cols['centre']}, width {cols['width']}; pixel indices count from {s['pixelBase']}")
    else:
        where = f"one cube per parameter, components along FITS axis {s['componentAxis']}" if s["componentAxis"] else "one 2D map per component"
        source = f"FITS maps: {where}; a NaN in the amplitude, centre or width of a slot means the component is not fitted at that pixel"
    if c.get("unit") == CHANNEL:
        centre = (f"channel numbers counted from {c['base']}" if c.get("base") is not None else "channel numbers with an undeclared first index (unresolved)") \
            + "; mean_channel = value - base is the 0-based channel index"
    elif c.get("unit"):
        centre = f"in {c['unit']}, frame {c.get('frame') or 'undeclared (unresolved)'}; placed on the cube's spectral axis only when the frames agree"
    else:
        centre = "unit not declared (unresolved)"
    width = (f"{w.get('kind') or 'convention undeclared (unresolved)'} in {w.get('unit') or 'an undeclared unit (unresolved)'}"
             + ("; sigma = FWHM / (2 sqrt(2 ln 2))" if w.get("kind") == "fwhm" else "; dispersion is the standard deviation, as sigma" if w.get("kind") == "dispersion" else ""))
    return {
        "source": source,
        "centre": centre,
        "width": width + "; centre and width in km/s use the crop's spectral axis (astropy WCS), no hand-rolled conversion",
        "amplitude": ("the crop's brightness unit" if dec.amp_basis == GRID else f"{dec.amp_unit or 'an unresolved unit'} ({dec.amp_basis})"),
        "model": "sum of the valid components, each amp * exp(-(channel - mean_channel)^2 / (2 sigma^2)) at every channel",
        "invalid": "a component whose sigma is not positive and finite is listed as invalid and is not drawn; no floor is applied; amplitude 0 is a real value; a slot with a NaN is 'not fitted here', never zero",
        "fwhm": "2 sqrt(2 ln 2) sigma",
        "t_doppler": (f"optional and derived, not part of the record: T_D = {doppler_k_per_kms2} K (km/s)^-2 x sigma^2: m_H sigma^2 / k_B for atomic hydrogen, with m_H = 1.6735e-27 kg and "
                      "k_B = 1.380649e-23 J/K; an upper limit on the kinetic temperature (thermal broadening only)"),
    }
