#!/usr/bin/env bash
set -euo pipefail

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
manifest="${CLOUD_VALIDATION_RESOURCE_MANIFEST:?CLOUD_VALIDATION_RESOURCE_MANIFEST is required}"
bundle="${CLOUD_VALIDATION_RUNTIME_BUNDLE:-}"
retry_file="${CLOUD_VALIDATION_OUT_DIR:-$(dirname "$manifest")}/cleanup-retry.txt"
if [[ ! -f "$manifest" ]]; then
  echo "resource manifest is missing: $manifest" >&2
  exit 1
fi

local_files=()
cleanup_succeeded=0
if [[ -n "$bundle" && -f "$bundle" ]]; then
  while IFS= read -r file; do
    [[ -n "$file" ]] && local_files+=("$file")
  done < <(jq -r '.local_temporary_files[]? // empty' "$bundle")
  local_files+=("$bundle")
fi

cleanup_local_files() {
  if [[ "$cleanup_succeeded" != "1" ]]; then
    echo "remote cleanup failed; retaining mode-0600 recovery bundle for replay" >&2
    mkdir -p "$(dirname "$retry_file")"
    {
      printf 'CLOUD_VALIDATION_RUNTIME_BUNDLE=%q ' "$bundle"
      printf 'CLOUD_VALIDATION_RESOURCE_MANIFEST=%q ' "$manifest"
      printf 'CLOUD_VALIDATION_ACCOUNT_MANAGER_URL=%q ' "${CLOUD_VALIDATION_ACCOUNT_MANAGER_URL:-}"
      printf 'CLOUD_VALIDATION_VIDEO_CLOUD_URL=%q ' "${CLOUD_VALIDATION_VIDEO_CLOUD_URL:-}"
      printf 'CLOUD_VALIDATION_DEVICE_URL=%q ' "${CLOUD_VALIDATION_DEVICE_URL:-}"
      printf 'CLOUD_VALIDATION_RUN_ID=%q ' "${CLOUD_VALIDATION_RUN_ID:-}"
      printf '%q\n' "$script_dir/cleanup-fixture.sh"
    } > "$retry_file"
    chmod 600 "$retry_file"
    return
  fi
  local file
  for file in "${local_files[@]}"; do
    if [[ -f "$file" || -L "$file" ]]; then
      rm -f -- "$file"
    fi
  done
}
trap cleanup_local_files EXIT

if [[ -n "${CLOUD_VALIDATION_FIXTURE_CLEANUP_COMMAND:-}" ]]; then
  /bin/bash -lc "$CLOUD_VALIDATION_FIXTURE_CLEANUP_COMMAND"
  cleanup_succeeded=1
  exit 0
fi
if jq -e '.cleanup_required == true' "$manifest" >/dev/null; then
  "$(cd "$script_dir/.." && pwd)/providers/cleanup-staging-fixture.sh"
fi
cleanup_succeeded=1
