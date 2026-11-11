"""Unit conversions and frame transforms for the viewer's readouts (ADR 0040).

Everything numerical is astropy (units equivalencies, brightness_temperature, SkyCoord); nothing is hand-rolled
except the textbook definition of a Gaussian beam's solid angle from the two FWHMs. The native value is always
returned beside the converted one, with the basis that made the conversion legitimate, or a reason it was refused.
A refusal is an answer, never a fallback: no rest frequency, beam, unit or convention is ever invented.
"""
from __future__ import annotations

import math
import warnings

import numpy as np

from astropy import units as u
from astropy.coordinates import FK4, FK5, ICRS, Galactic, SkyCoord
from astropy.stats import gaussian_fwhm_to_sigma
from astropy.wcs.utils import wcs_to_celestial_frame

from fits_header import clean

MAX_UNIT_TEXT = 40

# Spectral display units the viewer may ask for. kind: freq / wave / vel.
SPECTRAL_TARGETS = {
    "Hz": ("freq", u.Hz), "kHz": ("freq", u.kHz), "MHz": ("freq", u.MHz), "GHz": ("freq", u.GHz),
    "m": ("wave", u.m), "cm": ("wave", u.cm), "mm": ("wave", u.mm), "um": ("wave", u.um),
    "m/s": ("vel", u.m / u.s), "km/s": ("vel", u.km / u.s),
}
# What a FITS spectral CTYPE says about the axis (Paper III): frequency, vacuum wavelength, and velocities that each
# name their own convention. AWAV (air wavelength) needs a refractive index and is not converted.
AXIS_KIND = {"FREQ": "freq", "WAVE": "wave", "VRAD": "vel", "VOPT": "vel", "VELO": "vel"}
FILE_CONVENTION = {"VRAD": "radio", "VOPT": "optical", "VELO": "relativistic"}
CONVENTIONS = {"radio": u.doppler_radio, "optical": u.doppler_optical, "relativistic": u.doppler_relativistic}
FRAMES = {"icrs": "ICRS", "fk5": "FK5 (J2000)", "galactic": "Galactic"}
BRIGHTNESS_FAMILIES = {"temperature": ["K", "mK", "uK"], "flux": ["Jy/beam", "mJy/beam", "uJy/beam"]}
SPECSYS_NOTE = "The reference frame of the axis is the file's and is not changed; a frame change such as TOPOCENT to LSRK is not offered."


class Bad(ValueError):
    """A request that is malformed (not a scientific refusal)."""


def parse_unit(text):
    """An astropy unit for text, or None when astropy does not recognise it (never guessed)."""
    if not isinstance(text, str) or not text or len(text) > MAX_UNIT_TEXT:
        return None
    for fmt in ("generic", "fits"):
        try:
            with warnings.catch_warnings():
                warnings.simplefilter("ignore")
                un = u.Unit(text, format=fmt, parse_strict="raise")
            return un if not isinstance(un, u.UnrecognizedUnit) else None
        except Exception:
            continue
    return None


def _positive(h, key):
    v = clean(h.get(key))
    return float(v) if isinstance(v, (int, float)) and not isinstance(v, bool) and math.isfinite(v) and v > 0 else None


def rest_of(h):
    """(rest frequency as a Quantity in Hz, source card) from RESTFRQ, the older RESTFREQ, or RESTWAV; else (None, None)."""
    for key in ("RESTFRQ", "RESTFREQ"):
        v = _positive(h, key)
        if v is not None:
            return v * u.Hz, key
    v = _positive(h, "RESTWAV")
    if v is not None:
        return (v * u.m).to(u.Hz, u.spectral()), "RESTWAV"
    return None, None


