"""HEALPix polarization maps (a Planck HFI SkyMap) and their sampling onto a celestial grid as Stokes Q and U (ADR 0067).

What this module keeps:
- A HEALPix map is a binary table after an empty primary HDU, with PIXTYPE = 'HEALPIX'. It is read only when its header states
  ORDERING, NSIDE, COORDSYS and POLCCONV; a map that leaves out the convention or the coordinate system is refused with a sentence.
  Nothing pretends it is an image with a WCS.
- The pixelisation is astropy-healpix's (lon/lat to pixel, bilinear weights, cone search, pixel centres). None of it is written here.
- Q and U are sampled, never angles. In order:
  1. read Q, U and the covariances QQ, UU (and QU when the file has it) of the HEALPix pixels the kernel needs (memory-mapped,
     only those pixels); a missing value (NaN, the HEALPix bad-data sentinel, a negative variance) leaves the pixel out;
  2. the COSMO convention is turned into IAU: U_IAU = -U_COSMO, and so QU_IAU = -QU_COSMO (QQ and UU are unchanged);
  3. the kernel's weights (one pixel, the four bilinear weights, or a Gaussian over the pixel centres within 3 sigma) average Q
     and U as the pixels state them, against Galactic north (the local meridian, as healpy and reproject interpolate Q and U), and
     the covariances with the squared weights, over the pixels that have a value. Galactic north turns across a kernel of radius r
     by about r tan|b|; in a symmetric kernel the first order cancels in the mean angle and what remains is a slight
     depolarization, second order in that turn. At Planck's 1.7 arcmin pixels it is far below the noise; a Gaussian kernel whose
     turn at its 3 sigma edge would exceed 15 degrees (large kernels, or near a Galactic pole) is refused rather than corrected;
  4. the reference direction turns from Galactic north to the north of the target grid's own celestial frame at the target
     position, by the angle between the two norths there (astropy, through orientation.rotate_pa_frame); Q, U and the covariance
     turn by twice that angle. Every pixel of one target turns by the same angle, so turning after the average is the same as
     turning each pixel first;
  5. the angle is psi = 1/2 atan2(U, Q), east of north in the grid's own celestial frame, axial in [0, 180). The dust
     polarization is psi; the field orientation is psi + 90 degrees.
- Noise: P = sqrt(Q^2 + U^2) is not debiased; sigma_P^2 = (Q^2 QQ + U^2 UU + 2 Q U QU) / P^2 after the kernel. HEALPix pixels are
  taken as independent: the noise correlation between pixels in the original map (beam, scanning) is not modelled, so sigma_P
  understates the uncertainty where neighbouring pixels are positively correlated, most of all after smoothing.
"""
from __future__ import annotations

import math
import re

import numpy as np

import orientation as ori

MAX_NSIDE = 8192
POLCCONVS = ("COSMO", "IAU")
# What COORDSYS may say (the HEALPix and Planck conventions), and the frame it names. Only Galactic maps are sampled in this version:
# "celestial" does not say which equatorial frame (FK5 J2000 or ICRS), and ecliptic needs an equinox.
COORDSYS = {"G": "galactic", "GALACTIC": "galactic", "C": "celestial", "Q": "celestial", "CELESTIAL": "celestial", "EQUATORIAL": "celestial",
            "E": "ecliptic", "ECLIPTIC": "ecliptic"}
ORDERINGS = ("RING", "NESTED")
REQUIRED = ("Q_STOKES", "U_STOKES", "QQ_COV", "UU_COV")
OPTIONAL = ("I_STOKES", "QU_COV")
UNSEEN = -1.6375e30                 # the HEALPix bad-data value (healpy's UNSEEN, Planck's BAD_DATA)
METHODS = ("nearest", "bilinear", "gaussian")
PRODUCTS = {"dust_polarization": 0.0, "b_field": 90.0}   # degrees added to psi = 1/2 atan2(U, Q)
GAUSS_TRUNCATE = 3.0                # the Gaussian kernel takes pixel centres within this many sigma
MAX_PAIRS = 20_000_000              # target pixels x kernel pixels: bounds memory (a few hundred MB) and time
MAX_FWHM_ARCMIN = 600.0
MAX_KERNEL_TURN_DEG = 15.0         # Galactic north may turn at most this much at the edge (3 sigma) of a Gaussian kernel: r tan|b|
FWHM_PER_SIGMA = 2.0 * math.sqrt(2.0 * math.log(2.0))
SAFE = re.compile(r"[^\x20-\x7e]")


