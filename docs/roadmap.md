# Roadmap

Status of everything the product requirements ask for beyond what is shipped, in the order it will be built.
Each stage is one pull request (or a few small ones). A box is ticked only when the behavior is merged and covered by a test.
"Q" numbers refer to the requirement interview questions; "(partial)" means something exists but not what was asked.

The product shape (what the whole thing is meant to be, distilled from the interview) is `docs/product/shape.md`; this file tracks what is built.

Shipped: v0.1 workflow (register, view, compare, figures, boards, reviewer link) and v0.2 (remote hosts, Slurm jobs, runs, cube analysis, comparison inspector, managed copies). See `docs/demo.md` and ADRs 0008–0018.

## Stage P — Performance contract (audit of 2026-11-05, shape.md section 15)

Do first: it makes the viewer claims measurable and unblocks scrubbing on large cubes.

- [x] `make bench`: sparse large cube, plane / spectrum / coordinate timings, p95 and worker memory against written budgets, run in CI (docs/performance.md). Timings while a heavy operation runs are not part of it; the lane behaviour has its own test.
- [x] Two worker lanes (interactive and heavy): a spectrum or orientation request never blocks channel scrubbing, and cancelling it kills only the heavy process (Q169). Analysis jobs already run in their own subprocess.
- [x] Spectrum read through the memory map in channel blocks, vectorised: a box spectrum 5x faster, the size cap raised from 2e8 to 1e9 pixel-channels (Q92).
- [x] Large planes on a remote host show a block-mean preview first (labelled as a preview, drawn on the full grid) and the full plane once the channel settles; neighbouring channels are prefetched into the server cache (Q182, Q185). A browser-side plane cache was not added: the server cache is on loopback and a browser copy would need its own staleness rule.
- [x] Worker memory cap (RLIMIT_DATA on top of what the loaded libraries hold, `VESQIRA_WORKER_MEM_MB`, default 3072; read-only file maps are not counted, so any cube size still opens). An allocation over the cap fails that request only. Per-request time is bounded by the lane timeout.
- [ ] Not yet measured: real disk, ssh, 4D cubes, list virtualisation at scale, resumable transfers.

## Stage L — The integrated loop (owner direction, 2026-11-05)

One workspace: inspect, change parameters, run on HPC, add to a board with the supervisor, discuss, remember. The integrated-loop mock (run page, live board panel, change-and-run, what differs) was approved on 2026-11-05; other new screens still need an approved mock.

- [x] Typed run metrics with unit, uncertainty and its kind, sample count and definition, shown on the run, in the reviewer's run line and beside each other in the comparison inspector (ADR 0014 amendment). The run filter box tests results (`am>0.3`). Plotting a metric against a parameter still to do.
- [x] "What differs" strip for two runs or panels (Q87, Q132). Built: tick two panels on a board for a table of differing run settings, code, input, view and declared results with their comparability basis. Deferred: "Add this table to the board" (needs a new board item kind) and a difference-map button.
- [x] Runs as nested pages: a run opens as a page of blocks (default result drawn with the viewer, other results shown in place or side by side, declared numbers, settings with the difference from the parent run, code, what is known about rerunning, your assessment, the job log on demand); the inspector keeps a summary. Still to do: comments from boards on the run page and blocks you add yourself (a plot of numbers, a note, a linked run).
- [x] A board panel that follows a run still running and pins its result when it finishes. Built: a job on a board shows its state, progress (from the VGT chunk log) and an estimate, becomes its default output once imported as a run, and is pinned as a figure; a snapshot freezes it as a placeholder. Not built: adding a job to a board from the rerun form (next; `addJobToBoard` exists in the web API), and automatic import of finished results.
- [x] Change parameters from the cube view and run in one click (pulls shape.md slice 2 forward). Built: "Run again with…" drafts a job from a run (or from runs matched to the open input file), owner-only; submit stays on the Jobs screen after reading the script. Not yet: region-based entry from the viewer, "add to a board when it starts".
- [ ] Parameter grid board from a sweep.
- [ ] Integrated editor and terminal (Monaco, xterm.js over the local backend) (Q39). ADR 0029 (accepted): owner-only, off by default, the editor first.
- [ ] Optional VPN companion (gpclient): a per-host connect command run through polkit, ADR 0029 (accepted); built after the editor and terminal.

## Stage W — Projects as folders (owner direction, 2026-11-05)

See `docs/product/shape.md` section 3. Screens need the projects mock approved first.

- [ ] Open a folder on this computer or on a host as a project; recent projects; `.vesqira/project.toml` proposed, written only after confirmation, never committed.
- [ ] Discovery by good practices (run records, data layouts, self-describing formats, configs, job scripts), runs found before the maps they own, deduplicated by real path, rescanned on open and when the window returns; files of other disciplines listed without a viewer.
- [ ] "Needs your input": unknown units, interpretations, where the repo is on each host; answers stored as declared by the researcher; "leave unknown" is an answer.
- [ ] Project health checklist and run records written by every run Vesqira starts.
- [ ] Path fields with zsh-like completion (local and on hosts) and the desktop file chooser (xdg-desktop-portal), with GVFS sftp mounts mapped to hosts.
- [ ] `/api/runs` input hints computed from one catalog read instead of one per run (148 runs, 1 350 files is the target scale); a bench case at that scale.

## Stage 0 — Install and launch