def _axis(c):
    """What kind of spectral axis the cube has and what stands in the way of converting it: (info, refusal)."""
    f = c.spectral_facts()
    if f is None or not f.get("is_spectral"):
        return None, "the file has no spectral axis"
    if not f["resolved"]:
        return None, f.get("note") or "the spectral axis is not resolved"
    ctype = f.get("ctype") or ""
    # AIPS-era spellings such as VELO-LSR (common in survey cubes, e.g. MAGMA CO): the axis is a velocity, but its
    # convention is not declared, so only velocity unit changes are offered; anything needing a convention is refused.
    legacy = ctype[4:8] in ("-LSR", "-HEL", "-OBS")
    base = ctype[:4]
    if legacy and base not in ("VELO", "FELO", "VRAD", "VOPT"):
        return None, f"{ctype} is a legacy spelling that is not a velocity axis"
    if base == "AWAV":
        return None, "the axis is in air wavelength: converting it needs a refractive index, which is not applied"
    kind = "vel" if legacy else AXIS_KIND.get(base)
    unit = parse_unit(f.get("unit") or "")
    if kind is None or unit is None:
        return None, f"axis type {ctype or 'unknown'} is not frequency, wavelength or a velocity (FREQ, WAVE, VRAD, VOPT, VELO)"
    if not unit.is_equivalent({"freq": u.Hz, "wave": u.m, "vel": u.m / u.s}[kind]):
        return None, f"the axis unit {f.get('unit')} does not fit a {kind} axis"
    return {"kind": kind, "ctype": ctype, "unit": unit, "unit_text": f["unit"], "unit_status": f["unit_status"],
            "convention": None if legacy else FILE_CONVENTION.get(base), "legacy": legacy,
            "specsys": f.get("specsys") or ({"-LSR": "LSR (legacy CTYPE suffix)", "-HEL": "heliocentric (legacy CTYPE suffix)",
                                             "-OBS": "observer (legacy CTYPE suffix)"}[ctype[4:8]] if legacy else None)}, None


def _spectral_target(unit_text):
    t = SPECTRAL_TARGETS.get(unit_text) if isinstance(unit_text, str) else None
    if t is None:
        raise Bad("unknown spectral unit")
    return t


def _frequency(q, axis, rest):
    """The axis quantity as a frequency in Hz: (value, reason). Velocities go through the file's own convention."""
    try:
        if axis["kind"] == "vel":
            if axis.get("legacy"):
                return None, f"the axis is {axis['ctype']}, a legacy spelling that does not declare its velocity convention: it is not converted to frequency or wavelength"
            if rest is None:
                return None, "no rest frequency or wavelength in the file (RESTFRQ, RESTFREQ, RESTWAV)"
            return q.to(u.Hz, CONVENTIONS[axis["convention"]](rest)), None
        return q.to(u.Hz, u.spectral()), None
    except Exception:
        return None, "the value is outside what the convention can express"


def spectral_offers(c) -> dict | None:
    """Which spectral display units are available for this file and why the others are not. Facts about the file only."""
    f = c.spectral_facts()
    if f is None:
        return None
    axis, why = _axis(c)
    rest, src = rest_of(c.h)
    out = {"native_unit": f.get("unit"), "native_kind": None if axis is None else axis["kind"], "ctype": f.get("ctype"),
           "file_convention": None if axis is None else axis["convention"], "specsys": f.get("specsys"),
           "rest": None if rest is None else {"value": float(rest.value), "unit": "Hz", "source": src},
           "reference_frame_note": SPECSYS_NOTE, "targets": []}
    for text, (kind, _) in SPECTRAL_TARGETS.items():
        enabled, reason = True, None
        if axis is None:
            enabled, reason = False, why
        elif kind == "vel" and axis["kind"] != "vel" and rest is None:
            enabled, reason = False, "velocity needs a rest frequency or wavelength from the file (RESTFRQ, RESTFREQ, RESTWAV); there is none"
        elif kind != "vel" and axis.get("legacy"):
            enabled, reason = False, f"the axis is {axis['ctype']}, a legacy spelling that does not declare its velocity convention: only velocity units are offered"
        elif kind != "vel" and axis["kind"] == "vel" and rest is None:
            enabled, reason = False, "converting a velocity to frequency or wavelength needs a rest frequency from the file; there is none"
        out["targets"].append({"unit": text, "kind": kind, "enabled": enabled, "reason": reason,
                               "needs_convention": enabled and kind == "vel" and axis["kind"] != "vel"})
    return out


