# ADR 0068: Tool profiles: pipelines as declared data, not built-in cases

- Status: proposed, awaiting owner review; nothing implemented yet beyond the fixes in slice 0
- Date: 2026-11-13
- Related: ADR 0014 and 0018 (VGT products and header cards), 0042 (declared angle frame and quantity), 0046 (ROHSA runs and Gaussian components), 0047 (relative orientation), 0054 (strict rerun pins the commit), 0058 (import rules), 0067 (Planck maps declared without a pipeline)

## Context
Vesqira supports VGT and ROHSA by name: hard-coded header cards, run-record files, folder layouts, config keys, job templates and analysis defaults. The owner's direction (2026-11-13): Vesqira gives astronomers tools, not one group's pipeline. It must stay compatible with the open-source versions of the tools it knows, and other tools should plug in without changes to Vesqira's code. The point is to make routine work (registering outputs, aligning maps, comparing orientations, reviewing fits) quick and low effort for anyone, whichever code produced the data.

An audit of the code against the public repositories (2026-11-13) found:
- **VGT.** The only public implementation found is ISM-NU/VGT.jl (MIT). `PSIFRAME`, `PSIANGLE`, `VGTPROD`, the `VGT*`/`INB*` cards and `run_metadata.toml` are its conventions. They are public and documented, but they are one repository's choice, not a field standard. The fork Vesqira was first built against is private.
- **ROHSA.** Upstream (antoinemarchal/ROHSA) writes only the `.dat` rows (`# i, j, A, mean, sigma`, i and j 0-based, mean 1-based in channels, sigma in channels, no units) and its namelist. Vesqira's reader matches it. The run trace TOMLs, `parameters_n<N>.txt`, the `rohsa/<label>/{maps,cubes}` tree and the crop FITS come from the group's wrapper ISM-NU/ROHSA-LMC. What ADR 0046 calls "the pipeline" is mostly ROHSA-LMC.
- **Drift.** The VGT job form offered `noise_mode = separable_shrinkage`, which VGT.jl has rejected since 0.1.0, and could not set `angle_frame` or `output_bin`. The ROHSA form called its repository field "VGT folder on the host", although the entry point `rohsa/cli/preprocess.jl` is in ROHSA-LMC. Both are fixed in slice 0.
- **Owner-script defaults in user-visible text.** The relative-orientation defaults (P/σ_P ≥ 3, 2000 bootstraps, 1° floor, 10 arcmin disk) and line citations of `vgt_phase_vs_stars.py` and `star_vgt_scale_analysis.py` appear in results that other researchers cannot open. The 2.03 km/s CNM width cut is a built-in default.
- **Inventions.** Vesqira reads a `[metrics]` table that VGT.jl never writes. It falls back to "pixel frame, radians, field orientation" when a VGT product has no `PSIFRAME`, which states as known a convention nobody declared.
- **Already general.** The orientation declaration (`catalog.Interpretation`), import rules (ADR 0058), unit and WCS corrections, masks, relative-orientation statistics, point catalogs and the generic run records (`provenance.json`, `run.json`, `params`/`metrics.json`, MLflow) need no VGT or ROHSA. A Planck map already enters this way (ADR 0067).

## Decision
A tool Vesqira knows is described by a **profile**: a versioned data file that Vesqira's own code interprets. VGT.jl, ROHSA (the `.dat`) and ROHSA-LMC (runs and products) become the first three profiles, shipped in the repository. A researcher can add one for another tool in a project's `.vesqira/profiles/`, at the trust level of an import rule. Everything a profile supplies is labelled as declared by `profile:<name>@<version>`, never as verified.

A profile never runs code on the Vesqira server or the desktop. Rendering a job script, reading files and validating values stay in Go and in the worker, using the existing validators. This keeps AGENTS.md's rule against untrusted in-process plugins in v0.1.

