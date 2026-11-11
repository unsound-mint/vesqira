"""Unit conversions and frame transforms for the viewer's readouts (ADR 0040).

Everything numerical is astropy (units equivalencies, brightness_temperature, SkyCoord); nothing is hand-rolled
except the textbook definition of a Gaussian beam's solid angle from the two FWHMs. The native value is always
returned beside the converted one, with the basis that made the conversion legitimate, or a reason it was refused.
A refusal is an answer, never a fallback: no rest frequency, beam, unit or convention is ever invented.
"""
from __future__ import annotations

import math
import warnings

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


def convert_spectral(c, channel: int, unit_text, convention) -> dict:
    kind, target = _spectral_target(unit_text)
    if convention is not None and convention not in CONVENTIONS:
        raise Bad("unknown velocity convention")
    axis, why = _axis(c)
    if axis is None:
        return {"native": None, "converted": None, "refused": why, "basis": None}
    v = float(c._world(0, 0, channel)[c.spec])
    native = {"value": v if math.isfinite(v) else None, "unit": axis["unit_text"], "unit_status": axis["unit_status"]}
    base = {"native": native, "converted": None, "refused": None, "basis": None, "convention": None,
            "convention_source": None, "rest": None, "specsys": axis["specsys"], "reference_frame_note": SPECSYS_NOTE}
    if not math.isfinite(v):
        return {**base, "refused": "the native spectral value is not finite"}
    q = v * axis["unit"]
    rest, src = rest_of(c.h)
    if rest is not None:
        base["rest"] = {"value": float(rest.value), "unit": "Hz", "source": src}
    try:
        if kind != "vel":
            if axis["kind"] == "vel":
                freq, why = _frequency(q, axis, rest)
                if freq is None:
                    return {**base, "refused": why}
                out = freq.to(target, u.spectral())
                base["convention"], base["convention_source"] = axis["convention"], "file"
                basis = (f"astropy.units doppler_{axis['convention']} with the file's {src} = {rest.value:.12g} Hz (CTYPE {axis['ctype']} declares the "
                         f"{axis['convention']} convention), then spectral()")
            else:
                out = q.to(target, u.spectral())
                basis = "astropy.units.spectral(): c = 299792458 m/s, vacuum wavelength; no rest frequency or convention involved"
        elif axis.get("legacy"):
            if convention is not None:
                return {**base, "refused": f"the axis is {axis['ctype']}, which does not declare its velocity convention; a convention is not applied to it"}
            out = q.to(target)
            basis = f"unit change only (astropy.units); {axis['ctype']} is a legacy spelling whose velocity convention is not declared, and none is assumed"
        elif axis["kind"] == "vel":
            if convention is not None and convention != axis["convention"]:
                return {**base, "refused": f"the file declares the {axis['convention']} convention ({axis['ctype']}); another convention is not applied to a velocity axis"}
            out = q.to(target)
            base["convention"], base["convention_source"] = axis["convention"], "file"
            basis = f"unit change only (astropy.units); the velocity is the file's {axis['convention']} velocity ({axis['ctype']})"
        else:
            if rest is None:
                return {**base, "refused": "velocity needs a rest frequency or wavelength from the file (RESTFRQ, RESTFREQ, RESTWAV); there is none"}
            if convention is None:
                return {**base, "refused": f"the axis is {axis['ctype']} and the file declares no velocity convention: choose radio, optical or relativistic"}
            freq, why = _frequency(q, axis, rest)
            out = freq.to(target, CONVENTIONS[convention](rest))
            base["convention"], base["convention_source"] = convention, "user"
            basis = (f"astropy.units doppler_{convention} with the file's {src} = {rest.value:.12g} Hz; the {convention} convention was chosen by "
                     f"you, the file ({axis['ctype']}) declares none")
    except Bad:
        raise
    except Exception:
        return {**base, "refused": "the conversion is not defined for this value"}
    ov = float(out.value)
    if not math.isfinite(ov):
        return {**base, "refused": "the result is not finite (outside what the convention can express)"}
    return {**base, "converted": {"value": ov, "unit": unit_text}, "basis": basis}


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
