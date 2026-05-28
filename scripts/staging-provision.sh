#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
printf 'warning: scripts/staging-provision.sh is deprecated; use scripts/cloud-provision.sh\n' >&2
exec "$SCRIPT_DIR/cloud-provision.sh" "$@"
