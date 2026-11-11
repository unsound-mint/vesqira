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
