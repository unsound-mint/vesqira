# ADR 0042: One scene model for overlays, linked views and figures; declared angle conventions

- Status: proposed; awaiting owner review (decided by the developer while the owner had delegated ADR decisions, 2026-11-11)
- Date: 2026-11-11
- Related: ADR 0005 (workbench), 0017 (comparison), 0026 (regions), 0027 (histogram, zoom, shared scale), 0034 (GPU renderer), roadmap Stage 3 (linked views), Stage 6 (figures as specifications), Stage S (this ADR)

## Context
Three roadmap items are the same design seen from different sides:
- an overlay drawn from another dataset (CO contours on H I, VGT segments on intensity, star polarization vectors);
- linked views that follow each other by sky position and velocity instead of by pixel index (Stage 3, Q174);
- a figure stored as a specification that can be drawn again and exported as SVG (Stage 6, Q186).

Today a viewer pane draws one dataset with its own display settings. Orientation lines are drawn only on the orientation map itself. Two panes link only when the server confirms an identical grid. A figure is a PNG plus provenance.

Orientation data has a semantic gap that blocks every cross-dataset comparison:
- The catalog records only that a map is axial and in degrees or radians. It does not record the reference direction or what the angle means.
- The VGT pipeline writes ψ in radians in the pixel frame, counter-clockwise from FITS axis 1, already rotated 90° from the gradient so that it is a B-field proxy. The file carries no header key saying so.
- Starlight polarization catalogs give a position angle east of north (IAU) that is parallel to B.
- Planck 353 GHz gives Q/U in the COSMO convention, so U must change sign for IAU, and the dust polarization angle is perpendicular to B.

Comparing any two of these without converting both to the same quantity in the same frame produces a wrong answer that looks plausible.

Shipped code also resamples axial maps like scalars. `align` (reproject_interp or reproject_exact) and `convolve` accept a map declared axial and average its angles linearly, which breaks invariant 4.

## Decision

### 1. Declared angle semantics
`Interpretation` gains two fields. Both are declared by the researcher or proposed by an adapter, never inferred from pixels:
- `frame`: `pixel`, counter-clockwise from +axis 1 towards +axis 2 as stored; or `sky`, east of north (IAU). The north direction is taken from the file's WCS at each pixel.
- `quantity`: what the angle is.
  - `b_field`
  - `gradient`
  - `dust_polarization`: the E-vector of emission, perpendicular to B.
  - `starlight_polarization`: parallel to B.
  - `unspecified`

The VGT run adapter proposes `frame=pixel, quantity=b_field`, citing the pipeline's ψ = ½·atan2(U, Q) + π/2.

A comparison or overlay across datasets converts both sides to sky-frame B-field orientation. `gradient` and `dust_polarization` are rotated by 90°. The pixel frame is converted with the local north angle that astropy computes from the WCS at each sample. If either side has `unspecified` quantity or no frame, the operation is refused, and the refusal names the missing declaration.

Single-map statistics (the existing mean orientation and resultant length) stay available without these fields, because they do not mix frames.

### 2. Axial-aware resampling everywhere
Every operation that combines several angle samples into one value works on the doubled angle: Q = w·cos 2θ and U = w·sin 2θ, with w the declared reliability, or 1 when none is declared. Q and U are resampled independently, and the result is θ = ½·atan2(U, Q). These operations are reprojection, convolution, display downsampling and block means.

The resultant length is kept as a derived reliability. Nearest-neighbour sampling is exempt, because it picks one sample and averages nothing.

`align` and `convolve` on an axial map switch to this path, and their provenance says so. Moment, slab and scalar difference on an axial map are refused, as the scalar difference already is.

### 3. A scene per pane
A pane draws a `Scene`, which has a versioned schema (`scene/1`) in `packages/contracts`:
- `base`: the dataset, the channel or spectral slab given by its world value and index, and the display settings (colour map, stretch, limits, shared-scale group).
- `overlays`: an ordered list. Each overlay has `kind`, `datasetId` and kind-specific settings.
  - `contours`: levels as an explicit list, or a generator (start, step or factor, count) in the overlay dataset's own unit; plus a channel or slab.
  - `orientation`: sampling step, length (fixed, or scaled by reliability), reliability threshold.
  - `catalog`: a catalog dataset, and the columns used for position and an optional angle and its uncertainty.
  - `regions`: saved regions.
  - `beam`: drawn only from the header's BMAJ, BMIN and BPA, never a default.
  - `grid`: a coordinate grid.

