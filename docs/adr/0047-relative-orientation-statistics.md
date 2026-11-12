# ADR 0047: Relative orientation statistics

- Status: proposed; implemented, awaiting owner review (bins of a third map: amendment, issue #132)
- Date: 2026-11-11
- Related: ADR 0042 (declared angle frame and quantity, axial resampling), 0044 (point catalogs), roadmap Stage S

## Context
The owner compares VGT orientation maps with starlight polarization and with Planck dust polarization using the group's own scripts. This ADR follows the method of three of them, described here so a reader does not need the scripts:
- A map-against-map comparison script:
  - the alignment measure AM = ⟨cos 2Δψ⟩ with headless angles;
  - star angles carried into the map's pixel frame;
  - Planck handling: it asserts `POLCCONV = COSMO`, flips U to IAU, takes θ_B = ½ atan2(U, Q) + 90°, averages Q and U over blocks rather than angles, and cuts on P/σP.
- A star-comparison script:
  - VGT averaged axially in a disk around each star;
  - a shifted-pattern null, where the whole star pattern moves across the map and the angles stay;
  - defaults: a 10 arcmin radius, 3000 shifts and seed 20261020.
- A scale-analysis script: weighted AM, a permutation null, a bootstrap 16-84% range, and weights 1/max(σ_PA, 1°)².

ADR 0042 made the angle's frame and quantity declared facts, and ADR 0044 made catalogs datasets with a declared frame and angle. What is still missing is the comparison itself, with the group's chance level and numbers that come out the same on every run.

## Decision

### Two modes, one analysis
`POST /api/analysis/relative-orientation` takes `datasetId` and `otherId`:
- `datasetId` is a declared orientation map;
- `otherId` is a point catalog with a declared angle (mode A, map vs catalog) or a second declared orientation map (mode B, map vs map).

The worker is `services/scientific-worker/relorient.py`, run through `analysis.py` (`relorient_catalog`, `relorient_maps`). It runs on this computer under the analysis timeout, and it is killed with its process group when the request ends.

The result is kept like every analysis result: a registered file with a derived record that holds the summary, the parameters, the sources and the software versions.
- Mode A registers the per-source CSV as a catalog dataset.
- Mode B registers a FITS map of |Δψ| in degrees, plus a per-pixel CSV. The CSV is written only up to 1,000,000 used pixels, because a full map would exceed the catalog limits; the FITS keeps every value.

`GET /api/datasets/{id}/relative-orientation.csv` downloads the table. It serves only a file this program wrote, named in the result's own record.

No table was added, so there is no schema change and no schema 13.

### Both sides become the B-field orientation, or the request is refused
The ADR 0042 rules apply: a gradient or dust polarization is rotated by +90°; starlight polarization and the B-field are not rotated.
- A missing frame, a missing quantity, an `unspecified` quantity, a stale declaration, or a catalog with no angle column is refused, and the refusal names what is missing. The API checks first and the worker checks again.
- A catalog angle is carried from the catalog's declared frame into the map's celestial frame with its position (`orientation.rotate_pa_frame`), and then into the map's pixel frame (`sky_pa_to_pixel`).
- A sky-frame map is carried into its pixel frame pixel by pixel. Δψ is taken in the map's pixel frame, as the owner's scripts do.
- The table also gives both angles east of north in the map's celestial frame.

### Definitions
| Quantity | Definition |
|---|---|
| Δψ | ψ_map − ψ_other folded into [−90°, 90°). The relative orientation is \|Δψ\| ∈ [0°, 90°], so 1° and 179° are 2° apart |
| AM | mean of cos 2Δψ: 1 parallel, 0 no preferred relative orientation, −1 perpendicular |
| Weighted AM | Σ w cos 2Δψ / Σ w. Mode A uses w = 1/max(σ, 1°)², σ in radians, from the catalog's declared uncertainty column; the 1° floor keeps a very precise source from dominating. Mode B uses w = the product of the two declared reliabilities, 1 for a map without one |
| Projected Rayleigh statistic (mode B) | V = Σ w cos θ / sqrt(Σ w² / 2) with θ = 2Δψ. This is Jow et al. 2018 (MNRAS 474, 1018; arXiv:1708.04063), eq. 8, and eq. 6 when every w is 1; eq. 2 defines the relative angle in [−90°, 90°] and the doubling. The paper writes Z_x (eq. 6) and Z*_x (eq. 8) and introduces it as the V statistic of Durand & Greenwood (1958), renamed the projected Rayleigh statistic; V here follows that origin. Positive means preferentially parallel. It is asymptotically N(0, 1) only for independent angles |
| Histogram | \|Δψ\| in equal-width bins over [0°, 90°]: 9 bins of 10° by default, 2 to 90 bins as a parameter. Every bin is the same width, so no split needs a separate reason |
| Bootstrap (mode A) | 16th to 84th percentile of AM over 2000 resamples with replacement |

### Mode A sampling (map at each source)
- The map is averaged with the doubled angle over a disk:
  - The disk is centred on the pixel nearest the source.
  - Pixels count when their centres lie within the radius, measured as angular distance between pixel-centre unit vectors from astropy `pixel_to_world`. The group's star-comparison script measures pixel distance instead, with the radius in pixels taken from the mean pixel scale.
  - NaN pixels are left out.
  - Each pixel is weighted by the map's declared reliability. Without one the mean is unweighted, and the result says which.
- Kept from the script:
  - the search box half-width ri = ceil(r / mean pixel scale);
  - a box that leaves the map excludes the source;
  - fewer than 5 usable pixels excludes the source. The minimum is a parameter.
- Added:
  - A disk whose doubled-angle resultant is below 1e-6 has no mean and is excluded. The script would take atan2(0, 0) = 0.
  - The summary counts disks that reach the edge of the search box, which happens where the local pixel scale is smaller than the mean.
- Every catalog row is accounted for. Each excluded row is counted with its reason (position, angle, no pixel, outside the map, disk at the edge, too few pixels, zero weights, cancelling orientations) and listed in the table.

### Mode A null: the group's shifted pattern, ported step by step
1. n_real is the number of sources used. n_sub = int(keep · n_real), with keep = 0.9 by default.
2. For each of the N shifts (3000 by default), draw dx = uniform(−nx, nx), then dy = uniform(−ny, ny). Redraw until hypot(dx, dy) ≥ the minimum shift in pixels, which is 1° divided by the mean pixel scale.
3. Wrap positions as (x + dx) % nx and (y + dy) % ny. Each source keeps the pixel-frame angle it had at its true position.
4. Every source with a pixel position and an angle takes part, including sources outside the map, which the wrap brings in.
5. A shift is kept when at least n_sub shifted sources have a usable disk. It is then scored on exactly n_sub of them, drawn without replacement. A rejected shift is not redrawn.
6. chance95 = the 95th percentile of the kept AMs (117). The one-sided p = (#kept AM ≥ observed AM + 1)/(kept + 1) (116).

The random stream is `numpy.random.Generator(PCG64(seed))`, which is the same as the script's `default_rng(seed)`. Draws are consumed in the script's order, so the same seed gives the same null; the test checks this. The bootstrap uses its own stream (`SeedSequence(seed, spawn_key=(1,))`) so that it never shifts the null's sequence.

Known properties, ported unchanged and reported:
- The length test applies to the unwrapped offset (106), so an offset near ±nx is close to no shift once wrapped. The result reports how many kept shifts are shorter than the minimum after wrapping.
- A minimum shift that can hardly be drawn would loop forever in the script. Here it is refused: up front when the shift is longer than the map's diagonal, and after 10,000 draws for one shift.

### Mode B null: shifted map (this ADR's choice; the owner has no script for it)
- The second map moves relative to the first by whole pixels with wrap (`numpy.roll`), and its values are kept. Offsets are drawn as in mode A and rounded.
- The length test applies to the wrapped displacement, so a near-identity shift is never accepted.
  - Why wrap rather than excluding the wrapped edge: wrapping keeps the overlap size independent of the shift. Excluding the edge would shrink the overlap with distance and bias the null toward short shifts. VGT maps usually have NaN borders, so the seam mostly falls in no-data.
- A shift is kept when the overlap of usable pixels is at least keep · n_real. It is scored on every overlap pixel, not on exactly n_sub drawn pixels.
  - With thousands of correlated pixels, the overlap size changes the spread of the null negligibly. Drawing millions of pixels per shift cost about 0.6 s per shift on a 2048 × 2048 map. Scoring every overlap pixel takes under 0.05 s per shift: 200 shifts in about 10 s for the whole run (measured).
- The default is 1000 shifts.

### Correlated pixels
The projected Rayleigh statistic V is shown without a p-value, with a note that beam-scale correlation, and for VGT window-scale correlation, inflate it.

The effective number of independent samples is offered only as the number of beams: pixels used × pixel area / (π/(4 ln 2) BMAJ BMIN), using the larger of the two maps' declared beams. This is the owner's approximation in `figures/fig_fg_template_planck.py:102,110` and its docstring (18-20).
- It is offered only when both headers declare BMAJ and BMIN.
- It is labelled an approximation.
- Its caveat says that a VGT map is correlated over its window, so the count overstates independence.

### Output wording
Every result carries `label: "agreement statistic"` and a policy saying that it is not a validation of either tracer (invariant 9). The screens say "alignment measure" and "agreement". They never say validated, success, right or wrong.

### Screens
- The inspector of a declared orientation map and of a catalog with a declared angle has a "Compare orientation" section.
  - It lists only the counterparts that can be used, and says what is still undeclared.
  - Every parameter is shown with its default, including the seed.
- A result shows:
  - AM and the weighted AM;
  - chance95 and p, with the kept and attempted shifts;
  - the median |Δψ|;
  - the bootstrap range (mode A), or V, the beams and their notes (mode B);
  - samples used and excluded, with reasons;
  - a histogram with the null's 2.5-97.5% band and median per bin;
  - for mode A, a segment figure: the map's orientation solid and in the accent colour, the catalog's dashed and in ink, faded where a source was not used. It is drawn in the map's pixel frame over the map's outline;
  - the CSV link;
  - a disclosure with every definition, the parameters and the method.

**North and east on every angle picture (amendment).** The segments figure is drawn in the map's pixel frame, so an angle east of north means nothing on it without arrows. The worker reads them at the map centre from the file's WCS (`orientation.compass`: the pixel-grid directions of position angle 0 and 90 by `sky_pa_to_pixel`, counter-clockwise from +x with y up, so a rotated, mirrored or flipped WCS turns them; north is never assumed up) and the summary carries them as `map.compass`; the `view` reply carries the same `compass` for the viewer, which draws N and E whenever orientation segments or angle vectors are on screen. With WCS corrections in force (ADR 0061) the arrows rest on the corrected WCS and the reply lists the corrected celestial keywords (`corrected`), so the note says "corrected". Where the file has no celestial WCS (or the result predates this) no arrow is drawn and the legend says why. Exported plane figures (PNG and SVG) draw the same arrows (amendment, below).

**North and east on exported figures (amendment).** A plane figure draws the viewer's compass as an inset in the top-right corner of the picture, with the directions the worker reported at the map centre (`compass.x`, `compass.y`: 0-based pixel centres, y up, kept in the scene); the caption says "at the map centre". It is not drawn on the centre itself, where the source usually is. A picture too small for the inset centres it.
- One geometry: `compassShapes` (`apps/web/src/ui/compassGeometry.ts`) gives the arrow tips, heads and letter positions for the viewer's glyph (`Compass.tsx`), the PNG (`drawCompass`) and the SVG (`compassSvg`). N and E lie at the worker's angles, so a mirrored WCS has east on the right and north is never assumed up. The ink is the figure's ink with a dark halo, and the letters N and E say which arrow is which, so the arrows read on a bright or a dark plane and without colour. The SVG uses attributes only (no style sheet, no external reference).
- With WCS corrections in force (ADR 0061) the arrows rest on the corrected WCS, the picture carries a "WCS corrected" marker next to them, and the caption names the keywords. No celestial WCS, or a compass the worker could not compute, draws nothing and records nothing: there are no placeholder arrows.
- A figure option, "North and east arrows", on in the figure panel whenever the viewer has a usable compass. It is kept in the figure's scene as `scene.compass` (`SceneCompass`: pixel, `northDeg`, `eastDeg`, `frame`, `corrected`), so a board redraw reproduces it. A figure saved before the option has no `compass` in its scene and is drawn again exactly as before, with no arrows; switching the option off also leaves the field out, so the two read the same.
- "Draw again" draws the arrows of a figure that has them from the WCS in force now (a changed correction is already announced by the redraw check as a changed declaration). It says so when the recorded state differs, and when the WCS gives no arrows now it draws none and says why. The new revision records what its picture shows.
- Provenance: the reviewer's provenance has a tier line "north and east arrows (WCS state)", tier `declared` (the file's WCS header, or the researcher's correction), with the pixel, the angles, the frame and the corrected keywords, and a statement that a rotated, mirrored or flipped WCS turns the arrows. The server cannot read the WCS again, so the angles are the worker's reading as reported by the browser; the line says so and does not call them verified. The server refuses a compass whose angles are outside [0, 360), whose keywords are not FITS names, or that carries an unknown key (the same strict reading as the rest of the scene).
- Contract: `Scene.compass` and `SceneCompass` in `packages/contracts/bodies.json`; additive, so scene/1 stays.
- Tests: `figureCompass.test.ts` (geometry for rotation and flip, undefined and unusable compasses, the reference pixel, SVG and canvas output with and without the compass, the marker, saving with the option on and off, drawing again for an old figure, a figure with arrows, a corrected WCS and a WCS without arrows), the shared `scenes.json` fixtures (valid and invalid compasses, read by Go and by the web), and `compassfigure_test.go` (the scene and the provenance lines, refusals).
- Looked at, not reviewed: one SVG of a rotated, corrected compass and one flipped, rendered to PNG with ImageMagick and viewed as a crop. Not done: a figure screenshot in a browser over real data, the compass in a channel-map grid (a grid is panels of raster only and carries no compass), and a hand-placed arrow position (the inset is fixed in the top-right corner; the option switches it off).

### Bins of a third map (amendment, issue #132)
A map-against-map comparison takes an optional `stratifyId`: a scalar map on the same grid. The comparison is then also reported inside bins of it (`summary.stratified`). Without it nothing changes: the request, the numbers and the registered files are those above. A comparison with a catalog does not take it.

- **One Δψ, many cuts.** The worker computes Δψ, the weights and the used-pixel set once, for the whole comparison, so the frame handling (`orientation.sky_pa_in_grid_frame` for a map whose PSIFRAME differs from the grid, the quantity rotations), the reliabilities and the masks are applied exactly once and a bin only chooses which used pixels count. Each bin calls the comparison's own functions on its pixels: `alignment_measure` (AM, and the weighted AM when the comparison is weighted), `projected_rayleigh`, `bootstrap_am` and `beam_count`. Angles are never averaged arithmetically: Δψ is folded into [-90°, 90°) before cos 2Δψ, so a bin holding 1° against 179° has AM = cos 4°, not a perpendicular pair.
- **The third map** is refused with a sentence unless it is one scalar plane on the comparison's grid (`compat.Grids`, the rule the comparison already enforces: align it first otherwise), on this computer, not declared as an orientation map, and not one of the two compared maps. It needs a unit, from its BUNIT or declared by the author (as `scalar_diff` requires); a map with neither is refused rather than binned in an assumed unit. The result states the unit and whose it is (`file`, `user` or `corrected`). A correction in force on the third map (ADR 0061) is applied like for the other two.
- **Bins.** `strataBins` equal-count bins (default 5, 1 to 50) or user-set `strataEdges` (2 to 51 strictly increasing numbers, in the third map's unit); not both. A bin is [lower, upper) and the last is closed.
  - Equal count: the inner edges are the sorted third-map values at ranks i·n/N among the pixels the comparison uses, so every edge is a value the map has. Tied values are never split (equal values share a bin), so a heavy tie gives bins of unequal size or fewer bins than asked; the result says so (`binning.notes`).
  - User edges: pixels outside them are left out and counted.
- **Exclusions are counted.** `counts` gives the pixels the comparison uses, those with no value in the third map, those outside user edges, and those in bins; they add up. Pixels missing or masked in either orientation map were already left out of the comparison and are in its own counts. NaN is missing, never 0.
- **Per bin:** lower and upper edge, centre (the middle of the edges) and the median of the third map, in its unit; the number of pixels; the approximate number of independent beams (null, with the reason, when a map declares no beam); AM, the weighted AM, the projected Rayleigh V (no p-value); the bootstrap 16-84% range of AM, which assumes independent pixels and is therefore optimistic (the number of resamples falls for a large bin, since the cost is resamples × pixels; it is recorded); the median |Δψ|; and the uniform-angle reference: AM = 0 with standard deviation 1/sqrt(2 n) for the bin's n pixels, and for its n independent beams where offered (cos 2Δψ has variance 1/2). An empty bin has null statistics, never 0.
- **No null per bin.** The shifted-map null is a statement about the whole comparison; a subset of its pixels has no null in this slice. The bins are cuts of the same pixels, so their statistics are not independent of each other, and pixels are correlated over the beam.
- **Screen.** "Bin by a third map (optional)" appears under the second map (two maps on this computer only), listing scalar maps that are not orientation maps, not on a host and not archived. The result shows a plot of AM against the third map: each bin as a line over its range at its AM, a dot at the centre, bootstrap whiskers, the ±1σ uniform-angle band, the zero line, the pixel count of each bin and the unit on the axis. Beside it are a table (range, centre, pixels, beams, AM, interval, V, uniform σ) and the exports: figure as SVG and PNG with a provenance record (tool, maps, third-map unit and its source, binning, counts, parameters, result dataset, time) in the SVG's metadata and the PNG's `vesqira-provenance` text chunk, the same helpers and style as the cross-run plot (tokens resolved so the file stands alone), and the table as CSV with a comment header (maps, unit, binning, counts) and an empty cell for what is not available. The plot is linear in the third map; equal-count bins of a skewed map are drawn at their true widths.
- **Record.** The third map is the third `source` of the derived record (after the two maps) and `params.stratifiedBy` names it. `RelativeOrientationRequest.stratifyId`, `RelativeOrientationParams.strataBins` and `.strataEdges`, and `RelativeOrientationSummary.stratified` are additive in `bodies.json`; no table was added and no schema number changes.
- **Tests.** Worker (`test_relorient.py`, class `Strata`): a field aligned at high third-map values and random at low ones, against per-bin hand computations of AM, V, counts, edges, beams and the uniform reference; 1° against 179° and perpendicular in separate bins; equal-count binning on ties (60 zeros in 100 pixels give 60/20/20); missing and outside pixels counted and summing to the comparison's; refusals (another grid, no unit, a declared unit labelled as the author's, an all-missing third map, bad edges or bins, both given, none given); the default of five bins and a deterministic bootstrap; the comparison unchanged without a third map. Go (`relorient_test.go`): refusals by status, a stratified result, the third map as a source, a declared unit, and conformance to the OpenAPI spec. Web (`stratified.test.tsx`): the plot, the table, both exports and the request.
- **Not done.** A per-bin null; a comparison of the orientation built from summed phase maps with a single phase (it needs the individual phase maps, which a registered orientation map does not carry: skipped, not forced); a ratio map such as P/σ_P as a third map (no operation builds one, so P and σ_P are binned as the scalar maps they are, in their own unit); masks of the third map; a third map on a host; binning a catalog comparison by the map at each source; a log axis; owner review of the plot on real data and a browser look at the figure in both themes.

## Alternatives
- **Average the map with a fixed pixel disk, as the script does.** Rejected for membership because the requested radius is on the sky. The centre rounding, the box and the edge rule are kept, so on a regular grid the two give the same disk; the reproduction test relies on this.
- **Compare in the sky frame and keep each source's sky angle in the null.** Holding the sky angle instead of the pixel angle gives a different null wherever north turns across the grid. The script holds the pixel angle, so this does too.
- **A permutation null.** It ignores spatial structure in the map. The shifted pattern keeps it and is what the group reports for stars.
- **Convert V into a p-value with an effective N.** No validated method gives N for VGT's window correlation, so none is offered.
- **Register no file, only a record.** The derived store is keyed by a dataset, and a table people can open and export is the useful artefact.

## Consequences
- Two routes are added and described in `packages/contracts/bodies.json`. The YAML generator now quotes keys that YAML reads as null or a boolean: the summary has a `null` property, and the existing `y` and `n` keys are now quoted too.
- `analysis.go` has one runner for the analysis program (`runAnalysisProgram`), and `analysis_axial.go` one check of a declared reliability map (`reliabilityPath`). Both are reused.
- Measured on one machine, with page cache:
  - A 2048² map against 300 stars, a 30-pixel radius and 3000 shifts took 225 s, at about 1 GB peak resident memory for the run.
  - A 2048² map against a map took about 10 s for 200 shifts, whole run included. 1000 shifts should take under a minute by that rate; this was not measured.
  - Both are inside the 30-minute analysis timeout.

### Not claimed, deliberately
- AM, V, chance95 and p measure agreement between two tracers. They do not say that either tracer is correct, nor that the field is physically aligned.
- The bootstrap range assumes independent sources, and overlapping disks are not independent.
- The beam count is an approximation.
- No significance is stated from a raw pixel count.

### Deferred
- **Source selection by P/σP**: the catalog declaration names no P columns. Filter the catalog before registering it. (Done later: an optional P/σP filter, see the row filter note at the end.)
- **Block (Stokes) averaging before comparison**: use convolve or align first.
- ~~**Planck HEALPix ingestion and the COSMO→IAU U flip**.~~ Implemented by [ADR 0067](0067-planck-healpix-polarization-maps.md):
  - a HEALPix map is registered only when it states POLCCONV and COORDSYS;
  - it is sampled onto the map's grid as Q and U, with the flip and the per-pixel turn from Galactic north;
  - the result is a `dust_polarization` or `b_field` map declared by the operation, which this comparison takes like any other, and its P/σP threshold restricts the comparison.
  An angle map made outside Vesqira still carries no POLCCONV, so for such a map the convention remains unchecked.
- **Remote data.** Both inputs must be on this computer, as for the axial difference and for catalogs (ADR 0044). There is no host job for this analysis yet.
- **Drawing the segments through the scene's catalog overlay.** That overlay is not drawn yet (ADR 0042), so the figure is self-contained and has no raster underlay.
- **A cell or friends-of-friends grouping and a half-split ceiling.**
- **The design gate.** This means three directions and human approval; the owner reviews afterwards, per Stage S.

## Validation
- **Worker** (`test_relorient.py`, with expected values computed independently in the test):
  - Synthetic map plus catalog: aligned sources give AM > 0.98, perpendicular sources AM < −0.98, and random angles give |AM| within 4σ of 0.
  - 1° against 179° counts as aligned.
  - Dust is rotated by 90°, on the map side and on the catalog side; starlight is not.
  - A sky-frame map against a Galactic catalog, with its direction carried by hand in the test, agrees.
  - The disk mean, its pixel count and its resultant match a loop over `SkyCoord.separation`, with reliability weights.
  - **The owner's null:** the script's `compare` and `disk_axial_mean`, transcribed in the test, run on a rotated grid with holes and with sources outside the map. With the same seed they give the same number of used sources, the same AM (to 1e-6), the same number of kept shifts, the same p exactly, and chance95 to 1e-4. The remaining difference comes from the script's 1-arcsec flat-sky angle step against astropy's.
  - The null's p is roughly uniform over 30 unrelated catalogs.
  - The same seed gives a byte-identical summary, and another seed gives a different null.
  - Map mode: AM, the weighted AM, V and the weighted V against direct formulas. V against a hand-computed case and against Monte Carlo N(0, 1) for uniform angles.
  - The beam count, the |Δψ| FITS, and the table row count are checked.
  - An aligned pair has p = 1/(N+1), and an unrelated pair does not.
  - Refusals: an undeclared frame or quantity, an unspecified quantity, a catalog angle that is missing or unspecified, different grids, a minimum shift that is too long, and parameters out of range.
- **Go** (`relorient_test.go`, real worker): refusals in order (undeclared, unspecified, no catalog declaration, no angle, bad parameters, a shift too long for the fixture), then a catalog result and a map result. The test checks the registered kind, the derived record and its sources, the CSV download and its rows, and that request and response conform to the OpenAPI spec.
- **Web** (`relorient.test.tsx`): readiness messages, candidates, the request with its seed, refusal reasons shown, result numbers and figures, the absence of success wording, V without a p-value, and the section shown only for results.
- **e2e** (`zzz-relorient.spec.ts`): from the map's inspector to a result with AM > 0.99, the figures, the CSV, and axe scans.
- **Looked at, not reviewed:** a 1440 × 900 dark screenshot of the result. Not done: the other viewports and themes, keyboard-only use beyond the axe scan, and owner review of the numbers on real data.

## Rollback / Migration
Nothing is migrated. Removing the feature leaves the registered result files and their derived records, which read as an ordinary catalog or map with a record whose operation is `relorient_*`.

- **Shared row filter.** The optional P/σP filter on catalog runs (`snr_column`, `snr_sigma_column`, `snr_min`) calls `catalog_table.snr_filter(value, sigma, k)`, the same function the catalog overlay's row filter uses (ADR 0042, fourth slice; its wire names are `filter.column`, `filter.sigmaColumn`, `filter.min`). Rule: keep when `P/σP >= k`; a missing or unreadable P or σP, or σP <= 0, has no ratio and is excluded with its own reason (`snr_missing`), a known ratio under k with `snr`. Tests: `test_snr_filter.py`.

## Amendment: presets (ADR 0068, slice 1)
The analysis defaults (disk radius 10 arcmin, at least 5 pixels, P/σP ≥ 3, the 1° weight floor, 2000 bootstrap resamples, 3000 catalog shifts and 1000 map shifts, a 1° minimum shift, keep fraction 0.9, seed 20261020, 9 histogram bins, 5 third-map bins) came from one researcher's scripts. They are now named, versioned presets in `services/scientific-worker/relorient_presets.json`, each value with a one-sentence reason.
- `standard` is the default and holds exactly the values above, so no result changes. `lmc-star-comparison` is the group's LMC starlight comparison; every value equals `standard` and its description says so. P/σP ≥ 3 is the usual detection threshold for a polarization angle (angle error at most about 9.5°); it applies only when the P and σP columns are chosen.
- A request may name `preset`. A parameter set explicitly replaces the preset's value. The result and its derived record store `preset: {name, version, values, overridden}`: every value the preset supplied (including the bootstrap count and the weight floor, which are not request parameters) and the parameters that replaced one. A result made before this amendment has no `preset`; its parameters are as stated.
- An unknown name is refused (422) and the known ones are listed. `GET /api/analysis/relative-orientation/presets` serves the file for the form, which fills its fields from the chosen preset.
- The file is flat in the worker folder and listed in `DEPLOY` so the RPM and remote hosts carry it. Relative orientation itself still runs on this computer only.
- Results and definitions no longer cite script files or line numbers. A code comment may say a step was ported from the group's star-comparison script.
- Validation: `test_relorient.py` (the standard preset equals the previous literals, an unknown preset is refused, an explicit value wins, the numbers are the same with or without naming the preset, and the independent reference computation still matches), the Go test (preset recorded in the derived record, the presets route, an unknown name), and the web test (fields fill from a chosen preset).
- Rollback: drop the `preset` request field and the route. The worker's defaults equal the old literals, so nothing else changes; stored records keep an unused `preset` object.