class HealpixRefused(ValueError):
    """A HEALPix map that cannot be used as it is, or a request that cannot be met. The message is a sentence for the person."""


def _text(v, limit=40):
    return SAFE.sub("?", v.strip())[:limit] if isinstance(v, str) else None


def is_healpix(h) -> bool:
    """The header of a binary table that says it holds a HEALPix map."""
    return _text(h.get("XTENSION")) == "BINTABLE" and (_text(h.get("PIXTYPE")) or "").upper() == "HEALPIX"


def _columns(h) -> dict:
    """{NAME: {"n": column number, "repeat": values per row, "code": E or D, "unit": TUNIT text or None}} of the table's columns."""
    try:
        n = int(h.get("TFIELDS", 0))
    except (TypeError, ValueError):
        n = 0
    if not 0 < n <= 999:
        raise HealpixRefused("This HEALPix table does not say how many columns it has (TFIELDS); it was not registered.")
    out = {}
    for i in range(1, n + 1):
        name = (_text(h.get(f"TTYPE{i}")) or "").upper()
        m = re.fullmatch(r"\s*(\d*)\s*([A-Z])\s*(\(.*\))?\s*", h.get(f"TFORM{i}") if isinstance(h.get(f"TFORM{i}"), str) else "")
        if not name or m is None:
            continue
        out[name] = {"n": i, "repeat": int(m.group(1) or 1), "code": m.group(2), "unit": _text(h.get(f"TUNIT{i}")) or None,
                     "scaled": h.get(f"TSCAL{i}", 1) != 1 or h.get(f"TZERO{i}", 0) != 0}
    return out


def describe_header(h) -> dict:
    """What a HEALPix map's table header declares, as the dataset's header; HealpixRefused (a sentence) when it does not state what is
    needed to use it. Reads no data."""
    conv = _text(h.get("POLCCONV"))
    if not conv:
        raise HealpixRefused("This HEALPix map does not state its polarization convention (POLCCONV, COSMO or IAU), so the sign of U "
                             "is unknown; it was not registered.")
    if conv.upper() not in POLCCONVS:
        raise HealpixRefused(f"This HEALPix map states POLCCONV = {conv!r}, which is neither COSMO nor IAU, so the sign of U is unknown; "
                             "it was not registered.")
    coord = _text(h.get("COORDSYS"))
    if not coord:
        raise HealpixRefused("This HEALPix map does not state its coordinate system (COORDSYS), so its pixels cannot be placed on the "
                             "sky; it was not registered.")
    if coord.upper() not in COORDSYS:
        raise HealpixRefused(f"This HEALPix map states COORDSYS = {coord!r}, which is not a coordinate system Vesqira knows (Galactic, "
                             "celestial or ecliptic); it was not registered.")
    order = (_text(h.get("ORDERING")) or "").upper()
    if order not in ORDERINGS:
        raise HealpixRefused("This HEALPix map does not state its pixel ordering (ORDERING, RING or NESTED); it was not registered.")
    nside = h.get("NSIDE")
    if isinstance(nside, bool) or not isinstance(nside, int) or not 1 <= nside <= MAX_NSIDE or nside & (nside - 1):
        raise HealpixRefused(f"This HEALPix map's NSIDE must be a power of two up to {MAX_NSIDE}; it was not registered.")
    scheme = (_text(h.get("INDXSCHM")) or "IMPLICIT").upper()
    if scheme != "IMPLICIT" or (_text(h.get("OBJECT")) or "").upper() == "PARTIAL":
        raise HealpixRefused("This HEALPix map covers part of the sky with an explicit pixel index, which Vesqira does not read yet; "
                             "it was not registered.")
    cols = _columns(h)
    missing = [c for c in REQUIRED if c not in cols]
    if missing:
        raise HealpixRefused("This HEALPix table has no " + ", ".join(missing) + " column; sampling it needs Q_STOKES, U_STOKES, QQ_COV "
                             "and UU_COV. It was not registered.")
    used = [c for c in REQUIRED + OPTIONAL if c in cols]
    npix = 12 * nside * nside
    rows = h.get("NAXIS2")
    reps = {cols[c]["repeat"] for c in used}
    if any(cols[c]["code"] not in ("E", "D") for c in used) or any(cols[c]["scaled"] for c in used):
        raise HealpixRefused("This HEALPix table stores Stokes or covariance values in a form other than unscaled floating point; it "
                             "was not registered.")
    if not isinstance(rows, int) or isinstance(rows, bool) or len(reps) != 1 or rows * next(iter(reps)) != npix:
        raise HealpixRefused(f"This HEALPix table does not hold one value per pixel of NSIDE {nside} ({npix} pixels) in every column it "
                             "needs; it was not registered.")
    bad = h.get("BAD_DATA")
    return {
        "ok": True, "kind": "healpix", "nside": nside, "npix": npix, "ordering": order,
        "coordsys": coord, "frame": COORDSYS[coord.upper()], "polcconv": conv.upper(),
        "columns": [{"name": c, "unit": cols[c]["unit"]} for c in used],
        "stokes_unit": cols["Q_STOKES"]["unit"], "covariance_unit": cols["QQ_COV"]["unit"],
        "has_qu_cov": "QU_COV" in cols, "has_i": "I_STOKES" in cols,
        "bad_data": bad if isinstance(bad, (int, float)) and not isinstance(bad, bool) and math.isfinite(bad) else None,
        # the square root of the pixel area (every HEALPix pixel has area 4 pi / npix), as astropy-healpix defines the resolution; no
        # library is needed to describe a map, so a computer without astropy-healpix still registers one and says why it cannot sample it
        "pixel_resolution_arcmin": math.degrees(math.sqrt(4.0 * math.pi / npix)) * 60.0,
        "extname": _text(h.get("EXTNAME")), "telescope": _text(h.get("TELESCOP")), "frequency": _text(h.get("FREQ")),
        "provenance_note": "values are declared by the file header; nothing here is verified",
    }


