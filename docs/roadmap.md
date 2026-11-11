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

- [x] Typed run metrics with unit, uncertainty and its kind, sample count and definition, shown on the run, in the reviewer's run line and beside each other in the comparison inspector (ADR 0014 amendment). The run filter box tests results (`am>0.3`). A metric plotted against a sweep's parameters is on the sweep's grid board (ADR 0048).
- [x] "What differs" strip for two runs or panels (Q87, Q132). Built: tick two panels on a board for a table of differing run settings, code, input, view and declared results with their comparability basis. Deferred: "Add this table to the board" (needs a new board item kind) and a difference-map button.
- [x] Runs as nested pages: a run opens as a page of blocks (default result drawn with the viewer, other results shown in place or side by side, declared numbers, settings with the difference from the parent run, code, what is known about rerunning, your assessment, the job log on demand); the inspector keeps a summary. Still to do: comments from boards on the run page and blocks you add yourself (a plot of numbers, a note, a linked run).
- [x] A board panel that follows a run still running and pins its result when it finishes. Built: a job on a board shows its state, progress (from the VGT chunk log) and an estimate, becomes its default output once imported as a run, and is pinned as a figure; a snapshot freezes it as a placeholder. Adding the drafted job to a board from the rerun form is built. A finished VGT job is imported automatically once (per-job option, ADR 0043).
- [x] Change parameters from the cube view and run in one click (pulls shape.md slice 2 forward). Built: "Run again with…" drafts a job from a run (or from runs matched to the open input file), owner-only; submit stays on the Jobs screen after reading the script. Not yet: region-based entry from the viewer, "add to a board when it starts".
- [x] Parameter grid board from a sweep: job panels in parameter order, a metric table and metric-vs-parameter plot, no "best" label (ADR 0048).
- [~] Integrated editor and terminal (Monaco, xterm.js over the local backend) (Q39). ADR 0029 (accepted): owner-only, off by default, the editor first. Built: the editor for local text files (`--enable-editor`; confined saves, conflict view, audited opens and saves). Built since: editing text files on a host (same confinement and verified atomic save, ADR 0029) and the terminal (`--enable-terminal`: single-use token in the WebSocket subprotocol, sessions killed with their jobs, audit without content). The owner API refuses other OS accounts on Linux (peer gate).
- [x] Optional VPN companion (gpclient): a per-host connect command run through polkit, ADR 0029 (accepted); built after the editor and terminal.

## Stage W — Projects as folders (owner direction, 2026-11-05)

See `docs/product/shape.md` section 3. Projects mock approved by the owner on 2026-11-05.

- [~] Open a folder on this computer or on a host as a project; recent projects; `.vesqira/project.toml` proposed, written only after confirmation, never committed. Backend done for both (ADR 0030); the project file of a project on a host is written after confirmation through the verified host write path (ADR 0039), and never over a file Vesqira did not write. Screens built (the sidebar replaced the earlier folder tree): open a folder on this computer or on a host, recent projects, "what is in this folder" with the proposed file, the explorer in groups. Walked in a browser and covered by `e2e/tests/z-projects.spec.ts` for a local folder; opening on a host is not in that test.
- [x] Discovery by good practices (run records, data layouts, self-describing formats, configs, job scripts), runs found before the maps they own, deduplicated by real path, rescanned on open and when the window returns; files of other disciplines listed without a viewer. Backend done and tested (ADR 0030, `internal/discover`, `/api/projects`, host walk); "when the window returns" is the UI calling the rescan route. Only VGT runs are imported as runs; settings in `params.yaml` and `meta.yaml` are read like JSON records (anchors, aliases and deep nesting refused).
- [~] "Needs your input": unknown units, interpretations, where the repo is on each host; answers stored as declared by the researcher; "leave unknown" is an answer. Backend done (`/api/projects/{id}/needs` and `/answers`); a declared unit for a FITS file without BUNIT becomes that dataset's own declaration (ADR 0038): the viewer, figures, boards and the reviewer say it is yours, and the file is not changed. Analyses run on this computer carry it as the author's (ADR 0038); not those run as jobs on another host. The screen is built and tested, with axe in both themes.
- [~] Project health checklist and run records written by every run Vesqira starts. The checklist is computed and returned by the summary route and shown on its own page (header pill, axe-checked); every job Vesqira submits gets a `run.json` run record on the host once its folder exists, with each fact labelled by how it is known (ADR 0039).
- [x] Path fields with zsh-like completion (local and on hosts) and the desktop file chooser, with GVFS sftp mounts mapped to hosts (ADR 0031). Built: names-only completion, Browse through zenity or kdialog. Not yet: xdg-desktop-portal (needed for Flatpak), an "add this host" form prefilled from a GVFS mount.
- [x] Interactive filters: the run and dataset filter boxes complete keys, operators and values that occur and show conditions as chips (`ui/FilterBox`, vocabularies from the loaded items; same query string as before).
- [x] `/api/runs` input hints computed from one catalog read instead of one per run (148 runs, 1 350 files is the target scale); a bench case at that scale. Done: `catalog.HintIndex`, and a server test at 150 runs and 1 500 datasets that asserts one read.

