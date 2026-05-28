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

check_ignored() {
  path=$1
  if git check-ignore -q "$path"; then
    pass "$path is ignored"
  else
    fail "$path is not ignored"
  fi
}

check_no_match() {
  label=$1
  pattern=$2
  shift 2
  if git grep -n -E "$pattern" -- "$@" >/tmp/rtk_secret_check_matches.$$ 2>/dev/null; then
    echo "Potential $label found:" >&2
    cat /tmp/rtk_secret_check_matches.$$ >&2
    rm -f /tmp/rtk_secret_check_matches.$$
    fail "$label present in tracked workspace files"
  else
    rm -f /tmp/rtk_secret_check_matches.$$
    pass "no $label in tracked workspace files"
  fi
}

check_file_no_match() {
  file=$1
  label=$2
  pattern=$3
  if [ ! -f "$file" ]; then
    fail "missing $file"
    return
  fi
  if grep -n -E "$pattern" "$file" >/tmp/rtk_secret_check_file_matches.$$ 2>/dev/null; then
    echo "Potential $label found in $file:" >&2
    cat /tmp/rtk_secret_check_file_matches.$$ >&2
    rm -f /tmp/rtk_secret_check_file_matches.$$
    fail "$label present in $file"
  else
    rm -f /tmp/rtk_secret_check_file_matches.$$
    pass "no $label in $file"
  fi
}

workspace_paths=".gitignore README.md docs scripts e2e_test"
manifest_example='docs/examples/secrets-manifest.example.json'

echo "== ignore rules =="
check_ignored .secrets
check_ignored .secrets.backup
check_ignored .secrets/staging/linode/admin/env/admin.env
check_ignored cloud_env
check_ignored cloud_env/staging/linode/env/operator.env
check_ignored cloud_env/staging/linode/keys/root-ca.key.pem

echo
echo "== tracked workspace secret scan =="
check_no_match "private key block" '-----BEGIN ([A-Z0-9 ]+ )?PRIVATE KEY-----' $workspace_paths
check_no_match "bearer token literal" 'Bearer[[:space:]]+[A-Za-z0-9._~+/-]{24,}' $workspace_paths
check_no_match "JWT-like token" 'eyJ[A-Za-z0-9_-]{20,}\.[A-Za-z0-9_-]{20,}\.[A-Za-z0-9_-]{20,}' $workspace_paths
check_no_match "hard-coded password assignment" '(^|[^A-Za-z0-9_])(PASSWORD|PASS|TOKEN|SECRET|PRIVATE_KEY)[A-Za-z0-9_]*=[^[:space:]<>$][^[:space:]]{7,}' $workspace_paths

echo
echo "== manifest example =="
check_file_no_match "$manifest_example" "private key block" '-----BEGIN ([A-Z0-9 ]+ )?PRIVATE KEY-----'
check_file_no_match "$manifest_example" "JWT-like token" 'eyJ[A-Za-z0-9_-]{20,}\.[A-Za-z0-9_-]{20,}\.[A-Za-z0-9_-]{20,}'
check_file_no_match "$manifest_example" "production staging reference" '"environment"[[:space:]]*:[[:space:]]*"production"|video-cloud-staging|staging-token|factory-linode-certset|example.invalid'

if [ "$failures" -gt 0 ]; then
  echo "Secrets checks failed: $failures" >&2
  exit 1
fi

echo "Secrets checks passed."
