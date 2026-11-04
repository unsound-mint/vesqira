# ADR 0026: Saved regions and the DS9 region format

Status: accepted under delegated merge; owner review pending.

## Context
A region could be drawn or typed but not kept, named, edited or taken to another tool.

## Decision
- **Saved regions** belong to a dataset: a name (up to 80 characters), and a rectangle (inclusive pixel bounds) or a circle (centre pixel and radius), in whole 0-based pixels, the shapes and pixel-centre rule the worker already uses for spectra and statistics. At most 200 per dataset. They can be created, renamed, given a new shape, duplicated, hidden on the image and deleted (the viewer offers an undo that recreates it). Server: `GET/POST /api/datasets/{id}/regions`, `POST /api/regions/{id}`, `POST /api/regions/{id}/delete`. Shapes are validated; nothing is clipped to the image at save time, the worker clips and reports it as before.
- **Format: DS9 `.reg`.** It is the exchange format of the field and is read by DS9, CASA and astropy. `GET /api/datasets/{id}/regions.reg` (optionally `?id=`) writes it in **image coordinates** (1-based pixel centres), where a box of whole pixels and a circle select exactly the pixels they select here, so nothing is approximated and no sky position is claimed. Other formats (CRTF and others) are not written.
- **Import** is not built yet. astropy's `regions` package is the usual parser but is not packaged for Fedora, so the installed RPM cannot depend on it; a small reader limited to boxes and circles, with a preview that states what is exact, approximated or skipped, is the planned route.

## Alternatives
Storing regions as sky shapes (needs a declared WCS, and a sky region is not exactly representable in whole pixels); free-form polygons first (the statistics and spectrum code takes boxes and circles).

## Consequences
Implemented: save, edit, delete, undo, show/hide, DS9 export. Not implemented: import, polygons and ellipses, dragging handles on a saved region (edit by numbers or draw again and use "Use current shape").

## Validation
Store and API tests (validation, edit, delete twice, other dataset's list, the limit, DS9 centres and sizes by hand); hook tests through fake routes; a browser walkthrough saves, renames, duplicates, deletes, undoes and fetches the file with axe clean; the panel was inspected in a screenshot.

## Rollback/Migration
One additive table.
