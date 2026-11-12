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


def cube_galactic_overlap() -> fits.HDUList:
    """The field of hi_cube on a Galactic frame, pixels 1.25x larger: the two grids overlap, so a cursor maps across frames
    (hi_cube_galactic.fits is elsewhere on the sky and does not overlap)."""
    from astropy import units as u
    from astropy.coordinates import SkyCoord
    hdul = hi_cube()
    h = hdul[0].header
    g = SkyCoord(CRVAL_RA * u.deg, CRVAL_DEC * u.deg, frame="icrs").galactic
    h["CTYPE1"], h["CTYPE2"] = "GLON-TAN", "GLAT-TAN"
    h["CRVAL1"], h["CRVAL2"] = float(g.l.deg), float(g.b.deg)
    h["CDELT1"], h["CDELT2"] = -1.25 * CDELT_DEG, 1.25 * CDELT_DEG
    del h["RADESYS"]
    return hdul


def cube_topocentric() -> fits.HDUList:
    """hi_cube_coarser with its spectral axis in the topocentric frame: a different grid from hi_cube whose channels must not be
    linked to hi_cube's (LSRK) by value, because a frame change is out of scope (ADR 0040)."""
    hdul = cube_coarser_pixels()
    hdul[0].header["SPECSYS"] = "TOPOCENT"
    return hdul


def cube_topocentric_same_grid() -> fits.HDUList:
    """hi_cube with only SPECSYS changed (TOPOCENT): the same grid on every axis, so the grid check passes, but channel i is not
    the same spectral value in both frames and must not be linked by index."""
    hdul = hi_cube()
    hdul[0].header["SPECSYS"] = "TOPOCENT"
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


VGT_DN, VGT_KER, VGT_INPUT_BEAM_ARCSEC = 10, 8.0, (60.0, 48.0, 30.0)   # window side and Q/U sigma in input pixels; input beam bmaj, bmin, bpa


def vgt_product_cards(product: str = "psi", psiframe: str | None = "PIXEL", beam_unit_comment: str | None = "arcsec") -> list:
    """The cards VGT.jl writes into a product header (src/pipeline/outputs.jl, vgt_product_header): VGTPROD and the run parameters
    on every product; PSIFRAME and PSIANGLE on psi only (psiframe None leaves them out, like an older output). The input beam
    cards INBMAJ/INBMIN/INBPA are as VGT issue 119 describes; `beam_unit_comment` is the unit the INBMAJ comment states (None: it
    states none), because the unit is not documented anywhere else."""
    cards = [("BUNIT", "rad" if product == "psi" else "", ""), ("VGTPROD", product, ""),
             ("VGTDN", VGT_DN, "VGT gradient window side, input pixels"), ("VGTKER", VGT_KER, "Q/U Gaussian smoothing sigma, input pixels"),
             ("VGTKFWHM", 2 * math.sqrt(2 * math.log(2)) * VGT_KER, "Q/U Gaussian smoothing FWHM, input pixels"),
             ("VGTMINV", 8, "min valid gradients per window"), ("VGTFIT", "axial_vonmises", "window angle estimator"),
             ("VGTGRAD", "sobel", "gradient mode"), ("VGTNMODE", "constant", "noise model"), ("VGTNOISE", 0.37, "noise threshold input (K)")]
    bmaj, bmin, bpa = VGT_INPUT_BEAM_ARCSEC
    cards += [("INBMAJ", bmaj, f"input beam major FWHM, {beam_unit_comment}" if beam_unit_comment else "input beam major FWHM"),
              ("INBMIN", bmin, "input beam minor FWHM"), ("INBPA", bpa, "input beam position angle, deg")]
    if product == "psi" and psiframe is not None:
        cards += [("PSIFRAME", psiframe, "CCW from +axis 1 in pixel grid; not east of N" if psiframe == "PIXEL" else "IAU position angle, east of north in this frame"),
                  ("PSIANGLE", "BFIELD", "gradient angle + 90 deg (B-field, not pol.)")]
    return cards


def vgt_psi_with_cards(unit: str, cards: list, header: fits.Header | None = None) -> fits.HDUList:
    """vgt_psi with extra header cards (name, value, comment), on `header`'s grid when given."""
    a = vgt_angle_map()["angle_deg"]
    data = a if unit == "deg" else np.radians(a)
    h = header if header is not None else _map_header(a.shape)
    for k, v, c in cards:
        h[k] = (v, c)
    return fits.HDUList([fits.PrimaryHDU(data.astype(np.float64), header=h)])


