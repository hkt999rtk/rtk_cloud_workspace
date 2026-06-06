# Backend Release Readiness Checklist

Status: release-readiness snapshot.

Owner: `rtk_cloud_workspace`.

Last reviewed: 2026-06-06.

## Summary

Backend foundation is complete for the inspected workspace snapshot. Remaining
work is release evidence freshness, live staging or hardware evidence, operator
artifact/cutover sign-off, and roadmap features that require new product owners.

Do not reopen completed account lifecycle, provisioning lifecycle, OTA
foundation, PKI/mTLS, TURN registry, telemetry ingestion, WebRTC/TURN backend,
or Admin production-proxy work unless a regression is found.

## Repository Snapshot

| Repository | Commit | Backend closeout status | Evidence |
| --- | --- | --- | --- |
| `rtk_account_manager` | `796988e` | Foundation complete. | Local `GOWORK=off go test ./...` passed; readiness smoke/runbook docs exist. |
| `rtk_video_cloud` | `972a0f5` | Foundation complete. | Local `GOWORK=off go test ./...` passed; canonical local `TEST_REPORT.md`, `READINESS_TEST_REPORT.md`, and `RELEASE_TEST_REPORT.md` replace bootstrap placeholders. |
| `rtk_cloud_admin` | `7f14fca` | Foundation complete. | Local `GOWORK=off go test ./...` passed; account/video proxy routes, customer-safe DTOs, WebRTC stream stats, firmware, telemetry, and unavailable states exist. |
| `rtk_cloud_logger` | `46ea9da` | Foundation complete. | Local `GOWORK=off go test ./...` passed. |
| `rtk_cloud_frontend` | `39d5bb4` | Backend-adjacent server foundation complete. | Local `GOWORK=off go test ./...` passed; public WebRTC-only wording is guarded by route tests. |
| `rtk_cloud_client` | `7f06f49` | SDK/backend integration foundation complete. | Release and live-lab validation tooling exists; hardware evidence remains environment-dependent. |
| `rtk_cloud_contracts_doc` | `22c3eee` | Contract foundation complete. | WebRTC-only, product-readiness, telemetry, auth, OTA, metrics, and smart-home boundary contracts exist. |

## Closeout Decisions

| Area | Decision |
| --- | --- |
| Video Cloud reports | Local canonical reports are no longer bootstrap placeholders. Live staging, hardware, artifact packaging, and deployment cutover are explicit `SKIP` items until operator evidence exists. |
| Admin production source handling | Treat as implemented locally. Live production data is still required for deployment sign-off, but missing upstreams are represented as stable unavailable states rather than backend gaps. |
| WebRTC-only streaming | Active product/backend paths are WebRTC-only through `/api/request_webrtc`, WebRTC answer/close flows, TURN/coturn, and TURN registry. Legacy RTSP/relay terms are allowed only in migration, historical, negative-test, or TURN ICE-relay context. |
| Smart-home ecosystem | Schedules, scenes, consumer groups, household sharing, Matter, Alexa, and Google Assistant remain roadmap. They require named owners, contracts, tests, and deployment notes before being advertised as implemented. |
| Private-cloud evidence | Workspace and service-local evidence foundations exist. Environment-specific proof must be captured in readiness or release reports rather than tracked as new backend implementation work. |

## Validation Commands

Run these before declaring a backend-ready snapshot:

```sh
(cd repos/rtk_account_manager && GOWORK=off go test ./...)
(cd repos/rtk_video_cloud && GOWORK=off go test ./...)
(cd repos/rtk_cloud_admin && GOWORK=off go test ./...)
(cd repos/rtk_cloud_logger && GOWORK=off go test ./...)
(cd repos/rtk_cloud_frontend && GOWORK=off go test ./...)
go run ./scripts/go/rtk-cloud -- docs-check
git diff --check
rg -n -i "POST /request_stream|rtsprelay|mode=rtsp|mode=relay|ScopeRTSP|streaming_rtsp_push|streaming_relay_push" docs repos --glob '!**/.git/**'
```

For the final grep, accepted matches are limited to:

- `docs/webrtc-only-streaming-migration.md`
- mirrored contract migration notes under `docs/rtk_cloud_contracts_doc`
- historical replacement, cutover, or refactor notes
- deployment negative checks that reject placeholder or legacy behavior
- frontend tests that assert public pages do not contain legacy markers
- TURN/coturn ICE relay terminology

## Remaining Release Evidence

| Item | Status | Owner | Required evidence |
| --- | --- | --- | --- |
| Live staging smoke | SKIP until environment is selected. | deployment operator | Readiness report with endpoint, sanitized command output, target device class, and redaction status. |
| Hardware or release-candidate device lab | SKIP until hardware is available. | release operator | Readiness or hardware report with device class, firmware/build id, test commands, and pass/skip/fail results. |
| Release artifact packaging and upload | SKIP until release candidate is selected. | release operator | Release report with artifact version, checksums or artifact references, upload target, and redaction status. |
| Frontend public launch polish | Evidence follow-up. | frontend/product owner | Official privacy contact, legal review, backup/restore notes for lead/analytics storage, and public wording review. |
| Smart-home/Matter/voice assistants | Roadmap. | future product owner | New contract, owner repo, API/SDK behavior, authorization model, tests, and deployment plan. |

## Final Statement

Backend foundation complete; remaining items are release evidence, legacy
reference hygiene, live environment sign-off, and roadmap features.
