#!/usr/bin/env bash
# Starts the API for the acceptance tests on freshly generated fixtures. Needs python3 with astropy, Go, and a built
# apps/web (the reviewer listener serves apps/web/dist).
set -euo pipefail
root="$(cd "$(dirname "$0")/.." && pwd)"
run="$root/e2e/.run"
rm -rf "$run"
mkdir -p "$run/data"
(cd "$root/fixtures" && python3 generate.py "$run/data" >/dev/null)
(cd "$root/services/api" && go build -buildvcs=false -o "$run/api" .)
cd "$root"
exec "$run/api" --addr 127.0.0.1:8787 --db "$run/t.db" --data-dir "$run" --root "$run/data" \
  --review-addr 127.0.0.1:8788 --web-dist "$root/apps/web/dist"
