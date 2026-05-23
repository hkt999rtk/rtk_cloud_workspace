#!/usr/bin/env bash
set -euo pipefail

repos=(
  hkt999rtk/rtk_account_manager
  hkt999rtk/rtk_cloud_admin
  hkt999rtk/rtk_video_cloud
)

for repo in "${repos[@]}"; do
  printf '== %s ==\n' "$repo"
  gh api "repos/$repo/actions/runners" \
    --jq '.runners[] | {name:.name,status:.status,busy:.busy,labels:[.labels[].name]}'
  printf '\n'
done
