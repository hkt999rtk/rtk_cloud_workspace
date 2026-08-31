#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)"
template="$repo_root/loadtests/home-100k/reports/templates/test_report.md.tmpl"
out_dir=""

usage() {
  cat <<EOF
usage: $(basename "$0") --out-dir <run-artifact-dir> [--template <template-file>]

Reads results.json, server-evidence.json, sync telemetry, workflow logs, and
resource-samples/*.tsv from the run artifact directory, then writes the fixed
format test_report.md.
EOF
}

while [[ "$#" -gt 0 ]]; do
  case "$1" in
    --out-dir)
      out_dir="${2:-}"
      shift 2
      ;;
    --template)
      template="${2:-}"
      shift 2
      ;;
    -h|--help)
      usage
      exit 0
      ;;
    *)
      echo "unknown argument: $1" >&2
      usage >&2
      exit 2
      ;;
  esac
done

if [[ -z "$out_dir" ]]; then
  echo "--out-dir is required" >&2
  exit 2
fi
if [[ ! -f "$out_dir/results.json" ]]; then
  echo "missing results.json under $out_dir" >&2
  exit 2
fi
if [[ ! -f "$template" ]]; then
  echo "missing report template: $template" >&2
  exit 2
fi

python3 - "$out_dir" "$template" <<'PY'
import csv
import json
import math
import os
import re
import sys
from collections import defaultdict
from pathlib import Path

out_dir = Path(sys.argv[1])
template_path = Path(sys.argv[2])
results_path = out_dir / "results.json"

with results_path.open() as f:
    result = json.load(f)

template = template_path.read_text()

def md(value):
    text = "" if value is None else str(value)
    return text.replace("|", "\\|").replace("\n", " ")

def num(value, default=0):
    if value is None or value == "":
        return default
    try:
        return int(value)
    except (TypeError, ValueError):
        try:
            return float(value)
        except (TypeError, ValueError):
            return default

def float_num(value, default=0.0):
    if value is None or value == "":
        return default
    try:
        return float(value)
    except (TypeError, ValueError):
        return default

def has_video_evidence():
    video = result.get("video_evidence") or {}
    webrtc = video.get("webrtc_totals") or {}
    media = video.get("webrtc_media_totals") or {}
    turn = video.get("turn_evidence") or {}
    return any([
        num(webrtc.get("create_attempts"), 0),
        num(webrtc.get("setup_attempts"), 0),
        num(webrtc.get("close_attempts"), 0),
        num(media.get("attempts"), 0),
        bool(turn.get("coturn_available")),
    ])

def is_video_only_run():
    plan = result.get("plan") or {}
    return bool(plan.get("video_profile")) and has_video_evidence() and not (result.get("stage_results") or [])

def pct_value(value):
    if value is None:
        return None
    text = str(value).strip().rstrip("%")
    if text == "":
        return None
    try:
        return float(text)
    except ValueError:
        return None

def fmt_float(value):
    if value is None:
        return "-"
    return f"{value:.1f}"

def fmt_mi(value):
    if value is None:
        return "-"
    return f"{math.ceil(float(value) / (1024 * 1024))}Mi"

def percentile(values, p):
    values = sorted(v for v in values if v is not None)
    if not values:
        return None
    index = math.ceil((p / 100.0) * len(values)) - 1
    index = max(0, min(index, len(values) - 1))
    return values[index]

def lines_or_dash(lines):
    return "\n".join(lines) if lines else "- no data"

def read_tsv(path):
    if not path.exists():
        return []
    with path.open(newline="") as f:
        return list(csv.DictReader(f, delimiter="\t"))

def first_last(rows):
    times = [r.get("time", "") for r in rows if r.get("time")]
    if not times:
        return "-", "-"
    return min(times), max(times)

def total_device_totals(stages):
    keys = [
        "connect_attempts", "connect_success", "connect_fail",
        "token_attempts", "token_success", "token_fail",
        "token_first_attempt_success", "token_first_attempt_fail",
        "token_retry_attempts", "token_retry_success", "token_retry_exhausted",
        "mqtt_dial_attempts", "mqtt_dial_success", "mqtt_dial_fail",
        "mqtt_connack_attempts", "mqtt_connack_success", "mqtt_connack_fail",
        "subscribe_attempts", "subscribe_fail", "subscribes",
        "publishes", "received_messages", "delta_received",
        "reported_publishes", "rejected_publishes", "bytes_sent",
        "bytes_received",
    ]
    totals = {k: 0 for k in keys}
    for stage in stages:
        t = stage.get("device_mqtt_totals", {})
        for key in keys:
            totals[key] += int(num(t.get(key), 0))
    return totals

def total_app_totals(stages):
    keys = [
        "login_attempts", "login_success", "login_fail",
        "token_attempts", "token_success", "token_fail",
        "token_first_attempt_success", "token_first_attempt_fail",
        "token_retry_attempts", "token_retry_success", "token_retry_exhausted",
        "mqtt_dial_attempts", "mqtt_dial_success", "mqtt_dial_fail",
        "mqtt_connack_attempts", "mqtt_connack_success", "mqtt_connack_fail",
        "list_devices_requests", "read_shadow_requests", "desired_writes",
        "received_acks", "bytes_sent", "bytes_received",
    ]
    totals = {k: 0 for k in keys}
    for stage in stages:
        t = stage.get("app_user_totals", {})
        for key in keys:
            totals[key] += int(num(t.get(key), 0))
    return totals

def status_summary():
    lines = [
        f"- status: {md(result.get('status', 'UNKNOWN'))}",
        f"- result: {md(result.get('result', 'UNKNOWN'))}",
    ]
    if is_video_only_run():
        lines.append("- MQTT/shadow correlation: skipped for WebRTC-only workflow")
        lines.append("- runtime log stream correlation: skipped for WebRTC-only workflow")
        evidence = result.get("server_evidence") or {}
        if evidence:
            lines.append(f"- server evidence complete: {str(bool(evidence.get('complete'))).lower()}")
        return lines_or_dash(lines)
    correlation = result.get("server_correlation") or {}
    if correlation.get("status"):
        lines.append(f"- server correlation: {md(correlation.get('status'))}")
    for reason in correlation.get("reasons") or []:
        lines.append(f"- incomplete reason: {md(reason)}")
    runtime = result.get("runtime_log_correlation") or {}
    if runtime.get("status"):
        lines.append(f"- runtime log stream correlation: {md(runtime.get('status'))}")
        if num(runtime.get("missing_stream_count"), 0) or num(runtime.get("missing_sequence_count"), 0):
            lines.append(
                f"- runtime log missing streams/sequences: {num(runtime.get('missing_stream_count'), 0)}/"
                f"{num(runtime.get('missing_sequence_count'), 0)}"
            )
    health = result.get("load_generator_health") or {}
    if health.get("saturated"):
        lines.append("- load-generator saturated: true")
        for reason in health.get("reasons") or []:
            lines.append(f"- saturation reason: {md(reason)}")
    evidence = result.get("server_evidence") or {}
    if evidence:
        lines.append(f"- server evidence complete: {str(bool(evidence.get('complete'))).lower()}")
    return lines_or_dash(lines)

def test_conditions():
    plan = result.get("plan") or {}
    conditions = (plan.get("conditions") or {})
    brand_distribution = plan.get("brand_distribution") or []
    lines = [
        f"- Env root: `{md(conditions.get('env_root', '-'))}`",
    ]
    if brand_distribution:
        lines.extend([
            f"- Brand plan: `{md(conditions.get('brand_plan_file', '-'))}`",
            f"- Brand clouds: {len(brand_distribution)}",
            f"- Normal users: {num(conditions.get('users'), 0)}",
            f"- Developer users: {num(conditions.get('developer_users'), 0)}",
            "",
            "| Brand cloud | Devices | Normal users | Developer users |",
            "| --- | ---: | ---: | --- |",
        ])
        for brand in brand_distribution:
            lines.append(
                f"| {md(brand.get('brandname', '-'))} | {num(brand.get('devices'), 0)} | "
                f"{num(brand.get('normal_users'), 0)} | {md(format_developer_users(brand.get('developer_users') or {}))} |"
            )
        lines.append("")
    else:
        lines.append(f"- Brand: `{md(conditions.get('brandname', '-'))}`")
    lines.extend([
        f"- Region: `{md(conditions.get('region', '-'))}`",
        f"- Devices: {num(conditions.get('devices'), 0)}",
        f"- Users: {num(conditions.get('users'), 0)}",
        f"- Devices per user: {num(conditions.get('devices_per_user'), 0)}",
        f"- Runner nofile limit: {num(conditions.get('runner_nofile_limit'), 0)}",
        f"- Device session model: `{md(conditions.get('device_session_model', '-'))}`",
        f"- Runner read model: `{md(conditions.get('runner_read_model', '-'))}`",
        "- Runner read requirement: sustained MQTT reads through Go netpoll-backed connections and bounded per-device reader goroutines; command-time one-shot reads are not valid for capacity conclusions.",
    ])
    return "\n".join(lines)

def firmware_ota_simulation():
    plan = result.get("plan") or {}
    profile = plan.get("ota_profile") or {}
    ota = result.get("ota") or {}
    if not profile and not ota:
        return ""
    actual = ota.get("by_actual_terminal") or {}
    lines = [
        "## Firmware OTA Simulation",
        f"- Campaign ID: `{md(ota.get('campaign_id') or profile.get('campaign_id') or '-')}`",
        f"- Target version: `{md(ota.get('target_version') or profile.get('target_version') or '-')}`",
        f"- Evidence complete: {str(bool(ota.get('complete'))).lower()}",
        f"- Selected / MQTT ready / assigned: {num(ota.get('devices_selected'))} / {num(ota.get('mqtt_ready'))} / {num(ota.get('assignments_received'))}",
        f"- Terminal matched / expected: {num(ota.get('terminal_matched'))} / {num(ota.get('terminal_expected'))}",
        f"- Unique / duplicate device results: {num(ota.get('unique_device_results'))} / {num(ota.get('duplicate_device_results'))}",
        f"- Artifact bytes / hashes verified: {num(ota.get('artifact_bytes'))} / {num(ota.get('artifact_hash_verified'))}",
        f"- MQTT reboot disconnects / reconnect successes: {num(ota.get('mqtt_reboot_disconnects'))} / {num(ota.get('mqtt_reconnect_successes'))}",
        f"- Unexpected failures: {num(ota.get('unexpected_failures'))}",
    ]
    if actual:
        lines.append("- Actual terminal states:")
        for state in sorted(actual):
            lines.append(f"  - {md(state)}: {num(actual.get(state))}")
    for reason in ota.get("failure_reasons") or []:
        lines.append(f"- Failure reason: {md(reason)}")
    lines.append("- Per-device evidence: `shards/<vm-label>/ota-devices.jsonl`")
    return "\n".join(lines)

def report_title():
    plan = result.get("plan") or {}
    if plan.get("ota_profile") or result.get("ota"):
        return "Firmware OTA Virtual Device Load Test Report"
    return "100K Home IoT Device Shadow Load Test Report"

def account_activation():
    plan = result.get("plan") or {}
    conditions = plan.get("conditions") or {}
    brand_plan_value = str(conditions.get("brand_plan_file") or "").strip()
    if not brand_plan_value:
        return ""
    brand_plan_path = Path(brand_plan_value)
    if not brand_plan_path.is_absolute() and not brand_plan_path.exists():
        brand_plan_path = out_dir / brand_plan_path
    try:
        brand_plan = json.loads(brand_plan_path.read_text())
    except (OSError, ValueError):
        return ""
    run_id = str(brand_plan.get("run_id") or "").strip()
    brands = brand_plan.get("brands") or []
    if not run_id or not brands:
        return ""
    passed = 0
    for evidence_path in sorted((brand_plan_path.parent / "owner-activation").glob("*.json")):
        try:
            evidence = json.loads(evidence_path.read_text())
        except (OSError, ValueError):
            continue
        if evidence.get("status") == "PASS" and evidence.get("run_id") == run_id:
            passed += 1
    synthetic_members = sum(num(brand.get("normal_users"), 0) for brand in brands)
    activation_status = "PASS" if passed == len(brands) and passed > 0 else "INCOMPLETE"
    return "\n".join([
        "## Account Activation",
        f"- Formal email-activated owners: {passed}/{len(brands)} ({activation_status})",
        f"- Synthetic bulk-provisioned members: {synthetic_members}",
        "- Formal owners are Send Mail + local IMAP activations; synthetic members are not signup accounts.",
    ])

def video_load_profile():
    plan = result.get("plan") or {}
    profile = plan.get("video_profile") or {}
    evidence = result.get("video_evidence") or {}
    server = result.get("server_evidence") or {}
    api_counters = ((server.get("sources") or {}).get("video_cloud_api") or {}).get("counters") or {}
    if not profile.get("name") and not evidence:
        return ""
    webrtc = evidence.get("webrtc_totals") or {}
    media = evidence.get("webrtc_media_totals") or {}
    turn = evidence.get("turn_evidence") or {}

    def phase_row(name, attempts, successes):
        attempts = int(num(attempts, 0))
        successes = int(num(successes, 0))
        rate = 0.0 if attempts <= 0 else (successes * 100.0 / attempts)
        return f"| {md(name)} | {attempts} | {successes} | {rate:.2f}% |"

    lines = [
        "## Video Load Profile",
        f"- Video profile: `{md(profile.get('name', '-'))}`",
        f"- Video devices: {num(profile.get('video_devices'), 0)}",
        f"- Video viewers: {num(profile.get('video_viewers'), 0)}",
        f"- Viewer ladder: `{md(','.join(str(x) for x in (profile.get('viewer_ladder') or [])) or '-')}`",
        f"- WebRTC media set: `{md(profile.get('webrtc_media_set', '-'))}`",
        f"- WebRTC ICE policy: `{md(profile.get('webrtc_ice_policy', '-'))}`",
        f"- Video step duration: `{md(profile.get('step_duration', '-'))}`",
        f"- Video step cooldown: `{md(profile.get('step_cooldown', '-'))}`",
        f"- TURN transport: `{md(profile.get('turn_transport') or 'udp,tcp')}`",
        f"- Media security: `{md(profile.get('media_security') or 'dtls-srtp')}`",
        "- TURN relays encrypted DTLS-SRTP packets over the configured UDP/TCP transports; coturn does not terminate DTLS or inspect RTP/H.264 payloads.",
        f"- API running pods: {num(api_counters.get('video_cloud_api.k8s.running_pods'), 0)}",
        f"- WebRTC signaling store enabled pods: {num(api_counters.get('video_cloud_api.webrtc_signaling_store.enabled_pods'), 0)}",
        f"- WebRTC signaling store address pods: {num(api_counters.get('video_cloud_api.webrtc_signaling_store.addr_pods'), 0)}",
        f"- WebRTC signaling store prefix pods: {num(api_counters.get('video_cloud_api.webrtc_signaling_store.prefix_pods'), 0)}",
        f"- Device actor: `{md(profile.get('device_actor_role', '-'))}`",
        f"- App actor: `{md(profile.get('app_actor_role', '-'))}`",
        f"- Viewer actor: `{md(profile.get('viewer_actor_role', '-'))}`",
        f"- Evidence complete: {str(bool(evidence.get('complete'))).lower()}",
        "",
    ]
    steps = evidence.get("steps") or []
    if len(steps) > 1:
        lines.extend([
            "## Video Ladder",
            "| Step | Viewers | ICE policy | WebRTC success | Media success | first RTP p95 | first H.264 AU p95 | Relay samples | Non-relay samples | TURN allocations | TURN sessions |",
            "| --- | ---: | --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: |",
        ])
        for step in steps:
            step_webrtc = step.get("webrtc_totals") or {}
            step_media = step.get("webrtc_media_totals") or {}
            step_startup = step_media.get("video_startup_latency") or {}
            step_turn = step.get("turn_evidence") or {}
            attempts = num(step_media.get("attempts"), 0)
            successes = num(step_media.get("successes"), 0)
            media_rate = 0 if attempts <= 0 else successes * 100.0 / attempts
            lines.append(
                f"| {md(step.get('name') or str(step.get('viewers') or '-'))} | {num(step.get('viewers'), 0)} | "
                f"{md(step.get('ice_policy') or '-')} | {float_num(step_webrtc.get('success_rate_percent'), 0):.2f}% | "
                f"{media_rate:.2f}% | {num(step_media.get('time_to_first_rtp_p95_ms'), 0)} ms | "
                f"{num(step_startup.get('app_request_to_first_h264_access_unit_p95_ms'), 0)} ms | "
                f"{num(step.get('relay_candidate_samples'), 0)} | {num(step.get('non_relay_candidate_samples'), 0)} | "
                f"{num(step_turn.get('allocations'), 0)} | {num(step_turn.get('active_sessions'), 0)} |"
            )
        lines.append("")
    lines.extend([
        "## WebRTC Totals",
        "| Phase | Attempts | Success | Success rate |",
        "| --- | ---: | ---: | ---: |",
        phase_row("create", webrtc.get("create_attempts"), webrtc.get("create_success")),
        phase_row("setup", webrtc.get("setup_attempts"), webrtc.get("setup_success")),
        phase_row("close", webrtc.get("close_attempts"), webrtc.get("close_success")),
        f"- Setup p95: {num(webrtc.get('setup_p95_ms'), 0)} ms",
        f"- Setup p99: {num(webrtc.get('setup_p99_ms'), 0)} ms",
        f"- ICE server count: {num(webrtc.get('ice_server_count'), 0)}",
        f"- Open sessions: {num(webrtc.get('open_sessions'), 0)}",
    ])
    if media.get("enabled") or num(media.get("attempts"), 0) > 0:
        startup = media.get("video_startup_latency") or {}
        breakdown = startup.get("breakdown_p95") or {}
        lines.extend([
            "",
            "## WebRTC Media Totals",
            f"- Attempts: {num(media.get('attempts'), 0)}",
            f"- Successes: {num(media.get('successes'), 0)}",
            f"- Failures: {num(media.get('failures'), 0)}",
            f"- ICE connected p95: {num(media.get('ice_connected_p95_ms'), 0)} ms",
            f"- First RTP p95: {num(media.get('time_to_first_rtp_p95_ms'), 0)} ms",
            f"- RTP packets received: {num(media.get('packets_received'), 0)}",
            f"- RTP bytes received: {num(media.get('bytes_received'), 0)}",
            f"- H.264 packets received: {num(media.get('h264_packets_received'), 0)}",
            f"- H.264 bytes received: {num(media.get('h264_bytes_received'), 0)}",
            f"- Opus packets received: {num(media.get('opus_packets_received'), 0)}",
            f"- Opus bytes received: {num(media.get('opus_bytes_received'), 0)}",
        ])
        if num(startup.get("app_request_to_first_rtp_p95_ms"), 0) > 0 or num(startup.get("app_request_to_first_h264_access_unit_p95_ms"), 0) > 0:
            lines.extend([
                "",
                "## Video Startup Latency",
                f"- Samples: {num(startup.get('samples'), 0)}",
                f"- H.264 access unit samples: {num(startup.get('h264_access_unit_samples'), 0)}",
                f"- App request -> first RTP p50: {num(startup.get('app_request_to_first_rtp_p50_ms'), 0)} ms",
                f"- App request -> first RTP p95: {num(startup.get('app_request_to_first_rtp_p95_ms'), 0)} ms",
                f"- App request -> first RTP p99: {num(startup.get('app_request_to_first_rtp_p99_ms'), 0)} ms",
            ])
            if num(startup.get("h264_access_unit_samples"), 0) > 0:
                lines.extend([
                    f"- App request -> first H.264 access unit p50: {num(startup.get('app_request_to_first_h264_access_unit_p50_ms'), 0)} ms",
                    f"- App request -> first H.264 access unit p95: {num(startup.get('app_request_to_first_h264_access_unit_p95_ms'), 0)} ms",
                    f"- App request -> first H.264 access unit p99: {num(startup.get('app_request_to_first_h264_access_unit_p99_ms'), 0)} ms",
                ])
            lines.extend([
                f"- API create p95: {num(breakdown.get('api_create_ms'), 0)} ms",
                f"- Offer delivery p95: {num(breakdown.get('offer_delivery_ms'), 0)} ms",
                f"- Device answer p95: {num(breakdown.get('device_answer_ms'), 0)} ms",
            ])
            remote_answer_set_ms = num(breakdown.get("remote_answer_set_ms"), 0)
            if remote_answer_set_ms > 0:
                lines.append(f"- Remote answer set p95: {remote_answer_set_ms} ms")
            lines.extend([
                f"- ICE check p95: {num(breakdown.get('ice_check_ms') or breakdown.get('ice_connect_ms'), 0)} ms",
            ])
            ice_connected_since_session_start_ms = num(breakdown.get("ice_connected_since_session_start_ms"), 0)
            if ice_connected_since_session_start_ms > 0:
                lines.append(f"- ICE connected since session start p95: {ice_connected_since_session_start_ms} ms")
            lines.extend([
                f"- First RTP after ICE p95: {num(breakdown.get('first_rtp_after_ice_ms'), 0)} ms",
            ])
            if num(startup.get("h264_access_unit_samples"), 0) > 0:
                lines.append(f"- First H.264 access unit after RTP p95: {num(breakdown.get('first_h264_access_unit_after_rtp_ms'), 0)} ms")
    lines.extend([
        "",
        "## TURN Evidence",
        f"- registry available: {str(bool(turn.get('registry_available'))).lower()}",
        f"- active nodes: {num(turn.get('active_nodes'), 0)}",
        f"- coturn available: {str(bool(turn.get('coturn_available'))).lower()}",
        f"- allocations: {num(turn.get('allocations'), 0)}",
        f"- active sessions: {num(turn.get('active_sessions'), 0)}",
        f"- coturn UDP sockets: {num(turn.get('udp_sockets'), 0)}",
        f"- coturn TCP established: {num(turn.get('tcp_established'), 0)}",
        f"- relay UDP flows: {num(turn.get('relay_udp_flows'), 0)}",
        f"- relay TCP flows: {num(turn.get('relay_tcp_flows'), 0)}",
        f"- coturn journal events: {num(turn.get('journal_events'), 0)}",
        f"- active evidence status: {md(str(turn.get('evidence_status') or '-'))}",
        f"- relay candidate samples: {num(evidence.get('relay_candidate_samples'), 0)}",
        f"- non-relay candidate samples: {num(evidence.get('non_relay_candidate_samples'), 0)}",
    ])
    return "\n".join(lines)

def format_developer_users(developer_users):
    parts = []
    for role in sorted(developer_users.keys()):
        count = num(developer_users.get(role), 0)
        if count > 0:
            parts.append(f"{role}={count}")
    return ", ".join(parts) if parts else "-"

def gate_standards():
    conditions = ((result.get("plan") or {}).get("conditions") or {})
    functional = float_num(conditions.get("functional_success_threshold_percent"), 99.5)
    target = float_num(conditions.get("client_target_completeness_percent"), 100)
    exact = float_num(conditions.get("exact_event_correlation_percent"), 100)
    aggregate_percent = float_num(conditions.get("aggregate_correlation_tolerance_percent"), 0.1)
    aggregate_min = int(num(conditions.get("aggregate_correlation_min_tolerance"), 5))
    return "\n".join([
        f"- Functional success threshold: {functional:.2f}%",
        f"- Client target completeness threshold: {target:.2f}%",
        f"- Exact event correlation threshold: {exact:.2f}%",
        f"- Aggregate counter tolerance: max({aggregate_min}, {aggregate_percent:.2f}%)",
    ])

def render_map(title, values):
    lines = [f"- {title}:"]
    for key in sorted((values or {}).keys()):
        lines.append(f"  - {md(key)}: {num(values.get(key), 0)}")
    return lines

def scenario_mix():
    plan = result.get("plan") or {}
    lines = [f"- Scenario profile: `{md(plan.get('scenario_profile') or 'home-diverse-v1')}`"]
    lines.extend(render_map("Device mix", plan.get("device_mix") or {}))
    lines.extend(render_map("Presence mix", plan.get("presence_mix") or {}))
    return "\n".join(lines)

def device_traffic_profiles():
    profiles = ((result.get("plan") or {}).get("device_profiles") or {})
    if not profiles:
        return "- no device traffic profiles"
    lines = [
        "| Device type | Ratio weight | Traffic profile | Payload class |",
        "| --- | ---: | --- | --- |",
    ]
    for name in sorted(profiles):
        profile = profiles.get(name) or {}
        lines.append(
            f"| {md(name)} | {num(profile.get('ratio_weight'), 0)} | "
            f"{md(profile.get('traffic_profile', '-'))} | {md(profile.get('payload_class', '-'))} |"
        )
    return "\n".join(lines)

def user_scenario_profiles():
    profiles = ((result.get("plan") or {}).get("user_profiles") or {})
    if not profiles:
        return "- no user scenario profiles"
    lines = [
        "| User profile | Ratio weight | Action profile |",
        "| --- | ---: | --- |",
    ]
    for name in sorted(profiles):
        profile = profiles.get(name) or {}
        lines.append(
            f"| {md(name)} | {num(profile.get('ratio_weight'), 0)} | "
            f"{md(profile.get('action_profile', '-'))} |"
        )
    return "\n".join(lines)

def target_window_section():
    plan = result.get("plan") or {}
    target = plan.get("target") or {}
    if target:
        return "\n".join([
            f"- target connects: {num(target.get('target_connects'), 0)}",
            f"- ramp-up time: {md(target.get('ramp_up_time', '-'))}",
        ])
    rows = []
    for stage in plan.get("stages") or []:
        target_connects = stage.get("target_connects", stage.get("connected_devices", 0))
        ramp_up_time = stage.get("ramp_up_time", stage.get("warm_up", "-"))
        rows.append(
            f"- {md(stage.get('name'))}: target connects {num(target_connects, 0)}, "
            f"ramp-up time {md(ramp_up_time)}"
        )
    return lines_or_dash(rows)

def stage_results():
    stages = result.get("stage_results") or []
    if not stages:
        return "- no target results"
    lines = [
        "| Window | Devices | MQTT connect | Reconnects | Shadow get p95 | Desired update p95 | Delta receive p95 | Desired->reported p95 | Offline desired p95 | Delta clear | Conflicts | Rejected | Auth violations | Client tokens | Duplicate apply |",
        "| --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: |",
    ]
    for s in stages:
        lines.append(
            f"| {md(s.get('name'))} | {num(s.get('connected_devices'), 0)} | "
            f"{fmt_float(num(s.get('mqtt_connect_success_rate_percent'), 0))}% | "
            f"{num(s.get('mqtt_reconnect_count'), 0)} | "
            f"{fmt_float(num(s.get('shadow_get_p95_ms'), 0))} ms | "
            f"{fmt_float(num(s.get('desired_update_p95_ms'), 0))} ms | "
            f"{fmt_float(num(s.get('delta_receive_p95_ms'), 0))} ms | "
            f"{fmt_float(num(s.get('desired_reported_p95_ms'), 0))} ms | "
            f"{fmt_float(num(s.get('offline_desired_p95_ms'), 0))} ms | "
            f"{fmt_float(num(s.get('delta_clear_success_rate_percent'), 0))}% | "
            f"{num(s.get('version_conflict_count'), 0)} | "
            f"{num(s.get('rejected_update_count'), 0)} | "
            f"{num(s.get('authorization_violation_count'), 0)} | "
            f"{num(s.get('client_token_correlation_count'), 0)} | "
            f"{num(s.get('duplicate_apply_count'), 0)} |"
        )
    return "\n".join(lines)

def stage_diagnostics():
    stages = result.get("stage_results") or []
    lines = [
        "| Window | Shard | Target | Before | After | New assignments | Connect window | Action window | Connect attempts | Connect success | Connect fail | Subscribes | Commands scheduled | Commands attempted | Commands passed | Skip reason |",
        "| --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | --- |",
    ]
    for stage in stages:
        diagnostics = stage.get("stage_diagnostics") or []
        if isinstance(diagnostics, dict):
            diagnostics = [diagnostics]
        for idx, diag in enumerate(diagnostics):
            if not isinstance(diag, dict):
                continue
            lines.append(
                f"| {md(stage.get('name'))} | {idx} | {num(diag.get('connected_target'), 0)} | "
                f"{num(diag.get('connected_before'), 0)} | {num(diag.get('connected_after'), 0)} | "
                f"{num(diag.get('new_assignments'), 0)} | {fmt_float(num(diag.get('connect_window_seconds'), 0))}s | "
                f"{fmt_float(num(diag.get('action_window_seconds'), 0))}s | "
                f"{num(diag.get('connect_attempts'), 0)} | {num(diag.get('connect_successes'), 0)} | "
                f"{num(diag.get('connect_failures'), 0)} | {num(diag.get('subscribe_successes'), 0)} | "
                f"{num(diag.get('commands_scheduled'), 0)} | {num(diag.get('commands_attempted'), 0)} | "
                f"{num(diag.get('commands_passed'), 0)} | {md(diag.get('skip_reason', '-'))} |"
            )
    if len(lines) == 2:
        return "- no target diagnostics"
    return "\n".join(lines)

def device_mqtt_totals():
    stages = result.get("stage_results") or []
    lines = [
        "| Window | Connect attempts | Connect success | Connect fail | Token ok/fail | Dial ok/fail | CONNACK ok/fail | Subscribe ok/fail | Publishes | Received | Delta received | Reported publishes | Rejected publishes | Bytes sent | Bytes received |",
        "| --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: |",
    ]
    for s in stages:
        t = s.get("device_mqtt_totals") or {}
        lines.append(
            f"| {md(s.get('name'))} | {num(t.get('connect_attempts'), 0)} | {num(t.get('connect_success'), 0)} | "
            f"{num(t.get('connect_fail'), 0)} | {num(t.get('token_success'), 0)}/{num(t.get('token_fail'), 0)} | "
            f"{num(t.get('mqtt_dial_success'), 0)}/{num(t.get('mqtt_dial_fail'), 0)} | "
            f"{num(t.get('mqtt_connack_success'), 0)}/{num(t.get('mqtt_connack_fail'), 0)} | "
            f"{num(t.get('subscribes'), 0)}/{num(t.get('subscribe_fail'), 0)} | {num(t.get('publishes'), 0)} | "
            f"{num(t.get('received_messages'), 0)} | {num(t.get('delta_received'), 0)} | {num(t.get('reported_publishes'), 0)} | "
            f"{num(t.get('rejected_publishes'), 0)} | {num(t.get('bytes_sent'), 0)} | {num(t.get('bytes_received'), 0)} |"
        )
    total = result.get("device_mqtt_totals") or total_device_totals(stages)
    lines.append(
        f"| total | {num(total.get('connect_attempts'), 0)} | {num(total.get('connect_success'), 0)} | "
        f"{num(total.get('connect_fail'), 0)} | {num(total.get('token_success'), 0)}/{num(total.get('token_fail'), 0)} | "
        f"{num(total.get('mqtt_dial_success'), 0)}/{num(total.get('mqtt_dial_fail'), 0)} | "
        f"{num(total.get('mqtt_connack_success'), 0)}/{num(total.get('mqtt_connack_fail'), 0)} | "
        f"{num(total.get('subscribes'), 0)}/{num(total.get('subscribe_fail'), 0)} | {num(total.get('publishes'), 0)} | "
        f"{num(total.get('received_messages'), 0)} | {num(total.get('delta_received'), 0)} | {num(total.get('reported_publishes'), 0)} | "
        f"{num(total.get('rejected_publishes'), 0)} | {num(total.get('bytes_sent'), 0)} | {num(total.get('bytes_received'), 0)} |"
    )
    return "\n".join(lines)

def token_retry_totals(title, total_key, stage_key):
    stages = result.get("stage_results") or []
    lines = [
        f"## {title}",
        "",
        "| Stage | First-attempt success | First-attempt fail | Retry attempts | Retry success | Retry exhausted | Token request total |",
        "| --- | ---: | ---: | ---: | ---: | ---: | ---: |",
    ]
    for s in stages:
        t = s.get(stage_key) or {}
        lines.append(
            f"| {md(s.get('name'))} | {num(t.get('token_first_attempt_success'), 0)} | "
            f"{num(t.get('token_first_attempt_fail'), 0)} | {num(t.get('token_retry_attempts'), 0)} | "
            f"{num(t.get('token_retry_success'), 0)} | {num(t.get('token_retry_exhausted'), 0)} | "
            f"{num(t.get('token_attempts'), 0)} |"
        )
    total = result.get(total_key) or {}
    if not total:
        total = total_device_totals(stages) if stage_key == "device_mqtt_totals" else total_app_totals(stages)
    lines.append(
        f"| total | {num(total.get('token_first_attempt_success'), 0)} | "
        f"{num(total.get('token_first_attempt_fail'), 0)} | {num(total.get('token_retry_attempts'), 0)} | "
        f"{num(total.get('token_retry_success'), 0)} | {num(total.get('token_retry_exhausted'), 0)} | "
        f"{num(total.get('token_attempts'), 0)} |"
    )
    return "\n".join(lines)

def app_user_totals():
    stages = result.get("stage_results") or []
    lines = [
        "| Window | Login attempts | Login success | Login fail | Token ok/fail | Dial ok/fail | CONNACK ok/fail | List devices | Read shadow | Desired writes | Received ACKs | Bytes sent | Bytes received |",
        "| --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: |",
    ]
    for s in stages:
        t = s.get("app_user_totals") or {}
        lines.append(
            f"| {md(s.get('name'))} | {num(t.get('login_attempts'), 0)} | {num(t.get('login_success'), 0)} | "
            f"{num(t.get('login_fail'), 0)} | {num(t.get('token_success'), 0)}/{num(t.get('token_fail'), 0)} | "
            f"{num(t.get('mqtt_dial_success'), 0)}/{num(t.get('mqtt_dial_fail'), 0)} | "
            f"{num(t.get('mqtt_connack_success'), 0)}/{num(t.get('mqtt_connack_fail'), 0)} | "
            f"{num(t.get('list_devices_requests'), 0)} | "
            f"{num(t.get('read_shadow_requests'), 0)} | {num(t.get('desired_writes'), 0)} | "
            f"{num(t.get('received_acks'), 0)} | {num(t.get('bytes_sent'), 0)} | {num(t.get('bytes_received'), 0)} |"
        )
    total = result.get("app_user_totals") or total_app_totals(stages)
    lines.append(
        f"| total | {num(total.get('login_attempts'), 0)} | {num(total.get('login_success'), 0)} | "
        f"{num(total.get('login_fail'), 0)} | {num(total.get('token_success'), 0)}/{num(total.get('token_fail'), 0)} | "
        f"{num(total.get('mqtt_dial_success'), 0)}/{num(total.get('mqtt_dial_fail'), 0)} | "
        f"{num(total.get('mqtt_connack_success'), 0)}/{num(total.get('mqtt_connack_fail'), 0)} | "
        f"{num(total.get('list_devices_requests'), 0)} | "
        f"{num(total.get('read_shadow_requests'), 0)} | {num(total.get('desired_writes'), 0)} | "
        f"{num(total.get('received_acks'), 0)} | {num(total.get('bytes_sent'), 0)} | {num(total.get('bytes_received'), 0)} |"
    )
    return "\n".join(lines)

def all_server_counters():
    counters = defaultdict(int)
    evidence = result.get("server_evidence") or {}
    for source in (evidence.get("sources") or {}).values():
        for key, value in ((source or {}).get("counters") or {}).items():
            counters[str(key)] += int(num(value, 0))
    return counters

def all_phase_metrics():
    stages = result.get("stage_results") or []
    totals = defaultdict(lambda: defaultdict(int))
    rows = []
    for stage in stages:
        stage_name = stage.get("name")
        for phase, metric in sorted((stage.get("phase_metrics") or {}).items()):
            m = metric or {}
            attempts = int(num(m.get("attempts"), 0))
            success = int(num(m.get("success"), 0))
            fail = int(num(m.get("fail"), 0))
            total_ms = int(num(m.get("total_ms"), 0))
            avg_ms = int(total_ms / attempts) if attempts else 0
            row = {
                "stage": stage_name,
                "phase": phase,
                "attempts": attempts,
                "success": success,
                "fail": fail,
                "total_ms": total_ms,
                "avg_ms": avg_ms,
                "max_ms": int(num(m.get("max_ms"), 0)),
                "gt1s": int(num(m.get("gt1s"), 0)),
                "gt5s": int(num(m.get("gt5s"), 0)),
                "gt10s": int(num(m.get("gt10s"), 0)),
            }
            rows.append(row)
            for key, value in row.items():
                if key in {"stage", "phase", "max_ms"}:
                    continue
                else:
                    totals[phase][key] += int(value)
            totals[phase]["max_ms"] = max(totals[phase]["max_ms"], row["max_ms"])
    return rows, totals

def client_phase_metrics():
    rows, totals = all_phase_metrics()
    if not rows:
        return "- no client phase metrics"
    lines = [
        "| Stage | Phase | Attempts | Success | Fail | Avg ms | Max ms | >1s | >5s | >10s |",
        "| --- | --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: |",
    ]
    for row in rows:
        lines.append(
            f"| {md(row['stage'])} | {md(row['phase'])} | {row['attempts']} | {row['success']} | {row['fail']} | "
            f"{row['avg_ms']} | {row['max_ms']} | {row['gt1s']} | {row['gt5s']} | {row['gt10s']} |"
        )
    if len(rows) > 1:
        for phase in sorted(totals):
            t = totals[phase]
            avg_ms = int(t["total_ms"] / t["attempts"]) if t["attempts"] else 0
            lines.append(
                f"| total | {md(phase)} | {t['attempts']} | {t['success']} | {t['fail']} | "
                f"{avg_ms} | {t['max_ms']} | {t['gt1s']} | {t['gt5s']} | {t['gt10s']} |"
            )
    return "\n".join(lines)

def bottleneck_events():
    events = []
    for stage in result.get("stage_results") or []:
        for event in stage.get("bottleneck_events") or []:
            copied = dict(event or {})
            copied.setdefault("stage", stage.get("name"))
            events.append(copied)
    if not events:
        return "- no bottleneck event samples"
    lines = [
        "| Stage | Phase | Actor | Device | Detail | Elapsed ms | Remaining ms | Attempt | Retry | MQTT target |",
        "| --- | --- | --- | --- | --- | ---: | ---: | ---: | --- | --- |",
    ]
    for event in events[:20]:
        lines.append(
            f"| {md(event.get('stage'))} | {md(event.get('phase'))} | {md(event.get('actor'))} | "
            f"{md(event.get('device_id'))} | {md(event.get('detail'))} | {num(event.get('elapsed_ms'), 0)} | "
            f"{num(event.get('remaining_ms'), 0)} | {num(event.get('attempt'), 0)} | "
            f"{str(bool(event.get('is_retry'))).lower()} | {md(event.get('mqtt_target'))} |"
        )
    if len(events) > 20:
        lines.append(f"- omitted {len(events) - 20} additional bounded samples")
    return "\n".join(lines)

def metric_fail(totals, *phases):
    return sum(int(num(totals.get(phase, {}).get("fail"), 0)) for phase in phases)

def metric_gt(totals, key, *phases):
    return sum(int(num(totals.get(phase, {}).get(key), 0)) for phase in phases)

def counter_sum(counters, *names):
    return sum(int(num(counters.get(name), 0)) for name in names)

def bottleneck_summary():
    _, phase_totals = all_phase_metrics()
    counters = all_server_counters()
    device_total = result.get("device_mqtt_totals") or total_device_totals(result.get("stage_results") or [])
    app_total = result.get("app_user_totals") or total_app_totals(result.get("stage_results") or [])
    health = result.get("load_generator_health") or {}
    candidates = []

    token_support = []
    token_score = 0
    token_fail = metric_fail(phase_totals, "device_request_token", "app_request_token")
    retry_exhausted = int(num(device_total.get("token_retry_exhausted"), 0)) + int(num(app_total.get("token_retry_exhausted"), 0))
    token_slow = metric_gt(phase_totals, "gt5s", "device_request_token", "app_request_token") + metric_gt(phase_totals, "gt10s", "device_request_token", "app_request_token") * 2
    api_5xx = counter_sum(counters, "video_cloud_api.request_token.status_5xx", "ingress_nginx.request_token.status_5xx")
    api_p95 = max(int(num(counters.get("video_cloud_api.request_token.duration_p95_ms"), 0)), int(num(counters.get("ingress_nginx.request_token.duration_p95_ms"), 0)))
    token_score += token_fail * 3 + retry_exhausted * 5 + token_slow + api_5xx * 4
    if api_p95 >= 5000:
        token_score += 5
    for label, value in [
        ("token_phase_fail", token_fail),
        ("token_retry_exhausted", retry_exhausted),
        ("token_slow_gt5_gt10_weighted", token_slow),
        ("video_cloud_api.request_token.status_5xx", int(num(counters.get("video_cloud_api.request_token.status_5xx"), 0))),
        ("ingress_nginx.request_token.status_5xx", int(num(counters.get("ingress_nginx.request_token.status_5xx"), 0))),
        ("request_token.duration_p95_ms", api_p95),
    ]:
        if value:
            token_support.append(f"{label}={value}")
    if token_score:
        candidates.append(("token bootstrap", token_score, token_support))

    shadow_support = []
    shadow_fail = metric_fail(phase_totals, "app_shadow_accepted_wait", "device_delta_wait", "app_delta_clear_wait")
    api_timeout = counter_sum(counters, "video_cloud_api.timeout", "video_cloud_api.socket_error", "emqx.conn_congestion")
    shadow_score = shadow_fail * 3 + api_timeout * 2
    for label, value in [
        ("shadow_wait_fail", shadow_fail),
        ("video_cloud_api.timeout", int(num(counters.get("video_cloud_api.timeout"), 0))),
        ("video_cloud_api.socket_error", int(num(counters.get("video_cloud_api.socket_error"), 0))),
        ("emqx.conn_congestion", int(num(counters.get("emqx.conn_congestion"), 0))),
    ]:
        if value:
            shadow_support.append(f"{label}={value}")
    if shadow_score:
        candidates.append(("shadow/API-to-MQTT path", shadow_score, shadow_support))

    broker_support = []
    broker_fail = metric_fail(phase_totals, "device_mqtt_dial", "device_mqtt_connack", "device_subscribe_delta", "app_mqtt_dial", "app_mqtt_connack")
    broker_pressure = 0
    for key, value in counters.items():
        if key.startswith("emqx.") and any(marker in key for marker in ["shutdown_discarded", "shutdown_ssl_closed", "shutdown_tcp_closed"]):
            broker_pressure += int(num(value, 0))
    broker_score = broker_fail * 3 + broker_pressure
    for label, value in [("mqtt_phase_fail", broker_fail), ("emqx_shutdown_or_discarded", broker_pressure)]:
        if value:
            broker_support.append(f"{label}={value}")
    if broker_score:
        candidates.append(("broker path", broker_score, broker_support))

    generator_support = []
    generator_score = 0
    if health.get("saturated"):
        generator_score += 5
        generator_support.append("load_generator_health.saturated=true")
    for reason in health.get("reasons") or []:
        generator_score += 2
        generator_support.append(f"reason={reason}")
    if not candidates and generator_score == 0:
        coverage = client_target_coverage()
        if "insufficient-client-load" in coverage:
            generator_score = 1
            generator_support.append("missing target coverage with low server-side pressure")
    if generator_score:
        candidates.append(("generator path", generator_score, generator_support))

    if not candidates:
        return "- no strong bottleneck candidate from available counters"
    candidates.sort(key=lambda item: (-item[1], item[0]))
    lines = [
        "| Rank | Candidate | Score | Supporting counters |",
        "| ---: | --- | ---: | --- |",
    ]
    for idx, (name, score, support) in enumerate(candidates[:3], start=1):
        lines.append(f"| {idx} | {md(name)} | {score} | {md(', '.join(support[:8]) or 'counter signal present')} |")
    return "\n".join(lines)

def per_type_mqtt_totals():
    totals = defaultdict(lambda: defaultdict(int))
    for stage in result.get("stage_results") or []:
        for name, values in (stage.get("device_type_totals") or {}).items():
            for key in [
                "telemetry_publishes", "event_publishes", "desired_writes",
                "delta_received", "reported_publishes", "bytes_sent", "bytes_received",
            ]:
                totals[name][key] += int(num((values or {}).get(key), 0))
    if not totals:
        return "- no per-device-type MQTT totals"
    lines = [
        "| Device type | Telemetry publishes | Event publishes | Desired writes | Delta received | Reported publishes | Bytes sent | Bytes received |",
        "| --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: |",
    ]
    for name in sorted(totals):
        t = totals[name]
        lines.append(
            f"| {md(name)} | {num(t.get('telemetry_publishes'), 0)} | {num(t.get('event_publishes'), 0)} | "
            f"{num(t.get('desired_writes'), 0)} | {num(t.get('delta_received'), 0)} | "
            f"{num(t.get('reported_publishes'), 0)} | {num(t.get('bytes_sent'), 0)} | "
            f"{num(t.get('bytes_received'), 0)} |"
        )
    return "\n".join(lines)

def sum_stage_int_maps(field):
    totals = defaultdict(int)
    for stage in result.get("stage_results") or []:
        for name, count in (stage.get(field) or {}).items():
            totals[name] += int(num(count, 0))
    return totals

def user_action_totals():
    totals = sum_stage_int_maps("user_action_totals")
    if not totals:
        return "- no user action totals"
    lines = [
        "| Action | Count |",
        "| --- | ---: |",
    ]
    for name in sorted(totals):
        lines.append(f"| {md(name)} | {totals[name]} |")
    return "\n".join(lines)

def usage_window_totals():
    totals = sum_stage_int_maps("usage_window_totals")
    if not totals:
        return "- no usage window totals"
    lines = [
        "| Usage window | Count |",
        "| --- | ---: |",
    ]
    for name in sorted(totals):
        lines.append(f"| {md(name)} | {totals[name]} |")
    return "\n".join(lines)

def metered_traffic_report():
    """Render generic service/metric totals and persist a machine-readable artifact."""
    event_path = out_dir / "billing_usage_events.json"
    rows = []
    evidence = "client_counter"
    if event_path.exists():
        with event_path.open() as f:
            payload = json.load(f)
        events = payload.get("events", payload) if isinstance(payload, (dict, list)) else []
        if isinstance(events, dict):
            events = [events]
        totals = defaultdict(lambda: {"quantity": 0, "events": 0})
        for event in events:
            if not isinstance(event, dict):
                continue
            service = str(event.get("service_code", "")).strip()
            brand_cloud_id = str(event.get("brand_cloud_id", "")).strip()
            for measurement in event.get("measurements") or []:
                metric = str(measurement.get("metric_code", "")).strip()
                unit = str(measurement.get("unit", "")).strip()
                if not service or not metric or not unit or not brand_cloud_id:
                    continue
                key = (service, metric, unit, brand_cloud_id)
                totals[key]["quantity"] += num(measurement.get("quantity"), 0)
                totals[key]["events"] += 1
        for (service, metric, unit, brand_cloud_id), total in sorted(totals.items()):
            rows.append({
                "service_code": service, "metric_code": metric, "unit": unit,
                "quantity": total["quantity"], "event_count": total["events"],
                "brand_cloud_id": brand_cloud_id, "evidence": "billing_usage_event",
            })
        evidence = "billing_usage_event" if rows else "client_counter"

    if not rows:
        stages = result.get("stage_results") or []
        device = total_device_totals(stages)
        app = total_app_totals(stages)
        brand = ((result.get("plan") or {}).get("conditions") or {}).get("brandname", "")
        rows = [
            {"service_code": "mqtt", "metric_code": "publish_bytes", "unit": "bytes",
             "quantity": device["bytes_sent"] + app["bytes_sent"], "event_count": 0,
             "brand_cloud_id": "not-captured", "dimensions": {"brandname": brand},
             "evidence": "client_counter"},
            {"service_code": "mqtt", "metric_code": "delivery_bytes", "unit": "bytes",
             "quantity": device["bytes_received"] + app["bytes_received"], "event_count": 0,
             "brand_cloud_id": "not-captured", "dimensions": {"brandname": brand},
             "evidence": "client_counter"},
        ]

    artifact = {
        "run_id": result.get("run_id", ""),
        "service_model": "generic_usage_event", "evidence": evidence,
        "source": "billing_usage_events.json" if event_path.exists() and evidence == "billing_usage_event" else "results.json",
        "rows": rows, "pricing_applied": False,
    }
    (out_dir / "billing-usage-report.json").write_text(json.dumps(artifact, indent=2, sort_keys=True) + "\n")
    lines = [
        f"- evidence: `{evidence}`",
        "- pricing applied: `false` (usage facts only; no invoice calculation)",
        "| Service | Metric | Unit | Quantity | Brand Cloud | Events |",
        "| --- | --- | --- | ---: | --- | ---: |",
    ]
    for row in rows:
        lines.append(
            f"| {md(row.get('service_code'))} | {md(row.get('metric_code'))} | {md(row.get('unit'))} | "
            f"{num(row.get('quantity'), 0)} | {md(row.get('brand_cloud_id'))} | {num(row.get('event_count'), 0)} |"
        )
    if evidence == "client_counter":
        lines.append("- Client-derived rows are a traffic sanity report; they are not proof that the billing log/ledger received the same usage.")
    else:
        lines.append("- Rows were aggregated from the generic billing usage event envelope; pricing and invoicing remain separate.")
    return "\n".join(lines)

def failure_reasons():
    stages = result.get("stage_results") or []
    totals = defaultdict(int)
    lines = [
        "| Window | Reason | Count |",
        "| --- | --- | ---: |",
    ]
    for stage in stages:
        reasons = stage.get("failure_reasons") or {}
        for reason in sorted(reasons):
            count = int(num(reasons.get(reason), 0))
            if count == 0:
                continue
            totals[reason] += count
            lines.append(f"| {md(stage.get('name'))} | {md(reason)} | {count} |")
    for reason in sorted(totals):
        lines.append(f"| total | {md(reason)} | {totals[reason]} |")
    if len(lines) == 2:
        return "- no classified client failure reasons"
    return "\n".join(lines)

def normalize_failure_detail(detail):
    text = "" if detail is None else str(detail).strip()
    lower = text.lower()
    if not text:
        return ""
    if "mqtt connack read:" in lower:
        return "mqtt connack read failed"
    if "mqtt connect write:" in lower:
        return "mqtt connect write failed"
    if "mqtt tls dial host=" in lower:
        return text
    if "mqtt tls dial:" in lower:
        return "mqtt tls dial timeout" if "i/o timeout" in lower else "mqtt tls dial failed"
    if "mqtt dial:" in lower:
        return "mqtt dial failed"
    if "device request_token:" in lower:
        if "context deadline exceeded" in lower:
            return "device request_token context deadline exceeded"
        if "i/o timeout" in lower:
            return "device request_token i/o timeout"
        return "device request_token failed"
    if "app request_token base_url=" in lower:
        return normalize_app_request_token_detail(text)
    if "write: broken pipe" in lower:
        return "mqtt write broken pipe"
    if "connection reset by peer" in lower:
        return "mqtt connection reset by peer"
    if "use of closed network connection" in lower:
        return "mqtt closed network connection"
    if "i/o timeout" in lower:
        return "request_token i/o timeout" if "request_token" in lower else "network i/o timeout"
    if "context deadline exceeded" in lower:
        return "request_token context deadline exceeded" if "request_token" in lower else "context deadline exceeded"
    if "unexpected eof" in lower or lower == "eof":
        return "request_token EOF" if "request_token" in lower else "network EOF"
    if "access_token" in lower or "bearer " in lower or "-----begin" in lower or "private key" in lower:
        return "redacted sensitive detail"
    return text

def extract_failure_field(detail, name):
    prefix = name + "="
    for field in str(detail).split():
        if field.startswith(prefix):
            return field[len(prefix):].rstrip(":")
    return ""

def normalize_app_request_token_detail(detail):
    base_url = extract_failure_field(detail, "base_url")
    timeout = extract_failure_field(detail, "timeout")
    parts = ["app request_token"]
    if base_url:
        parts.append(f"base_url={base_url}")
    if timeout:
        parts.append(f"timeout={timeout}")
    prefix = " ".join(parts)
    lower = str(detail).lower()
    if "context deadline exceeded" in lower:
        return prefix + ": context deadline exceeded"
    if "i/o timeout" in lower:
        return prefix + ": i/o timeout"
    if "connection refused" in lower:
        return prefix + ": connection refused"
    return prefix + ": failed"

def failure_details():
    stages = result.get("stage_results") or []
    totals = defaultdict(lambda: defaultdict(int))
    lines = [
        "| Window | Reason | Detail | Count |",
        "| --- | --- | --- | ---: |",
    ]
    for stage in stages:
        stage_rows = defaultdict(lambda: defaultdict(int))
        for reason, details in (stage.get("failure_details") or {}).items():
            for detail, count in (details or {}).items():
                normalized = normalize_failure_detail(detail)
                if not normalized:
                    continue
                value = int(num(count, 0))
                if value == 0:
                    continue
                stage_rows[reason][normalized] += value
                totals[reason][normalized] += value
        for reason in sorted(stage_rows):
            for detail, count in sorted(stage_rows[reason].items(), key=lambda item: (-item[1], item[0])):
                lines.append(f"| {md(stage.get('name'))} | {md(reason)} | {md(detail)} | {count} |")
    for reason in sorted(totals):
        for detail, count in sorted(totals[reason].items(), key=lambda item: (-item[1], item[0])):
            lines.append(f"| total | {md(reason)} | {md(detail)} | {count} |")
    if len(lines) == 2:
        return "- no classified client failure details"
    return "\n".join(lines)

def server_correlation():
    if is_video_only_run():
        return "- skipped for WebRTC-only workflow; MQTT/shadow server/client counter correlation was not part of this run"
    correlation = result.get("server_correlation") or {}
    lines = [f"- status: {md(correlation.get('status', 'unknown'))}"]
    for reason in correlation.get("reasons") or []:
        lines.append(f"- reason: {md(reason)}")
    checks = correlation.get("checks") or []
    if checks:
        lines.extend([
            "| Source | Counter | Client total | Server total | Delta | Tolerance | Status |",
            "| --- | --- | ---: | ---: | ---: | ---: | --- |",
        ])
        for c in checks:
            lines.append(
                f"| {md(c.get('source'))} | {md(c.get('counter'))} | {num(c.get('client_total'), 0)} | "
                f"{num(c.get('server_total'), 0)} | {num(c.get('delta'), 0)} | {num(c.get('tolerance'), 0)} | {md(c.get('status'))} |"
            )
    return "\n".join(lines)

def runtime_log_correlation():
    if is_video_only_run():
        return "- skipped for WebRTC-only workflow; MQTT/shadow runtime stream correlation was not part of this run"
    correlation = result.get("runtime_log_correlation") or {}
    if not correlation:
        return "- no runtime log stream correlation"
    lines = [
        f"- status: {md(correlation.get('status', 'unknown'))}",
        f"- client command events: {num(correlation.get('client_command_events'), 0)}",
        f"- server runtime streams: {num(correlation.get('server_runtime_streams'), 0)}",
        f"- missing streams: {num(correlation.get('missing_stream_count'), 0)}",
        f"- missing expected log sequences: {num(correlation.get('missing_sequence_count'), 0)}",
    ]
    missing_streams = correlation.get("missing_stream_samples") or []
    if missing_streams:
        lines.extend([
            "| Missing stream window | Device | Command | Runtime log stream |",
            "| --- | --- | --- | --- |",
        ])
        for row in missing_streams:
            lines.append(
                f"| {md(row.get('stage'))} | {md(row.get('device_id'))} | {md(row.get('command_id'))} | "
                f"{md(row.get('runtime_log_stream_id'))} |"
            )
    missing_seqs = correlation.get("missing_sequence_samples") or []
    if missing_seqs:
        lines.extend([
            "| Missing seq window | Device | Command | Runtime log stream | Seq | Source | Message |",
            "| --- | --- | --- | --- | ---: | --- | --- |",
        ])
        for row in missing_seqs:
            lines.append(
                f"| {md(row.get('stage'))} | {md(row.get('device_id'))} | {md(row.get('command_id'))} | "
                f"{md(row.get('runtime_log_stream_id'))} | {num(row.get('seq'), 0)} | "
                f"{md(row.get('source'))} | {md(row.get('message'))} |"
            )
    return "\n".join(lines)

def start_coordination():
    coordination = result.get("start_coordination") or {}
    vms = coordination.get("vms") or []
    lines = [
        f"- mode: {md(coordination.get('mode', 'unknown'))}",
        f"- ready barrier: {md(coordination.get('ready_barrier', '-'))}",
        f"- start delay ms: {num(coordination.get('start_delay_ms'), 0)}",
        f"- max start skew ms: {num(coordination.get('max_skew_ms'), 0)}",
    ]
    if not vms:
        lines.append("- no runner coordination telemetry")
        return "\n".join(lines)
    lines.extend([
        "",
        "| VM | IP | Status | Ready at | Start signal received | Stage started | First connect | Stage completed | Disconnects | Error |",
        "| --- | --- | --- | --- | --- | --- | --- | --- | ---: | --- |",
    ])
    for vm in vms:
        lines.append(
            f"| {md(vm.get('label'))} | {md(vm.get('ip', '-'))} | {md(vm.get('status', '-'))} | "
            f"{md(vm.get('ready_at', '-'))} | {md(vm.get('start_signal_received_at', '-'))} | "
            f"{md(vm.get('stage_started_at', '-'))} | {md(vm.get('first_connect_at', '-'))} | "
            f"{md(vm.get('stage_completed_at', '-'))} | {num(vm.get('coordinator_disconnect_count'), 0)} | {md(vm.get('error', '-'))} |"
        )
    return "\n".join(lines)

def client_target_coverage():
    stages = result.get("stage_results") or []
    if not stages:
        return "- no target results"
    total_devices = num(((result.get("plan") or {}).get("conditions") or {}).get("devices"), 100000)
    total_users = num(((result.get("plan") or {}).get("conditions") or {}).get("users"), 5000)
    lines = [
        "| Window | Target connects | Shard target | New connect attempts | New connect success | Active connections | Active subscriptions | Target users | APP desired writes | APP received ACKs | Coverage status |",
        "| --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | --- |",
    ]
    for stage in stages:
        target_devices = int(num(stage.get("connected_devices"), 0))
        shard_target = int(num(stage.get("shard_connected_devices"), 0))
        target_users = max(1, int(target_devices * total_users / total_devices)) if target_devices > 0 and total_devices > 0 else 0
        device = stage.get("device_mqtt_totals") or {}
        app = stage.get("app_user_totals") or {}
        connect_attempts = int(num(device.get("connect_attempts"), 0))
        connect_success = int(num(device.get("connect_success"), 0))
        active_connections = int(num(device.get("active_connections"), 0))
        active_subscriptions = int(num(device.get("active_subscriptions"), 0))
        subscribes = int(num(device.get("subscribes"), 0))
        desired_writes = int(num(app.get("desired_writes"), 0))
        received_acks = int(num(app.get("received_acks"), 0))
        ok = (
            active_connections >= target_devices and
            active_subscriptions >= target_devices and
            desired_writes >= target_users and
            received_acks >= target_users
        )
        status = "ok" if ok else "insufficient-client-load"
        lines.append(
            f"| {md(stage.get('name'))} | {target_devices} | {shard_target or '-'} | {connect_attempts} | {connect_success} | "
            f"{active_connections} | {active_subscriptions} | {target_users} | {desired_writes} | {received_acks} | {status} |"
        )
    return "\n".join(lines)

def load_machine_resource_usage():
    rows = read_tsv(out_dir / "resource-samples" / "load-vms.tsv")
    if not rows:
        return "- no load VM resource timeline found at `resource-samples/load-vms.tsv`"
    groups = defaultdict(list)
    for row in rows:
        groups[row.get("label") or "unknown"].append(row)
    first, last = first_last(rows)
    lines = [
        f"- sample window: {md(first)} -> {md(last)}",
        "",
        "| VM | Role | IP | Samples | CPU p95 | CPU max | Load1 max | Mem max | Disk max | RX p95 Mbps | RX max Mbps | TX p95 Mbps | TX max Mbps | Unreachable |",
        "| --- | --- | --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: |",
    ]
    for label in sorted(groups):
        group = groups[label]
        cpus = [pct_value(r.get("cpu_pct")) for r in group]
        load1 = [pct_value(r.get("load1")) for r in group]
        mem_pcts = []
        for r in group:
            used = pct_value(r.get("mem_used_mb"))
            total = pct_value(r.get("mem_total_mb"))
            if used is not None and total:
                mem_pcts.append(100.0 * used / total)
        disk_pcts = [pct_value(r.get("disk_pct")) for r in group]
        rx_mbps = [pct_value(r.get("rx_mbps")) for r in group]
        tx_mbps = [pct_value(r.get("tx_mbps")) for r in group]
        unreachable = sum(1 for r in group if (r.get("status") or "").lower() != "ok")
        lines.append(
            f"| {md(label)} | {md(group[-1].get('role'))} | {md(group[-1].get('ip'))} | {len(group)} | "
            f"{fmt_float(percentile(cpus, 95))}% | {fmt_float(max([v for v in cpus if v is not None], default=None))}% | "
            f"{fmt_float(max([v for v in load1 if v is not None], default=None))} | "
            f"{fmt_float(max(mem_pcts, default=None))}% | {fmt_float(max([v for v in disk_pcts if v is not None], default=None))}% | "
            f"{fmt_float(percentile(rx_mbps, 95))} | {fmt_float(max([v for v in rx_mbps if v is not None], default=None))} | "
            f"{fmt_float(percentile(tx_mbps, 95))} | {fmt_float(max([v for v in tx_mbps if v is not None], default=None))} | {unreachable} |"
        )
    return "\n".join(lines)

def k8s_node_resource_usage():
    rows = read_tsv(out_dir / "resource-samples" / "k8s-nodes.tsv")
    if not rows:
        return "- no Kubernetes node resource timeline found at `resource-samples/k8s-nodes.tsv`"
    groups = defaultdict(list)
    for row in rows:
        groups[row.get("name") or "unknown"].append(row)
    first, last = first_last(rows)
    lines = [
        f"- sample window: {md(first)} -> {md(last)}",
        "",
        "| Node | Samples | CPU p95 | CPU max | Mem p95 | Mem max | Unavailable |",
        "| --- | ---: | ---: | ---: | ---: | ---: | ---: |",
    ]
    node_aliases = {name: f"k8s-node-{idx:02d}" for idx, name in enumerate(sorted(groups), 1)}
    for name in sorted(groups):
        group = groups[name]
        cpus = [pct_value(r.get("cpu_pct")) for r in group]
        mems = [pct_value(r.get("mem_pct")) for r in group]
        unavailable = sum(1 for r in group if (r.get("status") or "").lower() != "ok")
        lines.append(
            f"| {node_aliases[name]} | {len(group)} | {fmt_float(percentile(cpus, 95))}% | "
            f"{fmt_float(max([v for v in cpus if v is not None], default=None))}% | "
            f"{fmt_float(percentile(mems, 95))}% | {fmt_float(max([v for v in mems if v is not None], default=None))}% | {unavailable} |"
        )
    return "\n".join(lines)

def sync_telemetry():
    vms = ((result.get("sync_telemetry") or {}).get("vms") or [])
    if not vms:
        return "- no sync telemetry"
    lines = [
        "| VM | Files transferred | Bytes transferred | Elapsed ms | Remote disk before | Remote disk after |",
        "| --- | ---: | ---: | ---: | --- | --- |",
    ]
    for vm in vms:
        lines.append(
            f"| {md(vm.get('label'))} | {num(vm.get('files_transferred'), 0)} | {num(vm.get('bytes_transferred'), 0)} | "
            f"{num(vm.get('elapsed_ms'), 0)} | {md(vm.get('remote_disk_before', '-'))} | {md(vm.get('remote_disk_after', '-'))} |"
        )
    return "\n".join(lines)

def server_evidence():
    evidence = result.get("server_evidence") or {}
    sources = evidence.get("sources") or {}
    lines = [f"- complete: {str(bool(evidence.get('complete'))).lower()}"]
    for name in sorted(sources):
        source = sources[name] or {}
        detail = f" detail={md(source.get('detail'))}" if source.get("detail") else ""
        counters = source.get("counters") or {}
        counter_text = ""
        if counters:
            stream_counter_count = sum(1 for k in counters if str(k).startswith("runtime_log_stream."))
            visible = {k: v for k, v in counters.items() if not str(k).startswith("runtime_log_stream.")}
            counter_bits = [f"{k}:{v}" for k, v in sorted(visible.items())]
            if stream_counter_count:
                counter_bits.append(f"runtime_log_stream.*:{stream_counter_count} counters")
            counter_text = " counters=" + ", ".join(counter_bits)
        lines.append(f"- {md(name)}: available={str(bool(source.get('available'))).lower()}{detail}{counter_text}")
    for note in evidence.get("notes") or []:
        lines.append(f"- note: {md(note)}")
    postgres_samples = defaultdict(lambda: {"cpu": [], "memory": []})
    for source in sources.values():
        for sample in (source or {}).get("samples") or []:
            if sample.get("kind") != "k8s_pod_top":
                continue
            pod = sample.get("pod") or ""
            if "postgres" not in pod.lower():
                continue
            key = (sample.get("namespace") or "-", pod)
            postgres_samples[key]["cpu"].append(num(sample.get("cpu_millicores"), None))
            postgres_samples[key]["memory"].append(num(sample.get("memory_bytes"), None))
    if postgres_samples:
        lines.extend([
            "",
            "### Postgres Pod Resource Usage",
            "| Namespace | Pod | Samples | CPU p95 | Memory p95 |",
            "| --- | --- | ---: | ---: | ---: |",
        ])
        for namespace, pod in sorted(postgres_samples):
            group = postgres_samples[(namespace, pod)]
            lines.append(
                f"| {md(namespace)} | {md(pod)} | {len(group['cpu'])} | "
                f"{num(percentile(group['cpu'], 95), 0)}m | {fmt_mi(percentile(group['memory'], 95))} |"
            )
    return "\n".join(lines)

def report_source_artifacts():
    candidates = [
        "results.json",
        "server-evidence.json",
        "start-coordination.json",
        "sync-telemetry.json",
        "billing_usage_events.json",
        "billing-usage-report.json",
        "workflow-status.log",
        "resource-samples/load-vms.tsv",
        "resource-samples/k8s-nodes.tsv",
        "shards/<vm-label>/ota-devices.jsonl",
    ]
    lines = []
    for rel in candidates:
        if "<vm-label>" in rel:
            status = "present" if any((out_dir / "shards").glob("*/ota-devices.jsonl")) else "missing"
        else:
            status = "present" if (out_dir / rel).exists() else "missing"
        lines.append(f"- `{rel}`: {status}")
    return "\n".join(lines)

replacements = {
	"REPORT_TITLE": report_title(),
    "RUN_ID": md(result.get("run_id", "")),
    "STATUS": md(result.get("status", "UNKNOWN")),
    "RESULT": md(result.get("result", "UNKNOWN")),
    "STATUS_SUMMARY": status_summary(),
    "ACCOUNT_ACTIVATION": account_activation(),
    "TEST_CONDITIONS": test_conditions(),
	"FIRMWARE_OTA_SIMULATION": firmware_ota_simulation(),
    "GATE_STANDARDS": gate_standards(),
    "VIDEO_LOAD_PROFILE": video_load_profile(),
    "SCENARIO_MIX": scenario_mix(),
    "DEVICE_TRAFFIC_PROFILES": device_traffic_profiles(),
    "USER_SCENARIO_PROFILES": user_scenario_profiles(),
    "STAGES": target_window_section(),
    "STAGE_RESULTS": stage_results(),
    "STAGE_DIAGNOSTICS": stage_diagnostics(),
    "CLIENT_TARGET_COVERAGE": client_target_coverage(),
    "DEVICE_MQTT_TOTALS": device_mqtt_totals(),
    "DEVICE_TOKEN_RETRY_TOTALS": token_retry_totals("Device Token Retry Totals", "device_mqtt_totals", "device_mqtt_totals"),
    "APP_USER_TOTALS": app_user_totals(),
    "APP_TOKEN_RETRY_TOTALS": token_retry_totals("APP Token Retry Totals", "app_user_totals", "app_user_totals"),
    "BOTTLENECK_SUMMARY": bottleneck_summary(),
    "CLIENT_PHASE_METRICS": client_phase_metrics(),
    "BOTTLENECK_EVENTS": bottleneck_events(),
    "PER_TYPE_MQTT_TOTALS": per_type_mqtt_totals(),
    "USER_ACTION_TOTALS": user_action_totals(),
    "USAGE_WINDOW_TOTALS": usage_window_totals(),
    "METERED_TRAFFIC_REPORT": metered_traffic_report(),
    "FAILURE_REASONS": failure_reasons(),
    "FAILURE_DETAILS": failure_details(),
    "SERVER_CORRELATION": server_correlation(),
    "RUNTIME_LOG_CORRELATION": runtime_log_correlation(),
    "START_COORDINATION": start_coordination(),
    "LOAD_MACHINE_RESOURCE_USAGE": load_machine_resource_usage(),
    "K8S_NODE_RESOURCE_USAGE": k8s_node_resource_usage(),
    "SYNC_TELEMETRY": sync_telemetry(),
    "SERVER_EVIDENCE": server_evidence(),
    "REPORT_SOURCE_ARTIFACTS": report_source_artifacts(),
}

report = template
for key, value in replacements.items():
    report = report.replace("{{" + key + "}}", value)
unresolved = re.findall(r"\{\{[A-Z0-9_]+\}\}", report)
if unresolved:
    raise SystemExit(f"unresolved template markers: {', '.join(sorted(set(unresolved)))}")

(out_dir / "test_report.md").write_text(report.rstrip() + "\n")
print(f"report_file={out_dir / 'test_report.md'}", file=sys.stderr)
PY
