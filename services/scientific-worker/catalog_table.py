"""Point catalogs (ADR 0044): read a table of sources, and turn its declared columns into sky positions.

A catalog is untrusted input. Nothing here executes anything: tables are read with astropy's own readers (astropy.io.ascii,
astropy.io.votable, astropy.io.fits, ECSV), never by a parser written here, and every read is bounded before it allocates:
file size, row count, column count, name and cell length. Compressed files are refused (their expansion is not bounded by
the file size), and so are VOTables that name a DTD or entity, fetch a stream from a URL, use a compressed stream or declare
arrays or row counts past the limits.

What this module will not do: guess. A column is a longitude only when the person says so (a proposal from the column names is
only a proposal), its unit and frame are declared, and a value that cannot be read is counted and reported with its line, never
dropped and never turned into 0. Empty cells are "missing", which is not the same as "unreadable".
"""
from __future__ import annotations

import hashlib
import json
import math
import re
import stat
import os
import warnings
from collections import OrderedDict
from io import BytesIO

import numpy as np

MAX_FILE_BYTES = 50 << 20        # larger files are refused, not sampled
MAX_ROWS = 2_000_000
MAX_COLUMNS = 256
MAX_NAME = 64
MAX_CELL = 256                   # characters of a text cell that are ever sent
MAX_ARRAYSIZE = 1 << 16          # a VOTable FIELD arraysize larger than this is refused
MAX_SEXAGESIMAL_ROWS = 200_000   # astropy parses a sexagesimal string at a time; more rows than this is refused for positions
PREVIEW_ROWS = 20
PAGE_ROWS = 500
MAX_POSITION_ROWS = 100_000      # rows of positions in one reply
MAX_SAMPLES = 50                 # unreadable values listed in a report (the count is always exact)
MAX_VALUE_COLUMNS = 8
TABLES_KEPT = 2

FORMATS = ("degrees", "hours_degrees", "sexagesimal_hours_degrees", "sexagesimal_degrees")
FRAMES = ("icrs", "fk5", "fk4", "galactic")
EQUATORIAL = ("icrs", "fk5", "fk4")
QUANTITIES = ("b_field", "gradient", "dust_polarization", "starlight_polarization", "unspecified")
# every quantity in the vocabulary is an orientation (an axis, not an arrow): 0 and 180 degrees are the same
AXIAL_QUANTITIES = ("b_field", "gradient", "dust_polarization", "starlight_polarization")
ANGLE_UNITS = ("deg", "rad")
_EQUINOX = re.compile(r"^[JB][0-9]{4}(\.[0-9]{1,3})?$")
_SAFE = re.compile(r"[^\x20-\x7e -￿]")


class Bad(ValueError):
    """A refusal with a short message that is safe to show: it never repeats more than a bounded piece of the file."""


def clean(v, limit=MAX_CELL) -> str:
    return _SAFE.sub("?", str(v))[:limit]


# ---------------------------------------------------------------------------------------------------------------- reading

class Loaded:
    def __init__(self, table, fmt, linenos=None, skipped=None, skipped_total=0, notes=None, headerless=False):
        self.table, self.fmt = table, fmt
        self.linenos = linenos            # source line of each row (text tables), else None
        self.skipped = skipped or []      # lines that are not rows: {"line", "reason", "text"}
        self.skipped_total = skipped_total
        self.notes = notes or []
        self.headerless = headerless
        self.conv: OrderedDict = OrderedDict()   # converted positions by declaration


_LOADED: OrderedDict = OrderedDict()


def load(path: str) -> Loaded:
    """The table in `path` (an authorised file), parsed once and kept for the next page or conversion. A file that changed on disk
    is read again: the key holds size and modification time."""
    st = os.stat(path)
    if not stat.S_ISREG(st.st_mode):
        raise Bad("not a regular file")
    key = (path, st.st_size, st.st_mtime_ns)
    hit = _LOADED.get(key)
    if hit is not None:
        _LOADED.move_to_end(key)
        return hit
    if st.st_size > MAX_FILE_BYTES:
        raise Bad(f"the file is {st.st_size / 1048576:.0f} MB; catalogs over {MAX_FILE_BYTES >> 20} MB are not read")
    with open(path, "rb") as f:
        data = f.read(MAX_FILE_BYTES + 1)
    if len(data) > MAX_FILE_BYTES:
        raise Bad(f"catalogs over {MAX_FILE_BYTES >> 20} MB are not read")
    loaded = parse(data, os.path.basename(path))
    _LOADED[key] = loaded
    while len(_LOADED) > TABLES_KEPT:
        _LOADED.popitem(last=False)
    return loaded


def sniff(head: bytes, name: str) -> str:
    """The format from the first bytes (the name only decides between the plain-text kinds)."""
    if head[:2] == b"\x1f\x8b" or head[:3] == b"BZh" or head[:6] == b"\xfd7zXZ\x00" or head[:4] == b"PK\x03\x04":
        raise Bad("compressed files are not read: decompress the catalog first")
    if head.startswith(b"SIMPLE  ="):
        return "fits"
    s = head[:4096].lstrip(b"\xef\xbb\xbf \t\r\n")
    if s.startswith(b"<?xml") or s[:9].lower() == b"<votable ":
        return "votable"
    if s.startswith(b"# %ECSV"):
        return "ecsv"
    lower = name.lower()
    for line in s.split(b"\n")[:40]:
        t = line.strip()
        if t and not t.startswith(b"\\"):
            if t.startswith(b"|"):
                return "ipac"
            break
    if lower.endswith(".csv"):
        return "csv"
    if lower.endswith(".tsv"):
        return "tsv"
    return "text"


