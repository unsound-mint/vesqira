"""Typed masks (ADR 0055): derived uint8 FITS files that flag pixels of a dataset for a stated reason, and the reading of them by
an operation that takes masks.

A mask file holds 0 (not flagged) and 1 (flagged), as (ny, nx) when it applies to every channel or (channels, ny, nx) with one
plane per channel. The axes of a 3D mask are channel, y, x whatever the order of the data file's own axes, and the header says
so (VESQMAXS). The type, the method and the data file's name are header cards (VESQMTYP, VESQMTHD, VESQMSRC); the rule (the
estimator, the threshold, where sigma came from) is the HISTORY of the file and the summary the program prints.

Rules this module keeps (AGENTS.md invariants 5 and 8):
- A pixel with no value (NaN, BLANK, infinite) is the type `missing`. It is flagged by no other type: a value that is not there
  is neither below a threshold nor saturated. Zero is a value and is flagged by nothing unless the stated rule says so.
- A mask is a new file. Nothing here writes to a data file.
- Applying a mask is the operation's decision, per mask: `exclude` (the flagged pixel is left out), `keep` (it stays in) or, for an
  operation that already has weights, `weight` (it counts with a factor). There is no default policy and no global one.
"""
from __future__ import annotations

import json
import math
import os

import numpy as np
from astropy.io import fits

from plane_worker import Cube, Refused, authorize, fits_array

TYPES = ("missing", "invalid", "low_snr", "saturated", "user_excluded", "other")
POLICIES = ("exclude", "keep", "weight")
MAX_MASK_ELEMENTS = 1 << 28
MAD_TO_SIGMA = 1.4826


class MaskError(Exception):
    """A refusal with a sentence for the person."""


def _cube_header(c: Cube, extra: dict, channels: int) -> fits.Header:
    """The mask's header: the data's spatial WCS (and its spectral axis for a per-channel mask), nothing about brightness."""
    h = fits.Header()
    h["SIMPLE"], h["BITPIX"] = True, 8
    nx, ny = c.shape[c.x], c.shape[c.y]
    h["NAXIS"] = 3 if channels else 2
    h["NAXIS1"], h["NAXIS2"] = nx, ny
    if channels:
        h["NAXIS3"] = channels
    pairs = [(c.x, 1), (c.y, 2)] + ([(c.chan, 3)] if channels and c.chan is not None else [])
    for src, dst in pairs:
        for key in ("CTYPE", "CRVAL", "CRPIX", "CDELT", "CUNIT"):
            if f"{key}{src + 1}" in c.h:
                h[f"{key}{dst}"] = c.h[f"{key}{src + 1}"]
    for a, da in pairs[:2]:
        for b, db in pairs[:2]:
            for prefix in ("PC", "CD"):
                if f"{prefix}{a + 1}_{b + 1}" in c.h:
                    h[f"{prefix}{da}_{db}"] = c.h[f"{prefix}{a + 1}_{b + 1}"]
    for key in ("RADESYS", "EQUINOX", "LONPOLE", "LATPOLE"):
        if key in c.h:
            h[key] = c.h[key]
    h["VESQMAXS"] = ("x,y,channel" if channels else "x,y", "axes of the mask array, in FITS order")
    for k, v in extra.items():
        h[k] = v
    return h


def write_mask(path: str, flags: np.ndarray, c: Cube, mtype: str, method: str, history: list[str]) -> None:
    """Write a 0/1 uint8 mask atomically. `flags` is (ny, nx) or (channels, ny, nx)."""
    from analysis import write_fits
    data = np.ascontiguousarray(flags.astype(np.uint8))
    if data.ndim not in (2, 3) or data.size > MAX_MASK_ELEMENTS:
        raise MaskError("the mask is too large")
    h = _cube_header(c, {"VESQMTYP": mtype, "VESQMTHD": method, "VESQMSRC": os.path.basename(c.path)[:60],
                         "VESQMVAL": ("1 = flagged", "0 = not flagged")}, data.shape[0] if data.ndim == 3 else 0)
    for line in history:
        h["HISTORY"] = line[:68]
    write_fits(path, data, h)


