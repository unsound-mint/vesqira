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
- [x] Measured on 2026-11-13 (docs/performance.md, `make bench-disk`; AMD Ryzen 9 7940HS, 14 GiB RAM, Micron 2400 NVMe, btrfs, Linux 7.2, shared with other work at load average 7 to 60, so ranges over four runs are given): cold and warm reads on a 5 GiB dense cube, the same reads through a real ssh to a user-mode sshd on loopback, 4D cubes (length-1 STOKES axis reads like 3D; a Stokes axis longer than one is refused), peak memory and reply bytes per row, and an interrupted-and-resumed 1 GiB download. Resumable transfers themselves were already built (ADR 0051) and tested through the ssh stand-in (resume after a dropped connection, after a closed app, cancel, no overwrite, hash on both ends, one transfer per host); the measurement shows a resumed 1 GiB download re-sending only the missing part.
- [x] The dataset list and the run list draw only the rows near the viewport above 200 rows, with `aria-posinset` and `aria-setsize` and keyboard movement by index; 5000 datasets render in 44 ms (was 1020 ms) in vitest and the page holds 738 nodes with 5000 datasets and 5000 runs in Chromium.
- [x] The cold first spectrum (1.3 to 2.3 s for a 10 x 10 box, 2 to 4.5 s for 187 x 187 on the 5 GiB cube): the cause was a 4 MiB readahead per page fault (2400 MiB read for a 36 MB box). Fixed with madvise hints (docs/performance.md): 21 ms and 34 MiB read, and 362 ms and 642 MiB. A download killed by SIGKILL of rsync now resumes from its orphaned temporary file.
- [ ] Still open in Stage P: no measurement over a real network link (latency, bandwidth, a dropped VPN): ssh was loopback only. A Stokes axis longer than one is not supported. An upload killed by SIGKILL of rsync restarts that file (the temporary is on the host, which Vesqira does not delete from). Resume costs a checksum pass (140 to 380 MiB/s of kept data), so on a link faster than that a restart is quicker: not an issue for a VPN.

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
- [x] Path fields with zsh-like completion (local and on hosts) and the desktop file chooser, with GVFS sftp mounts mapped to hosts (ADR 0031). Built: names-only completion, Browse through the xdg-desktop-portal chooser first and zenity or kdialog after it, and "Add this host…" for a folder in a GVFS mount of an unregistered host (pre-fills the add-host form, adds nothing). Proposed; implemented, awaiting owner review (ADR 0031 amendment). Not tried on a real desktop portal or in a Flatpak; the portal dialog has no parent window.
- [x] Interactive filters: the run and dataset filter boxes complete keys, operators and values that occur and show conditions as chips (`ui/FilterBox`, vocabularies from the loaded items; same query string as before).
- [x] `/api/runs` input hints computed from one catalog read instead of one per run (148 runs, 1 350 files is the target scale); a bench case at that scale. Done: `catalog.HintIndex`, and a server test at 150 runs and 1 500 datasets that asserts one read.

## Stage R — Reports for the supervisor (owner direction, 2026-11-10)

Replaces the owner's Notion reports. ADR 0036 (proposed): reports on the owner's instance, drafted by the agent over MCP, published by the owner to an always-on publish server where named reader links read and comment. Reports mock: direction B (linked pages, with a graph) approved by the owner on 2026-11-10, with this brief: the supervisor rarely edits, presentation is what matters, so the cleanest possible hierarchy (one title, a quiet meta line, one primary action; threads in a side panel that never covers the text).