def parse(data: bytes, name: str) -> Loaded:
    if b"\0" in data[:65536] and not data.startswith(b"SIMPLE  ="):
        raise Bad("this is not a text, VOTable or FITS table (it contains binary data)")
    fmt = sniff(data, name)
    with warnings.catch_warnings():
        warnings.simplefilter("ignore")
        if fmt == "fits":
            return read_fits(data)
        if fmt == "votable":
            return read_votable(data)
        text = data.decode("utf-8", "replace")
        if fmt == "ecsv":
            return read_astropy_text(text, "ecsv", "ecsv")
        if fmt == "ipac":
            return read_astropy_text(text, "ipac", "ipac")
        return read_delimited(text, fmt)


def check_shape(rows: int, cols: int) -> None:
    if rows > MAX_ROWS:
        raise Bad(f"the table has {rows:,} rows; the limit is {MAX_ROWS:,}")
    if cols > MAX_COLUMNS:
        raise Bad(f"the table has {cols} columns; the limit is {MAX_COLUMNS}")
    if rows < 1:
        raise Bad("the table has no rows")


def finish(table, fmt, **kw) -> Loaded:
    """Common checks on a table astropy returned: bounded names, only plain columns."""
    from astropy.table import Table
    if not isinstance(table, Table):
        raise Bad("not a table")
    check_shape(len(table), len(table.colnames))
    names = [clean(n, MAX_NAME) or f"col{i + 1}" for i, n in enumerate(table.colnames)]
    seen: dict = {}
    for i, n in enumerate(names):
        k = seen.get(n, 0) + 1
        seen[n] = k
        if k > 1:
            names[i] = f"{n[:MAX_NAME - 4]}_{k}"
    table.rename_columns(table.colnames, names)
    return Loaded(table, fmt, **kw)


def read_astropy_text(text: str, fmt: str, label: str) -> Loaded:
    from astropy.io import ascii
    if text.count("\n") > MAX_ROWS + 10_000:
        raise Bad(f"the table has more than {MAX_ROWS:,} rows")
    try:
        t = ascii.read(text, format=fmt, guess=False)
    except Exception as e:
        raise Bad(f"cannot read this {label} table: {clean(e, 160)}") from None
    return finish(t, label)


_NUMBER = re.compile(r"^[+-]?(\d+\.?\d*|\.\d+)([eEdD][+-]?\d+)?$|^[+-]?(nan|inf|infinity)$", re.I)


def _is_number(tok: str) -> bool:
    return bool(_NUMBER.match(tok))


def read_delimited(text: str, fmt: str) -> Loaded:
    """CSV, TSV and whitespace tables, read by astropy.io.ascii after a line-level check that finds the rows astropy would refuse.

    Why the extra pass: astropy raises one error for a whole table when a single row has the wrong number of fields, without
    saying which; and a reader that raised would give a researcher no table at all. This pass splits every line with astropy's own
    splitter, keeps the lines that have the header's field count (extra empty fields at the end are padding, not data), and
    reports every other line with its number. The kept lines go to astropy unchanged."""
    from astropy.io import ascii
    lines = re.split(r"\r\n|\r|\n", text)
    if len(lines) > MAX_ROWS + 10_000:
        raise Bad(f"the table has more than {MAX_ROWS:,} lines; the limit is {MAX_ROWS:,} rows")
    items = [(i + 1, ln) for i, ln in enumerate(lines) if ln.strip()]
    data_items = [(n, ln) for n, ln in items if not ln.lstrip().startswith("#")]
    if not data_items:
        raise Bad("the file has no rows")

    sample = [ln for _, ln in data_items[:20]]
    if fmt == "csv" or (fmt == "text" and all("," in ln for ln in sample) and not all("\t" in ln for ln in sample)):
        reader, delim, label = ascii.Csv(), ",", "csv"
    elif fmt == "tsv" or (fmt == "text" and all("\t" in ln for ln in sample)):
        reader, delim, label = ascii.Tab(), "\t", "tsv"
    else:
        reader, delim, label = ascii.Basic(), " ", "text table"
    split = reader.data.splitter

    def fields(line: str) -> list:
        return next(iter(split([line])), [])

    first_data = data_items[0]
    first_fields = fields(first_data[1])
    while delim != " " and first_fields and first_fields[-1] == "":
        first_fields = first_fields[:-1]

    # the header: a commented line just before the data that has as many names as the data has fields, or the first data line
    # when it is not all numbers; otherwise there is none and the columns are numbered
    header, header_line, notes, headerless = None, 0, [], False
    for n, ln in reversed([(n, ln) for n, ln in items if n < first_data[0] and ln.lstrip().startswith("#")]):
        names = fields(ln.lstrip().lstrip("#").strip() if delim == " " else ln.lstrip().lstrip("#"))
        while delim != " " and names and names[-1] == "":
            names = names[:-1]
        if len(names) == len(first_fields) and not all(_is_number(x.strip()) for x in names):
            header, header_line = names, n
            break
    if header is None:
        if first_fields and not all(_is_number(x.strip()) for x in first_fields):
            header, header_line = first_fields, first_data[0]
            data_items = data_items[1:]
        else:
            headerless = True
            header = [f"col{i + 1}" for i in range(len(first_fields))]
            notes.append("the file has no header row: the columns are named col1, col2, ... in file order; say what each one is in the declaration")
    ncols = len(header)
    if ncols > MAX_COLUMNS:
        raise Bad(f"the table has {ncols} columns; the limit is {MAX_COLUMNS}")
    if not data_items:
        raise Bad("the file has a header but no rows")
    if len(data_items) > MAX_ROWS:
        raise Bad(f"the table has {len(data_items):,} rows; the limit is {MAX_ROWS:,}")

    names = []
    for i, nm in enumerate(header):
        nm = clean(nm.strip().lstrip("#").strip(), MAX_NAME) or f"col{i + 1}"
        if delim == ",":
            nm = nm.replace(",", "_")
        if delim == " ":
            nm = re.sub(r"\s+", "_", nm)
        names.append(nm)
    seen: dict = {}
    for i, nm in enumerate(names):
        k = seen.get(nm, 0) + 1
        seen[nm] = k
        if k > 1:
            names[i] = f"{nm[:MAX_NAME - 4]}_{k}"

    kept, linenos, skipped, skipped_total = [], [], [], 0

    def skip(n: int, reason: str, text: str) -> None:
        nonlocal skipped_total
        skipped_total += 1
        if len(skipped) < MAX_SAMPLES:
            skipped.append({"line": n, "reason": reason, "text": clean(text, 80)})

    for n, ln in data_items:
        f = fields(ln)
        if delim == " ":
            if len(f) != ncols:
                skip(n, f"has {len(f)} fields, the header has {ncols}", ln)
                continue
            kept.append(ln)
        else:
            extra = f[ncols:]
            if len(f) < ncols:
                skip(n, f"has {len(f)} fields, the header has {ncols}", ln)
                continue
            if any(x.strip() for x in extra):
                skip(n, f"has {len(f)} fields, the header has {ncols}", ln)
                continue
            if extra:                            # trailing empty fields are padding
                if any(delim in x or '"' in x for x in f[:ncols]):
                    skip(n, "has empty extra fields and a quoted field; cannot be tidied safely", ln)
                    continue
                ln = delim.join(f[:ncols])
            kept.append(ln)
        linenos.append(n)
    if not kept:
        raise Bad("no row could be read: " + (skipped[0]["reason"] if skipped else "the file has no rows"))
    body = ("\t" if delim == "\t" else delim).join(names) + "\n" + "\n".join(kept) + "\n"
    kind = {"csv": "csv", "tsv": "tab", "text table": "basic"}[label]
    try:
        t = ascii.read(body, format=kind, guess=False)
    except Exception as e:
        raise Bad(f"cannot read this {label}: {clean(e, 160)}") from None
    if len(t) != len(kept):                      # a quoted field spanning lines would shift the numbering: do not guess
        raise Bad("the rows could not be matched to the lines of the file (a quoted field may span lines)")
    return finish(t, label, linenos=np.asarray(linenos, dtype=np.int64), skipped=skipped, skipped_total=skipped_total, notes=notes, headerless=headerless)


