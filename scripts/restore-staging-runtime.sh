#!/usr/bin/env bash
set -euo pipefail

if [[ "${1:-}" == "-h" || "${1:-}" == "--help" ]]; then
  echo "Legacy staging runtime copying is retired. See docs/deployment-operations.md for environment-store handoff and explicit migration." >&2
  exit 0
fi

echo "error: legacy staging runtime copying is retired; cloud_env is configuration only. Recover the matching ~/.config/rtk_cloud/staging/ store and use the shared deployment plan/preflight flow. Import original storage receipts with deployment storage-import-state; never copy operational state into cloud_env or reinitialize missing identity/data." >&2
exit 2
