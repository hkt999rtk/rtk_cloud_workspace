#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
# Build once and execute once: go run would turn the checker's exit 2 into 1.
CHECK_TMP="$(mktemp -d)"
CHECK_PID=""
trap 'rm -rf "$CHECK_TMP"' EXIT

forward_signal() {
  local signal="$1" status="$2"
  trap '' INT TERM
  if [[ -n "$CHECK_PID" ]]; then
    kill -s "$signal" "$CHECK_PID" 2>/dev/null || true
    # Give the checker time to perform its bounded canary cleanup.
    wait "$CHECK_PID" 2>/dev/null || true
  fi
  exit "$status"
}
trap 'forward_signal INT 1' INT
trap 'forward_signal TERM 1' TERM

run_child() {
  local status=0
  "$@" &
  CHECK_PID=$!
  wait "$CHECK_PID" || status=$?
  CHECK_PID=""
  return "$status"
}

cd "$ROOT"
run_child go build -o "$CHECK_TMP/rtk-cloud" ./scripts/go/rtk-cloud
run_child "$CHECK_TMP/rtk-cloud" deployment check --workspace "$ROOT" "$@"
