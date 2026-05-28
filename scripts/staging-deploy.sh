#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
printf 'warning: scripts/staging-deploy.sh is deprecated; use scripts/cloud-deploy.sh\n' >&2
exec "$SCRIPT_DIR/cloud-deploy.sh" "$@"