# ---------------------------------------------------------------------------------------------------------------------- sampling

def check_params(p: dict) -> dict:
    """The sampling parameters, validated, with their defaults (method nearest, no smoothing, no P/sigma_P cut)."""
    product = p.get("product")
    if product not in PRODUCTS:
        raise HealpixRefused("say which product to derive: the dust polarization angle (dust_polarization) or the field orientation (b_field)")
    method = p.get("method") or "nearest"
    if method not in METHODS:
        raise HealpixRefused("the sampling method must be nearest, bilinear or gaussian")
    fwhm = p.get("fwhm_arcmin")
    if method == "gaussian":
        if isinstance(fwhm, bool) or not isinstance(fwhm, (int, float)) or not math.isfinite(fwhm) or not 0 < fwhm <= MAX_FWHM_ARCMIN:
            raise HealpixRefused(f"Gaussian smoothing needs a FWHM in arcminutes, above 0 and at most {MAX_FWHM_ARCMIN:g}")
    elif fwhm is not None:
        raise HealpixRefused("a smoothing FWHM applies to the gaussian method only")
    snr = p.get("snr_min")
    if snr is not None and (isinstance(snr, bool) or not isinstance(snr, (int, float)) or not math.isfinite(snr) or not 0 <= snr <= 1e6):
        raise HealpixRefused("the P/sigma_P threshold must be a number between 0 and 1000000")
    out = {"product": product, "method": method}
    if method == "gaussian":
        out["fwhm_arcmin"] = float(fwhm)
    if snr is not None:
        out["snr_min"] = float(snr)
    return out


def _healpix(facts):
    from astropy.coordinates import Galactic
    from astropy_healpix import HEALPix
    return HEALPix(nside=facts["nside"], order=facts["ordering"].lower(), frame=Galactic())


