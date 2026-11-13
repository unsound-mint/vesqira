# ADR 0070: Spectral smoothing and the family view

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
- Maps all on one host are compared on that host (see "A family on a host"). Maps in two places, or some on this computer and some on a host, are refused with a sentence; nothing is copied to compare them.
- Panels share one cursor. Pixels with no value are hatched, never a colour of the scale. The values at the cursor are in a table, so no number depends on a colour.

### A family on a host
- The host's copy of the worker (`hostrun.py`, the same mechanism as the other host analyses) runs `family_agreement` on the files where they are. Only the summary (under 1 MiB, the existing reply bound) and the result file names come back; no map is transferred to this computer.
- The comparison and spread maps are written in the host's derived folder, as temporaries first and then linked into place together, all or none, never replacing a file. The results are registered as host datasets with the record, the host id and the corrections that applied, through the same registration as other host results.
- The run takes the same heavy-task slot as other host analyses and is bounded by the same time limit. Cancelling the request makes the host stop before placing anything (the "placing" stage check), so a cancelled run leaves no files. A host that is unreachable, has an old copy of the worker, or cannot read a member is refused with the host's own reason; the run does not fall back to copying the cubes here.
- Corrections that change what a map means are applied as for any host analysis, and the record names them.

### A family from a sweep
- `GET /api/sweeps/{id}/family?role=` reads the finished jobs of a sweep (ADR 0048). Each job's run has registered outputs with a role; the person chooses one role (the only one is taken). A job contributes one map: its single registered output of that role. A job with none, with several, or without a result yet is listed with the reason.
- Members are labelled by the parameters that differ across the sweep (`sweeps.Label`). Members whose grid, frame or brightness unit differs from the first member's, using the same checks as the family view, are listed with the reason and not compared. An orientation output nobody declared as an orientation is left out, so it is never compared as a scalar. More than 12 maps: the first 12 in the sweep's order are used and the rest are listed.
- This is local only, like the other sweep routes. Nothing is guessed about which file in a run is "the map": the role comes from the run's own record.

### Clean-up of family maps
- `GET /api/analysis/family-maps` lists the datasets that family runs registered (those with a `family_agreement` record), newest first, grouped by run, each with what holds it. `POST /api/analysis/family-maps/remove` removes the chosen ones.
- A map is removed only when nothing holds it. The check is the dataset impact of ADR 0041: every group except the map's own history, other locations, offline copy and provenance record holds it. So a map used by a figure (including as a scene layer), board, report or published review, or one that another dataset was made from, is kept, and the reply says why. The maps of the same request that can go still go.
- Only datasets with a family comparison record are accepted, so a source can never be removed through this route. The removal is the catalog delete of ADR 0041, with its digest check; a file Vesqira wrote on this computer is deleted, a file on a host is left in place (Vesqira deletes nothing on a host) and the reply says so.
- There is no automatic policy. A comparison may be wanted next month, and what refers to it is not something a timer can judge. The person lists and removes.

## Alternatives
- Smooth with a hand-written kernel library: rejected; scipy is robust and the kernels are a few lines of definition on top of it.
- Treat NaN as zero or interpolate across gaps: rejected, it changes the science silently.
- State the effective resolution for every kernel by adding widths in quadrature: rejected; it is true only for Gaussians, and the native response is not known unless declared.
- Compute agreement in the browser: rejected; two implementations of axial statistics would drift, and the worker's is tested.
- Resample members onto a common grid in the view: rejected; reprojection is not the same as matching resolution, and it would hide the refusal the researcher needs to see.
- A new binary endpoint for the agreement maps: rejected for now; registering them as derived datasets reuses the plane endpoint and gives them provenance.
- Fetch host maps and compare them here: rejected; it moves whole maps over a link that is often a flaky VPN, and the host's worker already has the code.
- Pick a sweep job's map by file name or by position: rejected; the run's output role is recorded, a guess is not.
- Delete old family maps after N days: rejected; see above. Deleting a map a board points at would break the board silently.

## Consequences
- scipy 1.18.0 becomes a worker dependency (pinned in `requirements.txt`, and two modules are added to the worker `DEPLOY` manifest).
- No database migration. The labels sent with a family request are used only for the result's text and the records.
- A family run adds derived datasets, listed for removal in the family view.
- A comparison on a host leaves its files on that host after the catalog entry is removed. Deleting there needs a host-side delete that does not exist.
- `figuresOf` in the lifecycle impact now also counts a figure whose scene layers name the dataset, which was missed before and applies to every dataset delete.
- The API reply to a host family run includes the extra result files, so `analysisOutcome` carries a list of files.
- No database migration.

## Validation
- Worker tests (`services/scientific-worker/test_family_smooth.py`): a Gaussian line broadened by a known kernel has the width the quadrature sum gives; the kernels sum to 1 and a spike gives the kernel's weights; NaN is preserved and the minimum-weight rule is applied; an uncalibrated axis gets no unit width; decimation is recorded; strips give the same result as one pass; the 1 and 179 degree axial case has AM 1 and a difference of 2 degrees; a scalar family with mixed units or kinds is refused.
- API test: the family route end to end, the recognition of a family from derived records, and refusals.
- Web tests: scales, the selection and the request the view sends, a refusal sentence, the smoothing form.
- One browser spec (`e2e/tests/zzzzzzzzz-family.spec.ts`): smooth a cube twice through the API, find the family, compare it, read a pixel from the keyboard, take screenshots at 1440 by 900 and 390 by 844 in light and dark, and run axe.
- Golden numbers (`services/scientific-worker/test_family_golden.py`): a 2 by 2 scalar family and a 2 by 2 axial family with hand-computed differences, spread, AM, within-10-degrees share and circular spread, each also recomputed in plain Python without numpy; the 1 and 179 degree case; a pixel that is NaN in one member.
- Host: `hostrun.py` tests for the family op (all-or-none placement of N+1 files, refusals and name collisions that leave nothing, members outside the host's roots). An API test with the fake ssh host checks that a host family equals the same family here, that no map file appears on this computer, and the refusals (mixed places, an unreachable host, a channel out of range) leave no file on the host.
- Sweep and clean-up API test (`family_leftovers_test.go`): labels from the differing parameters, members with another grid left out with the reason, undeclared orientations left out; listing, a map used by a figure kept with the reason, a removable map removed with its file, a source refused.
- Web tests for the sweep panel and the clean-up list. The browser spec also checks the clean-up list and removes the maps.
- Not done: a design review by a second person, a run on a real multi-GB cube, a run against a real remote host, a browser test that opens a sweep (it needs the Slurm stand-ins and an imported run; the API and component tests cover it), and a host-side delete.

## Rollback/Migration
Nothing to migrate. To remove the feature, drop the two routes and the `smooth_spectral` operation, and delete the `family` web folder and the rail button. Derived datasets already made remain valid files with their records.
