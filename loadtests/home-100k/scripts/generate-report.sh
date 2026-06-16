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
        "connect_attempts", "connect_success", "connect_fail", "subscribes",
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
    lines = [f"- status: {md(result.get('status', 'UNKNOWN'))}"]
    correlation = result.get("server_correlation") or {}
    if correlation.get("status"):
        lines.append(f"- server correlation: {md(correlation.get('status'))}")
    for reason in correlation.get("reasons") or []:
        lines.append(f"- incomplete reason: {md(reason)}")
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
    ]
    return "\n".join(lines)

def render_map(title, values):
    lines = [f"- {title}:"]
    for key in sorted((values or {}).keys()):
        lines.append(f"  - {md(key)}: {num(values.get(key), 0)}")
    return lines

def scenario_mix():
    plan = result.get("plan") or {}
    lines = []
    lines.extend(render_map("Device mix", plan.get("device_mix") or {}))
    lines.extend(render_map("Presence mix", plan.get("presence_mix") or {}))
    return "\n".join(lines)

def stages_section():
    plan = result.get("plan") or {}
    rows = []
    for stage in plan.get("stages") or []:
        rows.append(
            f"- {md(stage.get('name'))}: {num(stage.get('connected_devices'), 0)} connected devices, "
            f"warm-up {md(stage.get('warm_up', '-'))}, steady {md(stage.get('steady_state', '-'))}, "
            f"cool-down {md(stage.get('cool_down', '-'))}"
        )
    return lines_or_dash(rows)

def stage_results():
    stages = result.get("stage_results") or []
    if not stages:
        return "- no stage results"
    lines = [
        "| Stage | Devices | MQTT connect | Reconnects | Shadow get p95 | Desired update p95 | Delta receive p95 | Desired->reported p95 | Offline desired p95 | Delta clear | Conflicts | Rejected | Auth violations | Client tokens | Duplicate apply |",
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

def device_mqtt_totals():
    stages = result.get("stage_results") or []
    lines = [
        "| Stage | Connect attempts | Connect success | Connect fail | Subscribes | Publishes | Received | Delta received | Reported publishes | Rejected publishes | Bytes sent | Bytes received |",
        "| --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: |",
    ]
    for s in stages:
        t = s.get("device_mqtt_totals") or {}
        lines.append(
            f"| {md(s.get('name'))} | {num(t.get('connect_attempts'), 0)} | {num(t.get('connect_success'), 0)} | "
            f"{num(t.get('connect_fail'), 0)} | {num(t.get('subscribes'), 0)} | {num(t.get('publishes'), 0)} | "
            f"{num(t.get('received_messages'), 0)} | {num(t.get('delta_received'), 0)} | {num(t.get('reported_publishes'), 0)} | "
            f"{num(t.get('rejected_publishes'), 0)} | {num(t.get('bytes_sent'), 0)} | {num(t.get('bytes_received'), 0)} |"
        )
    total = result.get("device_mqtt_totals") or total_device_totals(stages)
    lines.append(
        f"| total | {num(total.get('connect_attempts'), 0)} | {num(total.get('connect_success'), 0)} | "
        f"{num(total.get('connect_fail'), 0)} | {num(total.get('subscribes'), 0)} | {num(total.get('publishes'), 0)} | "
        f"{num(total.get('received_messages'), 0)} | {num(total.get('delta_received'), 0)} | {num(total.get('reported_publishes'), 0)} | "
        f"{num(total.get('rejected_publishes'), 0)} | {num(total.get('bytes_sent'), 0)} | {num(total.get('bytes_received'), 0)} |"
    )
    return "\n".join(lines)

def app_user_totals():
    stages = result.get("stage_results") or []
    lines = [
        "| Stage | Login attempts | Login success | Login fail | List devices | Read shadow | Desired writes | Received ACKs | Bytes sent | Bytes received |",
        "| --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: |",
    ]
    for s in stages:
        t = s.get("app_user_totals") or {}
        lines.append(
            f"| {md(s.get('name'))} | {num(t.get('login_attempts'), 0)} | {num(t.get('login_success'), 0)} | "
            f"{num(t.get('login_fail'), 0)} | {num(t.get('list_devices_requests'), 0)} | "
            f"{num(t.get('read_shadow_requests'), 0)} | {num(t.get('desired_writes'), 0)} | "
            f"{num(t.get('received_acks'), 0)} | {num(t.get('bytes_sent'), 0)} | {num(t.get('bytes_received'), 0)} |"
        )
    total = result.get("app_user_totals") or total_app_totals(stages)
    lines.append(
        f"| total | {num(total.get('login_attempts'), 0)} | {num(total.get('login_success'), 0)} | "
        f"{num(total.get('login_fail'), 0)} | {num(total.get('list_devices_requests'), 0)} | "
        f"{num(total.get('read_shadow_requests'), 0)} | {num(total.get('desired_writes'), 0)} | "
        f"{num(total.get('received_acks'), 0)} | {num(total.get('bytes_sent'), 0)} | {num(total.get('bytes_received'), 0)} |"
    )
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
        return "- no stage results"
    total_devices = num(((result.get("plan") or {}).get("conditions") or {}).get("devices"), 100000)
    total_users = num(((result.get("plan") or {}).get("conditions") or {}).get("users"), 5000)
    lines = [
        "| Stage | Target devices | Device connect attempts | Device connect success | Device subscribes | Target users | APP desired writes | APP received ACKs | Coverage status |",
        "| --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: | --- |",
    ]
    for stage in stages:
        target_devices = int(num(stage.get("connected_devices"), 0))
        target_users = max(1, int(target_devices * total_users / total_devices)) if target_devices > 0 and total_devices > 0 else 0
        device = stage.get("device_mqtt_totals") or {}
        app = stage.get("app_user_totals") or {}
        connect_attempts = int(num(device.get("connect_attempts"), 0))
        connect_success = int(num(device.get("connect_success"), 0))
        subscribes = int(num(device.get("subscribes"), 0))
        desired_writes = int(num(app.get("desired_writes"), 0))
        received_acks = int(num(app.get("received_acks"), 0))
        ok = (
            connect_attempts >= target_devices and
            connect_success >= target_devices and
            subscribes >= target_devices and
            desired_writes >= target_users and
            received_acks >= target_users
        )
        status = "ok" if ok else "insufficient-client-load"
        lines.append(
            f"| {md(stage.get('name'))} | {target_devices} | {connect_attempts} | {connect_success} | "
            f"{subscribes} | {target_users} | {desired_writes} | {received_acks} | {status} |"
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
            counter_text = " counters=" + ", ".join(f"{k}:{v}" for k, v in sorted(counters.items()))
        lines.append(f"- {md(name)}: available={str(bool(source.get('available'))).lower()}{detail}{counter_text}")
    for note in evidence.get("notes") or []:
        lines.append(f"- note: {md(note)}")
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
    "STATUS_SUMMARY": status_summary(),
    "TEST_CONDITIONS": test_conditions(),
    "SCENARIO_MIX": scenario_mix(),
    "STAGES": stages_section(),
    "STAGE_RESULTS": stage_results(),
    "CLIENT_TARGET_COVERAGE": client_target_coverage(),
    "DEVICE_MQTT_TOTALS": device_mqtt_totals(),
    "APP_USER_TOTALS": app_user_totals(),
    "SERVER_CORRELATION": server_correlation(),
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
