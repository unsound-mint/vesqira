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
printf '#!/bin/sh\nfor id in $(echo "$3" | tr , " "); do s=$(cat %s/$id 2>/dev/null); case "$s" in PENDING|RUNNING) echo "$id|$s";; esac; done\n' "$st" > "$bin/squeue"
printf '#!/bin/sh\nfor id in $(echo "$4" | tr , " "); do s=$(cat %s/$id 2>/dev/null); case "$s" in COMPLETED|FAILED|CANCELLED) echo "$id|$s|0:0|00:00:09|2026-11-04T10:00:00|2026-11-04T10:00:09|00:30:00|4G|billing=2,cpu=2,mem=4G,node=1|2|00:00:12||1"; echo "$id.batch|$s|0:0|00:00:09|2026-11-04T10:00:00|2026-11-04T10:00:09||||2|00:00:12|1048576K|1";; esac; done\n' "$st" > "$bin/sacct"
printf '#!/bin/sh\necho CANCELLED > %s/$1\n' "$st" > "$bin/scancel"
chmod +x "$bin"/*
cat > "$run/fake-ssh" <<FAKE
#!/bin/sh
while [ \$# -gt 0 ]; do case "\$1" in -o) shift 2;; -T) shift;; *) break;; esac; done
shift
cmd=\$(printf '%s' "\$*" | sed -e 's#\$HOME#$run/remote-home#g' -e 's#bash -lc#bash -c#')
PATH=$bin:$(dirname "$(cat "$run/python")"):\$PATH exec sh -c "\$cmd"
FAKE
chmod +x "$run/fake-ssh"
# A project folder for tests/z-projects.spec.ts: outside --root, so only opening it as a project makes it readable. It has
# a data layout, two run records, one config, and a git repository (a HEAD and a branch ref are all discovery reads).
proj="$run/project/hi-vgt-study"
mkdir -p "$proj/data/raw" "$proj/configs" "$proj/.git/refs/heads"
for f in hi_cube.fits cube_int16_scaled.fits hi_cube_no_spectral_ref.fits; do cp "$run/data/$f" "$proj/data/raw/$f"; done
# two scalar cubes with their BUNIT card removed, which gives the Needs page something to ask (orientation maps are never asked
# for a brightness unit, so the VGT maps would not do)
for n in hi_cube_nounit.fits hi_cube_nounit_b.fits; do
  python3 -I -c "import sys
from astropy.io import fits
with fits.open(sys.argv[1]) as h:
    del h[0].header['BUNIT']
    h.writeto(sys.argv[2])" "$run/data/hi_cube.fits" "$proj/data/raw/$n"
done
echo 'ref: refs/heads/main' > "$proj/.git/HEAD"
echo 0123456789abcdef0123456789abcdef01234567 > "$proj/.git/refs/heads/main"
echo 'lambda = 0.5' > "$proj/configs/vgt.toml"
for r in 2026-08-01T100000Z_abc1234:0.5 2026-08-02T110000Z_abc1234:0.9; do
  d="$proj/results/${r%%:*}"
  mkdir -p "$d"
  printf 'generated_at_utc = "2026-08-01T10:00:00Z"\ngitcommit = "abc1234"\n[config]\nlambda = %s\nwidth = 3\n[input]\npath = "%s/data/raw/hi_cube.fits"\nsize_bytes = 100\n[outputs]\npsi_path = "%s/psi.fits"\n' "${r##*:}" "$proj" "$d" > "$d/run_metadata.toml"
done
cd "$root"
exec "$run/api" --ssh "$run/fake-ssh" --addr 127.0.0.1:${VESQIRA_API_PORT:?} --db "$run/t.db" --data-dir "$run" --root "$run/data" \
  --review-addr 127.0.0.1:${VESQIRA_REVIEW_PORT:?} --web-dist "$root/apps/web/dist" --ssh-config "" --enable-terminal --enable-vpn
