"""Plain unit conversion of numbers that carry no dataset (ADR 0062, cross-run plot).

A run's declared metric (value, uncertainty, unit) is converted to another unit by astropy.units dimensional conversion
only: a unit converts to another if and only if both reduce to the same base dimensions. No equivalency is ever passed
(no spectral(), brightness_temperature(), dimensionless_angles(), temperature() ...), because each of those needs a
dataset's beam, frame, rest frequency or spectral axis, and a run's number has none. K to Jy/beam, km/s to Hz and rad to a
plain number are therefore refused, as are function units (mag, dex, dB, found by the module astropy defines them in) and anything that is not a pure scaling.
A refusal is an answer; nothing is guessed, rounded or defaulted.
"""
from __future__ import annotations

import math

from astropy import units as u

from convert import parse_unit

MAX_ITEMS = 5000


class Bad(ValueError):
    """A request that is malformed (not a scientific refusal)."""


def _plain(text):
    """The astropy unit for text, or None. A function unit (mag, dex, dB) is not a plain scaling and is not accepted."""
    un = parse_unit(text)
    if not isinstance(un, u.UnitBase) or _is_function_unit(un):
        return None
    return un


def _is_function_unit(un):
    """mag, dex and dB are logarithmic: astropy gives them plain unit classes from its function module, so they are found by
    where they come from (the unit itself or any base of it), not by isinstance."""
    return any(type(x).__module__.startswith("astropy.units.function") for x in [un, *un.decompose().bases])


def _factor(un, target):
    """The multiplier from un to target, or None when it is not a pure scaling (an offset or a non-linear unit)."""
    f = float(un.to(target, 1.0))
    if not math.isfinite(f) or f <= 0:
        return None
    if un.to(target, 0.0) != 0.0 or not math.isclose(float(un.to(target, 2.0)), 2.0 * f, rel_tol=1e-12):
        return None
    return f


def convert_values(req: dict) -> dict:
    to_text, items = req.get("to"), req.get("values")
    if not isinstance(items, list) or not items or len(items) > MAX_ITEMS:
        raise Bad(f"values must be a list of 1 to {MAX_ITEMS} items")
    target = _plain(to_text)
    if target is None:
        return {"refused": True, "reason": f"the target unit \"{to_text}\" is not a unit this program can read"}
    factors, refusals, results = {}, {}, []
    for it in items:
        if not isinstance(it, dict) or not isinstance(it.get("from"), str):
            raise Bad("each value needs a from unit")
        v, e, frm = it.get("value"), it.get("uncertainty"), it["from"]
        for n, x in (("value", v), ("uncertainty", e)):
            if x is None and n == "uncertainty":
                continue
            if isinstance(x, bool) or not isinstance(x, (int, float)) or not math.isfinite(x):
                raise Bad(f"{n} must be a finite number")
        if e is not None and e < 0:
            raise Bad("an uncertainty cannot be negative")
        if frm not in factors and frm not in refusals:
            un = _plain(frm)
            if un is None:
                refusals[frm] = f"\"{frm}\" is not a unit this program can read, so it is not converted"
            elif not un.is_equivalent(target):    # no equivalencies: the dimensions must agree on their own
                refusals[frm] = (f"\"{frm}\" and \"{to_text}\" measure different things; converting "
                                 "between them needs a beam, a frame or a spectral axis that a run's number does not carry")
            else:
                f = _factor(un, target)
                if f is None:
                    refusals[frm] = f"\"{frm}\" to \"{to_text}\" is not a plain scaling, so it is not converted"
                else:
                    factors[frm] = f
        if frm in refusals:
            return {"refused": True, "reason": refusals[frm], "from": frm}
        f = factors[frm]
        nv, ne = v * f, None if e is None else e * f
        if not math.isfinite(nv) or (ne is not None and not math.isfinite(ne)):
            return {"refused": True, "reason": "the converted number is outside the range of a double", "from": frm}
        results.append({"value": nv, "uncertainty": ne})
    return {"refused": False, "to": target.to_string(), "factors": factors, "results": results,
            "method": "astropy.units dimensional conversion, no equivalencies; uncertainty scaled by the same factor"}
