#!/usr/bin/env bash
# Runs Vesqira on generated synthetic fixtures so you can follow docs/demo.md. Nothing here is real data.
# Needs: python3 with numpy and astropy (pip install -r fixtures/requirements.txt), Go, Node (cd apps/web && npm ci).
set -euo pipefail
root="$(cd "$(dirname "$0")/.." && pwd)"
demo="$root/.demo"
mkdir -p "$demo/data"
(cd "$root/fixtures" && python3 generate.py "$demo/data" >/dev/null)
(cd "$root/services/api" && go build -buildvcs=false -o "$demo/api" .)
(cd "$root/apps/web" && npm run build >/dev/null)
cleanup() { kill "${api_pid:-0}" "${web_pid:-0}" 2>/dev/null || true; }
trap cleanup EXIT INT TERM
cd "$root"
"$demo/api" --addr 127.0.0.1:8787 --db "$demo/demo.db" --data-dir "$demo" --root "$demo/data" \
  --review-addr 127.0.0.1:8788 --web-dist "$root/apps/web/dist" &
api_pid=$!
(cd apps/web && npx vite --port 5173 --strictPort --host 127.0.0.1) &
web_pid=$!
cat <<MSG

Vesqira demo is starting.
  App:               http://127.0.0.1:5173
  Reviewer surface:  http://127.0.0.1:8788  (only published previews; used by share links)
  Files to register: $demo/data/*.fits
  Walk-through:      docs/demo.md
Press Ctrl-C to stop.
MSG
wait
