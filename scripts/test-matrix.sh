#!/usr/bin/env sh
set -eu

root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
cd "$root"

echo "== workspace status =="
git status --short --branch
git submodule status --recursive

echo
echo "== rtk_cloud_client quick validation =="
if [ -d repos/rtk_cloud_client ]; then
  (
    cd repos/rtk_cloud_client
    git diff --check
    git submodule status -- docs/rtk_cloud_contracts_doc || true
    PYTHONDONTWRITEBYTECODE=1 python3 -m unittest discover -s tools/tests
  )
else
  echo "SKIP: repos/rtk_cloud_client is missing"
fi

echo
echo "== repository status checks =="
repos=$(git config --file .gitmodules --get-regexp '^submodule\..*\.path$' | awk '{print $2}')
for repo in $repos; do
  if [ -d "$repo" ]; then
    echo "-- $repo"
    git -C "$repo" status --short --branch
  else
    echo "SKIP: $repo is missing"
  fi
done
