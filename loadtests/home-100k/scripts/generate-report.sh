#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)"
template="$repo_root/loadtests/home-100k/reports/templates/TEST_REPORT.md.tmpl"
out_dir=""

usage() {
  cat <<EOF
usage: $(basename "$0") --out-dir <run-artifact-dir> [--template <template-file>]

Reads results.json, server-evidence.json, sync telemetry, workflow logs, and
resource-samples/*.tsv from the run artifact directory, then writes the fixed
format TEST_REPORT.md.
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
    conditions = ((result.get("plan") or {}).get("conditions") or {})
    lines = [
        f"- Env root: `{md(conditions.get('env_root', '-'))}`",
        f"- Brand: `{md(conditions.get('brandname', '-'))}`",
        f"- Region: `{md(conditions.get('region', '-'))}`",
        f"- Devices: {num(conditions.get('devices'), 0)}",
        f"- Users: {num(conditions.get('users'), 0)}",
        f"- Devices per user: {num(conditions.get('devices_per_user'), 0)}",
        f"- Runner nofile limit: {num(conditions.get('runner_nofile_limit'), 0)}",
        f"- Device session model: `{md(conditions.get('device_session_model', '-'))}`",
        f"- Runner read model: `{md(conditions.get('runner_read_model', '-'))}`",
        "- Runner read requirement: sustained MQTT reads through Go netpoll-backed connections and bounded per-device reader goroutines; command-time one-shot reads are not valid for capacity conclusions.",
    ]
    return "\n".join(lines)

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
        "| VM | Role | IP | Samples | CPU p95 | CPU max | Load1 max | Mem max | Disk max | Unreachable |",
        "| --- | --- | --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: |",
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
        unreachable = sum(1 for r in group if (r.get("status") or "").lower() != "ok")
        lines.append(
            f"| {md(label)} | {md(group[-1].get('role'))} | {md(group[-1].get('ip'))} | {len(group)} | "
            f"{fmt_float(percentile(cpus, 95))}% | {fmt_float(max([v for v in cpus if v is not None], default=None))}% | "
            f"{fmt_float(max([v for v in load1 if v is not None], default=None))} | "
            f"{fmt_float(max(mem_pcts, default=None))}% | {fmt_float(max([v for v in disk_pcts if v is not None], default=None))}% | {unreachable} |"
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
    for name in sorted(groups):
        group = groups[name]
        cpus = [pct_value(r.get("cpu_pct")) for r in group]
        mems = [pct_value(r.get("mem_pct")) for r in group]
        unavailable = sum(1 for r in group if (r.get("status") or "").lower() != "ok")
        lines.append(
            f"| {md(name)} | {len(group)} | {fmt_float(percentile(cpus, 95))}% | "
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
        "workflow-status.log",
        "resource-samples/load-vms.tsv",
        "resource-samples/k8s-nodes.tsv",
    ]
    lines = []
    for rel in candidates:
        path = out_dir / rel
        status = "present" if path.exists() else "missing"
        lines.append(f"- `{rel}`: {status}")
    return "\n".join(lines)

replacements = {
    "RUN_ID": md(result.get("run_id", "")),
    "STATUS": md(result.get("status", "UNKNOWN")),
    "RESULT": md(result.get("result", "UNKNOWN")),
    "STATUS_SUMMARY": status_summary(),
    "TEST_CONDITIONS": test_conditions(),
    "GATE_STANDARDS": gate_standards(),
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

(out_dir / "TEST_REPORT.md").write_text(report.rstrip() + "\n")
print(f"report_file={out_dir / 'TEST_REPORT.md'}", file=sys.stderr)
PY
