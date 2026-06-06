# WebRTC-Only Streaming Migration Roadmap

Status: validated-closeout
Owner: rtk_cloud_workspace
Last updated: 2026-06-06

## Summary

Realtek Connect+ video is WebRTC-only for active product/backend paths. The
final product video path keeps WebRTC signaling through `rtk_video_cloud` and
ICE server selection through static STUN/TURN configuration or the TURN
registry. Legacy RTSP relay and legacy video relay terms are retained only in
historical, migration, or negative-test context.

Canonical contract source: `repos/rtk_cloud_contracts_doc/WEBRTC_ONLY_STREAMING_MIGRATION.md`.

## Issue Order

| Order | Repository | Issue | Dependency | Acceptance summary |
| --- | --- | --- | --- | --- |
| 1 | `rtk_cloud_contracts_doc` | [`[Streaming] Define WebRTC-only video contract and remove RTSP/legacy relay contract`](https://github.com/hkt999rtk/rtk_cloud_contracts_doc/issues/21) | None | Validated: active contract docs no longer present RTSP/legacy relay as supported streaming surfaces; WebRTC/TURN remains canonical. |
| 2 | `rtk_video_cloud` | [`[Streaming] Remove RTSP relay and legacy relay runtime`](https://github.com/hkt999rtk/rtk_video_cloud/issues/314) | Contracts issue | Validated: backend reports and tests cover WebRTC/TURN path; legacy runtime references are historical or migration-only. |
| 3 | `rtk_cloud_client` | [`[Streaming] Remove legacy RTSP/relay SDK helpers and fixtures`](https://github.com/hkt999rtk/rtk_cloud_client/issues/317) | Contracts and server issues | Validated: samples describe WebRTC-only streaming; remaining legacy markers are mirrored migration or negative-test references. |
| 4 | `rtk_cloud_admin` | [`[Streaming] Align admin dashboard with WebRTC-only stream stats`](https://github.com/hkt999rtk/rtk_cloud_admin/issues/54) | Server issue | Validated: stream stats are WebRTC-only and upstream-source aware. |
| 5 | `rtk_cloud_frontend` | [`[Streaming] Remove RTSP relay public copy and align Realtek Connect+ video wording`](https://github.com/hkt999rtk/rtk_cloud_frontend/issues/93) | Contracts issue | Validated: public pages reject legacy streaming markers in route tests. |
| 6 | `rtk_cloud_workspace` | [`[Streaming] Track WebRTC-only migration rollout`](https://github.com/hkt999rtk/rtk_cloud_workspace/issues/4) | All implementation issues | Validated: workspace checklist records grep results and accepted historical/migration-only references. |

## Removal Targets

- `POST /request_stream`
- `mode=rtsp`, `mode=relay`, and default legacy stream mode
- RTSP relay runtime and RTSP URL issuance
- legacy video relay manager/node runtime and relay URL issuance
- `rtsp` bearer-token scope
- legacy `Streaming` RTSP/relay device push payloads
- SDK `requestStream` / `LegacyStream*` helpers and matching fixtures

## Kept Interfaces

- `POST /api/request_webrtc`
- `POST /api/request_webrtc/answer`
- `POST /api/request_webrtc/close`
- device-transport `webrtc_offer`
- returned WebRTC `ice_servers`
- TURN registry control-plane routes under `/v1/turn/nodes/*`
- coturn/TURN relay behavior as part of WebRTC ICE traversal

## Validation Checklist

Final closeout commands:

- `go run ./scripts/go/rtk-cloud -- docs-check`
- `git diff --check`
- local links from this roadmap to the contracts migration note resolve
- all backend repo `GOWORK=off go test ./...` commands pass

```sh
rg -n -i "POST /request_stream|rtsprelay|mode=rtsp|mode=relay|ScopeRTSP|streaming_rtsp_push|streaming_relay_push" \
  docs repos \
  --glob '!**/.git/**'
```

Expected: no active product/runtime references remain. Matches may remain in
this migration note, mirrored contract migration notes, historical replacement
or refactor notes, deployment negative checks, and frontend tests that assert
public pages do not contain legacy streaming markers. `relay` may remain only
in TURN/coturn ICE relay context or clearly marked historical/migration text.
