# Cross-Repository Testing

Use this workspace to coordinate validation across pinned submodule commits.

## Test Case Governance

[`tests/catalog.yaml`](../tests/catalog.yaml) is the only source of truth for
published Test IDs, purpose, method, owner, source selector, targets,
environments, feature/profile relationships, load `covers` links, PR
`change_paths`, and evidence policy. The generated
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
(cd scripts/go && go run ./rtk-cloud -- test-coverage --base-ref origin/main)
(cd scripts/go && go run ./rtk-cloud -- test-e2e)
(cd scripts/go && go run ./rtk-cloud -- test-ui)
(cd scripts/go && go run ./rtk-cloud -- test-live --environment staging --plan)
(cd scripts/go && go run ./rtk-cloud -- test-feature --feature device-shadow --profile qualification-1k --plan)
```

`test-services` runs local service, SDK, frontend, and repository tooling tests.
Use `test-services --changed-since <git-ref> --install` in CI to run only
repositories affected by a workspace diff while installing JavaScript test
dependencies. Changes to the workspace test runner, catalog, contracts pointer,
Go workspace, or baseline workflow conservatively select all managed service
repositories.
`test-coverage` enforces the policy in `tests/coverage.yaml`. Every managed Go
module has a non-regression statement-coverage ratchet and an explicit 80%
target. Workspace-owned Go changes must additionally meet 80% differential
statement coverage against `--base-ref`; this prevents legacy debt from
lowering the standard for new code. Cloud Admin web helpers and the JavaScript
SDK enforce line, branch, and function thresholds using Node/V8 coverage.
Video Cloud also runs its native critical-package gate. The command writes
machine-readable results, a human report, raw logs, Go profiles, and profile
SHA-256 hashes under:

```text
.artifacts/test-runs/<run-id>/coverage/
  results.json
  TEST_REPORT.md
  profiles/*.out
  logs/*.log
```

Coverage artifacts are scanned for private keys, bearer tokens, cookies, and
credential-like JSON values before the run can pass.
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

## Feature Qualification

`test-feature` reuses the Home, Video, and Clip load runners to qualify
load-sensitive behavior. It does not replace service tests or browser UI
tests. A `qualification-1k` run always executes the feature canary first and
does not allocate the 1K load when the canary is not `PASS`.

```sh
go run ./scripts/go/rtk-cloud -- test-feature \
  --feature device-shadow \
  --profile qualification-1k \
  --environment staging \
  --env-root cloud_env/staging/lke \
  --run-id manual-shadow-001 \
  --run \
  --confirm video-cloud-staging
```

The initial managed features are `device-shadow`, `video-webrtc`, and
`clip-storage`. Their 1K scale is feature-specific: 1,000 MQTT devices for
Shadow; 1,000 Home devices plus 100 concurrent H264 relay sessions for Video;
and 1,000 Home devices plus 100 cameras uploading 10 clips each for Clip.
Larger 10K/50K/100K profiles remain capacity exercises.

Pull-request selection is catalog-driven:

```sh
go run ./scripts/go/rtk-cloud -- test-feature select \
  --base-ref origin/main \
  --head-ref HEAD
```

The reusable Feature Qualification workflow serializes shared-staging use,
deploys commit-anchored candidate images once, runs selected features, uploads
evidence, and cleans up load-generator resources even when the run fails.
Catalog, shared runner/contracts/deployment changes and an unresolved
`rtk_video_cloud` submodule pointer diff conservatively select all three
features. Pull requests default to `observe` mode: selection and its report run,
but the environment-coupled live job is skipped and the result check explicitly
reports that qualification was not enforced. Set the repository variable
`FEATURE_QUALIFICATION_MODE=required` only after the dedicated workspace runner,
staging runtime restore, and CI credentials have been verified. Manual and
reusable dispatches always run in `required` mode. After each feature has
produced at least one complete `PASS`, enable required mode and make
`Feature qualification result` a required branch check.

`docs-check` is read-only and validates documentation governance assumptions:
workspace repository entries, key docs entry points, and contracts submodule
commit alignment.

## Pull Request Coverage Gate

The `Workspace Test Baseline` workflow runs for every pull request and for
updates to `main`. It executes `test-matrix`, deterministic workspace E2E,
Home load-runner contract tests, and service suites selected from the workspace
diff. This gate is independent of shared staging and therefore still runs while
feature qualification is in observation mode. Repository-native CI remains
responsible for language/platform-specific coverage gates, race tests, native
SDK builds, and release checks.

The same workflow also runs the complete measurable coverage policy on every
PR, uploads the report and profiles for 30 days (90 days on `main`), and fails
when an overall ratchet, JavaScript metric, Video Cloud critical-package policy,
artifact redaction scan, or the 80% changed-Go-statement threshold fails.

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

Feature qualification output is written to:

```text
.artifacts/test-runs/<run_id>/features/<feature>/
  canary/
    results.json
    evidence-manifest.json
    TEST_REPORT.md
  qualification-1k/
    plan.json
    results.json
    server-evidence.json
    runtime-log-evidence.json
    evidence-manifest.json
    TEST_REPORT.md
  qualification-report.json
  qualification-report.md
```

`PASS` requires case-level behavior evidence, complete server/runtime-log
correlation, commit anchors, target completeness, and load thresholds.
`FAIL`, `INCOMPLETE`, and `BLOCKED` are distinct non-passing outcomes.
Clip qualification additionally requires mixed non-clip control traffic and
dedicated staging credentials for sampled download/decryption. Generated token
maps and private-key material are removed before artifact hashing/upload, and a
credential-like value found in text evidence forces `INCOMPLETE`.

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
