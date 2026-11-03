# Direction C — Contemporary Lab / Ledger (disposable prototype)

Not approved, not production. No frameworks, build step, CDN or network. Open the HTML files directly (file://). Every page carries an "ILLUSTRATIVE MOCK DATA" strip; imagery is the shared synthetic set, paths, hashes and numbers are invented.

## Files
`01-explorer.html` (ledger; `?view=runs` dense runs; `?state=empty`; `?saved=att`), `02-viewer.html` (`?state=loading|error|no-wcs|no-spectral`, `?row=c4`), `03-compare.html` (`?state=blocked`, `?b=r1&c=r2`), `04-board.html` (`?role=owner|reviewer`, `&state=denied`), `style.css`, `script.js`, `data.js` (fictional rows), `spectrum-data.js` (inline copy of shared spectrum.json and stats.json, because fetch fails on file://), `capture.py`, `verify.py`, `screenshots/` (32 PNG).
Add `?theme=dark|light` to any page. **No font files are bundled**; system stacks only.

## Concept
The ledger is the spine. Browser-style tabs (Datasets, Viewer, Compare, Board) are workspaces; a left ledger drawer (pinned at >=1360 px, overlay below) lists the same rows on every tab and drives master-detail: selecting a cube in the drawer loads it into the viewer (its WCS/spectral state decides which state appears), B/C on VGT runs choose the comparison pair, the board drawer shows figures (reviewer: only the 3 published previews). The viewer is a split pane (image over linked spectrum, property sheet at right).

## Tokens (hex)
| Token | Light | Dark |
|---|---|---|
| bg / surface / surface-2 / raised | #dde1e6 / #eceff2 / #f6f7f9 / #ffffff | #070d18 / #0e1626 / #131d31 / #182540 |
| ink / ink-2 / muted | #0a1626 / #26364a / #4a596c | #e8eef7 / #c5d0e0 / #9fb0c8 |
| line / line-strong | #c0c7d0 / #8d97a4 | #2a3a58 / #5c7094 |
| accent (teal, links, focus) | #0a6464 | #55d6c9 |
| lime fill (always with dark ink) / lime line | #c9f255 / #44600a | #b9e43c / #c6f24a |
| selected row | #e3f2b0 | #26361a |
| ok / warn / err / info (text on bg) | #17602c on #dff1e3; #7a4600 on #fbeacb; #9c1c16 on #fbe1df; #1d4f7a on #dcebf7 | #86e09b on #123221; #f2c76f on #3a2b0d; #ffa29c on #421716; #8fc4f3 on #11304d |

Type: UI `'IBM Plex Sans','Inter',system-ui,...` 13 px (12 px minimum for secondary); mono `'JetBrains Mono',ui-monospace,...` for paths, hashes, coordinates; `tabular-nums lining-nums` everywhere. 4 px grid, 28 px controls, 40 px ledger rows (36 compact).
Status is always glyph + word (✓ valid, ✕ missing/corrupt, ? unverified, ⊘ remote-OFFLINE with dashed border); provenance words carry shapes (● verified ◐ declared ◇ inferred ○ unresolved).

## Layout by breakpoint
- 1440x900: ledger drawer pinned (296 px) + split pane; viewer property sheet 304 px; explorer = ledger + 336 px detail.
- 1280x800: drawer collapses to an overlay (`[` or Ledger button, Esc closes); explorer hides Axes/Units columns (low priority); compare checklist cards go to 2 columns.
- 390x844: no page horizontal scroll. Ledger rows become stacked cards (name, type, provenance, WCS, source only); detail becomes a bottom sheet; viewer is full-width image + spectrum, all controls in a bottom sheet ("Controls & properties"); board defaults to the reviewer (read-only) view; panels stack.

## Keyboard
| Key | Action |
|---|---|
| `/` | focus ledger filter (Esc clears, Esc again returns to rows) |
| ↑ ↓ Home End PgUp PgDn | move between ledger rows (roving tabindex; selection follows focus) |
| Enter / Space | open row in its workspace / select |
| B, C | set baseline / candidate on a VGT run row |
| Enter on column header | sort (aria-sort cycles asc/desc) |
| `[` | toggle ledger drawer |
| ← → (Shift x10), Home End | previous/next channel, first/last |
| Alt+arrows (on focused image) | move cursor pixel (Shift x10) |
| R | toggle ROI tool (numeric ROI fields are the non-pointer alternative) |

## States shown
Explorer: normal, missing-WCS, unverified-WCS, remote-OFFLINE, corrupt, unresolved provenance, unknown (not 0) reliability, empty project, no-match, dense runs with mask/threshold/grid/config columns and a banner that different-mask runs are not equivalent. Viewer: default, loading (plane tiles 128/256, 4 MiB of 5.1 GiB, Cancel), error (partial range failed, cached preview stamped), no-wcs (no RA/Dec, no scale bar, pixel scale "unknown"), no-spectral (channel index only, velocity unavailable). Compare: default (WCS pass, beam unverified, mask policy shown), blocked (grid mismatch, difference and statistics withheld, "Resample with stated policy"). Board: owner (newer revision available, pinned rev 7), reviewer (scope sentence, anchored comments, request-comparison, expiry), raw-link access denied with next step. No credentials or HPC paths appear in reviewer views.

## Verified (actually run)
- `capture.py`: 32 screenshots via Playwright/Chromium: 4 pages x (1440 light, 1440 dark, 1280 light, 390 light), 4 viewer states, compare blocked (1440, 390), board reviewer (1440, 390) and denied, runs (light, dark, 390), empty, needs-attention, overlay drawer, mobile sheet. I looked at the screenshots and fixed overflow at 390, truncated chips, glyph/label issues, cramped checklist.
- Console errors/warnings: 0 across all captures. Horizontal overflow at any capture: 0 (after fix).
- `verify.py`: 25/25 keyboard/semantic checks (/ filter, row arrows, roving tabindex, Home/End, aria-sort, B, Enter opens, channel keys incl. Shift/Home/End, Alt+arrow cursor, R, focus ring 2 px solid, drawer-driven no-WCS switch, th scope, alt on all images, no @keyframes) and 32 contrast pairs (light and dark) all >= 4.5:1 for text, >= 3:1 for the lime UI line. Lowest text ratio: 5.44 (muted on bg, light).

## Not verified
No axe scan, no screen reader pass, no real touch device, no Firefox/WebKit, no forced-colors or 200 % zoom test, no print. Reduced motion is honoured by construction (no animations; transitions reset) but not visually tested. Tab order was printed for the viewer but not audited on other pages. Contrast was computed for token pairs, not every rendered pixel (images excluded).

## Known defects / honest limits
- Display controls (stretch, contrast, colormap) are CSS filter approximations on pre-rendered PNGs, labelled as such; cividis cannot be offered. Only 5 channel previews exist: the stepper shows the nearest, stated on screen.
- Cursor value and mask readout are generated (smooth function plus noise), flagged "mock"; the spectrum is the shared series and is not recomputed per ROI.
- Shared axial legend is a linear ramp 0-180; "0°≡180°" is carried by text, not by a visibly cyclic ramp. Hatch for invalid pixels uses an SVG alpha mask of the shared mask PNG (needs a browser with `mask-type: alpha`).
- Compare imagery does not change with the selected runs (one synthetic pair); only names, grid, mask, hashes and checklist change. Blocked state shows the same image shrunk to suggest a different grid.
- Tabs are links between pages, not true ARIA tabs; no close buttons. Drawer B/C buttons have tabindex -1 (keyboard uses the B/C keys).
- ◐ glyph renders small in some system fonts; the word beside it carries the meaning.
- Board comment form for reviewers is a stub; no persistence; "Show anchor" focuses the pin.
- Mobile bottom sheets do not trap focus.

## Cognitive trade-offs
Researcher: one always-present ledger gives fast row-level triage (provenance, WCS, source and mask visible without opening anything) and keyboard speed, at the cost of density and a learning curve (B/C, `/`, `[`); the viewer's property sheet and warnings stay beside the image so they are not hidden, but the ledger drawer competes for width and collapses on laptops. Supervisor: the reviewer view removes the ledger's raw rows, leads with a plain "you can see / cannot see" sentence, and shows numbered comment anchors on pinned panels; the mobile default is read-only. The cost is that a supervisor never sees the ledger's richness, so questions about masks or provenance rely on captions and comments rather than self-service inspection.
