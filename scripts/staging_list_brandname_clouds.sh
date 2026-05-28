#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
printf 'warning: scripts/staging_list_brandname_clouds.sh is deprecated; use scripts/cloud-list-brandname-clouds.sh\n' >&2
exec "$SCRIPT_DIR/cloud-list-brandname-clouds.sh" "$@"
