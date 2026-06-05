#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

WORKSPACE="$TMP/workspace"
ENV_ROOT="$WORKSPACE/cloud_env/staging/linode"
USERS_FILE="$ENV_ROOT/artifacts/users/rtk-users-test.json"
BIND_DIR="$ENV_ROOT/artifacts/device-bind"
BIND_ARTIFACT="$BIND_DIR/rtk-device-bind-test.json"
FAKE_BIN="$TMP/bin"
CURL_LOG="$TMP/curl-log"
mkdir -p \
	"$FAKE_BIN" \
	"$CURL_LOG" \
	"$ENV_ROOT/services/account-manager" \
	"$ENV_ROOT/state" \
	"$ENV_ROOT/artifacts/users" \
	"$BIND_DIR"

cat > "$ENV_ROOT/services/account-manager/account-manager-public-staging.env" <<'EOF_ENV'
ACCOUNT_MANAGER_LINODE_DOMAIN=account-manager.video-cloud-staging.example.com
EOF_ENV

cat > "$ENV_ROOT/state/account-manager-staging.env" <<'EOF_STATE'
ACCOUNT_MANAGER_LINODE_HOST=203.0.113.10
ACCOUNT_MANAGER_LINODE_PUBLIC_IPV4=203.0.113.10
EOF_STATE

jq -n '{
	brandname: "RTK",
	brand_cloud_id: "org-rtk",
	role: "member",
	users: [
		{email: "rtk+001@users.local", display_name: "RTK User 001", role: "member", password: "user-001-password"},
		{email: "rtk+002@users.local", display_name: "RTK User 002", role: "member", password: "user-002-password"}
	]
}' > "$USERS_FILE"

jq -n --arg users_file "$USERS_FILE" '{
	schema: "rtk-cloud-workspace.bulk-device-bind/v1",
	generated_at: "2026-05-31T00:00:00Z",
	brandname: "RTK",
	brand_cloud_id: "org-rtk",
	count: 3,
	inputs: {users_file: $users_file, devices_dir: "/redacted/devices"},
	assignments: [
		{assignment_index: 0, assigned_email: "rtk+001@users.local", device_id: "load-device-0001", device_type: "camera", category: "ip_camera", service_options: ["mqtt", "video_streaming", "video_storage"], claim_id: "claim-load-device-0001", account_device_id: "account-device-load-device-0001", operation_id: "op-1", status: "provision_requested"},
		{assignment_index: 1, assigned_email: "rtk+001@users.local", device_id: "load-device-0002", device_type: "light", category: "mqtt_device", service_options: ["mqtt"], claim_id: "claim-load-device-0002", account_device_id: "account-device-load-device-0002", operation_id: "op-2", status: "provision_requested"},
		{assignment_index: 2, assigned_email: "rtk+002@users.local", device_id: "load-device-0003", device_type: "camera", category: "ip_camera", service_options: ["mqtt", "video_streaming", "video_storage"], claim_id: "claim-load-device-0003", account_device_id: "account-device-load-device-0003", operation_id: "op-3", status: "provision_requested"}
	]
}' > "$BIND_ARTIFACT"

cat > "$FAKE_BIN/curl" <<'SH'
#!/usr/bin/env bash
set -euo pipefail
out=""
write_code=""
data=""
args=("$@")
for ((i = 0; i < ${#args[@]}; i++)); do
	case "${args[$i]}" in
	-o) out="${args[$((i + 1))]}" ;;
	-w) write_code="${args[$((i + 1))]}" ;;
	--data-binary) data="${args[$((i + 1))]}" ;;
	esac
done
url="${args[$((${#args[@]} - 1))]}"
payload="${data#@}"
mkdir -p "$FAKE_CURL_LOG"
case "$url" in
*/v1/auth/login)
	email="$(jq -r '.email' "$payload")"
	case "$email" in
	rtk+001@users.local) token="user-token-001" ;;
	rtk+002@users.local) token="user-token-002" ;;
	*) printf 'unexpected login email: %s\n' "$email" >&2; exit 1 ;;
	esac
	printf '{"tokens":{"access_token":"%s"}}' "$token" >"$out"
	status=200
	;;