Overlays are placed by WCS geometry, not by resampling data:
- Contours are traced on the overlay's own grid, and their vertices are transformed pixel → world → base pixel.
- Orientation samples are taken on the overlay's own grid. Positions are transformed the same way, and angles are carried through the sky frame into the base pixel frame.
- Catalog positions are transformed world → base pixel after converting between the declared celestial frames with astropy.

The worker does this geometry (a new interactive-lane request that returns vertices and segments), so the browser never receives the overlay's array. If the celestial frames cannot be related, the overlay is refused with the reason. Overlay data is never interpolated onto the base grid for display, so there is nothing to mislabel as resampled.

### 4. Linked views
Panes join a user-named link group. A group can link the cursor (by sky position), the channel (by spectral world value in a common unit and frame, using ADR 0040's conversion), and the pan/zoom (by sky region). A pane can leave its group.

The same-grid check stays as a fast path. Different grids link through the WCS transformation, and the link pill shows which kind of link is active. When the spectral frames differ (for example LSRK and TOPOCENT), the channel link is refused, because frame changes are out of scope (ADR 0040).

### 5. Figures are scenes
A figure stores its `Scene` plus each dataset's identity at save time: id, size, modification time, and the fingerprint once Stage 5 adds it. The PNG becomes a cached render of that scene.

"Draw again" re-renders the scene. If a source changed, it says so before drawing and never swaps data silently. SVG export writes the vector layers (contours, segments, catalog markers, regions, axes, beam, colour bar) as SVG over the embedded raster base. Review snapshots keep freezing the rendered PNG and the scene, so a reviewer's view never changes after publishing.

## Alternatives
- **Reproject overlay data onto the base grid for display.** This is simpler, but it resamples, needs axial care for every display, and blurs contours across grids. Geometry transformation is exact and moves less data.
- **Keep linking by identical grid only.** That fails the main use, comparing GASKAP, GASS, MAGMA and Planck on their native grids.
- **Infer the angle convention from the pipeline name.** That is an adapter's proposal, not a fact. Keeping it declared, with the adapter's proposal labelled as such, matches invariant 7.
- **Put figures in a separate rendering service.** That would be a new service for one user, and the browser renderer (ADR 0034) already draws the scene.

## Consequences
- The catalog needs a migration for the two interpretation fields. Existing axial declarations get `frame` and `quantity` as unknown: they keep working for single-map statistics and are refused for cross-dataset use until declared. VGT imports are re-proposed on the next rescan.
- New worker requests: overlay geometry (contours, orientation samples, catalog positions) and local north angle.
- The viewer state moves from per-pane fields into a scene object, so this is a refactor of `useViewer`. It is done before any new overlay kind lands, and the overlay slices land one after another.
- The figure model changes, and old figures stay PNG-only: they render as before, with no "draw again".

## Validation
- Golden tests computed independently in the tests with astropy:
  - the local north angle at known WCS positions (including a rotated CD matrix and a Galactic-frame file);
  - pixel ↔ sky angle conversion;
  - the 90° rotations by quantity.
- Axial resampling: a 1°/179° pair averages to 0°, not 90°; two orthogonal angles give a degenerate resultant that is flagged; the result matches the doubled-angle reference.
- Overlays:
  - a contour of a synthetic Gaussian on grid A lands at the same sky position on grid B;
  - catalog markers land at their known pixels;
  - an incompatible frame is refused.
- Linked views: a sky cursor maps to the same world position in both panes within half a pixel; a different spectral frame refuses the channel link.
- Figures: draw-again of an unchanged scene is pixel-identical to the cached PNG; a changed source is announced; the SVG contains the vector layers.

## Rollback / Migration
- The interpretation fields are additive (nullable).
- `scene/1` figures can be drawn as PNG by older code, since the PNG is kept.
- Reverting the axial resampling would bring the invariant-4 bug back, so it is not reverted.

## Implementation notes: first slice (sections 1 and 2)
Status: implemented for the declaration, the worker conversion and axial resampling; overlays, linked views and figures as scenes are not built.
- **Declaration.** `interpretations.frame` and `.quantity` (nullable, schema 7), validated by the API, editable in the orientation panel. Existing rows are unknown. The VGT run adapter proposes `pixel` and `b_field`, and says it is from the pipeline's convention (ψ = ½ atan2(U,Q) + 90°). A map an analysis makes keeps its input's unit, frame and quantity as a proposal by `vesqira-analysis`, with the companion reliability map as its reliability.
- **"Sky" means east of north in the file's own celestial frame** (RADESYS for RA/Dec, Galactic for GLON/GLAT). Comparing files in different celestial frames rotates the position angle between the frames (astropy `directional_offset_by` and `transform_to`), so a Galactic-north direction is not mistaken for celestial north.
- **Worker.** `orientation.py` holds the conversions, all through astropy's WCS and `SkyCoord.position_angle`; the worker op `orientation_sky` converts one pixel to sky-frame B-field orientation and refuses, naming the missing declaration, when the frame or the quantity is unknown or `unspecified`. No route or screen uses it yet.
- **Resampling.** `align` and `convolve` of a declared axial map resample the doubled angle. `align` needs the frame (a pixel-frame map goes to sky position angle, is resampled, is rotated into the reference's celestial frame if it differs, and comes back to the reference grid's own pixel frame, so the physical orientation survives a rotated grid); the quantity is not needed because the same quantity goes in and out. Nearest-neighbour picks one sample, so θ itself is resampled, but the frame conversion still applies. `convolve` applies the kernel in the stored frame and does not need a declared frame. Moment and slab are refused (API and worker).
- **Companion reliability.** The result's reliability map is R = |(Q, U)| / ŵ, where Q, U and ŵ are the resampled components and weight (w is the declared reliability, or 1), so R is in [0, 1] (clipped only for round-off). Where R is below 1e-6 the orientation is undefined: θ is NaN and the pixel is counted in the summary. For nearest-neighbour, the companion is the declared reliability picked with the same sample, and there is none when none was declared. Remote analysis jobs run the same program with the same options and register `result.reliability.fits` too.
- **Display downsampling.** Disabled for axial maps, not made doubled-angle aware: the browser asks for full resolution (`useViewer`) and the API refuses `step > 1` for a declared axial map (400). A reduced preview of an angle map cannot show orientation honestly at the zoom where it is used, and the full plane is bounded by the viewer limit anyway.

## Implementation notes: second slice (section 3, the scene model; figures store their scene)
Status: implemented as a behaviour-preserving refactor with the contract. No new overlay kind is drawn, panes link only on an identical grid, and there is no "draw again" or SVG export yet.
- **Contract.** `Scene` (`scene/1`) and its parts are in `packages/contracts/bodies.json`; the web types are generated from it (`apps/web/src/scene/scene.ts`). The base is the dataset, the channel index, its spectral world value as the worker reported it (absent until read), and the display: `mapping` (scalar, or axial for a declared orientation map, drawn with twilight whatever the scalar settings), colour map, stretch, range, custom limits and unit, and the shared-scale group. The overlay union defines all six kinds, each with a required single-value `kind`. The view is zoom (1 to 16, relative to the fitted image) plus an optional centre in image pixels, so a scene shows the same image window, width/zoom by height/zoom pixels around the centre, on any pane. Shared fixtures (`packages/contracts/fixtures/scenes.json`) are accepted or refused by the Go parser and round-tripped by the web tests.
- **Viewer state.** A pane is a scene plus ephemeral state. `paneReducer` and `sceneReducer` take plain, serialisable actions: `setChannel`, `setSpectral`, `setDisplay`, `addOverlay`, `updateOverlay`, `removeOverlay`, `moveOverlay`, `setView`, plus `resetForDataset` and `syncAxial`. In a patch, null removes a field. An action that changes nothing returns the same object. `setSpectral` and `syncAxial` record facts, not commands, so a future undo stack skips them. `useViewer` keeps its setters as wrappers over dispatch and adds `scene` and `dispatch`. Opening a dataset starts a scene on channel 0, fitted, with no region; colours and the orientation-line setting carry over, as before. Saved regions are server rows, and the regions overlay copies the shapes it draws.
- **Rendering.** One registry maps an overlay kind to a pure function that returns marks (lines, rectangles, circles in picture coordinates). The viewer turns the marks into the same SVG elements as before; a figure strokes them on its canvas with `strokeMarks`. The orientation lines of the base map and the regions (saved, working, being drawn) go through it. A kind with no renderer draws nothing.
- **Figures.** A plane or orientation figure sends `figureScene(live)`, the projection of the live scene onto the picture: the drawn channel, twilight for an axial map, the line step the figure used, whether a reliability map set the line opacity and which one, only the working region, and the fitted view (a figure shows the whole plane). The figure's overlays are drawn from that projection; when it cannot state the display (a custom range without valid limits) the figure is saved without a scene but drawn the same. The server reads it strictly (`internal/scene`): unknown keys and kinds, null or non-finite numbers, out-of-range values and over-long lists (16 overlays, 256 saved regions, 64 KiB) are refused. It also refuses the four kinds not drawn yet, a scene that disagrees with the figure's dataset, channel, spectral value, display, working region or declaration, an overlay from another dataset, reliability opacity that does not name the declared reliability map, and any scene on a spectrum figure. The server stores its own re-marshalled reading in `figures.scene` (schema 9) and hashes it into the evidence revision only when present, so a figure saved without a scene gets the revision it always would have.
- **Why not inside provenance.** The provenance document is reviewer-safe and is copied as is into snapshots, published reports and the PNG download, and ADR 0011 bans dataset ids there. A scene names dataset ids, so it is owner-side only (`Figure.scene`), and the reviewer test checks that no scene reaches a reviewer.
- **Linking.** The compare panes form a link group (`scene/link.ts`): an id, its panes, a basis (only `same-grid` exists) and the linked facets. The cursor and channel facets are linked, and the view is not, as before. `follow()` returns the actions that make one pane follow another, so a WCS basis can arrive without changing the callers. While the colour scale is shared, both scenes carry `scaleGroup`, also after a pane opens another dataset.
- **Behaviour change.** Zoom now keeps the image point at the pane's centre, instead of the screen offset. A pan survives a pane resize as an image position.
- **Deferred.** Drawing the other overlay kinds, and the worker's geometry request. Linking by WCS, and user-named groups. "Draw again" and SVG export from the stored scene. The scene holds today's layers and display settings, but the base raster also depends on the theme's no-value colours and the resolution the canvas was drawn at, which are not recorded yet. A reviewer-safe frozen scene in snapshots, with dataset ids replaced by source identities, and the per-dataset identity list for multi-dataset figures (section 5). The overlay list UI. Undo.

## Implementation notes: third slice (section 3, overlays from other datasets; the overlay list)
Status: implemented for contours, orientation lines of another map, the beam and the celestial grid, on screen and in figures. Catalog markers are not drawn (a figure still refuses them). Linking by WCS, "draw again", SVG export and undo are not built.
- **Worker.** `overlay_geometry` (interactive lane, `overlay_geometry.py`) returns geometry in base pixels (0-based, centres at integers, y up) as a frame like a plane's: a JSON head, then float32 paths (group, vertex count, then x, y pairs) or orientation segments (x, y, angle, weight). Positions go overlay pixel → world → base frame → base pixel through astropy (`pixel_to_world`, `SkyCoord.transform_to`, `world_to_pixel`) on each file's celestial WCS (`orientation.celestial_wcs`).
  - **Frames.** A file whose celestial frame is `unresolved` in its sky facts (RA/Dec with neither RADESYS nor EQUINOX) is refused by name: wcslib would assume ICRS, and two files are never related on an assumed frame. A frame inferred from EQUINOX (the FITS rule) is used and its status travels in the head. Footprints are compared both ways on a 33 × 33 sample of each image; no overlap is refused, saying so.
  - **Contours.** Traced by contourpy (matplotlib's contour engine, a new pinned dependency: `requirements.txt`, the RPM spec and its CI image; imported lazily, so a host without it refuses contours by name and serves everything else) on one channel of the overlay's own grid, NaN masked so a pixel with no value is a gap. Levels: a list, linear or geometric generators, or k × σ with σ = 1.4826 × MAD of the channel's finite pixels (`astropy.stats.mad_std`), labelled as the scatter of the whole plane, signal included. Levels are in the overlay's own unit; a scene level unit that is not the file's (or the declared one for a file without BUNIT) is refused, and a file with no unit is said to have none. A figure records the σ it drew with; a recomputed σ that differs is refused. Contours of a map declared axial are refused. Vertices outside the base (beyond 2 px) are dropped and the line split. Over 200 000 vertices, polylines are simplified (Ramer-Douglas-Peucker, written here: display only, endpoints kept, tolerance 0.1 to 4 base pixels, the smallest that fits) and the head reports the tolerance and counts; beyond that it is refused. Tracing is bounded at 2^28 pixel-levels per request.
  - **Orientation.** Single pixels of the overlay map on its own grid (never block means), every `step` pixels: given, or chosen so samples are the requested spacing apart in base pixels (the pane's gapPx divided by its scale, quantised to powers of √2 so a small zoom refetches nothing). Each sample becomes sky-frame B-field orientation (`to_sky_b_field`, refused naming the missing frame or quantity), is rotated into the base's celestial frame when it differs (`rotate_pa_frame`), and into the base's pixel frame at its new position (`sky_pa_to_pixel`). With the declared reliability map (same grid, same host, unchanged), weight = reliability / its 99th percentile clipped to [0, 1], NaN where unknown; a threshold drops samples below it or unknown. Capped at 20 000 samples by raising the step (reported).
  - **Beam.** From the named dataset's (base or other) BMAJ, BMIN, BPA only: absent BMAJ/BMIN, BMIN > BMAJ, or an elliptical beam without BPA is refused; a circular beam without BPA is drawn and says so. The ellipse is built on the sky (73 points by `directional_offset_by` around a corner's sky position, BPA rotated into the base frame when the beam's file is in another) and placed by the base's WCS, so a rotated or non-square grid shows its true shape.
  - **Grid.** Lines of constant longitude and latitude in ICRS, FK5 J2000 or Galactic, spaced in whole time units (RA) or degrees, generated on the sky, converted to the base frame and placed by its WCS; a pole inside the image extends the longitudes. Labels sit where a line leaves the image, longitude at the bottom and latitude at the left by preference, never within 15 % of an edge of another label.
- **API.** `POST /api/datasets/{id}/overlay-geometry` with `{overlay, spacing?}`; the overlay is read by the scene parser (`scene.ParseOverlay`). Every file is authorized as a plane read (roots, unchanged since registered) and must be on the base's machine. The overlay's declared unit, axial declaration, angle convention and reliability map come from the catalog, never the request. The cache key holds both file identities and declared units, the reliability map's identity, the declarations, the geometry settings (not id, visibility or style) and the spacing. `overlay_geometry.py` is deployed to remote hosts with the other worker files.
- **Scene.** Contours (with the `sigma` level mode and an optional `unit`), beam (`corner`), grid (`frame`) and the orientation fields `length: reliability` and `threshold` are accepted, all with an optional style: a palette colour (`white`, `cyan`, `amber`, `magenta`, `lime`, the `--overlay-*` tokens, light inks because the image canvas is dark in both themes), a width and a dash. The server re-marshals every kind with all its fields.
- **Web.** `useOverlayGeometry` keeps one entry per overlay (computing, ready, or refused with the server's reason), replaced when what it depends on changes, so the registry's pure renderers draw only geometry that matches the scene. Contours negative levels are dashed. The **Overlays** inspector section lists the scene's overlays in drawing order: show or hide, move, edit, remove, add (kind, dataset, kind settings, style); each row states its basis or its refusal. The base map's own orientation lines and regions can be hidden and moved but not removed. This section is built from the existing controls and was not put through the design-approval gate.
- **Figures.** A figure waits for overlays still being computed, leaves refused ones out, records the σ and the orientation step it drew with (the screen's geometry, so the stored step reproduces the drawn samples), strokes the same marks clipped to the picture, and prints each overlay's basis in the caption. The server accepts overlays from other registered datasets on the same machine (regions and the grid stay the base's), authorizes each file, and names each in the provenance by file name, size and time (never by id, ADR 0011).
- **Deferred.** Contours of a spectral slab (only one channel; a slab needs a declared aggregation and changes the unit). Catalog markers. Refetching orientation geometry at the figure's own scale (a figure uses the screen's step and records it). The per-dataset identity list for multi-dataset figures and lifecycle impact for an overlay's dataset (archiving it does not list figures that drew it). A mechanical check that the list of worker files deployed to remote hosts (in `main.go`, `tools/build-rpm.sh` and four tests) matches what `plane_worker.py` imports.
