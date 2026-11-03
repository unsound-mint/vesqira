# Direction B: "Dark Observatory" (disposable prototype)

Open `01-explorer.html` ... `04-board.html` directly from disk (no build, no network). Every page carries an `ILLUSTRATIVE MOCK DATA` pill in the title bar (a full-width strip on 390 px) and, on image surfaces, a marker on the canvas or figure. All imagery is the shared synthetic set in `../_shared/assets/`. Values in HUDs are mock: 64x64 samples baked into `data.js` from those PNGs, plus the inlined `spectrum.json` and `stats.json`. No fonts are bundled: system stacks only (`IBM Plex Sans`, `Inter`, `system-ui`; mono `JetBrains Mono`, `ui-monospace`) are used if the viewer already has them. Nothing here is approved design or a scientific result.

## Concept
An instrument/IDE-style workbench: 56 px icon rail (Datasets, Viewer, Compare, Boards, Review; labels as tooltips, visible labels in the mobile bottom bar), dockable explorer (left), tabbed inspector (right), bottom dock (channel timeline strip + spectrum/data-table), command palette (Ctrl+K) as the power-user path, status bar (source, RTT, cache, WCS, cursor readout). The image is the work surface; readouts float as HUDs. Prototype state is switched with the dashed "Prototype state" select (or `?state=`); `?theme=light|dark` forces a theme.

## Tokens
| Token | Dark | Light |
|---|---|---|
| canvas (`--bg0`) | #0e1115 | #101317 (stays dark on purpose: data on black) |
| panel (`--bg1`) / raised (`--bg2`) | #151a20 / #1c232b | #f6f7f8 / #ffffff |
| line / line-strong | #2b3641 / #44525f | #d2d8de / #98a4af |
| text / text2 / text3 | #e9eef3 / #b7c2cd / #94a1ae | #13181d / #38434d / #51606c |
| copper (primary accent) | #e09a6b | #8f4515 |
| cyan (data/secondary) | #62cde0 | #06647a |
| ok / warn / err | #7dd49f / #ecc05f / #ff8f87 | #17673a / #7c5200 / #a3231b |
| focus ring | #8fdcff | #0a5fc4 |
| HUD (both themes) | rgba(10,13,17,.82), text #eef2f6 | same |

Type: UI 13 px, mono for coordinates, paths, hashes; tabular numerals; 4 px grid. Provenance words always icon + word with distinct border style (solid verified, solid declared, dashed inferred, dotted unresolved).

## Layout and panel behaviour
- **1440x900**: rail, explorer (248), canvas, inspector (320); bottom dock shows spectrum and data table side by side under the timeline. Compare and Board start with the explorer closed/hidden to give the images width.
- **1280x800**: explorer auto-collapses (E reopens it as an overlay-width column), dock becomes tabs (Region spectrum / Data table), inspector 296, timeline axis labels dropped.
- **390x844**: full-screen canvas, rail becomes a 56 px bottom bar, explorer/inspector/dock become bottom drawers (toolbar buttons Files / Layers / Spectrum; Esc or scrim closes). Explorer page is tree, then detail with a Back button. Board is a single scrolling column, read-only review first.

## Keyboard
| Key | Action |
|---|---|
| Ctrl+K or / | open command palette; Up/Down, Enter, Esc |
| Left/Right or J/K | channel -1/+1 (viewer) |
| Shift + those | +/-10 channels |
| Home / End | first / last channel |
| E / I / D | toggle explorer / inspector / dock |
| R | ROI on/off |
| T | theme |
| Tab / arrows | tabs, tree (Up/Down/Left/Right/Enter) follow ARIA patterns |

## States shown
Explorer: populated (valid WCS, remote-available, local), WCS missing, remote OFFLINE, corrupt, empty project. Viewer: default, loading (plane 128/256, 4.0 MiB of 5.1 GiB session, cancel), error (partial range failure, cached preview), no-wcs, no-spectral, palette open. Compare: default, blocked (incompatible grids, numeric comparison withheld, stated resampling policy, nothing auto-resampled). Board: owner (rev 8 available), reviewer (scope summary, anchored comments, request comparison, expiry), reviewer denied (raw-data link, no path shown, next steps).

