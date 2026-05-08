# WebRTC-Only Streaming Migration Roadmap

Status: planning
Owner: rtk_cloud_workspace
Last updated: 2026-05-08

## Summary

Realtek Connect+ video is moving to WebRTC-only streaming. The final product
video path keeps WebRTC signaling through `rtk_video_cloud` and ICE server
selection through static STUN/TURN configuration or the TURN registry. Legacy
RTSP relay and legacy video relay paths are breaking-removal targets.

Canonical contract source: `repos/rtk_cloud_contracts_doc/WEBRTC_ONLY_STREAMING_MIGRATION.md`.

## Issue Order

| Order | Repository | Issue title | Dependency | Acceptance summary |
| --- | --- | --- | --- | --- |
| 1 | `rtk_cloud_contracts_doc` | `[Streaming] Define WebRTC-only video contract and remove RTSP/legacy relay contract` | None | Active contract docs no longer present RTSP/legacy relay as supported streaming surfaces; WebRTC/TURN remains canonical. |
| 2 | `rtk_video_cloud` | `[Streaming] Remove RTSP relay and legacy relay runtime` | Contracts issue | `/request_stream`, legacy relay binaries, `ScopeRTSP`, deployment units, OpenAPI entries, and related tests are removed; WebRTC/TURN tests pass. |
| 3 | `rtk_cloud_client` | `[Streaming] Remove legacy RTSP/relay SDK helpers and fixtures` | Contracts and server issues | `requestStream` / `LegacyStream*` APIs, RTSP probe tooling, legacy fixtures, and sample RTSP/relay controls are removed; WebRTC helper tests pass. |
| 4 | `rtk_cloud_admin` | `[Streaming] Align admin dashboard with WebRTC-only stream stats` | Server issue | RTSP/Relay chart lines, demo fallback data, and docs references are removed; stream stats are WebRTC-only. |
| 5 | `rtk_cloud_frontend` | `[Streaming] Remove RTSP relay public copy and align Realtek Connect+ video wording` | Contracts issue | Public site describes WebRTC Video over TURN only; no RTSP relay promotion/manual content remains. |
| 6 | `rtk_cloud_workspace` | `[Streaming] Track WebRTC-only migration rollout` | All implementation issues | Workspace records issue links, final validation, and submodule pointer snapshot. |

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

Before opening implementation issues:

- `./scripts/docs-check.sh`
- `git diff --check`
- local links from this roadmap to the contracts migration note resolve.

After all implementation issues close:

```sh
rg -n -i "POST /request_stream|rtsprelay|mode=rtsp|mode=relay|ScopeRTSP|streaming_rtsp_push|streaming_relay_push" \
  docs repos \
  --glob '!**/.git/**'
```

Expected: no active references remain. `relay` may remain only in TURN/coturn
ICE relay context or clearly marked historical changelog text.
