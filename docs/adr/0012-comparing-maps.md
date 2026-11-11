# ADR 0012: Comparing maps side by side

- Status: accepted under delegated merge, 2026-11-04; owner review pending
- Date: 2026-11-04

## Context
Researchers compare a map with another (a scalar map with an orientation map, or two channels of related cubes). AGENTS forbids silently merging incompatible measurements, and reprojection is not the same as matching resolution.

## Decision
- Two maps are shown side by side, each with its own display settings; the inspector follows the pane the user chooses.
- Cursor and channel are linked only when `compat.Grids` (ADR 0010) confirms the same shape, axis types, units, reference values, reference pixels, steps, rotation keywords and celestial frame. A value unresolved on either side is a reason, never a match. (ADR 0042, section 4: different grids can now be linked on request through the WCS; this check still decides the same-grid fast path.)
- When the grids differ or cannot be confirmed, the panes stay side by side and unlinked, the reasons are listed, and nothing is resampled, reprojected or averaged together. The inspector's "Other map" value appears only when linked.
- Linking compares pixel to pixel on the shared grid; there is no sky-coordinate matching between different grids in v0.1.

## Alternatives
Linking by sky position through reprojection (changes the data and needs a resolution policy); warning but linking anyway (invites misreading a shifted or rescaled map as aligned).

## Consequences
Maps from the same pipeline run link; maps on different grids need an explicit, separately designed resampling step later.

## Validation
Go: compatible for a file with itself; a Galactic frame and a coarser pixel scale each refused with a stated reason; bad ids refused. Web: linking only on a confirmed match, every reason listed, nothing linked while the check is running. Real data: a VGT psi map and its reliability map link; a cube and a coarser-pixel variant do not.

## Rollback / Migration
Remove the `/api/compat` route and the compare bar; no stored data changes.