- [x] Server serves the web app; Fedora RPM with launcher, desktop entry, icon and metadata (ADR 0021).
- [x] Release workflow: build the RPM in a Fedora container on a tag, run the install smoke test, attach it to the release.
- [ ] Source RPM built in the build root, and a COPR repository so `dnf copr enable` works.
- [ ] Flatpak (needs flatpak-builder and a decision on ssh and agent access from the sandbox).
- [ ] Manage readable folders inside the app instead of `vesqira add-root` (covered by Stage W: opening a project folder makes it readable).

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
- [x] Scalar difference map between two compatible maps with the same declared unit (ADR 0017 amendment); a map declared axial uses the axial difference.
- Channel animation: dropped for now.
- [x] Saved, named regions: create, rename, change shape, duplicate, hide, delete with undo, export as DS9 (ADR 0026). DS9 import in image coordinates and in sky coordinates (FK5/ICRS/galactic circles and unrotated boxes, placed with the file's WCS and listed as approximated) with a preview.
- [ ] Exploration history and "continue from here" (Q96–Q98).
- [ ] Linked views can be grouped and unlinked by the user; linking through a validated transformation (Q174).
- [x] Keyboard pixel cursor (Shift+arrows) and a table alternative for the spectrum plot (Q176).
- [x] Raw FITS header cards viewable and exportable (Q211).

## Stage 4 — Runs, parameters and reproducibility

- [~] Typed run metrics with units and uncertainty are in (stage L); cross-run filtering and plotting (Q128–Q131) are not.
- [ ] Parameter sweeps and "explore variations" as a set of linked runs (Q128, Q222).
- [x] Reproducibility facts beyond the git flag, each declared, inferred or unresolved (Q121–Q125, Q198). Capturing environment and hashes is still to do.
- [ ] Rerun modes: strict, best effort, adapted (Q123).
- [x] Accounting of failed evaluations when the run reports them; cancelled and excluded are stated as not recorded (Q224).
- [ ] Structured fields for controls: smoothing, noise model, how a control was constructed (Q246).
- [x] ROHSA job template next to the existing VGT one (follows the pipeline's ROHSA cluster script; results are not imported as a run).
- [x] Beam matching (convolution to a common beam) as an explicit derived resource; never implied by reprojection (Q205).
- [ ] Unit conversion with dimensional checks; frame transform between declared frames (Q204, Q209).

## Stage 5 — Data model and catalog

- [ ] Typed masks: low SNR, saturated, user excluded; masks as derived resources carried by outputs (Q201).
- [ ] Interpretation tiers: add `verified` and `inferred` with a mapping version (Q211).
- [ ] Dataset identity independent of the path: several locations per dataset, content fingerprint, "newer result available" notice (Q83, Q84, Q136).
  - [x] First part (Q83): a registered file on this computer that changed (size or modification time) or vanished is flagged in the list and the inspector, checked at start, when the window returns and after registering; a changed file offers "Read it again" and the viewer restarts on it. Remote files are not checked (that waits on the network). Identity, fingerprints and several locations are not built.
- [ ] WCS and unit corrections as a layer with history, never an edit of the original (Q143).
- [x] Filter boxes in the explorer: datasets by name, unit, host or nature; runs by parameters (key=value, <, >, <=, >=), QC and text (Q19–Q21, Q163). A sortable table is still to do.
- [ ] Delete and archive with an impact preview (Q145).
- [ ] Virtual subsets (a definition, not only a materialized cutout) (Q141).
- [ ] Generic import: scan a folder, review the proposed mapping, save the rules (Q17, Q82).
- [x] Host aliases from your ssh config are offered while adding a host (alias, plain host name and user only; keys, proxy commands and wildcard patterns are never read out; `--ssh-config` changes or disables it) (Q27).
- [ ] Transfers with a resumable queue and conflict handling (Q36–Q38).
- [ ] "Prepare for offline" (Q108).

## Stage 6 — Structure, contracts and operations

- [x] Contract layer, first part: an OpenAPI route list generated from the Go routes, with `make docs` failing on drift or on a web call to a route that does not exist (Q170, Q181). Bodies and generated TypeScript types are still to do.
- [x] `packages/contracts` exists. `packages/science-types`, `extensions/astronomy` and `integrations` are retired from AGENTS.md as structure to create now: the semantic code lives in the worker and the Go packages, and is split out when a second discipline or connector makes the seam real.
- [x] `GET /api/capabilities`: application, protocol and schema versions reported separately, and which features this server was started with (planes, hosts, jobs, analysis, runs, boards, regions, audit), so a client shows a missing feature instead of failing on a 404 (Q155, Q158). No client uses it yet.
- [ ] Workload classes: reserved interactive capacity, queue reasons shown to the user (Q169).
- [x] Every API request gets a correlation id (the caller's `X-Request-Id` when it is a plain token, else generated) returned in the response, and one JSON log line (method, path, status, size, duration; never the query string); quiet by default (failures and slow requests), `VESQIRA_LOG=debug` shows all. The id is not yet passed to the workers or shown in the UI; the support bundle is not built (Q158, Q163).
- [x] Dependency boundaries enforced by tests: a layering table for the Go packages and the reviewer-reachable set (`internal/arch`), and the reviewer page's imports in the web app (`boundaries.test.ts`) (Q180).
- [ ] Command registry with undo for board and review edits (Q175).
- [ ] Figures stored as a re-renderable specification, with SVG export; board layout grid with snap and resize (Q186, Q187, Q10–Q13).
- [ ] Map every case of the scientific acceptance pack to a test id once the pack is available (35 cases).

## Stage 7 — People and access (design first)

The owner chose a shared multi-user server (ADR 0028 option B). Its design ADR comes before any code.

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
