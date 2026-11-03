# ADR 0004: CSS stack

- Status: accepted
- Date: 2026-11-04

## Context
The web app is a dense, two-theme scientific interface with a performance-sensitive viewer. The styling approach is costly to replace later.

## Decision
- Design tokens as CSS custom properties (`design/tokens.css`) are the single source of truth for color, type, spacing, radius, motion and both themes.
- Component styles use CSS Modules, with modern CSS (`@layer`, container queries, `:has()`, `color-mix()`, logical properties).
- No Tailwind and no CSS-in-JS runtime.
- Stylelint forbids raw colors, font sizes and spacing values outside the tokens.
- Motion: tokenized durations and easings, CSS transitions on `transform` and `opacity` only, a global `prefers-reduced-motion` rule, and no animation in the scrubbing path (plane changes, ROI drag, cursor readout). No motion library.

## Alternatives
Tailwind (spreads the design across markup; harder to review against the design system); CSS-in-JS (runtime cost in the viewer).

## Consequences
The token file is experimental until a design direction is approved. Stylelint is added with the first component code.

## Validation
Stylelint in `make check` once the app exists; rendered screenshots in both themes.

## Rollback / Migration
Supersede this ADR; tokens stay valid under any approach.
