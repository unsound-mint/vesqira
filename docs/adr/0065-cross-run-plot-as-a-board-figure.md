# ADR 0065: A cross-run plot as a board figure

- Status: proposed; implemented, awaiting owner review. No design review and no owner approval of the new controls.
- Date: 2026-11-12
- Completes the item ADR 0062 deferred ("saving the plot as a board figure") on the figure model of ADR 0041 and 0042, the board grid of ADR 0060 and the review snapshots of ADR 0011. The unit conversion it carries is the amendment to ADR 0062.

## Context

ADR 0062 draws one result across runs and exports the picture, but a researcher who wants it on the weekly board next to the maps has to leave the program. The reason it was deferred: a figure is bound to one dataset (`figures.dataset_id NOT NULL`) and to a `FigureView` or `Scene` that redraws it; a plot across runs has neither, and forcing it into a dataset would claim a provenance it does not have. Everything downstream of a figure assumes the dataset: the lifecycle impact lists, "Draw again" and its change check, the newer-data notices, the offline plan, the reviewer-safe provenance (which has a source file, a view and a run).

The reviewer boundary is the hard part. A review snapshot copies a figure's picture and its provenance, and the contract of that provenance is "no path, no dataset id". A cross-run plot is made of run names, parameters and recorded values. They are in the picture, as a plot, but the table they came from is not the reviewer's.

## Decision

**A figure of a new kind, in the same table.** `figures` keeps its place, so board items, the 12-column grid, snapshots, comments, archive, delete, reports and the review work as they do. Schema 27 rebuilds the table (SQLite cannot relax a NOT NULL): `dataset_id` may be NULL, `kind` is `dataset` or `crossrun`, and a CHECK ties them (a figure has a dataset if and only if it is kind `dataset`). Every existing figure becomes kind `dataset`, keeps its id (the AUTOINCREMENT counter is carried over so an id once used is never reused), revision, picture and text. The DDL is one constant (`schema.FiguresTable`) used by the boards package for a fresh install and by the migration. In Go `Figure.DatasetID` is a pointer so every site that assumed a dataset failed to compile; `NewFigure.DatasetID` 0 means none and is valid only with a description.

**Two documents, split by who reads them.**

- The owner-only description `cross-run/1` lives in `figures.scene` (the column that already holds the owner-only scene and channel-grid descriptions and is never copied into a snapshot or a report): the query, the axes, the colour key, the conversion, and for each drawn run its id, name, the recorded value, unit, uncertainty and its kind and definition, the x as text and, when converted, the converted number. The server writes it from its own reading of the catalog: the browser says which runs, which axes and which conversion. A run that is not in the catalog, lacks the result or the x, or is named twice is refused. A conversion is accepted only if the worker, asked again per recorded unit, gives the factor the browser used (409 otherwise), and only for runs whose definition equals that of some run recorded in the target unit: defined differently never merges. The recorded numbers stay in the description beside the converted ones.
- The reviewer-safe provenance has the shape every figure's has, so boards, reports and the review read it unchanged. Its source says what it is ("cross-run plot of 12 runs", "no file: drawn from the run records in the catalog, as they were when it was saved"), its view kind is `runplot`, and a new `runPlot` object holds the filter words, the axes, the number of runs and the conversion (target, method, factor and count per recorded unit), with statements in words. It holds no run id, name or value. `freeze` copies the picture and this provenance and nothing else, so a snapshot cannot carry the description by construction; a test plants a distinctive run name and value, publishes, and asserts the reviewer's document has neither, and that the reviewer's listener serves no run, figure list, view or conversion route.

**Route.** `POST /api/figures/runs` (owner listener only) takes the title, caption, picture, the query and axes, the run ids, the conversion and `redrawnFrom`. `POST /api/figures` stays dataset-bound.

**Draw again reads the runs again.** The query model exists once, in the browser (ADR 0062), so the comparison is made there. "Draw again" fetches the runs, evaluates the saved query, builds the plot from the saved axes, asks the worker again for any conversion (a stored factor is never reused; a refusal stops the redraw with the sentence), and compares with the description before anything is saved: runs now matching that did not, runs no longer drawn with the reason (deleted, archived, no longer matching the filter, no longer recording the result), and runs whose value, unit, uncertainty, definition, x or conversion differ. It is announced in the same box as a dataset figure's changes, and a changed set is announced even when the pixels happen to be equal. The drawing is the page's own component rendered with no selection, no focus ring and nothing that depends on time, so the same runs give the same pixels and "same pixels" can be said. The redrawing becomes a new, linked figure only on "Save as a new revision" (`redrawnFrom`, recorded in the figure identity, as for dataset figures). `redraw-check` answers `cross-run`, `ready` for such a figure: there is no file to compare.

