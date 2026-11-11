# ADR 0044: Point catalogs are datasets of their own kind, with declared columns

- Status: proposed; implemented, awaiting owner review
- Date: 2026-11-11
- Related: ADR 0038 (declared units), 0042 (scene layers, angle frame and quantity), roadmap Stage S

## Context
ADR 0042 plans a `catalog` overlay: star polarization vectors, absorption sightlines and other tables of sources drawn over a map and compared with its orientations. A table of sources is not an image. It has no pixel grid, no WCS in a header, no BUNIT, and in the owner's real files no declared frame, units or meaning at all:
- starlight polarization tables: tab-separated, a `#`-commented header (`Star RA(J2000) DEC(J2000) P(%) sP(%) PA(deg) sPA(deg)`), RA as `h m s`, Dec as `d m s`, rows with trailing empty fields; the angle is east of north (IAU) and parallel to B, which the file does not say;
- foreground star tables: one with no header line (columns known only from the author), one in IPAC form with Galactic `glon glat`;
- absorption sightline tables from papers: CSV, FITS table or VOTable.

Until now every dataset was a FITS image, and `datasets.header` held a FITS header. A catalog has to be registered, listed, authorised, declared and made stale like one, without a fake FITS header.

## Decision
### 1. A dataset has a kind
`Dataset.kind` is `fits` (everything before this ADR) or `catalog`. It is derived from the stored header: a catalog's header is `{kind: "catalog", format, rows, columns[{name, dtype, unit, scalar}]}`; the rows stay in the file (invariant: scientific data in files, not in SQLite). The contract (`bodies.json`) types `header` as `FitsHeader` or `CatalogHeader` and `kind` is the discriminator, so the web client must narrow before reading an axis or a BUNIT.

Operations that need a pixel grid (view, plane, coord, spectrum, axial statistics, orientation and unit declaration, analysis, comparison, regions, figures) refuse a catalog with 409. Operations on a catalog refuse a FITS dataset with 409. The picker lists for comparison, reliability maps and analysis offer images only.

### 2. Reading a table (worker, `catalog_table.py`)
Only validated providers read bytes: `astropy.io.ascii` (CSV, TSV, whitespace text, ECSV, IPAC), `astropy.io.votable`, `astropy.io.fits` (first table extension). There is no parser written here. The format is decided from the first bytes; the file name only separates the plain-text kinds.

Bounds, all checked before anything large is allocated:

| Limit | Value |
|---|---|
| file size | 50 MB (refused, not sampled) |
| rows | 2,000,000 |
| columns | 256 |
| column name | 64 characters |
| text cell sent to the client | 256 characters (64 in the preview) |
| rows per page | 500 |
| positions per request | 100,000 |
| sexagesimal values converted per catalog | 200,000 (astropy parses them one at a time; larger catalogs need decimal degrees) |

Refused outright: compressed files (gzip, bzip2, xz, zip: expansion is not bounded by the file size), files with NUL bytes that are not FITS, and VOTables that declare a DTD or entity, take a stream from a URL, use a gzip stream, include other documents, or declare an `arraysize` over 65,536 or `nrows` over the row cap. The VOTable is parsed from bytes already in memory, so astropy never opens a path or a URL.

Lines that are not rows (wrong number of fields) are left out, counted and listed with their line number; they are never repaired. Trailing empty fields are padding, not data. A commented header (`#Star ...`) is recognised when its names match the data's field count. A file with no header row gets `col1, col2, ...` and a note, and no proposal. Empty cells are missing (null), never 0; a masked or NaN value in a FITS or VOTable is null too. A column of arrays is listed and cannot be used.

Units are reported only when the file declares them (ECSV meta, VOTable `unit`, FITS `TUNITn`).

The worker keeps the two most recently parsed tables (key: path, size, modification time) so paging does not parse again. All catalog operations run on the heavy lane, so scrubbing a channel is never delayed by a table.

### 3. The declaration (researcher-declared, never assumed)
A new overlay table `catalog_declarations` (schema 9) stores one declaration per catalog dataset, with the file's size and modification time at the time, so a changed file makes it stale exactly as in ADR 0038. A stale declaration is shown, not applied: positions are refused until the person confirms it again.

A declaration names:
- `lon`, `lat`: columns;
- `format`: `degrees`, `hours_degrees` (longitude in decimal hours), `sexagesimal_hours_degrees` (h m s and d m s) or `sexagesimal_degrees`;
- `frame`: `icrs`, `fk5`, `fk4` or `galactic`, and `equinox` (required for fk5 as `J...` and fk4 as `B...`, refused otherwise). Galactic longitude is never in hours. The frame is required; there is no default;
- optional `angle`: `column`, `unit` (`deg` or `rad`, required), `frame` (always `sky`: a catalog has no pixel grid), `quantity` (the vocabulary of ADR 0042: `b_field`, `gradient`, `dust_polarization`, `starlight_polarization`, `unspecified`, or not declared), and `sigmaColumn`;
- optional `values`: up to 8 columns to show beside each source.

"Sky" for a catalog angle means a position angle east of north in the catalog's own declared frame, the same sense ADR 0042 gives an orientation map. Every quantity in the vocabulary is an orientation, so a declared angle is reported in [0, 180); an angle whose quantity is not declared or is `unspecified` is reported as given (converted to degrees only).

The API validates the declaration against the table's columns and lists every problem (400 with `reasons`). The worker validates it again on use.

