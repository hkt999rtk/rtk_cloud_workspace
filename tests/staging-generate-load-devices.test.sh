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
	--prefix test-load \
	--generate-only >/tmp/staging-generate-load-devices.out

jq -e '.count == 7' "$OUT/summary.json" >/dev/null
jq -e '.enrollment.mode == "generate_only"' "$OUT/summary.json" >/dev/null
jq -e '.enrollment.succeeded == 7' "$OUT/summary.json" >/dev/null
jq -e '.enrollment.failed == 0' "$OUT/summary.json" >/dev/null
jq -e '.allocated.camera == 2' "$OUT/summary.json" >/dev/null
jq -e '.allocated.light == 2' "$OUT/summary.json" >/dev/null
jq -e '.allocated.air_conditioner == 2' "$OUT/summary.json" >/dev/null
jq -e '.allocated.smart_meter == 1' "$OUT/summary.json" >/dev/null
jq -e 'length == 7' "$OUT/manifests/devices.json" >/dev/null
jq -e '.[0].service_options == ["mqtt","video_streaming","video_storage"]' "$OUT/manifests/devices.json" >/dev/null
jq -e '.[2].service_options == ["mqtt"]' "$OUT/manifests/devices.json" >/dev/null

test -f "$OUT/ca/sim-device-ca.cert.pem"
test -f "$OUT/devices/camera/test-load-0001/device.key.pem"
test -f "$OUT/devices/camera/test-load-0001/device.cert.pem"
test -f "$OUT/devices/camera/test-load-0001/device.chain.pem"
test -f "$OUT/devices/light/test-load-0003/metadata.json"
test -f "$OUT/devices/air_conditioner/test-load-0005/metadata.json"
test -f "$OUT/devices/smart_meter/test-load-0007/metadata.json"
grep -F 'VIDEO_CLOUD_LOAD_DEVICE_IDS' "$OUT/loadtest.env" >/dev/null
grep -F 'test-load-0001,test-load-0002,test-load-0003,test-load-0004,test-load-0005,test-load-0006,test-load-0007' "$OUT/loadtest.env" >/dev/null
grep -F 'device_id,device_type,mqtt_capability,service_options,model,certificate_path,key_path,bundle_path' "$OUT/manifests/devices.csv" >/dev/null
test -f "$OUT/manifests/factory-enroll-results.jsonl"

openssl x509 -in "$OUT/devices/camera/test-load-0001/device.cert.pem" -noout -subject | grep -F 'test-load-0001' >/dev/null

if "$ROOT/scripts/cloud-generate-load-devices.sh" --out-dir "$TMP/missing-env-root" >/tmp/missing-env-root.out 2>/tmp/missing-env-root.err; then
	printf 'expected missing --env-root to fail\n' >&2
	exit 1
fi
grep -F -- '--env-root is required' /tmp/missing-env-root.err >/dev/null

if "$ROOT/scripts/cloud-generate-load-devices.sh" --env-root "$TMP/cloud_env/staging" --out-dir "$TMP/bad" --mix camera=1,sensor=1 --generate-only >/tmp/bad.out 2>/tmp/bad.err; then
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
python3 - "$FACTORY_LOG" "$PORT_FILE" "$FACTORY_CA/ca.cert.pem" "$FACTORY_CA/ca.key.pem" <<'PY' &
import hashlib
import hmac
import http.server
import json
import pathlib
import subprocess
import sys
import tempfile

log_path = pathlib.Path(sys.argv[1])
port_path = pathlib.Path(sys.argv[2])
ca_cert = pathlib.Path(sys.argv[3])
ca_key = pathlib.Path(sys.argv[4])
auth_key = b"test-secret"

