#!/usr/bin/env bash
# Fast loop for the desktop app: no RPM, no dnf. Builds the API, web app and window from this tree into .local/dev and runs
# them with their own data, config and port, so it can run next to the installed Vesqira. Your real HOME and ssh config are used.
#   tools/dev-desktop.sh run              build, then open the window on your screen (Ctrl-C or close the window to stop)
#   tools/dev-desktop.sh up               build, then run the window on a private virtual screen (no window appears on yours)
#   tools/dev-desktop.sh shot FILE        screenshot of that virtual screen
#   tools/dev-desktop.sh click X Y | key KEYS | type TEXT    drive it (a small X11 helper, tools/xinput.py)
#   tools/dev-desktop.sh open DIR         make DIR a project folder in the dev instance (what the folder chooser would do)
#   tools/dev-desktop.sh down | build | logs
# Rebuilds are incremental: Go, vite and cargo only redo what changed.
set -euo pipefail
root="$(cd "$(dirname "$0")/.." && pwd)"
dev="$root/.local/dev"; prefix="$dev/prefix"
disp="${VESQIRA_DEV_DISPLAY:-:99}"; size="${VESQIRA_DEV_SIZE:-1440x900}"
export VESQIRA_PORT="${VESQIRA_DEV_PORT:-8798}" VESQIRA_PREFIX="$prefix"
export XDG_CONFIG_HOME="$dev/cfg" XDG_DATA_HOME="$dev/data" XDG_STATE_HOME="$dev/state"
launcher="$prefix/bin/vesqira"

build() {
  mkdir -p "$prefix/libexec/vesqira" "$prefix/share/vesqira" "$prefix/bin" "$dev/cfg/vesqira"
  (cd "$root/services/api" && go build -buildvcs=false -o "$prefix/libexec/vesqira/vesqira-api" .)
  [ -d "$root/apps/web/node_modules" ] || (cd "$root/apps/web" && npm ci)
  (cd "$root/apps/web" && npm run build >/dev/null)
  (cd "$root/apps/desktop" && cargo build --quiet)
  ln -sf "$root/apps/desktop/target/debug/vesqira-desktop" "$prefix/libexec/vesqira/vesqira-desktop"
  ln -sfn "$root/apps/web/dist" "$prefix/share/vesqira/web"
  ln -sfn "$root/services/scientific-worker" "$prefix/share/vesqira/worker"
  ln -sf "$root/packaging/vesqira" "$launcher"
  # Folders the dev instance may read from the start: the generated fixtures when they exist.
  [ -f "$dev/cfg/vesqira/roots" ] || { mkdir -p "$dev/Vesqira"; echo "$dev/Vesqira" > "$dev/cfg/vesqira/roots"; }
}
xin() { DISPLAY="$disp" python3 -I "$root/tools/xinput.py" "$@"; }

case "${1:-run}" in
  build) build ;;
  run) build; exec "$launcher" ;;
  up)
    "$0" down >/dev/null 2>&1 || true
    build
    Xvfb "$disp" -screen 0 "${size}x24" -nolisten tcp >"$dev/xvfb.log" 2>&1 & echo $! > "$dev/xvfb.pid"
    sleep 1
    # A private X screen has no compositor or GPU; WebKit needs its software paths there.
    DISPLAY="$disp" WAYLAND_DISPLAY= GDK_BACKEND=x11 WEBKIT_DISABLE_COMPOSITING_MODE=1 WEBKIT_DISABLE_DMABUF_RENDERER=1 \
      setsid "$launcher" >"$dev/window.log" 2>&1 < /dev/null & echo $! > "$dev/window.pid"
    for _ in $(seq 1 60); do curl -fsS -o /dev/null --max-time 1 "http://127.0.0.1:$VESQIRA_PORT/api/roots" 2>/dev/null && break; sleep 0.5; done
    sleep 2; echo "up on $disp (window log: $dev/window.log)" ;;
  shot) [ -n "${2:-}" ] || { echo "usage: dev-desktop.sh shot FILE.png" >&2; exit 2; }
    DISPLAY="$disp" import -window root "$2"; echo "$2" ;;
  click) xin click "$2" "$3" ;;
  key) shift; xin key "$@" ;;
  type) shift; xin type "$@" ;;
  open) curl -fsS -X POST -H 'Content-Type: application/json' -d "{\"path\":\"$(cd "$2" && pwd -P)\"}" "http://127.0.0.1:$VESQIRA_PORT/api/projects/open"; echo ;;
  logs) tail -n 60 "$dev/window.log" "$dev/state/vesqira/api.log" 2>/dev/null ;;
  down)
    for f in window xvfb; do [ -f "$dev/$f.pid" ] && kill "$(cat "$dev/$f.pid")" 2>/dev/null || true; rm -f "$dev/$f.pid"; done
    pkill -f "$prefix/libexec/vesqira/vesqira-desktop" 2>/dev/null || true
    [ -x "$launcher" ] && "$launcher" stop 2>/dev/null || true ;;
  *) sed -n 2,11p "$0" | sed 's/^# \{0,1\}//'; exit 2 ;;
esac
