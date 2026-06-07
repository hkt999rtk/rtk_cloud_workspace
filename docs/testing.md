# Cross-Repository Testing

Use this workspace to coordinate validation across pinned submodule commits.

## Local Baseline

```sh
go run ./scripts/go/rtk-cloud -- status-all
go run ./scripts/go/rtk-cloud -- docs-check
go run ./scripts/go/rtk-cloud -- test-matrix
```

`docs-check` is read-only and validates documentation governance assumptions:
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

The home MQTT simulation profile extends the workspace load runner with an
env-root driven real user case. Operators should start it from the same local
environment directory used by cloud provisioning:

```sh
go run ./scripts/go/rtk-cloud -- mqtt-test \
  --env-root cloud_env/staging \
  --brandname RTK
```

`go run ./scripts/go/rtk-cloud -- mqtt-test` is the direct entry point and writes sanitized
`results.json` plus `TEST_REPORT.md` under
`<env-root>/artifacts/home-mqtt-loadtest/<timestamp>/`. The wrapper discovers
user credentials, device inventory, bind artifacts,
service endpoints, and per-device mTLS cert/key material from the resolved
environment root. APP actors use user credentials and Cloud APIs; device actors
use their per-device certificate only to bootstrap a device token over mutual
TLS. Production-like APP actors must model first-login app key generation, CSR
submission through Account Manager, certissuer-backed app certificate
pinning, and use the pinned certificate to bootstrap a subject-bound Video
Cloud `app` token before sending commands or subscribing to device data.
The default run is live MQTT E2E: each selected device calls
`POST /request_token` with its mTLS certificate, connects to the staging MQTT
broker with the issued token credential, subscribes to
`devices/<device_id>/up/messages`, publishes a sample home-device envelope, and
waits for the loopback message. WebRTC, relay, storage, clips, and snapshots are
disabled for this profile. The design and developer issue breakdown live in
`docs/home-mqtt-loadtest-simulation.md`.

For the 10,000-device MQTT-only capacity baseline, use the two-phase
`mqtt-loadtest` wrapper. It prepares 2,500 users and 10,000 non-camera devices
once, then runs repeatable local or distributed shards:

```sh
go run ./scripts/go/rtk-cloud -- mqtt-loadtest prepare \
  --env-root cloud_env/staging \
  --brandname RTK \
  --plan

go run ./scripts/go/rtk-cloud -- mqtt-loadtest run \
  --env-root cloud_env/staging \
  --brandname RTK
```

The default `baseline-10k` profile uses 100% connected MQTT devices, excludes
camera/WebRTC/TURN/media, uses telemetry every 5 minutes, and writes shard
results under `<env-root>/artifacts/mqtt-loadtest/<timestamp>/`. For multiple
load-generator VMs, pass `--hosts-file`; the wrapper assigns one shard per SSH
host and aggregates the copied shard results. See
`docs/linode-10k-mqtt-loadtest.md`.

For a destructive staging reset followed by the full onboarding and MQTT smoke,
use the one-stop orchestrator from the workspace root:

```sh
go run ./scripts/go/rtk-cloud -- staging-e2e-test \
  --env-root cloud_env/staging \
  --run \
  --confirm video-cloud-stg-0529 \
  --brandname RTK
```

The same command with `--plan` is read-only and should be used before a live run.
The orchestrator performs remove VM, provision all, create brand, create users,
create/factory-enroll devices, bind/provision devices, validate the bind
artifact, and run `go run ./scripts/go/rtk-cloud -- mqtt-test`. It writes sanitized
`summary.json` and `TEST_REPORT.md` under
`<env-root>/artifacts/staging-e2e/<timestamp>/`; per-step logs remain local
operator artifacts and should not be committed.

Provisioning smoke tests belong under
`e2e_test/provisioning/account_video_smoke/`. The first planned smoke composes
Account Manager test users, Account Manager Claim Token resolve/provision APIs,
and factory-enrolled Video Cloud `devid` certificates. It must report missing
app certificate bootstrap, video-side lifecycle, or mTLS prerequisites as
`BLOCKED`, not as pass.

Bulk device onboarding validation uses the workspace script sequence documented
in `scripts/README.zh-TW.md`: create users, generate/factory-enroll devices,
bind/provision devices, then validate the bind artifact. The validation profile
lives under `e2e_test/provisioning/bulk_bind_validation/` and is invoked via:

```sh
go run ./scripts/go/rtk-cloud -- validate-device-bind \
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
