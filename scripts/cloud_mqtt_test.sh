#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
# shellcheck source=scripts/lib/cloud-env.sh
source "$ROOT/scripts/lib/cloud-env.sh"

usage() {
	cat <<'USAGE'
Usage:
  scripts/cloud_mqtt_test.sh --env-root cloud_env/staging --brandname RTK [options]

Options:
  --env-root PATH          Required environment root. cloud_env/staging resolves to cloud_env/staging/linode.
  --brandname NAME         Required brand name used to discover users and device-bind artifacts.
  --out-dir PATH           Output directory. Default: <env-root>/artifacts/home-mqtt-loadtest/<timestamp>.
  --profile NAME           smoke or real-case. Default: smoke.
  --duration-seconds N     Simulated workload duration for report metadata. Default: 120.
  --max-users N            Limit selected users. Default: 1 for smoke, all for real-case.
  --seed N                 Deterministic workload seed. Default: 20260531.
  --mqtt-probe             Run live MQTT E2E through the broker. Default.
  --no-mqtt-probe          Only validate local artifacts; result is BLOCKED, not PASS.
  --help                   Show this help.

The script never prints passwords, bearer tokens, private keys, certificate bodies, or raw service env values.
USAGE
}

die() {
	printf 'error: %s\n' "$*" >&2
	exit 2
}

ENV_ROOT=""
BRANDNAME=""
OUT_DIR=""
PROFILE="smoke"
DURATION_SECONDS="120"
MAX_USERS=""
SEED="20260531"
MQTT_PROBE="true"

while [[ $# -gt 0 ]]; do
	case "$1" in
	--env-root)
		ENV_ROOT="${2:-}"
		shift 2
		;;
	--brandname)
		BRANDNAME="${2:-}"
		shift 2
		;;
	--out-dir)
		OUT_DIR="${2:-}"
		shift 2
		;;
	--profile)
		PROFILE="${2:-}"
		shift 2
		;;
	--duration-seconds)
		DURATION_SECONDS="${2:-}"
		shift 2
		;;
	--max-users)
		MAX_USERS="${2:-}"
		shift 2
		;;
	--seed)
		SEED="${2:-}"
		shift 2
		;;
	--mqtt-probe)
		MQTT_PROBE="true"
		shift
		;;
	--no-mqtt-probe)
		MQTT_PROBE="false"
		shift
		;;
	--help|-h)
		usage
		exit 0
		;;
	*)
		die "unknown argument: $1"
		;;
	esac
done

[[ -n "$ENV_ROOT" ]] || die "--env-root is required"
[[ -n "$BRANDNAME" ]] || die "--brandname is required"
case "$PROFILE" in
smoke|real-case) ;;
*) die "--profile must be smoke or real-case" ;;
esac
[[ "$DURATION_SECONDS" =~ ^[0-9]+$ ]] || die "--duration-seconds must be an integer"
[[ "$SEED" =~ ^[0-9]+$ ]] || die "--seed must be an integer"
if [[ -n "$MAX_USERS" && ! "$MAX_USERS" =~ ^[0-9]+$ ]]; then
	die "--max-users must be an integer"
fi

ENV_ROOT="$(cloud_env_init "$ROOT" "$ENV_ROOT")"
if [[ -z "$MAX_USERS" && "$PROFILE" == "smoke" ]]; then
	MAX_USERS="1"
fi

if [[ -z "$OUT_DIR" ]]; then
	stamp="$(date -u '+%Y%m%dT%H%M%SZ')"
	OUT_DIR="$(cloud_env_artifacts_dir "$ENV_ROOT")/home-mqtt-loadtest/$stamp"
fi
mkdir -p "$OUT_DIR"

