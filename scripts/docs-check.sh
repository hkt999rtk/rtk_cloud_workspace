#!/usr/bin/env sh
set -eu

root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
cd "$root"

failures=0

fail() {
  echo "FAIL: $*" >&2
  failures=$((failures + 1))
}

pass() {
  echo "OK: $*"
}

require_file() {
  if [ -f "$1" ]; then
    pass "found $1"
  else
    fail "missing $1"
  fi
}

require_dir() {
  if [ -d "$1" ]; then
    pass "found $1"
  else
    fail "missing $1"
  fi
}

echo "== workspace documentation entries =="
require_file README.md
require_file docs/README.md
require_file docs/architecture.md
require_file docs/documentation-governance.md
require_file docs/testing.md
require_file docs/LOAD_TEST_REPORT.md
require_file e2e_test/go.mod
require_file e2e_test/factory_enroll/README.md
require_file e2e_test/factory_enroll/cmd/rtk-factory-enroll-test/main.go
require_file e2e_test/factory_enroll/scripts/run_factory_enroll_local.sh
require_file e2e_test/video_cloud/load/cmd/rtk-video-loadtest/main.go
require_file e2e_test/video_cloud/load/scripts/run_video_loadtest.sh
require_file e2e_test/video_cloud/load/scripts/deploy_video_loadtest_two_host.sh
require_file docs/adr/README.md
require_file docs/product-level-evidence.md
require_file docs/cross-service-broker-packaging.md
require_file repos/rtk_cloud_contracts_doc/README.md
require_file scripts/collect-private-cloud-evidence.sh

if grep -q 'repos/rtk_mqtt' README.md docs/architecture.md scripts/test-matrix.sh; then
  fail "workspace README, architecture, or test matrix still references repos/rtk_mqtt"
else
  pass "removed repos/rtk_mqtt workspace references"
fi

echo
echo "== submodule registry =="
paths=$(git config --file .gitmodules --get-regexp '^submodule\..*\.path$' | awk '{print $2}')
for path in $paths; do
  require_dir "$path"
  if grep -q "\`$path\`" README.md; then
    pass "README documents $path"
  else
    fail "README does not document $path"
  fi
done

echo
echo "== service documentation entry points =="
require_file repos/rtk_cloud_client/docs/README.md
require_file repos/rtk_video_cloud/docs/architecture.md
require_file repos/rtk_account_manager/docs/SPEC.md
require_file repos/rtk_cloud_frontend/README.md
require_file repos/rtk_cloud_admin/README.md

echo
echo "== contracts submodule alignment =="
contract_paths="
repos/rtk_cloud_contracts_doc
repos/rtk_account_manager/contracts
repos/rtk_cloud_client/docs/rtk_cloud_contracts_doc
repos/rtk_video_cloud/docs/rtk_cloud_contracts_doc
repos/rtk_cloud_admin/rtk_cloud_contracts_doc
"

expected=""
for path in $contract_paths; do
  require_dir "$path"
  commit=$(git -C "$path" rev-parse HEAD)
  echo "$path $commit"
  if [ -z "$expected" ]; then
    expected=$commit
  elif [ "$commit" != "$expected" ]; then
    fail "$path is pinned to $commit, expected $expected"
  fi
done

if [ "$failures" -eq 0 ]; then
  echo
  echo "Documentation checks passed."
  exit 0
fi

echo
echo "Documentation checks failed: $failures" >&2
exit 1
