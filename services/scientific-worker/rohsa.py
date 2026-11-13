"""ROHSA Gaussian decomposition (ADR 0046): per-pixel parameters from the raw `rohsa_*.dat`, and the model curves.

The raw file is plain text, one row `i j amp mean sigma` per (pixel, Gaussian), written in y, x, Gaussian order:
column 1 is the zero-based row of the crop (FITS axis 2), column 2 the zero-based column (FITS axis 1), the Gaussian
index runs fastest. `mean` is 1-BASED in channels (the first channel is 1); `sigma` is in channels; `amp` is in the
brightness unit of the crop the fit used. All of that was checked against the pipeline's own component maps.

The file can be tens of GB, so it is never loaded. One streaming pass checks that it is exactly the regular layout
above (every row is the (i, j, g) its position says it is, all values finite) and records the byte offset of the first
row of every pixel: 8 bytes per pixel, in a sidecar under a cache folder the caller names, never next to the data. The
pass is resumable: a call works for a time budget, saves how far it got, and the next call continues. A pixel is then
read with one seek and N short lines, each checked against the pixel asked for.
"""
from __future__ import annotations

import hashlib
import json
import os
import stat
import sys
import time
from typing import NamedTuple

import numpy as np

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))   # python -P does not add the script dir
from srcopen import source_file, stat_source

MAX_ROW_BYTES = 512                # a data line longer than this is malformed
MAX_GAUSSIANS = 64
CHUNK_BYTES = 32 << 20
MAX_EXAMPLES = 20
MAX_CURVE_ELEMENTS = 4_000_000     # channels x (components + 2) in one reply
INDEX_VERSION = 1

# T_D = m_H sigma^2 / k_B for sigma in km/s, thermal broadening only: m_H = 1.6735e-27 kg, k_B = 1.380649e-23 J/K give 121.2 K per
# (km/s)^2 (the same as T = 21.86 dV_FWHM^2). Results before 2026-11-13 used 121.1, 0.1% lower; T_D is computed per reply, never stored.
DOPPLER_K_PER_KMS2 = 121.2
FWHM_PER_SIGMA = 2.0 * np.sqrt(2.0 * np.log(2.0))


class Layout(NamedTuple):
    """Where the columns of a table of components are: how many there are, and which one holds the pixel row, the pixel column, the
    amplitude, the centre and the width, and the index base of the pixel row and column (decomp.py reads it from the profile's
    declaration). The default is ROHSA's `.dat`: i, j, A, mean, sigma with i and j counted from 0."""
    ncols: int = 5
    row: int = 0
    col: int = 1
    amp: int = 2
    centre: int = 3
    width: int = 4
    base: int = 0


DEFAULT_LAYOUT = Layout()


class Bad(Exception):
    def __init__(self, code: str, message: str):
        super().__init__(message)
        self.code, self.message = code, message


def _key(dat_real: str, st: os.stat_result, layout: Layout = DEFAULT_LAYOUT) -> str:
    # the default layout hashes as it always did, so an index built before layouts were declared is still found
    extra = "" if layout == DEFAULT_LAYOUT else f"\0{tuple(layout)}"
    return hashlib.sha256(f"{dat_real}\0{st.st_size}\0{st.st_mtime_ns}{extra}".encode()).hexdigest()[:32]


def _paths(cache_dir: str, key: str) -> tuple[str, str]:
    return os.path.join(cache_dir, key + ".json"), os.path.join(cache_dir, key + ".offs")


def _load_meta(meta_path: str, st: os.stat_result, nx: int, ny: int, n: int) -> dict | None:
    try:
        with open(meta_path, "r", encoding="utf-8") as f:
            m = json.load(f)
    except (OSError, ValueError):
        return None
    ok = (isinstance(m, dict) and m.get("version") == INDEX_VERSION and m.get("size") == st.st_size
          and m.get("mtime_ns") == st.st_mtime_ns and (m.get("nx"), m.get("ny"), m.get("n_gauss")) == (nx, ny, n))
    return m if ok else None


def _save_meta(meta_path: str, m: dict) -> None:
    tmp = meta_path + ".tmp"
    with open(tmp, "w", encoding="utf-8") as f:
        json.dump(m, f)
    os.replace(tmp, meta_path)


