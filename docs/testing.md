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
Case-level IDs are required for UI, E2E, live, and load tests. Every Go unit
function and subtest receives an automatically generated canonical key; only
security-critical or long-lived regression cases additionally receive a
permanent `UNIT-*` catalog ID. The governed JavaScript suites use the same
model: every Node TestStream case is registered in
[`tests/unit-inventory.yaml`](../tests/unit-inventory.yaml) with a stable
`js://<module>/<source>#<percent-encoded-title>` key. Service suites retain one
`SVC-*` ID per module.

```sh
(cd scripts/go && go run ./rtk-cloud -- test-inventory check --from-run RUN_DIR)
(cd scripts/go && go run ./rtk-cloud -- test-inventory update --from-run RUN_DIR)
(cd scripts/go && go run ./rtk-cloud -- test-inventory render)
```

`update` only appends new canonical keys. A removed or renamed test remains
visible until its ledger entry is explicitly marked `retired` with a date and
reason.

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
(cd scripts/go && go run ./rtk-cloud -- test-coverage --profile unit --base-ref origin/main)
(cd scripts/go && go run ./rtk-cloud -- test-coverage --profile pr --run-id RUN_ID)
(cd scripts/go && go run ./rtk-cloud -- test-coverage --profile runtime --runtime-dir GOCOVERDIR_ROOT --run-id RUN_ID)
(cd scripts/go && TEST_DATABASE_URL=... go run ./rtk-cloud -- test-payment --profile fake-e2e --run-id RUN_ID)
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
`test-coverage` enforces schema v3 in `tests/coverage.yaml` across all ten Go
modules and the two governed JavaScript modules. Reports show raw and governed
Go coverage separately. Governed coverage
uses explicit package risk (`critical`, `high`, `normal`, or reporting-only
`wiring`), per-package ratchets, targets, owners, and documented exclusions.
The default remains the local `unit` profile for compatibility. The `pr`
profile additionally requires the configured PostgreSQL/EMQX integration
environment and fails when a required integration test is skipped. The
`runtime` profile aggregates deployed-process `GOCOVERDIR` evidence.
`test-coverage-aggregate` combines the ten required Go module jobs and two
required JavaScript jobs, preferring the integration-aware PR result for
Account Manager and Video Cloud.

New or modified Go statements must meet 80% differential coverage against
`--base-ref`; this prevents legacy debt from lowering the standard for new
code. Every Go test function and subtest is discovered from `go test -json`
and recorded as `go://<module>/<package>#<TestName>/<Subtest>`. Critical
security and regression tests also map to permanent `UNIT-*` catalog IDs.
Cloud Admin web helpers and the JavaScript SDK enforce Node/V8 line, branch,
and function thresholds. A custom Node reporter consumes structured TestStream
events and writes per-case timing, result, source, canonical key, commit anchor,
JUnit, and JSON. Build output such as Cloud Client `dist/test/*.js` is mapped
back to its TypeScript source path before inventory comparison. Duplicate
titles in one source file, missing completion events, unregistered tests,
missing active tests, or a non-passing critical ID fail the job. The command
writes:

```text
.artifacts/test-runs/<run-id>/coverage/
  results.json
  TEST_REPORT.md
  unit-inventory.json
  modules/<module>/
    coverage.out
    package-coverage.json
    unit-manifest.json
    junit.xml
    test-events.json
    coverage.log
```