def vgt_psi_galactic_grid() -> fits.Header:
    """The psi grid in Galactic coordinates (GLON/GLAT), near the Galactic plane: a map whose grid frame equals PSIFRAME=GALACTIC."""
    h = fits.PrimaryHDU(np.zeros((4, 4), dtype=np.float32)).header
    for i, (ct, cv, cd, cp) in enumerate([("GLON-TAN", 280.0, -CDELT_DEG, 2.5), ("GLAT-TAN", -33.0, CDELT_DEG, 2.5)], start=1):
        h[f"CTYPE{i}"], h[f"CRVAL{i}"], h[f"CDELT{i}"], h[f"CRPIX{i}"], h[f"CUNIT{i}"] = ct, cv, cd, cp, "deg"
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


# run_metadata.toml as VGT writes it now: project name "VGT", and no finalize_mask_mode, histogram_edge_mode, gradient_sample_mode or
# noise exponents in [config] (the old layout, kept in write_vgt_run's default, has the project name "Astronomy")
CURRENT_RUN_METADATA = """generated_at_utc = "2026-11-10T09:21:44Z"
gitcommit = "@COMMIT@"

[config]
angle_frame = "galactic"
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
finished_at_utc = "2026-11-10T09:21:20Z"
julia_threads = 16

[project]
drwatson_projectdir = "/home/someone/Projects/VGT"
name = "VGT"

[run_parameters]
angle_frame = "galactic"
dn = 10
fitter = "axial_vonmises"
input = "demo_cube"
ker = 8
label = "vgt"
noise = 0.37
"""


def write_vgt_run(directory: Path, dirty: bool = True, metrics: bool = False, current: bool = False, psiframe: str | None = "GALACTIC") -> Path:
    """A VGT-style run folder: the four output maps and a run_metadata.toml shaped like the real pipeline's.

    The metadata keeps the pipeline's habit of recording output paths relative to the machine it ran on, an input
    path under /tmp and a "-dirty" commit, so importers can be tested against what real runs contain.
    """
    directory.mkdir(parents=True, exist_ok=True)
    if current:   # what VGT.jl writes now: the keywords in every product header, and a run_metadata.toml without the retired keys
        vgt_psi_with_cards("rad", vgt_product_cards("psi", psiframe)).writeto(directory / "psi_demo.fits", overwrite=True)
        rel = vgt_reliability_map()
        for k, v, c in vgt_product_cards("reliability", None):
            rel[0].header[k] = (v, c)
        rel.writeto(directory / "psi_reliability_demo.fits", overwrite=True)
        for name, fill, product in (("psi_support_demo.fits", 1.0, "support"), ("psi_fit_rbar2_demo.fits", 0.2, "fit_rbar2")):
            h = _map_header((4, 4))
            for k, v, c in vgt_product_cards(product, None):
                h[k] = (v, c)
            fits.HDUList([fits.PrimaryHDU(np.full((4, 4), fill, dtype=np.float32), header=h)]).writeto(directory / name, overwrite=True)
    else:
        vgt_psi("rad").writeto(directory / "psi_demo.fits", overwrite=True)
        vgt_reliability_map().writeto(directory / "psi_reliability_demo.fits", overwrite=True)
        support = fits.HDUList([fits.PrimaryHDU(np.ones((4, 4), dtype=np.float32), header=_map_header((4, 4)))])
        support.writeto(directory / "psi_support_demo.fits", overwrite=True)
        fit = fits.HDUList([fits.PrimaryHDU(np.full((4, 4), 0.2, dtype=np.float64), header=_map_header((4, 4)))])
        fit.writeto(directory / "psi_fit_rbar2_demo.fits", overwrite=True)
    commit = "bcbaed4ec9800ef95c561cfb4ad45c344743e469" + ("-dirty" if dirty else "")
    if current:
        (directory / "run_metadata.toml").write_text(CURRENT_RUN_METADATA.replace("@COMMIT@", commit))
        return directory / "run_metadata.toml"
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


# ---- a ROHSA decomposition: raw .dat, the crop it fitted, traces, and the pipeline's product maps ----
RX, RY, RC, RN = 6, 4, 40, 3                 # crop columns, rows, channels; Gaussians per pixel (not square: x and y differ)
R_CRVAL, R_CDELT, R_CRPIX = -12000.0, 800.0, 3.0   # VRAD m/s; reference pixel is not 1, so a wrong channel origin shows
R_REGION, R_LABEL = "region_a", "rohsa_n3"


