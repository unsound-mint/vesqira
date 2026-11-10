# ADR 0035: Tiles and a multi-resolution pyramid for very large planes

- Status: proposed (deferred until a trigger below is measured; nothing implemented)
- Date: 2026-11-10

## Context
Today the viewer fetches one whole plane per channel (ADR 0008) and draws it as a texture (ADR 0034). The worker refuses planes above 2^24 pixels (16.7 M, 64 MiB as float32). Below that limit the whole-plane design is cheap and exact: a 4.2 MP plane arrives in 36 to 52 ms over loopback and draws in about 20 ms (ADR 0034). On a remote host a large plane is first requested reduced (`step=N`, a block mean marked "not for measurement") and the full plane follows 150 ms after the last change.

Whole planes stop being enough when a plane is larger than the worker or GPU limits (wide mosaics, survey cubes), when the link is slow enough that one plane takes seconds, or when the user zooms into a small part of a large plane and the rest is wasted transfer. None of these is observed yet: no registered file in use is above the limit, and the GPU path has no measured memory problem. This ADR records the design to use when one is, so the trigger is a measurement and the first version is not invented under pressure.

## Decision
- **Trigger, not date.** Build this only when one of these is measured on real data: a plane the worker refuses (above 2^24 pixels) that a researcher needs to view, a remote full-plane request slower than one second at the user's real link, or a GPU texture allocation failure or memory pressure on a plane the worker accepts. Until then planes stay whole.
- **Unit of transfer.** A tile is a rectangle of at most 512 x 512 native pixels at one pyramid level, requested as `GET /api/datasets/{id}/tile?channel=N&level=L&x=X&y=Y`, answered in the plane wire format of ADR 0008 (header plus little-endian float32, same statistics and missing-value meaning, plus the tile's native pixel rectangle and the level's reduction factor). The worker reads only that region with the existing selective read; it never loads the cube.
- **Levels.** Level 0 is native. Level k reduces by 2^k with the mean of the finite pixels of each block, NaN where a block has none, exactly the reduction ADR 0008 already defines for `step`. A reduced tile is a display aid: it carries `preview: true` semantics (readouts say "preview only", no histogram, no saved figure from it). Levels are computed on request from the file and kept in the bounded server cache keyed like planes (dataset, size, mtime, operation, indices); there is no precomputed pyramid file and no copy of the data until a measurement asks for one, and then only as an explicit derived resource (ADR 0018 managed copies).
- **Orientation maps are never reduced.** A block mean of axial angles is wrong (AGENTS.md invariant 4, ADR 0010). They are served only at level 0, tile by tile; zooming out on a large orientation map shows the native tiles that fit, or says it cannot.
- **Readouts and regions always use native pixels.** Coordinates, pixel values, region statistics and spectra never read a reduced tile; they use level 0 or the existing endpoints. Pixel indices keep their meaning on the full grid, as the current preview does.
- **Client.** The renderer of ADR 0034 draws visible tiles, each a texture, level chosen from the zoom so one texel is at least one screen pixel, finer levels replacing coarser ones as they arrive; the coarsest level of the whole plane is requested first so something is always on screen. Textures are deleted when a tile leaves the view; a bound on tiles in flight and on texture memory is part of the design. The CPU fallback path does not support tiled planes: it shows a clear message that the plane is too large for this browser, rather than silently reducing it.
- **Limits.** The worker keeps its memory cap and time limit per tile; the tile lane is the interactive lane of ADR 0008 (a slow spectrum must not delay it); a client that goes away cancels queued tiles.

## Alternatives
- **Precomputed pyramid files (HiPS, Zarr, tiled FITS):** the best cold-read performance and a standard format for archives, but it copies or rewrites the researcher's data, which AGENTS.md says not to do by default; revisit as an opt-in derived resource.
- **Server-side image tiles (PNG/JPEG):** small and cacheable but bakes stretch and colormap in, loses values, and breaks the missing-value and axial semantics. Not acceptable for a measurement display.
- **Raise the 16.7 M limit:** moves the problem to browser and GPU memory; a 100 MP float32 plane is 400 MB.
- **Do nothing:** correct while no real plane exceeds the limit.

## Consequences
A second endpoint and a tile cache to keep coherent with the plane cache (same staleness key, same 409 rule on a changed file), a tile scheduler in the client, and a reduced-tile state that every readout must respect. The preview rule becomes a general rule for any non-native level, which is simpler than today's special case.

## Validation
- Tile reassembly at level 0 equals the whole-plane read, pixel for pixel, including NaN and BLANK, on fixtures with a non-divisible size, a degenerate axis and a plane wider than one tile.
- A reduced tile equals an independent numpy block mean of the finite pixels; a block with no finite pixel is NaN, and zero stays a value.
- An axial map is refused at any level above 0.
- Readouts, spectra and region statistics are identical with and without tiles active.
- Bounds: bytes per tile, tiles in flight, worker memory per tile (peak RSS) and texture memory, in `make bench` and `make viewer-bench` budgets, cold and warm; remote partial-read failure and cancellation are exercised.

## Rollback / Migration
Additive: the whole-plane endpoint stays. If tiles are removed, planes above the limit are refused as today.
