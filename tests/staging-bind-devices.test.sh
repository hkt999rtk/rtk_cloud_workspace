#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
TMP="$(mktemp -d)"
SERVER_PID=""
trap 'if [[ -n "${SERVER_PID:-}" ]]; then kill "$SERVER_PID" 2>/dev/null || true; wait "$SERVER_PID" 2>/dev/null || true; fi; rm -rf "$TMP"' EXIT

WORKSPACE="$TMP/workspace"
ENV_ROOT="$WORKSPACE/cloud_env/staging/runtime"
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

"/usr/local/go/bin/go" run "$ROOT/scripts/go/rtk-cloud" -- test-data import-legacy \
	--env-root "$ENV_ROOT" \
	--brandname RTK \
	--latest-only >/dev/null

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
*/v1/auth/login|*/v1/brand-clouds/*/auth/login)
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
	printf '{"brand_clouds":[{"id":"org-rtk","name":"RTK","tenant_slug":"rtk","organization_kind":"brand_cloud","metadata":{"brandname":"RTK"}}]}' >"$out"
	status=200
	;;
	*/v1/admin/brand-clouds/org-rtk/device-bind-jobs)
		cp "$payload" "$FAKE_CURL_LOG/bulk-bind.json"
		if [[ "${FAKE_BULK_BIND_FAIL:-0}" == "1" ]]; then
			jq -n --arg device_id "$(jq -r '.items[0].video_cloud_devid' "$payload")" '{
				job: {status: "completed", requested: 1, created: 0, existing: 0, failed: 1},
				results: [{
					video_cloud_devid: $device_id,
					status: "failed",
					account_device_id: "",
					error: {code: "injected_failure", message: "forced bulk bind failure"}
				}]
			}' >"$out"
		else
			jq '
				{
					job: {
						status: "completed",
						requested: (.items | length),
						created: (.items | length),
						existing: 0,
						failed: 0
					},
					results: [.items[] | {
						video_cloud_devid,
						status: "created",
						account_device_id: ("account-device-" + .video_cloud_devid),
						device: {
							id: ("account-device-" + .video_cloud_devid),
							organization_id: "org-rtk",
							name: .device_name,
							category,
							metadata: {
								video_cloud_devid,
								video_cloud_activity_id: .activity_id,
								video_cloud_clip_public_key: .clip_public_key,
								service_options: .service_options
							}
						},
						provision_input: {
							video_cloud_devid,
							activity_id,
							clip_public_key,
							service_options
						},
						error: null
					}]
				}
			' "$payload" >"$out"
		fi
		status=200
		;;
	*/v1/orgs/org-rtk/devices\?*)
		printf 'bind-devices must not pre-scan /devices: %s\n' "$url" >&2
		exit 1
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

cat > "$TMP/fake_account_manager.py" <<'PY'
import base64
import json
import os
import sys
import time
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from urllib.parse import urlparse

log_dir = sys.argv[1]

def token(ttl):
    header = base64.urlsafe_b64encode(json.dumps({"alg": "none"}).encode()).rstrip(b"=").decode()
    payload = base64.urlsafe_b64encode(json.dumps({"exp": int(time.time()) + ttl}).encode()).rstrip(b"=").decode()
    return f"{header}.{payload}."

class Handler(BaseHTTPRequestHandler):
    def log_message(self, fmt, *args):
        return

    def send_json(self, status, body):
        data = json.dumps(body).encode()
        self.send_response(status)
        self.send_header("content-type", "application/json")
        self.send_header("content-length", str(len(data)))
        self.end_headers()
        self.wfile.write(data)

    def read_json(self):
        length = int(self.headers.get("content-length", "0"))
        if length == 0:
            return {}
        return json.loads(self.rfile.read(length))

    def do_GET(self):
        path = urlparse(self.path).path
        query = urlparse(self.path).query
        if path == "/v1/admin/brand-clouds" and query == "limit=200":
            self.send_json(200, {"brand_clouds": [{"id": "org-rtk", "name": "RTK", "tenant_slug": "rtk", "organization_kind": "brand_cloud", "metadata": {"brandname": "RTK"}}]})
            return
        if path == "/v1/orgs/org-rtk/devices" and "limit=" in query:
            self.send_json(500, {"error": "bind-devices must not pre-scan /devices"})
            return
        self.send_json(404, {"error": f"unexpected GET {self.path}"})

    def do_POST(self):
        path = urlparse(self.path).path
        payload = self.read_json()
        if path == "/v1/auth/login" or (path.startswith("/v1/brand-clouds/") and path.endswith("/auth/login")):
            self.send_json(200, {"tokens": {"access_token": token(3600), "refresh_token": token(86400)}})
            return
        if path == "/v1/admin/brand-clouds/org-rtk/device-bind-jobs":
            with open(os.path.join(log_dir, "bulk-bind.json"), "w") as f:
                json.dump(payload, f)
            if os.path.exists(os.path.join(log_dir, "fail-bulk")):
                device_id = payload["items"][0]["video_cloud_devid"]
                self.send_json(200, {
                    "job": {"status": "completed", "requested": 1, "created": 0, "existing": 0, "failed": 1},
                    "results": [{"video_cloud_devid": device_id, "status": "failed", "account_device_id": "", "error": {"code": "injected_failure", "message": "forced bulk bind failure"}}],
                })
                return
            results = []
            for item in payload["items"]:
                device_id = item["video_cloud_devid"]
                results.append({
                    "video_cloud_devid": device_id,
                    "status": "created",
                    "account_device_id": "account-device-" + device_id,
                    "device": {
                        "id": "account-device-" + device_id,
                        "organization_id": "org-rtk",
                        "name": item["device_name"],
                        "category": item["category"],
                        "metadata": {
                            "video_cloud_devid": device_id,
                            "video_cloud_activity_id": item["activity_id"],
                            "video_cloud_clip_public_key": item["clip_public_key"],
                            "service_options": item["service_options"],
                        },
                    },
                    "provision_input": {
                        "video_cloud_devid": device_id,
                        "activity_id": item["activity_id"],
                        "clip_public_key": item["clip_public_key"],
                        "service_options": item["service_options"],
                    },
                    "error": None,
                })
            self.send_json(200, {"job": {"status": "completed", "requested": len(results), "created": len(results), "existing": 0, "failed": 0}, "results": results})
            return
        if path.startswith("/v1/orgs/org-rtk/devices/") and path.endswith("/provision"):
            account_device_id = path.split("/")[-2]
            with open(os.path.join(log_dir, f"provision-{account_device_id}.json"), "w") as f:
                json.dump(payload, f)
            self.send_json(202, {"operation": {"id": payload["operation_id"], "status": "requested"}, "status": "requested"})
            return
        self.send_json(404, {"error": f"unexpected POST {self.path}"})

