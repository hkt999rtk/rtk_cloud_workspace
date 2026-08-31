#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
TMP="$(mktemp -d)"
CREDENTIAL_SERVER_PID=""
trap 'if [[ -n "$CREDENTIAL_SERVER_PID" ]]; then kill "$CREDENTIAL_SERVER_PID" 2>/dev/null || true; wait "$CREDENTIAL_SERVER_PID" 2>/dev/null || true; fi; rm -rf "$TMP"' EXIT

WORKSPACE="$TMP/workspace"
ENV_ROOT="$WORKSPACE/cloud_env/staging/runtime"
export RTK_CLOUD_TEST_MODE=1
export RTK_CLOUD_CONFIG_ROOT="$TMP/rtk_cloud"
mkdir -p "$WORKSPACE/cloud_env/staging/overrides" "$ENV_ROOT/env" "$ENV_ROOT/artifacts/users" "$ENV_ROOT/artifacts/device-bind" "$ENV_ROOT/devices/test_device/manifests"
mkdir -m 700 -p "$RTK_CLOUD_CONFIG_ROOT/staging/operator/env" "$RTK_CLOUD_CONFIG_ROOT/staging/runtime"
printf '%s\n' 'test-token' > "$RTK_CLOUD_CONFIG_ROOT/staging/operator/env/SENDMAIL_HTTP_BEARER_TOKEN"
printf '%s\n' 'MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY=' > "$RTK_CLOUD_CONFIG_ROOT/staging/runtime/email-outbox-encryption"
chmod 600 "$RTK_CLOUD_CONFIG_ROOT/staging/operator/env/SENDMAIL_HTTP_BEARER_TOKEN" "$RTK_CLOUD_CONFIG_ROOT/staging/runtime/email-outbox-encryption"
cp -R "$ROOT/cloud_deploy" "$WORKSPACE/cloud_deploy"
cp "$ROOT/cloud_env/staging/environment.env" "$WORKSPACE/cloud_env/staging/environment.env"
cp "$ROOT/cloud_env/staging/deployment.env" "$WORKSPACE/cloud_env/staging/deployment.env"
cp "$ROOT/cloud_env/staging/overrides/architecture.env" "$WORKSPACE/cloud_env/staging/overrides/architecture.env"
cp "$ROOT/cloud_env/staging/overrides/adapter.env" "$WORKSPACE/cloud_env/staging/overrides/adapter.env"

cat > "$ENV_ROOT/env/stack.env" <<'EOF_ENV'
CLOUD_ENV_NAME=staging
CLOUD_PROVIDER=lke
CLOUD_REGION=us-sea
CLOUD_DNS_ROOT_DOMAIN=realtekconnect.com
CLOUD_STACK_NAME=video-cloud-staging
VIDEO_CLOUD_DOMAIN=video-cloud-staging.realtekconnect.com
VIDEO_CLOUD_CERTISSUER_DOMAIN=certissuer.video-cloud-staging.realtekconnect.com
ACCOUNT_MANAGER_DOMAIN=account-manager.video-cloud-staging.realtekconnect.com
CLOUD_ADMIN_DOMAIN=admin.video-cloud-staging.realtekconnect.com
VIDEO_CLOUD_LABEL_PREFIX=video-cloud-staging
VIDEO_CLOUD_VPC_LABEL=video-cloud-staging-vpc
VIDEO_CLOUD_SUBNET_LABEL=video-cloud-staging-subnet
ACCOUNT_MANAGER_LINODE_LABEL=rtk-account-manager-staging
ACCOUNT_MANAGER_LINODE_FIREWALL_LABEL=rtk-account-manager-staging-fw
ADMIN_LINODE_LABEL=rtk-cloud-admin-staging
ADMIN_LINODE_FIREWALL_LABEL=rtk-cloud-admin-staging-firewall
EOF_ENV

cat > "$TMP/credential-server.py" <<'PY'
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
import json