def spectral_plan(c, unit_text, convention):
    """What converting the spectral axis to `unit_text` involves, decided once for the file: (base, refused, apply, basis).

    `base` is the readout's fixed part (native unit, specsys, rest frequency, convention and who chose it); `refused` is a
    sentence when the conversion cannot be done for any channel (else None); `apply` maps a Quantity array in the axis unit to
    the converted Quantity array (it may raise, which the callers report as "not defined for this value"); `basis` says how.
    A plan with no spectral axis returns `(None, why, None, None)`. Both the single readout (convert_spectral) and the channel
    labels (convert_spectral_many) go through here, so a label and a readout cannot disagree.
    """
    kind, target = _spectral_target(unit_text)
    if convention is not None and convention not in CONVENTIONS:
        raise Bad("unknown velocity convention")
    axis, why = _axis(c)
    if axis is None:
        return None, why, None, None
    native = {"unit": axis["unit_text"], "unit_status": axis["unit_status"]}
    base = {"native": native, "converted": None, "refused": None, "basis": None, "convention": None,
            "convention_source": None, "rest": None, "specsys": axis["specsys"], "reference_frame_note": SPECSYS_NOTE}
    rest, src = rest_of(c.h)
    if rest is not None:
        base["rest"] = {"value": float(rest.value), "unit": "Hz", "source": src}

    def refuse(text):
        return base, text, None, None
    if kind != "vel":
        if axis["kind"] == "vel":
            if axis.get("legacy"):
                return refuse(f"the axis is {axis['ctype']}, a legacy spelling that does not declare its velocity convention: it is not converted to frequency or wavelength")
            if rest is None:
                return refuse("no rest frequency or wavelength in the file (RESTFRQ, RESTFREQ, RESTWAV)")
            base["convention"], base["convention_source"] = axis["convention"], "file"
            basis = (f"astropy.units doppler_{axis['convention']} with the file's {src} = {rest.value:.12g} Hz (CTYPE {axis['ctype']} declares the "
                     f"{axis['convention']} convention), then spectral()")
            return base, None, (lambda q: q.to(u.Hz, CONVENTIONS[axis["convention"]](rest)).to(target, u.spectral())), basis
        basis = "astropy.units.spectral(): c = 299792458 m/s, vacuum wavelength; no rest frequency or convention involved"
        return base, None, (lambda q: q.to(target, u.spectral())), basis
    if axis.get("legacy"):
        if convention is not None:
            return refuse(f"the axis is {axis['ctype']}, which does not declare its velocity convention; a convention is not applied to it")
        basis = f"unit change only (astropy.units); {axis['ctype']} is a legacy spelling whose velocity convention is not declared, and none is assumed"
        return base, None, (lambda q: q.to(target)), basis
    if axis["kind"] == "vel":
        if convention is not None and convention != axis["convention"]:
            return refuse(f"the file declares the {axis['convention']} convention ({axis['ctype']}); another convention is not applied to a velocity axis")
        base["convention"], base["convention_source"] = axis["convention"], "file"
        return base, None, (lambda q: q.to(target)), f"unit change only (astropy.units); the velocity is the file's {axis['convention']} velocity ({axis['ctype']})"
    if rest is None:
        return refuse("velocity needs a rest frequency or wavelength from the file (RESTFRQ, RESTFREQ, RESTWAV); there is none")
    if convention is None:
        return refuse(f"the axis is {axis['ctype']} and the file declares no velocity convention: choose radio, optical or relativistic")
    base["convention"], base["convention_source"] = convention, "user"
    basis = (f"astropy.units doppler_{convention} with the file's {src} = {rest.value:.12g} Hz; the {convention} convention was chosen by "
             f"you, the file ({axis['ctype']}) declares none")
    return base, None, (lambda q: q.to(u.Hz, u.spectral()).to(target, CONVENTIONS[convention](rest))), basis


