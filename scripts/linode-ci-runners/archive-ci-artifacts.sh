#!/usr/bin/env bash
set -euo pipefail

usage() {
  cat <<'USAGE'
Usage:
  scripts/linode-ci-runners/archive-ci-artifacts.sh --repo OWNER/REPO --run-id RUN_ID [--prefix PREFIX]

Downloads all GitHub Actions artifacts for one completed run and uploads them to
Linode Object Storage using the S3-compatible endpoint.
USAGE
}

repo=""
run_id=""
prefix=""
while [[ $# -gt 0 ]]; do
  case "$1" in
    --repo) repo="$2"; shift 2 ;;
    --run-id) run_id="$2"; shift 2 ;;
    --prefix) prefix="$2"; shift 2 ;;
    -h|--help) usage; exit 0 ;;
    *) echo "unknown argument: $1" >&2; usage >&2; exit 2 ;;
  esac
done

[[ -n "$repo" ]] || { echo "--repo is required" >&2; exit 2; }
[[ -n "$run_id" ]] || { echo "--run-id is required" >&2; exit 2; }
: "${LINODE_OBJ_BUCKET:?LINODE_OBJ_BUCKET is required}"
: "${LINODE_OBJ_ENDPOINT:?LINODE_OBJ_ENDPOINT is required}"

command -v gh >/dev/null 2>&1 || { echo "gh is required" >&2; exit 1; }
command -v aws >/dev/null 2>&1 || { echo "aws CLI is required" >&2; exit 1; }

safe_repo="${repo//\//_}"
workdir=".artifacts/ci-runs/$safe_repo/$run_id"
mkdir -p "$workdir/artifacts"

gh run view "$run_id" --repo "$repo" --json databaseId,status,conclusion,headSha,headBranch,event,url,createdAt,updatedAt > "$workdir/run.json"
gh run download "$run_id" --repo "$repo" --dir "$workdir/artifacts" || true

if [[ -z "$prefix" ]]; then
  prefix="ci-runs/$safe_repo/$run_id"
fi
aws s3 sync "$workdir" "s3://$LINODE_OBJ_BUCKET/$prefix/" \
  --endpoint-url "$LINODE_OBJ_ENDPOINT" \
  --only-show-errors
printf 'uploaded artifacts to s3://%s/%s/ via %s\n' "$LINODE_OBJ_BUCKET" "$prefix" "$LINODE_OBJ_ENDPOINT"
