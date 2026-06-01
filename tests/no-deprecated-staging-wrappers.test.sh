#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

if find "$ROOT/scripts" -maxdepth 1 -type f \( -name 'staging-*.sh' -o -name 'staging_*.sh' \) | grep -q .; then
	echo "deprecated scripts/staging-* or scripts/staging_* wrappers still exist" >&2
	exit 1
fi

if find "$ROOT/scripts" -type f -name '*.sh' | grep -q .; then
	echo "scripts/*.sh files still exist; use scripts/go/rtk-cloud instead" >&2
	exit 1
fi

if find "$ROOT" -maxdepth 1 -type f -name 'staging-*.sh' | grep -q .; then
	echo "deprecated root staging-* wrappers still exist" >&2
	exit 1
fi
