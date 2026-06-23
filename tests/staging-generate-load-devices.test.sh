#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

OUT="$TMP/devices"
"/usr/local/go/bin/go" run "$ROOT/scripts/go/rtk-cloud" -- generate-load-devices \
	--env-root "$TMP/cloud_env/staging" \
	--out-dir "$OUT" \
	--count 7 \
	--mix camera=2,light=2,air_conditioner=2,smart_meter=1 \
	--prefix test-load \
	--generate-only >/tmp/staging-generate-load-devices.out 2>/tmp/staging-generate-load-devices.err

jq -e '.count == 7' "$OUT/summary.json" >/dev/null
jq -e '.enrollment.mode == "generate_only"' "$OUT/summary.json" >/dev/null
jq -e '.enrollment.succeeded == 7' "$OUT/summary.json" >/dev/null
jq -e '.enrollment.failed == 0' "$OUT/summary.json" >/dev/null
jq -e '.allocated.camera == 2' "$OUT/summary.json" >/dev/null
jq -e '.allocated.light == 2' "$OUT/summary.json" >/dev/null
jq -e '.allocated.air_conditioner == 2' "$OUT/summary.json" >/dev/null
jq -e '.allocated.smart_meter == 1' "$OUT/summary.json" >/dev/null
INSPECT="$TMP/inspect.json"
"/usr/local/go/bin/go" run "$ROOT/scripts/go/rtk-cloud" -- test-data inspect \
	--env-root "$TMP/cloud_env/staging" \
	--brandname RTK >"$INSPECT"
jq -e '.schema == "rtk-cloud-workspace-test-data/v1" and .devices == 7' "$INSPECT" >/dev/null

test -f "$OUT/ca/sim-device-ca.cert.pem"
grep -F 'VIDEO_CLOUD_LOAD_DEVICE_IDS' "$OUT/loadtest.env" >/dev/null
grep -F 'test-load-0001,test-load-0002,test-load-0003,test-load-0004,test-load-0005,test-load-0006,test-load-0007' "$OUT/loadtest.env" >/dev/null
for legacy in \
	"$OUT/manifests/devices.json" \
	"$OUT/manifests/devices.csv" \
	"$OUT/manifests/factory-enroll-results.jsonl" \
	"$OUT/devices/camera/test-load-0001/device.key.pem" \
	"$OUT/devices/camera/test-load-0001/device.cert.pem" \
	"$OUT/devices/light/test-load-0003/metadata.json"
do
	if [[ -e "$legacy" ]]; then
		printf 'legacy test-data file should be cleaned: %s\n' "$legacy" >&2
		exit 1
	fi
done
grep -F 'device generation progress: done=7/7 generated=7 failed=0' /tmp/staging-generate-load-devices.err >/dev/null

if "/usr/local/go/bin/go" run "$ROOT/scripts/go/rtk-cloud" -- generate-load-devices --out-dir "$TMP/missing-env-root" >/tmp/missing-env-root.out 2>/tmp/missing-env-root.err; then
	printf 'expected missing --env-root to fail\n' >&2
	exit 1
fi
grep -F -- '--env-root is required' /tmp/missing-env-root.err >/dev/null

if "/usr/local/go/bin/go" run "$ROOT/scripts/go/rtk-cloud" -- generate-load-devices --env-root "$TMP/cloud_env/staging" --out-dir "$TMP/bad" --mix camera=1,sensor=1 --generate-only >/tmp/bad.out 2>/tmp/bad.err; then
	printf 'expected unsupported mix type to fail\n' >&2
	exit 1
fi
grep -F 'unsupported device type' /tmp/bad.err >/dev/null

FACTORY_CA="$TMP/factory-ca"
mkdir -p "$FACTORY_CA"
openssl ecparam -name prime256v1 -genkey -noout -out "$FACTORY_CA/ca.key.pem"
openssl req -x509 -new -sha256 -key "$FACTORY_CA/ca.key.pem" -days 30 -subj "/CN=Test Factory CA" -out "$FACTORY_CA/ca.cert.pem"

FACTORY_LOG="$TMP/factory-enroll-requests.jsonl"
PORT_FILE="$TMP/factory-enroll-port.txt"
go run "$ROOT/tests/helpers/factory_enroll_mock.go" "$FACTORY_LOG" "$PORT_FILE" "$FACTORY_CA/ca.cert.pem" "$FACTORY_CA/ca.key.pem" &
FACTORY_PID=$!
cleanup() {
	if [[ -n "${FACTORY_PID:-}" ]]; then
		kill "$FACTORY_PID" 2>/dev/null || true
		wait "$FACTORY_PID" 2>/dev/null || true
	fi
	rm -rf "$TMP"
}
for _ in $(seq 1 50); do
	[[ -s "$PORT_FILE" ]] && break
	sleep 0.1