def rohsa_truth() -> dict[str, np.ndarray]:
    """Per-pixel Gaussian parameters, (RY, RX, RN), in the file's own conventions: mean is 1-BASED channels."""
    y, x, k = np.meshgrid(np.arange(RY), np.arange(RX), np.arange(RN), indexing="ij")
    amp = 1.0 + 0.5 * k + 0.1 * x + 0.01 * y
    mean_file = 8.0 + 11.0 * k + 0.25 * x + 0.5 * y + 1.0
    sigma = 1.2 + 0.7 * k + 0.05 * x
    amp[0, 0, 2] = 0.0                       # a fitted amplitude of exactly zero is a value, not a gap
    return {"amp": amp, "mean_file": mean_file, "sigma": sigma}


def rohsa_velocity_ms(channel0) -> np.ndarray:
    """Independent of astropy: FITS linear axis, 0-based channel c is pixel c+1."""
    return R_CRVAL + (np.asarray(channel0, dtype=float) + 1.0 - R_CRPIX) * R_CDELT


def rohsa_model() -> np.ndarray:
    """(RC, RY, RX) sum of the components, evaluated directly from the 0-based means."""
    t = rohsa_truth()
    c = np.arange(RC)[:, None, None, None]
    m0 = t["mean_file"] - 1.0
    return (t["amp"][None] * np.exp(-((c - m0[None]) ** 2) / (2 * t["sigma"][None] ** 2))).sum(axis=3)


def _rohsa_header(shape_yx, with_spectral: bool) -> fits.Header:
    ny, nx = shape_yx
    h = fits.PrimaryHDU(np.zeros((RC, ny, nx) if with_spectral else (ny, nx), dtype=np.float32)).header
    ax = [("RA---TAN", CRVAL_RA, -CDELT_DEG, (nx + 1) / 2, "deg"), ("DEC--TAN", CRVAL_DEC, CDELT_DEG, (ny + 1) / 2, "deg")]
    if with_spectral:
        ax.append(("VRAD", R_CRVAL, R_CDELT, R_CRPIX, None))      # like the real crop: no CUNIT3 (m/s is the FITS default)
    for i, (ct, cv, cd, cp, cu) in enumerate(ax, start=1):
        h[f"CTYPE{i}"], h[f"CRVAL{i}"], h[f"CDELT{i}"], h[f"CRPIX{i}"] = ct, cv, cd, cp
        if cu:
            h[f"CUNIT{i}"] = cu
    h["RADESYS"] = "ICRS"
    return h


def write_rohsa_dat(path: Path, truth: dict | None = None, header_lines: int = 3) -> None:
    t = truth or rohsa_truth()
    lines = [" # ", " # ______Parameters_____", " # i, j, A, mean, sigma"][:header_lines]
    for y in range(RY):
        for x in range(RX):
            for k in range(RN):
                lines.append(f"{y:12d}{x:12d}{t['amp'][y, x, k]:26.17g}{t['mean_file'][y, x, k]:26.17g}{t['sigma'][y, x, k]:26.17g}")
    path.write_text("\n".join(lines) + "\n")


