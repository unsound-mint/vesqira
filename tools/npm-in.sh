#!/bin/sh
# Usage: tools/npm-in.sh <dir> <script>. Installs <dir> from its lockfile, then runs its npm script.
# `npm run` copies the user's ~/.npmrc into npm_config_* variables for the scripts it starts. npm 12 refuses `npm ci` when
# allow-scripts reaches it that way (EALLOWSCRIPTS), so a nested install failed for anyone who sets it for global tools such as
# m365. The file setting still applies to the install; only the inherited variable is dropped.
set -e
dir=$1
script=$2
env -u npm_config_allow_scripts npm --prefix "$dir" ci --loglevel=error >/dev/null
npm --prefix "$dir" run "$script" --silent
