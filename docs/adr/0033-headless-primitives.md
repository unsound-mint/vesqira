# ADR 0033: Headless accessibility primitives under CSS Modules

- Status: accepted (first slice implemented: the present-mode modal)
- Date: 2026-11-10

## Context
ADR 0004 fixes the styling stack: tokens, CSS Modules, Stylelint that forbids raw values, no Tailwind and no CSS-in-JS. It says nothing about interaction behavior. Keyboard handling, focus management, ARIA state and collision handling are where hand-rolled widgets fail, and where a scientific instrument used by keyboard-heavy people can least afford to.

What exists when this was written (checked in the code): the `ui/` layer already wraps **native** checkbox, select and range inputs, which are accessible by construction and stay. Hand-rolled behavior is limited to the path combobox (`PathField`), the disclosure (`Section`) and the present-mode dialog. The present-mode dialog declared `role="dialog" aria-modal="true"` but did not contain focus (Tab reached the page behind it) or give focus back on leaving; a test written first failed on exactly that. So the library is for widgets the browser does not provide (modal dialog, menu, popover, tooltip, tree, combobox, table), not a replacement for native controls. The product needs number fields (display limits, region bounds, units), range sliders, a tree (`FolderTree`), data tables (catalog, runs, jobs), comboboxes (colormap, dataset, host) and menus.

Component kits that bundle a look (shadcn/ui, MUI, Chakra) were considered because they speed up reaching a generic baseline. Our design direction (ADR 0005) is a distinctive instrument, not that baseline, and the kits that matter most are built on Tailwind, which ADR 0004 rejected for reasons that still hold: the token guard rail is a CSS lint, and the design review compares CSS Modules against `docs/design/design-system.md`.

## Decision
- Use **React Aria Components** for interaction behavior wherever the widget is on its list (Modal/Dialog, Popover, Menu, Tooltip, Tree, Table/GridList, Tabs, Toolbar). Keep native `select`, checkbox and range inputs where they already serve. It provides behavior and ARIA only.
- Style it **only through CSS Modules and the tokens** of ADR 0004, using the data attributes the components expose (`[data-selected]`, `[data-focus-visible]`, `[data-pressed]`, `[data-disabled]`, `[data-placement]`) and the `className` render prop. No primitive's built-in colors, no `style` color props, no second styling system. Stylelint keeps applying unchanged because the CSS stays in `.module.css`.
- **Adopt incrementally.** Use primitives for every new interactive widget, and replace a hand-rolled one when its file is next touched for another reason or when it has an open accessibility defect. No big-bang migration, and no screen changes in appearance because of this ADR (new screens still pass the design gate).
- **Wrap, do not scatter.** Primitives are imported only through `apps/web/src/ui/`, where each is wrapped once with the product's class names, density and labels. Feature code imports from `ui/`, never from `react-aria-components`. `boundaries.test.ts` enforces it.
- The scrubbing path stays free of primitives where they would add work: the channel slider and the viewer canvas keep their current event handling until measured (ADR 0004: no animation or re-render cost in scrubbing). Using a primitive there needs a measurement in the same change.
- **Not `NumberField` for scientific values.** Measured (React Aria Components 1.22.1, jsdom, typing as a person types): with a `step`, a typed `0.0003` is snapped to `0` (the step grid), and typing `5e-4` keystroke by keystroke yields `54`, because the input filter drops the `e`; the parser alone reads `5e-4` correctly, so the failure is only in the typing path. Both silently change a value. Display limits, region bounds and other scientific numbers keep a plain text input parsed with `Number()`, which handles exponents and rejects garbage. Revisit only with a test that types exponent notation and small values through the real component.

## Alternatives
- **Tailwind + shadcn/ui:** fastest to a conventional look; spreads the design across markup, loses the CSS-level token guard and the review model, and the default look is the generic one we are avoiding. The behavior half of shadcn is Radix, which we can use without the rest.
- **Radix Primitives:** simple and widely used, but its catalog lacks Tree, Table, NumberField and the date/number locale handling we need; its maintenance pace should be rechecked before depending on it.
- **Base UI:** small and unstyled, newer and with a smaller catalog; a reasonable later swap behind `ui/` if React Aria's size or API becomes a problem.
- **Keep hand-rolling:** no dependency, but every widget re-earns keyboard and screen-reader behavior, which the axe scan does not check.

## Consequences
One new runtime dependency family (`react-aria-components`, with its `@react-aria/*` and `@react-stately/*` packages) and its licence (Apache-2.0, to be added to `THIRD_PARTY_NOTICES.md`). Bundle size grows; the budget below limits it. Components import from `ui/`, so a future swap touches one directory. Mixed hand-rolled and primitive widgets coexist for a while, so keyboard conventions must be checked across the seam.

## Validation
- Bundle budget recorded before and after the first widget (gzip, per entry chunk); a primitive that costs more than the agreed budget is lazy-loaded or not adopted.
- For each adopted widget: a keyboard test (arrow, Home/End, Escape, type-ahead where relevant), an axe scan in light and dark, and a screen-reader label review; the existing Playwright walk-through still passes.
- Stylelint and `boundaries.test.ts` pass: no raw values, no direct imports outside `ui/`.
- First slice (done): the present-mode dialog uses `ui/Modal`. Tests: Tab stays inside, the page behind is hidden from assistive technology, Escape closes, focus returns to the opener (this test failed before the change). Not yet done: axe in both themes on the modal, 1280x800 and 390x844 screenshots.
- Bundle: `Modal` is not exported from the `ui/` barrel (every screen imports it) and the board view loads `PresentView` lazily, so React Aria is a separate 60.8 kB chunk (19.9 kB gzip) fetched when someone presents; the entry chunk stays 432 kB (133.6 kB gzip), as before. Importing it through the barrel cost +58 kB (+18.7 kB gzip) in the entry chunk.

## Rollback / Migration
A widget is reverted by replacing its wrapper in `ui/`; callers do not change. Removing the dependency means reimplementing the wrappers. Nothing in stored data, the API or the contracts depends on it.
