# SDK App Deployed-Cloud Validation

Status: implementation source of truth

This suite validates released or workspace SDK code from the existing Android
and iOS sample apps against a deployed RTK Cloud. It is a product-level E2E
suite owned by the workspace; package-local unit and mock-server tests remain
in `repos/rtk_cloud_client`, and capacity tests remain under `loadtests/`.

## Test boundaries

| Layer | Owner | Required purpose |
| --- | --- | --- |
| SDK package tests | `repos/rtk_cloud_client/packages/*` | Deterministic API, parsing, transport, and error behavior without live credentials. |
| Sample app integration | Existing Android/iOS sample apps | Prove the real package is linked and platform lifecycle/callback behavior works. |
| Deployed-cloud validation | This directory | Compose Cloud fixtures, sample apps, a virtual device, evidence, cleanup, and one report. |
| Capacity/load | `loadtests/home-100k` | Measure 1K/10K/100K server and broker capacity; it is not an SDK pass/fail gate. |

The first deployed-cloud milestone covers capabilities already implemented by
the SDK packages: token/mTLS, HTTP, WebSocket, lifecycle, normalized errors,
timeouts, cancellation, reconnect, authorization failures, and app-side device
shadow. The shared OpenAPI contract and both Android/iOS public APIs expose
shadow get/update/delete/list. The default deploy profile therefore includes a
real desired → delta → virtual-device reported → delta-cleared round-trip.

### Current SDK capability matrix

| Capability | Go | Native | JavaScript | Android | iOS | Deployed-app validation |
| --- | --- | --- | --- | --- | --- | --- |
| Token + normalized HTTP | Supported | Supported | Supported | Supported | Supported | Android/iOS required |
| Device-side mTLS certificate load | Supported | Supported | N/A | SDK PKI API present | SDK PKI API present | Required through paired virtual-device and Cloud evidence |
| WebSocket connect/send/close | Supported | Supported | Supported | Supported | Supported | Android/iOS required |
| App-side WebSocket receive trigger | Adapter exists | Adapter exists | Adapter exists | Supported | Supported | Required through harness-owned Cloud command trigger |
| MQTT client | Hook | Hook | Unsupported | Unsupported | Unsupported | Device side uses `cloud-mqtt-test`; app side does not claim MQTT coverage |
| Shadow get/desired/documents/delta | Device-side | Device-side | Device-side | Supported | Supported | Required online round-trip |
| Secure hardware key validation | Host-dependent | Board-dependent | N/A | Physical device only | Physical device only | Device-lab tier only |

The mobile sample must not bypass a missing SDK API with direct HTTP/MQTT. A
scenario remains `SKIP` or `BLOCKED` until the shared contract and public SDK
surface exist. The WebSocket smoke proves connect/send/receive/close, callback
ordering, and reconnect. A harness process calls the production device-command
API until the App-owned WebSocket receives a command carrying the run
correlation. The Video Cloud admin token remains in a mode-`0600` header file
and is never copied into the App runtime bundle.

`token_mtls_http` is a composed deployed-cloud scenario: each sample app loads
the run-scoped PKCS#12 app identity, performs a real mTLS token/reissue and
authorized HTTP flow, while the paired virtual device independently loads its
run-issued device certificate and completes mTLS. The evidence provider must
return `device_mtls_authenticated`; an app-only HTTP success is insufficient.
Emulator-side PKI key generation is package-tested, but hardware key protection
remains device-lab-only.

Video Cloud `/ws/device` is a device-transport endpoint. The mobile profile
therefore receives a separate, short-lived device-scoped transport token issued
from the run-owned virtual-device certificate; the app-scoped token remains the
only token used by HTTP and shadow APIs. Token responses may be access-only:
the deployed stateless reissue contract accepts the signed access token as the
reissue input and does not assume a consumed one-time refresh grant.

## Runtime topology