### What a profile can declare
1. **Detection and output roles.** How to recognise a run of the tool by content (a file plus a key inside it, not by name alone), and which outputs fill which generic role. Roles: orientation map, reliability, support, component table, component maps, model cube, residual cube, crop. Contracts and the UI name roles, and the tool's name is only a label. The `vgt_*` and `rohsa_fit` roles and the `vgtHeader` field generalise with a compatibility note.
2. **Orientation conventions.** A table from header cards to the fields of the orientation declaration: quantity, axial or vector statistics, unit, zero direction, sense, and frame (pixel, or a named sky frame). Today's `PSIFRAME` handling becomes the VGT.jl entry in that table. A product the profile cannot place gets "unresolved", never an assumed default.
3. **Decompositions.** A format-neutral record of per-pixel components: amplitude (in the grid's unit), centre (unit, index base, spectral frame), width (sigma, FWHM or dispersion, and unit), the component order, and the source. The source can be a text table such as ROHSA's `.dat`, or one FITS map per parameter, as GaussPy+ and scousepy produce. The components panel, the sigma-v diagram, the width-class maps and the crop linking read this record, not ROHSA. The 1-based mean becomes a field of the ROHSA profile. The derived quantities (Doppler temperature, a CNM cut) stay optional, labelled and outside the contract.
4. **Run templates.** The repository URL and a pinned commit (strict rerun already checks it, ADR 0054), the entry command, any preparation lines shown to the researcher, the editable config keys with types and ranges (replacing `configKeys` and `rohsaKeys`), a progress pattern, resource defaults and the environment probes. The rendered script stays exactly what is submitted, and is shown before submission.
5. **Analysis presets.** Named sets of defaults with a plain description, for example "LMC star comparison (VGT phase scripts)" holding today's relative-orientation defaults and the 2.03 km/s cut. The neutral default for everyone is stated in the result. Results stop citing file paths and line numbers the reader cannot open.

### Order of work
Each slice is shippable alone, and the order follows how much routine friction it removes for a researcher who does not use VGT or ROHSA:
0. Fix the template drift (VGT.jl config keys, the ROHSA-LMC label). Small; in progress with this ADR.
1. Analysis presets (item 5). Small to medium.
2. Orientation-convention profile (item 2), the "unresolved" fallback, and one copy of the card table (`fits_header.py` and `analysis.py` each have one). Medium.
3. Detection and output roles (item 1). Large; contract renames get a compatibility period.
4. Decomposition record (item 3) with a ROHSA profile and a FITS-maps source. Large.
5. Run templates as data (item 4). Medium to large.

## Alternatives
- **Keep built-in cases and add more tools the same way.** Every new tool would need a Vesqira release, and each one adds name checks to core code, which AGENTS.md forbids.
- **Executable plugins (Python or Go loaded at run time).** These are the most flexible, but they need a trust and isolation model that v0.1 does not have. Data profiles cover reading formats and rendering templates, which is what tools need from Vesqira.
- **A GitHub connector that reads a tool's repository and infers its formats.** Inference from code is unreliable, and wrong conventions are exactly the silent errors the scientific invariants forbid. A connector can later fetch a profile a tool's authors publish, and fetch the pinned commit for a template.
- **Generalise only the UI wording.** This is cheap, but a non-VGT tool would still not be readable.

## Consequences
- The VGT- and ROHSA-specific code moves behind three profiles, and the core gains roles, a decomposition reader and a convention table. The existing behaviour for VGT.jl and ROHSA-LMC users must stay identical, and their fixtures are the regression gate.
- Contracts change names (roles, `vgtHeader`, the `rohsa_runs` table to a general decompositions table). Old names are read for one release, the migration keeps every recorded run, and provenance records which profile version interpreted a file.
- A researcher-written profile is a declaration, like an import rule. A wrong profile gives wrong labels, not wrong data on disk, and the label says who declared it.

## Validation
- For VGT.jl and ROHSA-LMC, the current fixtures give the same results before and after each slice.
- At least one tool that is neither VGT nor ROHSA goes through each slice, with a fixture generated to its documented format: a HOG-style orientation map with its own cards for slice 2, and a FITS-maps decomposition for slice 4.
- A product without a declared convention shows "unresolved" in every place that showed the old default.

## Rollback/Migration
Each slice is separate. Until slice 3, profiles only add declarations and change no stored data. Slices 3 and 4 include a schema migration that keeps old rows readable and records the profile used, and they can be reverted by restoring the previous reader, because the files on disk are never changed.
