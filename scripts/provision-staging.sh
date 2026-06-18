#!/usr/bin/env sh
set -eu

ROOT="$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)"
exec go run "$ROOT/scripts/go/rtk-cloud" -- staging-provision --workspace "$ROOT" "$@"
