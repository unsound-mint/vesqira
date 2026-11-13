# ADR 0072: Sky context panel with an opt-in survey background

- Status: implemented, awaiting owner review
- Date: 2026-11-13
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

Clicking an outline or a list entry opens that dataset. A dataset with no celestial WCS, a point catalog, and a HEALPix map are listed with the reason and have no outline; nothing infers a position for them. The panel works fully offline on a plain coordinate grid. A PNG of the panel can be saved.

### Geometry stays in the worker
`sky_footprint` and `sky_point` are worker operations (`services/scientific-worker/sky_context.py`), reached through `GET /api/datasets/{id}/sky-footprint` and `.../sky-point`, with the frame as a parameter. They reuse `overlay_geometry.Sky`, which builds the dataset's own frame from its header (declared, corrected, inferred or assumed, and the reply says which), and astropy's `transform_to`. The footprint is the four pixel edges sampled 24 times each, unwrapped around the centre across longitude 0. A footprint of 80 degrees or more is refused as survey-sized. The cursor is a world position from the same WCS, never a pixel of the survey.

The browser only lays out world coordinates it was given: a gnomonic projection and a graticule for display (`apps/web/src/sky/projection.ts`). Nothing there is a measurement.

### The background is opt-in, context, and never measurement
- It is off by default. The choice (frame, on or off, survey) is stored in this browser's `localStorage` under `vesqira.sky.v1`. It is not sent to the server and is not part of a workspace, so a group server cannot turn it on for anyone.
- Turning it on is a two-step action that states, before anything is contacted: which server (`alasky.cds.unistra.fr`, the HiPS server of CDS, Strasbourg), that it receives the parts of the sky being looked at (the tiles requested for the position and zoom) and the person's IP address, and that no dataset, file name, path or coordinate from the person's files is sent. The same sentence stays beside the switch while it is on.
- While it is shown the panel says "Background: name (HiPS id), provider. Context only: it is not data from your files and is never used in a measurement." No pixel of it is read back as a number; a click on it is a position on the sky.
- A saved image contains the background only when the person ticks "Include the survey image in the saved file" for that file. Its caption then names the survey, its HiPS id, its provider and its credit, and says it is context. By default the file holds the footprints on the plain grid.
- The surveys offered are a short fixed list, each with the exact service URL listed for its id in the CDS HiPS registry. All are on `alasky.cds.unistra.fr`. Aladin Lite is given the URL, so it never asks a registry to resolve an id.

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
- The saved PNG is not a registered figure. Putting a survey background into a board figure with HiPS id, name and provider in its provenance needs a figure kind, which is not built.
- At most 120 datasets are drawn (the panel says how many were left out) and three worker reads run at once.
- When the datasets are far apart (more than the 60 degree maximum field) some lie outside the view; the list still names them and a click opens one.
- A virtual subset has no sky route, so the cursor link works for registered datasets only.
- Not verified: the live survey tiles. The development sandbox could not reach the HiPS server, so the rendering was run against a local stand-in HiPS, and the browser test stubs the server's responses.

## Validation
- Worker tests (`test_sky_context.py`): footprints and points compared with astropy for ICRS, a Galactic-frame cube shown in ICRS and back, CDELT1 negative and positive, longitude 0 wrap, a header with no celestial WCS, a survey-sized map, assumed against declared frames, and a correction that moves the outline.
- API tests: the worker request, caching per frame, bad parameters without a worker call, unknown dataset.
- Web tests: display geometry against the gnomonic formulas; panel states (off with no request and no library load, consent and cancel, unreachable server, shown with its label, no WCS, cursor as a world coordinate, opening a dataset); stored settings default to off.
- Browser spec (`e2e/tests/zzzzzzzzzz-sky.spec.ts`): the background is off, every non-local request is blocked and counted, none is attempted, footprints, frame switch, cursor and opening a dataset work, axe passes, screenshots at 1440 by 900 and 390 by 844 in light and dark. CI makes no external request.
- Not done: the live survey, a second person's design review, a run on a real multi-GB cube.

## Rollback/Migration
Nothing to migrate; the browser setting is a local key that can be ignored. To remove the feature, delete the `apps/web/src/sky` folder and its section in `Shell.tsx`, the two routes and the two worker operations, and the `aladin-lite` dependency. Nothing else depends on them.