def native_values(c, channels) -> np.ndarray:
    """World values of channels on the spectral axis, in the axis unit, in one WCS call (NaN where not finite)."""
    pix = np.zeros((len(channels), len(c.shape)))
    pix[:, c.chan] = np.asarray(channels, dtype=float)
    with warnings.catch_warnings():
        warnings.simplefilter("ignore")
        return np.asarray(c.wcs.all_pix2world(pix, 0)[:, c.spec], dtype=float)


def convert_spectral_many(c, channels, unit_text, convention) -> dict:
    """The spectral axis of several channels in one unit: the fixed part of the readout (as convert_spectral's) plus
    `native.values` and `converted.values`, one number per channel (None where not finite or not defined)."""
    base, why, apply, basis = spectral_plan(c, unit_text, convention)
    if base is None:
        return {"native": None, "converted": None, "refused": why, "basis": None}
    native = native_values(c, channels)
    out = {**base, "native": {**base["native"], "values": [float(v) if math.isfinite(v) else None for v in native]},
           "converted": None, "refused": why, "basis": None}
    if why is not None:
        return out
    axis_unit = _axis(c)[0]["unit"]
    with warnings.catch_warnings(), np.errstate(all="ignore"):
        warnings.simplefilter("ignore")
        try:
            conv = np.asarray(apply(native * axis_unit).value, dtype=float)
        except Exception:
            return {**out, "refused": "the conversion is not defined for this value"}
    ok = np.isfinite(conv) & np.isfinite(native)
    out["converted"] = {"unit": unit_text, "values": [float(v) if k else None for v, k in zip(conv, ok)]}
    out["basis"] = basis
    return out


def convert_spectral(c, channel: int, unit_text, convention) -> dict:
    many = convert_spectral_many(c, [channel], unit_text, convention)
    if many["native"] is None:
        return many
    v = many["native"]["values"][0]
    native = {"value": v, "unit": many["native"]["unit"], "unit_status": many["native"]["unit_status"]}
    keep = {k: many[k] for k in ("refused", "convention", "convention_source", "rest", "specsys", "reference_frame_note")}
    base = {"native": native, "converted": None, "basis": None, **keep}
    if v is None:
        return {**base, "rest": None, "convention": None, "convention_source": None, "refused": "the native spectral value is not finite"}
    if many["refused"] is not None:
        return {**base, "refused": many["refused"]}
    cv = many["converted"]["values"][0]
    if cv is None:
        return {**base, "refused": "the result is not finite (outside what the convention can express)"}
    return {**base, "converted": {"value": cv, "unit": unit_text}, "basis": many["basis"]}


# ----- sky frame -----
def _frame_label(frame) -> str | None:
    if isinstance(frame, ICRS):
        return "ICRS"
    if isinstance(frame, FK5):
        return f"FK5 (J{frame.equinox.jyear:g})"
    if isinstance(frame, FK4):
        return f"FK4 (B{frame.equinox.byear:g})"
    if isinstance(frame, Galactic):
        return "Galactic"
    return None


def sky_offers(c) -> dict | None:
    if not c.celestial or c.wcs is None:
        return None
    sky = c.sky_facts()
    src, why = _sky_source(c)
    return {"frame": sky.get("frame"), "frame_status": _frame_status(sky), "source": src[0] if src else None,
            "refused": why, "targets": [{"id": k, "label": v, "enabled": src is not None, "reason": why} for k, v in FRAMES.items()]}


def _frame_status(sky: dict) -> str:
    # unresolved = no RADESYS and no EQUINOX on an RA/Dec pair: wcslib (and so astropy) then take ICRS, per the FITS standard
    return {"declared": "declared", "inferred": "inferred", "unresolved": "assumed"}.get(sky.get("frame_status"), "assumed")


