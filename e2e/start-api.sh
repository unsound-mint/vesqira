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
# A stand-in for the second laptop: a fake ssh that runs the remote command on this machine with $HOME redirected,
# so the walk-through covers hosts without a real second machine.
command -v python3 > "$run/python" # the remote command runs with a minimal PATH, so tests name the interpreter explicitly
mkdir -p "$run/remote_data" "$run/remote-home"
cp "$run/data/hi_cube.fits" "$run/remote_data/remote_cube.fits"
cat > "$run/fake-ssh" <<FAKE
#!/bin/sh
while [ \$# -gt 0 ]; do case "\$1" in -o) shift 2;; -T) shift;; *) break;; esac; done
shift
cmd=\$(printf '%s' "\$*" | sed 's#\$HOME#$run/remote-home#g')
exec sh -c "\$cmd"
FAKE
chmod +x "$run/fake-ssh"
cd "$root"
exec "$run/api" --ssh "$run/fake-ssh" --addr 127.0.0.1:8787 --db "$run/t.db" --data-dir "$run" --root "$run/data" \
  --review-addr 127.0.0.1:8788 --web-dist "$root/apps/web/dist"
