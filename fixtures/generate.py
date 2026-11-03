"""Deterministic synthetic astronomy fixtures with independently known answers.

Nothing here is real data. Every array is generated from a fixed seed or a closed formula,
so tests can assert exact expected values. Files are written on demand and never committed.
"""
from __future__ import annotations

import math
from pathlib import Path

import numpy as np
from astropy.io import fits

NX, NY, NC = 32, 24, 64          # FITS axes 1, 2, 3 (numpy shape is (NC, NY, NX))
CRVAL_RA, CRVAL_DEC = 150.0, 2.0  # degrees
CDELT_DEG = 0.01                  # pixel scale, degrees (RA axis stored negative)
CRPIX = (16.0, 12.0)              # 1-based FITS reference pixel
V0_MS, DV_MS, V_CRPIX = -32000.0, 1000.0, 1.0   # VRAD in m/s, channel 1 is -32 km/s
SOURCE_PIX = (20, 9)              # 0-based (x, y) peak of the synthetic source
SOURCE_CHANNEL = 40               # 0-based channel of the peak
PEAK_K = 12.5
NAN_BOX = (slice(2, 5), slice(2, 5))   # (y, x) block set to NaN in every channel
FLAGGED_CHANNELS = (10, 11)             # finite data, flagged bad by a separate FLAGS table (not missing)


def hi_cube() -> fits.HDUList:
    """3D H I-like cube with celestial TAN WCS and a VRAD spectral axis in m/s."""
    rng = np.random.default_rng(20261104)
    data = rng.normal(0.0, 0.2, size=(NC, NY, NX)).astype(np.float32)
    yy, xx = np.mgrid[0:NY, 0:NX]
    cc = np.arange(NC)[:, None, None]
    sx, sy = SOURCE_PIX
    data += (PEAK_K * np.exp(-(((xx - sx) ** 2 + (yy - sy) ** 2) / (2 * 2.0**2)))[None]
             * np.exp(-((cc - SOURCE_CHANNEL) ** 2) / (2 * 3.0**2))).astype(np.float32)
    data[:, NAN_BOX[0], NAN_BOX[1]] = np.nan
    h = fits.PrimaryHDU(data).header
    h["BUNIT"] = "K"
    h["BTYPE"] = "Tb"
    for i, (ct, cv, cd, cp, cu) in enumerate([
        ("RA---TAN", CRVAL_RA, -CDELT_DEG, CRPIX[0], "deg"),
        ("DEC--TAN", CRVAL_DEC, CDELT_DEG, CRPIX[1], "deg"),
        ("VRAD", V0_MS, DV_MS, V_CRPIX, "m/s"),
    ], start=1):
        h[f"CTYPE{i}"], h[f"CRVAL{i}"], h[f"CDELT{i}"], h[f"CRPIX{i}"], h[f"CUNIT{i}"] = ct, cv, cd, cp, cu
    h["RADESYS"] = "ICRS"
    h["SPECSYS"] = "LSRK"
    h["RESTFRQ"] = 1.420405751768e9
    flags = np.zeros(NC, dtype=np.uint8)
    flags[list(FLAGGED_CHANNELS)] = 1
    fh = fits.ImageHDU(flags, name="FLAGS")
    fh.header["COMMENT"] = "Synthetic per-channel flags: 1 = flagged by the data provider. Data values are kept."
    return fits.HDUList([fits.PrimaryHDU(data, header=h), fh])


def cube_other_frame() -> fits.HDUList:
    """Same cube on a Galactic frame: a different coordinate frame must not be merged with hi_cube."""
    hdul = hi_cube()
    h = hdul[0].header
    h["CTYPE1"], h["CTYPE2"] = "GLON-TAN", "GLAT-TAN"
    h["CRVAL1"], h["CRVAL2"] = 120.0, 10.0
    del h["RADESYS"]
    return hdul


def cube_coarser_pixels() -> fits.HDUList:
    """Same frame and field but 2x coarser pixel scale: resolution differs, so a match must be explicit."""
    hdul = hi_cube()
    h = hdul[0].header
    h["CDELT1"], h["CDELT2"] = -2 * CDELT_DEG, 2 * CDELT_DEG
    return hdul