def _to_cyx(raw: np.ndarray, c: Cube) -> np.ndarray:
    """An array in the data file's own native axis order, as (channel, y, x): the other axes are of length 1."""
    n = len(c.shape)
    kept = [k for k in range(n) if (n - 1 - k) in (c.x, c.y, c.chan)]      # numpy axis k is FITS axis n-1-k
    a = raw[tuple(slice(None) if k in kept else 0 for k in range(n))]
    return np.transpose(a, [kept.index(n - 1 - c.chan), kept.index(n - 1 - c.y), kept.index(n - 1 - c.x)])


def _channel_planes(c: Cube):
    from analysis import planes
    return planes(c, 0, c.channels - 1)


def build_missing(c: Cube, params: dict | None = None):
    """(flags, rule): the pixels with no value. Computed from the data; there is nothing to declare."""
    out = np.zeros((c.channels, c.shape[c.y], c.shape[c.x]), dtype=bool)
    for ch, p in _channel_planes(c):
        out[ch] = ~np.isfinite(p)
    rule = {"rule": "no value: NaN, BLANK or infinite in the file", "estimator": None, "threshold": None}
    return (out[0] if c.chan is None else out), rule


def _sigma_for(c: Cube, spec: dict):
    """(sigma source description, function channel -> sigma array or scalar)."""
    kind = spec.get("kind")
    if kind == "value":
        v = spec.get("value")
        if isinstance(v, bool) or not isinstance(v, (int, float)) or not math.isfinite(v) or v <= 0:
            raise MaskError("sigma must be a positive number")
        return {"kind": "value", "value": float(v), "source": "a value stated by the researcher"}, lambda ch, p: float(v)
    if kind == "mad":
        def mad(ch, p):
            fin = p[np.isfinite(p)]
            if fin.size == 0:
                return float("nan")
            return float(MAD_TO_SIGMA * np.median(np.abs(fin - np.median(fin))))
        return ({"kind": "mad", "source": "a robust estimate: 1.4826 x the median absolute deviation of the finite values of each channel, "
                 "about their median. It includes the signal, so it overestimates sigma in a channel with bright emission"}, mad)
    if kind == "map":
        real = authorize(spec.get("path"))
        sc = Cube(real)
        if (sc.shape[sc.y], sc.shape[sc.x]) != (c.shape[c.y], c.shape[c.x]):
            raise MaskError("the noise map is not on the same pixel grid as the data")
        if sc.channels not in (1, c.channels):
            raise MaskError("the noise map has neither one plane nor one per channel of the data")
        from analysis import planes
        one = next(planes(sc, 0, 0))[1] if sc.channels == 1 else None

        def from_map(ch, p):
            return one if one is not None else next(planes(sc, ch, ch))[1]
        return {"kind": "map", "source": "a noise map declared by the researcher", "name": os.path.basename(real)}, from_map
    raise MaskError("sigma is a value, a noise map, or the robust estimate")


def build_low_snr(c: Cube, params: dict):
    """value / sigma < k is low SNR. A pixel with no value, or with no usable sigma (NaN, zero or negative), is not low SNR: it is
    `missing` or has an unknown sigma, and is counted apart."""
    import catalog_table as ct
    k = params.get("k")
    if isinstance(k, bool) or not isinstance(k, (int, float)) or not math.isfinite(k) or k <= 0:
        raise MaskError("the threshold k must be a positive number")
    sig, sigma_of = _sigma_for(c, params.get("sigma") or {})
    out = np.zeros((c.channels, c.shape[c.y], c.shape[c.x]), dtype=bool)
    unknown = 0
    for ch, p in _channel_planes(c):
        s = sigma_of(ch, p)
        sg = np.broadcast_to(np.asarray(s, dtype=float), p.shape)
        keep, st = ct.snr_filter(p, sg, float(k))
        out[ch] = st == "below"
        unknown += int(np.count_nonzero((st == "missing") & np.isfinite(p)))
    rule = {"rule": f"value / sigma < {k:g}", "estimator": sig["kind"], "threshold": float(k), "sigma": sig,
            "sigma_unknown_pixels": unknown,
            "note": "A pixel with no value is not flagged here (it is `missing`); a pixel whose sigma is unknown, zero or negative is not flagged "
                    "either and is counted in sigma_unknown_pixels."}
    return (out[0] if c.chan is None else out), rule


