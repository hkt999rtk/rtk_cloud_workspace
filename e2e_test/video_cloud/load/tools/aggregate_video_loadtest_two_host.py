#!/usr/bin/env python3
"""Aggregate per-host rtk-video-loadtest results into a two-host report."""

from __future__ import annotations

import argparse
import json
from pathlib import Path
from typing import Any


def _load_json(path: Path) -> dict[str, Any]:
    with path.open("r", encoding="utf-8") as handle:
        data = json.load(handle)
    if not isinstance(data, dict):
        raise ValueError(f"{path} does not contain a JSON object")
    return data


def _parse_role_path(value: str) -> tuple[str, Path]:
    if "=" not in value:
        raise argparse.ArgumentTypeError("expected ROLE=PATH")
    role, raw_path = value.split("=", 1)
    role = role.strip()
    if not role:
        raise argparse.ArgumentTypeError("role must not be empty")
    return role, Path(raw_path)


def _metric(result: dict[str, Any], key: str, default: Any = 0) -> Any:
    summary = result.get("summary")
    if not isinstance(summary, dict):
        return default
    return summary.get(key, default)


def _actors(result: dict[str, Any]) -> dict[str, dict[str, Any]]:
    actors = result.get("actors")
    if isinstance(actors, dict):
        return {str(k): v for k, v in actors.items() if isinstance(v, dict)}
    return {}


def _metadata(result: dict[str, Any]) -> dict[str, str]:
    metadata = result.get("metadata")
    if not isinstance(metadata, dict):
        return {}
    return {str(k): str(v) for k, v in metadata.items()}


def _redacted_metadata(metadata: dict[str, Any]) -> dict[str, str]:
    redacted: dict[str, str] = {}
    for key, value in metadata.items():
        lower = str(key).lower()
        if "token" in lower or "secret" in lower or "password" in lower:
            redacted[str(key)] = "<redacted>"
        else:
            redacted[str(key)] = str(value)
    return redacted


def _safe_cell(value: Any, limit: int = 120) -> str:
    text = str(value).replace("|", "\\|").replace("\n", " ").strip()
    if len(text) > limit:
        return text[: limit - 3] + "..."
    return text


def _owner_lifecycle_rows(results: list[tuple[str, Path, dict[str, Any]]]) -> list[tuple[str, str, str, int, int, int, str]]:
    wanted = {
        "device_websocket_owner",
        "websocket_snapshot_metadata",
        "websocket_snapshot_binary",
        "device_websocket_reconnect",
        "device_websocket_disconnect",
        "device_websocket_close",
        "device_websocket_error",
    }
    rows: dict[tuple[str, str, str], dict[str, Any]] = {}
    for role, _, result in results:
        operations = result.get("operations")
        if not isinstance(operations, list):
            continue
        for operation in operations:
            if not isinstance(operation, dict):
                continue
            name = str(operation.get("name", ""))
            if name not in wanted and "websocket" not in name:
                continue
            device_id = str(operation.get("device_id", ""))
            if not device_id:
                continue
            key = (device_id, name, role)
            row = rows.setdefault(key, {"pass": 0, "fail": 0, "skip": 0, "evidence": ""})
            if operation.get("skipped"):
                row["skip"] += 1
            elif operation.get("success"):
                row["pass"] += 1
            else:
                row["fail"] += 1
            evidence = operation.get("evidence") or operation.get("error_detail") or operation.get("skip_reason") or ""
            if evidence:
                row["evidence"] = _safe_cell(evidence)
    return [
        (device_id, name, role, int(values["pass"]), int(values["fail"]), int(values["skip"]), str(values["evidence"]))
        for (device_id, name, role), values in sorted(rows.items())
    ]


def _webrtc_media_device_rows(results: list[tuple[str, Path, dict[str, Any]]]) -> list[tuple[str, str, int, int, int, str]]:
    rows: dict[tuple[str, str], dict[str, Any]] = {}
    for role, _, result in results:
        operations = result.get("operations")
        if not isinstance(operations, list):
            continue
        for operation in operations:
            if not isinstance(operation, dict):
                continue
            name = str(operation.get("name", ""))
            if not name.startswith("webrtc_media_"):
                continue
            device_id = str(operation.get("device_id", ""))
            if not device_id:
                continue
            key = (device_id, role)
            row = rows.setdefault(key, {"pass": 0, "fail": 0, "skip": 0, "evidence": ""})
            if operation.get("skipped"):
                row["skip"] += 1
            elif operation.get("success"):
                row["pass"] += 1
            else:
                row["fail"] += 1
            evidence = operation.get("evidence") or operation.get("error_detail") or operation.get("skip_reason") or ""
            if evidence:
                row["evidence"] = _safe_cell(evidence)
    return [
        (device_id, role, int(values["pass"]), int(values["fail"]), int(values["skip"]), str(values["evidence"]))
        for (device_id, role), values in sorted(rows.items())
    ]


