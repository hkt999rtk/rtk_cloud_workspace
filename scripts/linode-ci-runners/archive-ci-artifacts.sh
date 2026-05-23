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

safe_repo="${repo//\//_}"
workdir=".artifacts/ci-runs/$safe_repo/$run_id"
mkdir -p "$workdir/artifacts"

gh run view "$run_id" --repo "$repo" --json databaseId,status,conclusion,headSha,headBranch,event,url,createdAt,updatedAt > "$workdir/run.json"
gh run download "$run_id" --repo "$repo" --dir "$workdir/artifacts" || true

if [[ -z "$prefix" ]]; then
  prefix="ci-runs/$safe_repo/$run_id"
fi

if command -v aws >/dev/null 2>&1; then
  aws s3 sync "$workdir" "s3://$LINODE_OBJ_BUCKET/$prefix/" \
    --endpoint-url "$LINODE_OBJ_ENDPOINT" \
    --only-show-errors
else
  LINODE_UPLOAD_SOURCE="$workdir" LINODE_UPLOAD_PREFIX="$prefix" python3 - <<'PY'
import os
from pathlib import Path

import boto3

source = Path(os.environ["LINODE_UPLOAD_SOURCE"])
prefix = os.environ["LINODE_UPLOAD_PREFIX"].strip("/")
bucket = os.environ["LINODE_OBJ_BUCKET"]
endpoint = os.environ["LINODE_OBJ_ENDPOINT"]

client = boto3.client(
    "s3",
    endpoint_url=endpoint,
    aws_access_key_id=os.environ.get("LINODE_OBJ_ACCESS_KEY_ID") or os.environ.get("AWS_ACCESS_KEY_ID"),
    aws_secret_access_key=os.environ.get("LINODE_OBJ_SECRET_ACCESS_KEY") or os.environ.get("AWS_SECRET_ACCESS_KEY"),
)

for path in sorted(source.rglob("*")):
    if path.is_file():
        rel = path.relative_to(source).as_posix()
        client.upload_file(str(path), bucket, f"{prefix}/{rel}")
PY
fi
printf 'uploaded artifacts to s3://%s/%s/ via %s\n' "$LINODE_OBJ_BUCKET" "$prefix" "$LINODE_OBJ_ENDPOINT"
