# ADR 0059: Prepare for offline

Status: **proposed; implemented, awaiting owner review.** Decided by the implementing agent under the owner's delegation of ADR decisions (roadmap Stage 5, "Prepare for offline", Q108; v1 scope: cached figures, selected spectra, navigation). The preparation sheet, the Offline section and the offline banner have had no approved design direction and no visual review.

## Context
Cubes live on a cluster reached over a VPN that drops often. Today every view of a remote dataset fails once the host is unreachable: `authorize` stats the file through the host's worker (`authorizeRemote`) before anything is read, and the plane cache is in memory only. A researcher on a train can still open a board (its pictures are local) but not look at one plane of the cube behind it.

AGENTS.md forbids silently serving data that may not be the file that was catalogued (path is not identity; ADR 0053), forbids a copy that looks like the original, and keeps the reviewer away from anything indirect. An offline copy of a derived reply is a *derived resource with a stated source*, not evidence and not the file.

## Decision

### 1. What is kept, and only that
"Prepare for offline" copies **bounded, derived replies of the scientific worker**, never the cube:
- the view (`view`) and the header listing (`cards`);
- the planes the researcher chooses: a channel range at the **preview step** (the reduced plane the viewer shows first) or at full resolution, under a byte budget shown before starting;
- the mean spectra of the dataset's saved rect and circle regions;
- nothing else. Per-pixel coordinate readouts (`coord`), conversions, slabs, position-velocity cuts, histograms and analyses are not kept and still need the host.

Each reply is stored exactly as the worker returned it (`Head` JSON, `Payload` bytes). It is produced by the **real handlers** (`view`, `headerCards`, `plane`, `spectrum`, called in process with a request context that makes `ask` hand over its reply), so the stored key, the validation (an orientation map is only ever kept whole; a reduced preview of one is refused as it is live; an axial map gets no arithmetic-mean spectrum) and the bytes are those of a live answer by construction. When "full" is chosen the preview-step plane is kept as well (it costs 1/step² of the full one), because the viewer asks for the preview first. The preview step is `previewStepFor(w, h)`, the same formula as the web client's `previewStep`; a test pins the values and each side names the other.

Board **figures' rendered outputs are not copied.** They are already files under the data directory, served by `GET /api/figures/{id}/preview` with no host involved; copying them would only create a second version that could drift. The plan lists each figure as "works offline" with that reason, and says that redrawing it from the data works only for the planes kept (the redraw reads the same view and plane routes). A job item on a board is "works partially": the last state Vesqira saw is kept in the catalog, refreshing it needs the host. A dataset whose file is on this computer is "works offline: nothing copied".

### 2. The three-way list
One function (`classifyOffline`) says what a set of copies gives, for the plan (what the preparation would leave, existing copies plus the new ones) and for the result (what is actually kept):
- **Works offline**: the view, the header and every channel at full resolution are kept. Browsing works. Even then the reason says what still needs the host: spectra of regions drawn later, coordinate readouts, analyses.
- **Works partially**: something usable is kept. The reason names it ("Kept: view; header; planes 120–180 (1/4 preview); 2 region spectra. Needs the host: full-resolution planes, planes of other channels, spectra of regions not saved when this was prepared, coordinate readouts and analyses.") and, after a run, why it stopped early (the byte budget, the host stopped answering at channel N, the store limit).
- **Needs the remote**: nothing usable is kept, or the dataset cannot be read now; the reason is the host's own message.

A copy never reads as a full copy: the words "works offline" are reserved for a complete set of planes and the reason always lists what is not there.

### 3. Using a copy
`source` has a second form (`sourceWith(..., offlineOK)`) used by exactly five routes: view, header-cards, plane, spectrum and coord. When `chooseRead` fails with the **explicit unreachable signal** (the host did not answer, or a stat of a local file failed with a network-mount errno: `ESTALE`, `EIO`, `ETIMEDOUT`, `EHOSTDOWN`, `EHOSTUNREACH`, `ENETUNREACH`, `ENETDOWN`, `ECONNREFUSED`, `ECONNRESET`), and the dataset has any copy, `source` returns a key carrying the signatures the catalog last saw for the dataset's readable locations (host, size, mtime, content identity when recorded) and no path. `ask` then answers from the store instead of the worker and the cache:
- the copy must match the key (operation, channel, step, region, declared unit) **and** one of those signatures, must not be stale, and, when a content identity is recorded for the location now, must carry the same identity;
- an answer is marked: header `X-Vesqira-Offline` (JSON: `copy`, `takenAt`, `signature`) and the field `offline` in the JSON head of the reply (OpenAPI `OfflineMark`). The payload bytes are identical to the live answer; the head is identical except for that field (a test compares both). Offline replies never enter the plane cache;
- something not stored fails with **503 `offline_missing`** and a sentence naming what was asked and what is kept ("channel 30 at full resolution is not in the offline copy of lmc.fits. Kept: view; header; planes 10–12 (full resolution). The host is not reachable (is the VPN up?); the rest needs it."), not a generic error;
- a stale or identity-mismatched copy fails with **409 `offline_stale`**;
- a dataset with no copy keeps today's failure (502, "the host is not reachable"). Routes outside the five never answer from a copy.

