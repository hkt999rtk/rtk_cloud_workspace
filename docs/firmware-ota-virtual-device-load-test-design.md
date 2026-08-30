# 10K Firmware OTA Virtual Device Load Test Design

Date: 2026-08-28

Status: approved for implementation on 2026-08-28

Audience: Video Cloud, firmware, OTA operations, QA, and staging load-test
owners.

## Summary

Add an `ota-device-simulator` load model to the existing Go
`scripts/go/cloud-mqtt-test` executable. The model simulates up to 10,000
firmware-upgrade devices while preserving the existing credential-bundle,
device-token, MQTT, sharding, and result-artifact conventions.

The simulator owns the authenticated device side of an already-created OTA
campaign. It does not publish firmware, create campaigns, activate campaigns,
or clean up operator resources. Each selected device maintains an MQTT session,
polls the canonical device OTA API, streams and verifies the assigned artifact,
reports the deployment lifecycle, disconnects for reboot, reconnects, and then
reports the observed target version.

The first qualification target is exactly 10,000 devices on one Linux load
generator. The same binary also supports deterministic horizontal sharding.

## Goals and non-goals

Goals:

- Prove that 10,000 provisioned devices can be online concurrently through the
  existing MQTT transport while completing canonical OTA deployments.
- Exercise assignment selection, rollout pacing, artifact authorization,
  artifact delivery, deployment-event persistence, reboot recovery, and final
  observed-version reporting.
- Verify every device against an expected campaign, target version, and
  deterministic terminal outcome. Partial or unexplained completion is not a
  pass.
- Produce aggregate and per-device evidence without exposing credentials,
  bearer tokens, or signed artifact URLs.
- Support reproducible download, verification, installation, reboot, and
  timeout failure injection.

Non-goals for v1:

- Creating, publishing, activating, pausing, resuming, or deleting releases and
  campaigns.
- Implementing an OTA command over MQTT. The current canonical OTA device
  contract is HTTP polling under `/v1/device/ota/*`; the MQTT connection proves
  online and post-reboot recovery behavior.
- Writing firmware images to disk, emulating flash partitions, bootloader logic,
  A/B slot contents, or real hardware health checks.
- Cryptographically verifying the manifest signature. The current device
  contract supplies signature metadata but no trusted public-key distribution
  source. V1 requires non-empty signing algorithm, key ID, and signature and
  verifies the artifact size and SHA-256. Public-key verification requires a
  separately approved trust-source design.
- Replacing the Home 100K orchestration or provisioning workflows.

## Existing components reused

The implementation remains in `scripts/go/cloud-mqtt-test` and reuses:

- the Home 100K SQLite credential bundle and legacy manifest-file loader;
- deterministic `--shard-index` and `--shard-count` assignment selection;
- mTLS `/request_token` bootstrap and token retry behavior;
- MQTT TLS connect, CONNACK, shadow-delta subscription, keepalive, and session
  close helpers;
- `--run-id`, `--seed`, `--ramp-up`, `--concurrency`,
  `--max-connected-devices`, and existing endpoint discovery;
- the existing `results.json`/`report.md` output path and failure redaction.

The selected inventory continues to require a bound device with the `mqtt`
service option because an online MQTT session is part of this test's acceptance
criteria. Video Cloud remains authoritative for resolving each device's brand
and canonical Product; the simulator never sends a brand or Product in a device OTA
request.

## Architecture

### Process model

One process loads the selected shard, constructs one lightweight state object
per device, and starts bounded work through shared semaphores:

| Resource | Default bound | Purpose |
| --- | ---: | --- |
| MQTT/token connection workers | existing `--concurrency`; 250 in `baseline-10k` | Ramp device token bootstrap and MQTT connections. |
| OTA HTTP workers | 250 | Bound check, artifact-token, and event calls. |
| Artifact download workers | 64 | Bound simultaneous full firmware streams. |

There is no HTTP transport per device. Device API calls use a shared transport
and the device's bearer token. Signed artifact URLs use a second shared
transport without an Authorization header. The MQTT session retains its device
token manager in memory so OTA calls reuse the token and reboot reconnect can
renew it. Tokens are never added to result structures or logs.