def write_rohsa_run(directory: Path, with_crop: bool = True) -> Path:
    """<directory>/<region>/rohsa_n3.dat beside its crop <region>.fits, namelist, two traces and the product tree.

    Product maps keep the real pipeline's habit of inheriting the crop's BUNIT (so a file can say K on a map whose
    name says km/s): _mu_kms has no BUNIT, _sigma_kms says K, _amp_k says K, model_nhi_cm2 has none.
    """
    directory.mkdir(parents=True, exist_ok=True)
    region_dir = directory / R_REGION
    region_dir.mkdir(exist_ok=True)
    truth = rohsa_truth()
    write_rohsa_dat(region_dir / f"{R_LABEL}.dat")
    if with_crop:
        rng = np.random.default_rng(20261110)
        cube = (rohsa_model() + rng.normal(0.0, 0.05, size=(RC, RY, RX))).astype(np.float32)
        cube[5, 1, 2] = np.nan                           # a channel with no value at one pixel
        h = _rohsa_header((RY, RX), True)
        h["BUNIT"] = "K"
        fits.PrimaryHDU(cube, header=h).writeto(region_dir / f"{R_REGION}.fits", overwrite=True)
    (region_dir / "parameters_n3.txt").write_text(f"""&user_parameters
    filename = '/shared/home/someone/run/{R_REGION}.bin'
    ,fileout = '/shared/home/someone/run/{R_LABEL}.dat'
    ,n_gauss = {RN}
    ,lambda_amp = 10.0d0
    ,lb_sig = 1.0d0
    ,init_option = 'mean'
    ,noise = .true.
    /
""")
    (region_dir / f"{R_LABEL}_run_trace.toml").write_text(f"""candidate_id = ""
n_gauss = {RN}
parameters = "validation_output/rohsa/x/{R_REGION}/parameters_n3.txt"
region = "{R_REGION}"
rohsa_output = "validation_output/rohsa/x/{R_REGION}/{R_LABEL}.dat"
stage = "rohsa_decomposition"
status = "ran"

[config]
config_path = "/shared/home/someone/vgt/configs/rohsa/x.toml"
lambda_amp = 10.0
lb_sig = 1.0
n_gauss = {RN}
init_option = "mean"
""")
    (region_dir / f"{R_LABEL}_products_trace.toml").write_text(f"""n_gauss = {RN}
region = "{R_REGION}"
run_label = "{R_LABEL}"
stage = "rohsa_product_derivation"

[fit_metrics]
total_model_over_data = 0.99
voxel_rmse = 0.05
""")
    maps = region_dir / "rohsa" / R_LABEL / "maps"
    maps.mkdir(parents=True, exist_ok=True)
    cubes = region_dir / "rohsa" / R_LABEL / "cubes"
    cubes.mkdir(parents=True, exist_ok=True)

    def put(name: str, arr: np.ndarray, bunit: str | None, folder: Path = maps) -> None:
        hh = _rohsa_header(arr.shape[-2:], arr.ndim == 3)
        if bunit is not None:
            hh["BUNIT"] = bunit
        fits.PrimaryHDU(arr.astype(np.float32), header=hh).writeto(folder / f"{R_LABEL}_{name}.fits", overwrite=True)

    v_kms = rohsa_velocity_ms(truth["mean_file"] - 1.0) / 1000.0
    put("component_g1_amp_k", truth["amp"][:, :, 0], "K")
    put("component_g1_mu_kms", v_kms[:, :, 0], None)
    put("component_g1_sigma_kms", truth["sigma"][:, :, 0] * R_CDELT / 1000.0, "K")
    put("model_nhi_cm2", rohsa_model().sum(axis=0), None)
    put("residual_moment0_kkms", np.zeros((RY, RX)), "K km/s")
    put("model", rohsa_model(), "K", cubes)
    return region_dir / f"{R_LABEL}.dat"


PLANCK_PSI_GAL_DEG = 30.0      # the dust polarization angle of the uniform HEALPix fixture, east of Galactic north (IAU)
PLANCK_P = 2.0e-4              # its polarized intensity, K_CMB
PLANCK_QQ, PLANCK_UU, PLANCK_QU = 4.0e-10, 9.0e-10, 1.0e-10   # its covariances (K_CMB^2), stated in the file's convention


def healpix_pol_map(nside: int = 16, ordering: str = "RING", polcconv: str | None = "COSMO", coordsys: str | None = "GALACTIC",
                    psi_gal=PLANCK_PSI_GAL_DEG, p=PLANCK_P, qq=PLANCK_QQ, uu=PLANCK_UU, qu=PLANCK_QU, missing=(), qu_cov: bool = True) -> fits.HDUList:
    """A Planck HFI SkyMap-like HEALPix map: an empty primary HDU, then a binary table of I, Q, U and the covariances, one row per
    pixel. `psi_gal` is the dust polarization angle (IAU, east of Galactic north at each pixel) as a number or a function of
    (l, b) in degrees; `p`, `qq`, `uu` and `qu` are numbers or functions of (l, b). Q and U are written in `polcconv`'s sign
    (COSMO: U = -U_IAU and QU = -QU_IAU). Pixels in `missing` hold the HEALPix bad-data value. Nothing here is real data."""
    from astropy.coordinates import Galactic
    from astropy_healpix import HEALPix
    hp = HEALPix(nside=nside, order=ordering.lower(), frame=Galactic())
    lon, lat = hp.healpix_to_lonlat(np.arange(hp.npix))
    l, b = lon.deg, lat.deg

    def val(v):
        return np.broadcast_to(np.asarray(v(l, b) if callable(v) else v, dtype=float), l.shape).copy()
    psi, pv = val(psi_gal), val(p)
    q_iau, u_iau = pv * np.cos(np.radians(2 * psi)), pv * np.sin(np.radians(2 * psi))
    sign = -1.0 if polcconv == "COSMO" else 1.0
    cols = {"I_STOKES": np.full(hp.npix, 1e-3), "Q_STOKES": q_iau, "U_STOKES": sign * u_iau, "QQ_COV": val(qq), "UU_COV": val(uu)}
    if qu_cov:
        cols["QU_COV"] = val(qu)
    for k in cols:
        cols[k][list(missing)] = -1.6375e30
    units = {"I_STOKES": "K_CMB", "Q_STOKES": "K_CMB", "U_STOKES": "K_CMB", "QQ_COV": "K_CMB^2", "UU_COV": "K_CMB^2", "QU_COV": "K_CMB^2"}
    t = fits.BinTableHDU.from_columns([fits.Column(name=k, format="E", unit=units[k], array=v.astype(np.float32)) for k, v in cols.items()],
                                      name="FREQ-MAP")
    h = t.header
    h["PIXTYPE"], h["ORDERING"], h["NSIDE"], h["INDXSCHM"] = "HEALPIX", ordering, nside, "IMPLICIT"
    h["FIRSTPIX"], h["LASTPIX"], h["BAD_DATA"] = 0, hp.npix - 1, -1.6375e30
    if coordsys is not None:
        h["COORDSYS"] = coordsys
    if polcconv is not None:
        h["POLCCONV"] = polcconv
    h["COMMENT"] = "Synthetic HEALPix polarization fixture, not a measurement."
    return fits.HDUList([fits.PrimaryHDU(), t])