def read_fits(data: bytes) -> Loaded:
    from astropy.io import fits
    from astropy.table import Table
    try:
        with fits.open(BytesIO(data), memmap=False, lazy_load_hdus=True) as hdul:
            idx = None
            for i, h in enumerate(hdul):
                if i and isinstance(h, (fits.BinTableHDU, fits.TableHDU)):
                    idx = i
                    break
            if idx is None:
                raise Bad("this FITS file has no table extension")
            h = hdul[idx]
            hd = h.header
            rows, width, cols = int(hd.get("NAXIS2", 0)), int(hd.get("NAXIS1", 0)), int(hd.get("TFIELDS", 0))
            check_shape(rows, cols)
            if rows * width > len(data):
                raise Bad("the table extension is longer than the file (truncated?)")
            t = Table.read(h)
    except Bad:
        raise
    except Exception as e:
        raise Bad(f"cannot read this FITS table: {clean(e, 160)}") from None
    return finish(t, "fits")


_DOCTYPE = re.compile(rb"<!\s*(DOCTYPE|ENTITY)", re.I)
_STREAM_HREF = re.compile(rb"<\s*STREAM\b[^>]*\bhref\s*=", re.I)
_GZIP_STREAM = re.compile(rb"<\s*STREAM\b[^>]*\bencoding\s*=\s*[\"']?gzip", re.I)
_XINCLUDE = re.compile(rb"xi:include|xmlns:xi", re.I)
_ARRAYSIZE = re.compile(rb"\barraysize\s*=\s*[\"']\s*([0-9x*]+)", re.I)
_NROWS = re.compile(rb"\bnrows\s*=\s*[\"']?\s*(\d+)", re.I)


def read_votable(data: bytes) -> Loaded:
    """A VOTable, read by astropy.io.votable from the bytes already in memory (never from a URL or a path).

    Refused before parsing, because the parser would act on them: a DTD or entity declaration (expansion), a STREAM that points to
    a URL (a network fetch), a gzip-encoded stream (expansion past the file size), an arraysize or row count over the limits."""
    if _DOCTYPE.search(data):
        raise Bad("this VOTable declares a DTD or an entity, which is not read")
    if _STREAM_HREF.search(data):
        raise Bad("this VOTable takes its data from a URL, which is not fetched")
    if _GZIP_STREAM.search(data):
        raise Bad("this VOTable has a compressed data stream, which is not read")
    if _XINCLUDE.search(data):
        raise Bad("this VOTable includes other documents, which is not read")
    for m in _ARRAYSIZE.finditer(data):
        size = 1
        for dim in m.group(1).split(b"x"):          # 60000x60000 is a product, and a trailing * is a variable length
            if dim.strip(b"*").isdigit():
                size *= int(dim.strip(b"*"))
        if size > MAX_ARRAYSIZE:
            raise Bad(f"this VOTable declares an array of {size:,} values per cell; the limit is {MAX_ARRAYSIZE:,}")
    for m in _NROWS.finditer(data):
        if int(m.group(1)) > MAX_ROWS:
            raise Bad(f"this VOTable declares {int(m.group(1)):,} rows; the limit is {MAX_ROWS:,}")
    from astropy.io import votable
    try:
        vo = votable.parse(BytesIO(data), verify="ignore")
        t = vo.get_first_table().to_table(use_names_over_ids=True)
    except Exception as e:
        raise Bad(f"cannot read this VOTable: {clean(e, 160)}") from None
    return finish(t, "votable")


