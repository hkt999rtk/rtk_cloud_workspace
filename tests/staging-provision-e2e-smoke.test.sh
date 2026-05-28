#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

WORKSPACE="$TMP/workspace"
ENV_ROOT="$WORKSPACE/cloud_env/staging/linode"
SECRETS="$ENV_ROOT"
FAKE_BIN="$TMP/bin"
CURL_LOG="$TMP/curl.log"
mkdir -p \
	"$FAKE_BIN" \
	"$ENV_ROOT/state" \
	"$ENV_ROOT/state" \
	"$ENV_ROOT/services/cloud-admin" \
	"$ENV_ROOT/env" \
	"$ENV_ROOT/artifacts"

cat > "$FAKE_BIN/curl" <<'SH'
#!/usr/bin/env bash
set -euo pipefail
url="${@: -1}"
printf '%s\n' "$url" >> "$CURL_LOG"
case "$url" in
*/healthz)
	printf 'ok\n'
	;;
*/version)
	printf '{"version":"test"}\n'
	;;
*/v1/health)
	printf '{"status":"ok"}\n'
	;;
*/api/service-health)
	printf '{"status":"ok"}\n'
	;;
*)
	printf 'unexpected url: %s\n' "$url" >&2
	exit 1
	;;
esac
SH
chmod +x "$FAKE_BIN/curl"

cat > "$ENV_ROOT/env/operator.env" <<'EOF_ENV'
LINODE_TOKEN=test-token
EOF_ENV

cat > "$ENV_ROOT/state/video-cloud-staging.state.json" <<'EOF_STATE'
{
  "stack": "video-cloud-staging",
  "instances": {
    "edge": {"public_ipv4": "203.0.113.5"}
  }
}
EOF_STATE

cat > "$ENV_ROOT/state/account-manager-staging.env" <<'EOF_AM'
ACCOUNT_MANAGER_LINODE_PUBLIC_IPV4=203.0.113.60
EOF_AM

cat > "$ENV_ROOT/state/cloud-admin-staging.env" <<'EOF_ADMIN'
ADMIN_LINODE_PUBLIC_IPV4=203.0.113.70
EOF_ADMIN

OUT="$TMP/out.txt"
PATH="$FAKE_BIN:$PATH" CURL_LOG="$CURL_LOG" "$ROOT/scripts/cloud-provision.sh" \
	--workspace "$WORKSPACE" \
	--env-root "$ENV_ROOT" \
	--e2e >"$OUT" 2>&1

REPORT_DIR="$(grep -F '[cloud-provision] e2e report:' "$OUT" | tail -n 1 | sed 's/^.*e2e report: //')"
test -f "$REPORT_DIR/e2e-report.md"
grep -F 'status: passed' "$REPORT_DIR/e2e-report.md" >/dev/null
grep -F 'PASS `video-cloud-healthz`' "$REPORT_DIR/e2e-report.md" >/dev/null
grep -F 'PASS `account-manager-health`' "$REPORT_DIR/e2e-report.md" >/dev/null
grep -F 'PASS `admin-service-health`' "$REPORT_DIR/e2e-report.md" >/dev/null
grep -F 'https://video-cloud-staging.realtekconnect.com/healthz' "$CURL_LOG" >/dev/null
grep -F 'https://account-manager.video-cloud-staging.realtekconnect.com/v1/health' "$CURL_LOG" >/dev/null
grep -F 'https://admin.video-cloud-staging.realtekconnect.com/api/service-health' "$CURL_LOG" >/dev/null

cat > "$FAKE_BIN/curl" <<'SH'
#!/usr/bin/env bash
set -euo pipefail
url="${@: -1}"
case "$url" in
*/api/service-health)
	exit 22
	;;
*)
	printf 'ok\n'
	;;
esac
SH
chmod +x "$FAKE_BIN/curl"

FAIL_OUT="$TMP/fail-out.txt"
if PATH="$FAKE_BIN:$PATH" "$ROOT/scripts/cloud-provision.sh" \
	--workspace "$WORKSPACE" \
	--env-root "$ENV_ROOT" \
	--e2e >"$FAIL_OUT" 2>&1; then
	printf 'e2e unexpectedly passed when admin service-health failed\n' >&2
	exit 1
fi
FAIL_REPORT_DIR="$(grep -F '[cloud-provision] e2e report:' "$FAIL_OUT" | tail -n 1 | sed 's/^.*e2e report: //')"
grep -F 'status: failed' "$FAIL_REPORT_DIR/e2e-report.md" >/dev/null
grep -F 'FAIL `admin-service-health`' "$FAIL_REPORT_DIR/e2e-report.md" >/dev/null
