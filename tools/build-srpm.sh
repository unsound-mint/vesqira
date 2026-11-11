#!/usr/bin/env bash
# Builds the source RPM into dist/srpm/ (what COPR and `mock --rebuild` take). Uses the tarballs of tools/vendor-sources.sh,
# generating them when they are missing.
#   tools/build-srpm.sh [VERSION [OUTDIR]]    VERSION defaults to the latest v* git tag (leading v dropped), else the spec's own
# The spec is copied with its Version line set, so the SRPM carries a literal version and rebuilds without any --define.
set -euo pipefail
root="$(cd "$(dirname "$0")/.." && pwd)"
spec_version="$(sed -n 's/^Version:[[:space:]]*//p' "$root/packaging/vesqira.spec")"
tag="$(git -C "$root" describe --tags --abbrev=0 --match 'v[0-9]*' 2>/dev/null || true)"
version="${1:-${tag#v}}"; version="${version:-$spec_version}"; version="${version#v}"
outdir="${2:-$root/dist/srpm}"
mkdir -p "$outdir"; out="$(cd "$outdir" && pwd)"
sources="$root/dist/srpm-sources"
for f in "" -vendor-go -vendor-cargo -npm-cache; do
  [ -f "$sources/vesqira-$version$f.tar.gz" ] || { "$root/tools/vendor-sources.sh" "$version" "$sources"; break; }
done
work="$(mktemp -d)"; trap 'rm -rf "$work"' EXIT
sed "s/^Version:.*/Version:        $version/" "$root/packaging/vesqira.spec" > "$work/vesqira.spec"
rpmbuild -bs "$work/vesqira.spec" --define "_topdir $work/top" --define "_sourcedir $sources" --define "_srcrpmdir $out" >/dev/null
ls -1 "$out"/vesqira-"$version"-*.src.rpm
