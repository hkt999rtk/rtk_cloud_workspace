#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

ARTIFACT="$TMP/bind-artifact.json"
REPORT_DIR="$TMP/report"
ENV_ROOT="$TMP/runtime"
mkdir -p "$ENV_ROOT"

jq -n '{
	schema: "rtk-cloud-workspace.bulk-device-bind/v1",
	generated_at: "2026-05-30T00:00:00Z",
	brandname: "RTK",
	brand_cloud_id: "org-rtk",
	count: 2,
	assignments: [
		{
			assignment_index: 0,
			assigned_email: "rtk+001@users.local",
			device_id: "load-device-0001",
			device_type: "camera",
			category: "ip_camera",
			service_options: ["mqtt", "video_streaming", "video_storage"],
			claim_id: "claim-load-device-0001",
			account_device_id: "account-load-device-0001",
			operation_id: "op-load-device-0001",
			status: "provision_requested"
		},
		{
			assignment_index: 1,
			assigned_email: "rtk+002@users.local",
			device_id: "load-device-0002",
			device_type: "light",
			category: "mqtt_device",
			service_options: ["mqtt"],
			claim_id: "claim-load-device-0002",
			account_device_id: "account-load-device-0002",
			operation_id: "op-load-device-0002",
			status: "provision_requested"
		}
	]
}' > "$ARTIFACT"

if "/usr/local/go/bin/go" run "$ROOT/scripts/go/rtk-cloud" -- validate-device-bind --env-root "$ENV_ROOT" --out-dir "$REPORT_DIR" --expected-count 2 >"$TMP/missing.out" 2>/dev/null; then
	echo "expected empty environment test data to fail count validation" >&2
	exit 1
fi
jq -e '.overall == "fail" and .failure_categories.count_mismatch == 1' "$TMP/missing.out" >/dev/null

OUT="$TMP/out.json"
"/usr/local/go/bin/go" run "$ROOT/scripts/go/rtk-cloud" -- validate-device-bind \
	--env-root "$ENV_ROOT" \
	--bind-artifact "$ARTIFACT" \
	--out-dir "$REPORT_DIR" \
	--expected-count 2 \
	--expected-devices-per-user 1 >"$OUT"

if grep -Ei 'password|bearer|raw-token|device.key' "$OUT" >/dev/null; then
	echo "stdout must not include secrets" >&2
	exit 1
fi
jq -e '.action == "validated" and .overall == "pass" and .total_devices == 2' "$OUT" >/dev/null
RESULTS="$(jq -r '.results_file' "$OUT")"
REPORT="$(jq -r '.report_file' "$OUT")"
test -f "$RESULTS"
test -f "$REPORT"
jq -e '.overall == "pass" and .summary.total_devices == 2 and (.user_counts | length == 2)' "$RESULTS" >/dev/null
grep -F 'Bulk Device Bind Validation Report' "$REPORT" >/dev/null
grep -F 'MQTT-only devices' "$REPORT" >/dev/null
if grep -Ei 'password|bearer|raw-token|device.key' "$RESULTS" "$REPORT" >/dev/null; then
	echo "reports must not include secrets" >&2
	exit 1
fi

BAD="$TMP/bad-artifact.json"
jq '.assignments[1].service_options = ["mqtt", "video_storage"]' "$ARTIFACT" > "$BAD"
if "/usr/local/go/bin/go" run "$ROOT/scripts/go/rtk-cloud" -- validate-device-bind \
	--env-root "$ENV_ROOT" \
	--bind-artifact "$BAD" \
	--out-dir "$TMP/bad-report" \
	--expected-count 2 \
	--expected-devices-per-user 1 >"$TMP/bad.out"; then
	echo "expected mqtt-only video claim validation to fail" >&2
	exit 1
fi
jq -e '.overall == "fail"' "$TMP/bad.out" >/dev/null