class Handler(BaseHTTPRequestHandler):
    objects = {}

    def do_GET(self):
        path = self.path.split("?", 1)[0]
        if path == "/v4/profile":
            body, content_type = json.dumps({"username": "operator"}), "application/json"
        elif path == "/v4/lke/clusters":
            body, content_type = json.dumps({"data": []}), "application/json"
        elif path in ("/v4/regions/sg-sin-2", "/v4/regions/us-sea"):
            region = path.rsplit("/", 1)[-1]
            body, content_type = json.dumps({"id": region, "status": "ok", "capabilities": ["Kubernetes", "Object Storage"]}), "application/json"
        elif path == "/v4/object-storage/buckets":
            endpoint = f"http://{self.headers['Host']}"
            body, content_type = json.dumps({"data": [
                {"label": "rtk-video-staging-sg", "region": "sg-sin-2", "s3_endpoint": endpoint},
                {"label": "rtk-cloud-client-artifacts", "region": "us-sea", "s3_endpoint": endpoint},
            ]}), "application/json"
        elif path == "/v4/object-storage/keys":
            body, content_type = json.dumps({"data": [
                {"id": 41, "access_key": "media-access", "bucket_access": [{"bucket_name": "rtk-video-staging-sg", "region": "sg-sin-2", "permissions": "read_write"}]},
                {"id": 42, "access_key": "artifact-access", "bucket_access": [{"bucket_name": "rtk-cloud-client-artifacts", "region": "us-sea", "permissions": "read_write"}]},
            ]}), "application/json"
        elif path == "/token":
            body, content_type = json.dumps({"token": "registry-token"}), "application/json"
        elif path.startswith("/v2/") and path.endswith("/tags/list"):
            body, content_type = json.dumps({"name": "fixture", "tags": ["sha-test"]}), "application/json"
        elif path.startswith("/v1/domains/") and path.endswith("/records"):
            body, content_type = "[]", "application/json"
        elif path in ("/test-bucket", "/rtk-video-staging-sg", "/rtk-cloud-client-artifacts"):
            body, content_type = "<ListBucketResult><IsTruncated>false</IsTruncated></ListBucketResult>", "application/xml"
        elif path in self.objects:
            body, content_type = self.objects[path].decode(), "application/octet-stream"
        else:
            self.send_error(404)
            return
        encoded = body.encode()
        self.send_response(200)
        self.send_header("Content-Type", content_type)
        self.send_header("Content-Length", str(len(encoded)))
        self.end_headers()
        self.wfile.write(encoded)

    def do_PUT(self):
        length = int(self.headers.get("Content-Length", "0"))
        self.objects[self.path.split("?", 1)[0]] = self.rfile.read(length)
        self.send_response(200)
        self.end_headers()

    def do_DELETE(self):
        self.objects.pop(self.path.split("?", 1)[0], None)
        self.send_response(204)
        self.end_headers()

    def log_message(self, _format, *_args):
        return


server = ThreadingHTTPServer(("127.0.0.1", 0), Handler)
print(server.server_port, flush=True)
server.serve_forever()
PY

python3 "$TMP/credential-server.py" > "$TMP/credential-server.port" &
CREDENTIAL_SERVER_PID=$!
for _ in {1..100}; do
	[[ -s "$TMP/credential-server.port" ]] && break
	sleep 0.05
done
test -s "$TMP/credential-server.port"
CREDENTIAL_SERVER="http://127.0.0.1:$(cat "$TMP/credential-server.port")"
CREDENTIAL_ENV="$TMP/operator.env"
cat > "$CREDENTIAL_ENV" <<EOF_CREDENTIALS
LINODE_TOKEN=linode-secret
GHCR_PULL_USERNAME=test-user
GHCR_PULL_TOKEN=valid-ghcr-token
GODADDY_KEY=godaddy-key
GODADDY_SECRET=godaddy-secret
LINODE_OBJ_ACCESS_KEY_ID=object-access
LINODE_OBJ_SECRET_ACCESS_KEY=object-secret
LINODE_OBJ_ENDPOINT=$CREDENTIAL_SERVER
LINODE_OBJ_BUCKET=test-bucket
LINODE_OBJ_REGION=us-test-1
LINODE_MEDIA_OBJ_ACCESS_KEY_ID=media-access
LINODE_MEDIA_OBJ_SECRET_ACCESS_KEY=media-secret
LINODE_ARTIFACT_OBJ_ACCESS_KEY_ID=artifact-access
LINODE_ARTIFACT_OBJ_SECRET_ACCESS_KEY=artifact-secret
EOF_CREDENTIALS
chmod 600 "$CREDENTIAL_ENV"
export RTK_CLOUD_DEPLOYMENT_CREDENTIAL_ENV_FILE="$CREDENTIAL_ENV"
export RTK_CLOUD_LINODE_API_ROOT="$CREDENTIAL_SERVER/v4"
export RTK_CLOUD_GHCR_TOKEN_ROOT="$CREDENTIAL_SERVER/token"
export RTK_CLOUD_GHCR_REGISTRY_ROOT="$CREDENTIAL_SERVER"
export RTK_CLOUD_GODADDY_API_ROOT="$CREDENTIAL_SERVER"
export LINODE_TOKEN=linode-secret
export GHCR_PULL_USERNAME=test-user
export GHCR_PULL_TOKEN=valid-ghcr-token
export GODADDY_KEY=godaddy-key
export GODADDY_SECRET=godaddy-secret
export LINODE_OBJ_ACCESS_KEY_ID=object-access
export LINODE_OBJ_SECRET_ACCESS_KEY=object-secret
export LINODE_OBJ_ENDPOINT="$CREDENTIAL_SERVER"
export LINODE_OBJ_BUCKET=test-bucket
export LINODE_OBJ_REGION=us-test-1
export LINODE_MEDIA_OBJ_ACCESS_KEY_ID=media-access
export LINODE_MEDIA_OBJ_SECRET_ACCESS_KEY=media-secret
export LINODE_ARTIFACT_OBJ_ACCESS_KEY_ID=artifact-access
export LINODE_ARTIFACT_OBJ_SECRET_ACCESS_KEY=artifact-secret
export AUTH_TOKEN_BASE_URL=https://admin.video-cloud-staging.realtekconnect.com
export SENDMAIL_HTTP_BASE_URL=https://sm.realtekconnect.com
export SENDMAIL_HTTP_BEARER_TOKEN=test-token
export SENDMAIL_HTTP_TIMEOUT=15s

