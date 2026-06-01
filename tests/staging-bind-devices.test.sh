#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

WORKSPACE="$TMP/workspace"
ENV_ROOT="$WORKSPACE/cloud_env/staging/linode"
DEVICES_DIR="$ENV_ROOT/devices/test_device"
USERS_FILE="$ENV_ROOT/artifacts/users/rtk-users-test.json"
FAKE_BIN="$TMP/bin"
CURL_LOG="$TMP/curl-log"
mkdir -p \
	"$FAKE_BIN" \
	"$CURL_LOG" \
	"$ENV_ROOT/services/account-manager" \
	"$ENV_ROOT/state" \
	"$ENV_ROOT/artifacts/users" \
	"$DEVICES_DIR/manifests"

cat > "$ENV_ROOT/services/account-manager/account-manager-public-staging.env" <<'EOF_ENV'
ACCOUNT_MANAGER_LINODE_DOMAIN=account-manager.video-cloud-staging.example.com
ACCOUNT_MANAGER_LINODE_SSH_KEY=/tmp/fake-key
ACCOUNT_MANAGER_LINODE_SSH_USER=root
EOF_ENV

cat > "$ENV_ROOT/state/account-manager-staging.env" <<'EOF_STATE'
ACCOUNT_MANAGER_LINODE_HOST=203.0.113.10
ACCOUNT_MANAGER_LINODE_PUBLIC_IPV4=203.0.113.10
EOF_STATE

cat > "$ENV_ROOT/services/account-manager/account-manager-platform-admin.env" <<'EOF_ADMIN'
ACCOUNT_MANAGER_BOOTSTRAP_PLATFORM_ADMIN_EMAIL=root@example.com
ACCOUNT_MANAGER_BOOTSTRAP_PLATFORM_ADMIN_PASSWORD=correct-horse-battery-staple
EOF_ADMIN

jq -n '{
	brandname: "RTK",
	brand_cloud_id: "org-rtk",
	role: "member",
	users: [
		{email: "rtk+001@users.local", display_name: "RTK User 001", role: "member", password: "user-001-password"},
		{email: "rtk+002@users.local", display_name: "RTK User 002", role: "member", password: "user-002-password"}
	]
}' > "$USERS_FILE"

jq -n '[
	{device_id: "load-device-0001", device_type: "camera", display_name: "Camera 001", service_options: ["mqtt", "video_streaming", "video_storage"]},
	{device_id: "load-device-0002", device_type: "light", display_name: "Light 001", service_options: ["mqtt"]},
	{device_id: "load-device-0003", device_type: "camera", display_name: "Camera 002", service_options: ["mqtt", "video_streaming", "video_storage"]},
	{device_id: "load-device-0004", device_type: "smart_meter", display_name: "Smart Meter 001", service_options: ["mqtt"]}
]' > "$DEVICES_DIR/manifests/devices.json"

cat > "$FAKE_BIN/ssh" <<'SH'
#!/usr/bin/env bash
set -euo pipefail
cat >/dev/null
printf 'bootstrap admin env applied and account-manager is healthy\n' >&2
SH
chmod +x "$FAKE_BIN/ssh"

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
	root@example.com) token="platform-token" ;;
	rtk+001@users.local) token="user-token-001" ;;
	rtk+002@users.local) token="user-token-002" ;;
	*) printf 'unexpected login email: %s\n' "$email" >&2; exit 1 ;;
	esac
	printf '{"tokens":{"access_token":"%s"}}' "$token" >"$out"
	status=200
	;;
*/v1/admin/brand-clouds\?limit=200)
	printf '{"brand_clouds":[{"id":"org-rtk","name":"RTK","organization_kind":"brand_cloud","metadata":{"brandname":"RTK"}}]}' >"$out"
	status=200
	;;
*/v1/admin/device-claim-tokens)
	device_id="$(jq -r '.video_cloud_devid' "$payload")"
	if [[ "${FAKE_ALREADY_CLAIMED:-0}" == "1" ]]; then
		printf '{"error":"already_claimed","message":"device already claimed"}' >"$out"
		status=409
	else
		cp "$payload" "$FAKE_CURL_LOG/claim-$device_id.json"
		printf '{"id":"claim-%s","claim_token":"raw-token-%s","category":"%s","video_cloud_devid":"%s"}' \
			"$device_id" "$device_id" "$(jq -r '.category' "$payload")" "$device_id" >"$out"
		status=201
	fi
	;;