def build_saturated(c: Cube, params: dict):
    t, src = params.get("threshold"), params.get("source")
    if isinstance(t, bool) or not isinstance(t, (int, float)) or not math.isfinite(t):
        raise MaskError("the saturation threshold must be a number")
    if not isinstance(src, str) or not src.strip() or len(src) > 200:
        raise MaskError("say where the saturation threshold comes from (up to 200 characters)")
    out = np.zeros((c.channels, c.shape[c.y], c.shape[c.x]), dtype=bool)
    for ch, p in _channel_planes(c):
        with np.errstate(invalid="ignore"):
            out[ch] = np.isfinite(p) & (p >= float(t))          # at or above; a pixel with no value is never saturated
    rule = {"rule": f"value >= {t:g}", "estimator": None, "threshold": float(t), "source": src.strip(),
            "note": "The threshold is the researcher's, in the unit of the data as the file or the declaration gives it; it was not checked against the instrument."}
    return (out[0] if c.chan is None else out), rule


def build_regions(c: Cube, params: dict):
    """The union of saved regions, by the pixel-centre rule the region spectrum and statistics use (Cube.region_mask)."""
    regs = params.get("regions")
    if not isinstance(regs, list) or not regs or len(regs) > 200:
        raise MaskError("name at least one saved region")
    out = np.zeros((c.shape[c.y], c.shape[c.x]), dtype=bool)
    for r in regs:
        try:
            _, (bx0, bx1, by0, by1), m, _, _ = c.region_mask(r)
        except Refused as e:
            raise MaskError(f"region {r.get('name') or r.get('shape')}: {e.message}") from None
        out[by0:by1 + 1, bx0:bx1 + 1] |= m
    rule = {"rule": "pixels whose centre is inside a region", "estimator": None, "threshold": None, "regions": [
        {k: v for k, v in r.items() if k in ("name", "shape", "x0", "y0", "x1", "y1", "cx", "cy", "r")} for r in regs],
        "note": "Applies to every channel. A region overhanging the image is clipped to it."}
    return out, rule


def build_import(c: Cube, params: dict):
    """A mask the researcher provided: a FITS image of the data's own shape. `nonzero` says what a nonzero pixel means: it is flagged
    (`flagged`), or it is good and zero is flagged (`valid`). A file with a pixel with no value says neither, and is refused."""
    path, nonzero = params.get("path"), params.get("nonzero")
    if nonzero not in ("flagged", "valid"):
        raise MaskError("say what a nonzero pixel of the mask file means: flagged, or valid")
    real = authorize(path)
    with fits.open(real, memmap=True, mode="denywrite", do_not_scale_image_data=False) as hdul:
        hdu = hdul[0]
        n = int(hdu.header.get("NAXIS", 0))
        size = int(np.prod([int(hdu.header.get(f"NAXIS{i + 1}", 0)) for i in range(n)])) if n else 0
        if n < 2 or size == 0 or size > MAX_MASK_ELEMENTS:
            raise MaskError("the mask file is not an image of a size that can be read")
        raw = np.array(hdu.data)
    if raw.dtype.kind == "f" and not np.isfinite(raw).all():
        raise MaskError("the mask file has pixels with no value (NaN), which are neither flagged nor not flagged; fix the file or leave them out")
    native = tuple(reversed(c.shape))
    ny, nx = c.shape[c.y], c.shape[c.x]
    sq = raw.squeeze()
    if raw.shape == native and c.chan is not None and c.channels > 1:
        flags = _to_cyx(raw, c) != 0
        form = "one plane per channel"
    elif sq.shape == (ny, nx):
        flags = sq != 0
        form = "one plane for every channel"
    elif c.chan is not None and c.channels > 1 and sq.shape == (c.channels, ny, nx):
        flags = sq != 0
        form = "one plane per channel"
    else:
        raise MaskError(f"the mask file's shape {tuple(raw.shape)} is neither the data's plane ({ny}, {nx}) nor its cube ({c.channels}, {ny}, {nx})")
    if nonzero == "valid":
        flags = ~flags
    rule = {"rule": f"imported from {os.path.basename(real)}: a nonzero pixel is {'flagged' if nonzero == 'flagged' else 'valid, so zero is flagged'}",
            "estimator": None, "threshold": None, "file": os.path.basename(real), "nonzero": nonzero, "form": form}
    return flags, rule


