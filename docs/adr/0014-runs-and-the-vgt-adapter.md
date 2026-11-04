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