def cube_missing_spectral_reference() -> fits.HDUList:
    """Same cube, but the spectral axis lacks CRVAL3/CDELT3: velocity must be reported unknown."""
    hdul = hi_cube()
    for key in ("CRVAL3", "CDELT3", "CUNIT3", "SPECSYS", "RESTFRQ"):
        del hdul[0].header[key]
    return hdul


def cube_unlabelled_pixels() -> fits.HDUList:
    """Cube with no celestial WCS at all: positions must fall back to pixels, never invented degrees."""
    hdul = hi_cube()
    for i in (1, 2):
        for k in ("CTYPE", "CRVAL", "CDELT", "CRPIX", "CUNIT"):
            del hdul[0].header[f"{k}{i}"]
    del hdul[0].header["RADESYS"]
    return hdul


# ----- VGT-style orientation maps (axial angles, degrees, axis 0 and 180 identical) -----

def vgt_angle_map() -> dict[str, np.ndarray]:
    """4x4 axial angle map in [0, 180) with reliability in [0, 1]; NaN where the result is missing."""
    angle = np.array([
        [1.0, 179.0, 90.0, 45.0],
        [0.0, 180.0 - 1e-9, 89.0, 91.0],
        [10.0, 170.0, 135.0, 45.0],
        [0.0, 90.0, np.nan, 30.0],
    ])
    reliability = np.array([
        [0.9, 0.9, 0.5, 1.0],
        [0.0, 0.0, 0.8, 0.8],      # reliability 0 is a real value, not "missing"
        [0.7, 0.7, 0.6, 0.6],
        [1.0, 1.0, np.nan, 0.2],   # NaN reliability: unknown
    ])
    return {"angle_deg": angle, "reliability": reliability}


def axial_mean_deg(angles_deg, min_resultant: float = 1e-6):
    """Oracle: mean orientation via doubled-angle vectors. Returns (mean in [0,180), resultant length).

    Mean is None when the resultant is degenerate (orientations cancel), never an arbitrary angle.
    """
    a = np.asarray([x for x in angles_deg if not math.isnan(x)], dtype=float)
    if a.size == 0:
        return None, 0.0
    z = np.exp(2j * np.radians(a)).mean()
    r = float(abs(z))
    if r < min_resultant:
        return None, r
    return float((np.degrees(np.angle(z)) / 2.0) % 180.0), r


def vgt_hdul() -> fits.HDUList:
    """Synthetic VGT-style result: angle (deg, axial) and reliability as separate image HDUs."""
    m = vgt_angle_map()
    a = fits.PrimaryHDU()
    a.header["COMMENT"] = "Synthetic fixture, not a measurement."
    ang = fits.ImageHDU(m["angle_deg"].astype(np.float64), name="ANGLE")
    ang.header["BUNIT"] = "deg"
    ang.header["ORIENT"] = "axial"
    rel = fits.ImageHDU(m["reliability"].astype(np.float64), name="RELIABILITY")
    rel.header["BUNIT"] = ""
    return fits.HDUList([a, ang, rel])


def write_all(directory: Path) -> dict[str, Path]:
    directory.mkdir(parents=True, exist_ok=True)
    out = {}
    for name, hdul in {
        "hi_cube.fits": hi_cube(),
        "hi_cube_no_spectral_ref.fits": cube_missing_spectral_reference(),
        "hi_cube_no_celestial_wcs.fits": cube_unlabelled_pixels(),
        "hi_cube_galactic.fits": cube_other_frame(),
        "hi_cube_coarser.fits": cube_coarser_pixels(),
        "vgt_result.fits": vgt_hdul(),
    }.items():
        path = directory / name
        hdul.writeto(path, overwrite=True)
        out[name] = path
    return out


if __name__ == "__main__":
    import sys
    target = Path(sys.argv[1] if len(sys.argv) > 1 else "fixtures/_out")
    for n, p in write_all(target).items():
        print(p)
