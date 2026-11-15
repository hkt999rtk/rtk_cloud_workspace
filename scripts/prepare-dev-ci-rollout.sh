#!/usr/bin/env bash
set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"
BUILD_TMP="$(mktemp -d)"
trap 'rm -rf "$BUILD_TMP"' EXIT
go build -o "$BUILD_TMP/rtk-cloud" ./scripts/go/rtk-cloud
"$BUILD_TMP/rtk-cloud" ci-rollout-prepare "$@"
