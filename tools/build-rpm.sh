#!/usr/bin/env bash
# Builds an installable RPM for Fedora into dist/rpm/.   Needs: go, npm, cargo (with the webkit2gtk4.1, gtk3 and libsoup3 development packages), rpmbuild.
#   tools/build-rpm.sh [version]        install with: sudo dnf install dist/rpm/vesqira-*.rpm
set -euo pipefail
root="$(cd "$(dirname "$0")/.." && pwd)"
version="${1:-0.2.0}"
work="$(mktemp -d)"; trap 'rm -rf "$work"' EXIT
src="$work/vesqira-$version"
mkdir -p "$src/worker" "$work/rpm"

(cd "$root/apps/web" && { [ -d node_modules ] || npm ci; } && npm run build >/dev/null)
(cd "$root/services/api" && CGO_ENABLED=0 go build -trimpath -buildvcs=false -ldflags "-s -w" -o "$src/vesqira-api" .)
(cd "$root/apps/desktop" && cargo build --release --locked >/dev/null)
cp "$root/apps/desktop/target/release/vesqira-desktop" "$src/"
cp -a "$root/apps/web/dist" "$src/web"
for f in fits_header.py plane_worker.py analysis.py convert.py orientation.py catalog_table.py relorient.py; do cp "$root/services/scientific-worker/$f" "$src/worker/"; done
cp "$root/packaging/vesqira" "$root/packaging/vesqira.desktop" "$root/packaging/vesqira.svg" "$root/packaging/io.github.unsound_mint.Vesqira.metainfo.xml" \
   "$root/LICENSE" "$root/THIRD_PARTY_NOTICES.md" "$src/"
tar -C "$work" -czf "$work/vesqira-$version.tar.gz" "vesqira-$version"

rpmbuild -bb "$root/packaging/vesqira.spec" --define "_topdir $work/rpm" --define "_sourcedir $work" \
  --define "vesqira_version $version" >/dev/null
mkdir -p "$root/dist/rpm"
cp "$work"/rpm/RPMS/*/*.rpm "$root/dist/rpm/"
ls -1 "$root"/dist/rpm/*.rpm
