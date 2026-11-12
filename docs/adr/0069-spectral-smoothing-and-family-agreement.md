# ADR 0069: Spectral smoothing and the family view

- Status: implemented, awaiting owner review
- Date: 2026-11-13
- Related: ADR 0047 (relative orientation, axial statistics), 0049 (channel maps), 0055 (masks and interpretation tiers), 0068 (analysis presets), issue 133

## Context
A researcher compares maps that differ in one parameter: a cube smoothed along the spectrum by 3, 5 and 7 channels, a VGT run at several kernel sizes, a convolution at several beams. Two things were missing. A cube could only be smoothed in space, so there was no spectral smoothing with a recorded kernel. And there was no way to put such maps side by side on one scale and read how much they agree, without opening them one at a time and judging colours.

The agreement has to respect what the maps are. A difference of two orientation maps is not a subtraction (0 and 180 degrees are the same orientation), and maps on different grids cannot be compared pixel by pixel without a resampling that Vesqira does not do silently.

## Decision
### Spectral smoothing (`smooth_spectral`)
A derived operation on a cube, run by the scientific worker with scipy (`scipy.ndimage.convolve1d`). It makes a new dataset and never changes the source. The kernel, the threshold and what they mean are recorded in the result's provenance.
- Kernels, all normalised to sum 1:
  - boxcar: the mean of n consecutive channels;
  - Hanning: a Hann window over n channels without its two zero end points (3 channels give 1/4, 1/2, 1/4);
  - Gaussian: given by its FWHM in channels, sampled at the channel centres and cut at four sigma.
- Boxcar and Hanning widths are odd whole numbers, 3 or more, because an even window would shift the spectral axis by half a channel. Widths over 1025 channels are refused.
- The width is in channels. It is stated in the spectral unit only if the axis is calibrated (it has a spectral step and unit); an uncalibrated axis gets a statement in channels and no invented channel width.
- Missing data: a NaN is NaN in the result. A finite sample becomes the average of the finite samples its kernel reaches (a normalised convolution), if those carry at least a minimum share of the kernel weight (default 0.5, recorded); otherwise it becomes missing. Outside the cube counts as missing, never as zero. The number of samples dropped for want of weight is recorded.
- Resolution: the kernel's own FWHM is always stated. The effective resolution is stated only when the person declares the native response, and only for a Gaussian kernel, where widths add in quadrature. A boxcar or Hanning kernel gets no summed width, and the result says so.
- Decimation (keeping every n-th channel) happens only when asked for, and is recorded. The spectral axis of the result is rewritten to match.
- Memory is bounded: the cube is read in strips of rows.
- Masks are not carried through a smoothing, and the form says so. Orientation maps are refused (there is no spectral axis to smooth, and a plain average of angles would be wrong).
- On a host the operation runs as the existing analysis job; it needs no new transfer.

### The family view (`POST /api/analysis/family`, `GET /api/analysis/families`)
Two to twelve maps on one grid, one chosen as the reference.
- A family is recognised from derived records: results of one source made by one operation whose options are equal except one. It is not recognised from file names. The person can also pick maps by hand.
- Scalar maps: each member minus the reference over the pixels both have, plus the per-pixel spread across all members (standard deviation with n - 1, or the median absolute deviation, stated). A difference map and a spread map are drawn on scales that are shared across panels, symmetric about zero for a signed difference and starting at zero for a spread.
- Axial maps (orientations declared as such): the axial difference in [0, 90] degrees, the alignment measure AM = mean of cos 2 delta against the reference and between every pair, the share within 10 degrees, and a circular spread. A plain difference of angles is never taken. Pixels where the orientations cancel have an undefined spread and are left blank, and counted.
- Maps of mixed kinds, different brightness units or orientations stated in different frames are refused with a sentence. Maps that are not known to share a grid (from the headers, through the same check the other comparisons use) are refused with the reasons; nothing is resampled.
- All numbers are computed once, in the worker, with golden tests. The browser only draws maps and reads values.
- The comparison and spread maps are registered as derived datasets with the full record, so they can be opened, cited and put on a board like any other map. That costs N datasets per run (one per non-reference member and one for the spread). The viewer's plane endpoint serves them, so no new binary transport was added.
- The family view is computed on this computer. A member on a host is refused with a sentence.
- Panels share one cursor. Pixels with no value are hatched, never a colour of the scale. The values at the cursor are in a table, so no number depends on a colour.

## Alternatives
- Smooth with a hand-written kernel library: rejected; scipy is robust and the kernels are a few lines of definition on top of it.
- Treat NaN as zero or interpolate across gaps: rejected, it changes the science silently.
- State the effective resolution for every kernel by adding widths in quadrature: rejected; it is true only for Gaussians, and the native response is not known unless declared.
- Compute agreement in the browser: rejected; two implementations of axial statistics would drift, and the worker's is tested.
- Resample members onto a common grid in the view: rejected; reprojection is not the same as matching resolution, and it would hide the refusal the researcher needs to see.
- A new binary endpoint for the agreement maps: rejected for now; registering them as derived datasets reuses the plane endpoint and gives them provenance.

## Consequences
- scipy 1.18.0 becomes a worker dependency (pinned in `requirements.txt`, and two modules are added to the worker `DEPLOY` manifest).
- No database migration. The labels sent with a family request are used only for the result's text and the records.
- A family run adds derived datasets. A cleanup of old family maps is not built.
- Sweeps of jobs (ADR 0053) are not linked to the view yet: a sweep's outputs are run folders, and mapping a row to its maps needs a rule that has not been decided. The recognised-family list and manual selection cover derived analyses.

## Validation
- Worker tests (`services/scientific-worker/test_family_smooth.py`): a Gaussian line broadened by a known kernel has the width the quadrature sum gives; the kernels sum to 1 and a spike gives the kernel's weights; NaN is preserved and the minimum-weight rule is applied; an uncalibrated axis gets no unit width; decimation is recorded; strips give the same result as one pass; the 1 and 179 degree axial case has AM 1 and a difference of 2 degrees; a scalar family with mixed units or kinds is refused.
- API test: the family route end to end, the recognition of a family from derived records, and refusals.
- Web tests: scales, the selection and the request the view sends, a refusal sentence, the smoothing form.
- One browser spec (`e2e/tests/zzzzzzzzz-family.spec.ts`): smooth a cube twice through the API, find the family, compare it, read a pixel from the keyboard, take screenshots at 1440 by 900 and 390 by 844 in light and dark, and run axe.
- Not done: a design review by a second person, a run on a real multi-GB cube, and a check of the remote path.

## Rollback/Migration
Nothing to migrate. To remove the feature, drop the two routes and the `smooth_spectral` operation, and delete the `family` web folder and the rail button. Derived datasets already made remain valid files with their records.
