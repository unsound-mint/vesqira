# What Vesqira will never do

These are the promises Vesqira makes about your data, written for the person who owns it. They are the ten scientific invariants of `AGENTS.md` in plain words. Each one lists the automated check that fails if the promise is broken. Where there is none, this page says so. A passing test is evidence on synthetic fixtures (`fixtures/generate.py`), not a validation against a published benchmark; see the status in `README.md`.

Paths are relative to the repository root. Tests named `test_*.py` run with `make check`, Go tests with `go test` in `services/api`, web tests with `npm test` in `apps/web`, and `e2e/` with `make e2e`.

## 1. It will not invent units, calibration or a coordinate frame

If a header does not say what the unit, pixel scale or frame is, Vesqira shows `unknown` and shows pixels, not a guessed physical value. It never fills in 1 or 0.

- A missing spectral reference is reported as unresolved, not replaced with defaults: `services/scientific-worker/test_plane_worker.py` (`test_missing_spectral_reference_is_unresolved_never_default_values`, `test_no_celestial_wcs_falls_back_to_pixels`, `test_spectrum_without_spectral_reference_uses_channel_numbers`).
- A legacy `VELO-LSR` axis is not given a convention or frame it did not declare: same file, `test_legacy_velo_lsr_is_not_given_a_convention_or_frame`.
- A frame is inferred only when the header gives no better source, and is labelled inferred: `test_celestial_frame_is_inferred_from_equinox_only_when_radesys_is_absent`.
- Unit conversions refuse to guess: `services/scientific-worker/test_analysis.py` (`test_scalar_difference_is_exact_keeps_missing_missing_and_refuses_unit_guesses`, `test_moment_refuses_when_the_spectral_axis_is_not_declared`).
- Fixture level: `fixtures/test_fixtures.py` (`test_missing_spectral_reference_is_detectable`, `test_unlabelled_cube_has_no_celestial_keys`).

## 2. It will not assume which way your axes run

Axis order, direction and meaning come from the header, not from habit.

- Spectral axis located from the WCS, 4D cubes with a degenerate Stokes axis, reversed spectral axes, rotated (PC matrix) WCS: `services/scientific-worker/test_plane_worker.py` (`test_view_locates_axes_from_the_wcs`, `test_4d_cube_with_degenerate_stokes_uses_freq_not_axis_3`, `test_a_reversed_spectral_axis_follows_the_header_not_the_channel_index`, `test_a_rotated_wcs_is_read_with_its_pc_matrix`).
- Coordinates readouts agree with an independent projection formula: `test_coordinate_readout_matches_independent_tan_and_spectral_formula`, `test_sky_positions_land_on_the_pixels_the_independent_projection_says`; fixtures: `fixtures/test_fixtures.py` (`test_shape_and_axis_order_are_native_fits`, `test_off_axis_positions_match_independent_tan`).
- Cutouts and slabs keep exact coordinates: `services/scientific-worker/test_analysis.py` (`test_cutout_keeps_every_coordinate_exact`, `test_slab_keeps_the_spectral_axis_exact`).

## 3. It will not quietly combine measurements that do not match

Two maps are compared only when their grids, units and definitions agree. Every difference is listed. Putting a cube on another grid (reprojection) is a separate, visible step and not the same as matching resolution; convolution to a common beam refuses what it cannot do.

- Grid and unit comparison: `services/api/internal/compat/compat_test.go` (`TestEveryDifferenceIsReported`, `TestMissingUnitOnBothSidesIsNotAMatch`, `TestUnresolvedOnBothSidesIsNotAMatch`).
- Comparison is offered only for the same definition and unit: `apps/web/src/viewer/compareInspector.test.tsx`.
- Alignment puts NaN outside the grid and uses the proper frame transform: `services/scientific-worker/test_analysis.py` (`test_align_puts_a_cube_on_the_reference_grid_with_nan_outside_never_zero`, `test_aligning_across_frames_uses_the_proper_transform`); beam: `test_it_refuses_what_convolution_cannot_do`, `test_jy_per_beam_is_rescaled_by_the_beam_area_and_kelvin_is_not`.
- Deliberately different fixture variants exist to exercise this: `fixtures/test_fixtures.py` (`test_incompatible_variants_differ_where_they_should`).

## 4. It will not average angles as if they were ordinary numbers

Magnetic-field orientations from VGT are axial: 0 and 180 degrees are the same direction. Means, differences and weights use axial statistics. If the orientations cancel out (for example 0 and 90 degrees), there is no mean and Vesqira says so.

- 1 and 179 degrees average to 0, not 90: `fixtures/test_fixtures.py` and `services/scientific-worker/test_plane_worker.py` (`test_one_and_179_degrees_average_to_zero_not_ninety`).
- Perpendicular pair has no mean: `test_perpendicular_orientations_have_no_mean`, `fixtures/test_fixtures.py` (`test_perpendicular_pair_is_degenerate`).
- Whole map against an independent oracle, radians declared as degrees flagged, reliability weights, nonsensical axial requests refused: `test_whole_map_matches_the_independent_oracle_and_skips_nan`, `test_radians_declared_as_degrees_is_flagged_and_the_right_unit_is_not`, `test_reliability_weights_unknown_excluded_zero_kept`, `test_axial_requests_that_make_no_sense_are_refused`.
- Axial difference is modulo 180: `services/scientific-worker/test_analysis.py` (`test_axial_difference_is_modulo_180_and_keeps_missing_missing`).

## 5. It will not treat zero as missing, or missing as zero

Blank, NaN, flagged and excluded pixels are different things. A region with no valid pixel has an unknown mean, not zero. A real zero is drawn and counted as a value.

