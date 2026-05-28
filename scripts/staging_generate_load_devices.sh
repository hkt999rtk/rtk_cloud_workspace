#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
printf 'warning: scripts/staging_generate_load_devices.sh is deprecated; use scripts/cloud-generate-load-devices.sh\n' >&2
exec "$SCRIPT_DIR/cloud-generate-load-devices.sh" "$@"
