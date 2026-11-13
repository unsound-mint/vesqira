# ADR 0072: Sky context panel with an opt-in survey background

- Status: implemented, awaiting owner review
- Date: 2026-11-13 (figure kind and subset routes added the same day)
- Related: ADR 0042 (scene layers, overlay geometry), 0044 (catalogs), 0061 (WCS corrections), 0069 (access labels), issue 134

## Context
A researcher with several cubes and maps wants to see where they lie on the sky, and where the pixel under the viewer's cursor is, next to a survey image for orientation (an optical or infrared field, or the HI4PI column density). Registered datasets already carry a celestial WCS, which the worker reads with astropy. The survey image is a public HiPS served from the internet.

Two things need a decision. The first is the external service: this app has so far contacted no server on its own. The second is the geometry: the browser must not grow a second WCS implementation.

## Decision
### What the panel is
A section "Sky" in the inspector, closed by default. It draws, in a frame the person chooses (ICRS or Galactic):
- the footprint of every registered image or cube, from its celestial WCS, with the corrections in force;
- the open dataset's selected outline;
- the viewer's cursor (hover, or the pixel pinned by a click) as a world position.

Clicking an outline or a list entry opens that dataset. A dataset with no celestial WCS, a point catalog, and a HEALPix map are listed with the reason and have no outline; nothing infers a position for them. The panel works fully offline on a plain coordinate grid. A PNG of the panel can be saved, and the view can be saved as a registered figure (see below).

### Geometry stays in the worker
`sky_footprint` and `sky_point` are worker operations (`services/scientific-worker/sky_context.py`), reached through `GET /api/datasets/{id}/sky-footprint` and `.../sky-point`, with the frame as a parameter. They reuse `overlay_geometry.Sky`, which builds the dataset's own frame from its header (declared, corrected, inferred or assumed, and the reply says which), and astropy's `transform_to`. The footprint is the four pixel edges sampled 24 times each, unwrapped around the centre across longitude 0. A footprint of 80 degrees or more is refused as survey-sized. The cursor is a world position from the same WCS, never a pixel of the survey.

The browser only lays out world coordinates it was given: a gnomonic projection and a graticule for display (`apps/web/src/sky/projection.ts`). Nothing there is a measurement.

### The background is opt-in, context, and never measurement
- It is off by default. The choice (frame, on or off, survey) is stored in this browser's `localStorage` under `vesqira.sky.v1`. It is not sent to the server and is not part of a workspace, so a group server cannot turn it on for anyone.
- Turning it on is a two-step action that states, before anything is contacted: which server (`alasky.cds.unistra.fr`, the HiPS server of CDS, Strasbourg), that it receives the parts of the sky being looked at (the tiles requested for the position and zoom) and the person's IP address, that the view centres on the person's datasets so those tile requests reveal which field they are working on (which matters for an unreleased field), and that no dataset, file name, path or pixel value is sent. The same sentence stays beside the switch while it is on.
- While it is shown the panel says "Background: name (HiPS id), provider. Context only: it is not data from your files and is never used in a measurement." No pixel of it is read back as a number; a click on it is a position on the sky.
- A saved image contains the background only when the person ticks "Include the survey image in the saved file" for that file. Its caption then names the survey, its HiPS id, its provider and its credit, and says it is context. By default the file holds the footprints on the plain grid.
- The surveys offered are a short fixed list, each with the exact service URL listed for its id in the CDS HiPS registry. All are on `alasky.cds.unistra.fr`. Aladin Lite is given the URL, so it never asks a registry to resolve an id.

### Virtual subsets on the sky
`GET /api/subsets/{sid}/sky-footprint` and `.../sky-point` run the same worker operations with the subset's window (`WINDOW_OPS` in `plane_worker.py`), so the outline is computed from the parent's WCS with the crop applied by the worker (astropy), never in the browser. The reply is accepted only if the worker echoes the window, as for the other subset routes, and a stale subset answers 409. The panel keys a subset by its negative viewer id, draws it as its own outline and links the cursor in the subset's own pixels.

### Saving the view as a figure (schema 36)
`POST /api/figures/sky` makes a figure of a new kind `sky`, on the pattern of the cross-run figure (ADR 0065): no dataset (`dataset_id` is NULL), an owner-only description in `figures.scene`, and reviewer-safe provenance. Schema 36 rebuilds `figures` to add `sky` to the kind CHECK.
- The browser sends the frame, the centre and field of view, the survey's HiPS id or null, the picture, and which datasets or subsets it drew. The server reads every footprint itself through the panel's own route (same corrections, authorisation, windowing and cache), and writes the survey's name, provider and credit from its fixed list, which a test holds equal to `surveys.ts`. The server never contacts the survey host.
- The description records, per footprint, the dataset (and subset with its version), its name, the frame it was read in, how that frame is known (declared, corrected, inferred or assumed) and its extent. The reviewer sees the same by file name and basis, never an id.
- The survey is context. The provenance says so in words ("context only, never sampled into a number"), the picture's caption says so, and the reviewer page opens the provenance for this kind. A figure saved with the background off says "No survey image" and is the plain grid. The survey is in the picture only when the person ticked it for the capture, as for the PNG.
- Access labels (ADR 0069): the description carries `datasetId` for each footprint (a subset carries its source's), so the existing walk covers publishing a board, sharing the figure and pushing. A label on a drawn dataset asks for the same confirmation as for any figure. Deleting a dataset lists the sky figures that drew it.
- "Draw again" does not apply: the redraw check answers state `none` with a sentence saying to save a new view.
- The recorded centre and field are the panel's own view. Panning inside the survey picture itself (Aladin's own drag) is not read back, so with the background on the recorded field is where the panel last placed it.