## Stage R — Reports for the supervisor (owner direction, 2026-11-10)

Replaces the owner's Notion reports. ADR 0036 (proposed): reports on the owner's instance, drafted by the agent over MCP, published by the owner to an always-on publish server where named reader links read and comment. Reports mock: direction B (linked pages, with a graph) approved by the owner on 2026-11-10, with this brief: the supervisor rarely edits, presentation is what matters, so the cleanest possible hierarchy (one title, a quiet meta line, one primary action; threads in a side panel that never covers the text).

- [x] Reports in the catalog: Markdown body, edit history with author, base-revision conflicts, figure, run and report references, backlinks (a rename rewrites links), attachments (PNG, JPEG, GIF).
- [x] `vesqira mcp` (stdio) and `vesqira report` CLI for the agent: read and draft, no publish, send or resolve (a test pins the tool list).
- [x] Owner UI for reports and the reader page (mock B): page tree, a page drawn as readers see it and edited part by part in place, Markdown source view, insert figure or run from what exists, backlinks, comments panel with agent-drafted replies to send, Share (publish server, named reader links), publish preview, block-by-block review of the agent's changes; reader page with revisions and comments beside the text. Not yet: comments on a place in a figure, a reader-side graph, a second thread on a part that has one.
- [x] `vesqira publish-server`: publisher key, spaces, named reader links, published revisions, block-anchored comments, sync with the owner's instance (pull every minute), preflight, owner replies, agent-drafted replies sent by the owner. Reader page: with the UI.
- [x] `vesqira import notion` for a Notion Markdown & CSV export (zip or unpacked folder), with a dry run. PDFs and images over 4096 px are not imported.
- [x] Deployment docs for the owner's server (`docs/setup/publish-server.md`: systemd unit, Caddy with TLS, backups).
- [x] Graph view of report, run and figure links, with a keyboard list alternative; unlinked reports hidden by default.
- [x] Pages and presentation (ADR 0036, "Pages, front matter, changes and deletions"): page tree kept from the Notion import, front matter date and summary under the title, reader list newest first, what changed since the revision last read, comments on selected words, withdraw a report, delete a comment.
- [x] Follow-ups from the security review (ADR 0036, "Hardening"): image metadata stripped when publishing (`internal/imgmeta`, pixel-checked, fuzzed), fixed shape for derived `software`, bounded Markdown scans (one was cubic), comment-cursor guard. Left: a schema for derived `summary`.

## Stage S — Scene, cross-tracer comparison and the researcher's loop (owner direction, 2026-11-11)

The owner asked for the most convenient app for this kind of work, beyond the interview list. Items come from the owner's own research (VGT and ROHSA on LMC H I and CO, Slurm over a VPN, weekly reports) and from common radio-astronomy practice (contours, vectors, channel maps, PV diagrams, relative-orientation statistics). Design: ADR 0042. New screens do not wait for an approved mock during this push; the owner reviews afterwards.