# ----------------------------------------------------------------------------------------------------------------- columns

def dtype_name(c) -> str:
    k = c.dtype.kind
    return {"f": "float", "i": "int", "u": "int", "b": "bool", "U": "string", "S": "string"}.get(k, "other")


def is_scalar(c) -> bool:
    return len(c.shape) == 1 and c.dtype.kind in "fiubUS"


def unit_text(c):
    u = getattr(c, "unit", None)
    if u is None:
        return None
    try:
        s = u.to_string()
    except Exception:
        s = str(u)
    return clean(s, 40) or None


def column_info(t) -> list:
    return [{"name": n, "dtype": dtype_name(t[n]), "unit": unit_text(t[n]), "scalar": is_scalar(t[n])} for n in t.colnames]


def cell(col, i):
    """One cell as JSON: missing (masked or NaN) is null, never 0; text is cleaned and bounded."""
    v = col[i]
    if np.ma.is_masked(v):
        return None
    k = col.dtype.kind
    if k == "f":
        f = float(v)
        return f if math.isfinite(f) else None
    if k in "iu":
        return int(v)
    if k == "b":
        return bool(v)
    if k == "U":
        return clean(v)
    if k == "S":
        return clean(bytes(v).decode("utf-8", "replace"))
    return None


def rows_slice(L: Loaded, offset: int, limit: int) -> dict:
    t = L.table
    end = min(len(t), offset + limit)
    cols = [t[n] for n in t.colnames]
    rows = [[cell(c, i) if is_scalar(c) else None for c in cols] for i in range(offset, end)]
    out = {"offset": offset, "total": len(t), "columns": list(t.colnames), "rows": rows}
    out["lines"] = None if L.linenos is None else [int(x) for x in L.linenos[offset:end]]
    return out


def where(L: Loaded, i: int) -> dict:
    return {"row": i + 1, "line": None if L.linenos is None else int(L.linenos[i])}


# ------------------------------------------------------------------------------------------------------------- proposals

def _norm(name: str) -> str:
    return re.sub(r"[^a-z0-9]", "", re.sub(r"\(.*?\)", "", name.lower()))


EQ_LON = {"ra", "raj2000", "raicrs", "raj2k", "radeg", "ra2000", "ra1950", "rab1950", "rafk5"}
EQ_LAT = {"dec", "dej2000", "deicrs", "decj2000", "decdeg", "dedeg", "de", "dec2000", "dec1950", "deb1950", "decfk5"}
GAL_LON = {"glon", "gl", "lii", "gallon", "galacticlongitude", "l"}
GAL_LAT = {"glat", "gb", "bii", "gallat", "galacticlatitude", "b"}
PLAIN_LON = {"lon", "long", "longitude"}
PLAIN_LAT = {"lat", "latitude"}
ANGLE_NAMES = {"pa", "pos", "posangle", "positionangle", "theta", "angle", "chi", "psi", "polangle", "evpa", "pacor", "paiau"}
SIGMA_NAMES = {"spa", "epa", "dpa", "sigpa", "sigmapa", "paerr", "pae", "pasig", "errpa", "sigtheta", "stheta", "etheta", "pacorerr", "spacor", "epacor"}


def _first(L: Loaded, names: set, taken: set):
    for n in L.table.colnames:
        if n not in taken and _norm(n) in names and is_scalar(L.table[n]):
            return n
    return None


def _angle_unit(name: str, unit):
    m = re.search(r"\((.*?)\)", name)
    tag = (m.group(1) if m else "").lower()
    for text, out in (("deg", "deg"), ("rad", "rad")):
        if text in tag:
            return out, f"the column name says ({m.group(1)})"
    if unit in ("deg", "rad"):
        return unit, "the file declares this unit"
    return None, None


