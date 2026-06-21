#!/usr/bin/env bash
set -euo pipefail

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
workspace="$(cd "${script_dir}/.." && pwd)"

cd "${workspace}/scripts/go"
exec go run ./rtk-cloud -- destroy-linode-staging-resources \
  --workspace "${workspace}" \
  "$@"
