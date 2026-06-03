#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

require_workflow() {
	local repo="$1"
	local artifact="$2"
	local workflow="$ROOT/repos/$repo/.github/workflows/release.yml"

	test -f "$workflow"
	grep -F 'tags:' "$workflow" >/dev/null
	grep -F '"v*"' "$workflow" >/dev/null
	grep -F 'LINODE_OBJ_ACCESS_KEY_ID' "$workflow" >/dev/null
	grep -F 'LINODE_OBJ_SECRET_ACCESS_KEY' "$workflow" >/dev/null
	grep -F 'LINODE_OBJ_BUCKET' "$workflow" >/dev/null
	grep -F 'LINODE_OBJ_ENDPOINT' "$workflow" >/dev/null
	grep -F "releases/$artifact-\$VERSION" "$workflow" >/dev/null
}

require_main_push_release() {
	local repo="$1"
	local workflow="$ROOT/repos/$repo/.github/workflows/release.yml"

	test -f "$workflow"
	grep -F 'branches:' "$workflow" >/dev/null
	grep -F 'main' "$workflow" >/dev/null
}

require_workflow rtk_cloud_client rtk_cloud_client
require_workflow rtk_cloud_logger rtk_cloud_logger
require_main_push_release rtk_account_manager
