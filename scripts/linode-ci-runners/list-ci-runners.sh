#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
source "$ROOT_DIR/scripts/linode-ci-runners/runner-specs.sh"
load_runner_specs

seen="|"
for spec in "${RUNNER_SPECS[@]}"; do
  IFS='|' read -r _host_label _runner_name repo _type _custom_label <<<"$spec"
  case "$seen" in
    *"|$repo|"*) continue ;;
  esac
  seen="$seen$repo|"
  printf '== %s ==\n' "$repo"
  gh api "repos/$repo/actions/runners" \
    --jq '.runners[] | {name:.name,status:.status,busy:.busy,labels:[.labels[].name]}'
  printf '\n'
done
