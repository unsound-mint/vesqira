# Roadmap

Status of everything the product requirements ask for beyond what is shipped, in the order it will be built.
Each stage is one pull request (or a few small ones). A box is ticked only when the behavior is merged and covered by a test.
"Q" numbers refer to the requirement interview questions; "(partial)" means something exists but not what was asked.

The product shape (what the whole thing is meant to be, distilled from the interview) is `docs/product/shape.md`; this file tracks what is built.

Shipped: v0.1 workflow (register, view, compare, figures, boards, reviewer link) and v0.2 (remote hosts, Slurm jobs, runs, cube analysis, comparison inspector, managed copies). See `docs/demo.md` and ADRs 0008–0018.

## Stage P — Performance contract (audit of 2026-11-05, shape.md section 15)

Do first: it makes the viewer claims measurable and unblocks scrubbing on large cubes.

- [ ] `make bench`: sparse large cube, cold and warm plane / spectrum / coordinate timings, p50/p95 and worker memory, also while a heavy operation runs; budgets written down (Q149, Q164).
- [ ] Two worker lanes (interactive and heavy) so a spectrum or analysis never blocks channel scrubbing; heavy lane cancellable by restarting only that worker (Q169).
- [ ] Spectrum read in channel blocks, vectorised, so large regions are served instead of refused (Q92).
- [ ] Downsampled preview then full resolution on settle, marked in the reply header; browser-side plane cache; prefetch of neighbouring channels (Q182, Q185).
- [ ] Worker memory cap (RLIMIT_AS) and per-request limits that bound time and memory, not only output size.
- [ ] Not yet measured: real disk, ssh, 4D cubes, list virtualisation at scale, resumable transfers.

## Stage L — The integrated loop (owner direction, 2026-11-05)

One workspace: inspect, change parameters, run on HPC, add to a board with the supervisor, discuss, remember. New screens need an approved mock first.

- [ ] Typed run metrics with definitions, shown on the run and on the reviewer page (Q130, Q131).
- [ ] "What differs" strip for two runs or panels (Q87, Q132).
- [ ] Runs as nested pages: default result, other results of the same run, config and logs on demand.
- [ ] A board panel that follows a run still running and pins its result when it finishes.
- [ ] Change parameters from the cube view and run in one click (pulls shape.md slice 2 forward).
- [ ] Parameter grid board from a sweep.
- [ ] Integrated editor and terminal (Monaco, xterm.js over the local backend) (Q39).
- [ ] Optional VPN companion (gpclient): needs a privilege and security decision first.

## Stage 0 — Install and launch

- [x] Server serves the web app; Fedora RPM with launcher, desktop entry, icon and metadata (ADR 0021).
- [x] Release workflow: build the RPM in a Fedora container on a tag, run the install smoke test, attach it to the release.
- [ ] Source RPM built in the build root, and a COPR repository so `dnf copr enable` works.
- [ ] Flatpak (needs flatpak-builder and a decision on ssh and agent access from the sandbox).
- [ ] Manage readable folders inside the app instead of `vesqira add-root`.

## Stage 1 — Safety and honesty of what is stored

- [x] Job submission cannot run twice: record the intent before `sbatch`, look up an existing Slurm job (by job name/comment) before any retry, and show an "unknown, check the cluster" state instead of resubmitting after a timeout (Q168).
- [x] Review and QC history: run review becomes append-only revisions with author, time and the previous value (Q196, Q118).
- [x] Audit log for actions that change state or expose data: host added, job submitted/cancelled, review published/revoked, copy made (Q118, Q119).
- [x] Schema versioning: a version table, ordered migrations, a failing upgrade stops start-up, and an automatic backup of the catalog before a migration (Q166, Q167).
- [x] Project export and restore of the catalog with a manifest of external references (Q68, Q85).

## Stage 2 — Review with a supervisor

- [x] Comments can be anchored to a region, channel or coordinate of a figure (Q14, Q99).
- [x] Reply and resolve on comments; a finding that stays open until someone closes it (Q14, Q15, Q100).
- [x] Reviewer identity: a name asked on first comment, remembered in the reviewer's browser, shown to the author (Q45). Still self-declared; real accounts come in Stage 7.
- [x] "Ready for review" request with a scientific question (Q101, Q102). Suggested related runs: not yet.
- [x] Reviewer outcome per snapshot (not yet reviewed, comments, changes requested, reviewed) kept separate from the run's own QC (Q103, Q104).
- [x] Activity summary for the author and the reviewer: what changed since the last visit (Q101).
- [x] Publication preflight and audience preview: what exactly a link exposes, before it is created (Q112, Q113).
- [x] Correction notice: a snapshot can be superseded by a newer one with a reason (Q111).
- [x] Export a figure or a board as PNG and PDF with units, legend and provenance (Q107). PNG carries its provenance inside; PDF is the browser's print-to-PDF of a print layout.
- [x] Presenter order and presenter mode for a board (Q105, Q106). Order is the board order; live viewers in a presentation are not built.

## Stage 3 — Viewer and comparison