Android and iOS are separate jobs. They may use the same self-hosted macOS
runner, but that runner must serialize them so Android Emulator and iOS
Simulator do not compete for resources.

```text
sdk-e2e-android cloud <- deployed services -> Android Emulator/sample app
           ^                                      |
           +--------- Go virtual device ----------+

sdk-e2e-ios cloud    <- deployed services -> iOS Simulator/sample app
           ^                                      |
           +--------- Go virtual device ----------+
```

The two Brand Clouds are long-lived test fixtures. Account Manager does not
provide a Brand Cloud delete operation, so a run must not create a new cloud.
Every run creates uniquely named users/devices/bindings under the selected
cloud and a second device fixture under the opposite platform cloud. The
second, existing resource makes cross-cloud authorization denial testable
without accepting a missing-resource `404`. Every resource is recorded in
`resource-manifest.json`. Cleanup revokes or
disables only those run-owned resources. A cleanup failure makes the run
`FAIL` with reason `cleanup_failed` and preserves the manifest for recovery.
Device certificate common names are bounded to 63 bytes for the CA/OpenBao
IDNA-label contract. The fixture keeps a readable prefix plus a hash of the
full `run_id`, so truncation cannot make retries reuse an older device.

## Execution lifecycle

1. Validate URLs, toolchains, credentials, CA bundle, SDK source/artifact, and
   deployed server readiness/version. Management operations use the Video
   Cloud URL; SDK token, HTTP, shadow, and WebSocket runtime traffic use the
   mTLS-protected device URL.
   Readiness probes the cleanup contract with a nonexistent device before any
   fixture is created, so an older deployment fails without leaking resources.
2. Verify both dedicated Brand Clouds exist and are active.
3. Provision a primary run-scoped user, device, certificate, claim/binding, and
   app credentials plus a foreign-cloud run-scoped user/device fixture through
   supported Account Manager APIs.
4. Start `scripts/go/cloud-mqtt-test` as a bounded 1-5-device process and wait
   for a machine-readable ready artifact before launching the app test.
5. Run Android instrumentation or Swift XCTest plus the formal iOS XCUITest
   target on a real Simulator install/launch/background/restart lifecycle with
   the existing sample app in `cloud-validation` profile.
6. Collect platform result JSON/JUnit, simulator logs, Cloud audit/runtime logs,
   and virtual-device evidence using `run_id`, `platform`, and `scenario_id`.
7. Clean run-owned remote and local resources and generate the combined report.

The initial virtual-device adapter invokes the existing executable instead of
copying its protocol code. Extraction into a shared Go package is allowed only
after parity tests show the adapter produces the same token, MQTT, shadow, and
runtime evidence as the existing 1K smoke path.

Android readiness requires both `sys.boot_completed=1` and a network carrying
the `INTERNET` and `VALIDATED` capabilities. Its coordination state file is
run-scoped and removed by the finalizer. For nightly offline recovery, the
virtual device intentionally closes its clean MQTT session; after reconnect it
uses the production `shadow/get` and `shadow/get/accepted` topics to resync a
desired update that was written while offline, then publishes reported state.

## Commands

Plan/preflight is safe without live credentials and reports missing inputs as
`BLOCKED`:

```sh
./e2e_test/cloud_validation/run-cloud-validation.sh \
  --environment staging --platform ios --plan-only
```

Live platform runs:

```sh
./e2e_test/cloud_validation/run-cloud-validation.sh \
  --environment staging --platform ios

./e2e_test/cloud_validation/run-cloud-validation.sh \
  --environment staging --platform android
```

The nightly profile adds repeated lifecycle, process restart, and explicit
capability rows:

```sh
./e2e_test/cloud_validation/run-cloud-validation.sh \
  --environment staging --platform all --profile nightly
```