COMMAND_LOG="$TMP/commands.log"
CONTRACT_STEPS="reset,provision,data,mqtt,runtime-logs,billing-log,billing-db"
make_stub() {
	local path="$1"
	local name="$2"
	cat > "$path" <<SH
#!/usr/bin/env bash
set -euo pipefail
printf '%s\\t%s\\n' "$name" "\$*" >> "$COMMAND_LOG"
case "$name" in
create-users)
	mkdir -p "$ENV_ROOT/artifacts/users"
	printf '{"brandname":"RTK","users":[{"email":"rtk+001@users.local","password":"super-secret"}]}\\n' > "$ENV_ROOT/artifacts/users/rtk-users-test.json"
	chmod 600 "$ENV_ROOT/artifacts/users/rtk-users-test.json"
	;;
generate-devices)
	mkdir -p "$ENV_ROOT/devices/test_device/manifests"
	printf '[]\\n' > "$ENV_ROOT/devices/test_device/manifests/devices.json"
	;;
bind-devices)
	mkdir -p "$ENV_ROOT/artifacts/device-bind"
	printf '{"brandname":"RTK","count":1,"assignments":[{"device_id":"dev-1"}]}\\n' > "$ENV_ROOT/artifacts/device-bind/rtk-device-bind-test.json"
	;;
validate-bind)
	printf '{"overall":"pass","report_file":"validate-report.md"}\\n'
	;;
setup-data)
	out_dir=""
	while [[ \$# -gt 0 ]]; do
		case "\$1" in
			--out-dir)
				out_dir="\$2"
				shift 2
				;;
			*)
				shift
				;;
		esac
	done
	mkdir -p "\$out_dir/logs" "\$out_dir/bind-validation" "$ENV_ROOT/artifacts/users" "$ENV_ROOT/artifacts/device-bind"
	test_data_db="$ENV_ROOT/artifacts/test-data/rtk-test-data.sqlite"
	printf '{"brandname":"RTK","users":[{"email":"rtk+001@users.local"}]}\\n' > "$ENV_ROOT/artifacts/users/rtk-users-test.json"
	mkdir -p "$ENV_ROOT/devices/test_device/manifests"
	printf '[{"device_id":"dev-1","device_type":"camera","service_options":["mqtt","video_streaming","video_storage"]}]\\n' > "$ENV_ROOT/devices/test_device/manifests/devices.json"
	printf '{"brandname":"RTK","brand_cloud_id":"org-rtk","count":1,"assignments":[{"assigned_email":"rtk+001@users.local","device_id":"dev-1","device_type":"camera","service_options":["mqtt","video_streaming","video_storage"],"account_device_id":"account-dev-1","operation_id":"operation-1","status":"ready"}]}\\n' > "$ENV_ROOT/artifacts/device-bind/rtk-device-bind-test.json"
	"/usr/local/go/bin/go" run "$ROOT/scripts/go/rtk-cloud" -- test-data import-legacy --env-root "$ENV_ROOT" --brandname RTK --latest-only >/dev/null
	cat > "\$out_dir/summary.json" <<JSON
{
  "overall": "pass",
  "summary_file": "\$out_dir/summary.json",
  "users_file": "$ENV_ROOT/artifacts/users/rtk-users-test.json",
  "device_bind_file": "$ENV_ROOT/artifacts/device-bind/rtk-device-bind-test.json",
  "test_data_db": "\$test_data_db",
  "bind_validation_dir": "\$out_dir/bind-validation",
  "steps": [
    {"name": "create_brand", "status": "PASS", "exit_code": 0, "duration_seconds": 1, "log_file": "\$out_dir/logs/create_brand.log"},
    {"name": "create_users", "status": "PASS", "exit_code": 0, "duration_seconds": 1, "log_file": "\$out_dir/logs/create_users.log"},
    {"name": "create_devices", "status": "PASS", "exit_code": 0, "duration_seconds": 1, "log_file": "\$out_dir/logs/create_devices.log"},
    {"name": "bind_devices", "status": "PASS", "exit_code": 0, "duration_seconds": 1, "log_file": "\$out_dir/logs/bind_devices.log"},
    {"name": "validate_bind", "status": "PASS", "exit_code": 0, "duration_seconds": 1, "log_file": "\$out_dir/logs/validate_bind.log"}
  ]
}
JSON
	printf '{"overall":"pass","summary_file":"%s","users_file":"%s","device_bind_file":"%s","test_data_db":"%s"}\\n' "\$out_dir/summary.json" "$ENV_ROOT/artifacts/users/rtk-users-test.json" "$ENV_ROOT/artifacts/device-bind/rtk-device-bind-test.json" "\$test_data_db"
	;;
