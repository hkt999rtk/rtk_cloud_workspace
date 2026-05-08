# Video Cloud API-Level E2E Load Test Roadmap

Status: planning
Owner: rtk_cloud_workspace
Last updated: 2026-05-08

## Summary

Video cloud load testing will be developed as an API-level end-to-end client
simulation, not as a server-only benchmark. The v1 runner is owned by
`rtk_cloud_client` because it models client, app, device, and viewer behavior.
It uses a pre-provisioned and activated fleet; provisioning, claim/bind, and
account readiness onboarding are prerequisites, not part of the v1 load loop.

The v1 implementation target is a Go CLI named `rtk-video-loadtest`. It should
simulate many virtual actors from one process, support multiple load instances
through shared run metadata, validate WebRTC setup with Pion, and emit JSON plus
Markdown reports that can be used as CI/CD or release evidence.

## Source-of-Truth Boundaries

| Area | Owner | Rule |
| --- | --- | --- |
| Load runner implementation | `rtk_cloud_client` | Owns CLI, actor simulation, report generation, thresholds, and workflow entry points. |
| Server prerequisites | `rtk_video_cloud` | Owns server metrics/readiness expectations, TURN/WebRTC setup notes, test fleet assumptions, and cleanup policy. |
| Roadmap tracking | `rtk_cloud_workspace` | Tracks issue order, links, validation checklist, and final snapshot. |
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
  configuration, setup success/failure, and setup latency.
- Load profiles: `safe-staging`, `stress`, and `soak`.
- Artifacts: `load-results.json`, `load-report.md`, run metadata, threshold
  result, and error classification.

Out of scope for v1:

- Claim token resolve, bind, product provisioning, and readiness onboarding.
- Android/iOS UI automation.
- Browser-based viewer automation.
- Full media quality or bitrate validation beyond WebRTC setup and control-plane
  behavior unless the implementing repo adds it explicitly as a later profile.

## Public Interface Target

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

The runner must exit non-zero when configured thresholds fail. Thresholds should
cover at least success rate and p95/p99 latency; WebRTC setup metrics should be
reported separately from generic HTTP/API metrics.

## Issue Order

| Order | Repository | Issue | Dependency | Acceptance summary |
| --- | --- | --- | --- | --- |
| 1 | `rtk_cloud_client` | To be opened: `[LoadTest] Add Go video cloud API-level load runner` | None | `rtk-video-loadtest` has `run` and `report` commands and can emit JSON plus Markdown artifacts for a short safe-staging run. |
| 2 | `rtk_cloud_client` | To be opened: `[LoadTest] Add app/device/viewer actor simulation` | Runner skeleton | One process can run many app/device/viewer actors with controlled concurrency, ramp-up, deterministic run metadata, and bounded cleanup behavior. |
| 3 | `rtk_cloud_client` | To be opened: `[LoadTest] Add Pion WebRTC viewer setup validation` | Actor simulation | Viewer actor validates signaling, ICE/TURN configuration, WebRTC setup result, and setup latency using Go Pion WebRTC. |
| 4 | `rtk_cloud_client` | To be opened: `[LoadTest] Add JSON/Markdown report and threshold gate` | Runner metrics | Reports include success rate, p95/p99 latency, error classes, actor metrics, WebRTC metrics, and non-zero exit behavior on threshold failure. |
| 5 | `rtk_cloud_client` | To be opened: `[LoadTest] Add manual/CD workflow for local and cloud load runs` | Report and thresholds | A manual workflow or script can run safe-staging locally, on self-hosted runners, or in cloud instances and upload reports as artifacts. |
| 6 | `rtk_video_cloud` | To be opened: `[LoadTest] Document server prerequisites, metrics, and cleanup policy` | None | Server docs list required fleet state, tokens, TURN/WebRTC config, metrics endpoints, and cleanup expectations for client load tests. |
| 7 | `rtk_cloud_workspace` | To be opened: `[LoadTest] Track video cloud E2E load test roadmap and issue links` | All opened issues | Workspace records issue links, dependency order, final validation checklist, and v1 completion status. |

## Validation Checklist

Before opening issues:

```sh
./scripts/docs-check.sh
git diff --check
```

After issues are opened:

- Confirm each issue is in the intended repository.
- Replace `To be opened` entries in this roadmap with issue links.
- Commit and push the issue-link update.

V1 is complete when:

- `rtk_cloud_client` can run a safe-staging load profile against a
  pre-provisioned fleet and produce JSON plus Markdown reports.
- One process can simulate many API-level app/device/viewer actors.
- Multiple instances can share a `VIDEO_CLOUD_LOAD_RUN_ID` with unique
  `VIDEO_CLOUD_LOAD_INSTANCE_ID` values.
- WebRTC setup success/failure and latency are visible in the report.
- Threshold failures produce a non-zero exit code suitable for CI/CD gating.
- `rtk_video_cloud` documents the server prerequisites required to run the test
  without guessing.

## Future Profile

A later `provisioning-e2e` profile may include account-manager claim resolve,
claim/bind, cloud provisioning, readiness polling, video activation, and
transport-online verification. That profile should be planned separately because
it is a cross-service onboarding load test, not the v1 video cloud load loop.
