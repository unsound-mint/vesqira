# ADR 0027: Histogram and limits, zoom and pan, shared colour scale, keyboard cursor

Status: accepted under delegated merge; owner review pending.

## Context
The viewer stretched every plane to its own 1st to 99th percentile with no way to see or set the limits, no zoom for small maps, no way to compare brightness between two maps, and no keyboard route to a pixel.

## Decision
- **Histogram and limits.** The plane's finite values (already in the browser) are binned (64 bins, square-root counts); no-value pixels are not counted and zero is a value. Two limits can be dragged or typed. Setting them switches the range to "Set by hand"; values outside are clipped to the ends of the colour bar. A figure made with hand-set limits says so in its caption ("limits set by the author", with the numbers), and the server accepts `custom` as a range in a figure's view.
- **Shared scale.** For two scalar maps that declare the same brightness unit, "Same colour scale for both maps" starts from the two maps' 1st to 99th percentiles together and then follows the limits set on the map being worked in. Refused with a stated reason for orientation maps and for differing or undeclared units.
- **Zoom and pan.** Wheel, `+`, `-`, `0` and buttons; zoom only changes how the plane is shown (nearest-neighbour, pixels stay square). Drag pans when zoomed in; a click still pins a pixel. Zoom resets when the dataset changes.
- **Keyboard cursor.** Shift+arrows move the pinned pixel (Alt for ten); the coordinate readout and spectrum follow. Plain arrows still step channels. The spectrum plot has a table of the same values.
- Also fixed: two panes in a comparison could overlap because the pane grid column was not allowed to shrink.
- Dropped for now: blink, channel animation, exploration history.

## Alternatives
Computing the histogram on the server (the data is already in the browser; the server's stats supply min and max); a global scale shared by default (hides that two maps were stretched differently).

## Consequences
Implemented. The histogram is of the whole visible plane, not of a region. Limits are kept per viewer session and are not saved with the dataset.

## Validation
Unit tests for histogram counts (non-finite excluded, maximum in the last bin, zero as a value, constant plane), custom-limit rules and the caption; a control test for the typed limits; a browser walkthrough that moves the cursor by keyboard, zooms and fits, sets limits on the histogram, shares a scale across two maps and opens the spectrum table, with axe clean; the compare view was inspected in a screenshot.

## Rollback/Migration
None.