def _camera_recording_clip_rows(results: list[tuple[str, Path, dict[str, Any]]]) -> list[tuple[str, str, str, int, int, int, str]]:
    rows: dict[tuple[str, str, str], dict[str, Any]] = {}
    for role, _, result in results:
        operations = result.get("operations")
        if not isinstance(operations, list):
            continue
        for operation in operations:
            if not isinstance(operation, dict):
                continue
            name = str(operation.get("name", ""))
            if not (name.startswith("clip_") or name in {"recording_request", "recording_command_receive"}):
                continue
            device_id = str(operation.get("device_id", ""))
            if not device_id:
                continue
            key = (device_id, name, role)
            row = rows.setdefault(key, {"pass": 0, "fail": 0, "skip": 0, "evidence": ""})
            if operation.get("skipped"):
                row["skip"] += 1
            elif operation.get("success"):
                row["pass"] += 1
            else:
                row["fail"] += 1
            evidence = operation.get("evidence") or operation.get("error_detail") or operation.get("skip_reason") or ""
            if evidence:
                row["evidence"] = _safe_cell(evidence)
    return [
        (device_id, name, role, int(values["pass"]), int(values["fail"]), int(values["skip"]), str(values["evidence"]))
        for (device_id, name, role), values in sorted(rows.items())
    ]


def _mqtt_capability_from_name(name: str) -> str:
    if "air_conditioner" in name:
        return "air_conditioner"
    if "smart_meter" in name:
        return "smart_meter"
    if "light" in name:
        return "light"
    if name.startswith("mqtt_"):
        return "mqtt"
    return "unknown"


def _mqtt_iot_rows(results: list[tuple[str, Path, dict[str, Any]]]) -> list[tuple[str, str, str, int, int, int, str]]:
    rows: dict[tuple[str, str, str], dict[str, Any]] = {}
    for role, _, result in results:
        operations = result.get("operations")
        if not isinstance(operations, list):
            continue
        for operation in operations:
            if not isinstance(operation, dict):
                continue
            name = str(operation.get("name", ""))
            if not name.startswith("mqtt_"):
                continue
            if "broker_" in name and not any(capability in name for capability in ("light", "air_conditioner", "smart_meter")):
                continue
            device_id = str(operation.get("device_id", ""))
            if not device_id:
                continue
            capability = _mqtt_capability_from_name(name)
            key = (device_id, capability, role)
            row = rows.setdefault(key, {"pass": 0, "fail": 0, "skip": 0, "evidence": ""})
            if operation.get("skipped"):
                row["skip"] += 1
            elif operation.get("success"):
                row["pass"] += 1
            else:
                row["fail"] += 1
            evidence = operation.get("error_detail") or operation.get("evidence") or operation.get("skip_reason") or ""
            if evidence:
                row["evidence"] = _safe_cell(evidence)
    return [
        (device_id, capability, role, int(values["pass"]), int(values["fail"]), int(values["skip"]), str(values["evidence"]))
        for (device_id, capability, role), values in sorted(rows.items())
    ]


def _mqtt_readiness_rows(results: list[tuple[str, Path, dict[str, Any]]]) -> list[tuple[str, str, str, str, str]]:
    rows: list[tuple[str, str, str, str, str]] = []
    for role, _, result in results:
        operations = result.get("operations")
        if not isinstance(operations, list):
            continue
        for operation in operations:
            if not isinstance(operation, dict):
                continue
            name = str(operation.get("name", ""))
            if "ready" not in name and "command_receive" not in name and "status_receive" not in name:
                continue
            if not name.startswith("mqtt_"):
                continue
            device_id = str(operation.get("device_id", ""))
            if not device_id:
                continue
            if operation.get("skipped"):
                result_text = "SKIP"
                detail = operation.get("skip_reason") or ""
            elif operation.get("success"):
                result_text = "PASS"
                detail = operation.get("evidence") or ""
            else:
                result_text = "FAIL"
                detail = operation.get("error_detail") or operation.get("evidence") or ""
            rows.append((device_id, role, name, result_text, _safe_cell(detail)))
    return sorted(rows)


