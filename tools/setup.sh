#!/usr/bin/env bash
# One-time (and repeatable) developer setup: checks the tools the repo needs, then installs dependencies.
# Python packages go into ./.venv, never into the system Python. Run through `make setup`.
set -uo pipefail
root="$(cd "$(dirname "$0")/.." && pwd)"
cd "$root"
missing=()

# version_ge A B: true when A >= B (dotted numbers)
version_ge() { [ "$(printf '%s\n%s\n' "$2" "$1" | sort -V | head -n1)" = "$2" ]; }

need() { # need <command> <minimum> <found-version> <hint>
  local cmd="$1" min="$2" have="$3" hint="$4"
  if [ -z "$have" ]; then
    missing+=("$cmd: not found (need >= $min). $hint")
  elif ! version_ge "$have" "$min"; then
    missing+=("$cmd: found $have, need >= $min. $hint")
  else
    echo "ok   $cmd $have (>= $min)"
  fi
}

go_min="$(awk '/^go /{print $2}' services/api/go.mod)"
node_min="$(grep -h 'node-version' .github/workflows/ci.yml | head -n1 | tr -dc '0-9')"
py_min="3.11"

go_have="$(command -v go >/dev/null && go env GOVERSION 2>/dev/null | sed 's/^go//')"
node_have="$(command -v node >/dev/null && node -v | sed 's/^v//')"
py_have="$(command -v python3 >/dev/null && python3 -c 'import sys;print("%d.%d.%d"%sys.version_info[:3])')"

# An older Go (>= 1.21) fetches the toolchain go.mod asks for, unless GOTOOLCHAIN=local.
go_floor="$go_min"
if [ "${GOTOOLCHAIN:-auto}" != local ] && [ -n "$go_have" ] && version_ge "$go_have" 1.21 && ! version_ge "$go_have" "$go_min"; then
  echo "note go $go_have is older than $go_min; go downloads the toolchain on first use (needs network)"; go_floor=1.21
fi
need go "$go_floor" "$go_have" "Install from https://go.dev/dl/ (Fedora: sudo dnf install golang; Debian/Ubuntu: use the tarball, apt is usually too old). With Go >= 1.21 an older install downloads the needed toolchain itself unless GOTOOLCHAIN=local."
need node "$node_min" "${node_have%%.*}" "Install Node $node_min from https://nodejs.org/ or a version manager (fnm, nvm)."
need python3 "$py_min" "$py_have" "Install Python $py_min or newer (Fedora: sudo dnf install python3; Debian/Ubuntu: sudo apt install python3 python3-venv)."
command -v npm >/dev/null || missing+=("npm: not found. It ships with Node.")
command -v git >/dev/null || missing+=("git: not found. Install it with your package manager.")
if command -v python3 >/dev/null && ! python3 -c 'import venv, ensurepip' 2>/dev/null; then
  missing+=("python3 venv module: missing. Debian/Ubuntu: sudo apt install python3-venv")
fi

# drop blanked entries
clean=(); for m in "${missing[@]}"; do [ -n "$m" ] && clean+=("$m"); done
if [ "${#clean[@]}" -gt 0 ]; then
  echo; echo "Missing or too old:"; printf '  - %s\n' "${clean[@]}"
  echo; echo "Install these, then run 'make setup' again."; exit 1
fi

echo; echo "== Python packages (.venv)"
if [ ! -x .venv/bin/python ]; then python3 -m venv .venv || { echo "could not create .venv"; exit 1; }; fi
.venv/bin/python -m pip install --quiet --disable-pip-version-check -r fixtures/requirements.txt || exit 1
echo "ok   fixtures/requirements.txt installed in .venv"

echo; echo "== Web app dependencies"
(cd apps/web && npm ci --no-audit --no-fund) || exit 1
echo; echo "== End-to-end test dependencies and Chromium"
(cd e2e && npm ci --no-audit --no-fund && npx playwright install chromium) || {
  echo "Playwright could not install Chromium (network, or missing system libraries)."
  echo "On Linux try: cd e2e && npx playwright install --with-deps chromium (needs sudo)."; exit 1; }

cat <<MSG

Setup finished. make targets use .venv automatically; for direct python3 calls: source .venv/bin/activate
Next: make check, then make demo (walk-through: docs/demo.md).
MSG
