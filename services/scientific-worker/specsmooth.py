"""Spectral smoothing of a cube along its spectral axis, as a derived operation (ADR 0070).

The arithmetic lives here; analysis.py parses the request, opens the cube, and writes the result with the usual header and
provenance. Nothing here reads a file.

Rules this module keeps:
- The kernel is stated exactly: its weights (they sum to 1), its width in channels, and its FWHM in channels. Boxcar and
  Hanning widths are odd numbers of channels (an even window would shift the spectral axis by half a channel); a Gaussian is
  given by its FWHM in channels, sampled at the channel centres and cut at four sigma.
- Missing data stays missing (the same policy as the spatial convolution): a sample that is NaN is NaN in the result, and a
  finite sample is the average of the finite samples its kernel reaches (a normalised convolution), provided those samples
  carry at least `min_valid_weight` of the kernel. Outside the cube counts as missing, never as zero.
- scipy.ndimage does the convolution; the cube is read in strips of rows, so memory is bounded by the strip and the output.
"""
from __future__ import annotations

import math

import numpy as np
from scipy.ndimage import convolve1d

KINDS = ("boxcar", "gaussian", "hanning")
FWHM_PER_SIGMA = 2.0 * math.sqrt(2.0 * math.log(2.0))
MAX_HALF_WIDTH = 512            # channels each side of the centre
STRIP_BYTES = 32 << 20          # float64 bytes of one strip of the input


class Bad(Exception):
    """A request the operation refuses; the message is shown to the person."""


def build_kernel(kind: str, width: float) -> dict:
    """The kernel and what it means: {kind, width_channels, weights (sum 1), half_width, fwhm_channels, definition}."""
    if kind not in KINDS:
        raise Bad(f"the kernel must be one of {', '.join(KINDS)}")
    if not (isinstance(width, (int, float)) and math.isfinite(width) and width > 0):
        raise Bad("the kernel width must be a positive number of channels")
    if kind in ("boxcar", "hanning"):
        n = round(width)
        if abs(width - n) > 1e-9 or n < 3 or n % 2 == 0:
            raise Bad(f"a {kind} width is an odd whole number of channels, 3 or more (an even window would shift the spectral axis by half a channel)")
        if n // 2 > MAX_HALF_WIDTH:
            raise Bad(f"the kernel is wider than {2 * MAX_HALF_WIDTH + 1} channels")
        if kind == "boxcar":
            w = np.full(n, 1.0 / n)
            fwhm = float(n)
            text = f"boxcar: the mean of {n} consecutive channels"
        else:
            w = np.hanning(n + 2)[1:-1]            # the Hann window without its two zero ends: 3 channels give 1/4, 1/2, 1/4
            w = w / w.sum()
            fwhm = (n + 1) / 2.0
            text = f"Hanning: a Hann window over {n} channels (its two zero end points excluded), weights normalised to 1"
        return {"kind": kind, "width_channels": n, "weights": w, "half_width": n // 2, "fwhm_channels": fwhm, "definition": text}
    if width < 1.0:
        raise Bad("a Gaussian narrower than 1 channel (FWHM) is not sampled by the channels: give a FWHM of at least 1")
    sigma = width / FWHM_PER_SIGMA
    half = max(1, math.ceil(4.0 * sigma))
    if half > MAX_HALF_WIDTH:
        raise Bad(f"the kernel is wider than {2 * MAX_HALF_WIDTH + 1} channels")
    k = np.arange(-half, half + 1, dtype=float)
    w = np.exp(-0.5 * (k / sigma) ** 2)
    w /= w.sum()
    text = (f"Gaussian: FWHM {width:g} channels (sigma {sigma:.4g}), sampled at the channel centres, cut at ±{half} channels (4 sigma), "
            "weights normalised to 1")
    return {"kind": kind, "width_channels": float(width), "weights": w, "half_width": half, "fwhm_channels": float(width), "definition": text}


def smooth_block(block: np.ndarray, weights: np.ndarray, min_valid: float) -> tuple[np.ndarray, int]:
    """Smooth a (channel, row, column) block along axis 0. Returns (result with NaN for no value, finite samples dropped for want of
    valid weight). A NaN sample is NaN in the result; a finite sample is sum(w x) / sum(w) over the finite samples, if sum(w) over
    them is at least min_valid (the kernel sums to 1), and NaN otherwise."""
    finite = np.isfinite(block)
    num = convolve1d(np.where(finite, block, 0.0), weights, axis=0, mode="constant", cval=0.0)
    den = convolve1d(finite.astype(np.float64), weights, axis=0, mode="constant", cval=0.0)
    ok = finite & (den >= min_valid - 1e-12) & (den > 0)
    out = np.full(block.shape, np.nan)
    np.divide(num, den, out=out, where=ok)
    return out, int((finite & ~ok).sum())


def rows_per_strip(channels: int, nx: int) -> int:
    return max(1, STRIP_BYTES // (8 * channels * nx))


def resolution(kernel: dict, native_fwhm: float | None, step: float | None, unit: str | None) -> dict:
    """What can be said about the spectral resolution of the result, in channels (and the unit when the channel width is known).
    The native response is a statement by the person; it is never measured here and never assumed. Widths add in quadrature only
    for Gaussians, so a boxcar or Hanning kernel gets no summed width."""
    def both(ch: float) -> dict:
        r = {"channels": ch}
        if step is not None and unit:
            r["unit_value"], r["unit"] = ch * step, unit
        return r
    k = kernel["fwhm_channels"]
    out = {"kernel_fwhm": both(k), "native_fwhm_declared": None, "effective_fwhm": None}
    if native_fwhm is None:
        out["statement"] = ("The native spectral response of the cube is not declared, so the resolution of the result is not known; "
                            f"the kernel adds a FWHM of {k:g} channels, and the result is no finer than that.")
        return out
    out["native_fwhm_declared"] = both(native_fwhm)
    if kernel["kind"] == "gaussian":
        eff = math.hypot(native_fwhm, k)
        out["effective_fwhm"] = both(eff)
        out["statement"] = (f"With the native response declared by you as a Gaussian of FWHM {native_fwhm:g} channels, the widths add in quadrature: "
                            f"the result has a FWHM of {eff:.4g} channels.")
    else:
        out["statement"] = (f"The native response is declared as {native_fwhm:g} channels (FWHM), but a {kernel['kind']} kernel is not Gaussian, "
                            "so widths do not add in quadrature and the resolution of the result is not stated; "
                            f"the kernel itself has a FWHM of {k:g} channels.")
    return out
