#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

FAKE_BIN="$TMP/bin"
LOG="$TMP/curl.log"
CURL_STATE="$TMP/curl-state"
WORKSPACE="$TMP/workspace"
ENV_ROOT="$WORKSPACE/cloud_env/staging"
RESOLVED_ENV_ROOT="$ENV_ROOT/linode"
mkdir -p "$FAKE_BIN" "$RESOLVED_ENV_ROOT/env" "$RESOLVED_ENV_ROOT/state"
printf '{"instances":{}}\n' > "$RESOLVED_ENV_ROOT/state/video-cloud-staging.state.json"
printf 'ACCOUNT_MANAGER_LINODE_ID=202\n' > "$RESOLVED_ENV_ROOT/state/account-manager-staging.env"
printf 'ADMIN_LINODE_ID=303\n' > "$RESOLVED_ENV_ROOT/state/cloud-admin-staging.env"

cat > "$FAKE_BIN/curl" <<'SH'
#!/usr/bin/env bash
set -euo pipefail
printf '%s\n' "$*" >> "$CURL_LOG"
if [[ "$*" == *"/linode/instances?page_size=500"* ]]; then
	if [[ -f "$CURL_STATE/instances-deleted" ]]; then
		cat <<'JSON'
{"data":[{"id":203,"label":"prod-api","status":"running"}]}
JSON
		exit 0
	fi
	cat <<'JSON'
{
  "data": [
    {"id": 101, "label": "staging-api", "status": "running"},
    {"id": 102, "label": "staging-db", "status": "running"},
    {"id": 201, "label": "video-cloud-staging-edge", "status": "running"},
    {"id": 202, "label": "rtk-account-manager-staging", "status": "running"},
    {"id": 203, "label": "prod-api", "status": "running"}
  ]
}
JSON
	exit 0
fi
if [[ "$*" == *"-X DELETE https://api.linode.com/v4/linode/instances/"* ]]; then
	mkdir -p "$CURL_STATE"
	touch "$CURL_STATE/instances-deleted"
	exit 0
fi
if [[ "$*" == *"-X GET https://api.linode.com/v4/networking/firewalls?page_size=500"* ]]; then
	cat <<'JSON'
{"data":[
  {"id":301,"label":"video-cloud-staging-edge"},
  {"id":302,"label":"video-cloud-staging-api"},
  {"id":401,"label":"rtk-account-manager-staging-fw"},
  {"id":501,"label":"rtk-cloud-admin-staging-firewall"},
  {"id":999,"label":"prod-firewall"}
]}
JSON
	exit 0
fi
if [[ "$*" == *"-X DELETE https://api.linode.com/v4/networking/firewalls/"* ]]; then
	exit 0
fi
if [[ "$*" == *"-X GET https://api.linode.com/v4/vpcs?page_size=500"* ]]; then
	cat <<'JSON'
{"data":[
  {"id":601,"label":"video-cloud-staging-vpc"},
  {"id":999,"label":"prod-vpc"}
]}
JSON
	exit 0
fi
if [[ "$*" == *"-X DELETE https://api.linode.com/v4/vpcs/"* ]]; then
	exit 0
fi
exit 0
SH
chmod +x "$FAKE_BIN/curl"

if PATH="$FAKE_BIN:$PATH" CURL_LOG="$LOG" CURL_STATE="$CURL_STATE" LINODE_TOKEN=test-token \
	"$ROOT/scripts/staging-remove-all-vm.sh" --workspace "$WORKSPACE" >/dev/null 2>"$TMP/missing-env-root.err"; then
	printf 'expected missing --env-root to fail\n' >&2
	exit 1
fi
grep -F -- '--env-root is required' "$TMP/missing-env-root.err" >/dev/null
if [[ -e "$LOG" ]]; then
	printf 'missing --env-root unexpectedly called curl\n' >&2
	exit 1
fi

printf 'no\n' | PATH="$FAKE_BIN:$PATH" CURL_LOG="$LOG" CURL_STATE="$CURL_STATE" LINODE_TOKEN=test-token \
	"$ROOT/scripts/staging-remove-all-vm.sh" --workspace "$WORKSPACE" --env-root "$ENV_ROOT" >/dev/null
if [[ -e "$LOG" ]]; then
	printf 'cancelled run unexpectedly called curl\n' >&2
	exit 1
fi

printf 'yes\n' | PATH="$FAKE_BIN:$PATH" CURL_LOG="$LOG" CURL_STATE="$CURL_STATE" LINODE_TOKEN=test-token \
	"$ROOT/scripts/staging-remove-all-vm.sh" --workspace "$WORKSPACE" --env-root "$ENV_ROOT" >/dev/null

grep -F -- '-X GET https://api.linode.com/v4/linode/instances?page_size=500' "$LOG" >/dev/null
grep -F -- '-X DELETE https://api.linode.com/v4/linode/instances/101' "$LOG" >/dev/null
grep -F -- '-X DELETE https://api.linode.com/v4/linode/instances/102' "$LOG" >/dev/null
grep -F -- '-X DELETE https://api.linode.com/v4/linode/instances/201' "$LOG" >/dev/null
grep -F -- '-X DELETE https://api.linode.com/v4/linode/instances/202' "$LOG" >/dev/null
if grep -F -- '/linode/instances/203' "$LOG" >/dev/null; then
	printf 'deleted a VM whose label is not staging\n' >&2
	exit 1
fi
grep -F -- '-X DELETE https://api.linode.com/v4/networking/firewalls/301' "$LOG" >/dev/null
grep -F -- '-X DELETE https://api.linode.com/v4/networking/firewalls/302' "$LOG" >/dev/null
grep -F -- '-X DELETE https://api.linode.com/v4/networking/firewalls/401' "$LOG" >/dev/null
grep -F -- '-X DELETE https://api.linode.com/v4/networking/firewalls/501' "$LOG" >/dev/null
grep -F -- '-X DELETE https://api.linode.com/v4/vpcs/601' "$LOG" >/dev/null
if grep -F -- '/networking/firewalls/999' "$LOG" >/dev/null || grep -F -- '/vpcs/999' "$LOG" >/dev/null; then
	printf 'deleted non-staging firewall or VPC\n' >&2
	exit 1
fi

test ! -e "$RESOLVED_ENV_ROOT/state/video-cloud-staging.state.json"
test ! -e "$RESOLVED_ENV_ROOT/state/account-manager-staging.env"
test ! -e "$RESOLVED_ENV_ROOT/state/cloud-admin-staging.env"
find "$RESOLVED_ENV_ROOT/backups" -path '*remove-vm-*/state/video-cloud-staging.state.json' | grep -q .
find "$RESOLVED_ENV_ROOT/backups" -path '*remove-vm-*/state/account-manager-staging.env' | grep -q .
find "$RESOLVED_ENV_ROOT/backups" -path '*remove-vm-*/state/cloud-admin-staging.env' | grep -q .