export ROOT ENV_ROOT BRANDNAME OUT_DIR PROFILE DURATION_SECONDS MAX_USERS SEED MQTT_PROBE
export TEST_DEVICES_DIR="$(cloud_env_test_devices_dir "$ENV_ROOT")"
export ARTIFACTS_DIR="$(cloud_env_artifacts_dir "$ENV_ROOT")"
export ACCOUNT_MANAGER_ENV="$(cloud_env_account_manager_env "$ENV_ROOT")"
export ACCOUNT_MANAGER_STATE="$(cloud_env_account_manager_state "$ENV_ROOT")"
export VIDEO_ENV="$(cloud_env_video_env "$ENV_ROOT")"
export VIDEO_STATE="$(cloud_env_video_state "$ENV_ROOT")"
export STACK_ENV="$(cloud_env_stack_env "$ENV_ROOT")"
export KEYS_DIR="$(cloud_env_keys_dir "$ENV_ROOT")"
export CERTIFICATES_DIR="$(cloud_env_certificates_dir "$ENV_ROOT")"

python3 - <<'PY'
import json
import os
import socket
import ssl
import stat
import struct
import sys
import time
from collections import defaultdict
from datetime import datetime, timezone
from pathlib import Path

ROOT = Path(os.environ["ROOT"])
ENV_ROOT = Path(os.environ["ENV_ROOT"])
BRANDNAME = os.environ["BRANDNAME"]
BRAND_LOWER = BRANDNAME.lower()
OUT_DIR = Path(os.environ["OUT_DIR"])
PROFILE = os.environ["PROFILE"]
DURATION_SECONDS = int(os.environ["DURATION_SECONDS"])
MAX_USERS = int(os.environ["MAX_USERS"]) if os.environ.get("MAX_USERS") else None
SEED = int(os.environ["SEED"])
MQTT_PROBE = os.environ["MQTT_PROBE"] == "true"
TEST_DEVICES_DIR = Path(os.environ["TEST_DEVICES_DIR"])
ARTIFACTS_DIR = Path(os.environ["ARTIFACTS_DIR"])
ACCOUNT_MANAGER_ENV = Path(os.environ["ACCOUNT_MANAGER_ENV"])
ACCOUNT_MANAGER_STATE = Path(os.environ["ACCOUNT_MANAGER_STATE"])
VIDEO_ENV = Path(os.environ["VIDEO_ENV"])
VIDEO_STATE = Path(os.environ["VIDEO_STATE"])
STACK_ENV = Path(os.environ["STACK_ENV"])

HOME_TYPES = ("light", "air_conditioner", "smart_meter")
SECRET_WORDS = ("password", "token", "secret", "private", "bearer", "device.key", "-----begin")


def now_iso():
    return datetime.now(timezone.utc).isoformat().replace("+00:00", "Z")


def load_json(path):
    with path.open("r", encoding="utf-8") as fh:
        return json.load(fh)


def env_keys(path):
    keys = []
    if path.exists():
        for line in path.read_text(encoding="utf-8", errors="replace").splitlines():
            if "=" in line and not line.lstrip().startswith("#"):
                keys.append(line.split("=", 1)[0])
    return sorted(set(keys))


def env_value(path, key):
    if not path.exists():
        return ""
    for line in path.read_text(encoding="utf-8", errors="replace").splitlines():
        if line.startswith(f"{key}="):
            return line.split("=", 1)[1].strip().strip('"').strip("'")
    return ""


def latest(pattern):
    matches = sorted(ARTIFACTS_DIR.glob(pattern), key=lambda p: p.stat().st_mtime, reverse=True)
    return matches[0] if matches else None


def redacted_error(message):
    lower = message.lower()
    if any(word in lower for word in SECRET_WORDS):
        return "redacted sensitive error"
    return message


def percentile(values, pct):
    if not values:
        return None
    ordered = sorted(values)
    rank = (len(ordered) - 1) * pct / 100.0
    low = int(rank)
    high = min(low + 1, len(ordered) - 1)
    if low == high:
        return ordered[low]
    return ordered[low] + (ordered[high] - ordered[low]) * (rank - low)


def mqtt_remaining_length(length):
    encoded = bytearray()
    while True:
        digit = length % 128
        length //= 128
        if length > 0:
            digit |= 0x80
        encoded.append(digit)
        if length == 0:
            return bytes(encoded)


def mqtt_utf8(value):
    raw = value.encode("utf-8")
    if len(raw) > 65535:
        raise ValueError("mqtt string too long")
    return struct.pack("!H", len(raw)) + raw


def mqtt_write_packet(sock, packet_type, body):
    sock.sendall(bytes([packet_type]) + mqtt_remaining_length(len(body)) + body)


