#!/usr/bin/env bash
# Starts a publish server for the reports walk-through (ADR 0030) on 127.0.0.1:8790 with the built web app. The
# publisher key is written to .run/publish-server.key for the test to paste into the app, as a person would.
set -euo pipefail
root="$(cd "$(dirname "$0")/.." && pwd)"
run="$root/e2e/.run-publish"
rm -rf "$run"
mkdir -p "$run"
(cd "$root/services/api" && go build -buildvcs=false -o "$run/api" .)
"$run/api" publish-server add-publisher --data-dir "$run/data" --name e2e | grep -oE '[A-Za-z0-9_-]{40,}' | head -1 > "$run/key"
exec "$run/api" publish-server --addr 127.0.0.1:8790 --data-dir "$run/data" --web-dist "$root/apps/web/dist"
