#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

# Compiler output only: environment data remains in the environment SecretStore.
# Execute the native CLI because go run converts its exit code 2 into 1.
BUILD_TMP="$(mktemp -d)"
DEPLOY_PID=""
trap 'rm -rf "$BUILD_TMP"' EXIT

forward_signal() {
  trap '' INT TERM
  if [[ -n "$DEPLOY_PID" ]]; then
    kill -s "$1" "$DEPLOY_PID" 2>/dev/null || true
    wait "$DEPLOY_PID" 2>/dev/null || true
  fi
  exit 1
}
trap 'forward_signal INT' INT
trap 'forward_signal TERM' TERM

run_child() {
  local status=0
  "$@" &
  DEPLOY_PID=$!
  wait "$DEPLOY_PID" || status=$?
  DEPLOY_PID=""
  return "$status"
}

run_child go build -o "$BUILD_TMP/rtk-cloud" ./scripts/go/rtk-cloud
run_child "$BUILD_TMP/rtk-cloud" deployment "$@"
