#!/usr/bin/env sh
set -eu

ROOT="$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)"
exec go run "$ROOT/scripts/go/rtk-cloud" -- run-staging-e2e --workspace "$ROOT" "$@"
