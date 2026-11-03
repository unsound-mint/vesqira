# ADR 0005: Workbench layout and design tokens

- Status: accepted (approved by the owner, 2026-11-04)
- Date: 2026-11-04

## Context
Three exploratory directions were reviewed for design, usability and scientific honesty. The owner preferred the dark workbench layout and wants a productivity tool with the density and polish of VS Code, Notion, Photoshop and Premiere Pro, not a playful or marketing look.

## Decision
- **Layout:** icon rail, explorer sidebar, a central square-friendly canvas, a bottom channel timeline, a status bar, and a right inspector whose sections stack vertically as collapsible sections (no tabs). Spectrum and data table live in the inspector's Region section. Warnings are an on-demand note, never a permanent banner over the canvas.
- **Tokens** (`design/tokens.css`): 4px grid. Text 12/13/14/16/20px with 14px as default; nothing below 12px. Controls 28/32/40px. Row 28px, pill 22px, timeline strip 44px. Icons 16px in rows and 20px in the rail, 1.5px stroke. Radii 4/6/8px. Geist (UI) and Geist Mono (coordinates, values, paths). Dark-first cool slate in four layered surfaces plus an always-dark image canvas, one cyan accent, separate success/warning/error colours. A full light theme.
- **Detail fixes that the approval depended on:** minimum 28px controls and 12px text, real icons instead of glyphs, custom checkbox/switch/slider/select, outward 2px focus ring, computed values never shown stale (region size, table near the current channel), unknown shown as an em dash.
- **Progressive disclosure** comes from collapsible sections with sensible defaults, not a global detail mode.

## Alternatives
Material-style sizing (too sparse for data work); a global Basic/Standard/Expert mode (adds a mode switch pro tools avoid); tabbed inspector (hides sections).

## Consequences
The approved reference is `docs/design/prototypes/workbench/` (synthetic data; several controls are cosmetic). Open component work: resizable panels, zoom, collapsible timeline, a bundled open-licensed icon set (licence checked before bundling) and self-hosted Geist fonts (licence checked before bundling).

## Validation
Contrast of every declared text pair is computed on the artifact's token sheet (all pass WCAG 2.2 AA in both themes at the time of approval). A stylelint rule will reject values outside the token set once the app exists.

## Rollback / Migration
Supersede this ADR; tokens are one file.