def kernel(hp, lon_deg, lat_deg, method: str, fwhm_arcmin: float | None = None):
    """(target index, HEALPix pixel, weight) for every pair the kernel uses, as flat arrays. lon, lat are Galactic degrees of the
    target positions (all finite). The weights of one target sum to 1 before missing pixels are left out.

    nearest: the pixel holding the position. bilinear: astropy-healpix's four bilinear weights. gaussian: every pixel whose centre is
    within 3 sigma of the position, weighted exp(-d^2 / 2 sigma^2) by the angular distance d (astropy)."""
    import astropy.units as u
    from astropy.coordinates import angular_separation
    n = lon_deg.size
    lon, lat = lon_deg * u.deg, lat_deg * u.deg
    if method == "nearest":
        return np.arange(n), np.asarray(hp.lonlat_to_healpix(lon, lat), dtype=np.int64), np.ones(n)
    if method == "bilinear":
        idx, w = hp.bilinear_interpolation_weights(lon, lat)            # each (4, n)
        return np.tile(np.arange(n), 4), np.asarray(idx, dtype=np.int64).ravel(), np.asarray(w, dtype=float).ravel()
    sigma = math.radians(fwhm_arcmin / 60.0) / FWHM_PER_SIGMA
    radius = GAUSS_TRUNCATE * sigma
    per = max(1.0, math.pi * radius ** 2 / hp.pixel_area.to_value(u.sr))
    if n * per > MAX_PAIRS:
        raise HealpixRefused(f"smoothing {n} target pixels with a {fwhm_arcmin:g} arcmin kernel would combine about {int(n * per)} pixel "
                             f"pairs, more than the {MAX_PAIRS} this computes; choose a smaller kernel or a coarser target grid")
    tg, px, wt = [], [], []
    rad_q = radius * u.rad
    lon_r, lat_r = np.radians(lon_deg), np.radians(lat_deg)
    for i in range(n):
        cand = np.asarray(hp.cone_search_lonlat(lon[i], lat[i], rad_q), dtype=np.int64)  # every pixel the cone touches
        clon, clat = hp.healpix_to_lonlat(cand)
        d = np.asarray(angular_separation(lon_r[i], lat_r[i], clon.to_value(u.rad), clat.to_value(u.rad)), dtype=float)
        keep = d <= radius                                                           # membership by the pixel centre
        if not keep.any():
            continue
        k = cand[keep]
        wk = np.exp(-0.5 * (d[keep] / sigma) ** 2)
        tg.append(np.full(k.size, i))
        px.append(k)
        wt.append(wk / wk.sum())
    if not tg:
        return np.zeros(0, dtype=np.int64), np.zeros(0, dtype=np.int64), np.zeros(0)
    return np.concatenate(tg), np.concatenate(px), np.concatenate(wt)