Coverage artifacts are scanned for private keys, bearer tokens, cookies, and
credential-like JSON values before the run can pass.
`test-payment --profile fake-e2e` reuses the Account Manager PR coverage run
and its caller-supplied isolated PostgreSQL database. It resolves active
`test-payment` catalog selectors against canonical Go test events and fails
closed when an operation is missing, skipped, failed, or lacks the Account
Manager coverage gate. The payment report records each Test ID's purpose,
method, start/end time, duration, result, assessment, commit anchors, JUnit,
raw events, SHA-256 evidence, redaction result, and zero-resource cleanup
result under `.artifacts/test-runs/<run-id>/payments/fake-e2e/`. It never calls
NewebPay and is not evidence of sandbox qualification.
`test-e2e` runs deterministic workspace E2E and harness tests; add `--scripts`
to opt into the root staging script contract tests. `test-ui` runs the Cloud
Admin UI in a headless Chromium browser against the real local Go BFF and
fixture upstreams. Desktop and mobile are separate targets:
`test-ui --desktop` and `test-ui --mobile`; the default runs both sequentially
with isolated BFF state and artifact directories. Use
`test-ui --staging --desktop` or `test-ui --staging --mobile` with the required
`E2E_*` session variables and `E2E_EVIDENCE_SAFE=1` for read-only browser
validation against dedicated staging test accounts and data.
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

`Go Coverage Governance` separates policy/catalog/inventory validation, the
ten-module Go matrix, the two-module JavaScript inventory matrix, Account
Manager PostgreSQL integration, Video Cloud PostgreSQL/EMQX integration, and
final cross-language aggregation/redaction into parallel required jobs.
`Workspace Test Baseline` continues to run `test-matrix`, deterministic
workspace E2E, full desktop/mobile product UI evidence, Home load-runner
contracts, Node/V8 coverage, and service suites selected from the diff. Its
requirement-level aggregate runs as a non-blocking audit while
`FEATURE_QUALIFICATION_MODE=observe`; changing that variable to `required`
switches the same PR path to `test-feature-coverage check`. These deterministic
gates do not depend on shared staging.

Coverage evidence is retained for 30 days on pull requests and 90 days on
`main`. A gate fails for test failure, required integration SKIP, package or
module ratchet regression, missing critical permanent ID, unsafe artifact, or
changed-Go-statement coverage below 80%.

`Go Runtime Coverage Nightly` is deliberately separate. It builds
coverage-only images into the existing staging LKE cluster while deploying only
run-scoped namespaces whose stack name starts with `coverage-`. Manual
`preflight` validates the configured GitHub runner, credentials, cluster label,
repository capacity variable, live Linode instance/volume/NodeBalancer
headroom, orphaned coverage PVs, commit anchors, and staging deployment snapshot
without creating resources. Object-storage endpoint, bucket, region, and
credentials are read from the current shared-staging Video Cloud deployment
rather than duplicated GitHub secrets; runtime data remains isolated by its
run-specific object prefix. The quota-bounded runtime profile uses ephemeral
PostgreSQL/OpenBao storage, five namespace-level coverage PVCs, ClusterIP plus
runner-local tunnels, and a bounded local-live canary runner. It therefore
projects five additional active services and creates no LoadBalancer or
load-generator VM; insufficient
headroom is reported as `BLOCKED` before mutation. Manual `run` requires the exact confirmation
`video-cloud-staging-lke`. It mounts one run-scoped PVC per instrumented
module namespace, pins that namespace's instrumented deployments to one node
for ReadWriteOnce sharing, runs onboarding, all three feature canaries, and deployed
desktop/mobile smoke, then scales services down before collecting `covmeta` and
`covcounters`. Each coverage-only command includes a test-only SIGTERM hook
that snapshots atomic counters before Kubernetes terminates the process; the
hook is not present in normal staging or production images. `test-live` owns
its direct onboarding port-forwards, while feature canaries use the separate
runner-local HTTPS/MQTT tunnel. Commit/run anchors are required before
aggregation. Runtime
coverage and feature qualification share the `staging-mutating-tests`
concurrency lock. Cleanup deletes only that isolated stack's namespaces, PVCs,
collectors, generator resources, retained PV objects, and their exact Linode
block-volume handles, then verifies the shared staging
deployment UIDs/images are unchanged and writes `cleanup-report.json`. Any
residual Kubernetes or Linode resource fails the run. The cron remains disabled until repository
variable `RUNTIME_COVERAGE_NIGHTLY_ENABLED=true`; enable it only after the first
manual run passes. Coverage images are never used by shared staging or
production.

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
