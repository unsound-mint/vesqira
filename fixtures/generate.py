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
    h["BMAJ"], h["BMIN"], h["BPA"] = 0.03, 0.02, 45.0   # a beam larger than the 0.01 deg pixel: pixel scale and resolution differ
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


def cube_reverse_spectral() -> fits.HDUList:
    """Velocity decreases with channel number (CDELT3 < 0), as in many real cubes: velocity must follow the header, not the index."""
    hdul = hi_cube()
    h = hdul[0].header
    h["CRVAL3"], h["CDELT3"] = V0_MS + (NC - 1) * DV_MS, -DV_MS     # channel 1 is now the highest velocity
    return hdul


ROT_DEG = 30.0


def cube_rotated_wcs() -> fits.HDUList:
    """Celestial axes rotated by 30 degrees through a PC matrix: a coordinate readout that ignores PC is wrong here."""
    hdul = hi_cube()
    h = hdul[0].header
    c, s_ = math.cos(math.radians(ROT_DEG)), math.sin(math.radians(ROT_DEG))
    h["PC1_1"], h["PC1_2"], h["PC2_1"], h["PC2_2"] = c, -s_, s_, c
    return hdul


def cube_unlabelled_pixels() -> fits.HDUList:
    """Cube with no celestial WCS at all: positions must fall back to pixels, never invented degrees."""
    hdul = hi_cube()
    for i in (1, 2):
        for k in ("CTYPE", "CRVAL", "CDELT", "CRPIX", "CUNIT"):
            del hdul[0].header[f"{k}{i}"]
    del hdul[0].header["RADESYS"]
    return hdul


FREQ0_HZ, DFREQ_HZ = 1.4204e9, 5000.0   # FREQ axis of cube_stokes_freq (channel 1 = FREQ0)
SCALED_SHAPE = (4, 6, 8)                  # numpy (channels, y, x) of cube_int16_scaled
SCALED_BSCALE, SCALED_BZERO, SCALED_BLANK = 0.01, 5.0, -32768
SCALED_BLANK_PIX = (1, 1, 2)              # (x, y, channel) holds the BLANK sentinel
SCALED_ZERO_PIX = (4, 3, 2)               # (x, y, channel) is a real physical 0.0 (raw -500)


def scaled_raw() -> np.ndarray:
    c, y, x = np.mgrid[0:SCALED_SHAPE[0], 0:SCALED_SHAPE[1], 0:SCALED_SHAPE[2]]
    raw = ((x + 3 * y + 7 * c) % 2000 - 1000).astype(np.int16)
    raw[SCALED_ZERO_PIX[2], SCALED_ZERO_PIX[1], SCALED_ZERO_PIX[0]] = -500
    raw[SCALED_BLANK_PIX[2], SCALED_BLANK_PIX[1], SCALED_BLANK_PIX[0]] = SCALED_BLANK
    return raw


def cube_int16_scaled() -> fits.HDUList:
    """16-bit integer cube with BSCALE/BZERO/BLANK: physical = raw*BSCALE+BZERO, BLANK is missing (never 0)."""
    hdu = fits.PrimaryHDU(scaled_raw())
    h = hdu.header
    h["BSCALE"], h["BZERO"], h["BLANK"] = SCALED_BSCALE, SCALED_BZERO, SCALED_BLANK
    h["BUNIT"] = "Jy/beam"
    for i, (ct, cv, cd, cp, cu) in enumerate([
        ("RA---TAN", CRVAL_RA, -CDELT_DEG, 4.0, "deg"),
        ("DEC--TAN", CRVAL_DEC, CDELT_DEG, 3.0, "deg"),
        ("VRAD", V0_MS, DV_MS, 1.0, "m/s"),
    ], start=1):
        h[f"CTYPE{i}"], h[f"CRVAL{i}"], h[f"CDELT{i}"], h[f"CRPIX{i}"], h[f"CUNIT{i}"] = ct, cv, cd, cp, cu
    h["RADESYS"] = "ICRS"
    return fits.HDUList([hdu])