def _sky_source(c):
    try:
        with warnings.catch_warnings():
            warnings.simplefilter("ignore")
            frame = wcs_to_celestial_frame(c.wcs.celestial)
    except Exception:
        return None, "the file's celestial frame is not one astropy can build from its header"
    label = _frame_label(frame)
    if label is None:
        return None, "the file's celestial frame is not ICRS, FK5, FK4 or Galactic, so it is not transformed"
    return (label, frame), None


def convert_sky(c, x: int, y: int, channel: int, target_id) -> dict:
    if target_id not in FRAMES:
        raise Bad("unknown sky frame")
    if not c.celestial or c.wcs is None:
        return {"native": None, "converted": None, "refused": "the file has no complete celestial WCS", "basis": None}
    sky = c.sky_facts()
    status = _frame_status(sky)
    src, why = _sky_source(c)
    if src is None:
        return {"native": None, "converted": None, "refused": why, "basis": None}
    world = c._world(x, y, channel)
    lon, lat = float(world[c.lng]), float(world[c.lat])
    label, frame = src
    out = {"native": {"lon": lon, "lat": lat, "frame": label, "frame_status": status, "lon_name": sky["lon_name"], "lat_name": sky["lat_name"]},
           "converted": None, "refused": None, "basis": None,
           "frame_note": None if status == "declared" else (
               "the header declares no RADESYS or EQUINOX: ICRS is assumed, the FITS standard default (what WCS does)" if status == "assumed"
               else "no RADESYS: the frame is taken from EQUINOX by the FITS standard default")}
    if not (math.isfinite(lon) and math.isfinite(lat)):
        return {**out, "refused": "the position is not finite"}
    target = {"icrs": ICRS(), "fk5": FK5(equinox="J2000"), "galactic": Galactic()}[target_id]
    same = _frame_label(target) == label
    if same:
        a, b = lon, lat                          # the frame the file already has: no transformation, no rounding noise
    else:
        with warnings.catch_warnings():
            warnings.simplefilter("ignore")
            sc = SkyCoord(lon * u.deg, lat * u.deg, frame=frame).transform_to(target)
        a, b = float(sc.spherical.lon.deg), float(sc.spherical.lat.deg)
    names = ("l", "b") if target_id == "galactic" else ("RA", "Dec")
    out["converted"] = {"lon": float(a), "lat": float(b), "frame": FRAMES[target_id], "lon_name": names[0], "lat_name": names[1]}
    out["basis"] = ("same frame as the file: no transformation" if same else
                    f"astropy.coordinates SkyCoord, from the file's {label} ({status}) to {FRAMES[target_id]}")
    return out


# ----- linked channels: A's spectral world value -> B's nearest channel (ADR 0042, section 4) -----
MAX_LINK_CHANNELS = 100_000
REST_TOLERANCE = 1e-9


def _spec_refusal(c, who):
    axis, why = _axis(c)
    return axis, (None if axis is not None else f"{who}: {why}")


