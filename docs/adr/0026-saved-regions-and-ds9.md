# ADR 0026: Saved regions and the DS9 region format

Status: accepted under delegated merge; owner review pending.

## Context
A region could be drawn or typed but not kept, named, edited or taken to another tool.

## Decision
- **Saved regions** belong to a dataset: a name (up to 80 characters), and a rectangle (inclusive pixel bounds) or a circle (centre pixel and radius), in whole 0-based pixels, the shapes and pixel-centre rule the worker already uses for spectra and statistics. At most 200 per dataset. They can be created, renamed, given a new shape, duplicated, hidden on the image and deleted (the viewer offers an undo that recreates it). Server: `GET/POST /api/datasets/{id}/regions`, `POST /api/regions/{id}`, `POST /api/regions/{id}/delete`. Shapes are validated; nothing is clipped to the image at save time, the worker clips and reports it as before.
- **Format: DS9 `.reg`.** It is the exchange format of the field and is read by DS9, CASA and astropy. `GET /api/datasets/{id}/regions.reg` (optionally `?id=`) writes it in **image coordinates** (1-based pixel centres), where a box of whole pixels and a circle select exactly the pixels they select here, so nothing is approximated and no sky position is claimed. Other formats (CRTF and others) are not written.
- **Import.** astropy's `regions` package is the usual parser but is not packaged for Fedora, so the installed RPM cannot depend on it; a small reader in Go handles DS9 files in image or physical coordinates: boxes with no rotation and circles. A box always maps exactly (the pixels whose centres lie inside it). A circle is exact when its centre is a pixel centre and its radius selects the same pixels as the whole-pixel radius; otherwise it is *approximated* and the note states the rounding. Sky coordinates, other shapes, rotated boxes and excluded regions are *skipped* with the reason. `POST /api/datasets/{id}/regions/import` previews by default; applying saves the exact regions and the approximated ones only when asked. Export followed by import returns the same regions.

## Alternatives
Storing regions as sky shapes (needs a declared WCS, and a sky region is not exactly representable in whole pixels); free-form polygons first (the statistics and spectrum code takes boxes and circles).

## Consequences
Implemented: save, edit, delete, undo, show/hide, DS9 export and import (image coordinates). Sky-coordinate import is described in the amendment below. Not implemented: polygons and ellipses, dragging handles on a saved region (edit by numbers or draw again and use "Use current shape").

## Validation
Store and API tests (validation, edit, delete twice, other dataset's list, the limit, DS9 centres and sizes by hand); hook tests through fake routes; a browser walkthrough saves, renames, duplicates, deletes, undoes and fetches the file with axe clean; the panel was inspected in a screenshot.

## Rollback/Migration
One additive table.

## Amendment: sky-coordinate import

DS9 files written in sky coordinates (the usual case when DS9 has a WCS) are now read. Circles and unrotated boxes in `fk5`, `icrs`, `j2000`, `galactic` or `wcs` (the file's own frame) are placed with the dataset's WCS by the scientific worker (`sky2pix`, using astropy, never a re-implemented projection). Right ascension in sexagesimal is read as hours, declination and galactic longitude as degrees; sizes take `"`, `'`, `d` or `r`, and a size without a unit mark is read as arcseconds (DS9's default for sky coordinates) and the preview says so. A sky shape is never an exact whole-pixel shape, so every placed region is *approximated*, with a note giving the rounding; a box is accepted only when north is within half a degree of the image's vertical axis (either way up) and the box has no angle. A region is *skipped* with the reason when its frame differs from the dataset's declared frame (no frame transformation is done here), when the dataset does not declare a frame or has no celestial WCS, when the position does not map onto the image, when the image is rotated against the sky, for FK4, B1950 and ecliptic systems, and for other shapes. FK5 and ICRS are treated as the same frame (they differ by about 0.02 arcsec) and the note says so. Circle radii use the mean pixel scale at the reference pixel; the note says when the pixels are not square.