FREQ0_HZ, DFREQ_HZ = 1.4200e9, 1.0e5   # channel 1 is exactly 1420.0 MHz; 0.1 MHz per channel
REST_HI_HZ = 1.420405751768e9
GC_ICRS_DEG = (266.405, -28.936)       # the Galactic centre, to within a few arcseconds (l = 0, b = 0)


def cube_frequency_axis() -> fits.HDUList:
    """Small Jy/beam cube on a FREQ axis in Hz with a 60 x 60 arcsec beam, FK5 J2000 sky and a rest frequency.
    Unit and frame conversion fixture: every conversion answer is known in closed form."""
    nc = 8
    data = np.linspace(0.0, 1.0, nc * NY * NX, dtype=np.float32).reshape(nc, NY, NX)
    h = fits.PrimaryHDU(data).header
    h["BUNIT"] = "Jy/beam"
    h["BMAJ"] = h["BMIN"] = 60.0 / 3600.0
    h["BPA"] = 0.0
    for i, (ct, cv, cd, cp, cu) in enumerate([
        ("RA---TAN", CRVAL_RA, -CDELT_DEG, CRPIX[0], "deg"),
        ("DEC--TAN", CRVAL_DEC, CDELT_DEG, CRPIX[1], "deg"),
        ("FREQ", FREQ0_HZ, DFREQ_HZ, 1.0, "Hz"),
    ], start=1):
        h[f"CTYPE{i}"], h[f"CRVAL{i}"], h[f"CDELT{i}"], h[f"CRPIX{i}"], h[f"CUNIT{i}"] = ct, cv, cd, cp, cu
    h["RADESYS"], h["EQUINOX"] = "FK5", 2000.0
    h["SPECSYS"] = "LSRK"
    h["RESTFRQ"] = REST_HI_HZ
    return fits.HDUList([fits.PrimaryHDU(data, header=h)])


def cube_galactic_centre() -> fits.HDUList:
    """2D ICRS image whose reference pixel is the Galactic centre (266.405, -28.936 deg): l and b must come out near 0."""
    h = fits.PrimaryHDU(np.zeros((NY, NX), dtype=np.float32)).header
    for i, (ct, cv, cd, cp) in enumerate([("RA---TAN", GC_ICRS_DEG[0], -CDELT_DEG, CRPIX[0]), ("DEC--TAN", GC_ICRS_DEG[1], CDELT_DEG, CRPIX[1])], start=1):
        h[f"CTYPE{i}"], h[f"CRVAL{i}"], h[f"CDELT{i}"], h[f"CRPIX{i}"], h[f"CUNIT{i}"] = ct, cv, cd, cp, "deg"
    h["RADESYS"] = "ICRS"
    h["BUNIT"] = "K"
    return fits.HDUList([fits.PrimaryHDU(np.zeros((NY, NX), dtype=np.float32), header=h)])


def cube_stokes_freq() -> fits.HDUList:
    """4D cube, FITS axes RA, DEC, STOKES (length 1), FREQ: the spectral axis is axis 4, not 3.

    Same numbers as hi_cube, so a plane must equal the hi_cube plane. FREQ has no velocity convention
    declared, so velocity must stay unresolved.
    """
    src = hi_cube()
    data = src[0].data[:, None, :, :]      # numpy (FREQ, STOKES, DEC, RA)
    h = fits.PrimaryHDU(data).header
    h["BUNIT"] = "K"
    for i, (ct, cv, cd, cp, cu) in enumerate([
        ("RA---TAN", CRVAL_RA, -CDELT_DEG, CRPIX[0], "deg"),
        ("DEC--TAN", CRVAL_DEC, CDELT_DEG, CRPIX[1], "deg"),
        ("STOKES", 1.0, 1.0, 1.0, ""),
        ("FREQ", FREQ0_HZ, DFREQ_HZ, 1.0, "Hz"),
    ], start=1):
        h[f"CTYPE{i}"], h[f"CRVAL{i}"], h[f"CDELT{i}"], h[f"CRPIX{i}"] = ct, cv, cd, cp
        if cu:
            h[f"CUNIT{i}"] = cu
    h["RADESYS"] = "ICRS"
    h["SPECSYS"] = "LSRK"
    h["RESTFRQ"] = 1.420405751768e9
    return fits.HDUList([fits.PrimaryHDU(data, header=h)])


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