- [x] Zoom and pan, shared colour scale across panels (Q24, Q25). Blink: dropped for now.
- [x] Histogram of the visible plane with draggable and typed colour limits. Of a region: not yet.
- [ ] Scalar difference map between two compatible maps.
- Channel animation: dropped for now.
- [x] Saved, named regions: create, rename, change shape, duplicate, hide, delete with undo, export as DS9 (ADR 0026). DS9 import in image coordinates with a preview; sky-coordinate import still to do.
- [ ] Exploration history and "continue from here" (Q96–Q98).
- [ ] Linked views can be grouped and unlinked by the user; linking through a validated transformation (Q174).
- [x] Keyboard pixel cursor (Shift+arrows) and a table alternative for the spectrum plot (Q176).
- [x] Raw FITS header cards viewable and exportable (Q211).

## Stage 4 — Runs, parameters and reproducibility

- [ ] Typed run metrics with units and uncertainty; cross-run filtering and plotting (Q128–Q131).
- [ ] Parameter sweeps and "explore variations" as a set of linked runs (Q128, Q222).
- [x] Reproducibility facts beyond the git flag, each declared, inferred or unresolved (Q121–Q125, Q198). Capturing environment and hashes is still to do.
- [ ] Rerun modes: strict, best effort, adapted (Q123).
- [x] Accounting of failed evaluations when the run reports them; cancelled and excluded are stated as not recorded (Q224).
- [ ] Structured fields for controls: smoothing, noise model, how a control was constructed (Q246).
- [x] ROHSA job template next to the existing VGT one (follows hpc/irgetas/rohsa.slurm; results are not imported as a run).
- [x] Beam matching (convolution to a common beam) as an explicit derived resource; never implied by reprojection (Q205).
- [ ] Unit conversion with dimensional checks; frame transform between declared frames (Q204, Q209).

## Stage 5 — Data model and catalog

- [ ] Typed masks: low SNR, saturated, user excluded; masks as derived resources carried by outputs (Q201).
- [ ] Interpretation tiers: add `verified` and `inferred` with a mapping version (Q211).
- [ ] Dataset identity independent of the path: several locations per dataset, content fingerprint, "newer result available" notice (Q83, Q84, Q136).
- [ ] WCS and unit corrections as a layer with history, never an edit of the original (Q143).
- [x] Filter boxes in the explorer: datasets by name, unit, host or nature; runs by parameters (key=value, <, >, <=, >=), QC and text (Q19–Q21, Q163). A sortable table is still to do.
- [ ] Delete and archive with an impact preview (Q145).
- [ ] Virtual subsets (a definition, not only a materialized cutout) (Q141).
- [ ] Generic import: scan a folder, review the proposed mapping, save the rules (Q17, Q82).
- [ ] Import hosts from `~/.ssh/config` names (Q27).
- [ ] Transfers with a resumable queue and conflict handling (Q36–Q38).
- [ ] "Prepare for offline" (Q108).

## Stage 6 — Structure, contracts and operations

- [x] Contract layer, first part: an OpenAPI route list generated from the Go routes, with `make docs` failing on drift or on a web call to a route that does not exist (Q170, Q181). Bodies and generated TypeScript types are still to do.
- [~] `packages/contracts` now exists. `packages/science-types` and `extensions/astronomy` are named in AGENTS.md but not created: the semantic code lives in the worker and the Go packages. Decide whether to create or retire them.
- [ ] Capability and version endpoint (Q155).
- [ ] Workload classes: reserved interactive capacity, queue reasons shown to the user (Q169).
- [ ] Structured logs with correlation ids (Q158).
- [ ] CI rule that enforces dependency boundaries (Q180).
- [ ] Command registry with undo for board and review edits (Q175).
- [ ] Figures stored as a re-renderable specification, with SVG export; board layout grid with snap and resize (Q186, Q187, Q10–Q13).
- [ ] Map every case of the scientific acceptance pack to a test id once the pack is available (35 cases).

## Stage 7 — People and access (design first)

Needs an ADR before code: it changes the security boundary.

- [ ] Accounts and roles per project and per cluster (Q29–Q31).
- [ ] Authentication before anyone but the owner can act; delegation and approval lifecycle for other users' requests (Q44, Q71).
- [ ] Worker memory cap and the authorize-then-open window for source files.

## Stage 8 — Later phases (kept, not scheduled)

Investigations, hypotheses and meetings (Q47–Q51); source catalogs, crossmatch and feature associations (Q212, Q213); coverage, sensitivity and non-detection context (Q214); evidence collections, claim links and experiment proposals (Q215–Q226); BibTeX, DOI and arXiv records; events, webhooks, SDK and MCP adapter (Q170, Q171); calibration registry and time coordinates (Q202, Q203); Git manifests (Q86); project dashboard and draggable tabs (Q8, Q9).

## Not decided by code

- Trademark and domain clearance for the name.
- Whether the repository becomes public, and removing an earlier private document from history (needs a force-push).
- Owner review of ADRs 0007–0018.