**Lifecycle.** Deleting or archiving a run lists the cross-run plots whose description names it (`json_each` over `runs[].id`): the figures stay (pinned evidence), the boards and published reviews that hold them stay, and the redraw will say the run is gone. A dataset's impact queries compare `dataset_id` and so never match a NULL; `masksources` filters NULL out explicitly. Deleting a figure deletes its row, as before.

**Saving.** In the runs page, "Save as board figure" (with a title) draws the runs the filter matches (not the runs listed after them for a missing value, which the query cannot recreate), writes the query and the run count on the picture and saves it; a board selector then adds it. The picture is composed in the browser by the plot component, as the page's SVG and PNG exports are.

## Alternatives

- Keep `dataset_id NOT NULL` and store 0 or the first run's output dataset: a lie in the figure's provenance, and every lifecycle query would find a dataset that is not the figure's. Rejected.
- A separate table for cross-run figures: boards, snapshots, comments, archive, reports and the grid all reference `figures.id`; each would need a second path. Rejected.
- Put run ids, names or values in the reviewer-safe provenance "for traceability": the reviewer would receive the table behind the plot. The author keeps them in the owner-only description; the reviewer has the picture, the words and the counts.
- Redraw check on the server: it would need the query semantics in Go, a second matcher that disagrees with the filter box in some word. Rejected for the same reason as in ADR 0062.
- Trust the browser's conversion numbers: the stored description would claim a conversion the worker never did. The server asks again.

## Consequences

- One schema version (27; renumber at merge). Figures lists carry `kind`; `datasetId` is nullable in the contract and the web types. The consumers of provenance read `source`, `view` and `run` as before; they say "Source" instead of "File" for a cross-run plot and show its statements.
- A cross-run figure has no SVG export of its own (the page exports the live plot) and no newer-data notices (no file).
- The description grows with the number of runs (about 150 bytes a run, at most 5000 runs); it is stored once per figure and never copied into snapshots.
- A run deleted after the figure was saved leaves its name and recorded values in the owner-only description. That is the point of pinned evidence; it is not shown to reviewers.

## Validation

- Go: schema migration from 26 (ids and the counter kept, kind set, the CHECK refuses the wrong pairings, no table needed); the route against the OpenAPI document; the description holds the recorded 30 deg beside the converted value and the same factor for the uncertainty; refusals (unknown run, duplicate, missing result or x, no runs, bad kind, not a PNG, wrong factor, differently defined run, a run already in the target unit, a refusal by the worker, unknown field) leave no figure; a redrawing is linked and a first figure saved again finds its twin; the reviewer's document and provenance contain no canary name, value, run id, `scene`, `datasetId` and the reviewer's listener serves no owner route; the run impact lists the plots and the figure survives the run's deletion.
- Web: the request names the drawn runs only, with the factor used; the diff (added, gone with each reason, changed value, unit, definition, x, conversion, equal numbers written differently); redraw asks the worker again, announces before saving, saves a linked revision on request, stops on a refused conversion and on nothing to draw; the picture is deterministic with no selection or focus; save and add to a board in the runs page; the review page shows a cross-run plot as a source that is not a file.
- Not verified: a design review of the new controls; a board with real VGT and ROHSA runs; behaviour of `renderToStaticMarkup` plus canvas rasterisation across browsers (the unit tests mock the canvas, jsdom has none), so the pixel-equality message depends on the browser drawing the same SVG identically twice, which was not run in a real browser here.

## Rollback/Migration

Schema 27 rebuilds `figures`. The upgrade makes a backup of the catalog first (the Guard), and every row is copied unchanged apart from the new `kind`. To roll back, restore the pre-upgrade backup: an older program refuses a catalog of a newer schema. Cross-run figures made after the upgrade would be lost with it; a catalog with none can be downgraded by copying the rows back into the old table definition. Snapshot: `testdata/schema_v27.sql`.