def link_channels(a, b) -> dict:
    """For every channel of map A, the nearest channel of map B by spectral world value, and the residual in A's axis unit.

    Only what ADR 0040 allows, by astropy equivalencies: the two axes must be in the same reference frame (SPECSYS stated in
    both and equal: a missing one is not "the same", and a frame change is not offered), and the conversion between the two
    kinds of axis must be defined. Two velocity axes of the same convention are compared as velocities (a unit change only;
    each file's own line, so H I and CO link by velocity); a velocity against a frequency or wavelength goes through the rest
    frequency of the velocity file and its convention, and is refused when the other file states a different rest frequency.
    A legacy velocity spelling (VELO-LSR) has no declared convention, so it links only to another one. The nearest channel is
    found by value over B's channels (never by CRVAL and CDELT arithmetic: axes may run backwards); a value beyond B's range
    is `outside`, never clamped to an end channel."""
    ax, why = _spec_refusal(a, "the first map")
    bx, why_b = _spec_refusal(b, "the second map")
    out = {"refused": why or why_b, "unit": None, "a": None, "b": None, "index": None, "residual": None, "outside": None, "basis": None, "specsys": None}
    if ax is not None:
        out["a"] = {"ctype": ax["ctype"], "unit": ax["unit_text"], "specsys": ax["specsys"], "channels": a.channels}
    if bx is not None:
        out["b"] = {"ctype": bx["ctype"], "unit": bx["unit_text"], "specsys": bx["specsys"], "channels": b.channels}
    if out["refused"]:
        return out

    def refuse(text):
        return {**out, "refused": text}
    sa, sb = ax["specsys"], bx["specsys"]
    if not sa or not sb:
        who = "either map" if not sa and not sb else "the first map" if not sa else "the second map"
        return refuse(f"spectral frame not stated by {who} (SPECSYS): the frames cannot be compared, so the channels are not linked")
    if sa.strip().upper() != sb.strip().upper():
        return refuse(f"spectral frames differ ({sa} vs {sb}); changing frame is not offered")
    if a.channels > MAX_LINK_CHANNELS or b.channels > MAX_LINK_CHANNELS:
        return refuse(f"more than {MAX_LINK_CHANNELS} channels: not linked")
    ka, kb = ax["kind"], bx["kind"]
    ra, srca = rest_of(a.h)
    rb, srcb = rest_of(b.h)
    if ka == "vel" and kb == "vel":
        if ax["legacy"] != bx["legacy"]:
            return refuse(f"{ax['ctype'] if ax['legacy'] else bx['ctype']} is a legacy spelling that does not declare its velocity convention: it links only to another legacy velocity axis")
        if not ax["legacy"] and ax["convention"] != bx["convention"]:
            return refuse(f"velocity conventions differ ({ax['convention']} vs {bx['convention']}): a velocity in one is not the other's, and converting needs the rest frequency of one line")
        def to_a(q):
            return q.to(ax["unit"])
        basis = (f"velocity compared as velocity, a unit change only (astropy.units); both axes are {ax['convention'] or 'of an undeclared convention'}, "
                 "each file's own line, so no rest frequency is used")
    elif ka == "vel" or kb == "vel":
        v, vr, vsrc, fr, who_v = (ax, ra, srca, rb, "first map") if ka == "vel" else (bx, rb, srcb, ra, "second map")
        if v["legacy"]:
            return refuse(f"{v['ctype']} is a legacy spelling that does not declare its velocity convention: it is not related to a frequency or wavelength axis")
        if vr is None:
            return refuse(f"the {who_v} is a velocity axis and has no rest frequency or wavelength (RESTFRQ, RESTFREQ, RESTWAV): it cannot be related to a frequency axis")
        if fr is not None and abs(fr.value - vr.value) > REST_TOLERANCE * vr.value:
            return refuse(f"the two files state different rest frequencies ({vr.value:.12g} Hz and {fr.value:.12g} Hz): a velocity in one is not a frequency in the other")
        conv = CONVENTIONS[v["convention"]](vr)
        if ka == "vel":
            def to_a(q):
                return q.to(u.Hz, u.spectral()).to(ax["unit"], conv)
        else:
            def to_a(q):
                return q.to(u.Hz, conv).to(ax["unit"], u.spectral())
        basis = (f"astropy.units doppler_{v['convention']} with the {who_v}'s {vsrc} = {vr.value:.12g} Hz (its CTYPE {v['ctype']} declares the "
                 f"{v['convention']} convention); the other map " + ("states the same rest frequency" if fr is not None else "states none, so this one is used"))
    else:
        def to_a(q):
            return q.to(ax["unit"], u.spectral())
        basis = "astropy.units.spectral(): c = 299792458 m/s, vacuum wavelength; no rest frequency or convention involved"
    with warnings.catch_warnings(), np.errstate(all="ignore"):
        warnings.simplefilter("ignore")
        va = native_values(a, range(a.channels))
        vb = native_values(b, range(b.channels))
        try:
            vb_in_a = np.asarray(to_a(vb * bx["unit"]).value, dtype=float)
        except Exception:
            return refuse("the conversion between the two spectral axes is not defined for these values")
    ids = np.flatnonzero(np.isfinite(vb_in_a))
    if len(ids) == 0:
        return refuse("no channel of the second map has a finite spectral value")
    order = ids[np.argsort(vb_in_a[ids], kind="stable")]
    sv = vb_in_a[order]
    n = len(sv)
    if n > 1:
        pos = np.clip(np.searchsorted(sv, va), 1, n - 1)
        pick = np.where(np.abs(va - sv[pos - 1]) <= np.abs(sv[pos] - va), pos - 1, pos)
        gaps = np.abs(np.diff(sv))
        local = np.concatenate([[gaps[0]], np.maximum(gaps[:-1], gaps[1:]), [gaps[-1]]]) if n > 2 else np.array([gaps[0], gaps[0]])
    else:
        pick, local = np.zeros(len(va), dtype=int), np.array([0.0])
    res = sv[pick] - va
    index, residual, outside = [], [], []
    for i in range(len(va)):
        if not np.isfinite(va[i]):
            index.append(None), residual.append(None), outside.append(False)
            continue
        j = pick[i]
        index.append(int(order[j]))
        residual.append(float(res[i]))
        outside.append(bool(abs(res[i]) > 0.5 * local[j] * (1 + 1e-9)) if n > 1 else bool(res[i] != 0.0))
    return {**out, "refused": None, "unit": ax["unit_text"], "index": index, "residual": residual, "outside": outside, "basis": basis, "specsys": sa}