mqtt-test)
	mkdir -p "$TMP/mqtt-report"
	printf '{"overall":"pass","status":"PASS","report_file":"%s","results_file":"%s"}\\n' "$TMP/mqtt-report/test_report.md" "$TMP/mqtt-report/results.json"
	printf '# MQTT Report\\nPASS\\n' > "$TMP/mqtt-report/test_report.md"
	printf '{"overall":"pass","devices":[{"device_id":"dev-1","runtime_log_stream_id":"mqtt-e2e-dev-1","runtime_log_expectations":[{"seq":1,"source":"device_client","message":"mqtt_e2e telemetry device_client publish"}]}]}\\n' > "$TMP/mqtt-report/results.json"
	;;
mqtt-log-verify)
	out_dir=""
	while [[ \$# -gt 0 ]]; do
		case "\$1" in
			--out-dir)
				out_dir="\$2"
				shift 2
				;;
			*)
				shift
				;;
		esac
	done
	mkdir -p "\$out_dir"
	printf '{"overall":"pass","checked_devices":1,"checked_logs":1}\\n' > "\$out_dir/summary.json"
	printf '{"overall":"pass","summary_file":"%s"}\\n' "\$out_dir/summary.json"
	;;
billing-verify)
	out_dir=""
	while [[ \$# -gt 0 ]]; do
		case "\$1" in
			--out-dir)
				out_dir="\$2"
				shift 2
				;;
			*)
				shift
				;;
		esac
	done
	mkdir -p "\$out_dir"
	printf '{"overall":"pass","checks":{"log":true,"db":true}}\\n' > "\$out_dir/summary.json"
	printf '{"overall":"pass","summary_file":"%s"}\\n' "\$out_dir/summary.json"
	;;
esac
SH
	chmod +x "$path"
}

make_stub "$TMP/remove-k8s.sh" remove-k8s
make_stub "$TMP/provision-k8s.sh" provision-k8s
make_stub "$TMP/remove.sh" remove
make_stub "$TMP/provision.sh" provision
make_stub "$TMP/create-brand.sh" create-brand
make_stub "$TMP/create-users.sh" create-users
make_stub "$TMP/generate-devices.sh" generate-devices
make_stub "$TMP/bind-devices.sh" bind-devices
make_stub "$TMP/validate-bind.sh" validate-bind
make_stub "$TMP/setup-data.sh" setup-data
make_stub "$TMP/mqtt-test.sh" mqtt-test
make_stub "$TMP/mqtt-log-verify.sh" mqtt-log-verify
make_stub "$TMP/billing-verify.sh" billing-verify

export LKE_VIDEO_CLOUD_IMAGE=registry.example.test/rtk/video-cloud:test
export LKE_ACCOUNT_MANAGER_IMAGE=registry.example.test/rtk/account-manager:test
export LKE_CLOUD_ADMIN_IMAGE=registry.example.test/rtk/cloud-admin:test
export LKE_FRONTEND_IMAGE=registry.example.test/rtk/frontend:test
export LKE_CLOUD_LOGGER_IMAGE=registry.example.test/rtk/cloud-logger:test
export LKE_BILLING_IMAGE=registry.example.test/rtk/billing:test

RESET_PLAN_OUT="$TMP/reset-plan.out"
CLOUD_STAGING_E2E_REMOVE_K8S_SCRIPT="$TMP/remove-k8s.sh" \
	"/usr/local/go/bin/go" run "$ROOT/scripts/go/rtk-cloud" -- staging-reset-k8s \
	--workspace "$WORKSPACE" \
	--env-root "$ENV_ROOT" \
	--plan > "$RESET_PLAN_OUT"
grep -F 'cloud-staging-reset-k8s plan' "$RESET_PLAN_OUT" >/dev/null
grep -F 'purge_storage: false' "$RESET_PLAN_OUT" >/dev/null
grep -F 'storage: preserve PV/PVC/provider volumes' "$RESET_PLAN_OUT" >/dev/null
test ! -e "$COMMAND_LOG"