- [x] Reports in the catalog: Markdown body, edit history with author, base-revision conflicts, figure, run and report references, backlinks (a rename rewrites links), attachments (PNG, JPEG, GIF).
- [x] `vesqira mcp` (stdio) and `vesqira report` CLI for the agent: read and draft, no publish, send or resolve (a test pins the tool list).
- [x] Owner UI for reports and the reader page (mock B): page tree, a page drawn as readers see it and edited part by part in place, Markdown source view, insert figure or run from what exists, backlinks, comments panel with agent-drafted replies to send, Share (publish server, named reader links), publish preview, block-by-block review of the agent's changes; reader page with revisions and comments beside the text. Not yet: placing a comment on a figure from the reader page (the publish server and its API accept a figure-anchored comment, ADR 0036 and a test pin that; only the reader page has no way to mark a place, unlike board reviews, ADR 0023), a reader-side graph, a second thread on a part that has one.
- [x] `vesqira publish-server`: publisher key, spaces, named reader links, published revisions, block-anchored comments, sync with the owner's instance (pull every minute), preflight, owner replies, agent-drafted replies sent by the owner. Reader page: with the UI.
- [x] `vesqira import notion` for a Notion Markdown & CSV export (zip or unpacked folder), with a dry run. PDFs and images over 4096 px are not imported.
- [x] Deployment docs for the owner's server (`docs/setup/publish-server.md`: systemd unit, Caddy with TLS, backups).
- [x] Graph view of report, run and figure links, with a keyboard list alternative; unlinked reports hidden by default.
- [x] Pages and presentation (ADR 0036, "Pages, front matter, changes and deletions"): page tree kept from the Notion import, front matter date and summary under the title, reader list newest first, what changed since the revision last read, comments on selected words, withdraw a report, delete a comment.
- [x] Follow-ups from the security review (ADR 0036, "Hardening"): image metadata stripped when publishing (`internal/imgmeta`, pixel-checked, fuzzed), fixed shape for derived `software`, bounded Markdown scans (one was cubic), comment-cursor guard. Left: a schema for derived `summary`.

## Access labels (GitHub issue 135; ADR 0069, implemented)