def read_pixels(path: str, facts: dict, pix: np.ndarray) -> dict:
    """{column: float64 values at the HEALPix pixels `pix`} for the columns sampling needs, NaN for a missing value. The table is
    memory-mapped and only the rows holding those pixels are read."""
    from srcopen import fits_open
    names = ["Q_STOKES", "U_STOKES", "QQ_COV", "UU_COV"] + (["QU_COV"] if facts["has_qu_cov"] else [])
    sentinels = [UNSEEN] + ([facts["bad_data"]] if facts.get("bad_data") is not None else [])
    out = {}
    with fits_open(path, memmap=True) as hdul:
        hdu = hdul[1]
        again = describe_header(hdu.header)          # the file read now, not the one registered: it must still say the same
        for k in ("nside", "ordering", "frame", "polcconv", "has_qu_cov"):
            if again[k] != facts[k]:
                raise HealpixRefused("the HEALPix file changed since it was described; register it again")
        rows = hdu.header["NAXIS2"]
        rep = facts["npix"] // rows
        for name in names:
            col = hdu.data.field(name)
            v = np.asarray(col[pix] if rep == 1 else col[pix // rep, pix % rep], dtype=float)
            bad = ~np.isfinite(v)
            for s in sentinels:
                bad |= np.isclose(v, s, rtol=1e-6, atol=0.0)
            out[name] = np.where(bad, np.nan, v)
    if "QU_COV" not in out:
        out["QU_COV"] = np.zeros(pix.size)
    for name in ("QQ_COV", "UU_COV"):
        out[name] = np.where(out[name] < 0, np.nan, out[name])     # a negative variance is not a variance
    return out


def rotate_stokes(q, u_, qq, uu, qu, chi_deg):
    """The Stokes vector and its covariance after the reference direction turns by chi (IAU: psi' = psi + chi, so
    Q' + iU' = (Q + iU) e^{2i chi}); the covariance turns as R C R^T with the same rotation."""
    t = np.radians(2.0 * np.asarray(chi_deg, dtype=float))
    c, s = np.cos(t), np.sin(t)
    return (c * q - s * u_, s * q + c * u_,
            c * c * qq - 2 * c * s * qu + s * s * uu,
            s * s * qq + 2 * c * s * qu + c * c * uu,
            c * s * (qq - uu) + (c * c - s * s) * qu)


def p_sigma(q, u_, qq, uu, qu):
    """(P, sigma_P): P = sqrt(Q^2 + U^2), not debiased; sigma_P^2 = (Q^2 QQ + U^2 UU + 2 Q U QU) / P^2, first-order propagation.
    Where P is 0 there is no direction and sigma_P is NaN."""
    with np.errstate(all="ignore"):
        p = np.hypot(q, u_)
        var = (q * q * qq + u_ * u_ * uu + 2.0 * q * u_ * qu) / (p * p)
        return p, np.where((p > 0) & (var >= 0), np.sqrt(var), np.nan)


def sample(path: str, facts: dict, w, shape_yx: tuple[int, int], params: dict) -> dict:
    """Sample a HEALPix polarization map onto the celestial WCS `w` (2 axes, array shape `shape_yx`) and form the product. Returns the arrays (angle, P,
    sigma_P, Q, U in the grid frame, IAU) and the counts."""
    import astropy.units as u
    from astropy.coordinates import Galactic, SkyCoord
    if facts["frame"] != "galactic":
        raise HealpixRefused(f"this HEALPix map is in the {facts['frame']} system (COORDSYS = {facts['coordsys']}); only Galactic maps are "
                             "sampled in this version, because the equatorial frame (FK5 or ICRS) or the equinox it means is not stated")
    try:
        grid_frame = ori.sky_frame(w)
    except ori.Undeclared as e:
        raise HealpixRefused(str(e)) from None
    ny, nx = shape_yx
    ys, xs = np.mgrid[0:ny, 0:nx]
    xs, ys = xs.ravel().astype(float), ys.ravel().astype(float)
    with np.errstate(all="ignore"):
        world = w.pixel_to_world(xs, ys)
        gal = world.transform_to(Galactic())
        lon, lat = np.asarray(gal.l.deg, dtype=float), np.asarray(gal.b.deg, dtype=float)
    placed = np.isfinite(lon) & np.isfinite(lat)
    where = np.nonzero(placed)[0]
    hp = _healpix(facts)
    if params["method"] == "gaussian" and params["fwhm_arcmin"] < facts["pixel_resolution_arcmin"]:
        raise HealpixRefused(f"a Gaussian FWHM of {params['fwhm_arcmin']:g} arcmin is smaller than this map's pixels "
                             f"({facts['pixel_resolution_arcmin']:.3g} arcmin), so it would not smooth anything; use nearest or bilinear sampling")
    if params["method"] == "gaussian" and where.size:
        # Q and U are averaged against Galactic north at each pixel. Across a kernel of radius r that north turns by about r tan|b|,
        # which is negligible at Magellanic latitudes but not near a Galactic pole, where averaging would depolarize silently.
        radius = GAUSS_TRUNCATE * params["fwhm_arcmin"] / 60.0 / FWHM_PER_SIGMA
        edge = min(89.99, float(np.max(np.abs(lat[where]))) + radius)
        turn = radius * math.tan(math.radians(edge))
        if turn > MAX_KERNEL_TURN_DEG:
            raise HealpixRefused(f"at Galactic latitude up to {edge:.1f} degrees, Galactic north turns by about {turn:.0f} degrees across a "
                                 f"{params['fwhm_arcmin']:g} arcmin kernel, and averaging Q and U against it would depolarize the result; "
                                 f"choose a kernel under {MAX_KERNEL_TURN_DEG:g} degrees of turn, or nearest or bilinear sampling")
    tg, px, wt = kernel(hp, lon[where], lat[where], params["method"], params.get("fwhm_arcmin"))
    upix, inv = np.unique(px, return_inverse=True)
    vals = read_pixels(path, facts, upix)
    q, u_, qq, uu, qu = (vals[k][inv] for k in ("Q_STOKES", "U_STOKES", "QQ_COV", "UU_COV", "QU_COV"))
    flipped = facts["polcconv"] == "COSMO"
    if flipped:                                       # COSMO -> IAU: U changes sign, and with it the Q-U covariance
        u_, qu = -u_, -qu
    ok = np.isfinite(q) & np.isfinite(u_) & np.isfinite(qq) & np.isfinite(uu) & np.isfinite(qu)
    nt = where.size
    wsum = np.bincount(tg[ok], weights=wt[ok], minlength=nt)
    wall = np.bincount(tg, weights=wt, minlength=nt)
    has = wsum > 0
    norm = np.where(ok, wt / np.where(has, wsum, 1.0)[tg], 0.0)

    def agg(v, w2=False):
        return np.bincount(tg[ok], weights=((norm * norm) if w2 else norm)[ok] * v[ok], minlength=nt)
    # the kernel averages Q and U as each pixel states them, against Galactic north (the local meridian), as healpy and reproject
    # interpolate them; the covariances add with the squared weights
    Q, U = agg(q), agg(u_)
    QQ, UU, QU = agg(qq, True), agg(uu, True), agg(qu, True)
    sumw2 = np.bincount(tg[ok], weights=(norm * norm)[ok], minlength=nt)
    # then the reference direction turns from Galactic north to the grid frame's north at the target position (astropy). Every pixel of
    # one target turns by the same angle, so turning after the average is the same as turning each pixel first.
    if grid_frame.is_equivalent_frame(Galactic()):
        delta = np.zeros(nt)
    else:
        delta = ori.rotate_pa_frame(SkyCoord(lon[where] * u.deg, lat[where] * u.deg, frame="galactic"), np.zeros(nt), grid_frame)
    Q, U, QQ, UU, QU = rotate_stokes(Q, U, QQ, UU, QU, delta)
    P, sP = p_sigma(Q, U, QQ, UU, QU)
    psi = np.where(has & (P > 0), ori.wrap_axial(0.5 * np.degrees(np.arctan2(U, Q))), np.nan)
    angle = ori.wrap_axial(psi + PRODUCTS[params["product"]])
    angle = np.where(np.isfinite(psi), angle, np.nan)
    counts = {"pixels": int(nx * ny), "off_projection": int((~placed).sum()), "no_healpix_value": int((~has).sum()),
              "partial_kernel": int((has & (wsum < wall - 1e-9)).sum()), "zero_polarization": int((has & ~(P > 0)).sum())}
    snr = None
    if "snr_min" in params:
        k = params["snr_min"]
        with np.errstate(all="ignore"):
            known = np.isfinite(P) & np.isfinite(sP) & (sP > 0)
            keep = known & (P / np.where(known, sP, 1.0) >= k)
        before = np.isfinite(angle)
        snr = {"min": k, "rule": f"the angle is kept where P/sigma_P >= {k:g}; P and sigma_P are kept everywhere",
               "below": int((before & known & ~keep).sum()), "no_ratio": int((before & ~known).sum())}
        angle = np.where(keep, angle, np.nan)

    def full(v):
        out = np.full(nx * ny, np.nan)
        out[where] = np.where(has, v, np.nan)
        return out.reshape(ny, nx)
    counts["with_angle"] = int(np.isfinite(angle).sum())
    return {"angle": full(angle), "p": full(P), "sigma_p": full(sP), "q": full(Q), "u": full(U), "sum_w2": full(sumw2),
            "counts": counts, "snr": snr, "flipped": flipped, "grid_frame": getattr(grid_frame, "name", None),
            "rotation_deg": {"min": float(np.nanmin(delta)) if delta.size else None, "max": float(np.nanmax(delta)) if delta.size else None}}


def derivation(facts: dict, params: dict, res: dict) -> dict:
    """How the result was derived, in words a reviewer can read: the convention, the frame rotation, the sampling, any smoothing,
    and what the noise does and does not include."""
    m = params["method"]
    sampling = {"nearest": "nearest: each target pixel takes the HEALPix pixel that holds its centre (astropy-healpix lonlat_to_healpix)",
                "bilinear": "bilinear: the four bilinear weights of astropy-healpix (bilinear_interpolation_weights) at the target pixel centre",
                "gaussian": (f"Gaussian smoothing on the sphere, FWHM {params.get('fwhm_arcmin', 0):g} arcmin: every HEALPix pixel whose centre "
                             f"is within {GAUSS_TRUNCATE:g} sigma of the target pixel centre, weighted exp(-d^2 / 2 sigma^2) by angular distance")}[m]
    return {
        "convention": {"source": facts["polcconv"], "flipped": res["flipped"],
                       "rule": ("COSMO to IAU: U_IAU = -U_COSMO and QU_IAU = -QU_COSMO, before anything else" if res["flipped"]
                                else "the map states IAU; nothing was flipped")},
        "frame_rotation": {"from": "Galactic north at each HEALPix pixel", "to": f"north of the grid's own celestial frame ({res['grid_frame']})",
                           "method": ("at each target pixel, the angle between Galactic north and the grid frame's north there (astropy: a "
                                      "short step towards Galactic north carried into the grid frame); Q, U and their covariance turn by "
                                      "twice that angle"),
                           "rotation_range_deg": res["rotation_deg"]},
        "sampling": sampling,
        "smoothing": None if m != "gaussian" else {"fwhm_arcmin": params["fwhm_arcmin"], "truncation_sigma": GAUSS_TRUNCATE},
        "order": ("flip U (convention), average Q and U over the kernel in the Galactic basis, rotate into the grid frame, then form the "
                  "angle; every pixel of one target turns by the same angle, so rotating before or after the average is the same"),
        "basis": ("the kernel averages Q and U against Galactic north at each pixel (the local meridian), not parallel-transported to the "
                  "target. Galactic north turns across a kernel of radius r by about r tan|b|; the first order cancels in a symmetric "
                  "kernel and the rest is a slight depolarization, not corrected. A Gaussian kernel whose turn at its edge would exceed "
                  f"{MAX_KERNEL_TURN_DEG:g} degrees is refused"),
        "angle": ("psi = 1/2 atan2(U, Q), east of north in the grid's own celestial frame, axial in [0, 180) degrees"
                  + ("; the field orientation is psi + 90 degrees" if params["product"] == "b_field" else "; this is the dust polarization angle")),
        "missing": "a HEALPix pixel with no value (NaN, the bad-data value, a negative variance) is left out and the kernel's weights are "
                   "renormalised over the pixels that have one; a target pixel none of whose kernel pixels has a value is NaN, never 0",
        "noise": ("P = sqrt(Q^2 + U^2), not debiased. sigma_P^2 = (Q^2 QQ + U^2 UU + 2 Q U QU) / P^2 after the kernel, with each "
                  "covariance summed with the squared weights (sum of w^2 var). " + ("" if facts["has_qu_cov"] else "The map has no QU_COV, "
                  "so the Q-U covariance is taken as 0. ") + "Correlated noise between HEALPix pixels (from the beam, the scanning or "
                  "this smoothing) is not modelled: pixels are taken as independent, so sigma_P understates the uncertainty where they are positively correlated, and neighbouring target "
                  "pixels that share HEALPix pixels are not independent samples."),
    }


def _unit_card(text):
    """A unit as a FITS card can carry it (printable ASCII), or None: the unit is the file's own text, never converted."""
    return text if isinstance(text, str) and text and text.isascii() and text.isprintable() else None


def op_sample(args, params: dict) -> dict:
    """analysis.py healpix_sample: --input is the HEALPix map, --reference the dataset whose celestial grid receives it, --out the angle
    map, --p-out and --sigma-out the companion P and sigma_P maps."""
    from analysis import authorize, image_header, open_cube, write_fits
    from fits_header import read_header_at
    from srcopen import source_file
    p = check_params(params)
    real = authorize(args.input)
    with source_file(real) as (_, f):
        h0, end = read_header_at(f, 0)
        ext = read_header_at(f, end)[0] if h0.get("EXTEND") is True and not int(h0.get("NAXIS", 0)) else None
    if ext is None or not is_healpix(ext):
        raise HealpixRefused("the input is not a HEALPix map (a binary table with PIXTYPE = 'HEALPIX' after an empty primary HDU)")
    facts = describe_header(ext)
    target = open_cube(args.reference)
    try:
        w = ori.celestial_wcs(target)
    except ori.Undeclared as e:
        raise HealpixRefused("the target dataset: " + str(e)) from None
    shape = (target.shape[target.y], target.shape[target.x])
    res = sample(real, facts, w, shape, p)
    how = derivation(facts, p, res)
    quantity = p["product"]
    base = image_header(target, {"VESQOP": "healpix_sample"})
    hist = [f"Vesqira: HEALPix {facts['polcconv']} map (NSIDE {facts['nside']}, {facts['ordering']}, {facts['coordsys']}) sampled as Q, U",
            "  " + how["convention"]["rule"],
            "  reference direction rotated from Galactic north to the grid frame's north at each pixel (astropy)",
            "  sampling: " + p["method"] + (f", Gaussian FWHM {p['fwhm_arcmin']:g} arcmin" if "fwhm_arcmin" in p else ""),
            "  correlated noise between HEALPix pixels is not modelled"]
    ha = base.copy()
    ha["BUNIT"] = "deg"
    ha["VESQPROD"] = (quantity, "dust_polarization: psi; b_field: psi + 90 deg")
    ha["VESQPCNV"] = (facts["polcconv"], "polarization convention of the HEALPix input")
    ha["HISTORY"] = "Vesqira: " + ("dust polarization angle psi = 1/2 atan2(U, Q)" if quantity == "dust_polarization" else "field orientation psi + 90 deg")
    ha["HISTORY"] = "  east of north in the grid's own celestial frame, axial, degrees in [0, 180)"
    for line in hist:
        ha["HISTORY"] = line
    write_fits(args.out, res["angle"].astype("<f4"), ha)
    unit = _unit_card(facts["stokes_unit"])
    companions = {}
    for key, path, what in (("p", args.p_out, "polarized intensity P = sqrt(Q^2 + U^2), not debiased"),
                            ("sigma_p", args.sigma_out, "uncertainty of P from QQ, UU (and QU) after the kernel, sum of w^2 var")):
        if not path:
            continue
        hc = base.copy()
        if unit:
            hc["BUNIT"] = unit
        hc["HISTORY"] = "Vesqira: companion of a sampled HEALPix polarization map: " + what
        for line in hist:
            hc["HISTORY"] = line
        write_fits(path, res[key].astype("<f4"), hc)
        companions[key] = True
    ok = np.isfinite(res["p"])
    with np.errstate(all="ignore"):
        ratio = res["p"][ok] / res["sigma_p"][ok]
    summary = {
        "product": quantity, "quantity": quantity, "angle_unit": "deg", "angle_frame": "sky",
        "angle_frame_note": "east of north in the grid's own celestial frame (" + str(res["grid_frame"]) + ")",
        "healpix": {k: facts[k] for k in ("nside", "ordering", "coordsys", "polcconv", "stokes_unit", "covariance_unit", "has_qu_cov", "pixel_resolution_arcmin")},
        "parameters": p, "derivation": how, "counts": res["counts"], "snr_filter": res["snr"],
        "p_unit": facts["stokes_unit"], "companions": companions,
        "p_median": float(np.median(res["p"][ok])) if ok.any() else None,
        "snr_median": float(np.median(ratio[np.isfinite(ratio)])) if np.isfinite(ratio).any() else None,
        "shape": list(res["angle"].shape),
        "policy": ("Derived from a HEALPix polarization map, not measured on this grid: Q and U were sampled ("
                   + p["method"] + (f", Gaussian FWHM {p['fwhm_arcmin']:g} arcmin" if "fwhm_arcmin" in p else "") + "), "
                   + ("converted from COSMO to IAU (U = -U), " if res["flipped"] else "stated IAU by the file, ")
                   + "rotated from Galactic north to the grid frame's north at each pixel, and the angle was formed last"
                   + (" and turned by +90 degrees to the field orientation" if quantity == "b_field" else "") + ". "
                   + "P is not debiased; sigma_P propagates QQ and UU" + (" and QU" if facts["has_qu_cov"] else " (QU taken as 0)")
                   + " through the kernel's squared weights, and correlated noise between HEALPix pixels is not modelled. "
                   + "No beam is declared for the result; the resolution is the HEALPix map's"
                   + (" widened by the smoothing" if "fwhm_arcmin" in p else "") + "."),
    }
    return {"ok": True, "op": "healpix_sample", "summary": summary}