def propose(L: Loaded) -> dict:
    """What the column names and a few values suggest. A proposal is never stored or applied: the person confirms it as a declaration.
    Every proposed field says what it comes from, and a field the names do not settle is left empty."""
    t = L.table
    taken: set = set()
    out: dict = {"basis": "proposed from the column names", "lon": None, "lat": None, "format": None, "frame": None, "equinox": None,
                 "angle": None, "sigma": None, "notes": []}
    lon = _first(L, EQ_LON, taken)
    lat = _first(L, EQ_LAT, taken)
    kind = "equatorial"
    if lon is None or lat is None:
        glon, glat = _first(L, GAL_LON, taken), _first(L, GAL_LAT, taken)
        if glon and glat:
            lon, lat, kind = glon, glat, "galactic"
    if lon is None or lat is None:
        plon, plat = _first(L, PLAIN_LON, taken), _first(L, PLAIN_LAT, taken)
        if plon and plat:
            lon, lat, kind = plon, plat, "plain"
    if lon and lat:
        taken |= {lon, lat}
        out["lon"], out["lat"] = lon, lat
        raw = (lon + " " + lat).lower()
        if kind == "galactic":
            out["frame"] = "galactic"
            out["frame_basis"] = "the columns are named like Galactic longitude and latitude"
        elif kind == "plain":
            out["notes"].append("the names lon and lat do not say which celestial frame the positions are in: choose it")
        elif "b1950" in raw or "1950" in raw:
            out["frame"], out["equinox"] = "fk4", "B1950"
            out["frame_basis"] = "the column names say 1950; the catalog may be FK4 or ICRS-like, check it"
        elif "j2000" in raw or "2000" in raw:
            out["frame"], out["equinox"] = "fk5", "J2000"
            out["frame_basis"] = "the column names say J2000; FK5 and ICRS differ by tens of milliarcseconds, check it"
        else:
            out["notes"].append("the column names do not say which frame or epoch the positions use: choose it")
        # sexagesimal strings or numbers; the unit of a number is only settled when a value is past 24
        lc, bc = t[lon], t[lat]
        samples = [str(v) for v in (lc[:200] if lc.dtype.kind in "US" else []) if not np.ma.is_masked(v)]
        if lc.dtype.kind in "US" and samples:
            sexa = all(re.search(r"\d\s*[: hdms]\s*\d|\d[hdms]", s) for s in samples[:50])
            if sexa:
                if kind in ("equatorial", "plain"):
                    out["format"] = "sexagesimal_hours_degrees"
                    out["format_basis"] = "the values look like h m s and d m s"
                else:
                    out["format"] = "sexagesimal_degrees"
                    out["format_basis"] = "the values look like d m s"
        elif lc.dtype.kind in "fiu":
            vals = np.ma.filled(np.ma.asarray(lc).astype(float), np.nan)
            vals = vals[np.isfinite(vals)]
            if vals.size and float(vals.max()) > 24.0:
                out["format"] = "degrees"
                out["format_basis"] = "the largest longitude is above 24, so it is not in hours"
            elif vals.size:
                out["notes"].append("every longitude is 24 or less: they could be hours or degrees, choose the format")
    else:
        out["notes"].append("no pair of columns is named like a longitude and latitude (RA/DEC, GLON/GLAT, lon/lat)")
    ang = _first(L, ANGLE_NAMES, taken)
    if ang:
        taken.add(ang)
        unit, why = _angle_unit(ang, unit_text(t[ang]))
        out["angle"] = {"column": ang, "unit": unit, "unit_basis": why, "quantity": None,
                        "quantity_basis": "the file does not say whether the angle is along B, across it or something else: choose the quantity"}
        sig = _first(L, SIGMA_NAMES, taken)
        if sig:
            out["sigma"] = sig
    return out


# ----------------------------------------------------------------------------------------------------------- declaration

def validate_declaration(decl, L: Loaded) -> dict:
    """The declaration as it will be used, or a Bad that names what is wrong. The columns must exist and be plain; the frame, format
    and equinox must agree; nothing is filled in."""
    if not isinstance(decl, dict):
        raise Bad("the declaration is missing")
    t = L.table
    cols = set(t.colnames)

    def col(key, v, required=True):
        if v is None or v == "":
            if required:
                raise Bad(f"{key}: choose a column")
            return None
        if not isinstance(v, str) or v not in cols:
            raise Bad(f"{key}: there is no column named {clean(v, 40)!r}")
        if not is_scalar(t[v]):
            raise Bad(f"{key}: column {clean(v, 40)!r} holds arrays, not one value per row")
        return v

    lon, lat = col("longitude", decl.get("lon")), col("latitude", decl.get("lat"))
    if lon == lat:
        raise Bad("longitude and latitude must be different columns")
    fmt = decl.get("format")
    if fmt not in FORMATS:
        raise Bad("format: say how the positions are written (degrees, hours and degrees, or sexagesimal)")
    frame = decl.get("frame")
    if frame not in FRAMES:
        raise Bad("frame: choose the celestial frame (icrs, fk5, fk4 or galactic); it is never assumed")
    equinox = decl.get("equinox") or None
    if frame in ("fk5", "fk4"):
        want = "J" if frame == "fk5" else "B"
        if not isinstance(equinox, str) or not _EQUINOX.match(equinox) or equinox[0] != want:
            raise Bad(f"equinox: {frame.upper()} needs an equinox such as {want}{'2000' if want == 'J' else '1950'}")
    elif equinox:
        raise Bad(f"equinox: {frame} has no equinox")
    if frame == "galactic" and fmt in ("hours_degrees", "sexagesimal_hours_degrees"):
        raise Bad("format: Galactic longitude is not written in hours")
    out = {"lon": lon, "lat": lat, "format": fmt, "frame": frame, "equinox": equinox, "angle": None, "values": []}
    ang = decl.get("angle")
    if ang is not None:
        if not isinstance(ang, dict):
            raise Bad("angle: malformed")
        if ang.get("frame", "sky") != "sky":
            raise Bad("angle: a catalog has no pixel grid; its angle is a position angle on the sky (east of north)")
        unit = ang.get("unit")
        if unit not in ANGLE_UNITS:
            raise Bad("angle: choose the unit, deg or rad")
        q = ang.get("quantity")
        if q is not None and q not in QUANTITIES:
            raise Bad("angle: unknown quantity")
        a = col("angle", ang.get("column"))
        sg = col("uncertainty", ang.get("sigmaColumn"), required=False)
        if a in (lon, lat) or sg in (a, lon, lat):
            raise Bad("angle: the angle and its uncertainty must be columns of their own")
        out["angle"] = {"column": a, "unit": unit, "frame": "sky", "quantity": q, "sigmaColumn": sg}
    vals = decl.get("values") or []
    if not isinstance(vals, list) or len(vals) > MAX_VALUE_COLUMNS:
        raise Bad(f"values: at most {MAX_VALUE_COLUMNS} columns")
    for v in vals:
        out["values"].append(col("values", v))
    if len(set(out["values"])) != len(out["values"]):
        raise Bad("values: a column is listed twice")
    return out


