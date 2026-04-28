#!/usr/bin/env sh
set -eu

root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
cd "$root"

echo "workspace:"
git status --short --branch
echo

git submodule foreach --recursive '
  echo
  echo "[$name] $path"
  git status --short --branch
  git log -1 --oneline --decorate
'

