# Contributing to Vesqira

Vesqira is in **early development**: the v0.1 astronomy workflow exists end to end on synthetic fixtures (see `README.md`), and nothing is validated against a published benchmark yet. The most useful contributions today are scientific review, design critique, and documentation fixes.

## Before you start

1. Read `AGENTS.md` (scientific invariants and scope).
2. v0.1 is **astronomy only**.
3. Open an issue before large changes. Scope expansion needs an ADR (`docs/adr/0000-template.md`).

## Scientific correctness comes first

Do not invent calibration, units, or coordinate frames. Treat unknown as `unknown`, never `0` or `1`. Axial orientations (0° ≡ 180°) need axial statistics. Missing is never zero. The ten invariants are in `AGENTS.md`; `docs/what-vesqira-will-never-do.md` explains them for scientists and names the test that guards each. A change that touches one needs a test with an independently computed expected value.

## UI work

The approved direction is recorded in ADR 0005 and `docs/design/design-directions.md`; build on it rather than proposing new layouts without discussion. Prototypes under `docs/design/prototypes/` use synthetic data and are disposable.

## First 10 minutes

```sh
git clone <this repository> && cd vesqira
make setup     # checks go, node, python3; creates .venv; npm ci for web and e2e; installs Chromium for Playwright
make check     # docs checks, fixtures, worker, Go API, typecheck, stylelint (token guard), web tests, build
make demo      # the app on generated synthetic data: http://127.0.0.1:5173
```

Then follow the walk-through in `docs/demo.md` (the same steps run as `make e2e`). `make setup` is safe to repeat. It needs Go, Node 24 and Python 3.11 or newer, and says exactly which one is missing. Make targets use `.venv` automatically; for a direct `python3` call run `source .venv/bin/activate`. `make setup` on Linux may not install Chromium's system libraries: if `make e2e` cannot start the browser, run `cd e2e && npx playwright install --with-deps chromium`.

A dev container (`.devcontainer/devcontainer.json`) runs the same setup. **It is untested**: it was written without Docker available, so expect to fix it on first use and send the fix.

## What is where

| Path | What it is |
|---|---|
| `apps/web` | React and TypeScript app: viewer, catalog, boards, review surface. `npm run dev` serves the components page |
| `services/api` | Go control plane: catalog, projects, hosts and jobs, provenance, review server, audit. SQLite |
| `services/scientific-worker` | Python worker (astropy) that reads FITS headers, planes, spectra and orientation statistics |
| `fixtures` | Generated synthetic FITS data and the tests that pin its scientific meaning (`generate.py`) |
| `e2e` | Playwright browser walk-through of `docs/demo.md` |
| `design`, `docs/design` | Design tokens and the approved design direction |
| `docs` | Architecture decisions (`docs/adr`), product shape, roadmap, demo, performance |
| `packaging`, `tools` | RPM build, demo and setup scripts, benchmarks, docs and contract checkers |

## Running one test

```sh
cd services/api && go test ./internal/compat -run TestEveryDifferenceIsReported   # one Go test
cd services/api && VESQIRA_REQUIRE_WORKER=1 go test ./...                         # Go tests that call the real worker
cd services/scientific-worker && python3 -m unittest test_plane_worker.PlaneWorker   # one Python class; add .test_name for one test
cd fixtures && python3 -m unittest test_fixtures
cd apps/web && npx vitest run src/viewer/viewer.test.tsx                          # one web test file; -t "name" for one test
cd e2e && npx playwright test -g "register, read"                                 # needs make setup and a built web app (make e2e builds it)
```

Run the viewer benchmark with `make viewer-bench` (headless Chromium on a 2048 x 2048 x 24 cube, checked against `tools/viewer_perf_budgets.json`). It needs ports 8787 and 5173 free, so stop `make demo` first. What it measures and its limits: `docs/performance.md`. The worker benchmark is `make bench`.

CI runs `make docs` always, and the web, fixtures and API jobs when those parts change; `.github/workflows/e2e.yml` runs the browser walk-through on pull requests. All visual values come from `design/tokens.css`; stylelint rejects anything else. Run the API against your own data folder: `cd services/api && go run . --root /path/to/data` (loopback only; files outside `--root` are never read).

## Decisions: write an ADR

A new technology, storage mechanism, external service, security boundary or scientific semantics contract gets a short ADR in `docs/adr/`, copied from `docs/adr/0000-template.md` (Context, Decision, Alternatives, Consequences, Validation, Rollback) and numbered after the highest existing one. Write it in the same pull request as the change, or before it when you want feedback on the direction first.

## Data and privacy

Never commit raw scientific data, credentials, `.env` files or private documents (`.gitignore` blocks the common cases).

## License

By contributing you agree your contribution is licensed under **AGPL-3.0-only** (see `LICENSE`). Copyright (C) 2026 Denis Andreyev.

## Commits and pull requests

Small, focused changes with a clear description of what changed and why. Call out any scientific assumption you touched.