def astropy_frame(frame: str, equinox):
    from astropy.coordinates import FK4, FK5, ICRS, Galactic
    if frame == "icrs":
        return ICRS()
    if frame == "galactic":
        return Galactic()
    if frame == "fk5":
        return FK5(equinox=equinox)
    return FK4(equinox=equinox)


# -------------------------------------------------------------------------------------------------------------- conversion

class Field:
    """Values of one declared column in degrees (NaN where unusable) and what happened to the rest."""

    def __init__(self, n: int, column: str):
        self.values = np.full(n, np.nan)
        self.column = column
        self.gone = np.zeros(n, dtype=bool)      # empty cell: missing
        self.bad = np.zeros(n, dtype=bool)       # a value that cannot be read
        self.samples: list = []

    @property
    def missing(self) -> int:
        return int(self.gone.sum())

    @property
    def invalid(self) -> int:
        return int(self.bad.sum())

    def report(self) -> dict:
        return {"column": self.column, "missing": self.missing, "invalid": self.invalid, "samples": self.samples}


def _note(L: Loaded, f: Field, i: int, value, reason: str, missing: bool) -> None:
    if missing:
        f.gone[i] = True
        return
    f.bad[i] = True
    if len(f.samples) < MAX_SAMPLES:
        f.samples.append({**where(L, i), "column": f.column, "value": clean(value, 40), "reason": reason})


def read_field(L: Loaded, name: str, how: str, low: float, high: float) -> Field:
    """Column `name` as degrees. how: "deg", "hours" (decimal), "hms" or "dms" (sexagesimal). A value outside [low, high] degrees is
    unreadable (for hours the range is checked in hours first), and an empty cell is missing."""
    from astropy.coordinates import Angle
    import astropy.units as u
    col = L.table[name]
    n = len(col)
    f = Field(n, name)
    scale = 15.0 if how in ("hours", "hms") else 1.0
    raw = np.full(n, np.nan)
    if col.dtype.kind in "fiub":
        arr = np.ma.filled(np.ma.asarray(col).astype(float), np.nan)
        raw = arr
        mask = np.ma.getmaskarray(np.ma.asarray(col)) | np.isnan(arr)
        for i in np.flatnonzero(mask):
            _note(L, f, int(i), "", "", True)
        for i in np.flatnonzero(np.isinf(arr)):
            _note(L, f, int(i), arr[i], "is not finite", False)
        if how in ("hms", "dms"):                      # a number where a sexagesimal string was declared: read as decimal
            pass
    else:
        strs = [("" if np.ma.is_masked(v) else str(v).strip()) for v in col]
        todo = []
        for i, s in enumerate(strs):
            if s == "":
                _note(L, f, i, "", "", True)
            else:
                todo.append(i)
        if how in ("hms", "dms"):
            if len(todo) > MAX_SEXAGESIMAL_ROWS:
                raise Bad(f"{len(todo):,} sexagesimal values; positions are read for at most {MAX_SEXAGESIMAL_ROWS:,} of them (use decimal degrees for larger catalogs)")
            unit = u.hourangle if how == "hms" else u.deg
            try:
                vec = Angle([strs[i] for i in todo], unit=unit).deg / scale if todo else np.array([])
                raw[todo] = vec
            except Exception:                         # find which ones; the rest stay usable
                for i in todo:
                    try:
                        raw[i] = Angle(strs[i], unit=unit).deg / scale
                    except Exception as e:
                        why = {"IllegalHourError": "hours must be 0 to 24", "IllegalMinuteError": "minutes must be 0 to 59",
                               "IllegalSecondError": "seconds must be 0 to 59.99"}.get(type(e).__name__, "not sexagesimal")
                        _note(L, f, i, strs[i], f"is not a valid angle ({why})", False)
        else:
            for i in todo:
                try:
                    v = float(strs[i].replace("D", "E").replace("d", "e"))
                except ValueError:
                    _note(L, f, i, strs[i], "is not a number", False)
                    continue
                if math.isfinite(v):
                    raw[i] = v
                else:
                    _note(L, f, i, strs[i], "is not finite", False)
    # range, in the declared unit (hours are checked as hours)
    with np.errstate(invalid="ignore"):
        lo, hi = low / scale, high / scale
        bad = np.isfinite(raw) & ((raw < lo) | (raw > hi))
    for i in np.flatnonzero(bad):
        what = "hours" if scale == 15.0 else "degrees"
        _note(L, f, int(i), raw[i], f"{raw[i]:g} is outside the range {lo:g} to {hi:g} {what}", False)
    ok = np.isfinite(raw) & ~bad
    f.values[ok] = raw[ok] * scale
    return f


def number_field(L: Loaded, name: str, unit: str) -> Field:
    """An angle-like number column (angle or uncertainty) in degrees."""
    col = L.table[name]
    n = len(col)
    f = Field(n, name)
    if col.dtype.kind in "fiub":
        arr = np.ma.filled(np.ma.asarray(col).astype(float), np.nan)
        mask = np.ma.getmaskarray(np.ma.asarray(col)) | np.isnan(arr)
        for i in np.flatnonzero(mask):
            _note(L, f, int(i), "", "", True)
        for i in np.flatnonzero(np.isinf(arr)):
            _note(L, f, int(i), arr[i], "is not finite", False)
            arr[i] = np.nan
    else:
        arr = np.full(n, np.nan)
        for i, v in enumerate(col):
            s = "" if np.ma.is_masked(v) else str(v).strip()
            if s == "":
                _note(L, f, i, "", "", True)
                continue
            try:
                x = float(s)
            except ValueError:
                _note(L, f, i, s, "is not a number", False)
                continue
            if math.isfinite(x):
                arr[i] = x
            else:
                _note(L, f, i, s, "is not finite", False)
    f.values = np.degrees(arr) if unit == "rad" else arr
    return f