*/v1/orgs/org-rtk/devices/claim/resolve)
	token="$(jq -r '.claim_token' "$payload")"
	device_id="${token#raw-token-}"
	user_tag="001"
	if [[ "$token" == *"0003" || "$token" == *"0004" ]]; then
		user_tag="002"
	fi
	cp "$payload" "$FAKE_CURL_LOG/resolve-$device_id.json"
	jq -n \
		--arg claim_id "claim-$device_id" \
		--arg account_device_id "account-device-$device_id" \
		--arg device_id "$device_id" \
		--arg user_tag "$user_tag" \
		--arg activity_id "bulk-bind-$device_id" \
		--arg clip_public_key "bulk-bind-test-public-key" \
		'{
			claim_id: $claim_id,
			device: {id: $account_device_id, video_cloud_devid: $device_id},
			provision_input: {
				video_cloud_devid: $device_id,
				activity_id: $activity_id,
				clip_public_key: $clip_public_key,
				service_options: (if ($device_id == "load-device-0001" or $device_id == "load-device-0003") then ["mqtt", "video_streaming", "video_storage"] else ["mqtt"] end)
			},
			_user_tag: $user_tag
		}' >"$out"
	status=200
	;;
*/v1/orgs/org-rtk/devices/*/provision)
	account_device_id="$(basename "$(dirname "$url")")"
	cp "$payload" "$FAKE_CURL_LOG/provision-$account_device_id.json"
	jq -n --arg operation_id "$(jq -r '.operation_id' "$payload")" \
		'{operation: {id: $operation_id, status: "requested"}, status: "requested"}' >"$out"
	status=202
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

if PATH="$FAKE_BIN:$PATH" "/usr/local/go/bin/go" run "$ROOT/scripts/go/rtk-cloud" -- bind-devices \
	--workspace "$WORKSPACE" \
	--brandname RTK \
	--users-file "$USERS_FILE" \
	--devices-dir "$DEVICES_DIR" >"$TMP/missing-env-root.out" 2>&1; then
	echo "expected missing --env-root to fail" >&2
	exit 1
fi
grep -F -- '--env-root is required' "$TMP/missing-env-root.out" >/dev/null

DRY_RUN="$TMP/dry-run.json"
PATH="$FAKE_BIN:$PATH" FAKE_CURL_LOG="$CURL_LOG" "/usr/local/go/bin/go" run "$ROOT/scripts/go/rtk-cloud" -- bind-devices \
	--workspace "$WORKSPACE" \
	--env-root "$ENV_ROOT" \
	--brandname RTK \
	--users-file "$USERS_FILE" \
	--devices-dir "$DEVICES_DIR" \
	--count 4 \
	--dry-run >"$DRY_RUN"
jq -e '.action == "dry_run" and .brandname == "RTK" and .count == 4' "$DRY_RUN" >/dev/null
jq -e '.assignments | length == 4' "$DRY_RUN" >/dev/null
jq -e '[.assignments[] | select(.assigned_email == "rtk+001@users.local")] | length == 2' "$DRY_RUN" >/dev/null
jq -e '[.assignments[] | select(.assigned_email == "rtk+002@users.local")] | length == 2' "$DRY_RUN" >/dev/null
jq -e '.assignments[0].service_options == ["mqtt", "video_streaming", "video_storage"]' "$DRY_RUN" >/dev/null
jq -e '.assignments[1].service_options == ["mqtt"]' "$DRY_RUN" >/dev/null
if find "$CURL_LOG" -type f | grep -q .; then
	echo "dry-run must not call Account Manager APIs" >&2
	exit 1
fi

DEFAULT_DRY_RUN="$TMP/default-dry-run.json"
PATH="$FAKE_BIN:$PATH" FAKE_CURL_LOG="$CURL_LOG" "/usr/local/go/bin/go" run "$ROOT/scripts/go/rtk-cloud" -- bind-devices \
	--workspace "$WORKSPACE" \
	--env-root "$ENV_ROOT" \
	--brandname RTK \
	--dry-run >"$DEFAULT_DRY_RUN"
jq -e '.action == "dry_run" and .brandname == "RTK" and .count == 4' "$DEFAULT_DRY_RUN" >/dev/null
jq -e --arg users "$USERS_FILE" --arg devices "$DEVICES_DIR" '.users_file == $users and .devices_dir == $devices' "$DEFAULT_DRY_RUN" >/dev/null
jq -e '.assignments | length == 4' "$DEFAULT_DRY_RUN" >/dev/null

MANY_USERS="$ENV_ROOT/artifacts/users/rtk-users-100-test.json"
MANY_DEVICES="$TMP/devices-100"
mkdir -p "$MANY_DEVICES/manifests"
jq -n '{
	brandname: "RTK",
	brand_cloud_id: "org-rtk",
	role: "member",
	users: [range(1; 11) as $i | {
		email: ("rtk+" + ($i | tostring | if length < 3 then ("000"[0:(3-length)] + .) else . end) + "@users.local"),
		display_name: ("RTK User " + ($i | tostring)),
		role: "member",
		password: ("user-" + ($i | tostring) + "-password")
	}]
}' > "$MANY_USERS"
jq -n '
	([range(1; 41) as $i | {
		device_id: ("load-device-" + ($i | tostring | if length < 4 then ("0000"[0:(4-length)] + .) else . end)),
		device_type: "camera",
		display_name: ("Camera " + ($i | tostring)),
		service_options: ["mqtt", "video_streaming", "video_storage"]
	}] +
	[range(41; 66) as $i | {
		device_id: ("load-device-" + ($i | tostring | if length < 4 then ("0000"[0:(4-length)] + .) else . end)),
		device_type: "light",
		display_name: ("Light " + ($i | tostring)),
		service_options: ["mqtt"]
	}] +
	[range(66; 86) as $i | {
		device_id: ("load-device-" + ($i | tostring | if length < 4 then ("0000"[0:(4-length)] + .) else . end)),
		device_type: "air_conditioner",
		display_name: ("AC " + ($i | tostring)),
		service_options: ["mqtt"]
	}] +
	[range(86; 101) as $i | {
		device_id: ("load-device-" + ($i | tostring | if length < 4 then ("0000"[0:(4-length)] + .) else . end)),
		device_type: "smart_meter",
		display_name: ("Meter " + ($i | tostring)),
		service_options: ["mqtt"]
	}])
' > "$MANY_DEVICES/manifests/devices.json"
MANY_DRY_RUN="$TMP/dry-run-100.json"
PATH="$FAKE_BIN:$PATH" FAKE_CURL_LOG="$CURL_LOG" "/usr/local/go/bin/go" run "$ROOT/scripts/go/rtk-cloud" -- bind-devices \
	--workspace "$WORKSPACE" \
	--env-root "$ENV_ROOT" \
	--brandname RTK \
	--users-file "$MANY_USERS" \
	--devices-dir "$MANY_DEVICES" \
	--count 100 \
	--dry-run >"$MANY_DRY_RUN"
jq -e '
	.assignments
	| group_by(.assigned_email)
	| length == 10 and all(.[]; length == 10)
' "$MANY_DRY_RUN" >/dev/null

OUT="$TMP/out.json"
PATH="$FAKE_BIN:$PATH" FAKE_CURL_LOG="$CURL_LOG" "/usr/local/go/bin/go" run "$ROOT/scripts/go/rtk-cloud" -- bind-devices \
	--workspace "$WORKSPACE" \
	--env-root "$ENV_ROOT" \
	--brandname RTK \
	--users-file "$USERS_FILE" \
	--devices-dir "$DEVICES_DIR" \
	--count 4 >"$OUT"

if grep -Ei 'password|bearer|raw-token|private|device.key' "$OUT" >/dev/null; then
	echo "stdout must not include secrets" >&2
	exit 1
fi
jq -e '.action == "bound" and .brandname == "RTK" and .count == 4 and .created_claims == 4 and .resolved_claims == 4 and .provision_started == 4' "$OUT" >/dev/null
ARTIFACT="$(jq -r '.artifact_file' "$OUT")"
test -f "$ARTIFACT"
if grep -Ei 'password|bearer|raw-token|device.key' "$ARTIFACT" >/dev/null; then
	echo "artifact must be redacted" >&2
	exit 1
fi
jq -e '.schema == "rtk-cloud-workspace.bulk-device-bind/v1" and (.assignments | length == 4)' "$ARTIFACT" >/dev/null
jq -e '.assignments[0] | .assigned_email == "rtk+001@users.local" and .device_id == "load-device-0001" and .claim_id == "claim-load-device-0001" and .account_device_id == "account-device-load-device-0001" and .operation_id != "" and .status == "provision_requested"' "$ARTIFACT" >/dev/null
jq -e '.assignments[1].service_options == ["mqtt"]' "$ARTIFACT" >/dev/null
jq -e '.category == "ip_camera" and .service_options == ["mqtt", "video_streaming", "video_storage"]' "$CURL_LOG/claim-load-device-0001.json" >/dev/null
jq -e '.category == "mqtt_device" and .service_options == ["mqtt"]' "$CURL_LOG/claim-load-device-0002.json" >/dev/null
jq -e '.service_options == ["mqtt"] and .video_cloud_devid == "load-device-0002"' "$CURL_LOG/provision-account-device-load-device-0002.json" >/dev/null

if PATH="$FAKE_BIN:$PATH" FAKE_CURL_LOG="$CURL_LOG" FAKE_ALREADY_CLAIMED=1 "/usr/local/go/bin/go" run "$ROOT/scripts/go/rtk-cloud" -- bind-devices \
	--workspace "$WORKSPACE" \
	--env-root "$ENV_ROOT" \
	--brandname RTK \
	--users-file "$USERS_FILE" \
	--devices-dir "$DEVICES_DIR" \
	--count 1 >"$TMP/already.out" 2>"$TMP/already.err"; then
	echo "expected already-claimed device to fail" >&2
	exit 1
fi
grep -F 'claim token create failed' "$TMP/already.err" >/dev/null
