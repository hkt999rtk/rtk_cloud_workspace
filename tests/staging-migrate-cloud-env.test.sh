#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

WORKSPACE="$TMP/workspace"
mkdir -p "$WORKSPACE/cloud_env/staging/linode"

if "/usr/local/go/bin/go" run "$ROOT/scripts/go/rtk-cloud" -- migrate-env --workspace "$WORKSPACE" > "$TMP/missing-env-root.out" 2>&1; then
	echo "expected missing --env-root to fail" >&2
	exit 1
fi
grep -F -- '--env-root is required' "$TMP/missing-env-root.out" >/dev/null

if "/usr/local/go/bin/go" run "$ROOT/scripts/go/rtk-cloud" -- migrate-env \
	--workspace "$WORKSPACE" \
	--env-root "$WORKSPACE/cloud_env/staging" > "$TMP/retired.out" 2>&1; then
	echo "expected migrate-env to be retired" >&2
	exit 1
fi
grep -F 'migrate-env is retired with the staging VM toolkit' "$TMP/retired.out" >/dev/null