def mqtt_read_exact(sock, count):
    chunks = bytearray()
    while len(chunks) < count:
        chunk = sock.recv(count - len(chunks))
        if not chunk:
            raise ConnectionError("mqtt connection closed")
        chunks.extend(chunk)
    return bytes(chunks)


def mqtt_read_packet(sock):
    first = mqtt_read_exact(sock, 1)[0]
    multiplier = 1
    remaining = 0
    while True:
        digit = mqtt_read_exact(sock, 1)[0]
        remaining += (digit & 127) * multiplier
        if (digit & 128) == 0:
            break
        multiplier *= 128
        if multiplier > 128 * 128 * 128:
            raise ValueError("malformed mqtt remaining length")
    return first, mqtt_read_exact(sock, remaining)


def mqtt_connect(sock, client_id):
    variable = mqtt_utf8("MQTT") + bytes([4, 2]) + struct.pack("!H", 30)
    mqtt_write_packet(sock, 0x10, variable + mqtt_utf8(client_id))
    packet_type, body = mqtt_read_packet(sock)
    if packet_type != 0x20 or len(body) < 2 or body[1] != 0:
        raise RuntimeError(f"mqtt connack failed: {list(body)}")


def mqtt_subscribe(sock, packet_id, topic, qos=0):
    body = struct.pack("!H", packet_id) + mqtt_utf8(topic) + bytes([qos])
    mqtt_write_packet(sock, 0x82, body)
    packet_type, response = mqtt_read_packet(sock)
    if packet_type != 0x90 or len(response) < 3 or response[2] == 0x80:
        raise RuntimeError(f"mqtt suback failed for {topic}: {list(response)}")


def mqtt_publish(sock, topic, payload):
    mqtt_write_packet(sock, 0x30, mqtt_utf8(topic) + payload)


def mqtt_decode_publish(flags, body):
    if len(body) < 2:
        raise ValueError("publish body too short")
    topic_len = struct.unpack("!H", body[:2])[0]
    topic_end = 2 + topic_len
    if len(body) < topic_end:
        raise ValueError("publish topic truncated")
    pos = topic_end
    qos = (flags >> 1) & 0x03
    if qos:
        pos += 2
    return body[2:topic_end].decode("utf-8", errors="replace"), body[pos:]


def run_device_shadow_e2e(record, host, port, timeout_seconds=10):
    device_id = record["device_id"]
    token = f"mqtt-e2e-{int(time.time())}-{device_id}"
    base = f"$vc/devices/{device_id}/shadow/update"
    topics = {
        "accepted": base + "/accepted",
        "documents": base + "/documents",
        "rejected": base + "/rejected",
    }
    payload = json.dumps({
        "state": {
            "reported": {
                "e2e_probe": {
                    "brand": BRANDNAME,
                    "device_type": record["device_type"],
                    "timestamp": now_iso(),
                }
            }
        },
        "clientToken": token,
    }, separators=(",", ":")).encode("utf-8")

    started = time.monotonic()
    context = ssl.create_default_context()
    context.check_hostname = False
    context.verify_mode = ssl.CERT_NONE
    context.load_cert_chain(record["cert_path"], record["key_path"])
    with socket.create_connection((host, int(port)), timeout=timeout_seconds) as raw:
        with context.wrap_socket(raw, server_hostname=str(host)) as sock:
            sock.settimeout(timeout_seconds)
            mqtt_connect(sock, f"rtk-e2e-{device_id}-{os.getpid()}")
            mqtt_subscribe(sock, 1, topics["accepted"], 0)
            mqtt_subscribe(sock, 2, topics["documents"], 0)
            mqtt_subscribe(sock, 3, topics["rejected"], 0)
            mqtt_publish(sock, base, payload)

            seen = {}
            deadline = time.monotonic() + timeout_seconds
            while time.monotonic() < deadline:
                sock.settimeout(max(0.1, deadline - time.monotonic()))
                packet_type, body = mqtt_read_packet(sock)
                if packet_type >> 4 != 3:
                    continue
                topic, message = mqtt_decode_publish(packet_type & 0x0F, body)
                if topic not in topics.values():
                    continue
                try:
                    doc = json.loads(message.decode("utf-8"))
                except Exception:
                    doc = {}
                if doc.get("clientToken") != token:
                    continue
                if topic == topics["rejected"]:
                    return {
                        "device_id": device_id,
                        "device_type": record["device_type"],
                        "status": "FAIL",
                        "error": f"shadow rejected: {doc.get('code', 'unknown')}",
                        "latency_ms": round((time.monotonic() - started) * 1000),
                    }
                if topic == topics["accepted"]:
                    seen["accepted"] = True
                if topic == topics["documents"]:
                    seen["documents"] = True
                if seen.get("accepted") and seen.get("documents"):
                    return {
                        "device_id": device_id,
                        "device_type": record["device_type"],
                        "status": "PASS",
                        "topics": ["accepted", "documents"],
                        "latency_ms": round((time.monotonic() - started) * 1000),
                    }
    return {
        "device_id": device_id,
        "device_type": record["device_type"],
        "status": "FAIL",
        "error": "timed out waiting for shadow accepted/documents",
        "latency_ms": round((time.monotonic() - started) * 1000),
    }


