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

Cross-cloud and factory-environment end-to-end tests live under `e2e_test/` in
this workspace. Start from `e2e_test/README.md` for the canonical taxonomy,
ownership rules, and artifact layout.

The factory enrollment v1 suite validates the local factory flow without Linode
or production issuer secrets:

- Runner: `e2e_test/factory_enroll/cmd/rtk-factory-enroll-test/`
- Package: `e2e_test/factory_enroll/factoryenrolltest/`
- Local script: `e2e_test/factory_enroll/scripts/run_factory_enroll_local.sh`
- Source fixture: `../rtk_video_cloud/examples/factory-enrollment/`

The runner generates device keys/CSRs, derives each `devid` from the public key
fingerprint, calls local `cmd/factoryenroll`, and verifies the returned device
certificate. Device private keys are not written unless explicitly requested for
local debugging.

The video cloud API-level load runner is also workspace-owned:

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

The planned home MQTT simulation profile extends the workspace load runner with
an env-root driven real user case. Operators should start it from the same local
environment directory used by cloud provisioning:

```sh
scripts/cloud-run-home-mqtt-loadtest.sh \
  --env-root cloud_env/staging \
  --brandname RTK
```

The wrapper must discover user credentials, device inventory, bind artifacts,
service endpoints, and per-device mTLS cert/key material from the resolved
environment root. APP actors use user credentials and Cloud APIs; device actors
use per-device MQTT mTLS identity. WebRTC, relay, storage, clips, and snapshots
are disabled for this profile. The design and developer issue breakdown live in
`docs/home-mqtt-loadtest-simulation.md`.

Provisioning smoke tests belong under
`e2e_test/provisioning/account_video_smoke/`. The first planned smoke composes
Account Manager test users, Account Manager Claim Token resolve/provision APIs,
and factory-enrolled Video Cloud `devid` certificates. It must report missing
video-side lifecycle or mTLS prerequisites as `BLOCKED`, not as pass.

Bulk device onboarding validation uses the workspace script sequence documented
in `scripts/README.zh-TW.md`: create users, generate/factory-enroll devices,
bind/provision devices, then validate the bind artifact. The validation profile
lives under `e2e_test/provisioning/bulk_bind_validation/` and is invoked via:

```sh
scripts/cloud-validate-device-bind.sh \
  --bind-artifact cloud_env/staging/linode/artifacts/device-bind/rtk-device-bind-<timestamp>.json
```

This profile verifies API-level onboarding results without requiring live video
streaming success: all expected devices have account device ids and provision
operation ids, every user has the expected number of devices, and mqtt-only
devices do not carry video service options.

Admin BFF live checks are currently implemented in `rtk_cloud_admin`, but the
workspace index for that product-facing flow lives at `e2e_test/admin_bff/`.
Move wrappers or runners into the workspace when they coordinate multiple
services or shared E2E fixtures.

E2E fixtures are documented under `e2e_test/fixtures/`. Generated local fixture
artifacts should use `.artifacts/e2e_test/fixtures/<fixture_type>/<run_id>/`.
