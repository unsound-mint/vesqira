# ADR 0014: Runs and the VGT run adapter

- Status: accepted under delegated merge, 2026-11-04; owner review pending
- Date: 2026-11-04

## Context
ADR 0013 introduces Runs. VGT writes `run_metadata.toml` next to its output maps, recording parameters, the code commit (with a `-dirty` suffix for an unclean tree), the input file and the output paths of the machine it ran on.

## Decision
- **Import.** `POST /api/runs/import` takes a run folder or its `run_metadata.toml`, which must resolve inside a source root like any file. Output files are looked up by file name next to the metadata (the paths inside it belong to the run's own machine and are never followed); each found map is registered as an ordinary dataset and linked to the run by role (psi, reliability, support, fit_rbar2). Re-importing the same folder refreshes the run.
- **Parameters are recorded as the pipeline wrote them**, one row when both sections agree and both rows when they differ. Keys or values that look like a location or a person (file, path, dir, home, root, host, user, anything containing a slash) are dropped when the run is parsed, so no path, host or user name reaches the catalog view, a figure or a reviewer.
- **Code version.** A commit ending in `-dirty` is shown as "not reproducible from the commit" everywhere, including to the reviewer.
- **Input link is a hint.** The run's input file name and size are matched against registered datasets and offered as "inferred from name and size; not proof of identity". The input path is not stored.
- **Proposed interpretation.** The psi map is proposed as axial orientation in radians (the VGT source computes `0.5*atan2(U,Q)+pi/2`), paired with the reliability map when `compat.Grids` confirms the same grid. The declaration records `declaredBy: vgt-run-adapter` and is shown as the run import's proposal, not the file's and not the user's. A user's own declaration is never overwritten by a later import, and the user can change the proposal like any declaration.
- **Figures carry the run.** A figure of a run output includes the run name, role, short commit, dirty flag, finish time and parameters in its reviewer-safe provenance.
- **Dependency.** `github.com/BurntSushi/toml` (MIT) parses the metadata.

## Alternatives
Reading TOML in the Python worker (a process hop for a tiny file); following the paths in the metadata (they refer to another machine and could point anywhere); matching the input cube by name alone.

## Consequences
Runs from other pipelines need their own adapter. Only the VGT metadata layout is understood.

## Validation
Parser tests: parameters kept, every location-like key and value dropped, outputs reduced to names, unknown output keys ignored, dirty versus clean commit, non-metadata and oversized input refused. An end-to-end Go test imports a generated run folder through HTTP with the real worker: four outputs linked, an outside path refused, the adapter's interpretation and reliability pairing, the inferred input link, re-import without duplication, a user's declaration kept, and a reviewer response that shows the parameters and the dirty commit but contains no input path, project path, user name or root. The browser walk-through imports the run and follows it to the reviewer.

## Rollback / Migration
Drop the `runs` and `run_outputs` tables and the routes; datasets are unaffected.

## Amendment: what is known about rerunning a run

A run now carries a list of facts about reproducibility, each marked `declared`, `inferred` or `unresolved`: code version (commit, and whether the run reports uncommitted changes), parameters, input (name and size as declared; a registered file with the same name and size is only *inferred*, and no content hash is recorded), software environment (not recorded by the run metadata, so unresolved), random seed (declared only when a parameter named like a seed exists), output files (registered by reference, no checksums recorded), what counts as the same result (not stated), and evaluations that failed (declared only when the run reports both attempts and failures; cancelled or excluded evaluations are not recorded). The run panel shows them under "What is known about rerunning this". This is not an assessment of the result and never replaces the author's own QC. Recording more (environment capture, hashes) would be done by the pipeline or an explicit Vesqira action, not guessed.

## Amendment: filtering runs and datasets

The explorer lists get a filter box (shown once there are more than three runs or five datasets). Words narrow the list. For runs, `key=value`, `key<value`, `key>value`, `key<=value` and `key>=value` test the run's declared parameters (as numbers when both sides are numbers, otherwise `=` compares text); `qc:passed` tests the author's assessment; other words search the name, kind, commit and input name. A parameter a run does not have never matches: absence is not a value. Datasets are searched by name, host, brightness unit and declared nature. The filtering is done in the browser over the lists already loaded.

## Amendment: typed run results (metrics)

A run's metadata may carry an optional `[metrics]` section (Q130, Q131). Each entry is a bare number or a table with `value`, `unit`, `uncertainty`, `uncertainty_kind` (`standard_deviation`, `standard_error`, `confidence_68`, `confidence_95`, `other`), `n` (independent samples) and `definition` (the run's own words about what the number measures). Rules, all tested: every metric is *declared*, since Vesqira neither computes nor verifies it; a value that is missing, `nan` or infinite is unknown and shown as "not recorded", never 0, while a recorded 0 stays 0; a negative uncertainty is dropped; an uncertainty without a recognised kind keeps an empty kind ("kind not stated") instead of a guessed one; a unit or definition that names a place on a machine is dropped like location-like parameters; at most 50 metrics. Metrics are stored with the run (a `metrics` column added by `schema.AddColumn`), replaced on re-import, shown on the run, carried into a figure's provenance (so a reviewer sees the values that were current when the figure was made) and listed side by side in the comparison inspector. Two runs' same-named metrics are offered as comparable only when both state the same definition and unit; otherwise the row says why not (Q132). The run filter box tests results with the same words as parameters (`alignment_measure>0.3`) when no parameter has that name; a result the run did not record never matches (unknown is not 0). Plotting a metric against a parameter is not built yet.

## Amendment: the adapter also proposes the angle's frame and quantity
The adapter proposes `frame = pixel` and `quantity = b_field` for the psi output (ADR 0042), from the pipeline's convention (ψ = ½ atan2(U,Q) + 90°, radians, counter-clockwise from FITS axis 1, already rotated from the gradient). It is a proposal labelled with its author, overridable in the orientation panel; the files carry none of it.
