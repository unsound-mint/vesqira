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
MAX_PAIRS = 200


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


def _judge(frm, target, to_text):
    """(factor, None) when frm converts to target by plain scaling, else (None, the reason in words). The one place the rule lives:
    a real conversion and a probe of which pairs would convert both come through here, so a probe cannot promise what a conversion refuses."""
    un = _plain(frm)
    if un is None:
        return None, f"\"{frm}\" is not a unit this program can read, so it is not converted"
    if not un.is_equivalent(target):    # no equivalencies: the dimensions must agree on their own
        return None, (f"\"{frm}\" and \"{to_text}\" measure different things; converting "
                      "between them needs a beam, a frame or a spectral axis that a run's number does not carry")
    f = _factor(un, target)
    if f is None:
        return None, f"\"{frm}\" to \"{to_text}\" is not a plain scaling, so it is not converted"
    return f, None


def probe_pairs(req: dict) -> dict:
    """Dry run: for each (from, to) pair, the factor or the refusal reason, with no values. A refusal is an answer."""
    pairs = req.get("pairs")
    if not isinstance(pairs, list) or not pairs or len(pairs) > MAX_PAIRS:
        raise Bad(f"pairs must be a list of 1 to {MAX_PAIRS} items")
    out = []
    for p in pairs:
        if not isinstance(p, dict) or not isinstance(p.get("from"), str) or not isinstance(p.get("to"), str):
            raise Bad("each pair needs a from unit and a to unit")
        frm, to_text = p["from"], p["to"]
        target = _plain(to_text)
        if target is None:
            f, why = None, f"the target unit \"{to_text}\" is not a unit this program can read"
        else:
            f, why = _judge(frm, target, to_text)
        out.append({"from": frm, "to": to_text, "refused": f is None, **({"reason": why} if f is None else {"factor": f})})
    return {"refused": False, "pairs": out}


def convert_values(req: dict) -> dict:
    if "pairs" in req:
        return probe_pairs(req)
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
            f, why = _judge(frm, target, to_text)
            if f is None:
                refusals[frm] = why
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
