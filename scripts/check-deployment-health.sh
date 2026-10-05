#!/usr/bin/env bash
set -euo pipefail
export RTK_CLOUD_CHECK_SCRIPT_PHASE=post-deploy
export RTK_CLOUD_CHECK_SCRIPT_READ_ONLY=true
exec bash "$(dirname "${BASH_SOURCE[0]}")/check-deployment-credentials.sh" "$@"