- NaN and blank are skipped and counted: `services/scientific-worker/test_plane_worker.py` (`test_plane_matches_a_full_astropy_load_including_nan`, `test_int16_scaling_blank_is_nan_and_real_zero_stays_zero`, `test_blank_pixels_do_not_enter_the_mean`, `test_all_nan_region_is_unknown_not_zero`, `test_float64_beyond_float32_range_is_counted_not_called_missing`).
- Flags are separate from data: `fixtures/test_fixtures.py` (`test_flagged_channels_keep_finite_data_and_are_flagged_separately`, `test_nan_block_is_missing_never_zero`, `test_map_keeps_zero_reliability_distinct_from_missing`).
- Analysis products keep missing missing: `services/scientific-worker/test_analysis.py` (`test_moment0_matches_an_independent_sum_and_missing_is_not_zero`, `test_missing_pixels_stay_missing_and_nothing_becomes_zero`).
- What you see: `apps/web/src/viewer/viewer.test.tsx` ("draws NaN as striped no-data and zero as a real value", "formats unknown, no-value and zero distinctly"); `apps/web/src/viewer/orientation.test.tsx`; `apps/web/src/viewer/compareInspector.test.tsx` ("a metric a run did not record reads as unknown, not zero").

## 6. A file that opens is not a result that is correct

Reading a file, or a job finishing, says nothing about whether the science is right. A truncated file is an error, not a zero-filled image.

- `services/scientific-worker/test_plane_worker.py` (`test_truncated_data_is_an_error_not_a_zero_filled_plane`, `test_a_cube_cut_short_still_serves_the_planes_that_are_there`, `test_changed_file_is_re_read`).
- Header values are shown as the file's own claims: `apps/web/src/review/review.test.tsx` (header values "are not verified").
- Scientific quality control (beyond the assessment label in item 9) has no automated guard yet.

## 7. It will not pretend to know where a number came from

Provenance is labelled verified, declared, inferred or unresolved. A run record that is absent is never shown as a reassuring one.

- `services/api/internal/runs/repro_test.go` (`TestReproducibilityNeverTurnsAnAbsentRecordIntoAReassuringOne`); `services/scientific-worker/test_plane_worker.py` (frame status "inferred", above); `apps/web/src/catalog/run.test.tsx`.
- The four labels as a closed vocabulary across the whole product have no single automated guard yet; the guards above cover the places that set them today.

## 8. It will not change your original files

Registering, reading, analysing and sharing never write to the source file. New results are new files with their own provenance. A changed source file is noticed.

- Inputs are never modified and a failed analysis leaves no output: `services/scientific-worker/test_analysis.py` (`test_inputs_are_never_modified_and_failures_leave_no_output`); API level, including the byte-for-byte managed copy and the untouched input: `services/api/internal/server/analysis_test.go`.
- A file that changed on a remote host is refused until registered again: `services/api/internal/server/hosts_test.go`.
- Derived results (masks, normalisations, outputs) as distinct resources: covered by the analysis tests above; no separate guard for masks and overlays yet.

## 9. It will not stamp a result "successful" or "valid"

Finishing a computation, passing a scientific check and your interpretation are three different things. A new run starts as "not assessed"; only the researcher sets the assessment, and earlier ones are kept.

- `services/api/internal/server/planes_test.go` (the assessment checks: unknown word refused, history kept).
- Journeys across jobs, imports and review: `e2e/tests/walkthrough.spec.ts` ("the context-supplement gaps: preview, managed copy, nature, assessment ...").

## 10. It will not invent statistics

Methods for coordinates, statistics and units come from established libraries (astropy, wcslib, numpy) and are checked against independent computations, not hand-written approximations.

- Region spectra and moments against numpy and closed forms: `services/scientific-worker/test_plane_worker.py` (`test_rect_spectrum_matches_an_independent_numpy_mean_and_counts_nan`, `test_spectrum_is_the_same_whatever_the_block_size_and_the_reader`), `services/scientific-worker/test_analysis.py` (`test_stats_match_numpy_and_count_what_is_missing`, `test_moments_1_and_2_of_a_selected_line_match_the_closed_form`).
- There is no check that forbids adding a new, unvalidated method; that is a review rule (`CONTRIBUTING.md`).

## Promises about who can see your data

Not one of the ten invariants, but the other thing a supervisor and you rely on: a review link shows only what you published.

- A reviewer sees only the snapshot and its previews, none of the owner API, and every bad link looks the same: `services/api/internal/review/review_test.go` (`TestReviewerSeesTheSnapshotAndItsPreviewsOnly`, `TestNothingOfTheOwnerAPIIsReachable`, `TestEveryBadTokenIsTheSame404AndFailuresAreLimited`).
- Reviewer code cannot import anything beyond the snapshot store: `services/api/internal/arch/arch_test.go`.
- Source locations and credentials are not exposed to reviewers: `services/api/internal/server/boards_test.go` (`TestFigureBoardReviewPathExposesNoSourceToTheReviewer`), `services/api/internal/server/planes_test.go` (`TestRunImportLinksOutputsAndKeepsLocationsFromReviewers`).
- Files outside the folders you chose are never read, and symlinks are refused: `services/api/internal/sourceroot/sourceroot_test.go`, `services/api/internal/server/server_test.go` (`TestRegisterRefusesPathsOutsideRootsAndNeverCallsWorker`), `services/scientific-worker/test_plane_worker.py` (`test_symlinked_path_is_refused_and_errors_do_not_echo_paths`, `test_everything_outside_the_roots_is_the_same_refusal`).
