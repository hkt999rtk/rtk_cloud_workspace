# Cross-Repository Testing

Use this workspace to coordinate validation across pinned submodule commits.

## Test Case Governance

[`tests/catalog.yaml`](../tests/catalog.yaml) is the only source of truth for
published Test IDs, purpose, method, owner, source selector, targets,
environments, and evidence policy. The generated
[`docs/test-catalog.md`](test-catalog.md) is the human-readable index.

```sh
(cd scripts/go && go run ./rtk-cloud -- test-catalog check)
(cd scripts/go && go run ./rtk-cloud -- test-catalog render)
```

IDs use `<layer>-<system>-<feature>-<NNN>`. Published IDs are immutable and are
never reused; removed cases remain in the catalog with `status: retired`.
Case-level IDs are required for UI, E2E, live, and load tests. Unit and service
tests use one suite ID per managed suite.

## Local Baseline

```sh
(cd scripts/go && go run ./rtk-cloud -- status-all)
(cd scripts/go && go run ./rtk-cloud -- docs-check)
(cd scripts/go && go run ./rtk-cloud -- test-matrix)
```

`test-matrix` is the fast workspace baseline: it checks the workspace diff,
workspace-owned Go tooling, catalog validity and generated-document drift, and
repository/submodule status. It does not run every service or product E2E test.

Use the explicit test layers when broader validation is needed:

```sh
(cd scripts/go && go run ./rtk-cloud -- test-services)
(cd scripts/go && go run ./rtk-cloud -- test-e2e)
(cd scripts/go && go run ./rtk-cloud -- test-ui)
(cd scripts/go && go run ./rtk-cloud -- test-live --environment staging --plan)
```

`test-services` runs local service, SDK, frontend, and repository tooling tests.
`test-e2e` runs deterministic workspace E2E and harness tests; add `--scripts`
to opt into the root staging script contract tests. `test-ui` runs the Cloud
Admin UI in a headless Chromium browser against the real local Go BFF and
fixture upstreams. Desktop and mobile are separate targets:
`test-ui --desktop` and `test-ui --mobile`; the default runs both sequentially
with isolated BFF state and artifact directories. Use `test-ui --staging` with
the required `E2E_*` session variables and `E2E_EVIDENCE_SAFE=1` for read-only
desktop-browser validation against dedicated staging test accounts and data.
Use `--run-id ID` to correlate desktop and mobile outputs; CI uses
`gh-<run_id>-<run_attempt>`. The
deprecated wrapper governance test remains a separate migration gate because
it is expected to fail while legacy compatibility wrappers still exist.
`test-live` is plan-only by default and delegates to the staging E2E flow; a
live run still requires `--run --confirm <CLOUD_STACK_NAME>`.

`docs-check` is read-only and validates documentation governance assumptions:
workspace repository entries, key docs entry points, and contracts submodule
commit alignment.

## Reports and Evidence

Case-level runners must report Test ID, target/environment, start and completion
time, duration, purpose, method, result, and an explicit `PASS` or `FAIL`
assessment. UI output is written to:

```text
.artifacts/test-runs/<run_id>/ui/<desktop|mobile>/
  results.json
  junit.xml
  evidence-manifest.json
  TEST_REPORT.md
  evidence/<test-id>.png
  playwright-report/
  raw/
```

Every executed UI case must produce a final viewport screenshot, including
passing cases. Failed cases additionally retain trace, video, error context,
and all retry attempts. A retry that ultimately passes is reported as `FLAKY`
with final assessment `PASS`. Missing Test IDs, catalog-required results, or
screenshots fail the run. Desktop `--full` automatically runs the fixture
phases needed by conditional error, stale, expiry, retry, and lifecycle cases,
then merges them into one report. Staging evidence may only contain dedicated
test data.

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

Home loading-test material is centralized under `loadtests/home-100k/`. For the
100,000-device Home IoT Device Shadow capacity baseline, use the `home-100k`
suite. It owns the 100K home scenario, IoT Device Shadow desired / reported /
delta convergence, offline-device coverage, report contract, and ephemeral
Linode load-generator VM lifecycle commands. These VMs are test actors only and
do not run application runtime services:

```sh
go run ./loadtests/home-100k/cmd/home-100k -- plan \
  --env-root cloud_env/staging/runtime \
  --brandname RTK \
  --region us-sea
```

See `loadtests/home-100k/README.md` and `loadtests/home-100k/docs/` for the
operator guide, scenario notes, report schema, scripts, and legacy MQTT
reference. Workspace-level testing docs should link there instead of duplicating
Home loading-test commands.

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
The orchestrator performs K8s reset, K8s rollout readiness checks, create brand,
create users, create/factory-enroll devices, bind/provision devices, validate the
bind artifact, run `go run ./scripts/go/rtk-cloud -- mqtt-test`, and verify
persisted MQTT runtime logs. It writes sanitized
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
bind/provision devices, then validate the SQLite test-data DB. The validation
profile lives under `e2e_test/provisioning/bulk_bind_validation/` and is invoked
via:

```sh
go run ./scripts/go/rtk-cloud -- validate-device-bind \
  --env-root cloud_env/staging/runtime \
  --brandname RTK
```

This profile verifies API-level onboarding results without requiring live video
streaming success: all expected devices have account device ids and provision
operation ids, every user has the expected number of devices, and mqtt-only
devices do not carry video service options. The source of truth is
`<env-root>/artifacts/test-data/<brand>-test-data.sqlite`; validation JSON/MD
outputs remain run evidence under the selected output directory.

Admin BFF live checks are currently implemented in `rtk_cloud_admin`, but the
workspace index for that product-facing flow lives at `e2e_test/admin_bff/`.
Move wrappers or runners into the workspace when they coordinate multiple
services or shared E2E fixtures.

E2E fixtures are documented under `e2e_test/fixtures/`. Generated local fixture
artifacts should use `.artifacts/e2e_test/fixtures/<fixture_type>/<run_id>/`.