def _refuse_links(*paths: str) -> None:
    """A sidecar that exists must be a plain file: a link in the cache folder must never be followed by a write."""
    for p in paths:
        try:
            st = os.lstat(p)
        except FileNotFoundError:
            continue
        if not stat.S_ISREG(st.st_mode):
            raise Bad("unsupported", "a cache file for the index is not a plain file; it is not used")


def _check_args(nx: int, ny: int, n: int) -> None:
    for v in (nx, ny):
        if not isinstance(v, int) or isinstance(v, bool) or v < 1:
            raise Bad("bad_request", "bad crop shape")
    if not isinstance(n, int) or isinstance(n, bool) or not 1 <= n <= MAX_GAUSSIANS:
        raise Bad("bad_request", f"n_gauss must be an integer in [1, {MAX_GAUSSIANS}]")


def _scan_fast(buf: bytes, base: int, row0: int, nx: int, n: int, lay: Layout = DEFAULT_LAYOUT):
    """Offsets of pixel starts for a chunk that is all data lines, or None when the chunk needs the careful path."""
    if b"#" in buf:
        return None
    lines = buf.split(b"\n")[:-1]                       # buf ends with a newline
    if not lines:
        return np.empty(0, dtype=np.uint64)
    toks = buf.split()
    if len(toks) != lay.ncols * len(lines):
        return None
    lens = np.fromiter((len(x) for x in lines), dtype=np.int64, count=len(lines)) + 1
    if int(lens.max()) > MAX_ROW_BYTES:
        return None
    try:
        vals = np.array(toks, dtype="S").astype(np.float64).reshape(-1, lay.ncols)
    except ValueError:
        return None
    if not np.isfinite(vals).all():
        return None
    r = row0 + np.arange(len(lines), dtype=np.int64)
    p = r // n
    if not (np.array_equal(vals[:, lay.row], p // nx + lay.base) and np.array_equal(vals[:, lay.col], p % nx + lay.base)):
        return None
    starts = base + np.cumsum(lens) - lens
    return starts[(r % n) == 0].astype(np.uint64)


def _scan_slow(buf: bytes, base: int, row0: int, nx: int, n: int, line0: int, examples: list, lay: Layout = DEFAULT_LAYOUT):
    """Line by line: skips comment and blank lines, reports every malformed row (up to a cap) and builds offsets."""
    offs: list[int] = []
    nbad = 0
    r, pos, lineno = row0, 0, line0
    for raw in buf.split(b"\n")[:-1]:
        start, pos = pos, pos + len(raw) + 1
        lineno += 1
        s = raw.strip()
        if not s or s.startswith(b"#"):
            continue
        problem = None
        if len(raw) + 1 > MAX_ROW_BYTES:
            problem = f"longer than {MAX_ROW_BYTES} bytes"
        else:
            t = s.split()
            try:
                if len(t) != lay.ncols:
                    raise ValueError(f"{len(t)} columns instead of {lay.ncols}")
                v = [float(x) for x in t]
                if not all(np.isfinite(v)):
                    raise ValueError("a value is not finite")
                p = r // n
                if v[lay.row] != p // nx + lay.base or v[lay.col] != p % nx + lay.base:
                    raise ValueError(f"row is for pixel ({v[lay.row]:g}, {v[lay.col]:g}) but position {r + 1} should be ({p // nx + lay.base}, {p % nx + lay.base})")
            except ValueError as e:
                problem = str(e)
        if problem:
            nbad += 1
            if len(examples) < MAX_EXAMPLES:
                examples.append({"line": lineno, "problem": problem})
            r += 1
            continue
        if r % n == 0:
            offs.append(base + start)
        r += 1
    return np.array(offs, dtype=np.uint64), r - row0, nbad


def index_step(dat_path: str, cache_dir: str, nx: int, ny: int, n: int, budget_s: float = 15.0, layout: Layout = DEFAULT_LAYOUT) -> dict:
    """Continue (or start) the index of one .dat for a crop of nx x ny pixels and n Gaussians; returns its state.

    state is "building" (call again), "ready", or "refused" with a reason and example rows: the file is not the layout
    described above, so no pixel of it is read.
    """
    _check_args(nx, ny, n)
    st = stat_source(dat_path)
    os.makedirs(cache_dir, exist_ok=True)
    key = _key(dat_path, st, layout)
    meta_path, offs_path = _paths(cache_dir, key)
    _refuse_links(meta_path, offs_path, meta_path + ".tmp")
    m = _load_meta(meta_path, st, nx, ny, n)
    expected_rows = nx * ny * n
    created, was_building = m is None, m is None or m["state"] == "building"
    if m is None:
        m = {"version": INDEX_VERSION, "size": st.st_size, "mtime_ns": st.st_mtime_ns, "nx": nx, "ny": ny, "n_gauss": n,
             "state": "building", "done": 0, "rows": 0, "lines": 0, "pixels": 0, "bad_count": 0, "examples": [], "reason": None}
        open(offs_path, "wb").close()
    elif m["state"] != "building":
        return {**_summary(m, st), "created": False, "settled": False, "cache_file": offs_path}
    t0 = time.monotonic()
    # the sidecar may hold more offsets than the metadata admits (a call died between the two writes): trust the metadata
    with open(offs_path, "r+b") as out, source_file(dat_path) as (_, f):
        out.truncate(m["pixels"] * 8)
        out.seek(0, os.SEEK_END)
        while m["state"] == "building":
            f.seek(m["done"])
            buf = f.read(CHUNK_BYTES)
            if not buf:
                break
            eof = m["done"] + len(buf) >= st.st_size
            cut = buf.rfind(b"\n")
            if eof:
                consumed = len(buf)
                if cut != len(buf) - 1:
                    buf += b"\n"            # the last line has no newline
            elif cut < 0:
                m.update(state="refused", reason=f"a line is longer than {CHUNK_BYTES} bytes")
                break
            else:
                consumed = cut + 1
                buf = buf[:consumed]
            nlines = buf.count(b"\n")
            offs = _scan_fast(buf, m["done"], m["rows"], nx, n, layout)
            if offs is not None:
                rows = nlines
            else:
                offs, rows, nbad = _scan_slow(buf, m["done"], m["rows"], nx, n, m["lines"], m["examples"], layout)
                m["bad_count"] += nbad
                if nbad:
                    m.update(state="refused", reason=f"{m['bad_count']} malformed row(s); the file is not the ROHSA row layout")
            out.write(offs.tobytes())
            m["pixels"] += len(offs)
            m["rows"] += rows
            m["lines"] += nlines
            m["done"] += consumed
            if m["state"] == "refused":
                break
            if m["rows"] > expected_rows:
                m.update(state="refused", reason=f"the file has more rows than {nx} x {ny} pixels x {n} Gaussians = {expected_rows}")
            elif time.monotonic() - t0 >= budget_s:
                break               # out of time after at least one chunk: the next call continues
    if m["state"] == "building" and m["done"] >= st.st_size:
        if m["rows"] != expected_rows:
            m.update(state="refused", reason=f"the file has {m['rows']} data rows; a {nx} x {ny} crop with {n} Gaussians needs {expected_rows}")
        else:
            m["state"] = "ready"
    _save_meta(meta_path, m)
    out = _summary(m, st)
    # what this call did, for the caller's audit: it began an index, and/or it settled one (ready or refused)
    out.update(created=created, settled=was_building and m["state"] != "building", cache_file=offs_path)
    return out


def _summary(m: dict, st: os.stat_result) -> dict:
    return {"state": m["state"], "done_bytes": m["done"], "size_bytes": st.st_size, "rows": m["rows"], "reason": m["reason"],
            "malformed": m["bad_count"], "examples": m["examples"], "index_bytes": m["pixels"] * 8}


def read_pixel(dat_path: str, cache_dir: str, nx: int, ny: int, n: int, x: int, y: int, layout: Layout = DEFAULT_LAYOUT):
    """(amp, raw centre, raw width) of the n components at pixel (x, y), each row verified, as the file states them (for the default
    layout, the 1-based mean in channels and sigma in channels)."""
    _check_args(nx, ny, n)
    if not (0 <= x < nx and 0 <= y < ny):
        raise Bad("bad_request", "pixel outside the crop")
    st = stat_source(dat_path)
    meta_path, offs_path = _paths(cache_dir, _key(dat_path, st, layout))
    m = _load_meta(meta_path, st, nx, ny, n)
    if m is None or m["state"] == "building":
        raise Bad("index_not_ready", "the .dat has not been indexed yet")
    if m["state"] != "ready":
        raise Bad("unsupported", m["reason"] or "the .dat could not be indexed")
    p = y * nx + x
    with open(offs_path, "rb") as f:
        f.seek(p * 8)
        raw = f.read(8)
    if len(raw) != 8:
        raise Bad("unsupported", "the index is shorter than the crop; rebuild it")
    off = int(np.frombuffer(raw, dtype="<u8")[0])
    with source_file(dat_path) as (_, f):
        f.seek(off)
        chunk = f.read(n * MAX_ROW_BYTES)
    rows = []
    for line in chunk.split(b"\n"):
        s = line.strip()
        if not s or s.startswith(b"#"):
            continue
        rows.append(s.split())
        if len(rows) == n:
            break
    if len(rows) != n:
        raise Bad("unsupported", "the .dat changed since it was indexed; rebuild")
    try:
        v = np.array([[float(t) for t in r] for r in rows])
    except ValueError:
        raise Bad("unsupported", "the .dat changed since it was indexed; rebuild")
    if v.shape[1] != layout.ncols or not (np.all(v[:, layout.row] == y + layout.base) and np.all(v[:, layout.col] == x + layout.base)):
        raise Bad("unsupported", "the .dat changed since it was indexed; rebuild")
    return v[:, layout.amp].copy(), v[:, layout.centre].copy(), v[:, layout.width].copy()


def gaussian(channels: np.ndarray, amp: float, mean0: float, sigma: float) -> np.ndarray:
    """amp * exp(-(c - mean0)^2 / (2 sigma^2)) on 0-based channel numbers, mean0 0-based. sigma must be positive."""
    return amp * np.exp(-((channels - mean0) ** 2) / (2.0 * sigma ** 2))


def components_at(amp: np.ndarray, mean_raw: np.ndarray, sigma_ch: np.ndarray, nc: int, world, dv: float | None) -> tuple[list, np.ndarray, np.ndarray]:
    """Components and curves on the crop's channel grid, for a table in ROHSA's convention: the file's mean counts channels from 1.

    world(c) maps 0-based (fractional) channel numbers to the velocity axis in km/s, dv is |km/s per channel|; both are
    None when the spectral axis is not a resolved velocity, and then centre and width are given in channels only.
    A component whose sigma is not positive and finite is invalid: listed, flagged, and left out of the curves (no floor
    is invented). An amplitude of 0 is a real fitted value and stays.
    """
    return components_from_channels(amp, mean_raw - 1.0, sigma_ch, nc, world, dv, raw_centre=mean_raw)   # the file counts channels from 1


def components_from_channels(amp: np.ndarray, mean0: np.ndarray, sigma_ch: np.ndarray, nc: int, world, dv: float | None,
                             raw_centre=None, raw_width=None, present=None) -> tuple[list, np.ndarray, np.ndarray]:
    """The same for components already in the grid's terms: mean0 the 0-based (fractional) channel of the centre, sigma_ch in channels.

    present False marks a slot a tool did not fit here (NaN in the source): listed as such, never drawn, never zero. raw_centre and
    raw_width are what the source states, kept beside the converted values.
    """
    ch = np.arange(nc, dtype=np.float64)
    comps, curves = [], np.zeros((len(amp), nc))
    for k in range(len(amp)):
        a, m0, s = float(amp[k]), float(mean0[k]), float(sigma_ch[k])
        here = True if present is None else bool(present[k])
        valid = bool(here and np.isfinite(a) and np.isfinite(m0) and np.isfinite(s) and s > 0)
        rc = None if raw_centre is None else float(raw_centre[k])
        c = {"k": k + 1, "present": here, "amp": a if here else None, "mean_channel_file": rc, "mean_channel": m0 if here else None,
             "sigma_channel": s if here else None, "valid": valid, "centre": None, "sigma": None, "fwhm": None, "t_doppler_k": None,
             "centre_raw": rc, "width_raw": None if raw_width is None else float(raw_width[k])}
        if valid:
            curves[k] = gaussian(ch, a, m0, s)
            if world is not None and dv is not None:
                c["centre"] = float(world(np.array([m0]))[0])
                c["sigma"] = s * dv
                c["fwhm"] = float(FWHM_PER_SIGMA) * c["sigma"]
                c["t_doppler_k"] = DOPPLER_K_PER_KMS2 * c["sigma"] ** 2
        comps.append(c)
    model = curves.sum(axis=0) if len(amp) else np.zeros(nc)
    return comps, curves, model