done
trap cleanup EXIT
FACTORY_PORT="$(cat "$PORT_FILE")"
FACTORY_OUT="$TMP/factory-devices"
FACTORY_ENV_ROOT="$TMP/factory_env"
mkdir -p "$FACTORY_ENV_ROOT/env" "$FACTORY_ENV_ROOT/services/video-cloud"
{
	printf 'CLOUD_ENV_NAME=test\n'
	printf 'CLOUD_PROVIDER=lke\n'
	printf 'CLOUD_REGION=us-sea\n'
	printf 'CLOUD_DNS_ROOT_DOMAIN=example.test\n'
	printf 'CLOUD_STACK_NAME=video-cloud-test\n'
	printf 'VIDEO_CLOUD_DOMAIN=video-cloud-test.example.test\n'
	printf 'VIDEO_CLOUD_CERTISSUER_DOMAIN=certissuer.video-cloud-test.example.test\n'
	printf 'ACCOUNT_MANAGER_DOMAIN=account-manager.video-cloud-test.example.test\n'
	printf 'CLOUD_ADMIN_DOMAIN=admin.video-cloud-test.example.test\n'
	printf 'VIDEO_CLOUD_LABEL_PREFIX=video-cloud-test\n'
	printf 'VIDEO_CLOUD_VPC_LABEL=video-cloud-test-vpc\n'
	printf 'VIDEO_CLOUD_SUBNET_LABEL=video-cloud-test-subnet\n'
	printf 'ACCOUNT_MANAGER_LINODE_LABEL=rtk-account-manager-test\n'
	printf 'ACCOUNT_MANAGER_LINODE_FIREWALL_LABEL=rtk-account-manager-test-fw\n'
	printf 'ADMIN_LINODE_LABEL=rtk-cloud-admin-test\n'
	printf 'ADMIN_LINODE_FIREWALL_LABEL=rtk-cloud-admin-test-fw\n'
} > "$FACTORY_ENV_ROOT/env/stack.env"
{
	printf 'FACTORY_ENROLL_URL=http://127.0.0.1:%s\n' "$FACTORY_PORT"
	printf 'FACTORY_ENROLL_AUTH_KEY=test-secret\n'
} > "$FACTORY_ENV_ROOT/services/video-cloud/video-cloud.env"
FACTORY_ENROLL_RUN_ID="test-run" \
"/usr/local/go/bin/go" run "$ROOT/scripts/go/rtk-cloud" -- generate-load-devices \
	--env-root "$FACTORY_ENV_ROOT" \
	--out-dir "$FACTORY_OUT" \
	--count 2 \
	--mix camera=1,light=1 \
	--prefix factory-load >/tmp/staging-factory-enroll.out 2>/tmp/staging-factory-enroll.err

grep -F 'enroll start: index=001 device=factory-load-0001 type=camera service_options=mqtt,video_streaming,video_storage' /tmp/staging-factory-enroll.err >/dev/null
grep -F 'enroll ok: index=002 device=factory-load-0002 type=light status=200' /tmp/staging-factory-enroll.err >/dev/null
jq -s -e '
  map(select(.ok == true)) as $ok |
  any($ok[]; .devid == "factory-load-0001" and .service_options == ["mqtt","video_streaming","video_storage"]) and
  any($ok[]; .devid == "factory-load-0002" and .service_options == ["mqtt"])
' "$FACTORY_LOG" >/dev/null
jq -e '.enrollment.mode == "factory_enroll" and .enrollment.succeeded == 2 and .enrollment.failed == 0' "$FACTORY_OUT/summary.json" >/dev/null
FACTORY_INSPECT="$TMP/factory-inspect.json"
"/usr/local/go/bin/go" run "$ROOT/scripts/go/rtk-cloud" -- test-data inspect \
	--env-root "$FACTORY_ENV_ROOT" \
	--brandname RTK >"$FACTORY_INSPECT"
jq -e '.schema == "rtk-cloud-workspace-test-data/v1" and .devices == 2' "$FACTORY_INSPECT" >/dev/null
for legacy in \
	"$FACTORY_OUT/manifests/factory-enroll-results.jsonl" \
	"$FACTORY_OUT/manifests/devices.json" \
	"$FACTORY_OUT/devices/camera/factory-load-0001/factory-enroll-response.redacted.json" \
	"$FACTORY_OUT/devices/camera/factory-load-0001/device.cert.pem"
do
	if [[ -e "$legacy" ]]; then
		printf 'legacy test-data file should be cleaned: %s\n' "$legacy" >&2
		exit 1
	fi
done
