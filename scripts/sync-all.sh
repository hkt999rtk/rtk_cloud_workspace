#!/usr/bin/env sh
set -eu

root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
cd "$root"

git fetch origin
git submodule sync --recursive
git submodule update --init --recursive
git submodule foreach --recursive 'git fetch --all --prune'

echo "Fetched workspace and submodule remotes. Pinned commits were not changed."