METHODS = {"missing": build_missing, "low_snr": build_low_snr, "saturated": build_saturated, "regions": build_regions, "import": build_import}


def op_mask(args) -> dict:
    """analysis.py mask --input DATA --out MASK.fits --params JSON: build one mask of the data (the type is the caller's)."""
    from analysis import open_cube, fail
    try:
        p = json.loads(args.params) if args.params else {}
        if not isinstance(p, dict):
            raise ValueError
    except ValueError:
        fail("the parameters are not a JSON object")
    method, mtype = p.get("method"), p.get("type")
    if method not in METHODS or mtype not in TYPES:
        fail("unknown mask method or type")
    if mtype == "other" and not (isinstance(p.get("meaning"), str) and p["meaning"].strip()):
        fail("a mask of type other needs a description of what it flags")
    c = open_cube(args.input)
    if (method == "missing") != (mtype == "missing"):
        fail("the type missing is computed from the data (and only that); every other type is stated by the researcher")
    method = {"regions": "regions", "import": "import"}.get(method, "computed")       # how it was made: by the worker from the data and a stated rule, ...
    try:
        flags, rule = METHODS[p["method"]](c, p)
        hist = [f"Vesqira mask {mtype} ({method}): {rule['rule']}"]
        write_mask(args.out, flags, c, mtype, method, hist)
    except MaskError as e:
        fail(str(e))
    total = int(flags.size)
    return {"ok": True, "op": "mask", "summary": {
        "type": mtype, "method": method, "flagged": int(flags.sum()), "total": total, "width": int(flags.shape[-1]), "height": int(flags.shape[-2]),
        "channels": int(flags.shape[0]) if flags.ndim == 3 else 0, "rule": rule,
        "policy": "A mask flags pixels for a stated reason and changes nothing in the data; an operation says per mask whether to exclude, keep or weight."}}


# ---------------------------------------------------------------------------------------------------------------
# Applying masks

def parse_specs(text: str | None) -> list[dict]:
    """The --masks option: a JSON list of {path, type, policy[, weight, id, name]}. Every mask has an explicit policy."""
    if not text:
        return []
    try:
        specs = json.loads(text)
    except ValueError:
        raise MaskError("the masks are not a JSON list") from None
    if not isinstance(specs, list) or len(specs) > 16:
        raise MaskError("at most 16 masks can be applied at once")
    for s in specs:
        if not isinstance(s, dict) or s.get("type") not in TYPES or s.get("policy") not in POLICIES:
            raise MaskError("every mask needs a type and a policy (exclude, keep or weight); there is no default")
        if s["type"] == "missing" and s["policy"] != "exclude":
            raise MaskError("a pixel with no value cannot be kept or weighted: the policy for `missing` is exclude")
        if s["policy"] == "weight":
            w = s.get("weight")
            if isinstance(w, bool) or not isinstance(w, (int, float)) or not 0.0 <= w <= 1.0:
                raise MaskError("the weight policy needs a factor from 0 to 1 for a flagged pixel")
    return specs