AGGREGATE_COVERAGE_FAMILIES: dict[str, list[str]] = {
    "auth": ["request_token", "refresh_token"],
    "app_http": ["get_statistics", "server_time", "version", "query_camera_activate"],
    "device_http": ["camera_event", "write_log", "retrieve_log", "start_video_record", "notify_camera"],
    "config": ["get_camera_info", "set_camera_info", "camera_read_conf", "camera_write_conf"],
    "webrtc": ["request_webrtc_create", "webrtc_setup", "request_webrtc_close"],
    "webrtc_media": [
        "webrtc_media_offer",
        "webrtc_media_answer",
        "webrtc_media_ice_connected",
        "webrtc_media_first_rtp",
        "webrtc_media_receive",
        "webrtc_media_close",
    ],
    "camera_recording_clip": [
        "recording_request",
        "recording_command_receive",
        "clip_upload",
        "clip_total",
        "clip_enum",
        "clip_info",
        "clip_download_range",
        "clip_download_invalid_range",
        "clip_delete",
    ],
    "owner_transport": ["device_websocket_owner"],
    "websocket_snapshot": ["websocket_snapshot_metadata", "websocket_snapshot_binary"],
    "mqtt": [
        "mqtt_connect",
        "mqtt_command_subscribe",
        "mqtt_state_publish",
        "mqtt_snapshot_publish",
        "mqtt_light_command_publish",
        "mqtt_light_command_receive",
        "mqtt_light_command_result_receive",
        "mqtt_light_state_report_receive",
        "mqtt_air_conditioner_command_publish",
        "mqtt_air_conditioner_command_receive",
        "mqtt_air_conditioner_command_result_receive",
        "mqtt_air_conditioner_state_report_receive",
        "mqtt_smart_meter_status_receive",
        "mqtt_smart_meter_telemetry_publish",
        "mqtt_smart_meter_telemetry_receive",
    ],
    "negative": ["negative_missing_bearer", "negative_wrong_scope", "negative_invalid_device", "negative_malformed_json", "negative_timeout"],
}


def _aggregate_operations(results: list[tuple[str, Path, dict[str, Any]]]) -> list[dict[str, Any]]:
    operations: list[dict[str, Any]] = []
    for _, _, result in results:
        raw_operations = result.get("operations")
        if not isinstance(raw_operations, list):
            continue
        operations.extend(operation for operation in raw_operations if isinstance(operation, dict))
    return operations


def _aggregate_coverage_matrix(results: list[tuple[str, Path, dict[str, Any]]]) -> dict[str, dict[str, Any]]:
    operations = _aggregate_operations(results)
    matrix: dict[str, dict[str, Any]] = {}
    for family, expected in AGGREGATE_COVERAGE_FAMILIES.items():
        expected_set = set(expected)
        covered = sorted({str(op.get("name", "")) for op in operations if str(op.get("name", "")) in expected_set})
        family_ops = [op for op in operations if str(op.get("name", "")) in expected_set]
        if not family_ops:
            matrix[family] = {"status": "NOT_RUN", "operations": [], "summary": "not exercised by this aggregate run"}
            continue
        if any((not op.get("success")) and not op.get("skipped") for op in family_ops):
            matrix[family] = {"status": "FAIL", "operations": covered, "summary": "one or more covered operations failed"}
            continue
        if any(op.get("skipped") for op in family_ops) or len(covered) < len(expected):
            matrix[family] = {
                "status": "SKIP",
                "operations": covered,
                "summary": f"partial aggregate coverage: {len(covered)}/{len(expected)} operations",
            }
            continue
        matrix[family] = {"status": "PASS", "operations": covered, "summary": "covered by aggregate two-host run"}
    return matrix


