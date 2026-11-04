# ADR 0010: Axial orientation maps

- Status: accepted under delegated merge, 2026-11-04; owner review pending
- Date: 2026-11-04

## Context
VGT outputs are orientation maps: angles for which 0 and 180 degrees are the same orientation, with a companion reliability map. Real pipeline files (one product per primary HDU) declare neither that the values are angles, nor their unit, nor the zero direction or sense. An ordinary mean, stretch or spectrum of such values is wrong.

## Decision
- **The interpretation is a user declaration.** A dataset can be declared "axial orientation" with a required unit (`deg` or `rad`) and an optional reliability companion. It is stored in a separate catalog table with the time and `declaredBy: user`, shown as "declared by you", and never written to the file. The unit is never inferred from the values (the interface shows the value range only as a hint). Clearing the declaration returns the map to an ordinary scalar.
- **Companion compatibility.** A reliability map is accepted only if `compat.Grids` finds the same shape, axis types, units, reference values, reference pixels, steps and celestial frame; any value unresolved on either side counts as a reason. Reasons are returned to the user. The same check is the base for comparing maps later. The companion's range is not assumed to be 0 to 1.
- **Statistics are doubled-angle, from a validated provider.** The worker uses `astropy.stats.circmean` and `circvar` on 2 theta (weights supported); the mean orientation is half the circular mean, in [0, 180). The mean resultant length R is always reported. When R is below 1e-6 the orientations cancel and the mean is reported as undefined. Non-finite angles are excluded. Reliability NaN excludes the pixel from the weighted result; reliability 0 keeps it with no influence. Values outside 0 to 180 in the declared unit are counted and wrapped modulo 180. A wrong unit is only partly visible: degrees declared as radians show many out-of-range values, but radians declared as degrees do not, so the result carries `values_fit_radians` (every value within pi while degrees are declared) and the interface warns that the statistics may be invalid.
- **No arithmetic means of angles, enforced on the server.** `GET .../spectrum` on a dataset declared axial is refused with 409. The viewer also stops offering stretch, range and colormap for such maps and uses a cyclic map (twilight) with a text legend, so 0 and 180 have no seam.
- **Display convention.** Orientation lines are drawn from the +x pixel axis towards +y (counter-clockwise on a north-up, y-up picture). This is what the VGT source implements (`atan(ay, ax)` in array index space, then the pipeline's +pi/2 rotation), and the files do not say so; the interface labels it a convention assumed by Vesqira (not declared by the file or the user), never a position angle east of north. A convention field the user sets is a candidate follow-up. Line opacity follows the companion map relative to its own 99th percentile.
- **Declarations are checked again at use.** The declaration stores the file size and modification time it was made against; if the catalogued file differs the declaration is `stale` and statistics are refused until the user declares again. Each statistics request re-checks the companion with `compat.Grids`, which now also compares PC/CD/CROTA, LONPOLE/LATPOLE and EQUINOX and treats a missing unit as a reason. Only single-plane files (two axes longer than 1) can be declared.
- **Axes beyond the image.** A 2D file that kept the cube's CTYPE3 keywords (as real VGT outputs do) opens as a 2D image; WCS axes beyond NAXIS are ignored.

## Alternatives
Inferring unit or axial-ness from the value range (can be wrong silently); keeping the declaration in a FITS header keyword (modifies originals); a custom circular-statistics implementation (an astropy provider exists; the fixture oracle stays independent); showing the arithmetic mean with a warning (the invariant forbids it).

## Consequences
Real VGT outputs open as scalar maps until declared. The reliability pairing is a manual, confirmed step with a file-name suggestion. Mean orientation over a region assumes the same convention for the whole map.

## Validation
Worker: 1 and 179 degrees average to 0 (not 90); 0 and 90 degrees are undefined with R near 0; whole-map result equals the independent oracle in the fixtures; radians equal degrees; reliability weights compared with an independent computation (unknown excluded, zero kept); a tangential-field fixture fixes the handedness (right of centre 90, above 0, diagonals 135 and 45); a 2D map with a stray CTYPE3 opens. Go end to end: unit required, incompatible companion refused with reasons, spectrum of an axial map refused, statistics through HTTP. Web: cyclic colormap ends equal, 0 and 180 colour identically, NaN is no-value, a tangential field draws the right way round, the declaration needs a unit. On the real 2174 x 2174 VGT psi map, the API's mean orientation (152.07 degrees, R 0.4347) and reliability-weighted result (146.26 degrees, R 0.5185) equal an independent numpy computation to full precision, no value lies outside 0 to pi, and the pipeline's own plot shows the same layout (no-data strip on the left, rows up).

## Rollback / Migration
Drop the `interpretations` table and the axial routes; datasets and files are unaffected.
