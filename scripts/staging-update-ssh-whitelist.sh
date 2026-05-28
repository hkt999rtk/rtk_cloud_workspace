#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
printf 'warning: scripts/staging-update-ssh-whitelist.sh is deprecated; use scripts/cloud-update-ssh-whitelist.sh\n' >&2
exec "$SCRIPT_DIR/cloud-update-ssh-whitelist.sh" "$@"