class Converted:
    def __init__(self, L, decl, lon: Field, lat: Field, angle, sigma):
        self.decl, self.lon, self.lat, self.angle, self.sigma = decl, lon, lat, angle, sigma
        self.lon_deg = np.where(np.isfinite(lon.values) & np.isfinite(lat.values), lon.values % 360.0, np.nan)
        self.lat_deg = np.where(np.isfinite(lon.values) & np.isfinite(lat.values), lat.values, np.nan)
        self.valid = np.isfinite(self.lon_deg)
        self.angle_deg = None
        self.sigma_deg = None
        if angle is not None:
            from orientation import wrap_axial
            a = angle.values
            if decl["angle"]["quantity"] in AXIAL_QUANTITIES:
                a = wrap_axial(a)                      # an orientation: 0 and 180 degrees are the same, so it is kept in [0, 180)
            self.angle_deg = a
        if sigma is not None:
            s = sigma.values.copy()
            neg = np.isfinite(s) & (s < 0)
            for i in np.flatnonzero(neg):
                _note(L, sigma, int(i), sigma.values[i], "an uncertainty cannot be negative", False)
            s[neg] = np.nan
            self.sigma_deg = s


def convert(L: Loaded, decl: dict) -> Converted:
    key = hashlib.sha1(json.dumps(decl, sort_keys=True).encode()).hexdigest()
    hit = L.conv.get(key)
    if hit is not None:
        return hit
    how_lon, how_lat = {"degrees": ("deg", "deg"), "hours_degrees": ("hours", "deg"),
                        "sexagesimal_hours_degrees": ("hms", "dms"), "sexagesimal_degrees": ("dms", "dms")}[decl["format"]]
    lon = read_field(L, decl["lon"], how_lon, -360.0, 360.0)
    lat = read_field(L, decl["lat"], how_lat, -90.0, 90.0)
    angle = sigma = None
    if decl["angle"]:
        angle = number_field(L, decl["angle"]["column"], decl["angle"]["unit"])
        if decl["angle"]["sigmaColumn"]:
            sigma = number_field(L, decl["angle"]["sigmaColumn"], decl["angle"]["unit"])
    c = Converted(L, decl, lon, lat, angle, sigma)
    L.conv[key] = c
    while len(L.conv) > 2:
        L.conv.popitem(last=False)
    return c


def parse_report(c: Converted, L: Loaded) -> dict:
    """What happened to every row, for the person to read. Counts are exact over the whole table; samples are the first few
    unreadable values with their line. A row is placed when both coordinates were read; an empty cell is missing, not unreadable."""
    n = len(L.table)
    unreadable = c.lon.bad | c.lat.bad
    missing = (c.lon.gone | c.lat.gone) & ~unreadable
    samples = sorted(c.lon.samples + c.lat.samples, key=lambda x: x["row"])[:MAX_SAMPLES]
    rep = {"rows": n, "placed": int(c.valid.sum()), "unreadable": int(unreadable.sum()), "missing": int(missing.sum()),
           "samples": samples, "lon": c.lon.report(), "lat": c.lat.report()}
    if c.angle is not None:
        rep["angle"] = c.angle.report()
    if c.sigma is not None:
        rep["sigma"] = c.sigma.report()
    return rep


# ---------------------------------------------------------------------------------------------------------------- the ops

def _need_int(req: dict, key: str, default: int, low: int, high: int) -> int:
    v = req.get(key, default)
    if isinstance(v, bool) or not isinstance(v, int) or not low <= v <= high:
        raise Bad(f"{key} must be an integer in [{low}, {high}]")
    return v


def op_describe(path: str, req: dict) -> dict:
    L = load(path)
    t = L.table
    cols = [t[n] for n in t.colnames]
    preview = [[clean(cell(c, i), 64) if isinstance(cell(c, i), str) else cell(c, i) if is_scalar(c) else None for c in cols] for i in range(min(PREVIEW_ROWS, len(t)))]
    return {"format": L.fmt, "rows": len(t), "columns": column_info(t), "preview": {"columns": list(t.colnames), "rows": preview},
            "read": {"skippedTotal": L.skipped_total, "skipped": L.skipped, "headerless": L.headerless, "notes": L.notes},
            "proposal": propose(L),
            "limits": {"fileBytes": MAX_FILE_BYTES, "rows": MAX_ROWS, "columns": MAX_COLUMNS}}


def op_rows(path: str, req: dict) -> dict:
    L = load(path)
    return rows_slice(L, _need_int(req, "offset", 0, 0, MAX_ROWS), _need_int(req, "limit", 100, 1, PAGE_ROWS))


def op_report(path: str, req: dict) -> dict:
    """How the declaration reads this table: counts of rows placed, missing and unreadable, with the first few unreadable values."""
    L = load(path)
    decl = validate_declaration(req.get("declaration"), L)
    return parse_report(convert(L, decl), L)


def _nullable(a: np.ndarray, lo: int, hi: int) -> list:
    return [None if not math.isfinite(x) else float(x) for x in a[lo:hi]]