# ----- brightness / pixel values -----
def _family(un):
    if un is None:
        return None
    for fam, probe in (("temperature", u.K), ("flux", u.Jy / u.beam)):
        if un.is_equivalent(probe):
            return fam
    return None


def beam_of(h):
    """(beam solid angle as a Quantity in sr, description) from BMAJ/BMIN (degrees, FWHM), else (None, reason)."""
    maj, mnr = _positive(h, "BMAJ"), _positive(h, "BMIN")
    if maj is None or mnr is None:
        return None, "the header has no complete beam (BMAJ and BMIN)"
    # solid angle of an elliptical Gaussian: 2 pi sigma_maj sigma_min, sigma = FWHM * gaussian_fwhm_to_sigma
    s1, s2 = (maj * u.deg).to(u.rad).value * gaussian_fwhm_to_sigma, (mnr * u.deg).to(u.rad).value * gaussian_fwhm_to_sigma
    return 2 * math.pi * s1 * s2 * u.sr, f"{maj * 3600:.4g} x {mnr * 3600:.4g} arcsec FWHM (BMAJ, BMIN)"


def plane_frequency(c, channel: int):
    """The frequency of a channel from the spectral axis: (Quantity in Hz | None, reason)."""
    axis, why = _axis(c)
    if axis is None:
        return None, why
    v = float(c._world(0, 0, channel)[c.spec])
    if not math.isfinite(v):
        return None, "the spectral value of this channel is not finite"
    rest, _ = rest_of(c.h)
    freq, why = _frequency(v * axis["unit"], axis, rest)
    return freq, why


def value_offers(c, bunit, status, cross_channel: int = 0) -> dict:
    un = parse_unit(bunit) if bunit else None
    fam = _family(un)
    out = {"unit": bunit, "unit_status": status, "targets": [], "reason": None}
    if not bunit:
        out["reason"] = "the unit is unknown (no BUNIT and none declared): no conversion is offered"
        return out
    if un is None:
        out["reason"] = f"{bunit} is not a unit astropy recognises: no conversion is offered"
        return out
    if fam is None:
        out["reason"] = f"{bunit} is not a temperature or a flux density per beam: no conversion is offered"
        return out
    for t in BRIGHTNESS_FAMILIES[fam]:
        out["targets"].append({"unit": t, "enabled": True, "reason": None, "cross": False})
    other = "flux" if fam == "temperature" else "temperature"
    beam, bwhy = beam_of(c.h)
    freq, fwhy = plane_frequency(c, cross_channel)
    reason = None if beam is not None and freq is not None else (bwhy if beam is None else f"no frequency for the plane: {fwhy}")
    for t in BRIGHTNESS_FAMILIES[other]:
        out["targets"].append({"unit": t, "enabled": reason is None, "reason": reason, "cross": True})
    return out


