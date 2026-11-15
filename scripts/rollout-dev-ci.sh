#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

# The restricted GitHub job runs the actual image executor. The public entry
# below only starts this workflow on explicit operator request.
if [[ "${1:-}" == "--execute" ]]; then
  shift
  if [[ "${GITHUB_ACTIONS:-}" != "true" ]]; then
    echo "The execution mode is reserved for the restricted GitHub job." >&2
    exit 1
  fi
  BUILD_TMP="$(mktemp -d)"
  trap 'rm -rf "$BUILD_TMP"' EXIT
  go build -o "$BUILD_TMP/rtk-cloud" ./scripts/go/rtk-cloud
  "$BUILD_TMP/rtk-cloud" ci-rollout "$@"
  exit
fi

environment=""
repository=""
while (($#)); do
  case "$1" in
    --environment) [[ $# -ge 2 ]] || { echo "Missing environment" >&2; exit 2; }; environment="$2"; shift 2 ;;
    --repository) [[ $# -ge 2 ]] || { echo "Missing repository" >&2; exit 2; }; repository="$2"; shift 2 ;;
    -h|--help) echo "Usage: $0 --environment dev --repository OWNER/REPOSITORY"; exit 0 ;;
    *) echo "Unknown argument: $1" >&2; exit 2 ;;
  esac
done
if [[ "$environment" != "dev" || ! "$repository" =~ ^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$ ]]; then
  echo "An explicit dev environment and service repository are required." >&2
  exit 2
fi
request_id="$(openssl rand -hex 12)"
echo "[RUNNING] Start manual dev rollout for $repository"
gh workflow run dev-ci-rollout.yml --repo "$repository" --ref main -f "request_id=$request_id"
echo "[RUNNING] Wait for the requested GitHub job"
run_id=""
for ((attempt=0; attempt<60; attempt++)); do
  run_id="$(gh run list --repo "$repository" --workflow dev-ci-rollout.yml --event workflow_dispatch --limit 100 --json databaseId,displayTitle --jq ".[] | select(.displayTitle == \"Dev rollout $request_id\") | .databaseId" | head -n 1)"
  [[ -n "$run_id" ]] && break
  sleep 2
done
if [[ ! "$run_id" =~ ^[0-9]+$ ]]; then
  echo "The request was sent, but its GitHub run could not be located; inspect the workflow before retrying." >&2
  exit 1
fi
echo "[RUNNING] Follow GitHub run $run_id"
gh run watch "$run_id" --repo "$repository" --exit-status
echo "[PASS] Manual dev rollout completed for $repository"
