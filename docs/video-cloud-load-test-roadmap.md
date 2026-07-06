# Video Cloud API-Level E2E Load Test Roadmap

Status: implemented video runner and Home MQTT/WebRTC orchestration
Owner: rtk_cloud_workspace
Last updated: 2026-07-04

## Summary

Video cloud load testing will be developed as an API-level end-to-end client
simulation, not as a server-only benchmark. The v1 runner is owned by `rtk_cloud_workspace` because it coordinates behavior across client, device, viewer, and video cloud server boundaries.
It uses a pre-provisioned and activated fleet; provisioning, claim/bind, and
account readiness onboarding are prerequisites, not part of the v1 load loop.

The v1 implementation is a Go CLI named `rtk-video-loadtest` under
`e2e_test/video_cloud/load/`. It simulates many virtual actors from one process,
supports multi-host load instances through shared run metadata, validates
WebRTC signaling and media with Pion, and emits JSON plus Markdown reports that
can be used as manual, lab, or release evidence.

The env-root driven home MQTT simulation profile is implemented under
`loadtests/home-100k`. It starts from the same local environment directory used
by staging provisioning, loads existing users, device fixtures, bind artifacts,
and device mTLS material for token bootstrap, then models a real home user
operating lights, air conditioners, and smart meters through Cloud APIs. See
[`loadtests/home-100k/docs/home-mqtt-loadtest-simulation.md`](../loadtests/home-100k/docs/home-mqtt-loadtest-simulation.md).
Video-enabled Home profiles reuse `rtk-video-loadtest` for relay-only WebRTC
viewer ladders while `home-100k` owns orchestration and the final report.

## Source-of-Truth Boundaries

| Area | Owner | Rule |
| --- | --- | --- |
| Load runner implementation | `rtk_cloud_workspace` | Owns CLI, actor simulation, report generation, thresholds, local scripts, and manual two-host execution. |
| Server prerequisites | `rtk_video_cloud` | Owns server metrics/readiness expectations, TURN/WebRTC setup notes, test fleet assumptions, and cleanup policy. |
| Roadmap tracking | `rtk_cloud_workspace` | Tracks issue order, links, validation checklist, final snapshot, and product-level load report. |
| Provisioning contract | `rtk_cloud_contracts_doc` | Remains the source of truth for onboarding/provisioning, but v1 load tests do not execute provisioning. |

## V1 Test Boundary

In scope:

- API-level app actor behavior: authenticate or carry a supplied token, list or
  select devices, read device/session state, request WebRTC viewing, poll
  request outcome, and read stream stats.
- Provisioned device actor behavior: represent already activated test devices,
  maintain online/session behavior where the existing APIs support it, and
  produce deterministic identifiers for multi-instance runs.
- Viewer actor behavior: use Go and Pion WebRTC to validate signaling, ICE/TURN
  configuration, setup success/failure, setup latency, first RTP, and first
  H.264 access-unit evidence.
- Load profiles: `safe-staging`, `stress`, and `soak`.
- Artifacts: `load-results.json`, `load-report.md`, run metadata, threshold
  result, and error classification.
- Home MQTT profile: APP/user actors use Account Manager login,
  first-login app key generation, CSR submission, certificate pinning,
  mTLS app-token bootstrap, and
  Cloud APIs; device actors use mTLS credentials from env-root only to
  bootstrap MQTT device tokens; the workload models stateful light,
  air-conditioner, and smart-meter behavior.
- Home WebRTC/TURN profiles: `video-1k-v1`, `video-50k-turn-v1`, and
  `video-100k-turn-v1` run the home MQTT/shadow background load and then attach
  relay-only H.264 WebRTC viewer steps. The TURN sizing ladder is
  `100,500,1000,2000,5000` viewers by default. Relay-only sizing allows both
  TURN/UDP and TURN/TCP ICE server URLs; the gate is selected relay candidates,
  not UDP-only transport.

Out of scope for v1:

- Claim token resolve, bind, product provisioning, and readiness onboarding.
- Android/iOS UI automation.
- Browser-based viewer automation.
- Full media quality, bitrate, or decoder QoE validation beyond first RTP and
  first H.264 access-unit evidence.
- Storage, clips, and snapshots for the home MQTT/WebRTC profiles remain
  disabled or reported as `NOT_RUN` unless a later profile explicitly enables
  them.

## Public Interface Target

Workspace layout:

- `e2e_test/video_cloud/load/cmd/rtk-video-loadtest/`: CLI entry point.
- `e2e_test/video_cloud/load/loadtest/`: actor scheduler, WebRTC/Pion validation, reporting, and threshold evaluation.
- `e2e_test/video_cloud/load/scripts/`: local and two-host execution scripts.
- `e2e_test/video_cloud/load/tools/`: report candidate and two-host aggregation helpers.
- `docs/LOAD_TEST_REPORT.md`: canonical product-level load report file.


CLI examples:

```sh
rtk-video-loadtest run \
  --profile safe-staging \
  --api-url "$VIDEO_CLOUD_LOAD_API_URL" \
  --duration 10m \
  --virtual-devices 100 \
  --virtual-viewers 500 \
  --output load-results.json

rtk-video-loadtest report \
  --input load-results.json \
  --output load-report.md
```

Environment variables:

- `VIDEO_CLOUD_LOAD_API_URL`
- `VIDEO_CLOUD_LOAD_ACCOUNT_TOKEN`
- `VIDEO_CLOUD_LOAD_ADMIN_TOKEN`
- `VIDEO_CLOUD_LOAD_RUN_ID`
- `VIDEO_CLOUD_LOAD_INSTANCE_ID`
- `VIDEO_CLOUD_LOAD_DEVICE_PREFIX`
- `VIDEO_CLOUD_LOAD_DEVICE_TOKEN_MAP_FILE`
- `VIDEO_CLOUD_LOAD_APP_TOKEN_MAP_FILE`
- `VIDEO_CLOUD_LOAD_DEVICE_IDS_FILE`
- `VIDEO_CLOUD_LOAD_WEBRTC_ICE_POLICY=relay` for TURN sizing runs

Home MQTT/WebRTC wrapper:

```sh
HOME100K_DESCRIPTION_FILE=loadtests/home-100k/scenarios/video-50k-turn.description.env \
HOME100K_RUN_ID=lt50k-video-turn-$(date -u +%Y%m%dT%H%M%SZ) \
./loadtests/home-100k/scripts/home-100k.sh workflow-live
```

The wrapper must resolve the environment root with `scripts/go/rtk-cloud/internal/envroot`
and discover users, device inventory, bind artifacts, service endpoints, and
device mTLS material from that root. Device mTLS is used to obtain a token; MQTT
publish/subscribe uses that token credential. Missing prerequisites should
produce a redacted `BLOCKED` report instead of falling back to fake data.

The runner must exit non-zero when configured thresholds fail. Thresholds should
cover at least success rate and p95/p99 latency; WebRTC setup and startup
metrics must be reported separately from generic HTTP/API metrics. Home reports
must distinguish signaling success from media success and must not report TURN
capacity success without relay candidate and external TURN/coturn evidence.

## Execution Policy

This workspace currently does not define GitHub Actions workflows for E2E load
testing. The checked-in `e2e_test/` tree provides the runner, local scripts,
two-host deployment script, aggregation helper, and report candidate helper.
Operators can run these manually from a local, lab, or cloud host. If automated
CI/CD execution is needed later, add it as a separate workspace decision instead
of reintroducing cross-cloud load workflows in `rtk_cloud_client`.

For larger viewer steps, operators should use the two-host runner shape:

```text
device host:     VIDEO_CLOUD_LOAD_ACTORS=device
app/viewer host: VIDEO_CLOUD_LOAD_ACTORS=app,viewer
```

The device host keeps device websocket owners online. The app/viewer host
creates sessions, receives media, records startup latency, and closes sessions.
Token maps and device IDs should be passed as files, not expanded into SSH
environment variables.

