#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

environment=""
arguments=("$@")
for ((index = 0; index < ${#arguments[@]}; index++)); do
  case "${arguments[$index]}" in
    --environment)
      if ((index + 1 < ${#arguments[@]})); then
        environment="${arguments[$((index + 1))]}"
      fi
      ;;
    --environment=*)
      environment="${arguments[$index]#--environment=}"
      ;;
  esac
done

if [[ -n "$environment" ]]; then
  # Provider credentials can all be valid while workload identities held on
  # PVCs are detached from the PKI registry after a database restore/rebuild.
  # Keep this read-only verification in the standard deployment check so that
  # such a stack is a NO-GO before any rollout starts.
  go run "$ROOT/scripts/go/rtk-cloud" -- secrets verify --environment "$environment"
fi

exec go run "$ROOT/scripts/go/rtk-cloud" -- deployment credentials-check --workspace "$ROOT" "$@"