Each device is represented by an `otaDeviceSession` containing only its
assignment/certificate reference, token manager, MQTT session, OTA state,
expected injected outcome, and timing counters. A single result collector owns
aggregate mutation. This avoids locks on every counter and prevents concurrent
JSONL writes.

### Data flow

1. Load and validate the existing credential bundle, bindings, certificates,
   endpoints, shard selection, and required OTA flags.
2. Deterministically assign one expected outcome to every device using the
   seed, run ID, and device ID.
3. Ramp mTLS token bootstrap and MQTT connections. The ready barrier requires
   the selected device count, token-success count, CONNACK-success count, and
   subscription-success count to be identical.
4. Poll `POST /v1/device/ota/check` with device bearer authentication. Each
   device check is deterministically sampled from a truncated normal
   distribution between 10 and 60 seconds (mean 35 seconds) until the expected
   assignment arrives or the device deadline expires.
5. Reject an assignment immediately if `campaign_id` or `target_version` does
   not equal the required CLI values. Validate the manifest identity,
   hardware-revision compatibility, anti-rollback counter, artifact size,
   SHA-256 shape, and signature metadata.
6. Acquire a download slot and only then call
   `POST /v1/device/ota/deployments/{deployment_id}/artifact-token`. This avoids
   consuming the service's short-lived artifact URL while queued locally.
7. Report `downloading`, stream the complete artifact through SHA-256 without a
   local file, and verify byte count and digest against the immutable manifest.
8. Report `downloaded`, `installing`, and `rebooting` with configurable delays
   and deterministic jitter.
9. Close the MQTT session for reboot. On the successful path, renew/reuse the
   token, reconnect MQTT, require CONNACK and subscription success, report
   `verifying`, and finally report `succeeded` with the target version.
10. Collect the per-device result and write aggregate and detailed evidence.

### Polling and retry policy

`assigned` is the only check decision that advances a device. `deferred` and
`no_update` are both polled until the device deadline because campaign schedule,
phase percentage, rate limits, and concurrent limits can make assignment
temporarily unavailable. The latest decision and reason code are retained for
timeout evidence.

HTTP connection errors, timeouts, status 429, and status 5xx are retried with a
bounded exponential backoff and deterministic jitter while the device deadline
remains. Other 4xx responses are terminal unexpected failures.

Deployment event retry is safe only when the exact same sequence and complete
JSON payload, including `device_timestamp`, are reused. An event payload is
therefore constructed once and retained until it receives a successful
response. A retry never increments the sequence. The next state increments the
sequence by one.

Artifact downloads are not transparently resumed in v1. A transient failure
requests a fresh artifact token and restarts the full stream, subject to the
retry count and device deadline. The artifact token is never cached across
devices or exposed in evidence.

## Device state machine

The server generates sequence 1 for `offered`. The simulator begins at
sequence 2:

| Sequence | Reported status | Observed version | MQTT behavior |
| ---: | --- | --- | --- |
| 2 | `downloading` | current | Connected. |
| 3 | `downloaded`, progress 100 | current | Connected. |
| 4 | `installing` | current | Connected. |
| 5 | `rebooting` | current | Session is closed after acknowledgement. |
| 6 | `verifying` | target | Reconnect, CONNACK, and subscription must succeed first. |
| 7 | `succeeded` | target | Remains connected until run collection completes. |

Every event includes a UTC `device_timestamp`. Progress is 0 for
`downloading`, 100 for `downloaded`, and omitted for other states. A successful
deployment must observe the target version; reporting success with any other
version is a simulator error before the request is sent.

### Deterministic fault injection

Failure percentages are validated in the range 0 through 100 and their sum
must not exceed 100. A SHA-256 hash of `seed`, `run_id`, and `device_id` maps
each device into exactly one bucket, so devices cannot receive overlapping
faults and repeated runs with the same inputs select the same devices.

| Bucket | Injection point | Expected terminal status | MQTT outcome |
| --- | --- | --- | --- |
| Download failure | After `downloading`, before a valid full stream | `failed` | Stays connected. |
| Verify failure | After bytes are read, before `downloaded` | `failed` | Stays connected. |
| Install failure | After `installing` | `failed` | Stays connected. |
| Reboot failure | After `rebooting` and disconnect | `failed` | Must remain disconnected. |
| Timeout | After `rebooting` and disconnect | `timed_out` | Must remain disconnected. |
| No injection | Complete normal state machine | `succeeded` | Must reconnect. |