def render_report(
    results: list[tuple[str, Path, dict[str, Any]]],
    metadata: dict[str, dict[str, str]],
    *,
    output_title: str,
    server_commit: str,
    contracts_commit: str,
    binary_sha256: str,
) -> str:
    if not results:
        raise ValueError("at least one input result is required")

    run_ids = sorted({str(result.get("run_id", "")) for _, _, result in results if result.get("run_id")})
    api_urls = sorted(
        {
            str(result.get("config", {}).get("api_url", ""))
            for _, _, result in results
            if isinstance(result.get("config"), dict) and result.get("config", {}).get("api_url")
        }
    )
    contracts = sorted(
        {
            _metadata(result).get("contracts_commit", "")
            for _, _, result in results
            if _metadata(result).get("contracts_commit")
        }
    )
    client_commits = sorted(
        {
            _metadata(result).get("client_commit", "")
            for _, _, result in results
            if _metadata(result).get("client_commit")
        }
    )
    profiles = sorted({str(result.get("profile", "")) for _, _, result in results if result.get("profile")})

    total_ops = sum(int(_metric(result, "total_operations")) for _, _, result in results)
    successes = sum(int(_metric(result, "successes")) for _, _, result in results)
    failures = sum(int(_metric(result, "failures")) for _, _, result in results)
    skips = sum(int(_metric(result, "skips")) for _, _, result in results)
    success_rate = successes / total_ops if total_ops else 0.0

    lines = [
        f"# {output_title}",
        "",
        f"- Run ID: `{', '.join(run_ids) if run_ids else 'unknown'}`",
        f"- Profile: `{', '.join(profiles) if profiles else 'unknown'}`",
        f"- Server URL: `{', '.join(api_urls) if api_urls else 'unknown'}`",
        f"- Server commit: `{server_commit or 'unknown'}`",
        f"- Client commit: `{', '.join(client_commits) if client_commits else 'unknown'}`",
        f"- Contracts commit: `{contracts_commit or (', '.join(contracts) if contracts else 'unknown')}`",
        f"- Binary SHA256: `{binary_sha256 or 'unknown'}`",
        "",
        "## Aggregate Summary",
        "",
        "| Metric | Value |",
        "| --- | ---: |",
        f"| Total operations | {total_ops} |",
        f"| Successes | {successes} |",
        f"| Failures | {failures} |",
        f"| Skips | {skips} |",
        "| Blocks | 0 |",
        f"| Success rate | {success_rate * 100:.2f}% |",
        "",
        "## Host Results",
        "",
        "| Host role | Instance ID | Actors | Ops | Success | Fail | Skip | Success rate | Source |",
        "| --- | --- | --- | ---: | ---: | ---: | ---: | ---: | --- |",
    ]

    for role, path, result in results:
        cfg = result.get("config") if isinstance(result.get("config"), dict) else {}
        instance_id = str(result.get("instance_id", "unknown"))
        actors = str(cfg.get("actors") or metadata.get(role, {}).get("actors") or "unknown")
        ops = int(_metric(result, "total_operations"))
        ok = int(_metric(result, "successes"))
        fail = int(_metric(result, "failures"))
        skip = int(_metric(result, "skips"))
        rate = float(_metric(result, "success_rate", 0.0))
        lines.append(f"| {role} | `{instance_id}` | `{actors}` | {ops} | {ok} | {fail} | {skip} | {rate * 100:.2f}% | `{path}` |")

    lines.extend(["", "## Actor Totals", "", "| Actor | Ops | Success | Fail | Skip |", "| --- | ---: | ---: | ---: | ---: |"])
    actor_totals: dict[str, dict[str, int]] = {}
    for _, _, result in results:
        for actor, actor_metrics in _actors(result).items():
            total = actor_totals.setdefault(actor, {"ops": 0, "success": 0, "fail": 0, "skip": 0})
            total["ops"] += int(actor_metrics.get("operations", 0))
            total["success"] += int(actor_metrics.get("successes", 0))
            total["fail"] += int(actor_metrics.get("failures", 0))
            total["skip"] += int(actor_metrics.get("skips", 0))
    for actor in ("device", "app", "viewer"):
        total = actor_totals.get(actor, {"ops": 0, "success": 0, "fail": 0, "skip": 0})
        lines.append(f"| {actor} | {total['ops']} | {total['success']} | {total['fail']} | {total['skip']} |")

    lines.extend(["", "## Aggregate Coverage Matrix", "", "| Family | Status | Operations | Summary |", "| --- | --- | --- | --- |"])
    aggregate_coverage = _aggregate_coverage_matrix(results)
    family_order = [
        "auth",
        "app_http",
        "device_http",
        "config",
        "owner_transport",
        "websocket_snapshot",
        "webrtc",
        "webrtc_media",
        "camera_recording_clip",
        "mqtt",
        "negative",
    ]
    for family in family_order:
        item = aggregate_coverage.get(family, {"status": "NOT_RUN", "operations": [], "summary": ""})
        operations = ", ".join(f"`{op}`" for op in item.get("operations", [])) or "-"
        lines.append(f"| {family} | {item.get('status', 'NOT_RUN')} | {operations} | {_safe_cell(item.get('summary', ''))} |")

    lines.extend(
        [
            "",
            "## Device Owner Lifecycle",
            "",
            "| Device ID | Operation | Host role | Pass | Fail | Skip | Last evidence |",
            "| --- | --- | --- | ---: | ---: | ---: | --- |",
        ]
    )
    owner_rows = _owner_lifecycle_rows(results)
    if owner_rows:
        for device_id, name, role, passed, failed, skipped, evidence in owner_rows:
            lines.append(f"| `{device_id}` | `{name}` | {role} | {passed} | {failed} | {skipped} | {evidence or '-'} |")
    else:
        lines.append("| - | - | - | 0 | 0 | 0 | No device owner lifecycle operations recorded. |")

    lines.extend(
        [
            "",
            "## WebRTC Media Device Coverage",
            "",
            "| Device ID | Host role | Pass | Fail | Skip | Last evidence |",
            "| --- | --- | ---: | ---: | ---: | --- |",
        ]
    )
    media_rows = _webrtc_media_device_rows(results)
    if media_rows:
        for device_id, role, passed, failed, skipped, evidence in media_rows:
            lines.append(f"| `{device_id}` | {role} | {passed} | {failed} | {skipped} | {evidence or '-'} |")
    else:
        lines.append("| - | - | 0 | 0 | 0 | No WebRTC media operations recorded. |")

    lines.extend(
        [
            "",
            "## Camera Recording Clip Coverage",
            "",
            "| Device ID | Operation | Host role | Pass | Fail | Skip | Last evidence |",
            "| --- | --- | --- | ---: | ---: | ---: | --- |",
        ]
    )
    clip_rows = _camera_recording_clip_rows(results)
    if clip_rows:
        for device_id, name, role, passed, failed, skipped, evidence in clip_rows:
            lines.append(f"| `{device_id}` | `{name}` | {role} | {passed} | {failed} | {skipped} | {evidence or '-'} |")
    else:
        lines.append("| - | - | - | 0 | 0 | 0 | No camera recording clip operations recorded. |")

    lines.extend(
        [
            "",
            "## MQTT IoT Coverage",
            "",
            "| Device ID | Capability | Host role | Pass | Fail | Skip | Last detail |",
            "| --- | --- | --- | ---: | ---: | ---: | --- |",
        ]
    )
    mqtt_rows = _mqtt_iot_rows(results)
    if mqtt_rows:
        for device_id, capability, role, passed, failed, skipped, evidence in mqtt_rows:
            lines.append(f"| `{device_id}` | `{capability}` | {role} | {passed} | {failed} | {skipped} | {evidence or '-'} |")
    else:
        lines.append("| - | - | - | 0 | 0 | 0 | No MQTT IoT operations recorded. |")

    lines.extend(
        [
            "",
            "## MQTT Readiness Timeline",
            "",
            "| Device ID | Host role | Operation | Result | Detail |",
            "| --- | --- | --- | --- | --- |",
        ]
    )
    readiness_rows = _mqtt_readiness_rows(results)
    if readiness_rows:
        for device_id, role, operation, result_text, detail in readiness_rows:
            lines.append(f"| `{device_id}` | {role} | `{operation}` | {result_text} | {detail or '-'} |")
    else:
        lines.append("| - | - | - | - | No MQTT readiness operations recorded. |")

    if metadata:
        lines.extend(["", "## Host Metadata", ""])
        for role, values in sorted(metadata.items()):
            lines.append(f"### {role}")
            if not values:
                lines.append("")
                lines.append("- None")
            else:
                for key, value in sorted(values.items()):
                    lines.append(f"- `{key}`: `{value}`")
            lines.append("")

    return "\n".join(lines).rstrip() + "\n"


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--input", action="append", type=_parse_role_path, required=True, help="ROLE=load-results.json")
    parser.add_argument("--metadata", action="append", type=_parse_role_path, default=[], help="ROLE=metadata.json")
    parser.add_argument("--output", default="two-host-load-report.md", help="aggregate Markdown output path")
    parser.add_argument("--title", default="Two-Host rtk_video_cloud Load Test Report")
    parser.add_argument("--server-commit", default="")
    parser.add_argument("--contracts-commit", default="")
    parser.add_argument("--binary-sha256", default="")
    args = parser.parse_args(argv)

    results = [(role, path, _load_json(path)) for role, path in args.input]
    metadata = {role: _redacted_metadata(_load_json(path)) for role, path in args.metadata}
    report = render_report(
        results,
        metadata,
        output_title=args.title,
        server_commit=args.server_commit,
        contracts_commit=args.contracts_commit,
        binary_sha256=args.binary_sha256,
    )
    Path(args.output).write_text(report, encoding="utf-8")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
