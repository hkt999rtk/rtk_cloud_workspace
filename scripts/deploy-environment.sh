#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"
exec go run "$ROOT/scripts/go/rtk-cloud" -- deployment "$@"