def write_outputs(result):
    OUT_DIR.mkdir(parents=True, exist_ok=True)
    results_file = OUT_DIR / "results.json"
    report_file = OUT_DIR / "TEST_REPORT.md"
    result["results_file"] = str(results_file)
    result["report_file"] = str(report_file)
    results_file.write_text(json.dumps(result, indent=2, sort_keys=True) + "\n", encoding="utf-8")
    report_file.write_text(render_report(result), encoding="utf-8")
    print(json.dumps({
        "action": "home-mqtt-loadtest",
        "overall": result["overall"],
        "status": result["status"],
        "results_file": str(results_file),
        "report_file": str(report_file),
    }, sort_keys=True))


def render_report(result):
    lines = [
        "# Home MQTT Load-Test Report",
        "",
        f"- Status: {result['status']}",
        f"- Overall: {result['overall']}",
        f"- Generated: {result['generated_at']}",
        f"- Env root: `{result['env']['root']}`",
        f"- Brand: `{result['brandname']}`",
        f"- Profile: `{result['profile']}`",
        f"- Duration seconds: {result['duration_seconds']}",
        f"- Seed: {result['seed']}",
        "",
        "## Inputs",
        "",
        f"- Users artifact: `{result['inputs'].get('users_artifact', 'missing')}`",
        f"- Device bind artifact: `{result['inputs'].get('device_bind_artifact', 'missing')}`",
        f"- Device manifest: `{result['inputs'].get('device_manifest', 'missing')}`",
        f"- Account Manager endpoint: `{result['endpoints'].get('account_manager_base_url', 'unknown')}`",
        f"- Video Cloud endpoint: `{result['endpoints'].get('video_cloud_base_url', 'unknown')}`",
        f"- MQTT endpoint: `{result['endpoints'].get('mqtt_host', 'unknown')}:{result['endpoints'].get('mqtt_port', 'unknown')}`",
        "",
    ]
    if result["status"] == "BLOCKED":
        lines += ["## Blockers", ""]
        for blocker in result.get("blockers", []):
            lines.append(f"- {blocker}")
        lines.append("")
        return "\n".join(lines) + "\n"

    metrics = result["metrics"]
    lines += [
        "## Summary",
        "",
        "| Metric | Value |",
        "| --- | ---: |",
        f"| Users selected | {metrics['users_selected']} |",
        f"| Devices selected | {metrics['devices_selected']} |",
        f"| Commands attempted | {metrics['commands_attempted']} |",
        f"| Commands passed | {metrics['commands_passed']} |",
        f"| Success rate | {metrics['success_rate_percent']:.1f}% |",
        f"| Command p95 ms | {metrics['command_latency_p95_ms']:.1f} |",
        f"| Command p99 ms | {metrics['command_latency_p99_ms']:.1f} |",
        f"| Telemetry freshness max ms | {metrics['telemetry_freshness_max_ms']:.1f} |",
        "",
        "## Per Capability",
        "",
        "| Capability | Devices | Commands | Success |",
        "| --- | ---: | ---: | ---: |",
    ]
    for row in result["capability_metrics"]:
        lines.append(f"| {row['capability']} | {row['devices']} | {row['commands']} | {row['success_percent']:.1f}% |")
    lines += [
        "",
        "## Per Device MQTT E2E",
        "",
        "| Device | Type | Status | Latency ms | Error |",
        "| --- | --- | --- | ---: | --- |",
    ]
    for device in result["devices"]:
        error = device.get("error", "")
        latency = device.get("latency_ms", [0])[0] if device.get("latency_ms") else 0
        lines.append(f"| {device['device_id']} | {device['device_type']} | {device.get('mqtt_status', 'UNKNOWN')} | {latency} | {error} |")
    lines += [
        "",
        "## Negative Checks",
        "",
    ]
    if result["negative_checks"]:
        lines += [
            "| Check | Result |",
            "| --- | --- |",
        ]
        for check in result["negative_checks"]:
            lines.append(f"| {check['name']} | {check['result']} |")
    else:
        lines.append("- NOT_RUN")
    lines += [
        "",
        "## MQTT mTLS",
        "",
        f"- Probe result: {result['mqtt']['probe_result']}",
        f"- Client identities checked: {result['mqtt']['client_identities_checked']}",
        "- Certificate and key bodies are intentionally omitted.",
        "",
        "## Out Of Scope",
        "",
    ]
    for item in result["out_of_scope"]:
        lines.append(f"- {item}: NOT_RUN")
    return "\n".join(lines) + "\n"


