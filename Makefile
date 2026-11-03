.PHONY: check docs web
check: docs web

docs:
	python3 tools/check_docs.py
	@if [ -d tests ]; then python3 -m unittest discover -s tests; fi

# Web checks run when the app exists; install once with: cd apps/web && npm ci
web:
	@if [ -f apps/web/package.json ]; then \
	  if [ ! -d apps/web/node_modules ]; then echo "apps/web: run 'npm ci' in apps/web first"; exit 1; fi; \
	  cd apps/web && npm run typecheck && npm run lint:css && npm test && npm run build; \
	fi