Injected terminal events use stable codes such as
`SIMULATED_DOWNLOAD_FAILURE`; an injected error is an expected outcome, not a
runner error. Natural transport, authentication, contract, checksum, or state
errors are always unexpected and fail the run.

The campaign must be configured so the selected injected failures do not pause
or cancel it before all devices receive assignments unless a later design adds
an explicit campaign-pause qualification mode. Unexpected campaign pause is
observed as assignment timeout and fails this v1 test.

## CLI contract

Select the new model with:

```text
--profile baseline-10k
--load-model ota-device-simulator
```

Required flags:

| Flag | Meaning |
| --- | --- |
| `--ota-campaign-id` | Exact campaign ID every assignment must contain. |
| `--ota-target-version` | Exact target version every assignment and successful event must use. |
| `--ota-current-version` | Initial observed version sent by every simulated device. |
| `--ota-hardware-revision` | Hardware revision sent to check and validated against the manifest. |

Optional flags and defaults:

| Flag | Default | Validation/behavior |
| --- | --- | --- |
| `--ota-anti-rollback-counter` | `0` | Non-negative integer. |
| `--ota-poll-min-interval` | `10s` | Positive lower bound for the per-device normal polling distribution. |
| `--ota-poll-max-interval` | `60s` | Positive upper bound; must exceed the lower bound. The distribution is centered at the midpoint with the bounds at three standard deviations. |
| `--ota-upgrade-timeout` | `30m` | Per-device deadline beginning after the MQTT ready barrier. |
| `--ota-http-concurrency` | `250` | Positive worker bound. |
| `--ota-download-concurrency` | `64` | Positive and no greater than HTTP concurrency. |
| `--ota-install-delay` | `2s` | Non-negative accelerated device delay. |
| `--ota-reboot-delay` | `2s` | Non-negative accelerated device delay. |
| `--ota-verify-delay` | `1s` | Non-negative accelerated device delay. |
| `--ota-stage-jitter-percent` | `20` | Range 0 through 100. |
| `--ota-download-failure-percent` | `0` | Mutually exclusive deterministic bucket. |
| `--ota-verify-failure-percent` | `0` | Mutually exclusive deterministic bucket. |
| `--ota-install-failure-percent` | `0` | Mutually exclusive deterministic bucket. |
| `--ota-reboot-failure-percent` | `0` | Mutually exclusive deterministic bucket. |
| `--ota-timeout-percent` | `0` | Mutually exclusive deterministic bucket. |

The model also honors existing `--seed`, `--run-id`, `--ramp-up`,
`--concurrency`, `--shard-index`, `--shard-count`,
`--max-connected-devices`, `--device-token-request-timeout`, and
`--device-token-request-retries`. It requires `--mqtt-probe=true` because MQTT
online/reboot evidence is mandatory.

Configuration errors are reported as `BLOCKED`. Live execution errors and
unexpected device outcomes are reported as `FAIL`. The process preserves the
existing output-writing behavior even when blocked or failed.

## Evidence and acceptance

### Aggregate result

`results.json` retains its existing top-level shape and adds an `ota` object:

```json
{
  "ota": {
    "campaign_id": "campaign-id",
    "target_version": "2.0.0",
    "devices_selected": 10000,
    "mqtt_ready": 10000,
    "assignments_received": 10000,
    "terminal_expected": 10000,
    "terminal_matched": 10000,
    "by_expected_terminal": {"succeeded": 10000},
    "by_actual_terminal": {"succeeded": 10000},
    "artifact_bytes": 10485760000,
    "artifact_hash_verified": 10000,
    "mqtt_reboot_disconnects": 10000,
    "mqtt_reconnect_successes": 10000,
    "unexpected_failures": 0
  }
}
```

The final implementation also includes per-operation request counts, response
status classes, retry counts, p50/p95/p99 latencies, artifact throughput,
latest polling reason codes, process peak goroutines, and Go heap statistics.
Metrics must not use device ID, deployment ID, or error text as label keys.

