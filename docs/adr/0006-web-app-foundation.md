# ADR 0006: Web app foundation

- Status: proposed (accepted on merge by the owner)
- Date: 2026-11-04

## Context
The workbench design and tokens are approved (ADR 0005) and the CSS stack is decided (ADR 0004). The first code needs a build tool, component conventions and guards that keep the app on the token set.

## Decision
- `apps/web` is a Vite + React + TypeScript app. `design/tokens.css` is imported by the app and is the only token source; Vite is allowed to read it from outside the app root.
- Components use native elements (checkbox, select, range, button) restyled with CSS Modules, so forms, labels and screen readers keep working without a headless UI library. Custom widgets follow WAI-ARIA patterns (disclosure for sections, window splitter for resize handles).
- Fonts: self-hosted Geist and Geist Mono via Fontsource (OFL-1.1). Icons: Lucide through `lucide-react` (ISC), 16px in rows, 20px in the rail, 1.5px stroke. Licence texts are in `THIRD_PARTY_NOTICES.md`.
- stylelint (`stylelint-declaration-strict-value`) rejects colours, font sizes, radii, spacing, durations and z-index that are not `var(--…)` tokens. `design/tokens.css` is exempt. Reduced motion is handled by zeroing the motion tokens.
- Tests: Vitest and Testing Library cover keyboard and ARIA behaviour of custom widgets. Visual checks use real-browser screenshots.
- CI runs `make docs` always and `make web` only when web or design files change, with npm caching.

## Alternatives
Tailwind or CSS-in-JS (rejected in ADR 0004); Radix/Headless UI (extra dependency for widgets that native elements already cover); Next.js (no server rendering need).

## Consequences
Every visual value has to exist as a token first. Dependencies are limited to React, lucide-react and the two font packages at runtime. `stylelint` is pinned to 17 until the strict-value plugin supports 18.

## Validation
`make check` runs docs checks, typecheck, stylelint, tests and a production build. The guard was exercised with a deliberate hex colour, pixel padding and radius, which it rejected.

## Rollback / Migration
Components are small and isolated; swapping the build tool touches only `apps/web` config.
