#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
printf 'warning: scripts/staging_migrate_cloud_env.sh is deprecated; use scripts/cloud-migrate-env.sh\n' >&2
exec "$SCRIPT_DIR/cloud-migrate-env.sh" "$@"
