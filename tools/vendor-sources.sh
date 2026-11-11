#!/usr/bin/env bash
# Generates the source tarballs packaging/vesqira.spec builds from, into DIR (default dist/srpm-sources). This is the only
# step that needs the network; the RPM %build step runs without it.
#   tools/vendor-sources.sh [VERSION [DIR]]      VERSION defaults to the Version line of the spec; VESQIRA_REF (default HEAD) is the git ref archived
# Needs: git, go (>= the go line of services/api/go.mod), cargo, npm, tar, gzip.
#   Source0  vesqira-VERSION.tar.gz               git archive of the committed tree
#   Source1  vesqira-VERSION-vendor-go.tar.gz     go mod vendor (services/api)
#   Source2  vesqira-VERSION-vendor-cargo.tar.gz  cargo vendor --locked (apps/desktop)
#   Source3  vesqira-VERSION-npm-cache.tar.gz     npm cache filled by npm ci (apps/web); the build runs npm ci --offline against it
# Dependency versions are exactly those in go.sum, Cargo.lock and package-lock.json (checked in the tree).
set -euo pipefail
root="$(cd "$(dirname "$0")/.." && pwd)"
version="${1:-$(sed -n 's/^Version:[[:space:]]*//p' "$root/packaging/vesqira.spec")}"
outdir="${2:-$root/dist/srpm-sources}"
mkdir -p "$outdir"; out="$(cd "$outdir" && pwd)"
ref="${VESQIRA_REF:-HEAD}"
work="$(mktemp -d)"; trap 'rm -rf "$work"' EXIT
tar_opts=(--sort=name --mtime=@0 --owner=0 --group=0 --numeric-owner)
pack() { tar "${tar_opts[@]}" -C "$1" -cf - "$2" | gzip -n -9 > "$3"; }   # reproducible: same inputs, same bytes

git -C "$root" archive --prefix="vesqira-$version/" "$ref" | gzip -n -9 > "$out/vesqira-$version.tar.gz"
tar -xzf "$out/vesqira-$version.tar.gz" -C "$work"
src="$work/vesqira-$version"

(cd "$src/services/api" && go mod vendor)
pack "$src/services/api" vendor "$out/vesqira-$version-vendor-go.tar.gz"

(cd "$src/apps/desktop" && cargo vendor --locked --quiet vendor >/dev/null)
pack "$src/apps/desktop" vendor "$out/vesqira-$version-vendor-cargo.tar.gz"

# --ignore-scripts: only the downloads are wanted here; the build step runs the install scripts offline
(cd "$src/apps/web" && npm ci --ignore-scripts --no-audit --no-fund --silent --cache "$work/npm-cache")
pack "$work" npm-cache "$out/vesqira-$version-npm-cache.tar.gz"

ls -l "$out"/vesqira-"$version"*.tar.gz