`--platform all` runs the platforms sequentially on one host. CI should prefer
independent platform jobs so one platform failure does not hide the other.
For package-mode `all`, provide separate
`CLOUD_VALIDATION_IOS_ARTIFACT`/`CLOUD_VALIDATION_IOS_ARTIFACT_SHA256` and
`CLOUD_VALIDATION_ANDROID_ARTIFACT`/`CLOUD_VALIDATION_ANDROID_ARTIFACT_SHA256`;
one file cannot represent both Swift and AAR distributions.

## Inputs and secret handling

Non-secret configuration is documented in `config/staging.env.example`.
Secrets come from the CI secret store or an operator-provided environment file;
they must never be committed. The harness writes a per-run credential bundle
with mode `0600`, passes only its path through simulator/instrumentation runtime
configuration, and removes it during cleanup.

When `CLOUD_VALIDATION_ENV_ROOT` points at a local environment created by the
workspace provisioner, the runner reads mode-`0600` operator state to obtain
the Account Manager platform-admin token, a short-lived Video Cloud admin
token, and the Cloud Logger query token. The provisioner persists
`state/secrets/video-auth`; the API deployment consumes that same value as
`VIDEO_CLOUD_AUTH_SECRET`. Set
`CLOUD_VALIDATION_DISABLE_LOCAL_CREDENTIAL_DISCOVERY=1` when CI must require
all three tokens from its secret store instead. Tokens remain in child-process
environment only and are never copied to reports or command arguments.

The built-in staging providers live under `providers/`. They create isolated
run-scoped fixture data through the existing staging setup path, obtain the app
token with mTLS, query Cloud Logger, and run independent customer-safe Cloud
API probes for evidence that redacted logs intentionally omit. Direct probe
responses remain inside the private fixture root; reports retain only HTTP
status, shadow version/convergence, virtual-device mTLS counters, and source
event IDs. Providers then unprovision the
device, revoke its Video Cloud entitlement, revoke the app certificate, and
soft-delete the run user. Provider command variables remain supported as
explicit overrides. They contain executable paths or command references only;
credentials remain in the provider process environment. Do not
embed tokens, passwords, private keys, or shell-expanded credentials in a
provider command string because `bash -lc` command text is process-visible.
Providers list run-owned local secret files in `local_temporary_files`. If
setup fails after a user or device was created, it writes a partial recovery
bundle and a secret-free resource manifest before returning the original
error. Cleanup uses replay-safe admin endpoints and retains the private temp
root when remote cleanup fails so an operator can retry; upload sanitization
never includes that root.

The following may never appear in argv, JUnit, screenshots, Markdown reports,
or uploaded raw logs:

- passwords, bearer/refresh tokens, Claim Tokens, private keys, certificate
  bodies, CSRs, Authorization headers, or secret-bearing URLs;
- credentials from another platform/cloud;
- unredacted customer data.

Generated reports may include resource IDs, certificate fingerprints, endpoint
hosts, SDK/server commits, and redacted error codes. Raw local artifacts stay
under `.artifacts/e2e_test/cloud_validation/`.

## Result contract

Only these statuses are valid:

- `PASS`: the scenario ran and matched its documented result.
- `FAIL`: it ran and did not match, including cleanup failure.
- `SKIP`: intentionally not run because the profile excludes it or a declared
  optional capability is unavailable.
- `BLOCKED`: required credentials, infrastructure, toolchain, or service state
  prevented execution.

Detailed `reason_code` values include `not_configured`,
`environment_unavailable`, `capability_not_implemented`, `timeout`,
`app_crash`, `virtual_device_failed`, `cloud_evidence_missing`, and
`cleanup_failed`. `SKIP` and `BLOCKED` always require a human-readable reason.
The public runner preserves exit `0` for PASS, `1` for FAIL, `2` for BLOCKED,
and `3` when every live platform scenario is SKIP. Plan-only validation returns
`0` after writing its report so it can be used as a non-mutating contract check.

