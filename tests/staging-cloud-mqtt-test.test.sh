#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

ENV_ROOT="$TMP/cloud_env/staging/linode"
mkdir -p \
	"$ENV_ROOT/env" \
	"$ENV_ROOT/services/account-manager" \
	"$ENV_ROOT/services/video-cloud" \
	"$ENV_ROOT/state" \
	"$ENV_ROOT/artifacts/users" \
	"$ENV_ROOT/artifacts/device-bind" \
	"$ENV_ROOT/devices/test_device/manifests" \
	"$ENV_ROOT/devices/test_device/devices/light/light-001" \
	"$ENV_ROOT/devices/test_device/devices/air_conditioner/ac-001" \
	"$ENV_ROOT/devices/test_device/devices/smart_meter/meter-001"

cat > "$ENV_ROOT/env/stack.env" <<'EOF_ENV'
CLOUD_ENV_NAME=staging
CLOUD_PROVIDER=linode
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

cat > "$ENV_ROOT/services/account-manager/account-manager-public-staging.env" <<'EOF_AM'
ACCOUNT_MANAGER_LINODE_DOMAIN=account-manager.video-cloud-staging.realtekconnect.com
JWT_ACCESS_SECRET=test-secret
EOF_AM

cat > "$ENV_ROOT/services/video-cloud/video-cloud-staging.env" <<'EOF_VIDEO'
VIDEO_CLOUD_AUTH_SECRET=test-secret
EOF_VIDEO

cat > "$ENV_ROOT/state/video-cloud-staging.state.json" <<'EOF_STATE'
{"instances":{"mqtt":{"public_ipv4":"127.0.0.1","private_ip":"10.42.1.40"}}}
EOF_STATE
touch "$ENV_ROOT/state/account-manager-staging.env"
touch "$ENV_ROOT/devices/test_device/loadtest.env"
printf 'light-001\nac-001\nmeter-001\n' > "$ENV_ROOT/devices/test_device/manifests/device_ids.txt"

for path in \
	"$ENV_ROOT/devices/test_device/devices/light/light-001" \
	"$ENV_ROOT/devices/test_device/devices/air_conditioner/ac-001" \
	"$ENV_ROOT/devices/test_device/devices/smart_meter/meter-001"
do
	printf 'cert\n' > "$path/device.cert.pem"
	printf 'key\n' > "$path/device.key.pem"
	printf 'chain\n' > "$path/device.chain.pem"
done

jq -n '[
	{
		device_id: "light-001",
		device_type: "light",
		service_options: ["mqtt"],
		certificate_path: "devices/light/light-001/device.cert.pem",
		certificate_chain_path: "devices/light/light-001/device.chain.pem",
		key_path: "devices/light/light-001/device.key.pem"
	},
	{
		device_id: "ac-001",
		device_type: "air_conditioner",
		service_options: ["mqtt"],
		certificate_path: "devices/air_conditioner/ac-001/device.cert.pem",
		certificate_chain_path: "devices/air_conditioner/ac-001/device.chain.pem",
		key_path: "devices/air_conditioner/ac-001/device.key.pem"
	},
	{
		device_id: "meter-001",
		device_type: "smart_meter",
		service_options: ["mqtt"],
		certificate_path: "devices/smart_meter/meter-001/device.cert.pem",
		certificate_chain_path: "devices/smart_meter/meter-001/device.chain.pem",
		key_path: "devices/smart_meter/meter-001/device.key.pem"
	}
]' > "$ENV_ROOT/devices/test_device/manifests/devices.json"

jq -n '{
	brandname: "RTK",
	brand_cloud_id: "brand-1",
	role: "user",
	users: [
		{email: "rtk+001@users.local", display_name: "User 1", role: "user", password: "secret", action: "created"}
	]
}' > "$ENV_ROOT/artifacts/users/rtk-users-20260531T000000Z.json"
chmod 600 "$ENV_ROOT/artifacts/users/rtk-users-20260531T000000Z.json"

jq -n '{
	schema: "rtk-cloud-workspace.bulk-device-bind/v1",
	generated_at: "2026-05-31T00:00:00Z",
	brandname: "RTK",
	brand_cloud_id: "brand-1",
	count: 3,
	assignments: [
		{assignment_index: 1, assigned_email: "rtk+001@users.local", device_id: "light-001", device_type: "light", category: "mqtt_device", service_options: ["mqtt"], account_device_id: "account-light", status: "provision_requested"},
		{assignment_index: 2, assigned_email: "rtk+001@users.local", device_id: "ac-001", device_type: "air_conditioner", category: "mqtt_device", service_options: ["mqtt"], account_device_id: "account-ac", status: "provision_requested"},
		{assignment_index: 3, assigned_email: "rtk+001@users.local", device_id: "meter-001", device_type: "smart_meter", category: "mqtt_device", service_options: ["mqtt"], account_device_id: "account-meter", status: "provision_requested"}
	]
}' > "$ENV_ROOT/artifacts/device-bind/rtk-device-bind-20260531T000000Z.json"

OUT="$TMP/out.json"
"$ROOT/scripts/cloud_mqtt_test.sh" \
	--env-root "$TMP/cloud_env/staging" \
	--brandname RTK \
	--out-dir "$TMP/report" \
	--seed 7 \
	--no-mqtt-probe > "$OUT"

jq -e '.overall == "pass" and .status == "PASS"' "$OUT" >/dev/null
RESULTS="$(jq -r '.results_file' "$OUT")"
REPORT="$(jq -r '.report_file' "$OUT")"
jq -e '.overall == "pass" and .metrics.success_rate_percent >= 95 and (.negative_checks | length == 4)' "$RESULTS" >/dev/null
jq -e '.mqtt.probe_result == "NOT_RUN" and (.out_of_scope | index("webrtc"))' "$RESULTS" >/dev/null
grep -F 'Home MQTT Load-Test Report' "$REPORT" >/dev/null
grep -F 'cross_user_device_access' "$REPORT" >/dev/null
if grep -Ei 'secret|password|bearer|BEGIN|device.key.pem' "$REPORT" "$RESULTS" >/dev/null; then
	echo "report output must be redacted" >&2
	exit 1
fi

rm "$ENV_ROOT/devices/test_device/devices/light/light-001/device.key.pem"
if "$ROOT/scripts/cloud_mqtt_test.sh" \
	--env-root "$TMP/cloud_env/staging" \
	--brandname RTK \
	--out-dir "$TMP/blocked" > "$TMP/blocked.out"; then
	echo "expected missing key to block" >&2
	exit 1
fi
jq -e '.overall == "blocked" and .status == "BLOCKED"' "$TMP/blocked.out" >/dev/null
grep -F 'missing key file' "$TMP/blocked/TEST_REPORT.md" >/dev/null
if grep -Ei 'secret|password|bearer|BEGIN' "$TMP/blocked/TEST_REPORT.md" "$TMP/blocked/results.json" >/dev/null; then
	echo "blocked report output must be redacted" >&2
	exit 1
fi