*/v1/orgs/org-rtk/devices/00000000-0000-0000-0000-000000000000/unprovision)
	if [[ "${FAKE_UNPROVISION_ROUTE_MISSING:-0}" == "1" ]]; then
		printf '404 page not found' >"$out"
	elif [[ "${FAKE_UNPROVISION_NESTED_NOT_FOUND:-0}" == "1" ]]; then
		printf '{"error":{"code":"not_found","message":"Resource not found"}}' >"$out"
	else
		printf '{"error":"not_found","message":"Resource not found"}' >"$out"
	fi
	status=404
	;;
*/v1/orgs/org-rtk/devices/*/unprovision)
	account_device_id="$(basename "$(dirname "$url")")"
	device_id="${account_device_id#account-device-}"
	if [[ "${FAKE_UNPROVISION_FAIL:-0}" == "1" ]]; then
		printf '{"error":"not_found","message":"device not found"}' >"$out"
		status=404
	else
		cp "$payload" "$FAKE_CURL_LOG/unprovision-$account_device_id.json"
		jq -n \
			--arg account_device_id "$account_device_id" \
			--arg device_id "$device_id" \
			'{
				unprovision: {
					device_id: $account_device_id,
					organization_id: "org-rtk",
					video_cloud_devid: $device_id,
					status: "unprovisioned",
					unprovisioned_at: "2026-05-31T01:02:03Z"
				}
			}' >"$out"
		status=200
	fi
	;;
*)
	printf 'unexpected curl url: %s\n' "$url" >&2
	exit 1
	;;
esac
if [[ -n "$write_code" ]]; then
	printf '%s' "${write_code//'%{http_code}'/$status}"
fi
SH
chmod +x "$FAKE_BIN/curl"

if PATH="$FAKE_BIN:$PATH" "/usr/local/go/bin/go" run "$ROOT/scripts/go/rtk-cloud" -- unprovision-devices \
	--workspace "$WORKSPACE" \
	--brandname RTK \
	--bind-artifact "$BIND_ARTIFACT" >"$TMP/missing-env-root.out" 2>&1; then
	echo "expected missing --env-root to fail" >&2
	exit 1
fi
grep -F -- '--env-root is required' "$TMP/missing-env-root.out" >/dev/null

DRY_RUN="$TMP/dry-run.json"
PATH="$FAKE_BIN:$PATH" FAKE_CURL_LOG="$CURL_LOG" "/usr/local/go/bin/go" run "$ROOT/scripts/go/rtk-cloud" -- unprovision-devices \
	--workspace "$WORKSPACE" \
	--env-root "$ENV_ROOT" \
	--brandname RTK \
	--bind-artifact "$BIND_ARTIFACT" \
	--count 2 \
	--dry-run >"$DRY_RUN"
jq -e '.action == "dry_run" and .brandname == "RTK" and .count == 2' "$DRY_RUN" >/dev/null
jq -e '.assignments | length == 2' "$DRY_RUN" >/dev/null
jq -e '.assignments[0].account_device_id == "account-device-load-device-0001"' "$DRY_RUN" >/dev/null
if find "$CURL_LOG" -type f | grep -q .; then
	echo "dry-run must not call Account Manager APIs" >&2
	exit 1
fi

DEFAULT_DRY_RUN="$TMP/default-dry-run.json"
PATH="$FAKE_BIN:$PATH" FAKE_CURL_LOG="$CURL_LOG" "/usr/local/go/bin/go" run "$ROOT/scripts/go/rtk-cloud" -- unprovision-devices \
	--workspace "$WORKSPACE" \
	--env-root "$ENV_ROOT" \
	--brandname RTK \
	--dry-run >"$DEFAULT_DRY_RUN"
jq -e '.action == "dry_run" and .brandname == "RTK" and .count == 3' "$DEFAULT_DRY_RUN" >/dev/null
jq -e --arg bind "$BIND_ARTIFACT" '.bind_artifact == $bind' "$DEFAULT_DRY_RUN" >/dev/null

OUT="$TMP/out.json"
PATH="$FAKE_BIN:$PATH" FAKE_CURL_LOG="$CURL_LOG" "/usr/local/go/bin/go" run "$ROOT/scripts/go/rtk-cloud" -- unprovision-devices \
	--workspace "$WORKSPACE" \
	--env-root "$ENV_ROOT" \
	--brandname RTK \
	--bind-artifact "$BIND_ARTIFACT" \
	--count 3 >"$OUT"

if grep -Ei 'password|bearer|raw-token|private|device.key|user-token' "$OUT" >/dev/null; then
	echo "stdout must not include secrets" >&2
	exit 1
fi
jq -e '.action == "unprovisioned" and .brandname == "RTK" and .count == 3 and .unprovisioned == 3' "$OUT" >/dev/null
ARTIFACT="$(jq -r '.artifact_file' "$OUT")"
test -f "$ARTIFACT"
if grep -Ei 'password|bearer|raw-token|device.key|user-token' "$ARTIFACT" >/dev/null; then
	echo "artifact must be redacted" >&2
	exit 1
fi
jq -e '.schema == "rtk-cloud-workspace.bulk-device-unprovision/v1" and (.assignments | length == 3)' "$ARTIFACT" >/dev/null
jq -e '.assignments[0] | .assigned_email == "rtk+001@users.local" and .device_id == "load-device-0001" and .account_device_id == "account-device-load-device-0001" and .response_device_id == "account-device-load-device-0001" and .video_cloud_devid == "load-device-0001" and .status == "unprovisioned"' "$ARTIFACT" >/dev/null
jq -e '.assignments[1].service_options == ["mqtt"]' "$ARTIFACT" >/dev/null
jq -e '.reason == "user_resale_factory_ready"' "$CURL_LOG/unprovision-account-device-load-device-0001.json" >/dev/null

NESTED_OUT="$TMP/nested-out.json"
PATH="$FAKE_BIN:$PATH" FAKE_CURL_LOG="$CURL_LOG" FAKE_UNPROVISION_NESTED_NOT_FOUND=1 "/usr/local/go/bin/go" run "$ROOT/scripts/go/rtk-cloud" -- unprovision-devices \
	--workspace "$WORKSPACE" \
	--env-root "$ENV_ROOT" \
	--brandname RTK \
	--bind-artifact "$BIND_ARTIFACT" \
	--count 1 >"$NESTED_OUT"
jq -e '.action == "unprovisioned" and .brandname == "RTK" and .count == 1 and .unprovisioned == 1' "$NESTED_OUT" >/dev/null

if PATH="$FAKE_BIN:$PATH" FAKE_CURL_LOG="$CURL_LOG" FAKE_UNPROVISION_FAIL=1 "/usr/local/go/bin/go" run "$ROOT/scripts/go/rtk-cloud" -- unprovision-devices \
	--workspace "$WORKSPACE" \
	--env-root "$ENV_ROOT" \
	--brandname RTK \
	--bind-artifact "$BIND_ARTIFACT" \
	--count 1 >"$TMP/fail.out" 2>"$TMP/fail.err"; then
	echo "expected unprovision failure to fail" >&2
	exit 1
fi
grep -F 'unprovision failed' "$TMP/fail.err" >/dev/null

if PATH="$FAKE_BIN:$PATH" FAKE_CURL_LOG="$CURL_LOG" FAKE_UNPROVISION_ROUTE_MISSING=1 "/usr/local/go/bin/go" run "$ROOT/scripts/go/rtk-cloud" -- unprovision-devices \
	--workspace "$WORKSPACE" \
	--env-root "$ENV_ROOT" \
	--brandname RTK \
	--bind-artifact "$BIND_ARTIFACT" \
	--count 1 >"$TMP/route-missing.out" 2>"$TMP/route-missing.err"; then
	echo "expected missing unprovision route to fail" >&2
	exit 1
fi
grep -F 'Account Manager unprovision API route is not deployed' "$TMP/route-missing.err" >/dev/null
