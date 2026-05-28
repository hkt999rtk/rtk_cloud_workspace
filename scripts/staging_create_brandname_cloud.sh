#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
printf 'warning: scripts/staging_create_brandname_cloud.sh is deprecated; use scripts/cloud-create-brandname-cloud.sh\n' >&2
exec "$SCRIPT_DIR/cloud-create-brandname-cloud.sh" "$@"