- [x] A per-dataset label (released, collaboration only, unreleased) with an audited history, inherited by figures, boards and reports from the recorded dependency edges, shown as text and icon, confirmed on publish, stated on the review page, and blockable per workspace by an admin on the group server; pushed work carries its label and the server applies its policy (schema 34).
- [x] A run's outputs inherit the label of the cube it read, inferred from the run's input name and size (the job's exact input dataset when it recorded one); the inference only adds restriction. A run that records its input dataset id would make it exact (ADR 0069).

## Tool profiles (owner direction, 2026-11-13; ADR 0068)

Vesqira gives tools, not one group's pipeline. VGT.jl, ROHSA and ROHSA-LMC become declared profiles, and other tools plug in the same way.
- [x] 0: template drift fixed (VGT.jl config keys, ROHSA-LMC repository label)
- [x] 1: analysis presets; results stop citing the owner's script lines
- [x] 2: orientation conventions from a header-card profile; "unresolved" instead of an assumed default
- [x] 3: run detection and generic output roles (schema 35; `vgtHeader` goes in schema 36)
- [x] 4: format-neutral decompositions (ROHSA `.dat` and FITS maps, also read on a host per pixel; HDUs by name and EXTVER; a cell click uses the pixel index for maps too); no schema change, GaussPy+ not shipped (layout not verified). Review of a run kept on a host stays local-only
- [x] 5: run templates as data (steps, typed keys, command, probes, progress); a synthetic tool runs through the fake Slurm path; schema unchanged

## Review rounds (issue 130, ADR 0073, awaiting owner review)

- [x] A board's snapshots are its rounds. A later round embeds, frozen at publish, what changed since the round before: figures added, removed or redrawn, title, caption and note changes, corrections and masks (from the frozen provenance only), access label and run-state changes. The reviewer sees "Changed since <date>" and a New / Redrawn / Unchanged marker in words on each figure.
- [x] Comments carry over, read-only and labelled with their round, onto the same figure (same revision, or the same drawing redrawn). The earlier picture of a redrawn figure is shown only while the earlier link is valid; a revoked earlier round's text and comments are withheld.
- [x] Owner: round numbers and the per-round change list on the board's snapshots, and the round a publish would make in the preflight.
- [ ] Not done: a figure replaced by a different figure with no draw-again link counts as removed and added; matching by title or by data would be a guess.

## Spectral smoothing and the family view (issue 133, ADR 0070, awaiting owner review)

- [x] Spectral smoothing of a cube as a derived operation: boxcar, Gaussian or Hanning, width in channels (in the spectral unit only on a calibrated axis), kernel and minimum valid weight recorded, NaN kept, no in-place change, optional recorded decimation, the effective resolution stated only from a declared native response.
- [x] Family view: maps that differ in one parameter side by side on a shared scale, each compared with a chosen reference (scalar difference and spread, or axial difference and AM for orientation maps), refused with a sentence when the grids are not shared. A family is recognised from derived records and can be picked by hand.
- [x] Open a family from a sweep of jobs: the outputs of one chosen run role, labelled by the parameters that differ; jobs with another grid, frame or unit, no result or an undeclared orientation are listed with the reason and not compared.
- [x] A family of maps on a host, compared by the host's worker where the files are (bounded reply, all-or-none placement of the results, cancellation, a clear refusal when the host cannot); nothing is copied here.
- [x] Clean-up of family maps: list them by run and remove the ones nothing uses; a map used by a figure, board, report, published review or a derived dataset is kept with the reason; sources are never touched; no automatic policy.
- [ ] Not built: deleting the result files on a host, a real-host run, a browser test that opens a sweep, a design review by a second person.

## Sky context (issue 134, ADR 0072, awaiting owner review)

- [x] Sky panel in the inspector: footprints of registered images from their celestial WCS (corrections applied) in ICRS or Galactic, computed by the worker with astropy; datasets without a celestial WCS, catalogs and HEALPix maps listed with the reason and never drawn.
- [x] The viewer cursor (hover or pinned pixel) shown as a world position, and a click on an outline or list entry opens the dataset.
- [x] Opt-in survey background (Aladin Lite 3.8.2, bundled, loaded after opt-in): off by default, the contacted server and what it receives stated before it is contacted, context only, stored per browser. Works fully offline without it.
- [x] Local PNG of the panel; the survey is in it only when ticked for that file, with its name, HiPS id and provider in the caption.
- [ ] Not built: a survey background inside a registered board figure with HiPS provenance, footprints for virtual subsets, a check against the live survey servers, a second person's design review.

## Stage S — Scene, cross-tracer comparison and the researcher's loop (owner direction, 2026-11-11)

The owner asked for the most convenient app for this kind of work, beyond the interview list. Items come from the owner's own research (VGT and ROHSA on LMC H I and CO, Slurm over a VPN, weekly reports) and from common radio-astronomy practice (contours, vectors, channel maps, PV diagrams, relative-orientation statistics). Design: ADR 0042. New screens do not wait for an approved mock during this push; the owner reviews afterwards.

- [x] Declared angle frame and quantity (pixel or sky; B-field, gradient, dust or starlight polarization), and axial-aware resampling for align and convolve (fixes the shipped linear interpolation of axial maps); previews of axial maps stay disabled.
- [~] Viewer scene: base plus ordered overlays, placed by WCS geometry, not resampling. Built: the scene/1 contract and viewer store (figures store their scene, never shown to reviewers), and overlays from other datasets: contours (levels incl. k×σ by MAD), orientation lines, beam ellipse, coordinate grid, each stating its basis or why it is not drawn. Catalog markers and B-field vectors from point catalogs (row filter shared with relative orientation, click a marker for its row, a keyboard list of sources). Linked views by WCS and redrawable figures with SVG export are built (see below).
- [x] Point catalogs as datasets (CSV, TSV, whitespace tables, ECSV, IPAC, VOTable, FITS table; sexagesimal or degrees; declared frame and angle columns; parse report by line), e.g. starlight polarization and absorption sightlines (ADR 0044). On this computer only.
- [x] Relative orientation between two orientation maps or a map and a catalog: alignment measure (weighted too), projected Rayleigh statistic (no p-value: correlated pixels) and histogram, the owner's shifted-pattern null reproduced, an optional P/σP cut, deterministic seeds (ADR 0047). Remote inputs and block averaging are not built.
- [x] Relative orientation stratified by bins of a third scalar map (equal-count or user-set edges): per bin AM, the projected Rayleigh statistic, pixels, independent beams, the uniform-angle reference and the edges in the third map's unit (stated, never assumed); a plot with its provenance export, a table and CSV (ADR 0047 amendment, #132). Not built: a per-bin null, a P/σ_P ratio map as the third map, the summed-phase comparison.
- [~] Planck HEALPix polarization maps (issue 120, ADR 0067). Implemented and awaiting owner review:
  - registered as their own kind only when POLCCONV and COORDSYS are stated;
  - sampled onto a dataset's grid as Q and U (nearest, bilinear or Gaussian on the sphere), with the COSMO→IAU flip and the per-pixel turn from Galactic north with astropy;
  - `dust_polarization` or `b_field` maps declared by the operation, with P and σP companions and a P/σP threshold;
  - correlated noise between HEALPix pixels is stated as not modelled.
  Not built: a screen for sampling (API only), remote sampling, spin-2 harmonic smoothing, and a test on a real Planck file.
- [x] Linked view groups by sky position and spectral value (ADR 0042 §4): the worker's affine over the visible region with its stated error; channels by spectral value with the residual; SPECSYS must match for a channel link (same-grid pairs too).
- [x] Figures as scenes: draw again (data and declaration changes announced first; a redraw is a new linked figure), SVG export from the same marks (ADR 0042 §5).
- [x] Channel-map grid (shared scale, converted spectral labels, slab means) and position-velocity diagram along a line or polyline (offsets as sky separations; bilinear or nearest sampling, stated) (ADR 0049).
- [x] Gaussian decomposition browser for ROHSA results: component curves over the spectrum at a pixel (streamed index of the .dat), component and residual maps, CNM versus the rest with the width cut as a declared input (ADR 0046).
- [~] Review of a whole ROHSA decomposition (issue 131, ADR 0046 note): a sigma-v diagram (width against centroid velocity, weighted by integrated intensity, optically thin column density or component count, user bins, contributor counts, log scale, SVG and PNG with provenance), the pixels behind a clicked cell marked on the maps, and one derived map per width class (the cut or user edges; NaN where nothing valid was fitted, 0 where nothing of the class is) registered with the run, the weight and the edges; a map pixel opens its spectrum with the components. Local fits only; the diagram and the class maps each read the whole `.dat` (minutes for 20 GB, measured only on a synthetic file), a cell click reads only the 2-bytes-per-row index the diagram wrote; the column-density constant is not checked against the pipeline; no design review, no browser check.
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

- [~] Typed run metrics with units and uncertainty are in (stage L). Cross-run table and plot (Q19–Q21, Q128–Q131, ADR 0062, schema 24): every run as a sortable table (status and host from the job, any parameter or result as a column, newest import first, runs that did not record a value say "not recorded" and sort last), filtered by the same query as the explorer (ranges `block_size:16..64`, dates, status, a unit on a result `am>0.3rad`; runs the query cannot judge are listed apart and can be shown last), saved views (query, columns, sort, plot axes) kept in the catalog, CSV export with units, uncertainty, its kind and the query in a header, and a plot of a result against a parameter or another result coloured by a parameter (marker shapes as well as colour, one kind of error bar per series, results in other units or defined differently on their own panel labelled not directly comparable, hollow for inferred, point and row selection linked, SVG and PNG with the query and run ids in their provenance). Unit conversion for a panel that differs only by unit (worker `convert_values`, astropy dimensional conversion with no equivalency, so K to Jy/beam and km/s to Hz are refused; converted points marked, the CSV keeps the recorded numbers beside the converted ones; ADR 0062 amendment) and the plot as a board figure (a figure kind with no dataset, schema 28, owner-only description of the runs, reviewer-safe words and counts, Draw again reads the runs again and announces what changed; ADR 0065) are implemented and await owner review. Not built: ranking against a stated objective, a design review of the screen, trying it on real VGT and ROHSA runs.
- [~] Parameter sweeps and "explore variations" as a set of linked runs (Q128, Q222): VGT sweeps built (drafts read before submitting, concurrency limit, exactly once per job, ADR 0048); ROHSA reruns and sweeps edit the config line by line with a sizing suggestion from the owner's submit script, one job at a time by default; ROHSA fits on a host import, index on the host and auto-import when a job ends (ADRs 0046, 0048).
- [x] Reproducibility facts beyond the git flag, each declared, inferred or unresolved (Q121–Q125, Q198). The job captures its environment itself (code commit and diff hash, manifests, modules, node, input fingerprint), each labelled (ADR 0054).
- [x] Rerun modes: strict, best effort, adapted (Q123, ADR 0054).
- [x] Accounting of failed evaluations when the run reports them; cancelled and excluded are stated as not recorded (Q224).
- [x] Structured fields for controls: smoothing, noise model, how a control was constructed (Q246, ADR 0054); relative orientation records its null as verified.
- [x] ROHSA job template next to the existing VGT one (follows the pipeline's ROHSA cluster script; results are not imported as a run).
- [x] Beam matching (convolution to a common beam) as an explicit derived resource; never implied by reprojection (Q205).
- [~] Unit conversion with dimensional checks; frame transform between declared frames (Q204, Q209). Built for the viewer readout (ADR 0040): spectral units and velocity conventions, brightness K and Jy/beam with the file's beam, sky frames ICRS/FK5/Galactic, each with its basis or a refusal. Not built: conversions in figures, plots and the compare view; spectral reference frame changes.

## Stage 5 — Data model and catalog

- [~] Typed masks: low SNR, saturated, user excluded; masks as derived resources carried by outputs (Q201, ADR 0055). Applied in statistics, moments, map-vs-map relative orientation, and region histograms and spectra (per-mask policy, built in #94). Carried, by the researcher's per-mask choice and with a stated rule, through cutout, slab, virtual subset, align and convolve (the result's no-value mask is computed again, never carried), and drawn with a legend on saved figures (PNG and SVG), recorded in the figure's scene and announced by draw again when a mask changed or was forgotten. On a host (ADR 0064, schema 26): masks are made and kept on the host next to the file, applied by its worker (region spectrum and histogram, hatch) and by statistics and moment jobs, never crossing the VPN. Not yet: carrying masks into a result on a host, masks carried through moment maps and differences, a masked region spectrum as a figure input.
- [x] Interpretation tiers: add `verified` and `inferred` with a mapping version (Q211, ADR 0055).
- [x] Dataset identity independent of the path: several locations per dataset, content fingerprint, "newer result available" notice (Q83, Q84, Q136; ADR 0053): sampled and full fingerprints labelled honestly, verified/probable/declared links, verified downloads linked.
  - [x] First part (Q83): a registered file on this computer that changed (size or modification time) or vanished is flagged in the list and the inspector, checked at start, when the window returns and after registering; a changed file offers "Read it again" and the viewer restarts on it. Remote files are not checked (that waits on the network). Identity, fingerprints and several locations are not built.
- [x] WCS and unit corrections as a layer with history, never an edit of the original (Q143; ADR 0061, schema 23). A unit declared for a file with no BUNIT (ADR 0038); a unit the file states and WCS keywords (CRVAL, CRPIX, CDELT, CD/PC, CTYPE, CUNIT, RESTFRQ/RESTFREQ, SPECSYS, EQUINOX, RADESYS) corrected at tier declared with a required reason and optional source, applied in memory by the worker (which echoes what it applied), validated by astropy with axes keeping their kind and roles; a before/after preview of the centre and corners; an append-only history in the inspector (revert is a new record; stale for a changed file, applied nowhere until confirmed); an impact list (live views re-read, figures and derived results pinned, Draw again announces it); a versioned JSON sidecar with identity-checked import; reviewer provenance with the tier and reason. Analysis jobs on a host receive the corrections and must echo them before their result is registered (ADR 0064). Not built: corrections in VGT and ROHSA jobs, SIP/TPV and LONPOLE/LATPOLE/CROTA, per-keyword revert, a spectral frame transform.
- [x] Filter boxes in the explorer: datasets by name, unit, host or nature; runs by parameters (key=value, <, >, <=, >=), QC and text (Q19–Q21, Q163). A sortable table is still to do.
- [x] Delete and archive with an impact preview (Q145, ADR 0041): datasets, runs, figures and boards; files on disk are never deleted; published reviews keep their frozen copy; a schema guard test keeps delete complete.
- [x] Virtual subsets: a definition, not only a materialized cutout (Q141; ADR 0063, schema 25). A saved window onto a dataset (a box in the source's pixels or the box of a saved region, every N-th pixel, a channel range or a world interval with its unit and frame resolved to channels by a stated rule against the WCS in force) with a name, a reason and an append-only history of versions, each pinned to the source file's identity. Viewed without writing anything: the view, plane, coordinate, spectrum and header routes read the source through the window in the worker (only the window is read; coordinates in the subset's own pixels with CRPIX shifted and the CD or PC matrix scaled for a step, equal to a materialised file's readouts), and a worker that does not apply the window is refused. A changed source makes it stale, not shown as current, with a re-pin that records a new version. Written as an ordinary FITS file on demand (shifted WCS, corrections as HISTORY, BUNIT, a derived record naming the subset, version and source identity), reused for the same version, bytes, corrections and unit. Listed under its source in the explorer marked virtual; the delete preview lists the subsets. Opens in the plane viewer through its window (view, plane, coordinate, spectrum). Not built: masks viewed on a subset (a file written from one can carry the masks chosen, ADR 0055), saved regions, overlays, channel maps, position-velocity, history, figures and analyses on a subset, carrying masks into one written on a host, subsets in boards. A subset of a host file is written on the host and registered there (ADR 0064).
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

- [x] Accounts and roles per workspace (Q29–Q31), ADR 0050 slices 1 to 3.
- [x] Push from a local instance (slice 4, schema 32) and run requests relayed to the key holder's own instance, approved there against the exact script (slice 5, schema 33), specified in [ADR 0066](adr/0066-push-to-the-group-server-and-relayed-run-requests.md).
- [x] `multiuser import` of a local catalog into a new group workspace: all or nothing, paths rewritten only through explicit `--map` prefixes, `--dry-run` lists the same plan (ADR 0050 slice 4).
- [x] Sign-in before anyone but the owner can act, and an approval lifecycle for other people's run requests (Q44, Q71), ADR 0050 slices 2 and 5.
- [ ] Preapproved templates and budgets for trusted collaborators (deferred in ADR 0050; every request needs an explicit approval today).
- [x] Worker memory cap and the authorize-then-open window for source files. The memory cap applies to every worker entry point and its setting reaches the workers. Every source read goes through `srcopen.py` in the worker and `sourceroot.OpenFile` in the API: the resolved path is opened without following a link, so a component swapped after the check is refused (ADR 0050, section 8). The device and inode a location had when it was recorded (schema 27) are compared when a dataset is read, and sent to the worker with the plane requests, which refuse another file with a sentence; the other worker ops do not carry them yet. The file tree opens its folder without following a link and shows links with their target and where they point; the worker's own listings (`walk`, `list`, `canon`) and the path completion still use path names.

## Stage 8 — Later phases (kept, not scheduled)

Investigations, hypotheses and meetings (Q47–Q51); source catalogs, crossmatch and feature associations (Q212, Q213); coverage, sensitivity and non-detection context (Q214); evidence collections, claim links and experiment proposals (Q215–Q226); BibTeX, DOI and arXiv records; events, webhooks, SDK and MCP adapter (Q170, Q171); calibration registry and time coordinates (Q202, Q203); Git manifests (Q86); project dashboard and draggable tabs (Q8, Q9).

## Not decided by code

- Trademark and domain clearance for the name.
- Whether the repository becomes public, and removing an earlier private document from history (needs a force-push).
- Owner review of ADRs 0007–0018.