def vgt_psi_planck_field() -> fits.HDUList:
    """A VGT psi product on the 4x4 ICRS grid whose B-field angle is the uniform Planck fixture's field: dust polarization + 90
    degrees east of Galactic north, stated as PSIFRAME = GALACTIC (radians). A relative orientation against the Planck field is
    aligned (AM near 1) only if both sides carry the angle through the sky correctly."""
    a = np.full((4, 4), math.radians((PLANCK_PSI_GAL_DEG + 90.0) % 180.0))
    h = _map_header(a.shape)
    for k, v, c in vgt_product_cards("psi", "GALACTIC"):
        h[k] = (v, c)
    return fits.HDUList([fits.PrimaryHDU(a, header=h)])


def write_all(directory: Path) -> dict[str, Path]:
    directory.mkdir(parents=True, exist_ok=True)
    out = {}
    for name, hdul in {
        "hi_cube.fits": hi_cube(),
        "hi_cube_no_spectral_ref.fits": cube_missing_spectral_reference(),
        "hi_cube_no_celestial_wcs.fits": cube_unlabelled_pixels(),
        "hi_cube_galactic.fits": cube_other_frame(),
        "hi_cube_coarser.fits": cube_coarser_pixels(),
        "hi_cube_galactic_overlap.fits": cube_galactic_overlap(),
        "hi_cube_topocentric.fits": cube_topocentric(),
        "hi_cube_topocentric_same_grid.fits": cube_topocentric_same_grid(),
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
        # VGT.jl products, one per PSIFRAME case (all on the ICRS grid but the last): the angle's frame travels in the header
        "vgt_psi_pixel_frame.fits": vgt_psi_with_cards("rad", vgt_product_cards("psi", "PIXEL")),
        "vgt_psi_galactic_frame.fits": vgt_psi_with_cards("rad", vgt_product_cards("psi", "GALACTIC")),
        "vgt_psi_icrs_frame.fits": vgt_psi_with_cards("rad", vgt_product_cards("psi", "ICRS")),
        "vgt_psi_no_frame.fits": vgt_psi_with_cards("rad", vgt_product_cards("psi", None)),
        "vgt_psi_unknown_frame.fits": vgt_psi_with_cards("rad", vgt_product_cards("psi", "ECLIPTIC")),
        "vgt_psi_galactic_on_galactic_grid.fits": vgt_psi_with_cards("rad", vgt_product_cards("psi", "GALACTIC"), vgt_psi_galactic_grid()),
        "vgt_psi_beam_unit_unstated.fits": vgt_psi_with_cards("rad", vgt_product_cards("psi", "GALACTIC", None)),
        "vgt_tangential_deg.fits": vgt_tangential_deg(),
        # HEALPix polarization maps (ADR 0067): a uniform Galactic field in the COSMO convention, and two that cannot be used
        "planck_353_cosmo.fits": healpix_pol_map(),
        "planck_353_no_polcconv.fits": healpix_pol_map(polcconv=None),
        "planck_353_no_coordsys.fits": healpix_pol_map(coordsys=None),
        "vgt_psi_planck_field.fits": vgt_psi_planck_field(),
    }.items():
        path = directory / name
        hdul.writeto(path, overwrite=True)
        out[name] = path
    write_vgt_run(directory / "vgt_run_demo", metrics=True)
    write_vgt_run(directory / "vgt_run_current", dirty=False, current=True)
    write_rohsa_run(directory / "rohsa_run_demo")
    return out


if __name__ == "__main__":
    import sys
    target = Path(sys.argv[1] if len(sys.argv) > 1 else "fixtures/_out")
    for n, p in write_all(target).items():
        print(p)
