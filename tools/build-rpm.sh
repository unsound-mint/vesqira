#!/usr/bin/env bash
# Builds the source RPM (tools/build-srpm.sh) and then the installable RPM from it, with no network during the build, into dist/rpm/.
#   Needs: rpmbuild and the BuildRequires of packaging/vesqira.spec (sudo dnf builddep dist/srpm/vesqira-*.src.rpm), plus
#   go, cargo and npm for the vendoring step.
#   tools/build-rpm.sh [version]        install with: sudo dnf install dist/rpm/vesqira-*.rpm
# A clean build root instead: mock -r fedora-44-x86_64 --enable-network=false --rebuild dist/srpm/vesqira-*.src.rpm
set -euo pipefail
root="$(cd "$(dirname "$0")/.." && pwd)"
srpm="$("$root/tools/build-srpm.sh" "${1:-}" | tail -n 1)"
work="$(mktemp -d)"; trap 'rm -rf "$work"' EXIT
rpmbuild --rebuild "$srpm" --define "_topdir $work" >/dev/null
mkdir -p "$root/dist/rpm"
cp "$work"/RPMS/*/*.rpm "$root/dist/rpm/"
ls -1 "$root"/dist/rpm/*.rpm
