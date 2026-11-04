#!/usr/bin/env bash
# Starts Vesqira on YOUR data: the API (loopback only) and the web app.
#   tools/start.sh --root /path/to/your/fits/folder [--root /another/folder ...]
# Files outside the given roots are never read. Open http://127.0.0.1:5173 afterwards.
# Needs python3 with astropy (pip install -r fixtures/requirements.txt), Go, and `npm ci` once in apps/web.
set -euo pipefail
root="$(cd "$(dirname "$0")/.." && pwd)"
[ "$#" -gt 0 ] || { echo "usage: tools/start.sh --root /path/to/fits/folder [--root ...] [--review-addr 127.0.0.1:8788]" >&2; exit 2; }
data="${VESQIRA_DATA:-$HOME/.local/share/vesqira}"
mkdir -p "$data"
[ -d "$root/apps/web/node_modules" ] || (cd "$root/apps/web" && npm ci)
(cd "$root/services/api" && go build -buildvcs=false -o "$data/api" .)
(cd "$root/apps/web" && npm run build >/dev/null)
cleanup() { kill "${api_pid:-0}" "${web_pid:-0}" 2>/dev/null || true; }
trap cleanup EXIT INT TERM
cd "$root"
"$data/api" --addr 127.0.0.1:8787 --db "$data/vesqira.db" --data-dir "$data" --web-dist "$root/apps/web/dist" "$@" &
api_pid=$!
(cd apps/web && npx vite --port 5173 --strictPort --host 127.0.0.1) &
web_pid=$!
echo
echo "Vesqira is starting. Open http://127.0.0.1:5173"
echo "  data and catalog: $data   (set VESQIRA_DATA to change)"
echo "Press Ctrl-C to stop."
wait