class Handler(http.server.BaseHTTPRequestHandler):
    def do_POST(self):
        length = int(self.headers.get("content-length", "0"))
        body = self.rfile.read(length)
        request_id = self.headers.get("X-Video-Cloud-Request-ID", "")
        timestamp = self.headers.get("X-Video-Cloud-Timestamp", "")
        body_hash = hashlib.sha256(body).hexdigest()
        canonical = "\n".join(["POST", self.path, timestamp, request_id, body_hash])
        expected = "v1=" + hmac.new(auth_key, canonical.encode(), hashlib.sha256).hexdigest()
        payload = json.loads(body)
        ok = (
            self.path == "/v1/factory/enroll"
            and self.headers.get("X-Video-Cloud-Signature") == expected
            and "allowed_services" not in payload
            and isinstance(payload.get("service_options"), list)
            and payload.get("metadata", {}).get("service_options") == payload.get("service_options")
        )
        log_path.open("a").write(json.dumps({
            "ok": ok,
            "devid": payload.get("devid"),
            "service_options": payload.get("service_options"),
            "metadata_service_options": payload.get("metadata", {}).get("service_options"),
        }) + "\n")
        if not ok:
            self.send_response(400)
            self.end_headers()
            self.wfile.write(b'{"error":"bad factory enroll request"}')
            return
        with tempfile.TemporaryDirectory() as tmp:
            tmp_path = pathlib.Path(tmp)
            csr_path = tmp_path / "device.csr.pem"
            cert_path = tmp_path / "device.cert.pem"
            csr_path.write_text(payload["csr_pem"])
            subprocess.run([
                "openssl", "x509", "-req", "-sha256",
                "-in", str(csr_path),
                "-CA", str(ca_cert),
                "-CAkey", str(ca_key),
                "-CAcreateserial",
                "-days", "30",
                "-out", str(cert_path),
            ], check=True, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
            cert_pem = cert_path.read_text()
        response = {
            "request_id": request_id,
            "device_id": payload["devid"],
            "serial_number": payload["serial_number"],
            "certificate_pem": cert_pem,
            "certificate_chain_pem": ca_cert.read_text(),
        }
        data = json.dumps(response).encode()
        self.send_response(200)
        self.send_header("content-type", "application/json")
        self.send_header("content-length", str(len(data)))
        self.end_headers()
        self.wfile.write(data)

    def log_message(self, *args):
        pass

server = http.server.HTTPServer(("127.0.0.1", 0), Handler)
port_path.write_text(str(server.server_port))
server.serve_forever()
PY
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
	printf 'CLOUD_PROVIDER=linode\n'
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
"$ROOT/scripts/cloud-generate-load-devices.sh" \
	--env-root "$FACTORY_ENV_ROOT" \
	--out-dir "$FACTORY_OUT" \
	--count 2 \
	--mix camera=1,light=1 \
	--prefix factory-load >/tmp/staging-factory-enroll.out 2>/tmp/staging-factory-enroll.err

grep -F 'enroll start: index=001 device=factory-load-0001 type=camera service_options=mqtt,video_streaming,video_storage' /tmp/staging-factory-enroll.err >/dev/null
grep -F 'enroll ok: index=002 device=factory-load-0002 type=light status=200' /tmp/staging-factory-enroll.err >/dev/null
jq -s -e 'length == 2 and all(.ok == true) and .[0].service_options == ["mqtt","video_streaming","video_storage"] and .[1].service_options == ["mqtt"]' "$FACTORY_LOG" >/dev/null
jq -s -e 'length == 2 and all(.status == "ok")' "$FACTORY_OUT/manifests/factory-enroll-results.jsonl" >/dev/null
jq -e '.enrollment.mode == "factory_enroll" and .enrollment.succeeded == 2 and .enrollment.failed == 0' "$FACTORY_OUT/summary.json" >/dev/null
jq -e 'length == 2 and .[0].certificate_profile == "factory-enrolled-device-mtls-client"' "$FACTORY_OUT/manifests/devices.json" >/dev/null
test -f "$FACTORY_OUT/devices/camera/factory-load-0001/factory-enroll-response.redacted.json"
openssl x509 -in "$FACTORY_OUT/devices/camera/factory-load-0001/device.cert.pem" -noout -subject >/dev/null