- [x] Declared angle frame and quantity (pixel or sky; B-field, gradient, dust or starlight polarization), and axial-aware resampling for align and convolve (fixes the shipped linear interpolation of axial maps); previews of axial maps stay disabled.
- [~] Viewer scene: base plus ordered overlays, placed by WCS geometry, not resampling. Built: the scene/1 contract and viewer store (figures store their scene, never shown to reviewers), and overlays from other datasets: contours (levels incl. k×σ by MAD), orientation lines, beam ellipse, coordinate grid, each stating its basis or why it is not drawn. Catalog markers and B-field vectors from point catalogs (row filter shared with relative orientation, click a marker for its row, a keyboard list of sources). Linked views by WCS and redrawable figures with SVG export are built (see below).
- [x] Point catalogs as datasets (CSV, TSV, whitespace tables, ECSV, IPAC, VOTable, FITS table; sexagesimal or degrees; declared frame and angle columns; parse report by line), e.g. starlight polarization and absorption sightlines (ADR 0044). On this computer only.
- [x] Relative orientation between two orientation maps or a map and a catalog: alignment measure (weighted too), projected Rayleigh statistic (no p-value: correlated pixels) and histogram, the owner's shifted-pattern null reproduced, an optional P/σP cut, deterministic seeds (ADR 0047). Remote inputs, Planck HEALPix ingestion and block averaging are not built.
- [x] Linked view groups by sky position and spectral value (ADR 0042 §4): the worker's affine over the visible region with its stated error; channels by spectral value with the residual; SPECSYS must match for a channel link (same-grid pairs too).
- [x] Figures as scenes: draw again (data and declaration changes announced first; a redraw is a new linked figure), SVG export from the same marks (ADR 0042 §5).
- [x] Channel-map grid (shared scale, converted spectral labels, slab means) and position-velocity diagram along a line or polyline (offsets as sky separations; bilinear or nearest sampling, stated) (ADR 0049).
- [x] Gaussian decomposition browser for ROHSA results: component curves over the spectrum at a pixel (streamed index of the .dat), component and residual maps, CNM versus the rest with the width cut as a declared input (ADR 0046).
- [x] Job finished: jobs are watched while the app runs (batched squeue/sacct per host, backoff when unreachable), a notification list and an optional desktop notification, and finished VGT outputs imported once as a run (ADR 0043).
- [x] Host quota and run-directory sizes; job efficiency from sacct (elapsed, MaxRSS against the request, CPU when accounted) offered when running again, applied only on a click; sacct columns a site lacks fall back without affecting state tracking (ADR 0045).

## Stage 0 — Install and launch

