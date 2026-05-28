#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
printf 'warning: scripts/staging_check_certificates.sh is deprecated; use scripts/cloud-check-certificates.sh\n' >&2
exec "$SCRIPT_DIR/cloud-check-certificates.sh" "$@"