### Per-device result

`ota-devices.jsonl` contains one record per selected device and is written with
mode `0600`. Records are sorted by device ID before final write so a fixed
shard and seed produce stable evidence. Each record contains:

- device ID, device type, campaign ID, deployment ID, release ID;
- current/target version and expected/actual terminal status;
- final sequence, last check decision/reason, stage timestamps and latencies;
- downloaded byte count, computed SHA-256, and hash/size verification booleans;
- MQTT initial-connect, reboot-disconnect, and reconnect results;
- retry counters and a stable redacted error category/detail.

It never contains a certificate, private key, bearer/refresh token, Authorization
header, artifact token, or signed URL.

### PASS rule

A shard passes only when all of these equal the selected-device count:

- initial token, MQTT CONNACK, and subscription successes;
- assignments for the required campaign and target version;
- emitted per-device result rows;
- terminal outcomes matching the deterministic failure profile.

Additionally, every non-faulted device must have a valid full artifact size and
SHA-256, and every successful device must disconnect and reconnect MQTT before
reporting `verifying` and `succeeded`. Any missing, duplicate, mismatched, or
unexpected result makes the shard fail. Multi-shard qualification passes only
after an external aggregation confirms disjoint device IDs and an exact global
count of 10,000.

## Implementation plan after approval

1. Extend `loadOptions`, CLI parsing, validation, model selection, and result
   attachment in `scripts/go/cloud-mqtt-test/main.go`.
2. Add the OTA protocol/state-machine implementation in a focused
   `ota_device_simulator.go` file in the same `main` package. Refactor the
   sustained MQTT connection result only as needed to retain an in-memory token
   manager and support explicit reboot reconnect.
3. Add `ota_device_simulator_test.go` using `httptest` plus the existing fake
   MQTT helpers. Keep contract tests local to the existing module and add no
   third-party dependency.
4. Update CLI help and the relevant load-test documentation with canary,
   single-host 10K, and sharded examples.

## Test plan

Unit and component tests cover:

- option validation, required flags, duration parsing, and percentage totals;
- deterministic fault selection and disjoint deterministic shards;
- assigned, deferred, and no-update polling;
- wrong campaign, target, manifest, hardware revision, and anti-rollback data;
- complete artifact streaming, size mismatch, SHA-256 mismatch, token expiry,
  fresh-token restart, and no firmware file creation;
- exact event sequence and exact-payload idempotent retry;
- every injected fault and expected terminal classification;
- device deadline expiry and natural HTTP/authentication failures;
- initial MQTT ready barrier, successful reboot reconnect, and reconnect failure;
- redaction and exactly one JSONL record per selected device;
- aggregate count, latency, throughput, and runtime-stat calculation.

Repository checks after implementation:

```sh
(cd scripts/go && GOWORK=off go test ./cloud-mqtt-test)
(cd scripts/go && GOWORK=off go test -race ./cloud-mqtt-test)
```

The existing actor-separated, Home 100K sustained, and SDK device simulator
tests must remain green.

## Staging qualification

Qualification is intentionally separate from unit-test success:

1. Provision and bind an OTA-compatible canary inventory, publish an immutable
   artifact, and activate a campaign whose selectors and phase limits include
   the canary devices.
2. Run a small all-success canary and require the complete artifact/event/MQTT
   lifecycle for every device.
3. Prepare an exact 10,000-device credential bundle and a campaign whose
   schedule, rate, concurrency, and safety policy can complete within the test
   deadline.
4. Run `baseline-10k` on one Linux generator and require exactly 10,000 selected,
   MQTT-ready, assigned, and expected-terminal devices. Record generator CPU,
   memory, goroutine peak, API latency, download throughput, and server metrics.
5. Repeat with multiple deterministic shards. Aggregate the JSONL files,
   require 10,000 unique device IDs with no overlap or omission, and compare
   terminal totals with the single-host semantic result.

Server-side capacity or campaign-policy failures remain valid test findings;
they must not be converted into simulator success by lowering the strict PASS
rule.

## Approval

The design, including the CLI defaults, signature-verification boundary,
strict PASS rule, and MQTT reboot reconnect requirement, was approved on
2026-08-28. Go implementation may proceed against this version.
