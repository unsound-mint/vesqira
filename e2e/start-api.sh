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
# Stand-ins for Slurm (sbatch, squeue, sacct, scancel) keeping one state file per job.
bin="$run/slurm-bin"
mkdir -p "$bin" "$run/slurm-state" "$run/remote_data/configs"
cat > "$run/remote_data/configs/vgt.toml" <<'CFG'
[input]
input_file = "/shared/cubes/hnv1.fits"
output_dir = ""

[noise]
noise = 0.17
noise_mode = "scalar"

[vgt]
dn = 20
ker = 4
min_valid = 100
CFG
st="$run/slurm-state"
printf '#!/bin/sh\n[ "$1" = "--parsable" ] || exit 2\nfor f; do :; done\n[ -f "$f" ] || exit 3\nn=$(( $(cat %s/counter 2>/dev/null || echo 4241) + 1 )); echo $n > %s/counter\necho PENDING > %s/$n\necho "fake slurm log for job $n" > "$PWD/$(sed -n "s/^#SBATCH --job-name=//p" "$f")-$n.out"\necho $n\n' "$st" "$st" "$st" > "$bin/sbatch"
printf '#!/bin/sh\nid=$3; s=$(cat %s/$id 2>/dev/null); case "$s" in PENDING|RUNNING) echo $s;; esac\n' "$st" > "$bin/squeue"
printf '#!/bin/sh\nid=$5; s=$(cat %s/$id 2>/dev/null); case "$s" in COMPLETED|FAILED|CANCELLED) echo "$s|0:0|00:00:09|2026-11-04T10:00:00|2026-11-04T10:00:09";; esac\n' "$st" > "$bin/sacct"
printf '#!/bin/sh\necho CANCELLED > %s/$1\n' "$st" > "$bin/scancel"
chmod +x "$bin"/*
cat > "$run/fake-ssh" <<FAKE
#!/bin/sh
while [ \$# -gt 0 ]; do case "\$1" in -o) shift 2;; -T) shift;; *) break;; esac; done
shift
cmd=\$(printf '%s' "\$*" | sed -e 's#\$HOME#$run/remote-home#g' -e 's#bash -lc#bash -c#')
PATH=$bin:\$PATH exec sh -c "\$cmd"
FAKE
chmod +x "$run/fake-ssh"
cd "$root"
exec "$run/api" --ssh "$run/fake-ssh" --addr 127.0.0.1:8787 --db "$run/t.db" --data-dir "$run" --root "$run/data" \
  --review-addr 127.0.0.1:8788 --web-dist "$root/apps/web/dist"