RESET_PURGE_PLAN_OUT="$TMP/reset-purge-plan.out"
CLOUD_STAGING_E2E_REMOVE_K8S_SCRIPT="$TMP/remove-k8s.sh" \
	"/usr/local/go/bin/go" run "$ROOT/scripts/go/rtk-cloud" -- staging-reset-k8s \
	--workspace "$WORKSPACE" \
	--env-root "$ENV_ROOT" \
	--purge-storage \
	--plan > "$RESET_PURGE_PLAN_OUT"
grep -F 'purge_storage: true' "$RESET_PURGE_PLAN_OUT" >/dev/null
grep -F 'storage: purge PV/PVC/provider volumes' "$RESET_PURGE_PLAN_OUT" >/dev/null
test ! -e "$COMMAND_LOG"

RESET_RUN_OUT="$TMP/reset-run.out"
CLOUD_STAGING_E2E_REMOVE_K8S_SCRIPT="$TMP/remove-k8s.sh" \
	"/usr/local/go/bin/go" run "$ROOT/scripts/go/rtk-cloud" -- staging-reset-k8s \
	--workspace "$WORKSPACE" \
	--env-root "$ENV_ROOT" \
	--confirm video-cloud-staging > "$RESET_RUN_OUT"
grep -F $'remove-k8s\t--workspace '"$WORKSPACE"$' --env-root '"$WORKSPACE/cloud_env/staging/runtime"$' --yes' "$COMMAND_LOG" >/dev/null
grep -F '"overall":"pass"' "$RESET_RUN_OUT" >/dev/null

: > "$COMMAND_LOG"
RESET_PURGE_RUN_OUT="$TMP/reset-purge-run.out"
CLOUD_STAGING_E2E_REMOVE_K8S_SCRIPT="$TMP/remove-k8s.sh" \
	"/usr/local/go/bin/go" run "$ROOT/scripts/go/rtk-cloud" -- staging-reset-k8s \
	--workspace "$WORKSPACE" \
	--env-root "$ENV_ROOT" \
	--confirm video-cloud-staging \
	--purge-storage > "$RESET_PURGE_RUN_OUT"
grep -F $'remove-k8s\t--workspace '"$WORKSPACE"$' --env-root '"$WORKSPACE/cloud_env/staging/runtime"$' --yes --purge-storage' "$COMMAND_LOG" >/dev/null
grep -F '"purge_storage":true' "$RESET_PURGE_RUN_OUT" >/dev/null

: > "$COMMAND_LOG"
PROVISION_PLAN_OUT="$TMP/provision-plan.out"
CLOUD_STAGING_E2E_PROVISION_K8S_SCRIPT="$TMP/provision-k8s.sh" \
	"/usr/local/go/bin/go" run "$ROOT/scripts/go/rtk-cloud" -- staging-provision \
	--workspace "$WORKSPACE" \
	--env-root "$ENV_ROOT" \
	--plan > "$PROVISION_PLAN_OUT"
grep -F 'cloud-staging-provision plan' "$PROVISION_PLAN_OUT" >/dev/null
grep -F 'phase: provision' "$PROVISION_PLAN_OUT" >/dev/null
grep -F 'provision K8s staging with '"$TMP/provision-k8s.sh" "$PROVISION_PLAN_OUT" >/dev/null
test ! -s "$COMMAND_LOG"

PROVISION_RUN_OUT="$TMP/provision-run.out"
if ! CLOUD_STAGING_E2E_PROVISION_K8S_SCRIPT="$TMP/provision-k8s.sh" \
	"/usr/local/go/bin/go" run "$ROOT/scripts/go/rtk-cloud" -- staging-provision \
	--workspace "$WORKSPACE" \
	--env-root "$ENV_ROOT" \
	--confirm video-cloud-staging > "$PROVISION_RUN_OUT"; then
	cat "$PROVISION_RUN_OUT" >&2
	exit 1
fi
grep -F $'provision-k8s\t--workspace '"$WORKSPACE"$' --env-root '"$WORKSPACE/cloud_env/staging/runtime"$' --confirm video-cloud-staging' "$COMMAND_LOG" >/dev/null
grep -F '"overall":"pass"' "$PROVISION_RUN_OUT" >/dev/null

: > "$COMMAND_LOG"
ACCEPTANCE_PLAN_OUT="$TMP/acceptance-plan.out"
CLOUD_STAGING_E2E_DATA_SETUP_SCRIPT="$TMP/setup-data.sh" \
CLOUD_STAGING_E2E_MQTT_TEST_SCRIPT="$TMP/mqtt-test.sh" \
CLOUD_STAGING_E2E_MQTT_LOG_VERIFY_SCRIPT="$TMP/mqtt-log-verify.sh" \
	"/usr/local/go/bin/go" run "$ROOT/scripts/go/rtk-cloud" -- staging-acceptance \
	--workspace "$WORKSPACE" \
	--env-root "$ENV_ROOT" \
	--plan > "$ACCEPTANCE_PLAN_OUT"