blockers = []
required_files = {
    "stack_env": STACK_ENV,
    "account_manager_env": ACCOUNT_MANAGER_ENV,
    "video_env": VIDEO_ENV,
    "video_state": VIDEO_STATE,
    "device_manifest": TEST_DEVICES_DIR / "manifests" / "devices.json",
    "device_ids": TEST_DEVICES_DIR / "manifests" / "device_ids.txt",
    "loadtest_env": TEST_DEVICES_DIR / "loadtest.env",
}
for name, path in required_files.items():
    if not path.exists():
        blockers.append(f"missing {name}: {path}")
    elif not os.access(path, os.R_OK):
        blockers.append(f"unreadable {name}: {path}")

users_artifact = latest(f"users/{BRAND_LOWER}-users-*.json")
bind_artifact = latest(f"device-bind/{BRAND_LOWER}-device-bind-*.json")
if not users_artifact:
    blockers.append(f"missing latest users artifact for brand {BRANDNAME}")
if not bind_artifact:
    blockers.append(f"missing latest device-bind artifact for brand {BRANDNAME}")

if users_artifact:
    mode = stat.S_IMODE(users_artifact.stat().st_mode)
    if mode & 0o077:
        blockers.append(f"users artifact must not be group/world readable: {users_artifact}")

inputs = {
    "users_artifact": str(users_artifact) if users_artifact else "missing",
    "device_bind_artifact": str(bind_artifact) if bind_artifact else "missing",
    "device_manifest": str(required_files["device_manifest"]),
    "env_key_counts": {
        "stack": len(env_keys(STACK_ENV)),
        "account_manager": len(env_keys(ACCOUNT_MANAGER_ENV)),
        "video_cloud": len(env_keys(VIDEO_ENV)),
    },
}
endpoints = {
    "account_manager_base_url": f"https://{env_value(STACK_ENV, 'ACCOUNT_MANAGER_DOMAIN') or env_value(ACCOUNT_MANAGER_ENV, 'ACCOUNT_MANAGER_LINODE_DOMAIN') or 'unknown'}",
    "video_cloud_base_url": f"https://{env_value(STACK_ENV, 'VIDEO_CLOUD_DOMAIN') or 'unknown'}",
}
if VIDEO_STATE.exists():
    try:
        state = load_json(VIDEO_STATE)
        mqtt = state.get("instances", {}).get("mqtt", {})
        endpoints["mqtt_host"] = env_value(TEST_DEVICES_DIR / "loadtest.env", "MQTT_HOST") or mqtt.get("public_ipv4") or mqtt.get("private_ip") or "unknown"
        endpoints["mqtt_port"] = int(env_value(TEST_DEVICES_DIR / "loadtest.env", "MQTT_TLS_PORT") or env_value(TEST_DEVICES_DIR / "loadtest.env", "MQTT_PORT") or 8883)
    except Exception as exc:
        blockers.append(f"invalid video state JSON: {redacted_error(str(exc))}")
        endpoints["mqtt_host"] = "unknown"
        endpoints["mqtt_port"] = "unknown"

