"""The orientation conventions of the tools Vesqira reads: one table (orientation_profiles.json), one reader (ADR 0068, item 2).

A profile maps the header cards a tool writes onto the fields of an orientation declaration: the angle's frame (and its zero
direction and sense), what the angle is (quantity), its unit and its statistics. This module is the only place that table is
interpreted: fits_header.py reports what a header declares through it, and analysis.py uses its producer-card list to keep a derived
file from re-declaring itself. A field the profile cannot place is "unresolved" (None): never a default, never inferred from the WCS
or from the values. What a profile supplies is declared by `profile:<name>@<version>`, never verified.
"""
import json
import re
from functools import lru_cache
from pathlib import Path

PROFILES_FILE = Path(__file__).with_name("orientation_profiles.json")
FIELDS = ("frame", "quantity", "unit")
_PLACEMENT_KEYS = {"frame", "sky_frame", "zero_direction", "sense"}


def label(profile: dict) -> str:
    """How a declaration made by this profile is named wherever it is shown or stored."""
    return f"profile:{profile['name']}@{profile['version']}"


def check(doc: dict) -> list[dict]:
    """The profiles of a parsed table, or ValueError saying what is wrong with it."""
    if doc.get("format") != 1 or not isinstance(doc.get("profiles"), list):
        raise ValueError("orientation profiles: unknown format")
    for p in doc["profiles"]:
        for key in ("name", "version", "detect", "producer_cards", "fields"):
            if key not in p:
                raise ValueError(f"orientation profile {p.get('name')!r} lacks {key!r}")
        if "@" in p["name"] or ":" in p["name"]:
            raise ValueError(f"orientation profile name {p['name']!r} must not contain '@' or ':' (it is part of the declaration's label)")
        applies = p.get("applies")
        if applies is not None and (not isinstance(applies.get("card"), str) or not applies.get("values") or any(v != v.upper() for v in applies["values"])):
            raise ValueError(f"orientation profile {p['name']!r}: applies needs a card and values in upper case")
        if set(p["fields"]) - set(FIELDS):
            raise ValueError(f"orientation profile {p['name']!r} declares unknown fields {sorted(set(p['fields']) - set(FIELDS))}")
        for field, spec in p["fields"].items():
            if "card" not in spec or not isinstance(spec.get("values"), dict) or any(k != k.upper() for k in spec["values"]):
                raise ValueError(f"orientation profile {p['name']!r}: field {field!r} needs a card and values keyed in upper case")
        for frame in p["fields"].get("frame", {}).get("values", {}).values():
            if set(frame) - _PLACEMENT_KEYS or frame.get("frame") not in ("pixel", "sky"):
                raise ValueError(f"orientation profile {p['name']!r}: a frame value must name pixel or sky, with only {sorted(_PLACEMENT_KEYS)}")
    return doc["profiles"]


@lru_cache(maxsize=1)
def shipped() -> tuple:
    """The profiles that ship with Vesqira."""
    return tuple(check(json.loads(PROFILES_FILE.read_text(encoding="utf-8"))))


def producer_cards(profiles=None) -> re.Pattern:
    """Matches the name of any card the profiles' tools write to describe their own product (and not the data's WCS)."""
    parts = [c for p in (shipped() if profiles is None else profiles) for c in p["producer_cards"]]
    return re.compile("|".join(parts)) if parts else re.compile(r"(?!)")


def _word(value) -> str | None:
    return value.strip().upper() if isinstance(value, str) and value.strip() else None


def read(header, profiles=None) -> dict | None:
    """What a header declares of an orientation map, by the first profile whose detect cards it carries; None when none does.
    A profile may say which products carry an angle (`applies`: a card and its values): a header whose card names another product, such as
    VGT.jl's reliability map, is not an orientation map of that profile and gets None, not an orientation with everything unresolved.

    The result names the profile and the cards as written, then each field (frame, sky_frame, zero_direction, sense, quantity, unit,
    statistics) as the profile places it, or None; `unresolved` lists the fields among frame, quantity and unit that are None and
    `notes` say why a card that is present was not placed; `from` gives, for each field that was placed, the card and value it came from. `header` is any mapping of card to value (an astropy Header)."""
    for p in (shipped() if profiles is None else profiles):
        if not any(c in header for c in p["detect"]):
            continue
        applies = p.get("applies")
        if applies and applies["card"] in header and _word(header[applies["card"]]) not in applies["values"]:
            continue
        cards, basis, stated, notes = {}, {}, {}, []
        out = {"frame": None, "sky_frame": None, "zero_direction": None, "sense": None, "quantity": None, "unit": None}
        for field, spec in p["fields"].items():
            card = spec["card"]
            if card not in header:
                continue
            cards[card] = header[card]
            word = _word(header[card])
            hit = spec["values"].get(word) if word else None
            if hit is None:
                notes.append(f"{card} is {header[card]!r}, which profile {label(p)} does not place, so the {field} is unresolved")
                continue
            if field == "frame":
                out.update({k: hit.get(k) for k in _PLACEMENT_KEYS})
            else:
                out[field] = hit
            basis[field], stated[field] = "declared", f"{card}={header[card]}"
        return {"profile": p["name"], "version": p["version"], "label": label(p), "source": p.get("source"), "cards": cards,
                "statistics": p.get("statistics", {}).get("value"), **out, "basis": basis, "from": stated,
                "unresolved": [f for f in FIELDS if out[f] is None], "notes": notes}
    return None