grep -F 'cloud-environment-e2e-test plan' "$ACCEPTANCE_PLAN_OUT" >/dev/null
grep -F 'phase: acceptance' "$ACCEPTANCE_PLAN_OUT" >/dev/null
grep -F 'setup brand/users/devices with '"$TMP/setup-data.sh" "$ACCEPTANCE_PLAN_OUT" >/dev/null
if grep -F 'reset environment K8s' "$ACCEPTANCE_PLAN_OUT" >/dev/null || grep -F 'provision environment K8s' "$ACCEPTANCE_PLAN_OUT" >/dev/null; then
	echo "staging-acceptance plan must not include reset/provision phases" >&2
	exit 1
fi
test ! -s "$COMMAND_LOG"

ACCEPTANCE_RUN_OUT="$TMP/acceptance-run.out"
CLOUD_STAGING_E2E_DATA_SETUP_SCRIPT="$TMP/setup-data.sh" \
CLOUD_STAGING_E2E_MQTT_TEST_SCRIPT="$TMP/mqtt-test.sh" \
CLOUD_STAGING_E2E_MQTT_LOG_VERIFY_SCRIPT="$TMP/mqtt-log-verify.sh" \
CLOUD_STAGING_E2E_BILLING_VERIFY_SCRIPT="$TMP/billing-verify.sh" \
CLOUD_STAGING_E2E_K8S_PORT_FORWARD=0 \
	"/usr/local/go/bin/go" run "$ROOT/scripts/go/rtk-cloud" -- staging-acceptance \
	--workspace "$WORKSPACE" \
	--env-root "$ENV_ROOT" \
	--confirm video-cloud-staging \
	--brandname RTK \
	--user-count 1 \
	--device-count 3 \
	--device-mix camera=1,light=1,smart_meter=1 \
	--steps data,mqtt,runtime-logs,billing-log,billing-db \
	--skip-mqtt-probe > "$ACCEPTANCE_RUN_OUT"
expected_acceptance=$'setup-data\nmqtt-test\nmqtt-log-verify\nbilling-verify'
actual_acceptance="$(cut -f1 "$COMMAND_LOG")"
[[ "$actual_acceptance" == "$expected_acceptance" ]] || {
	printf 'unexpected acceptance command order:\n%s\n' "$actual_acceptance" >&2
	exit 1
}

: > "$COMMAND_LOG"
PLAN_OUT="$TMP/plan.out"
CLOUD_STAGING_E2E_REMOVE_K8S_SCRIPT="$TMP/remove-k8s.sh" \
CLOUD_STAGING_E2E_PROVISION_K8S_SCRIPT="$TMP/provision-k8s.sh" \
CLOUD_STAGING_E2E_DATA_SETUP_SCRIPT="$TMP/setup-data.sh" \
CLOUD_STAGING_E2E_MQTT_TEST_SCRIPT="$TMP/mqtt-test.sh" \
CLOUD_STAGING_E2E_MQTT_LOG_VERIFY_SCRIPT="$TMP/mqtt-log-verify.sh" \
	"/usr/local/go/bin/go" run "$ROOT/scripts/go/rtk-cloud" -- staging-e2e-test \
	--workspace "$WORKSPACE" \
	--env-root "$ENV_ROOT" \
	--plan > "$PLAN_OUT"

grep -F 'cloud-environment-e2e-test plan' "$PLAN_OUT" >/dev/null
grep -F 'target: k8s' "$PLAN_OUT" >/dev/null
grep -F 'reset environment K8s with '"$TMP/remove-k8s.sh" "$PLAN_OUT" >/dev/null
grep -F 'provision environment K8s with '"$TMP/provision-k8s.sh" "$PLAN_OUT" >/dev/null
test ! -s "$COMMAND_LOG"

