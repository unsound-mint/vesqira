# Contributing to Vesqira

Vesqira is in **early development**: the web app foundation exists, the scientific viewer does not. The most useful contributions today are scientific review, design critique, and documentation fixes.

## Before you start

1. Read `AGENTS.md` (scientific invariants and scope).
2. v0.1 is **astronomy only**.
3. Open an issue before large changes. Scope expansion needs an ADR (`docs/adr/0000-template.md`).

## Scientific correctness comes first

Do not invent calibration, units, or coordinate frames. Treat unknown as `unknown`, never `0` or `1`. Axial orientations (0° ≡ 180°) need axial statistics. See the ten invariants in `AGENTS.md`.

## UI work

The approved direction is recorded in ADR 0005 and `docs/design/design-directions.md`; build on it rather than proposing new layouts without discussion. Prototypes under `docs/design/prototypes/` use synthetic data and are disposable.

## Checks

Requires Node 24 and Python 3.

```sh
cd apps/web && npm ci   # once
npm run dev             # components page at http://localhost:5173
make check              # docs checks, typecheck, stylelint (token guard), tests, build
```

CI runs `make docs` always and `make web` only when web or design files change. All visual values come from `design/tokens.css`; stylelint rejects anything else. More checks are added only when the code they cover exists.

## Data and privacy

Never commit raw scientific data, credentials, `.env` files or private documents (`.gitignore` blocks the common cases).

## License

By contributing you agree your contribution is licensed under **AGPL-3.0-only** (see `LICENSE`). Copyright (C) 2026 Denis Andreyev.

## Commits and pull requests

Small, focused changes with a clear description of what changed and why. Call out any scientific assumption you touched.
