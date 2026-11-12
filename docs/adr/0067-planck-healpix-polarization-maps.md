# ADR 0067: Planck HEALPix polarization maps, sampled as Q and U

- Status: proposed; implemented, awaiting owner review
- Date: 2026-11-12
- Related: issue 120; ADR 0042 (declared angle frame and quantity), 0044 (datasets of another kind), 0047 (relative orientation, which listed this as deferred), 0014 amendment (`skyFrame`), 0017 and 0018 (resampling and smoothing are derived results)

## Context
The main comparison for VGT field maps toward the Magellanic Clouds is Planck 353 GHz dust polarization: an HFI SkyMap, a HEALPix binary table in Galactic coordinates with `POLCCONV = COSMO`. Until now a researcher converted it by hand into an angle map, where the sign of U, the turn from Galactic north to the map's north and the 90 degrees between dust polarization and the field are easy to get wrong, and Vesqira could not check any of it because an angle map carries no `POLCCONV` (ADR 0047, Deferred).

## Decision

### A HEALPix map is a dataset of its own kind
- `Dataset.kind` gains `healpix`. The worker recognises a FITS file whose primary HDU is empty and whose first extension is a binary table with `PIXTYPE = 'HEALPIX'` (`healpix_pol.is_healpix`, called from `fits_header.describe`), before the catalog fallback for header-only files.
- The header is `HealpixHeader`: NSIDE, ORDERING, COORDSYS (as written, and the frame it names), POLCCONV, the columns used with their TUNIT, whether QU_COV is present, BAD_DATA and the pixel size. Nothing pretends it is an image: no axes, no WCS, no BUNIT.
- Refused at registration, with a sentence (worker code `refused`, passed through unprefixed): no `POLCCONV`, or one that is neither COSMO nor IAU; no `COORDSYS`, or an unknown one; no ORDERING; NSIDE not a power of two up to 8192; a partial-sky map (explicit index); no Q_STOKES, U_STOKES, QQ_COV or UU_COV; columns that are not unscaled floats; a row count that does not cover 12·NSIDE² pixels. One value per row and Planck's 1024-per-row layout are both read.
- Every route that needs a pixel grid refuses it with 409 and a sentence (`Dataset.HasPixelGrid`, `NoGridReason`): view, planes, interpretation, units, corrections, regions, masks, links, overlays, figures, analysis, relative orientation. The web client narrows on `kind` (`isImage`, `isHealpix`) and keeps it out of every image list.
- No schema change: the kind is derived from the stored header, and results are ordinary derived records.

### Sampling: Q and U, never angles
`POST /api/analysis/healpix-sample` (`datasetId` the HEALPix map, `targetId` a dataset with a celestial WCS, both on this computer) runs `analysis.py healpix_sample`. In order:
1. Read Q, U, QQ, UU (and QU) of only the pixels the kernel needs, memory-mapped. NaN, the HEALPix bad-data value and a negative variance leave a pixel out.
2. **COSMO to IAU:** U_IAU = −U_COSMO, and therefore QU_IAU = −QU_COSMO. An IAU map is not flipped.
3. **Kernel**, over the pixels that have a value (weights renormalised, as convolve does; a target with none is NaN, never 0):
   - `nearest` (default): the pixel holding the target pixel centre;
   - `bilinear`: astropy-healpix's four bilinear weights;
   - `gaussian`: every pixel centre within 3σ, weighted exp(−d²/2σ²) by angular distance; FWHM between the map's pixel size and 600 arcmin.
   Q and U are averaged as the pixels state them, against Galactic north at each pixel, as healpy and reproject interpolate them. This differs from parallel-transporting each pixel to the target only at second order in the kernel radius (the first order cancels in a symmetric kernel): far below the noise at Planck's 1.7 arcmin pixels, and stated rather than corrected for a smoothing of degrees at high latitude.
4. **Frame rotation:** at each target pixel, the angle between Galactic north and the north of the grid's own celestial frame, from astropy (`orientation.rotate_pa_frame`: a short step towards Galactic north carried into the grid's frame). Q and U turn by twice it (Q' + iU' = (Q + iU) e^{2iχ}), and the covariance turns with it (R C Rᵀ). Every pixel of one target turns by the same angle, so turning after the average equals turning each pixel first.
5. **Angle:** ψ = ½ atan2(U, Q), east of north in the grid's own celestial frame, axial in [0, 180).

The worker refuses a map that is not Galactic: `C` does not say whether FK5 J2000 or ICRS is meant, and ecliptic needs an equinox.

### Two products, labelled
- `dust_polarization` is ψ; `b_field` is ψ + 90°, wrapped into [0, 180).
- The result is a FITS map on the target's exact celestial header, so `compat.Grids` accepts it against the target. It is declared by the operation (`vesqira-analysis`): axial, degrees, frame `sky` with no `skyFrame`, and the quantity asked for. Relative orientation adds the +90° for `dust_polarization` itself (ADR 0042), so both products compare the same.
- The derived record and the summary say how it was derived: the convention and whether it was flipped, the frame rotation (with its range on the grid), the sampling, the smoothing, the order, the basis, the treatment of missing pixels and the noise. The policy sentence travels to a reviewer. No beam is written: the file states none.

### Noise
- P = √(Q² + U²), not debiased. σ_P² = (Q² QQ + U² UU + 2 Q U QU) / P², first-order propagation, with each covariance summed with the squared normalised weights (Σ w² var). Without QU_COV the Q–U covariance is taken as 0, and the result says so.
- P and σ_P are registered as companion datasets on the same grid, each with a record naming the angle map (`companionOf`).
- `snrMin` blanks the angle where P/σ_P is below it (σ_P not positive or unknown also blanks); the counts below the threshold and without a ratio are in the summary. P and σ_P are kept everywhere. A comparison is restricted by P/σ_P by sampling with this threshold.
- **Not modelled:** correlated noise between HEALPix pixels, from the beam, the scanning or the smoothing. Pixels are taken as independent, so σ_P understates the uncertainty where they are positively correlated, and neighbouring target pixels that share HEALPix pixels are not independent samples. Every result says this.