RUN_OUT="$TMP/run.out"
RUN_ERR="$TMP/run.err"
CLOUD_STAGING_E2E_REMOVE_K8S_SCRIPT="$TMP/remove-k8s.sh" \
CLOUD_STAGING_E2E_PROVISION_K8S_SCRIPT="$TMP/provision-k8s.sh" \
CLOUD_STAGING_E2E_DATA_SETUP_SCRIPT="$TMP/setup-data.sh" \
CLOUD_STAGING_E2E_MQTT_TEST_SCRIPT="$TMP/mqtt-test.sh" \
CLOUD_STAGING_E2E_MQTT_LOG_VERIFY_SCRIPT="$TMP/mqtt-log-verify.sh" \
CLOUD_STAGING_E2E_BILLING_VERIFY_SCRIPT="$TMP/billing-verify.sh" \
CLOUD_STAGING_E2E_PROGRESS_INTERVAL=100ms \
CLOUD_STAGING_E2E_K8S_PORT_FORWARD=0 \
	"/usr/local/go/bin/go" run "$ROOT/scripts/go/rtk-cloud" -- staging-e2e-test \
	--workspace "$WORKSPACE" \
	--env-root "$ENV_ROOT" \
	--run \
	--confirm video-cloud-staging \
	--brandname RTK \
	--user-count 1 \
	--device-count 3 \
	--device-mix camera=1,light=1,smart_meter=1 \
	--steps "$CONTRACT_STEPS" \
	--skip-mqtt-probe > "$RUN_OUT" 2> "$RUN_ERR"

expected=$'remove-k8s\nprovision-k8s\nsetup-data\nmqtt-test\nmqtt-log-verify\nbilling-verify'
actual="$(cut -f1 "$COMMAND_LOG")"
[[ "$actual" == "$expected" ]] || {
	printf 'unexpected command order:\n%s\n' "$actual" >&2
	exit 1
}
grep -F $'remove-k8s\t' "$COMMAND_LOG" >/dev/null
grep -F $'provision-k8s\t' "$COMMAND_LOG" >/dev/null
grep -F $'setup-data\t' "$COMMAND_LOG" | grep -F -- "--env-root $WORKSPACE/cloud_env/staging/runtime" | grep -F -- '--brandname RTK' | grep -F -- '--user-count 1' | grep -F -- '--device-count 3' | grep -F -- '--device-mix camera=1,light=1,smart_meter=1' | grep -F -- '--user-concurrency 64' | grep -F -- '--device-concurrency 64' | grep -F -- '--bind-concurrency 64' | grep -F -- '--out-dir ' >/dev/null
grep -F $'setup-data\t' "$COMMAND_LOG" | grep -F -- '--no-resume' >/dev/null
if grep -E '(^|[[:space:]])(remove-all-vm|provision|deploy|remove_vm|provision_all)([[:space:]]|$)' "$COMMAND_LOG" >/dev/null; then
	echo "staging-e2e-test should not invoke retired VM runtime commands" >&2
	exit 1
fi
for step in reset_k8s provision_k8s setup_brand_devices cloud_mqtt_test verify_mqtt_logs verify_billing; do
	grep -E "\\[cloud-staging-e2e\\] pass: ${step} duration_seconds=[0-9]+" "$RUN_ERR" >/dev/null
done
grep -E "\\[cloud-staging-e2e\\] start: provision_k8s log=.*/logs/provision_k8s.log" "$RUN_ERR" >/dev/null

SUMMARY="$(jq -r '.summary_file' "$RUN_OUT")"
REPORT="$(jq -r '.report_file' "$RUN_OUT")"
test -f "$SUMMARY"
test -f "$REPORT"
jq -e '.overall == "pass" and .target == "k8s" and (.steps | length == 6) and .artifacts.data_setup_summary_file != "" and .artifacts.bind_validation_dir != "" and .artifacts.mqtt_log_verify_summary_file != "" and .artifacts.billing_verify_summary_file != ""' "$SUMMARY" >/dev/null
jq -e '.steps[] | select(.name == "setup_brand_devices")' "$SUMMARY" >/dev/null
grep -F 'Staging E2E Test Report' "$REPORT" >/dev/null
grep -F 'Data setup summary' "$REPORT" >/dev/null
grep -F 'cloud_mqtt_test' "$REPORT" >/dev/null
grep -F 'verify_mqtt_logs' "$REPORT" >/dev/null
grep -F 'verify_billing' "$REPORT" >/dev/null
if grep -R -Ei 'super-secret|password|bearer|token|PRIVATE KEY|-----BEGIN' "$SUMMARY" "$REPORT" >/dev/null; then
	echo "orchestrator reports must be redacted" >&2
	exit 1
fi

: > "$COMMAND_LOG"
QUIET_OUT="$TMP/quiet.out"
QUIET_ERR="$TMP/quiet.err"
CLOUD_STAGING_E2E_REMOVE_K8S_SCRIPT="$TMP/remove-k8s.sh" \
CLOUD_STAGING_E2E_PROVISION_K8S_SCRIPT="$TMP/provision-k8s.sh" \
CLOUD_STAGING_E2E_DATA_SETUP_SCRIPT="$TMP/setup-data.sh" \
CLOUD_STAGING_E2E_MQTT_TEST_SCRIPT="$TMP/mqtt-test.sh" \
CLOUD_STAGING_E2E_MQTT_LOG_VERIFY_SCRIPT="$TMP/mqtt-log-verify.sh" \
CLOUD_STAGING_E2E_BILLING_VERIFY_SCRIPT="$TMP/billing-verify.sh" \
CLOUD_STAGING_E2E_PROGRESS_INTERVAL=100ms \
CLOUD_STAGING_E2E_K8S_PORT_FORWARD=0 \
	"/usr/local/go/bin/go" run "$ROOT/scripts/go/rtk-cloud" -- staging-e2e-test \
	--workspace "$WORKSPACE" \
	--env-root "$ENV_ROOT" \
	--run \
	--confirm video-cloud-staging \
	--brandname RTK \
	--user-count 1 \
	--device-count 3 \
	--device-mix camera=1,light=1,smart_meter=1 \
	--steps "$CONTRACT_STEPS" \
	--skip-mqtt-probe \
	--quiet > "$QUIET_OUT" 2> "$QUIET_ERR"

