# Cross-Repository Testing

Use this workspace to coordinate validation across pinned submodule commits.

## Local Baseline

```sh
./scripts/status-all.sh
./scripts/docs-check.sh
./scripts/test-matrix.sh
```

`docs-check.sh` is read-only and validates documentation governance assumptions:
workspace repository entries, key docs entry points, and contracts submodule
commit alignment.

## LAN Interop

Known LAN roles used by the client/server project:

- `github-runner.local`: deployed video-cloud test server.
- `client-a.local`: Linux load actor host used by the workspace E2E two-host profile for the device actor.
- `client-b.local`: Linux load actor host used by the workspace E2E two-host profile for app and viewer actors.

Credentials, tokens, device ids, and generated test artifacts must stay outside
the repository and be passed through environment variables or local temp files.

## Workspace E2E Tests

Cross-cloud end-to-end tests live under `e2e_test/` in this workspace. The first
workspace-owned suite is the video cloud API-level load runner:

- Go module: `e2e_test/go.mod`
- Runner: `e2e_test/video_cloud/load/cmd/rtk-video-loadtest/`
- Package: `e2e_test/video_cloud/load/loadtest/`
- Scripts: `e2e_test/video_cloud/load/scripts/`
- Tools: `e2e_test/video_cloud/load/tools/`
- Product load report: `docs/LOAD_TEST_REPORT.md`

`rtk_cloud_client` owns SDK and client package validation. It no longer owns the
cross-cloud video load runner. `rtk_video_cloud` owns server prerequisites,
metrics expectations, TURN/WebRTC setup notes, and cleanup policy for these E2E
runs.
