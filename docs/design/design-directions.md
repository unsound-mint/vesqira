# Design directions (v0.1, astronomy)

**Status:** direction approved by the owner on 2026-11-04: the Workbench (prototype B's layout on the tokens in `design/tokens.css`), recorded in [ADR 0005](../adr/0005-workbench-layout-and-tokens.md). The three exploration prototypes and the review below are kept as history; the approved reference is [`prototypes/workbench/`](prototypes/workbench/).

All imagery and numbers are **synthetic** (`prototypes/_shared/make_mock_assets.py`, fixed seed). Nothing is a scientific result. Prototypes are plain HTML/CSS/JS opened from disk; they are not the production app and their controls are partly cosmetic (see each `NOTES.md`).

## The three directions

| | A: Precision Editorial | B: Dark Observatory | C: Contemporary Lab / Ledger |
|---|---|---|---|
| Structure | Top bar, document page, image as a framed "plate" with margin notes | Icon rail, dockable panels, channel timeline, status bar, command palette | Open tabs, persistent sortable ledger, master-detail split panes |
| Theme | Light-first paper, ink, teal; dark variant | Dark-first charcoal, copper and cyan; light variant | Mist grey, blue-black, lime/teal; dark variant |
| Density | Generous, dense where needed | High, IDE-like | High, table-centred |
| Prototype | [`prototypes/dir-a/`](prototypes/dir-a/NOTES.md) | [`prototypes/dir-b/`](prototypes/dir-b/NOTES.md) | [`prototypes/dir-c/`](prototypes/dir-c/NOTES.md) |

Each covers four screens (dataset explorer, spectral viewer, VGT comparison, board with supervisor review) plus loading, error, missing-WCS, missing-spectral-reference, blocked-comparison and access-denied states. Screenshots (WebP) are in each `screenshots/` folder at 1440×900, 1280×800 and 390×844.

## What was verified, and what was not

- **Done:** screenshots in a real Chromium for every page and state; zero console errors and no horizontal scroll at 390px (per capture scripts); scripted keyboard checks; computed text contrast, with no pair under 4.5:1 in the pairs each script covered. I viewed the 1440×900 viewer screen of each direction myself.
- **Independent reviews** (design, usability and scientific-honesty reviewers; screenshot and code based, not user testing).
- **Not done:** axe, screen reader, real touch devices, Firefox/Safari, 200% zoom, forced-colors, reduced-motion visual check, any real FITS data, any test with real users.

## Review findings

**Design review.** A fits "instrument-grade editorial" best in tone and typography and has the strongest reviewer board; its viewer plate is small and the page long (P1). B has the most instrument-like viewer and best mobile viewer, but the image is only about 485px wide with HUDs overlapping it (P0), and its compare and board screens waste space (P1). C has the best explorer and compare workflow, but is the most generic-admin looking and its board is form-heavy (P1).

**Usability.** Best for the researcher: C (selecting a ledger row opens the cube; image and spectrum both visible), then B (best keyboard discoverability). Best for the supervisor: A (reads like a journal figure with a plain scope statement). Issues: C shows a stale value ("0.18 K, mask valid") while a plane is loading (P0); C uses Alt+arrows, which collide with browser navigation; A's reviewer board shows a mixed-revision figure with no stated action (P1); B's icon-only rail is poor for first use; A's reviewer board is about 3000px tall on mobile.

**Scientific honesty.** All three pass the core checks: mock markers on every page, no sky coordinates or angular scale bar without WCS, comparison withheld with a reason when grids differ, a wrapped axial legend with "0°≡180°" and doubled-angle statistics, and non-colour mask cues in compare. Must fix before any direction is approved:

1. Gate every paired or numeric readout on comparability, including status bars and hover (B shows paired angles while comparison is blocked).
2. Replace the unqualified "WCS valid" with wording tied to what was actually validated.
3. Never render unknown as 0 (B's `|| 0` fallbacks).
4. Caveat statistics when checklist items are unverified; flag mixed-revision panels (A, B).
5. Fix B's 30° versus 90° difference-range inconsistency and add a legend to its panel C.
6. Keep raw and HPC paths out of reviewer markup and data. Production gating must be server-side, not CSS or JS.
7. Add a non-colour mask cue to C's reviewer panel and fix its blocked-state tile (invented geometry).
8. Before production: golden fixtures for the 1°/179° axial mean, degenerate resultant, NaN versus 0, incompatible WCS and missing spectral reference.

## Earlier recommendation (superseded by ADR 0005)

**A hybrid, with A as the base:** A's visual language (paper surface, serif figure titles, framed plate, caption-led board); B's viewer mechanics (channel timeline showing flagged gaps, status bar, collapsible panels, mobile drawers); C's explorer for the Datasets screen (sortable columns, baseline/candidate selection, side-by-side provenance). The viewer image should reach at least 700px square at 1280×800 by collapsing the margin notes into one column. If you prefer a single unmixed direction: A for a review-first product, C for a data-management-first product.

## Next

The foundation is built first (tokens, fonts, icons, base components), then the viewer slice: FITS cube registration, one-plane read, a WCS-correct readout tested against an independent implementation.