Every platform result conforms to `reports/platform-result.schema.json` and
records the run/scenario correlation, SDK and server identity, duration,
normalized SDK error code, and redacted evidence. Overall reports also record
host/toolchain versions, contracts commit, fixture cloud, resource manifest,
and cleanup outcome. The harness writes `results.json`, `junit.xml`, and
`SUMMARY.md`; JUnit represents `BLOCKED` and `SKIP` as skipped test cases but
keeps the exact status and reason in the message.

## Source and package modes

- `source`: PR/default mode; sample apps resolve the SDK from the current
  `rtk_cloud_client` checkout.
- `package`: release mode; Android consumes the release AAR/Maven artifact and
  iOS consumes the released Swift package/XCFramework. The harness records and
  verifies the supplied artifact checksum.

A source-mode pass does not qualify a release artifact. Package mode must be
run before SDK release acceptance.

## Validation tiers

| Tier | Required coverage |
| --- | --- |
| PR | SDK unit tests, sample builds, local mock integration; no live secret requirement. |
| Deploy | Android/iOS platform smoke, one virtual device, Cloud evidence, cleanup. |
| Nightly | Deterministic offline/reconnect, token expiry/reissue, cross-cloud denial, restart, and repeated lifecycle are required. App-certificate rotation remains an explicit `SKIP(capability_not_implemented)` until the sample SDK profile owns CSR rotation. |
| Release | Package-mode validation of the exact distributable artifact. |
| Device lab | Secure Enclave/hardware Keystore, real background limits, Wi-Fi/cellular, BLE/SoftAP, camera/media. |

The reusable workflow is `.github/workflows/sdk-cloud-validation.yml`; the
scheduled caller is `.github/workflows/sdk-cloud-validation-nightly.yml`.
Because this repository does not own a staging deploy workflow, the explicit
post-deploy caller `.github/workflows/sdk-cloud-validation-after-deploy.yml`
listens for repository-dispatch event `staging-deploy-succeeded`. The deploy
owner must emit that event only after a successful staging deployment. Manual
dispatch defaults to build/contract validation; live mutation requires
`run_live=true`.
Repository variables provide deployed URLs, Cloud Logger URL, environment root,
MQTT address, both dedicated Cloud slugs, and CA path. CI secrets provide
Account Manager platform-admin, Video Cloud admin, and Cloud Logger tokens.
Custom provider commands are optional overrides. A missing input produces a
`BLOCKED(not_configured)` report and a non-zero live job; it is not silently
skipped.

The optional 1K smoke remains a separate `home-100k` invocation after both SDK
jobs pass. It keeps its own run ID, report, cleanup gate, and pass/fail decision;
the SDK workflow does not fold capacity results into mobile correctness.

iOS Simulator does not prove Secure Enclave, hardware-backed Keychain access,
cellular transitions, or long-running background behavior. Android Emulator
does not prove hardware-backed Keystore, Doze/OEM restrictions, cellular
handover, or vendor BLE behavior. Those rows remain `SKIP` until a physical
device-lab profile is selected.

The existing iOS sample now also has `RTKSampleApp.xcodeproj` and a formal
`CloudValidationUITests` target. XCUITest verifies install, launch,
foreground/background, process restart, and customer-safe failure copy. The
deployed SDK scenarios still run through the same sample source and Swift
package; the Xcode project is a test host, not a second application.

## Failure and recovery rules

- App crash, ANR, test timeout, missing result file, emulator disappearance,
  virtual-device exit, missing required Cloud evidence, or cleanup failure can
  never be reported as `PASS`.
- A failed platform does not prevent the other platform report from being
  generated.
- Remote cleanup runs in a finalizer. If it fails, the resource manifest and a
  redacted recovery command are retained.
- Retries create a new `run_id`; they do not overwrite failed evidence.
- Queue observation and post-reconnect convergence have independent timeout
  windows; emulator build/boot time cannot consume the convergence budget.
- The optional 1K smoke starts only after SDK validation and produces its own
  report and pass/fail decision.