## Historical Issue Order

| Order | Repository | Issue | Dependency | Acceptance summary |
| --- | --- | --- | --- | --- |
| 1 | `rtk_cloud_client` | [`[LoadTest] Add Go video cloud API-level load runner`](https://github.com/hkt999rtk/rtk_cloud_client/issues/319) | None | `rtk-video-loadtest` has `run` and `report` commands and can emit JSON plus Markdown artifacts for a short safe-staging run. |
| 2 | `rtk_cloud_client` | [`[LoadTest] Add app/device/viewer actor simulation`](https://github.com/hkt999rtk/rtk_cloud_client/issues/320) | Runner skeleton | One process can run many app/device/viewer actors with controlled concurrency, ramp-up, deterministic run metadata, and bounded cleanup behavior. |
| 3 | `rtk_cloud_client` | [`[LoadTest] Add Pion WebRTC viewer setup validation`](https://github.com/hkt999rtk/rtk_cloud_client/issues/321) | Actor simulation | Viewer actor validates signaling, ICE/TURN configuration, WebRTC setup result, and setup latency using Go Pion WebRTC. |
| 4 | `rtk_cloud_client` | [`[LoadTest] Add JSON/Markdown report and threshold gate`](https://github.com/hkt999rtk/rtk_cloud_client/issues/322) | Runner metrics | Reports include success rate, p95/p99 latency, error classes, actor metrics, WebRTC metrics, and non-zero exit behavior on threshold failure. |
| 5 | `rtk_cloud_client` | [`[LoadTest] Add manual/CD workflow for local and cloud load runs`](https://github.com/hkt999rtk/rtk_cloud_client/issues/323) | Report and thresholds | A manual workflow or script can run safe-staging locally, on self-hosted runners, or in cloud instances and upload reports as artifacts. |
| 6 | `rtk_video_cloud` | [`[LoadTest] Document server prerequisites, metrics, and cleanup policy`](https://github.com/hkt999rtk/rtk_video_cloud/issues/316) | None | Server docs list required fleet state, tokens, TURN/WebRTC config, metrics endpoints, and cleanup expectations for client load tests. |
| 7 | `rtk_cloud_workspace` | [`[LoadTest] Track video cloud E2E load test roadmap and issue links`](https://github.com/hkt999rtk/rtk_cloud_workspace/issues/5) | All opened issues | Workspace records issue links, dependency order, final validation checklist, and v1 completion status. |

## Validation Checklist

Before changing the runner:

```sh
./go run ./scripts/go/rtk-cloud -- docs-check
git diff --check
```

V1 is complete when:

- `rtk_cloud_workspace` can run a safe-staging load profile against a
  pre-provisioned fleet and produce JSON plus Markdown reports.
- One process can simulate many API-level app/device/viewer actors.
- Multiple instances can share a `VIDEO_CLOUD_LOAD_RUN_ID` with unique
  `VIDEO_CLOUD_LOAD_INSTANCE_ID` values.
- WebRTC setup success/failure, first RTP, first H.264 access-unit latency, and
  relay candidate evidence are visible in the report.
- Threshold failures produce a non-zero exit code suitable for automation gating.
- `rtk_video_cloud` documents the server prerequisites required to run the test
  without guessing.
- `loadtests/home-100k` can merge home MQTT/shadow evidence and two-host WebRTC
  ladder artifacts into one `TEST_REPORT.md`.

## Future Profile

A later `provisioning-e2e` profile may include account-manager claim resolve,
claim/bind, cloud provisioning, readiness polling, video activation, and
transport-online verification. That profile should be planned separately because
it is a cross-service onboarding load test, not the v1 video cloud load loop.

The home MQTT simulation profile is a runtime profile, not a provisioning
profile. It assumes users and devices already exist under the environment root
and validates realistic APP-to-device traffic. Video-enabled Home profiles add
WebRTC relay and media-path evidence, but storage, clips, snapshots, and true
decoder QoE remain separate future profiles.
