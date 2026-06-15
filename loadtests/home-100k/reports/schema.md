# 100K Home Report Schema

The first implementation may emit Markdown and JSON, but both formats must
carry the same sections.

## Required Top-Level Fields

- `run_id`
- `status`: `PASS`, `FAIL`, or `INCOMPLETE`
- `conditions`
- `device_mix`
- `presence_mix`
- `stages`
- `device_metrics`
- `user_metrics`
- `shadow_metrics`
- `server_evidence`
- `load_generator_health`
- `bottleneck_assessment`
- `client_token_correlation_count`

## Current Artifact Files

The `home-100k run` command writes these files under `--out-dir`:

- `plan.json`
- `results.json`
- `server-evidence.json`
- `TEST_REPORT.md`

`results.json` includes the selected plan, per-stage results, server evidence
summary, and paths to the generated artifacts.

Shard `results.json` files under `shards/<vm-label>/` must include:

- `run_id`
- `role`
- `shard_index`
- `stage_results`
- `load_generator_health`

`aggregate` reads all shard `load_generator_health` sections. Any saturated
shard forces the run-level status to `INCOMPLETE`.

## Required Stage Metrics

Each stage result must include:

- `mqtt_connect_success_rate_percent`
- `mqtt_reconnect_count`
- `shadow_get_p50_ms`
- `shadow_get_p95_ms`
- `shadow_get_p99_ms`
- `desired_update_p95_ms`
- `delta_receive_p95_ms`
- `desired_reported_p95_ms`
- `offline_desired_p95_ms`
- `desired_reported_convergence_rate_percent`
- `offline_desired_convergence_rate_percent`
- `delta_clear_success_rate_percent`
- `duplicate_apply_count`
- `version_conflict_count`
- `rejected_update_count`
- `authorization_violation_count`
- `client_token_correlation_count`

## Required Status Rules

- Missing IoT Device Shadow evidence sets `status=INCOMPLETE`.
- Missing server evidence sets `status=INCOMPLETE`.
- Load-generator saturation sets `status=INCOMPLETE`.
- Shadow convergence below the selected threshold sets `status=FAIL`.
- Authorization bypass or cross-user access success sets `status=FAIL`.

## Required Server Evidence Sources

- `emqx`
- `video_cloud_api`
- `iot_device_shadow`
- `postgres`
- `redis_valkey`
- `ingress_nginx`
- `host_pod_resources`

## Required Redaction

Reports must not include passwords, bearer tokens, refresh tokens, private keys,
certificate PEM bodies, raw secret env values, or wildcard MQTT credentials.
