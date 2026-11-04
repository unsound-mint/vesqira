# ADR 0009: Region spectrum semantics

- Status: accepted under delegated merge, 2026-11-04; owner review pending
- Date: 2026-11-04

## Context
A region spectrum is the first derived scientific quantity in the viewer. Its meaning must be stated by the product, not guessed by the reader: which pixels count, what happens to missing data, and what "size" means.

## Decision
- **Shapes.** A rectangle (inclusive pixel bounds) or a circle (centre pixel and whole-pixel radius). A pixel belongs to the region when its centre lies inside. Coordinates are 0-based on the wire and 1-based in the interface. A region that overhangs the image is clipped, and the interface says "N of M pixels, clipped".
- **Mean.** Per channel, the arithmetic mean of the finite pixels inside the region, computed in float64 from the scaled file values (BSCALE/BZERO applied, BLANK excluded). NaN, BLANK and infinite values are excluded and counted per channel. There is no weighting, no noise model and no beam correction.
- **Unknown is not zero.** A channel with no finite pixel in the region has a NaN mean on the wire and a gap in the plot. A region with no finite pixel in any channel draws no spectrum and says so.
- **Flags.** Provider-flagged channels keep their data and are marked on the axis; they are not excluded from the mean.
- **Size.** The pixel count is geometric. Area and pixel scale come from astropy's celestial pixel scales at the reference pixel, labelled as such; projection distortion across the region is ignored. Without a resolved celestial WCS the area is "unknown", never a pixel-derived guess.
- **Axis.** Spectral values come from the worker's WCS when the axis is resolved (shown as km/s or MHz for reading only); otherwise the plot uses channel numbers and says the spectral axis is unresolved.
- **Transport and bounds.** `GET /api/datasets/{id}/spectrum?shape=rect|circle&...` returns the plane frame format with payload `float64 axis[C], float64 mean[C], uint32 count[C]` (little-endian, 8-byte aligned). Region pixels times channels is capped at 200 million, radius at 4096 pixels, channels at 65,536; the request is cached by region and file signature.

## Alternatives
Median or sigma-clipped means (a scientific choice the user has not made); weighting by the beam (needs a declared beam); computing the spectrum in the browser (would need every plane); polygon regions (not needed for v0.1).

## Consequences
The mean is a plain average, appropriate for a first look and not a measurement of flux. Later statistics (median, noise, moments) need their own stated policies.

## Validation
Worker tests compare means and counts with an independent numpy computation on the generated cube (including a block of NaN pixels and an all-NaN region), check circle membership against the pixel-centre rule, clipping, BLANK exclusion on the scaled integer cube, channel-number fallback without a spectral reference, and bad regions failing in a controlled way. An end-to-end Go test finds the synthetic source's peak channel through HTTP. On the real 3 GB LMC cube a 106,602-pixel region over 159 channels returned in about 1.9 s including drawing.

## Rollback / Migration
Remove the route and the worker op; no stored data changes.
