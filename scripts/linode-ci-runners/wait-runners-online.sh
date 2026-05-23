#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
source "$ROOT_DIR/scripts/linode-ci-runners/runner-specs.sh"
load_runner_specs
TIMEOUT_SECONDS="${CI_RUNNER_ONLINE_TIMEOUT_SECONDS:-900}"
SLEEP_SECONDS="${CI_RUNNER_ONLINE_POLL_SECONDS:-15}"

deadline=$(( $(date +%s) + TIMEOUT_SECONDS ))

while true; do
  missing=0
  for spec in "${RUNNER_SPECS[@]}"; do
    IFS='|' read -r _host_label runner_name repo _type custom_label <<<"$spec"
    status="$(gh api "repos/$repo/actions/runners" --jq ".runners[] | select(.name == \"$runner_name\") | .status" 2>/dev/null || true)"
    if [[ "$status" == "online" ]]; then
      printf 'online: %s (%s)\n' "$runner_name" "$custom_label"
    else
      printf 'waiting: %s (%s), current=%s\n' "$runner_name" "$custom_label" "${status:-missing}"
      missing=$((missing + 1))
    fi
  done
  if [[ "$missing" -eq 0 ]]; then
    exit 0
  fi
  if [[ "$(date +%s)" -ge "$deadline" ]]; then
    printf 'timed out waiting for CI runners to become online\n' >&2
    exit 1
  fi
  sleep "$SLEEP_SECONDS"
done