### 4. Proposals
From the column names (RA/DEC with J2000 or 1950, GLON/GLAT, lon/lat, PA, sPA and variants) and a few values (sexagesimal strings; a numeric longitude above 24 cannot be hours) the worker proposes columns, format, frame, equinox, angle column, unit (from `(deg)` in the name or the file's unit) and an uncertainty column. Each proposed field carries its basis, the whole is labelled "proposed from the column names", and a name that does not settle a field leaves it empty with a note: `lon`/`lat` do not say a frame; `RA`/`DEC` without an epoch do not; a longitude column whose values are all 24 or less could be hours or degrees; the quantity of an angle is never proposed. A proposal is never stored. The form fills from it only when the person asks, and they still choose and confirm.

### 5. Sky positions and frame changes
`catalog_positions` returns longitude and latitude in degrees in the declared frame, the angle and its uncertainty in degrees, and the parse report. Sexagesimal values are parsed with `astropy.coordinates.Angle` (hour angle for right ascension); longitudes are checked in hours first and wrapped into [0, 360); latitudes must lie in [-90, 90]. A value that cannot be read is counted with its row, line, column, the value (at most 40 characters) and the reason, and has null in every output field. Missing (empty) is counted apart from unreadable. Counts are exact over the whole table; the list of samples is the first 50.

`catalog_transform` converts positions to another frame with `SkyCoord.transform_to`, and carries an orientation angle with its position through the same transform (`orientation.rotate_pa_frame`): a position angle is a direction at a point, and north differs between frames. FK5 and FK4 need an equinox in the request too.

### 6. Registration and discovery
`POST /api/datasets` sends a file named like a table (`.csv .tsv .ecsv .dat .txt .tbl .cat .xcat .vot .votable`) to the catalog reader, and a FITS file with no image axes is offered to it as well; if the worker cannot read a table from it, it stays a FITS dataset as before. `POST /api/catalogs` registers any file as a catalog. Both resolve the path through the same source-root check as FITS registration. Re-registering after the file changed refreshes size and modification time and so makes the declaration stale. Remote hosts are not supported for catalogs in this slice.

Project discovery offers catalogs instead of "files of other disciplines without a viewer": `.ecsv` and `.vot` anywhere, and `.csv .tsv .dat .txt .tbl .cat` only inside a folder the researcher declared as data, and only when the first lines look like a table (nearly every line has the same number of fields). A README beside the tables is not offered. A project scan registers the catalogs it offers, as it does FITS files.

## Alternatives
- **Store the catalog's rows in SQLite.** Faster paging, but copies scientific data and makes the file a second source of truth. The worker cache gives the speed.
- **Infer the frame, the unit or the angle quantity from the names and register the guess.** Starlight tables say `RA(J2000)` and nothing else; the angle's relation to B is in a README. A silent guess is exactly the plausible-looking wrong answer ADR 0042 is about.
- **A hand-written CSV or sexagesimal reader.** More code to audit, and astropy already handles delimiters, units and sexagesimal ranges.
- **Fake a FITS header for a catalog.** Every FITS-only surface would then fail in confusing ways. A kind with a typed header makes the refusal explicit.

## Consequences
- Schema 9 adds `catalog_declarations`. Existing catalogs get the empty table; no row changes. Datasets deleted take their declaration with them; the impact preview names it.
- The web client's `Dataset` is a union on `kind`; code that reads a header narrows first.
- Later overlay slices read `catalog_positions` and `catalog_transform` instead of parsing tables themselves.
- Catalogs over 200,000 sexagesimal positions are refused for positions (decimal degrees work up to the row cap).
- Not covered: remote catalogs, catalog row filtering and sorting, proper motion and epoch propagation (the declared frame's equinox is not an observation epoch), per-row frames, FITS tables with several table extensions (the first is read), array columns.

## Validation
- Worker tests: each format; sexagesimal hours against degrees and decimal hours; a Galactic IPAC table against `astropy` transforms (positions and the carried angle); malformed lines reported with line numbers; caps refuse; a hostile VOTable (entity expansion, external entity, huge arraysize, `nrows`, URL stream, gzip stream) is refused before parsing; empty cells are null and never 0; declaration validation; proposals say what they come from.
- Go tests: registration authorisation (outside root, symlink, relative path), declaration validation with reasons, staleness after the file changes and after re-registration, withdrawing, cascade on delete, the migration from schema 7, FITS-only routes refusing a catalog and the reverse, a FITS table registered as a catalog and an image still an image, responses conforming to the OpenAPI spec.
- Web tests: the inspector (columns and units as read, labelled proposal, equinox only for fk5/fk4, reasons from a refusal, parse report with lines, stale declaration) and the paged table.
- Measured: a 47 MB CSV of 1.4 million rows loads in 4.4 s at a 775 MB peak, and a report and a 100,000-row transform each take well under a second once it is loaded. Cells are shown as astropy reads them (numbers are numbers: `00123` shows as 123); the file is the record of the original text.
- Not verified: a design review of the new inspector sections and the table (the workbench direction is approved, ADR 0005, but these screens have had no screenshot or keyboard review at the three viewports);.

## Rollback / Migration
- `catalog_declarations` is additive. An older program refuses a catalog with schema 9 ("newer than this program"), and the pre-migration backup written by the guard restores schema 7.
- Registered catalogs are plain references: removing the feature leaves rows in `datasets` that an older program would try to read as FITS headers and refuse. Archive or delete them first.