### Library: Aladin Lite 3.8.2, bundled, loaded lazily
Aladin Lite (CDS) is the HiPS renderer; writing one is not worth it. It is an npm dependency with an exact pin (`"aladin-lite": "3.8.2"`), part of this app's own build, and a dynamic `import()` makes it a separate chunk (about 2.4 MB, 850 kB gzipped, WebAssembly inlined) fetched from the app's own origin. It is imported only after the person turned the background on and the survey's `<url>/properties` file answered. No script comes from a CDN, so no script-source rule is needed: the web app sets no CSP, and the desktop shell's Tauri config has `csp: null`. Whoever adds a CSP later must allow `blob:` workers and `wasm-unsafe-eval` for this chunk.

Things found by running it that the code depends on:
- By default Aladin Lite posts a usage record (survey, target, field of view, page URL) to a CDS logging server. `log: false` turns it off. A test against a local stand-in HiPS saw no request other than to the given survey URL.
- Its shapes and `gotoRaDec` take ICRS whatever frame it displays; `cooFrame` only changes the grid and orientation. The panel gives it ICRS outlines and ICRS cursor positions (computed by the worker in ICRS) and sets `cooFrame` to the chosen frame.
- It needs WebGL2. Without it, or when the properties file cannot be fetched within 8 seconds, the panel reports "Not shown: reason" and stays on the plain grid.

## Alternatives
- Load Aladin Lite from the CDS CDN: rejected; it adds a third-party script origin, runs code the build did not pin, and fails offline.
- Resolve a HiPS id through the registry at run time: rejected; it adds a second server that learns of the use, and the id can map to another host later.
- Do the WCS transforms in the browser (wcslib.js or hand-written): rejected; two implementations of frames would drift, and the worker's is tested against astropy.
- Draw the HiPS with a hand-written renderer: rejected; HEALPix tile ordering, projections and wasm are what Aladin Lite already does.
- Store the opt-in per workspace: rejected for now; a workspace can be shared, and a network choice should be made by the person at this computer.
- A static survey thumbnail through the API: rejected; it makes the server contact external hosts on the user's behalf.

## Consequences
- A new dependency, GPL-3 as listed in its package.json (the LICENSE file shipped with 3.8.2 is the LGPL-3.0 text, and later releases state LGPL-3.0-or-later). Both are compatible with this project's AGPL-3.0. Recorded in `THIRD_PARTY_NOTICES.md`; the owner should confirm which it is before a release.
- The main bundle does not grow; one lazy chunk is added.
- Footprints of datasets under an access label (ADR 0069) are drawn in this local panel and in the saved PNG. The panel never publishes anything; a saved PNG is a file the person chose to write.
- The saved PNG is a file, not a registered figure. "Save as figure" is the figure route (below).
- At most 120 datasets are drawn (the panel says how many were left out) and three worker reads run at once.
- When the datasets are far apart (more than the 60 degree maximum field) some lie outside the view; the list still names them and a click opens one.
- A virtual subset has its own sky routes (below). With a spatial step, its outline is the edge of the sample grid, half a step beyond the outermost sampled pixel centres, as for a materialised file.
- Publishing a figure that includes the survey image sends survey pixels to the reviewer and to anyone the link is passed to. The survey's licence terms are the author's to check; the credit is in the picture and the provenance.
- Not verified: the live survey tiles. The development sandbox could not reach the HiPS server, so the rendering was run against a local stand-in HiPS, and the browser test stubs the server's responses.

## Validation
- Worker tests (`test_sky_context.py`): footprints and points compared with astropy for ICRS, a Galactic-frame cube shown in ICRS and back, CDELT1 negative and positive, longitude 0 wrap, a header with no celestial WCS, a survey-sized map, assumed against declared frames, and a correction that moves the outline.
- API tests: the worker request, caching per frame, bad parameters without a worker call, unknown dataset.
- Web tests: display geometry against the gnomonic formulas; panel states (off with no request and no library load, consent and cancel, unreachable server, shown with its label, no WCS, cursor as a world coordinate, opening a dataset); stored settings default to off.
- Browser spec (`e2e/tests/zzzzzzzzzz-sky.spec.ts`): the background is off, every non-local request is blocked and counted, none is attempted, footprints, frame switch, cursor and opening a dataset work, axe passes, screenshots at 1440 by 900 and 390 by 844 in light and dark. CI makes no external request.
- Subset routes: worker tests against astropy (footprint and cursor through a window, equal to a materialised file's, smaller than the source), API tests (reads through the window, never an outline from a worker that ignored it), panel tests.
- Sky figure: schema 36 migration test and snapshot; API tests (what is recorded, no id in the reviewer JSON, refusals leave no figure, label gating on share and publish including a subset's source, impact list, redraw answer, survey list equal to the web list); panel tests; the browser spec saves a view, publishes it and reads it as the reviewer with axe and screenshots at 1440 and 390 in both themes.
- Not done: the live survey (a figure with the survey was not made against real tiles), a second person's design review of the Save as figure control and the reviewer text, a run on a real multi-GB cube.

## Rollback/Migration
Nothing to migrate; the browser setting is a local key that can be ignored. To remove the feature, delete the `apps/web/src/sky` folder and its section in `Shell.tsx`, the two routes and the two worker operations, and the `aladin-lite` dependency. Nothing else depends on them.