### Library: astropy-healpix
`astropy-healpix` 2.0.1 (BSD-3-Clause), pinned in `services/scientific-worker/requirements.txt`, `python3-astropy-healpix` in the RPM.
- It is already installed wherever alignment works, because `reproject` depends on it, so it adds no new footprint there.
- It is a small C extension over numpy and astropy, maintained by the Astropy project, with wheels for every platform the worker runs on, and the HEALPix operations needed here (lon/lat to pixel in RING and NESTED, bilinear weights, cone search, pixel centres).
- Fedora ships 1.1.2; every call used here exists in it.

## Alternatives
- **healpy.** Validated and complete (spherical-harmonic smoothing would treat Q and U as a spin-2 field exactly), but it bundles HEALPix C++ and CFITSIO, is far larger, and is not otherwise needed. If harmonic smoothing becomes necessary, it is the candidate.
- **reproject's `reproject_from_healpix`.** It reprojects a scalar map. It knows nothing of the polarization convention or of the reference direction, and it gives no covariance.
- **Ingest a pre-made angle map.** This is what researchers did by hand: the convention cannot be checked from an angle map.
- **Smooth on the target grid with convolve's kernel.** Target pixels finer than HEALPix pixels share a HEALPix pixel, so Σ w² var over target pixels would understate σ_P badly. Smoothing over HEALPix pixels keeps the sum over independent pixels.
- **Parallel-transport each pixel to the target before averaging.** This was written and then removed. It changes nothing at Planck's resolution, and a "uniform" field in Galactic coordinates is not parallel-transport invariant, so it would also break the exact checks against an independent position-angle computation.
- **A P/σ_P weight through the reliability declaration.** That map is a 0–1 resultant length; P/σ_P is not.

## Consequences
- The worker module `healpix_pol.py` is in `DEPLOY`. Remote HEALPix maps register on a host, but sampling them is refused (this computer only), as for relative orientation.
- `Persistent.Header` passes a worker `refused` answer through as the worker's sentence; `fits_header.py` prints the same, so the one-shot and persistent readings stay identical.
- The web client knows the kind and shows the header facts when a HEALPix map is selected. **Not built:** a screen to sample one (the route is used through the API), remote sampling, harmonic (spin-2) smoothing, writing Q and U themselves as datasets, a σ_ψ map, debiased P, non-Galactic maps, and partial-sky maps.

## Validation
- **Worker** (`test_healpix_pol.py`):
  - A uniform Galactic field (ψ = 30°, COSMO) on a 9 × 9 ICRS grid around the LMC with 2° pixels, where north turns by more than 15° across the grid. Nearest, bilinear and Gaussian agree with 30° + the position angle of the North Galactic Pole at each pixel (`SkyCoord.position_angle`, independent of the code's helper) within 0.001°. On a Galactic grid the angle is 30° with no rotation.
  - The flip: the COSMO and IAU versions of the same field agree, and both are 60° from what an unflipped COSMO map would give at every pixel.
  - b_field = dust + 90° in [0, 180), on a case that crosses 180°.
  - RING and NESTED of a position-dependent field give the same map, equal to the field at the pixel holding each target position turned into ICRS. The same NESTED data labelled RING gives a different map.
  - Noise: nearest σ_P equals the pixel's propagated covariance, and the case would separate a flip of U without QU. The Gaussian kernel's Σ w², with a missing pixel inside some kernels, is recomputed by a brute-force loop over every pixel centre (`SkyCoord.separation`). Bilinear Σ w² is checked against astropy-healpix's weights.
  - A missing pixel stays NaN and is counted. The P/σ_P threshold blanks only the angle.
  - Refusals: each missing or unusable header keyword, a missing covariance column, a non-Galactic map, a kernel smaller than a pixel, parameters out of range. An ordinary FITS table is not taken for HEALPix.
- **Go:**
  - `healpix_test.go`, with the real worker:
    - registration as kind `healpix`;
    - the two refusals in the worker's words;
    - 409 naming HEALPix from view, interpretation, analysis, relative orientation and sampling with the wrong kinds;
    - parameter refusals and the worker's small-kernel refusal;
    - both products sampled onto a VGT map's grid, declared by the operation, with P and σ_P companions, derived records and contract conformance.
  - **End to end through the API:** relative orientation between that VGT map (PSIFRAME = GALACTIC, the same field + 90°) and each product gives AM ≥ 0.9999 over all 16 pixels.
  - `equivalence_test.go` covers the two refused fixtures in both header readers.
- **Web:** `kinds.test.ts` (kind narrowing; HEALPix kept out of image lists).
- **Measured** on one machine with page cache: a synthetic NSIDE 2048 map (1.2 GB, six columns) onto a 200 × 200 ICRS grid.
  - nearest: 0.4 s, 100 MB peak resident memory;
  - bilinear: 0.4 s, 110 MB;
  - Gaussian with a 5 arcmin FWHM: 9 s, 300 MB.
  The table is memory-mapped, and only the rows the kernel needs are read.
- **Not done:**
  - a real Planck PR3 file, which was not downloaded: no network in tests, and 2 GB;
  - owner review of numbers on real data.

## Rollback / Migration
Nothing is migrated. Removing the feature leaves registered HEALPix datasets, which then read as FITS datasets with an unusual header: delete them first. Derived angle, P and σ_P maps are ordinary FITS datasets with records whose operation is `healpix_sample`.
