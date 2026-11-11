# ADR 0047: Relative orientation statistics

- Status: proposed; implemented, awaiting owner review
- Date: 2026-11-11
- Related: ADR 0042 (declared angle frame and quantity, axial resampling), 0044 (point catalogs), roadmap Stage S

## Context
The owner compares VGT orientation maps with starlight polarization and with Planck dust polarization using his own scripts in the VGT repository. Three of them define the method this ADR follows. Paths below are relative to `scripts/` in that repository, read at the date above:
- `compare_vgt_polarization.py`:
  - the alignment measure AM = ⟨cos 2Δψ⟩ with headless angles (lines 23-24, 44-45, 73-77);
  - star angles carried into the map's pixel frame (55-64);
  - Planck handling: it asserts `POLCCONV = COSMO` (96), flips U to IAU (88), takes θ_B = ½ atan2(U, Q) + 90° (170), averages Q and U over blocks rather than angles (159-160, 176-178), and cuts on P/σP (163-167).
- `vgt_phase_vs_stars.py`:
  - VGT averaged axially in a disk around each star (55-66);
  - the shifted-pattern null (97-117);
  - defaults: a 10 arcmin radius and 3000 shifts (123-124), seed 20261020 (34).
- `star_vgt_scale_analysis.py`: weighted AM (63-69), a permutation null (72-79), a bootstrap 16-84% range (82-87), and weights 1/max(σ_PA, 1°)² (269).

ADR 0042 made the angle's frame and quantity declared facts, and ADR 0044 made catalogs datasets with a declared frame and angle. What is still missing is the comparison itself, with the owner's chance level and numbers that come out the same on every run.

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
| Δψ | ψ_map − ψ_other folded into [−90°, 90°) (`compare_vgt_polarization.py:44-45`). The relative orientation is \|Δψ\| ∈ [0°, 90°], so 1° and 179° are 2° apart |
| AM | mean of cos 2Δψ: 1 parallel, 0 no preferred relative orientation, −1 perpendicular |
| Weighted AM | Σ w cos 2Δψ / Σ w (`star_vgt_scale_analysis.py:63-69`). Mode A uses w = 1/max(σ, 1°)², σ in radians, from the catalog's declared uncertainty column (line 269). Mode B uses w = the product of the two declared reliabilities, 1 for a map without one |
| Projected Rayleigh statistic (mode B) | V = Σ w cos θ / sqrt(Σ w² / 2) with θ = 2Δψ. This is Jow et al. 2018 (MNRAS 474, 1018; arXiv:1708.04063), eq. 8, and eq. 6 when every w is 1; eq. 2 defines the relative angle in [−90°, 90°] and the doubling. The paper writes Z_x; V is the name later papers use. Positive means preferentially parallel. It is asymptotically N(0, 1) only for independent angles |
| Histogram | \|Δψ\| in equal-width bins over [0°, 90°]: 9 bins of 10° by default, 2 to 90 bins as a parameter. Every bin is the same width, so no split needs a separate reason |
| Bootstrap (mode A) | 16th to 84th percentile of AM over 2000 resamples with replacement (`star_vgt_scale_analysis.py:82-87`) |

### Mode A sampling (map at each source)
- The map is averaged with the doubled angle over a disk:
  - The disk is centred on the pixel nearest the source (`vgt_phase_vs_stars.py:57`).
  - Pixels count when their centres lie within the radius, measured as angular distance between pixel-centre unit vectors from astropy `pixel_to_world`. The owner's script measures pixel distance instead (63), with r_px taken from the mean pixel scale (140).
  - NaN pixels are left out.
  - Each pixel is weighted by the map's declared reliability. Without one the mean is unweighted, and the result says which.
- Kept from the script:
  - the search box half-width ri = ceil(r / mean pixel scale) (50-52, 58);
  - a box that leaves the map excludes the source (58-60);
  - fewer than 5 usable pixels excludes the source (64). The minimum is a parameter.
- Added:
  - A disk whose doubled-angle resultant is below 1e-6 has no mean and is excluded. The script would take atan2(0, 0) = 0.
  - The summary counts disks that reach the edge of the search box, which happens where the local pixel scale is smaller than the mean.