def _map_header(shape_yx: tuple[int, int]) -> fits.Header:
    h = fits.PrimaryHDU(np.zeros(shape_yx, dtype=np.float32)).header
    ny, nx = shape_yx
    for i, (ct, cv, cd, cp) in enumerate([("RA---TAN", CRVAL_RA, -CDELT_DEG, (nx + 1) / 2), ("DEC--TAN", CRVAL_DEC, CDELT_DEG, (ny + 1) / 2)], start=1):
        h[f"CTYPE{i}"], h[f"CRVAL{i}"], h[f"CDELT{i}"], h[f"CRPIX{i}"], h[f"CUNIT{i}"] = ct, cv, cd, cp, "deg"
    h["RADESYS"] = "ICRS"
    return h


def vgt_psi(unit: str) -> fits.HDUList:
    """VGT-style orientation product in a primary HDU, like the real pipeline's psi files (one product per file).

    Axial angles in [0, 180) degrees or [0, pi) radians. The file declares neither the unit nor that it is axial:
    that is an interpretation the user declares.
    """
    a = vgt_angle_map()["angle_deg"]
    data = a if unit == "deg" else np.radians(a)
    return fits.HDUList([fits.PrimaryHDU(data.astype(np.float64), header=_map_header(a.shape))])


def vgt_reliability_map() -> fits.HDUList:
    """Companion map on the same grid. Its range is whatever the pipeline produced, not assumed to be 0 to 1."""
    r = vgt_angle_map()["reliability"] * 0.5   # peaks at 0.5 on purpose
    return fits.HDUList([fits.PrimaryHDU(r.astype(np.float32), header=_map_header(r.shape))])


TANGENTIAL_N = 33


def vgt_tangential_deg() -> fits.HDUList:
    """Geometric oracle: axial orientation of a field circling the centre pixel.

    Angle is measured from the +x pixel axis towards +y (y = FITS row index, increasing upwards). A tangential field
    is perpendicular to the radius, so right of the centre it is 90 deg, above it 0 deg, on the up-right diagonal
    135 deg. A mirrored convention (theta -> 180 - theta) swaps 45 and 135 and is therefore detectable.
    """
    n = TANGENTIAL_N
    yy, xx = np.mgrid[0:n, 0:n]
    c = n // 2
    theta = (np.degrees(np.arctan2(yy - c, xx - c)) + 90.0) % 180.0
    theta[c, c] = np.nan                                  # no direction at the centre itself
    return fits.HDUList([fits.PrimaryHDU(theta.astype(np.float64), header=_map_header((n, n)))])


# An optional section that a pipeline (or the researcher) may add: the measured results of the run and what they mean.
METRICS_BLOCK = """
[metrics.alignment_measure]
value = 0.344
unit = ""
uncertainty = 0.02
uncertainty_kind = "standard_error"
n = 2695
definition = "Axial agreement with the reference field, within the reliable-pixel mask (synthetic demo value)"

[metrics.fit_failure_fraction]
value = 0.0
definition = "Fraction of fit attempts that failed (a real zero, not a missing value)"
"""


