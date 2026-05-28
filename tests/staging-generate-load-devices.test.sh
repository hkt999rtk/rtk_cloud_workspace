#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

OUT="$TMP/devices"
"$ROOT/scripts/cloud-generate-load-devices.sh" \
	--env-root "$TMP/cloud_env/staging" \
	--out-dir "$OUT" \
	--count 7 \
	--mix camera=2,light=2,air_conditioner=2,smart_meter=1 \
	--prefix test-load >/tmp/staging-generate-load-devices.out

jq -e '.count == 7' "$OUT/summary.json" >/dev/null
jq -e '.allocated.camera == 2' "$OUT/summary.json" >/dev/null
jq -e '.allocated.light == 2' "$OUT/summary.json" >/dev/null
jq -e '.allocated.air_conditioner == 2' "$OUT/summary.json" >/dev/null
jq -e '.allocated.smart_meter == 1' "$OUT/summary.json" >/dev/null
jq -e 'length == 7' "$OUT/manifests/devices.json" >/dev/null

test -f "$OUT/ca/sim-device-ca.cert.pem"
test -f "$OUT/devices/camera/test-load-0001/device.key.pem"
test -f "$OUT/devices/camera/test-load-0001/device.cert.pem"
test -f "$OUT/devices/light/test-load-0003/metadata.json"
test -f "$OUT/devices/air_conditioner/test-load-0005/metadata.json"
test -f "$OUT/devices/smart_meter/test-load-0007/metadata.json"
grep -F 'VIDEO_CLOUD_LOAD_DEVICE_IDS' "$OUT/loadtest.env" >/dev/null
grep -F 'test-load-0001,test-load-0002,test-load-0003,test-load-0004,test-load-0005,test-load-0006,test-load-0007' "$OUT/loadtest.env" >/dev/null

openssl x509 -in "$OUT/devices/camera/test-load-0001/device.cert.pem" -noout -subject | grep -F 'test-load-0001' >/dev/null

if "$ROOT/scripts/cloud-generate-load-devices.sh" --out-dir "$TMP/missing-env-root" >/tmp/missing-env-root.out 2>/tmp/missing-env-root.err; then
	printf 'expected missing --env-root to fail\n' >&2
	exit 1
fi
grep -F -- '--env-root is required' /tmp/missing-env-root.err >/dev/null

if "$ROOT/scripts/cloud-generate-load-devices.sh" --env-root "$TMP/cloud_env/staging" --out-dir "$TMP/bad" --mix camera=1,sensor=1 >/tmp/bad.out 2>/tmp/bad.err; then
	printf 'expected unsupported mix type to fail\n' >&2
	exit 1
fi
grep -F 'unsupported device type' /tmp/bad.err >/dev/null
