#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
test_root="$(mktemp -d)"
trap 'rm -rf "$test_root"' EXIT
export RUNNER_TEMP="$test_root/runner"
export RTK_CLOUD_CONFIG_ROOT="$RUNNER_TEMP/rtk_cloud"
export LINODE_TOKEN="fixture-linode-token"
export GHCR_PULL_USERNAME="fixture-user"
export GHCR_PULL_TOKEN="fixture-ghcr-token"
mkdir -p "$RUNNER_TEMP"

bundle="$RUNNER_TEMP/bundle.json"
jq -n --slurpfile catalog "$repo_root/cloud_env/secret-catalog.json" '
  {
    operator: {},
    runtime: (reduce $catalog[0].runtime_secret_ids[] as $id ({}; .[$id] = ("fixture-" + $id))),
    files: {}
  }
' > "$bundle"
chmod 0600 "$bundle"

output="$($repo_root/scripts/ci/materialize-rtk-cloud-secret-store.sh ci-test "$bundle")"
[[ "$output" == "materialized and verified CI SecretStore for ci-test" ]]
[[ "$output" != *"fixture-linode-token"* ]]
[[ "$(<"$RTK_CLOUD_CONFIG_ROOT/ci-test/operator/env/LINODE_TOKEN")" == "fixture-linode-token" ]]

mode() {
  if stat -f '%Lp' "$1" >/dev/null 2>&1; then
    stat -f '%Lp' "$1"
  else
    stat -c '%a' "$1"
  fi
}
[[ "$(mode "$RTK_CLOUD_CONFIG_ROOT/ci-test")" == "700" ]]
[[ "$(mode "$RTK_CLOUD_CONFIG_ROOT/ci-test/runtime/postgres")" == "600" ]]

echo "CI SecretStore materialization test passed"
