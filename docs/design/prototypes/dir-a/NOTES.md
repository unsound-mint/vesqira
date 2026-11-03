# Direction A: Precision Editorial (disposable prototype)

All data is synthetic (`../_shared/assets/`). Every page carries an "ILLUSTRATIVE MOCK DATA" flag. Cursor values, hashes, owners and dates are invented. This is a design exploration, not an approved design.

Open the HTML files directly (file://). No build step, no network, no framework. **No font files are bundled**; stacks are system fallbacks only (`IBM Plex Sans`/`Inter` -> system-ui; `Source Serif 4` -> Georgia; `JetBrains Mono` -> ui-monospace). Screenshots therefore render in whatever system fonts the capture machine has.

Pages: `01-explorer.html`, `02-viewer.html`, `03-compare.html`, `04-board.html`. State via `?state=` (visible "prototype state switcher" in the top strip) and `?theme=dark|light`.

## Concept

Light-first, warm cloud-white paper, ink text. The scientific image is a journal "plate": framed, with true axis ticks (RA/Dec computed from a mock TAN-style WCS, or native pixels when WCS is absent). The inspector is marginalia: ruled sections beside or below the plate, not cards. Serif only for page titles, plate titles, large numerals and figure captions. Navigation is a slim top bar (breadcrumb, 4 tabs, command search), no icon rail.

## Tokens actually used (style.css)

| Token | Light | Dark |
|---|---|---|
| bg / alt / paper | #FAF8F3 / #F1EEE6 / #FFFFFF | #121618 / #1B2124 / #171C1F |
| ink / graphite / muted | #1C2127 / #424A54 / #596069 | #ECE8DF / #C4C8CC / #A2A9AF |
| rule (decorative) / ctl (control borders) | #D8D2C4 / #7C7567 | #30383D / #858F96 |
| accent / strong / wash / on-accent | #0A6772 / #07505A / #DCEDEC / #FFFFFF | #58C0CA / #7DD3DB / #16363A / #08171A |
| ok (+wash) | #1D6B3B (#E4F1E7) | #6FCB8E (#173020) |
| warn (+wash) | #8A4B00 (#F8EBD3) | #E8B35A (#3A2D12) |
| bad (+wash) | #A12B22 (#F6E0DC) | #F0847A (#3D1C19) |
| plate backdrop | #0B0D10 | #050607 |

Type: sans 15px body (13 to 14 for controls, 12 minimum), tabular lining numerals everywhere; serif 26 to 30 titles, 34 velocity numeral; mono 12 to 13 for paths, coordinates, readouts. Spacing on a 4px grid (4/8/12/16/24/32).

Status words always appear as text with a glyph and border style (verified solid, declared tinted, inferred dashed warm, unresolved dashed red); colour is never the only cue. Masks use hatching plus dimming plus legend text.

## Layout per breakpoint

- 1440x900: Explorer = table + 300px marginalia. Viewer = plate (sized to viewport height, up to 760px) left, two-column marginalia right (readout, spectrum | display, layers, notices); channel scrubber sits directly under the plate. Compare = three plates in a row, legends, then three ruled columns (checklist, summary, provenance). Board = three figure panels + review marginalia 300 to 340px.
- 1280x800: same structure, narrower marginalia (272 to 328px). At <=1180px marginalia collapses below the plate/table/figure in two columns.
- 390x844: plate full width, scrubber and a one-line cursor readout under it; inspector and controls move to a bottom drawer (fixed bar shows channel, velocity and notice count; panel is full-height). Explorer table becomes stacked rows. Compare/board stack, panels full width.

## Keyboard workflow (viewer)

| Key | Action |
|---|---|
| Left / Right | previous / next channel (works on plate, slider, or page body) |
| Shift + Left / Right | step 10 channels |
| Home / End | first / last channel |
| Ctrl + arrows (Shift = 10) | move cursor readout without a mouse |
| R / M | toggle ROI / mask overlay |
| ? | shortcut dialog |
| / | focus command search |
| Esc | leave plate, close drawer; dialogs close natively |

Alt+arrows deliberately not used (browser Back/Forward). Board: reviewer comment form, request form and denied dialog are all native controls. Explorer: filter chips are toggle buttons (`aria-pressed`); file names are buttons that update the marginalia.

## What each state shows

- Explorer: `ready` = 12 files incl. missing-WCS, remote-OFFLINE and corrupt rows, working filters (type, WCS, source, text; zero-result row). `empty` = empty-project onboarding.
- Viewer: `ready`; `loading` (plane 128 of 256, 4 MiB of 5.1 GiB, progress, cancel, dimmed previous plane, values "pending"); `error` (range request failed, cached 64x64 preview drawn pixelated, values "unavailable (not 0)", spectrum gap "not fetched", retry); `nowcs` (banner text as specified, pixel ticks, native-pixel scale bar, RA/Dec "unavailable", velocity still shown because spectral keywords are intact); `nospectral` (velocity unavailable, channel index only, spectrum x-axis in channel index).
- Compare: `ready` (linked view note, cyclic axial legend 0 = 180, hatching legend, sequential 0 to 90 difference legend, checklist with pass/unverified, stats 6.51 / 15.56 / 0.81 from stats.json with the doubled-angle note, provenance with the four words); `blocked` (grids differ in ticks and caption, difference map and numbers withheld with reason, "Resample with stated policy" opens a proposed policy using doubled-angle components, pin disabled).
- Board: `owner` (share scope, expiry, pin status incl. "newer revision available", comments); `reviewer` (scope summary "can see / cannot see", expiry, anchored comment, request comparison, no paths shown, source is a locked button); `denied` (dialog explaining why and the next step). No SSH/HPC credentials appear anywhere; the HPC path is shown only in the owner view.

## Verified (actually run, `python3 capture.py all`)

- 33 screenshots in `screenshots/` (4 pages x 1440 light, 1440 dark, 1280 light, 390 light; 4 viewer states at 1280; viewer drawer and nowcs at 390; explorer-empty, compare-blocked, board-reviewer, board-denied at 1440 and 390; three dark variants of states). I read a subset visually (viewer ready/loading/error/nowcs/drawer/dark/390, explorer 1440/1280/empty, compare ready/blocked, board owner/denied, mobile contact sheet) and fixed what I saw.
- Console errors/warnings/page errors/failed requests: 0 across all captures and the keyboard pass.
- Horizontal scroll at any captured size (incl. 390): none.
- Keyboard: Tab reached every counted control on all four pages at 1440 and on the viewer at 390; each focused element had a computed outline of at least 2px. Viewer arrows/Shift/Home/End, Ctrl+arrow, R, ? confirmed to act as listed. The 1 to 4 count differences are radio groups and the date input (one tab stop each); not otherwise investigated.
- Contrast: script walks every visible text node of 10 page/state combos in light and dark (4,206 text nodes), computes ratio against the first opaque ancestor background, threshold 4.5 (3 for large). 0 below threshold. Excluded from the check: text on image overlays (plate notes, scale bar, ROI/pin labels, which use fixed black/white backing) and disabled controls.

## Not verified / known defects

- Not run: axe, screen reader, 200 percent zoom, forced-colors, touch interaction, real mobile browsers, Firefox/Safari. Non-text contrast of focus rings, borders and plate overlays was not computed. Image overlay text contrast was eyeballed only.
- Fonts: the intended families are not bundled and may not be installed; metrics will differ from the screenshots.
- Mock behaviour: min/max/stretch/colormap only change labels and the colour bar; the five channel images are the nearest synthetic plane, not a real cube. Cursor values are a hash function, not pixel reads. Pan/zoom and ROI dragging are not implemented (ROI fixed; hover linking only on compare). Comment/request forms do not persist.
- Full-page screenshots of 390 pages with the fixed drawer bar show it mid-page; the viewer 390 shots are viewport-only for that reason.
- Some tables truncate long paths (title attribute carries the full path); at 1280 the explorer needs 8 columns in about 940px and is tight.
- 100+ channel scrubbing, very long names beyond the sample, and zero-valid-pixel states are not designed here.
- Date input renders in the browser locale format.

## Cognitive trade-offs

Researcher: strong for focused inspection: large plate, tick labels, readout and spectrum visible together, honest per-state wording, keyboard-first. Weaker for dense multi-panel comparison and for bulk table work (the explorer is a plain table, no saved views); the plate is square-fixed, so very wide fields would need a different frame. Marginalia shows many sections at once, which can feel like a lot beside a calm plate; the two-column inspector asks the eye to travel.

Supervisor: strong: serif captions, pinned/newer-revision badges, an explicit "can see / cannot see" statement and fixed snapshot semantics read like a figure in a paper, and need no tool knowledge. Weaker: little at-a-glance triage across many boards (no list view), and the editorial typography spends vertical space, so long review threads and many panels scroll more than in a denser direction.