The host going away *between* the stat and the read (the worker call fails with a network error or times out for a file on a host) falls back the same way, for the exact signature just verified.

**What "last seen" means.** The catalog's recorded signature of a location is the last one a live look accepted (a live look that finds a different file refuses with 409 and does not change the catalog). Every live stat of a remote file also tells the store what was seen (`NoteSeen`): copies made from another signature get `seen_*` set, are **stale**, are kept so the researcher sees that they are, and are never served. A file that changed on the host with no live look since cannot be detected while offline; the copy is then the last accepted file and is marked with its signature and time so the banner says exactly which file it is. This is stated in the banner ("from the file as it was then"), not hidden.

**Speed.** A dead VPN costs one ssh connect timeout (10 s) per request, which makes a scrub unusable. A host that just failed to answer is recorded in the in-memory reachability state, and for 15 s a dataset that *has an offline copy* skips the connect and goes straight to the copy (`offlineRetry`). Datasets without a copy are unchanged. A successful stat records the host as reachable, so the window ends as soon as the VPN is back, at the cost of at most one request served from a copy after it returns.

### 4. Store
- **Files** under `<data-dir>/offline/<dataset id>/<hash>.bin` (0600 in 0700 folders), written to a temporary file and renamed. Each file holds the head length, the head and the payload; its SHA-256 is in the index and is checked on every read, so an altered or truncated copy is removed and never served. The directory is **not a source root** and is not in the backup archive (`internal/backup` lists the folders it keeps): a copy is rebuildable only while the host answers, so a restore starts without them and the Offline section says so.
- **Index** `offline_entries` (schema 22): one row per kept reply, unique on dataset, host, size, mtime, op, channel, step, pixel, region description and declared unit; plus identity, bytes, SHA-256, `taken_at` and the `seen_*` triple. `offline_settings`: one row, the size limit (default 2 GiB). Both are created by the store for a fresh install and by the migration for an upgraded one; the snapshot test and the full-schema test cover them.
- **Bounded.** Preparing refuses what exceeds the budget or the room left (`409`, with the numbers) before it copies anything, and stops at the budget during the run (a copy that would pass it is not written; the item says so). Nothing is evicted silently. Lowering the limit below what is kept deletes nothing; the next preparation is refused until something is forgotten. A newer preparation of a changed file replaces the copies made from the old signature (`Supersede`).
- **Forget** removes a dataset's copies, rows first and then files (`POST /api/offline/forget`). **Deleting a dataset** removes them with it: `deleteSteps` and `optionalTables` in lifecycle, files removed after the commit, and the ADR 0041 impact preview names them (group `offline`, "removed"). A folder with no rows (an interrupted delete) is swept at start.
- **No secrets.** Entries are worker replies: the header cards the viewer already shows, pixel values, mean spectra. A test asserts that no host address appears in a served copy or its header.