users = []
assignments = []
manifest = []
if users_artifact:
    try:
        data = load_json(users_artifact)
        if data.get("brandname", "").lower() != BRAND_LOWER:
            blockers.append(f"users artifact brand mismatch: {users_artifact}")
        users = data.get("users", [])
    except Exception as exc:
        blockers.append(f"invalid users artifact: {redacted_error(str(exc))}")
if bind_artifact:
    try:
        data = load_json(bind_artifact)
        if data.get("brandname", "").lower() != BRAND_LOWER:
            blockers.append(f"device-bind artifact brand mismatch: {bind_artifact}")
        assignments = data.get("assignments", [])
    except Exception as exc:
        blockers.append(f"invalid device-bind artifact: {redacted_error(str(exc))}")
if required_files["device_manifest"].exists():
    try:
        manifest = load_json(required_files["device_manifest"])
    except Exception as exc:
        blockers.append(f"invalid device manifest: {redacted_error(str(exc))}")

user_emails = {u.get("email") for u in users if u.get("email")}
manifest_by_id = {d.get("device_id"): d for d in manifest if isinstance(d, dict) and d.get("device_id")}
home_assignments = [
    a for a in assignments
    if a.get("device_type") in HOME_TYPES and "mqtt" in a.get("service_options", []) and a.get("assigned_email") in user_emails
]
if not home_assignments:
    blockers.append("no bound home MQTT devices for users in latest artifacts")
for device_type in HOME_TYPES:
    if not any(a.get("device_type") == device_type for a in home_assignments):
        blockers.append(f"missing bound {device_type} device in latest device-bind artifact")

selected_by_user = defaultdict(list)
for assignment in home_assignments:
    selected_by_user[assignment["assigned_email"]].append(assignment)
selected_users = sorted(selected_by_user)
if MAX_USERS:
    selected_users = selected_users[:MAX_USERS]
selected_assignments = [a for email in selected_users for a in selected_by_user[email]]

cert_records = []
for assignment in selected_assignments:
    device_id = assignment.get("device_id")
    device_type = assignment.get("device_type")
    record = manifest_by_id.get(device_id)
    if not record:
        blockers.append(f"device {device_id} missing from manifest")
        continue
    cert_rel = record.get("certificate_path") or f"devices/{device_type}/{device_id}/device.cert.pem"
    key_rel = record.get("key_path") or f"devices/{device_type}/{device_id}/device.key.pem"
    chain_rel = record.get("certificate_chain_path") or f"devices/{device_type}/{device_id}/device.chain.pem"
    cert = TEST_DEVICES_DIR / cert_rel
    key = TEST_DEVICES_DIR / key_rel
    chain = TEST_DEVICES_DIR / chain_rel
    for label, path in (("cert", cert), ("key", key), ("chain", chain)):
        if not path.exists():
            blockers.append(f"device {device_id} missing {label} file")
        elif not os.access(path, os.R_OK):
            blockers.append(f"device {device_id} unreadable {label} file")
    cert_records.append({
        "device_id": device_id,
        "device_type": device_type,
        "cert_path": str(cert),
        "key_path": str(key),
        "chain_path": str(chain),
    })

base_result = {
    "generated_at": now_iso(),
    "status": "BLOCKED" if blockers else "PASS",
    "overall": "blocked" if blockers else "pass",
    "brandname": BRANDNAME,
    "profile": PROFILE,
    "duration_seconds": DURATION_SECONDS,
    "seed": SEED,
    "env": {"root": str(ENV_ROOT)},
    "inputs": inputs,
    "endpoints": endpoints,
    "blockers": blockers,
}
if blockers:
    write_outputs(base_result)
    sys.exit(3)

