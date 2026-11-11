# `make setup` creates .venv; recipes use it when present, so python3 below has numpy and astropy without activating anything
export PATH := $(CURDIR)/.venv/bin:$(PATH)

.PHONY: setup check docs web fixtures api e2e e2e-isolateddemo bench viewer-bench desktop
# Check tools, create .venv, install web, e2e and Playwright dependencies (tools/setup.sh); safe to repeat
setup:
	tools/setup.sh

check: docs fixtures api web

docs:
	python3 tools/check_docs.py
	@if [ -d tests ]; then python3 -m unittest discover -s tests; fi

# Web checks run when the app exists; install once with: cd apps/web && npm ci
web:
	@if [ -f apps/web/package.json ]; then \
	  if [ ! -d apps/web/node_modules ]; then echo "apps/web: run 'npm ci' in apps/web first"; exit 1; fi; \
	  cd apps/web && npm run check:api && npm run typecheck && npm run lint:css && npm test && npm run build; \
	fi

# Needs numpy and astropy: make setup (or pip install -r services/scientific-worker/requirements.txt)
fixtures:
	cd fixtures && python3 -m unittest discover -s .

# Go control plane and the Python header worker. Needs Go and the fixtures requirements.
api:
	@if [ -f services/api/go.mod ]; then \
	  cd services/scientific-worker && python3 -m unittest discover -s . && cd ../api && gofmt -l . | (! grep .) && go vet ./... && VESQIRA_REQUIRE_WORKER=1 go test ./...; \
	fi

# Acceptance walk-through in a real browser. Needs python3 with astropy, Go, and: cd apps/web && npm ci; cd e2e && npm ci && npx playwright install chromium
e2e:
	cd apps/web && npm run build
	cd e2e && npx playwright test

# e2e on a free block of ports (e2e/ports.ts), so worktrees can run it at the same time; make e2e keeps the fixed ports CI and docs use
e2e-isolated:
	VESQIRA_E2E_PORT_BASE=$$(python3 tools/free-port-base.py) $(MAKE) e2e

# Run the app on generated fixtures and follow docs/demo.md
demo:
	tools/demo.sh

# Worker timings on a large sparse cube against tools/bench_budgets.json (page-cache speed, not a disk)
bench:
	python3 tools/bench.py --check

# Build and open the desktop window from this tree, no RPM (see docs/desktop-dev.md)
desktop:
	tools/dev-desktop.sh run

# Viewer timings in a real Chromium (key to pixels, main-thread blocking, CPU renderer) against tools/viewer_perf_budgets.json
viewer-bench:
	node tools/viewer_perf.mjs --check