class Applied:
    """The masks an operation takes, opened for reading plane by plane. Use as a context manager; `plane(ch, p)` gives the pixels to
    leave out and the weight of each, and `record()` the per-mask counts for the result's summary."""

    def __init__(self, c: Cube, specs: list[dict], allow_weight: bool):
        self.c, self.specs, self.files, self.arrays = c, specs, [], []
        self.stats = [{"flagged_with_value": 0, "excluded": 0} for _ in specs]
        self.unique_excluded = 0
        if not specs:
            return
        if not allow_weight and any(s["policy"] == "weight" for s in specs):
            raise MaskError("this operation has no weights, so a mask cannot be applied with the weight policy: exclude it or keep it")

    def __enter__(self):
        ny, nx = self.c.shape[self.c.y], self.c.shape[self.c.x]
        for s in self.specs:
            real = authorize(s["path"])
            hdul = fits.open(real, memmap=True, mode="denywrite", do_not_scale_image_data=True)
            self.files.append(hdul)
            h = hdul[0].header
            if h.get("VESQMTYP") != s["type"]:
                raise MaskError(f"the mask file is not a {s['type']} mask")
            shape = tuple(hdul[0].data.shape) if h.get("NAXIS") in (2, 3) else ()
            if shape == (ny, nx):
                pass
            elif len(shape) == 3 and shape == (self.c.channels, ny, nx):
                pass
            else:
                raise MaskError(f"a {s['type']} mask has shape {shape}, not the data's ({ny}, {nx}) or ({self.c.channels}, {ny}, {nx})")
            self.arrays.append(hdul[0].data)
        return self

    def __exit__(self, *a):
        for f in self.files:
            f.close()

    def plane(self, ch: int, p: np.ndarray):
        """(exclude, weight) for channel `ch` whose values are `p`: a bool plane of pixels to leave out, and a float plane of weights
        (1 where nothing says otherwise). Counts the flagged pixels that have a value."""
        ex = np.zeros(p.shape, dtype=bool)
        w = np.ones(p.shape, dtype=float)
        fin = np.isfinite(p)
        for i, (s, a) in enumerate(zip(self.specs, self.arrays)):
            m = np.asarray(a[ch] if a.ndim == 3 else a) != 0
            self.stats[i]["flagged_with_value"] += int(np.count_nonzero(m & fin))
            if s["policy"] == "exclude":
                ex |= m
                self.stats[i]["excluded"] += int(np.count_nonzero(m & fin))
            elif s["policy"] == "weight":
                w = np.where(m, w * float(s["weight"]), w)
        self.unique_excluded += int(np.count_nonzero(ex & fin))
        return ex, w

    def plane_quiet(self, ch: int, p: np.ndarray):
        """The same planes as `plane`, without counting (a second pass over the same channels)."""
        saved, uniq = [dict(s) for s in self.stats], self.unique_excluded
        got = self.plane(ch, p)
        self.stats, self.unique_excluded = saved, uniq
        return got

    def record(self) -> list[dict]:
        out = []
        for s, st in zip(self.specs, self.stats):
            row = {k: s[k] for k in ("id", "name", "type", "policy", "weight", "tier", "rule", "fingerprint") if k in s}
            row.update(pixels_flagged_with_value=st["flagged_with_value"], pixels_excluded=st["excluded"])
            out.append(row)
        return out


def masks_policy_sentence(specs: list[dict]) -> str:
    if not specs:
        return ""
    parts = [f"{s['type']} masks {'excluded' if s['policy'] == 'exclude' else 'kept' if s['policy'] == 'keep' else 'weighted by ' + format(s['weight'], 'g')}"
             for s in specs]
    return "Masks applied (a policy per mask, never global): " + "; ".join(parts) + ". Pixels with no value are never counted, and zero is never treated as masked."


def mask_plane(path: str, channel: int) -> tuple[dict, bytes]:
    """The plane of a mask file for the viewer: uint8, row 0 = FITS y 1. A 2D mask is the plane of every channel."""
    real = authorize(path)
    with fits.open(real, memmap=True, mode="denywrite", do_not_scale_image_data=True) as hdul:
        h = hdul[0].header
        if h.get("VESQMTYP") not in TYPES or h.get("NAXIS") not in (2, 3):
            raise Refused("unreadable", "not a Vesqira mask")
        a = hdul[0].data
        if a.ndim == 3:
            if not 0 <= channel < a.shape[0]:
                raise Refused("bad_request", "channel out of range")
            plane = np.array(a[channel])
        else:
            plane = np.array(a)
    plane = (plane != 0).astype(np.uint8)
    return {"ok": True, "op": "mask_plane", "origin": 0, "width": int(plane.shape[1]), "height": int(plane.shape[0]), "channel": channel,
            "channels": int(a.shape[0]) if a.ndim == 3 else 0, "type": h.get("VESQMTYP"), "dtype": "|u1", "flagged": int(plane.sum()),
            "nbytes": int(plane.nbytes)}, plane.tobytes()
