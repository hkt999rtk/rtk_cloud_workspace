#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

FAKE_BIN="$TMP/bin"
LOG="$TMP/curl.log"
mkdir -p "$FAKE_BIN"

cat > "$FAKE_BIN/curl" <<'SH'
#!/usr/bin/env bash
set -euo pipefail
printf '%s\n' "$*" >> "$CURL_LOG"
if [[ "$*" == *"/linode/instances?page_size=500"* ]]; then
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
exit 0
SH
chmod +x "$FAKE_BIN/curl"

printf 'no\n' | PATH="$FAKE_BIN:$PATH" CURL_LOG="$LOG" LINODE_TOKEN=test-token \
	"$ROOT/scripts/staging-remove-all-vm.sh" >/dev/null
if [[ -e "$LOG" ]]; then
	printf 'cancelled run unexpectedly called curl\n' >&2
	exit 1
fi

printf 'yes\n' | PATH="$FAKE_BIN:$PATH" CURL_LOG="$LOG" LINODE_TOKEN=test-token \
	"$ROOT/scripts/staging-remove-all-vm.sh" >/dev/null

grep -F -- '-X GET https://api.linode.com/v4/linode/instances?page_size=500' "$LOG" >/dev/null
grep -F -- '-X DELETE https://api.linode.com/v4/linode/instances/101' "$LOG" >/dev/null
grep -F -- '-X DELETE https://api.linode.com/v4/linode/instances/102' "$LOG" >/dev/null
grep -F -- '-X DELETE https://api.linode.com/v4/linode/instances/201' "$LOG" >/dev/null
grep -F -- '-X DELETE https://api.linode.com/v4/linode/instances/202' "$LOG" >/dev/null
if grep -F -- '/linode/instances/203' "$LOG" >/dev/null; then
	printf 'deleted a VM whose label is not staging\n' >&2
	exit 1
fi
