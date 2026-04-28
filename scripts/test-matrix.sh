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
    python3 -m unittest discover -s tools/tests
  )
else
  echo "SKIP: repos/rtk_cloud_client is missing"
fi

echo
echo "== repository status checks =="
for repo in repos/rtk_video_cloud repos/rtk_cloud_contracts_doc repos/rtk_account_manager repos/rtk_mqtt; do
  if [ -d "$repo" ]; then
    echo "-- $repo"
    git -C "$repo" status --short --branch
  else
    echo "SKIP: $repo is missing"
  fi
done