- [x] Server serves the web app; Fedora RPM with launcher, desktop entry, icon and metadata (ADR 0021).
- [x] Release workflow: build the RPM in a Fedora container on a tag, run the install smoke test, attach it to the release.
- [~] Source RPM built in the build root, and a COPR repository so `dnf copr enable` works. Built: `make srpm` from vendored sources, verified to rebuild offline; `.copr/Makefile` builds from tags. Not done: the owner's COPR project (steps in `packaging/COPR.md`), a mock build.
- [ ] Flatpak: decision proposed in ADR 0052 (ssh through the agent socket, host paths through the document portal); not built.
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
- [x] Histogram of the visible plane or of a region (drawn or saved), with draggable and typed colour limits.
- [x] Scalar difference map between two compatible maps with the same declared unit (ADR 0017 amendment); a map declared axial uses the axial difference.
- Channel animation: dropped for now.
- [x] Saved, named regions: create, rename, change shape, duplicate, hide, delete with undo, export as DS9 (ADR 0026). DS9 import in image coordinates and in sky coordinates (FK5/ICRS/galactic circles and unrotated boxes, placed with the file's WCS and listed as approximated) with a preview.
- [x] Exploration history and "continue from here" (Q96–Q98): per-project steps, continue branches without deleting later steps (ADR 0057).
- [x] Linked views can be grouped and unlinked by the user; linking through a validated transformation (Q174): see Stage S.
- [x] Keyboard pixel cursor (Shift+arrows) and a table alternative for the spectrum plot (Q176).
- [x] Raw FITS header cards viewable and exportable (Q211).

## Stage 4 — Runs, parameters and reproducibility

- [~] Typed run metrics with units and uncertainty are in (stage L); cross-run filtering and plotting (Q128–Q131) are not.
- [~] Parameter sweeps and "explore variations" as a set of linked runs (Q128, Q222): VGT sweeps built (drafts read before submitting, concurrency limit, exactly once per job, ADR 0048); ROHSA reruns and sweeps edit the config line by line with a sizing suggestion from the owner's submit script, one job at a time by default; ROHSA fits on a host import, index on the host and auto-import when a job ends (ADRs 0046, 0048).
- [x] Reproducibility facts beyond the git flag, each declared, inferred or unresolved (Q121–Q125, Q198). The job captures its environment itself (code commit and diff hash, manifests, modules, node, input fingerprint), each labelled (ADR 0054).
- [x] Rerun modes: strict, best effort, adapted (Q123, ADR 0054).
- [x] Accounting of failed evaluations when the run reports them; cancelled and excluded are stated as not recorded (Q224).
- [x] Structured fields for controls: smoothing, noise model, how a control was constructed (Q246, ADR 0054); relative orientation records its null as verified.
- [x] ROHSA job template next to the existing VGT one (follows the pipeline's ROHSA cluster script; results are not imported as a run).
- [x] Beam matching (convolution to a common beam) as an explicit derived resource; never implied by reprojection (Q205).
- [~] Unit conversion with dimensional checks; frame transform between declared frames (Q204, Q209). Built for the viewer readout (ADR 0040): spectral units and velocity conventions, brightness K and Jy/beam with the file's beam, sky frames ICRS/FK5/Galactic, each with its basis or a refusal. Not built: conversions in figures, plots and the compare view; spectral reference frame changes.

## Stage 5 — Data model and catalog

- [~] Typed masks: low SNR, saturated, user excluded; masks as derived resources carried by outputs (Q201, ADR 0055). Applied in statistics, moments and map-vs-map relative orientation; not yet in region histograms and spectra, align, convolve, remote analyses or figure PNGs.
- [x] Interpretation tiers: add `verified` and `inferred` with a mapping version (Q211, ADR 0055).
- [x] Dataset identity independent of the path: several locations per dataset, content fingerprint, "newer result available" notice (Q83, Q84, Q136; ADR 0053): sampled and full fingerprints labelled honestly, verified/probable/declared links, verified downloads linked.
  - [x] First part (Q83): a registered file on this computer that changed (size or modification time) or vanished is flagged in the list and the inspector, checked at start, when the window returns and after registering; a changed file offers "Read it again" and the viewer restarts on it. Remote files are not checked (that waits on the network). Identity, fingerprints and several locations are not built.
- [~] WCS and unit corrections as a layer with history, never an edit of the original (Q143). Built: a unit declared for a file with no BUNIT (ADR 0038; audited, with staleness). Not built: correcting a unit the file does state, WCS corrections, a history view, sidecar import and export, impact analysis.
- [x] Filter boxes in the explorer: datasets by name, unit, host or nature; runs by parameters (key=value, <, >, <=, >=), QC and text (Q19–Q21, Q163). A sortable table is still to do.
- [x] Delete and archive with an impact preview (Q145, ADR 0041): datasets, runs, figures and boards; files on disk are never deleted; published reviews keep their frozen copy; a schema guard test keeps delete complete.
- [ ] Virtual subsets (a definition, not only a materialized cutout) (Q141).
- [x] Generic import: scan a folder, review the proposed mapping, save the rules (Q17, Q82; ADR 0058). Rules by glob or regex, first match wins, roles for datasets with units, orientation maps, catalogs and typed masks; declarations at tier "declared" with the rule named; project.toml written only on confirmation; rescans count new matches and never register on their own.
- [x] Host aliases from your ssh config are offered while adding a host (alias, plain host name and user only; keys, proxy commands and wildcard patterns are never read out; `--ssh-config` changes or disables it) (Q27).
- [x] Transfers with a resumable queue and conflict handling (Q36–Q38): rsync with partial resume across dropped connections, name reservation, conflict choices that never overwrite silently, sha256 verified on both ends (ADR 0051). No "Download" button on a remote dataset yet (paths are typed or completed).
- [~] "Prepare for offline" (Q108, ADR 0059). Built (v1: navigation, selected planes and spectra, figures): from a dataset on a host or a board, choose a channel range, preview or full-resolution planes and a byte budget; the sheet lists each item as works offline, works partially or needs the remote, with the reason and the estimate. Only bounded derived replies are copied (view, header, planes, saved-region spectra) into an offline store under the data directory (schema 22, size limit setting, forget, removed with the dataset and named in the delete preview); board pictures are already local and are not copied. When the host is unreachable the view, header, plane and spectrum routes serve a copy only if it was made from the file signature last seen (size, mtime, identity), marked as a copy (header, `offline` field, viewer banner with the time and file); anything not kept fails naming what is missing, and a stale copy is refused. Owner listener only. Not built: per-pixel coordinate readouts, slabs, position-velocity and analyses offline, preparing a file on a network mount, queued offline edits (Q109), a visual review of the sheet and banner; not tried over a real VPN.

## Stage 6 — Structure, contracts and operations

- [x] Contract layer, first part: an OpenAPI route list generated from the Go routes, with `make docs` failing on drift or on a web call to a route that does not exist (Q170, Q181). Bodies and generated TypeScript types (ADR 0032): datasets, plane frame, capabilities, figures, boards, the review surface and every route added since are described and contract-tested; `make docs` fails if a new route lands undescribed (`packages/contracts/undescribed.txt` only shrinks).
- [x] `packages/contracts` exists. `packages/science-types`, `extensions/astronomy` and `integrations` are retired from AGENTS.md as structure to create now: the semantic code lives in the worker and the Go packages, and is split out when a second discipline or connector makes the seam real.
- [x] `GET /api/capabilities`: application, protocol and schema versions reported separately, and which features this server was started with (planes, hosts, jobs, analysis, runs, boards, regions, audit), so a client shows a missing feature instead of failing on a 404 (Q155, Q158). No client uses it yet.
- [x] Workload classes: reserved interactive capacity, queue reasons shown to the user (Q169; ADR 0056). Interactive, heavy and background budgets; header reads a person waits on run interactive; one full file hash at a time; request ids in the UI and a redacted support bundle.
- [x] Every API request gets a correlation id (the caller's `X-Request-Id` when it is a plain token, else generated) returned in the response, and one JSON log line (method, path, status, size, duration; never the query string); quiet by default (failures and slow requests), `VESQIRA_LOG=debug` shows all. The id reaches the scientific workers, whose failure and slow-request log lines name it; it is not shown in the UI, and the support bundle is not built (Q158, Q163).
- [x] Dependency boundaries enforced by tests: a layering table for the Go packages and the reviewer-reachable set (`internal/arch`), and the reviewer page's imports in the web app (`boundaries.test.ts`) (Q180).
- [x] Command registry with undo for board and review edits (Q175, ADR 0057); board edits guarded on the server; publishing is never undoable.
- [x] Figures stored as a re-renderable specification, with SVG export (Q186, Q187): re-renderable scenes and SVG export.
- [x] Board layout grid (Q10–Q13, ADR 0060, schema 21): 12 columns of fixed-height rows, panels placed by column, row and span, snapped while dragged or resized from the corner, never overlapping and compact; saved as the board's working state and one undoable step per change; every move and resize also from the keyboard (arrows, Shift for resize, spoken result); the presentation, printed page and reviewer follow the saved reading order, the reviewer read-only; narrow screens stack. "Arrange automatically" puts channels in columns and methods (run kind) in rows from what the figures record, and says what is missing instead of guessing. Not built: grouping and annotation shapes on the board, board templates, the Analysis mode with synchronised viewers, a bulk-add flow to attach the arrangement to.
- [ ] Map every case of the scientific acceptance pack to a test id once the pack is available (35 cases).

## Stage 7 — People and access (design first)

The owner chose a shared multi-user server (ADR 0028 option B). Its design ADR comes before any code.

Needs an ADR before code: it changes the security boundary.

- [ ] Accounts and roles per project and per cluster (Q29–Q31). Design proposed in ADR 0050 (no code until accepted; open questions for the owner listed there).
- [ ] Authentication before anyone but the owner can act; delegation and approval lifecycle for other users' requests (Q44, Q71).
- [~] Worker memory cap and the authorize-then-open window for source files. The memory cap applies to every worker entry point and its setting reaches the workers; the authorize-then-open window is not closed (ADR 0050 specifies opening by component without following links).

## Stage 8 — Later phases (kept, not scheduled)

Investigations, hypotheses and meetings (Q47–Q51); source catalogs, crossmatch and feature associations (Q212, Q213); coverage, sensitivity and non-detection context (Q214); evidence collections, claim links and experiment proposals (Q215–Q226); BibTeX, DOI and arXiv records; events, webhooks, SDK and MCP adapter (Q170, Q171); calibration registry and time coordinates (Q202, Q203); Git manifests (Q86); project dashboard and draggable tabs (Q8, Q9).

## Not decided by code

- Trademark and domain clearance for the name.
- Whether the repository becomes public, and removing an earlier private document from history (needs a force-push).
- Owner review of ADRs 0007–0018.