- Every catalog row is accounted for. Each excluded row is counted with its reason (position, angle, no pixel, outside the map, disk at the edge, too few pixels, zero weights, cancelling orientations) and listed in the table.

### Mode A null: the owner's shifted pattern, ported line by line (`vgt_phase_vs_stars.py:97-117`)
1. n_real is the number of sources used. n_sub = int(keep · n_real), with keep = 0.9 by default.
2. For each of the N shifts (3000 by default), draw dx = uniform(−nx, nx), then dy = uniform(−ny, ny). Redraw until hypot(dx, dy) ≥ the minimum shift in pixels, which is 1° divided by the mean pixel scale (100, 104-107).
3. Wrap positions as (x + dx) % nx and (y + dy) % ny (108). Each source keeps the pixel-frame angle it had at its true position (92, 110).
4. Every source with a pixel position and an angle takes part, including sources outside the map, which the wrap brings in.
5. A shift is kept when at least n_sub shifted sources have a usable disk. It is then scored on exactly n_sub of them, drawn with `choice(..., replace=False)` (112-114). A rejected shift is not redrawn.
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
  - With thousands of correlated pixels, the overlap size changes the spread of the null negligibly. Drawing millions of pixels per shift cost 0.6 s per shift against 0.15 s on a 2048 × 2048 map (measured).
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

## Alternatives
- **Average the map with a fixed pixel disk, as the script does.** Rejected for membership because the requested radius is on the sky. The centre rounding, the box and the edge rule are kept, so on a regular grid the two give the same disk; the reproduction test relies on this.
- **Compare in the sky frame and keep each source's sky angle in the null.** Holding the sky angle instead of the pixel angle gives a different null wherever north turns across the grid. The script holds the pixel angle, so this does too.
- **A permutation null (`star_vgt_scale_analysis.py:72-79`).** It ignores spatial structure in the map. The shifted pattern keeps it and is what the owner reports for stars.
- **Convert V into a p-value with an effective N.** No validated method gives N for VGT's window correlation, so none is offered.
- **Register no file, only a record.** The derived store is keyed by a dataset, and a table people can open and export is the useful artefact.

## Consequences
- Two routes are added and described in `packages/contracts/bodies.json`. The YAML generator now quotes keys that YAML reads as null or a boolean: the summary has a `null` property, and the existing `y` and `n` keys are now quoted too.
- `analysis.go` has one runner for the analysis program (`runAnalysisProgram`), and `analysis_axial.go` one check of a declared reliability map (`reliabilityPath`). Both are reused.
- Measured on one machine, with page cache:
  - A 2048² map against 300 stars, a 30-pixel radius and 3000 shifts took 225 s, at about 1 GB peak resident memory for the run.
  - A 2048² map against a map took about 10 s for 200 shifts, and 1000 shifts take well under a minute.
  - Both are inside the 30-minute analysis timeout.

### Not claimed, deliberately
- AM, V, chance95 and p measure agreement between two tracers. They do not say that either tracer is correct, nor that the field is physically aligned.
- The bootstrap range assumes independent sources, and overlapping disks are not independent.
- The beam count is an approximation.
- No significance is stated from a raw pixel count.

### Deferred
- **Source selection by P/σP** (`vgt_phase_vs_stars.py:87`): the catalog declaration names no P columns. Filter the catalog before registering it.
- **Block (Stokes) averaging before comparison** (`compare_vgt_polarization.py:67-70, 159-178`): use convolve or align first.
- **Planck HEALPix ingestion and the COSMO→IAU U flip** (88, 96). An angle map derived from Planck must already be in the IAU convention: an angle map carries no POLCCONV, so this cannot be checked here.
- **Remote data.** Both inputs must be on this computer, as for the axial difference and for catalogs (ADR 0044). There is no host job for this analysis yet.
- **Drawing the segments through the scene's catalog overlay.** That overlay is not drawn yet (ADR 0042), so the figure is self-contained and has no raster underlay.
- **A cell or friends-of-friends grouping and a half-split ceiling** (`star_vgt_scale_analysis.py:123-156`).
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