latencies = []
capability_counts = {kind: {"devices": 0, "commands": 0, "passed": 0} for kind in HOME_TYPES}
per_device = []
mqtt_probe_result = "NOT_RUN"
if MQTT_PROBE:
    host = endpoints.get("mqtt_host")
    port = endpoints.get("mqtt_port")
    if not host or host == "unknown" or not port:
        mqtt_probe_result = "BLOCKED: missing MQTT endpoint"
    else:
        mqtt_probe_result = "PASS"
        for item in selected_assignments:
            dtype = item["device_type"]
            capability_counts[dtype]["devices"] += 1
            capability_counts[dtype]["commands"] += 1
            record = next((r for r in cert_records if r["device_id"] == item["device_id"]), None)
            if not record:
                outcome = {
                    "device_id": item["device_id"],
                    "device_type": dtype,
                    "status": "FAIL",
                    "error": "missing certificate record",
                    "latency_ms": 0,
                }
            else:
                try:
                    outcome = run_device_shadow_e2e(record, host, port)
                except Exception as exc:
                    outcome = {
                        "device_id": item["device_id"],
                        "device_type": dtype,
                        "status": "FAIL",
                        "error": redacted_error(type(exc).__name__),
                        "latency_ms": 0,
                    }
            if outcome["status"] == "PASS":
                capability_counts[dtype]["passed"] += 1
            else:
                mqtt_probe_result = "FAIL"
            latencies.append(outcome.get("latency_ms", 0))
            per_device.append({
                "device_id": item["device_id"],
                "device_type": dtype,
                "assigned_email": item["assigned_email"],
                "commands": 1,
                "success_percent": 100.0 if outcome["status"] == "PASS" else 0.0,
                "latency_ms": [outcome.get("latency_ms", 0)],
                "mqtt_status": outcome["status"],
                **({"error": outcome["error"]} if outcome.get("error") else {}),
            })
else:
    base_result["status"] = "BLOCKED"
    base_result["overall"] = "blocked"
    base_result.setdefault("blockers", []).append("--no-mqtt-probe skips live MQTT E2E")

total_commands = sum(row["commands"] for row in per_device)
total_passed = sum(1 for row in per_device if row.get("mqtt_status") == "PASS")
success_rate = (total_passed / total_commands * 100.0) if total_commands else 0.0
telemetry_freshness = [row["latency_ms"][0] for row in per_device if row.get("device_type") == "smart_meter"]
capability_metrics = []
for cap, row in capability_counts.items():
    pct = 100.0 if row["commands"] == row["passed"] else (row["passed"] / row["commands"] * 100.0 if row["commands"] else 0.0)
    capability_metrics.append({
        "capability": cap,
        "devices": row["devices"],
        "commands": row["commands"],
        "success_percent": pct,
    })

result = {
    **base_result,
    "users": [{"email": email, "assigned_devices": len(selected_by_user[email])} for email in selected_users],
    "devices": per_device,
    "mtls_files": [{"device_id": r["device_id"], "device_type": r["device_type"], "cert": "present", "key": "present", "chain": "present"} for r in cert_records],
    "metrics": {
        "users_selected": len(selected_users),
        "devices_selected": len(selected_assignments),
        "commands_attempted": total_commands,
        "commands_passed": total_passed,
        "success_rate_percent": success_rate,
        "command_latency_p95_ms": percentile(latencies, 95) or 0.0,
        "command_latency_p99_ms": percentile(latencies, 99) or 0.0,
        "telemetry_freshness_max_ms": max(telemetry_freshness) if telemetry_freshness else 0.0,
    },
    "capability_metrics": capability_metrics,
    "negative_checks": [],
    "mqtt": {
        "probe_result": mqtt_probe_result,
        "client_identities_checked": len(cert_records),
        "client_identity_mode": "device_id",
    },
    "out_of_scope": ["webrtc", "relay", "storage", "clip", "snapshot"],
}
if result["overall"] != "blocked" and result["metrics"]["success_rate_percent"] < 95.0:
    result["status"] = "FAIL"
    result["overall"] = "fail"
if result["overall"] != "blocked" and MQTT_PROBE and mqtt_probe_result != "PASS":
    result["status"] = "FAIL"
    result["overall"] = "fail"
write_outputs(result)
sys.exit(0 if result["overall"] == "pass" else 1)
PY