def convert_value(c, channel: int, value, bunit, status, to_unit) -> dict:
    un = parse_unit(bunit) if bunit else None
    base = {"native": {"value": value, "unit": bunit, "unit_status": status}, "converted": None, "refused": None, "basis": None}
    if not isinstance(to_unit, str) or not to_unit or len(to_unit) > MAX_UNIT_TEXT:
        raise Bad("bad unit")
    if not bunit:
        return {**base, "refused": "the unit is unknown (no BUNIT and none declared): nothing to convert from"}
    if un is None:
        return {**base, "refused": f"{bunit} is not a unit astropy recognises"}
    tgt = parse_unit(to_unit)
    if tgt is None:
        return {**base, "refused": f"{to_unit} is not a unit astropy recognises"}
    sf, tf = _family(un), _family(tgt)
    if sf is None or tf is None:
        # a dimensionally different request (for example K to km/s) is refused, never approximated
        return {**base, "refused": f"{bunit} and {to_unit} do not measure the same thing, and only temperature and flux density per beam can be related"}
    if value is None or not math.isfinite(value):
        return {**base, "refused": "no value at this pixel"}
    q = value * un
    if sf == tf:
        out = q.to(tgt)
        basis = f"unit change within one dimension (astropy.units): {bunit} to {to_unit}"
    else:
        beam, bwhy = beam_of(c.h)
        if beam is None:
            return {**base, "refused": f"{bwhy}: Jy/beam and K cannot be related without the beam"}
        freq, fwhy = plane_frequency(c, channel)
        if freq is None:
            return {**base, "refused": f"no frequency for this plane ({fwhy}): Jy/beam and K cannot be related without it"}
        out = q.to(tgt, u.brightness_temperature(freq, beam_area=beam))
        basis = (f"astropy.units.brightness_temperature (Rayleigh-Jeans) with beam {beam_of(c.h)[1]}, solid angle {beam.value:.6e} sr, "
                 f"and the frequency of this plane {freq.value:.12g} Hz (from the spectral axis, channel {channel + 1})")
    ov = float(out.value)
    if not math.isfinite(ov):
        return {**base, "refused": "the result is not finite"}
    return {**base, "converted": {"value": ov, "unit": to_unit, "unit_status": status}, "basis": basis}


def as_unit_text(req, key):
    v = req.get(key)
    if v is None:
        return None
    if not isinstance(v, str) or not v or len(v) > MAX_UNIT_TEXT or not v.isprintable():
        raise Bad(f"{key} must be unit text")
    return v


def as_value(req):
    v = req.get("value")
    if v is None:
        return None
    if isinstance(v, bool) or not isinstance(v, (int, float)):
        raise Bad("value must be a number")
    v = float(v)
    return v if math.isfinite(v) else None


def offers(c, bunit, status) -> dict:
    return {"spectral": spectral_offers(c), "sky": sky_offers(c), "value": value_offers(c, bunit, status)}


def convert(c, req: dict, x: int, y: int, channel: int, bunit, status) -> dict:
    """One readout's worth of requested conversions; each part is present only when asked for."""
    out = {"x": x, "y": y, "channel": channel, "spectral": None, "sky": None, "value": None}
    spec, conv = as_unit_text(req, "spectral"), req.get("convention")
    if conv is not None and not isinstance(conv, str):
        raise Bad("convention must be text")
    if spec is not None:
        out["spectral"] = convert_spectral(c, channel, spec, conv)
    if req.get("frame") is not None:
        out["sky"] = convert_sky(c, x, y, channel, req.get("frame"))
    to = as_unit_text(req, "to")
    if to is not None:
        out["value"] = convert_value(c, channel, as_value(req), bunit, status, to)
    return out