def op_positions(path: str, req: dict, to=None) -> dict:
    """Positions (and angles and uncertainties) of rows [offset, offset+limit) in the declared frame, or in the frame `to` when it is
    given. A row whose position cannot be read has null in every field; the report counts all of them over the whole table."""
    L = load(path)
    decl = validate_declaration(req.get("declaration"), L)
    c = convert(L, decl)
    n = len(L.table)
    lo = _need_int(req, "offset", 0, 0, MAX_ROWS)
    hi = min(n, lo + _need_int(req, "limit", 1000, 1, MAX_POSITION_ROWS))
    lon, lat = c.lon_deg[lo:hi].copy(), c.lat_deg[lo:hi].copy()
    angle = None if c.angle_deg is None else c.angle_deg[lo:hi].copy()
    frame, equinox = decl["frame"], decl["equinox"]
    note = None
    if to is not None:
        lon, lat, angle = transform(lon, lat, angle, decl, to, c.decl["angle"]["quantity"] if decl["angle"] else None)
        frame, equinox = to["frame"], to.get("equinox")
        if angle is not None:
            note = "angles are position angles east of north in the new frame, carried across with astropy"
    out = {"frame": frame, "equinox": equinox, "offset": lo, "count": hi - lo, "total": n,
           "row": list(range(lo + 1, hi + 1)), "lon": _nullable(lon, 0, hi - lo), "lat": _nullable(lat, 0, hi - lo),
           "report": parse_report(c, L)}
    if decl["angle"]:
        out["angle"] = None if angle is None else _nullable(angle, 0, hi - lo)
        out["angleUnit"] = "deg"
        out["angleQuantity"] = decl["angle"]["quantity"]
        out["angleFrame"] = "sky"
        out["angleAxial"] = decl["angle"]["quantity"] in AXIAL_QUANTITIES
        if c.sigma_deg is not None:
            out["sigma"] = _nullable(c.sigma_deg, lo, hi)
        if note:
            out["note"] = note
    return out


def check_target(to) -> dict:
    if not isinstance(to, dict) or to.get("frame") not in FRAMES:
        raise Bad("frame: choose icrs, fk5, fk4 or galactic")
    eq = to.get("equinox") or None
    if to["frame"] in ("fk5", "fk4"):
        want = "J" if to["frame"] == "fk5" else "B"
        if not isinstance(eq, str) or not _EQUINOX.match(eq) or eq[0] != want:
            raise Bad(f"equinox: {to['frame'].upper()} needs an equinox such as {want}{'2000' if want == 'J' else '1950'}")
    elif eq:
        raise Bad(f"equinox: {to['frame']} has no equinox")
    return {"frame": to["frame"], "equinox": eq}


def transform(lon, lat, angle, decl, to, quantity):
    """Positions in the target frame through SkyCoord.transform_to, and axial angles carried with them (orientation.rotate_pa_frame).
    Points whose position is NaN stay NaN. The source frame is the declared one; nothing is assumed."""
    from astropy.coordinates import SkyCoord
    import astropy.units as u
    from orientation import rotate_pa_frame, wrap_axial
    src = astropy_frame(decl["frame"], decl["equinox"])
    dst = astropy_frame(to["frame"], to.get("equinox"))
    ok = np.isfinite(lon) & np.isfinite(lat)
    olon, olat = np.full(lon.shape, np.nan), np.full(lon.shape, np.nan)
    oang = None if angle is None else np.full(lon.shape, np.nan)
    if ok.any():
        sc = SkyCoord(lon[ok] * u.deg, lat[ok] * u.deg, frame=src)
        tc = sc.transform_to(dst)
        sph = tc.spherical
        olon[ok], olat[ok] = sph.lon.deg % 360.0, sph.lat.deg
        if angle is not None:
            a = angle[ok]
            rot = rotate_pa_frame(sc, np.where(np.isfinite(a), a, np.nan), dst)
            oang[ok] = wrap_axial(rot) if quantity in AXIAL_QUANTITIES else rot
    return olon, olat, oang


def snr_filter(value, sigma, k: float):
    """The one row filter for catalogs: keep a row when value / sigma >= k, or value >= k when `sigma` is None.
    `value` and `sigma` are float arrays (NaN = missing or unreadable, never read as 0). Returns (keep, status): `keep` is a
    bool array; `status` is an object array with "" for a kept row, "below" for a known value under k, and "missing" when the
    value (or sigma) is missing or sigma <= 0 (no ratio exists): such a row does not pass. Callers map the status to their own wording."""
    v = np.asarray(value, dtype=float)
    if sigma is None:
        known = np.isfinite(v)
        stat = v
    else:
        sg = np.asarray(sigma, dtype=float)
        with np.errstate(all="ignore"):
            known = np.isfinite(v) & np.isfinite(sg) & (sg > 0)
            stat = np.where(known, v / np.where(known, sg, 1.0), np.nan)
    with np.errstate(invalid="ignore"):
        keep = known & (stat >= k)
    status = np.full(v.shape, "", dtype=object)
    status[~known] = "missing"
    status[known & ~keep] = "below"
    return keep, status


def handle(op: str, path: str, req: dict):
    """(head fields, payload bytes). The payload is a JSON document: the head line of a worker reply is read into a small buffer."""
    if op == "catalog_describe":
        doc = op_describe(path, req)
    elif op == "catalog_rows":
        doc = op_rows(path, req)
    elif op == "catalog_report":
        doc = op_report(path, req)
    elif op == "catalog_positions":
        doc = op_positions(path, req)
    elif op == "catalog_transform":
        doc = op_positions(path, req, to=check_target(req.get("to")))
    else:
        raise Bad("bad request")
    payload = json.dumps(doc, allow_nan=False, separators=(",", ":")).encode()
    return {"ok": True, "op": op, "nbytes": len(payload)}, payload


OPS = ("catalog_describe", "catalog_rows", "catalog_report", "catalog_positions", "catalog_transform")
