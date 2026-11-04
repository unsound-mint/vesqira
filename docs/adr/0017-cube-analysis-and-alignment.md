# ADR 0017: Cube analysis and alignment

- Status: accepted under delegated merge, 2026-11-04; owner review pending
- Date: 2026-11-04

## Context
Researchers need the standard cube operations: statistics, moment maps, cutouts, spectral slabs, and putting maps or cubes on a common celestial grid so they can be compared. ADR 0013 requires that these are explicit derived resources with provenance, use validated libraries for coordinates, and that heavy work on a remote host goes through Slurm.

## Decision
- **One program, `analysis.py`**, with five operations (stats, moment, cutout, slab, align). It never modifies an input, writes outputs atomically (temporary file then rename), reads cubes one channel at a time (a 3 GB cube's moment 0 took 17 s and 1.4 GB of memory), and prints one JSON object (the summary, the policy it kept and the software versions).
- **Missing data stays missing.** NaN, BLANK and infinite values are excluded and counted, never treated as zero. A sum over nothing is NaN. Moment 1 and 2 need a positive total weight and a selected value, otherwise NaN. Values below a user threshold are "not selected", not zero. Outside the footprint of a reprojection the result is NaN.
- **Spectral values come from the file's own WCS** (astropy). A spectral axis that is not fully declared is a refusal (no invented velocities). Moment units are the brightness unit times the axis unit (or "brightness unit unresolved" when BUNIT is absent), the axis unit for moments 1 and 2. A frequency axis gives frequency moments; no velocity convention is assumed. Moment formulas are the plain definitions on the WCS axis values (checked against an independent numpy sum and the closed-form line centre on the fixtures); `spectral-cube` was not adopted to avoid a large dependency tree for three one-line sums.
- **Cutouts and slabs** copy values unchanged and shift the WCS reference pixel, so every coordinate stays exact (checked against astropy on the original and the cutout).
- **Alignment uses `astropy` `reproject`** (interpolation or flux-conserving exact method) onto the reference's celestial WCS, frame conversion included, carrying the input's spectral axis. It states in the file header, the summary and the interface that pixel values are interpolated, noise correlation changes and angular resolution is NOT matched (reprojection is not resolution matching). `reproject` is only needed on machines that run alignments and is reported as missing otherwise.
- **Results are new datasets with a derived record** (operation, options, source datasets, summary, software). Local analyses run in their own process group with a 30-minute limit and a capped output, write under `<data dir>/derived` (which is a source root), and register like any file. A figure of a derived dataset carries how it was made (operation, options, source file names, policy, versions) to the reviewer, never a path.
- **On a host the analysis is a Slurm job.** `analysis.py` is deployed to the host with the worker (by content hash); a job template renders a script that runs it (validated paths, numbers and words only; output `result.fits` and `result.json` in a folder inside the host's source roots). The script is reviewed and submitted like any job (ADR 0016); afterwards the result file is registered in place with its derived record, and a statistics job's result is read from `result.json`.

## Alternatives
Computing on login nodes (violates the Slurm rule); spectral-cube for moments; resampling with our own interpolation; averaging planes in the browser.

## Consequences
Both files of an alignment must be on the same machine. Cutouts and slabs support (x, y, channel) cubes. Alignment adds the `reproject` dependency (and its dask/zarr tree) where it is used.

## Validation
Python tests on the fixtures: statistics equal numpy and count the NaN block as no value; moment 0 equals an independent sum with NaN where nothing exists; moments 1 and 2 equal the closed form and the source's line centre; refusal without a declared spectral axis; cutout and slab keep world coordinates identical to the original; alignment gives the reference grid with the same spectral axis, NaN outside the footprint and no invented zeros, the source peak where the independent TAN positions put it, and a proper frame transform (Galactic reference); inputs are byte-identical afterwards and failures leave no files. Go: option validation, new derived datasets with records, alignment sources, reviewer provenance by file name only, local analysis of a remote file refused, a remote analysis job whose script was rendered, submitted, run and registered with its derived record on the host. The browser walk-through covers statistics, a moment map and alignment.

## Rollback / Migration
Drop the `derived` table and the analysis routes; derived files can be deleted from the data directory.