## Verified (all run in this session)
- `capture.py` (Playwright, Chromium, file://): 34 PNGs plus 2 mobile drawer PNGs from a keyboard/drawer script = 36 files in `screenshots/`. I looked at the 1440 and 1280 viewer, compare, board, explorer, palette, loading, no-wcs, no-spectral, blocked, denied and the 390 shots; fixed overflow, overlaps, truncation, role leakage in the status bar, and hidden explorer at 1280. Not every one of the 36 files was opened individually.
- Console errors / page errors / failed requests across all captures: **0**. Horizontal scroll at 390 px: **none** on all captured pages.
- Keyboard (scripted): arrows, Shift+arrows, Home/End, J/K change the channel as listed; Ctrl+K opens palette with focus in the input; typing `theme` + Enter toggles theme; Esc closes; Up/Down/Enter in the palette; Tab order is rail, title bar, explorer, toolbar, notice, timeline, dock, inspector; all 33 stops had a 2 px outline; skip link present; mobile drawer opens and Esc closes.
- Contrast: computed text/background pairs for about 40 text selectors on 9 page states in both themes, plus every visible tab, at 1440x900: **0 pairs below 4.5:1** (HUD text measured against a worst-case white backdrop). Token spot checks on panel bg: dark text 15.0, text2 9.7, text3 6.6, copper 7.5, cyan 9.4; light text 16.6, text2 9.4, text3 6.0, copper 6.5, cyan 6.3, warn 6.4; primary button 8.1 dark / 6.9 light.

## Not verified
- No screen reader run; no axe scan; ARIA tree, slider, combobox and tab patterns are by construction only.
- Contrast was not measured at 1280/390 or on non-text UI (focus ring vs neighbours, hatch vs image). Colour-vision simulation not done.
- Touch interaction not tested (pointer events only emulated by mouse); drawers tested by click, not swipe.
- Firefox/Safari not tried; `container-type: size` and `100dvh` assumed.
- `prefers-reduced-motion` is handled in CSS (no animation exists beyond a 0.1 s chevron turn) but was not toggled in a test.
- The no-wcs, no-spectral and loading variants were seen at 1280/1440 only, not at 390.

## Known defects / limits
- Colormap, range and stretch controls change labels and colour bars only; planes are pre-rendered magma PNGs. Only 5 sample planes exist, so other channels show the nearest sample (the HUD says so).
- Cursor values are decoded from synthetic PNG colours at 64x64; the fictional WCS (SIN, 10 arcsec/px, 2304 px grid vs 512 px preview) is mock. Axial angles in Compare are decoded from hue.
- Pin/click-to-anchor on the board is a fixed set of two pins; comments you post are DOM-only, not stored. Request-comparison and Resample buttons do nothing real.
- ROI is a fixed rectangle (not draggable); the spectrum is always the shared spectrum, not recomputed.
- At 1280x800 the spectrum plot is only about 90 px tall (tradeoff for a large canvas); at 1440 the image is limited by canvas height to about 465 px square, so side letterbox is used for HUDs.
- Compare panels are limited by width (3 across) and are smaller than the viewer image; at 390 they stack vertically with no side-by-side flicker view.
- Light theme keeps the viewing surface dark; some reviewers may expect a fully light canvas.
- Explorer "Used by" and the "rev 8" content are invented mock context.
- Board source links in owner view show full HPC-style paths (title attribute); reviewers see "source: restricted". No credentials appear anywhere.

## Cognitive trade-offs
**Researcher**: fast, keyboard-first, everything about the cube visible at once (cursor, plane, spectrum, flags), but high density and many panels; command palette and consistent shortcuts offset that. Provenance badges and the non-blocking warning strip add reading load but prevent silent assumptions. The dark surface suits long sessions but the figure sheet on the board is light, so the board is where colour judgement should be made.
**Supervisor**: lands on a read-only, pinned figure with an explicit "what you can / cannot see" box and anchored comments; rail, explorer, HPC paths and revision history are removed rather than disabled. The cost is that dark IDE chrome can feel heavy for a non-expert, and the owner's newer revision is intentionally invisible to them, so they may review a stale rev 7 (the banner states pinned rev and expiry).