def write_vgt_run(directory: Path, dirty: bool = True, metrics: bool = False) -> Path:
    """A VGT-style run folder: the four output maps and a run_metadata.toml shaped like the real pipeline's.

    The metadata keeps the pipeline's habit of recording output paths relative to the machine it ran on, an input
    path under /tmp and a "-dirty" commit, so importers can be tested against what real runs contain.
    """
    directory.mkdir(parents=True, exist_ok=True)
    psi = vgt_psi("rad")
    psi.writeto(directory / "psi_demo.fits", overwrite=True)
    vgt_reliability_map().writeto(directory / "psi_reliability_demo.fits", overwrite=True)
    support = fits.HDUList([fits.PrimaryHDU(np.ones((4, 4), dtype=np.float32), header=_map_header((4, 4)))])
    support.writeto(directory / "psi_support_demo.fits", overwrite=True)
    fit = fits.HDUList([fits.PrimaryHDU(np.full((4, 4), 0.2, dtype=np.float64), header=_map_header((4, 4)))])
    fit.writeto(directory / "psi_fit_rbar2_demo.fits", overwrite=True)
    commit = "bcbaed4ec9800ef95c561cfb4ad45c344743e469" + ("-dirty" if dirty else "")
    (directory / "run_metadata.toml").write_text(f"""generated_at_utc = "2026-06-25T09:21:44Z"
gitcommit = "{commit}"

[config]
dn = 10
fitter_mode = "axial_vonmises"
input_file = "/tmp/vgt_inputs/demo_cube.fits"
ker = 8
noise = 0.37
output_dir = "validation_output/demo_run"

[diagnostics]
fit_attempts = 1000
fit_failures = 0

[input]
absolute_path = "/tmp/vgt_inputs/demo_cube.fits"
path = "/tmp/vgt_inputs/demo_cube.fits"
size_bytes = 207360
stem = "demo_cube"

[outputs]
fit_rbar2_path = "validation_output/demo_run/psi_fit_rbar2_demo.fits"
metadata_path = "validation_output/demo_run/run_metadata.toml"
psi_path = "validation_output/demo_run/psi_demo.fits"
reliability_path = "validation_output/demo_run/psi_reliability_demo.fits"
support_path = "validation_output/demo_run/psi_support_demo.fits"

[performance]
elapsed_seconds = 106.162
finished_at_utc = "2026-06-25T09:21:20Z"
julia_threads = 16

[project]
drwatson_projectdir = "/home/someone/Projects/VGT"
name = "Astronomy"

[run_parameters]
dn = 10
fitter = "axial_vonmises"
input = "demo_cube"
ker = 8
label = "vgt"
noise = 0.37
""" + (METRICS_BLOCK if metrics else ""))
    return directory / "run_metadata.toml"


def write_all(directory: Path) -> dict[str, Path]:
    directory.mkdir(parents=True, exist_ok=True)
    out = {}
    for name, hdul in {
        "hi_cube.fits": hi_cube(),
        "hi_cube_no_spectral_ref.fits": cube_missing_spectral_reference(),
        "hi_cube_no_celestial_wcs.fits": cube_unlabelled_pixels(),
        "hi_cube_galactic.fits": cube_other_frame(),
        "hi_cube_coarser.fits": cube_coarser_pixels(),
        "hi_cube_reverse_spectral.fits": cube_reverse_spectral(),
        "hi_cube_rotated.fits": cube_rotated_wcs(),
        "cube_int16_scaled.fits": cube_int16_scaled(),
        "cube_stokes_freq.fits": cube_stokes_freq(),
        "hi_cube_freq.fits": cube_frequency_axis(),
        "galactic_centre.fits": cube_galactic_centre(),
        "vgt_result.fits": vgt_hdul(),
        "vgt_psi_deg.fits": vgt_psi("deg"),
        "vgt_psi_rad.fits": vgt_psi("rad"),
        "vgt_reliability.fits": vgt_reliability_map(),
        "vgt_tangential_deg.fits": vgt_tangential_deg(),
    }.items():
        path = directory / name
        hdul.writeto(path, overwrite=True)
        out[name] = path
    write_vgt_run(directory / "vgt_run_demo", metrics=True)
    return out


if __name__ == "__main__":
    import sys
    target = Path(sys.argv[1] if len(sys.argv) > 1 else "fixtures/_out")
    for n, p in write_all(target).items():
        print(p)