### 5. Preparing
`POST /api/offline/plan` reads what the host says (the cube's layout, that the file is the catalogued one) and returns the three-way list, the estimate (`planeBytes` per plane at the step, spectra at 20 bytes a channel, a fixed allowance for view and header), the budget, the store's room and whether it fits. `POST /api/offline/prepare` plans again (the host may have changed since the sheet was shown), refuses what does not fit, and runs **one preparation at a time** in the background; `GET /api/offline/prepare` gives progress and the final items, `.../cancel` stops it (what was kept stays). `GET /api/offline` lists each dataset's copies (coverage in words, bytes, time, stale), `PUT /api/offline/settings` sets the limit. A board is prepared by naming it: its figures are listed, and the datasets they were drawn from become dataset items (the same channel range applies to each, clamped to its cube). All of it is registered only on the owner listener; the audit log records preparation, forgetting and limit changes.

### 6. Interface (inside existing surfaces)
- A **preparation sheet** (a modal like the delete dialog, loaded on demand): first and last channel (a single cube starts at its middle twenty channels), detail (preview planes, or full-resolution planes too), budget in MiB; the estimate against the budget and the store's room, in words ("About 3.8 MiB of your 256.0 MiB budget. The offline store has … free of …. It fits."); the three-way list with each status as a word beside its pill; progress and result after Prepare. The Prepare button is disabled until the plan fits.
- A dataset's inspector has an **Offline** section (collapsed): what is kept, when, whether it is current or stale, and "Prepare for offline…" and "Forget offline copy". A file on this computer says it needs none. A board has "Prepare for offline…" beside archive and delete.
- The viewer shows an **offline banner** whenever the last answer for the dataset came from a copy: that it is a copy, when it was taken, the file it was taken from (size and the identity when recorded), and that it cannot show anything newer; the server's sentence follows when something not kept was asked for. When only the reduced plane was kept the viewer keeps showing it (labelled "preview, reduced N× — from the offline copy") rather than replacing it with an error. The state is fed from the response header by `requestRaw`; a live answer clears it.

## Alternatives
- **Copy the cube, or large parts of it.** Violates the bounded-bytes rule and turns a derived preview into a second source of truth. Rejected; the budget is the guard.
- **A persistent plane cache (write-through) instead of a prepare step.** Hidden copying of whatever was viewed; no honest statement of what is kept; it would also keep entries for files nobody chose. Rejected for v1 (a later "keep what I viewed" can write to this store).
- **Copy board figures into the store.** They are already local; see Decision 1.
- **Capture by re-implementing the handlers' keying in prepare.** Two copies of the validation and the cache key drift. Rejected for in-process calls of the real handlers.
- **Serve copies whenever the host is slow, or on any 502.** 502 is also "answered unexpectedly" and worker failures; falling back there would hide real faults. Only the explicit unreachable signal does.
- **Evict the oldest copy to make room.** Silent loss of something the researcher prepared before leaving the office. Rejected; refuse and say why.
- **Store entries in SQLite blobs.** Planes are megabytes; AGENTS.md keeps scientific binary data in files, not database rows.

## Consequences
- A reviewer never reaches a copy: the routes exist only on the owner listener (`TestReviewerCannotReachOfflineCopies`), the review package cannot import the store, and a review snapshot never carried dataset ids.
- A copy is as old as the preparation. The banner and the Offline section show the time; staleness is only learned from a live look. A file that changed on the host while the VPN was down is served as the file it was.
- `source` and `ask` in `planes.go` gained a few lines (the offline form of `source`, the offline branch and capture hook of `ask`); the five routes call `sourceOffline`. `authorizeRemote` now records reachability and what it saw, and uses the unreachable constants.
- A local dataset on a flaky network mount shows the offline path when a stat fails with a network errno, but cannot be *prepared* (a local file is listed as needing no copy; Vesqira cannot tell a network mount from a disk). Deferred.
- Plan estimates are estimates (the view and header are a flat allowance); the budget is enforced on real bytes.
- Queued offline edits and their sync (Q109) are not built; this ADR is only about reading.

## Validation
- Go (`internal/server/offline_test.go`, with the real worker through the fake ssh that can be taken down): a prepared view, header, preview and full planes and a region spectrum are served with the host unreachable, payload bytes equal to the live answer and head equal apart from `offline`, the header carries the copy mark and no host address; a plane not kept is a 503 naming it; coord and another region's spectrum say they need the host; a route that is not one of the five is not answered from a copy; with the host back the answer is live and unmarked. A changed file (seen by a live look) makes the copy stale and it is refused with 409 while the host is down. Over-budget and over-limit preparations are refused with nothing copied; forget and dataset delete remove rows and files; the impact preview names the copy; the review listener answers nothing for any offline or dataset-read route. Responses match the OpenAPI spec. `internal/offline`: round trip, private files, tamper detection, the limit never evicts, stale and supersede, forget and the orphan sweep. `internal/schema`: the 19 to 20 migration; the snapshot and contiguity tests.
- Web: the sheet's three statuses as words, the estimate against the budget, the request it sends, a plan that does not fit disables Prepare; the Offline section for a local file and a stale copy; the banner appears from the response header, names the file, names what is missing in the server's words, goes when a live answer arrives and is per dataset.
- e2e (`e2e/tests/zzzzz-offline.spec.ts`): prepare channels of a cube on the stand-in host from the sheet, take the host away (the stand-in ssh refuses and the open session is killed), reload: the viewer shows the plane with the banner, a channel not kept names itself, and the banner is gone when the host is back.
- Not verified: behaviour over a real VPN and a real cluster; a visual review of the new sheet and banner at the required viewports (no approved design, no reviewer pass); keyboard walk-through beyond what the e2e spec does.

## Rollback/Migration
Schema 20 adds two tables and no column; the older program ignores them. Rolling back deletes nothing: the files stay under `<data-dir>/offline` and can be removed by hand. Removing the feature is dropping the routes, the five `sourceOffline` calls and the two tables. No existing row is rewritten.