grep -F $'setup-data\t' "$COMMAND_LOG" | grep -F -- "--env-root $WORKSPACE/cloud_env/staging/runtime" | grep -F -- '--brandname RTK' | grep -F -- '--user-count 1' | grep -F -- '--device-count 3' | grep -F -- '--device-mix camera=1,light=1,smart_meter=1' | grep -F -- '--quiet' >/dev/null
grep -F $'setup-data\t' "$COMMAND_LOG" | grep -F -- '--no-resume' >/dev/null
grep -E "\\[cloud-staging-e2e\\] start: provision_k8s log=.*/logs/provision_k8s.log" "$QUIET_ERR" >/dev/null
if grep -F '[cloud-staging-e2e] progress:' "$QUIET_ERR" >/dev/null; then
	echo "quiet staging e2e should not print progress lines" >&2
	exit 1
fi

LKE_ENV_ROOT="$WORKSPACE/cloud_env/staging/runtime"
mkdir -p "$LKE_ENV_ROOT/env"
cat > "$LKE_ENV_ROOT/env/stack.env" <<'EOF_LKE_ENV'
CLOUD_ENV_NAME=staging
CLOUD_PROVIDER=lke
CLOUD_REGION=us-sea
CLOUD_DNS_ROOT_DOMAIN=realtekconnect.com
CLOUD_STACK_NAME=video-cloud-staging
EOF_LKE_ENV
: > "$COMMAND_LOG"
LKE_OUT="$TMP/lke.out"
LKE_ERR="$TMP/lke.err"
CLOUD_STAGING_E2E_REMOVE_SCRIPT="$TMP/remove.sh" \
CLOUD_STAGING_E2E_PROVISION_SCRIPT="$TMP/provision.sh" \
CLOUD_STAGING_E2E_DATA_SETUP_SCRIPT="$TMP/setup-data.sh" \
CLOUD_STAGING_E2E_MQTT_TEST_SCRIPT="$TMP/mqtt-test.sh" \
CLOUD_STAGING_E2E_MQTT_LOG_VERIFY_SCRIPT="$TMP/mqtt-log-verify.sh" \
CLOUD_STAGING_E2E_BILLING_VERIFY_SCRIPT="$TMP/billing-verify.sh" \
CLOUD_STAGING_E2E_K8S_PORT_FORWARD=0 \
	"/usr/local/go/bin/go" run "$ROOT/scripts/go/rtk-cloud" -- staging-e2e-test \
	--workspace "$WORKSPACE" \
	--env-root "$ENV_ROOT" \
	--run \
	--confirm video-cloud-staging \
	--brandname RTK \
	--user-count 1 \
	--device-count 3 \
	--device-mix camera=1,light=1,smart_meter=1 \
	--steps "$CONTRACT_STEPS" \
	--skip-mqtt-probe > "$LKE_OUT" 2> "$LKE_ERR"

grep -F $'remove\t--workspace '"$WORKSPACE"$' --env-root '"$LKE_ENV_ROOT"$' --yes' "$COMMAND_LOG" >/dev/null
grep -F $'provision\t--workspace '"$WORKSPACE"$' --env-root '"$LKE_ENV_ROOT"$' --all --confirm video-cloud-staging' "$COMMAND_LOG" >/dev/null
if grep -F 'staging certificate cache' "$LKE_ERR" >/dev/null; then
	echo "LKE staging e2e should not require VM certificate caches before remove" >&2
	exit 1
fi

if "/usr/local/go/bin/go" run "$ROOT/scripts/go/rtk-cloud" -- staging-e2e-test \
	--workspace "$WORKSPACE" \
	--env-root "$ENV_ROOT" \
	--run \
	--confirm wrong-stack >/tmp/should-fail.out 2>/tmp/should-fail.err; then
	echo "expected wrong confirm to fail" >&2
	exit 1
fi
grep -F 'does not match CLOUD_STACK_NAME=video-cloud-staging' /tmp/should-fail.err >/dev/null