server = ThreadingHTTPServer(("127.0.0.1", 0), Handler)
print(server.server_port, flush=True)
server.serve_forever()
PY

python3 -u "$TMP/fake_account_manager.py" "$CURL_LOG" >"$TMP/fake-account-manager.port" 2>"$TMP/fake-account-manager.log" &
SERVER_PID="$!"
for _ in {1..50}; do
	if [[ -s "$TMP/fake-account-manager.port" ]]; then
		break
	fi
	sleep 0.1
done
ACCOUNT_MANAGER_BASE_URL="http://127.0.0.1:$(cat "$TMP/fake-account-manager.port")"
export ACCOUNT_MANAGER_BASE_URL
export ACCOUNT_MANAGER_BOOTSTRAP_PLATFORM_ADMIN_EMAIL=root@example.com
export ACCOUNT_MANAGER_BOOTSTRAP_PLATFORM_ADMIN_PASSWORD=correct-horse-battery-staple

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
jq -e --arg devices "$DEVICES_DIR" '.users_file == "" and .devices_dir == $devices' "$DEFAULT_DRY_RUN" >/dev/null
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
	--count 4 \
	--skip-bootstrap \
	--skip-direct-provision-bridge >"$OUT" 2>"$TMP/bind.err"

if grep -Ei 'password|bearer|raw-token|private|device.key' "$OUT" >/dev/null; then
	echo "stdout must not include secrets" >&2
	exit 1
fi
jq -e '.action == "bound" and .brandname == "RTK" and .count == 4 and .created_devices == 4 and .provision_started == 4 and .already_bound == 0' "$OUT" >/dev/null
TEST_DATA_DB="$(jq -r '.test_data_db' "$OUT")"
test -f "$TEST_DATA_DB"
INSPECT="$TMP/test-data-inspect.json"
"/usr/local/go/bin/go" run "$ROOT/scripts/go/rtk-cloud" -- test-data inspect \
	--env-root "$ENV_ROOT" \
	--brandname RTK >"$INSPECT"
jq -e '.schema == "rtk-cloud-workspace-test-data/v1" and .users == 2 and .devices == 4 and .bindings == 4' "$INSPECT" >/dev/null
if find "$ENV_ROOT/artifacts/device-bind" -name 'rtk-device-bind-*.json' -type f 2>/dev/null | grep -q .; then
	echo "bind-devices must not create legacy device-bind JSON artifacts" >&2
	exit 1
fi
jq -e '.items[0] | .category == "ip_camera" and .service_options == ["mqtt", "video_streaming", "video_storage"]' "$CURL_LOG/bulk-bind.json" >/dev/null
jq -e '.items[1] | .category == "mqtt_device" and .service_options == ["mqtt"]' "$CURL_LOG/bulk-bind.json" >/dev/null
jq -e '.service_options == ["mqtt"] and .video_cloud_devid == "load-device-0002"' "$CURL_LOG/provision-account-device-load-device-0002.json" >/dev/null
grep -F 'binding device 1/4: device=load-device-0001 user=rtk+001@users.local services=mqtt,video_streaming,video_storage' "$TMP/bind.err" >/dev/null
grep -F 'bulk bind chunk summary: chunk=1 requested=4 created=4 existing=0 failed=0' "$TMP/bind.err" >/dev/null
grep -F 'starting provision: device=load-device-0001 account_device=account-device-load-device-0001' "$TMP/bind.err" >/dev/null
grep -F 'bind progress: done=4/4 bulk_created=4 provision_started=4 skipped=0' "$TMP/bind.err" >/dev/null

touch "$CURL_LOG/fail-bulk"
if PATH="$FAKE_BIN:$PATH" FAKE_CURL_LOG="$CURL_LOG" "/usr/local/go/bin/go" run "$ROOT/scripts/go/rtk-cloud" -- bind-devices \
	--workspace "$WORKSPACE" \
	--env-root "$ENV_ROOT" \
	--brandname RTK \
	--users-file "$USERS_FILE" \
	--devices-dir "$DEVICES_DIR" \
	--count 1 \
	--skip-bootstrap \
	--skip-direct-provision-bridge >"$TMP/already.out" 2>"$TMP/already.err"; then
	echo "expected already-claimed device to fail" >&2
	exit 1
fi
grep -F 'admin bulk bind failed' "$TMP/already.err" >/dev/null
