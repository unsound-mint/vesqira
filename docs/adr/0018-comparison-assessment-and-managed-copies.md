# ADR 0018: Comparison inspector, assessment, data nature, beam, managed copies

- Status: accepted under delegated merge, 2026-11-04; owner review pending
- Date: 2026-11-04

## Context
Checking the work against the project's context supplement found gaps: comparing two orientation maps needs an axial difference and a statement of what differs; resolution (beam) was invisible; synthetic controls could look like observations; a finished run said nothing separate about scientific QC; registration gave no preview and no managed-copy option; some failure cases lacked fixtures.

## Decision
- **Axial difference.** `analysis.py axial_diff` computes input minus reference modulo 180 degrees, in (-90, 90], for two single-plane maps on the same grid. 1 and 179 degrees differ by 2. A pixel without a value in either map is NaN. Statistics use the doubled-angle method (mean difference, resultant length, RMS, fraction within 10 degrees) and the mean is undefined when the offsets cancel. The API requires both maps to be declared axial (with units) and the grids to be confirmed identical; otherwise it refuses with the reasons. The result is a new scalar dataset with a derived record.
- **Comparison inspector.** Shows both files' treatment (scalar or axial, who declared it), brightness unit, pixel scale, beam, run, code version (dirty is not reproducible), channel, valid-pixel counts (with the reminder that a coverage change is not better agreement), the run settings that differ, and grid compatibility. It offers the axial difference when applicable and, when the grids differ, a proposed alignment with its caveats (interpolation, no resolution matching, NaN outside the footprint).
- **Beam is not pixel scale.** The header worker reports BMAJ, BMIN and BPA (degrees) as declared or unresolved; the inspector shows pixel scale and beam separately and never derives one from the other.
- **Nature of the data.** The user declares observed, simulated or constructed control. It is stored apart from the file, shown in figure captions and the reviewer's provenance statements ("not an observation").
- **Assessment is separate from computation.** Every run has a review with scientific QC (not assessed, passed, failed, inconclusive), interpretation (not interpreted, supports, challenges, inconclusive) and a note, defaulting to not assessed. It travels with figures; nothing in the product says "valid".
- **Preview and managed copies.** `GET /api/preview` reports shape, planes, plane size, whether sky and spectral axes are declared and what reading costs, without recording anything. A managed copy is a byte-for-byte, read-only copy under the data directory identified by its SHA-256, recorded as derived from the original, which remains the reference; copies above 64 GiB are refused; files on hosts are never copied here.
- **Fixtures.** A reversed spectral axis and a PC-rotated WCS were added, with an independent TAN oracle for the rotated case and a reversed-axis moment test.

## Consequences
The acceptance pack mentioned in the supplement is not in the repository; none of its cases has been run and none is claimed.

## Validation
Worker and analysis tests (modulo-180 difference, NaN stays NaN, unit conversion, refusal for non-maps; beam declared vs unresolved; reversed axis; rotated WCS against an independent formula), Go tests (axial difference needs declared maps and a shared grid, preview, managed copy identity and read-only mode, nature in provenance, review defaults and reviewer visibility), unit tests for the inspector and resolution text, and the browser walk-through.

## Rollback / Migration
Drop the `dataset_nature` and `run_review` tables and the new routes.

## Amendment: raw header cards

`GET /api/datasets/{id}/header-cards` returns the primary header as the file states it, one 80-column card per entry (the card's own image, control characters replaced, at most 3000 cards), read by the worker inside the source roots. `?format=text` serves it as `<name>.header.txt`. The dataset inspector shows it under "Raw header cards", loaded when opened. Nothing in it is interpreted; the readouts elsewhere remain declared or unresolved.

## Amendment: the resolution of a VGT map (issue 119)
A VGT product carries no `BMAJ` on purpose: its resolution is the input cube's beam widened by a Gaussian smoothing of the Stokes-like Q and U (sigma `VGTKER`, FWHM `VGTKFWHM`, both in input pixels) and by a `VGTDN` x `VGTDN` pixel gradient window, which is a top-hat and not a Gaussian. Status: implemented.
- **What is established.** The worker reads `VGTKFWHM`, `VGTDN` and the input beam `INBMAJ`, `INBMIN`, `INBPA`. The formula is the one the issue gives and is exact for Gaussians: along each axis, input beam and kernel FWHM (kernel FWHM times the pixel scale, which must be the same on both axes, since a width in pixels is one width in arcseconds only then) combine in quadrature, `sqrt(b^2 + k^2)`, with the input position angle. The window is stated beside it (side in pixels and arcseconds) and not included, so the result is *declared by the file's keywords and approximate*, never verified. Pixel scale and resolution stay separate: VGT keeps the input grid while lowering the resolution.
- **What is not established.** Neither the issue nor VGT.jl's documentation (checked at HEAD, 2026-11-12) states the unit of `INBMAJ`/`INBMIN`, and the current VGT.jl source does not write the `INB*` cards. A FITS card has no unit field, so Vesqira accepts the unit only when the card's comment names exactly one of arcsec, arcmin or deg; otherwise it does not guess, and the effective resolution is reported as "not established" with the reason. The same holds when `VGTKFWHM`, the input beam or a square pixel scale is missing. `INBMIN` is assumed to share `INBMAJ`'s unit.
- **Convolve.** The file's own beam is never overridden: a file with `BMAJ` or `BMIN` refuses a supplied source resolution. For a file with none, convolution starts from a source resolution that the researcher either accepts (basis `vgt_approximate`; the server fills the numbers from the file's own keywords, ignoring any the client sent, and refuses when the approximation is not established) or enters (basis `user`). The basis, the numbers and an "approximate" flag are in the result's summary and policy sentence and in the derived record's options; the result's beam is the target, reached only as well as the starting resolution is true. Host jobs accept the same options.
- **Validation.** Worker: the quadrature numbers against an independent computation; an unstated unit, a missing input beam and non-square pixels are not established; a file with a beam is not overridden; the result carries no producer cards. Go: the server replaces client numbers with the file's, refuses an unestablished approximation, a missing basis and any operation but convolve.
