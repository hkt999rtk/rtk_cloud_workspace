# Provisioning Interface-First Issue Roadmap

Status: supporting-note.

Owner: `rtk_cloud_workspace`.

Last reviewed: 2026-04-30.

## Purpose

This roadmap maps the Realtek Connect+ provisioning gap to concrete GitHub
issues by owner repository. It is intentionally not the normative contract. The
normative cross-repo interface source is
`repos/rtk_cloud_contracts_doc/PRODUCT_ONBOARDING.md`, with the existing
cloud-side provisioning contract in `repos/rtk_cloud_contracts_doc/PROVISION.md`.

## Strategy

Provisioning uses an interface-first rollout:

1. define product-level onboarding boundaries in the contracts repository
2. define issue ownership in the workspace repository
3. open implementation issues that link back to the pushed documents
4. implement repo-local API, SDK, worker, and website changes in follow-up PRs

This avoids opening issues that each restate the same background and keeps the
contracts repository as the source of truth for shared semantics.

## Owner Matrix

| Capability | Owner repository | First issue type |
| --- | --- | --- |
| Product onboarding boundary | `hkt999rtk/rtk_cloud_contracts_doc` | Contract/interface docs |
| Product readiness state model | `hkt999rtk/rtk_cloud_contracts_doc` | Contract/interface docs |
| Local Wi-Fi/BLE onboarding SDK interface | `hkt999rtk/rtk_cloud_client` | SDK interface docs/API stubs |
| QR/serial/activation-code claim material helpers | `hkt999rtk/rtk_cloud_client` | SDK interface docs/API stubs |
| Claim/bind ownership policy | `hkt999rtk/rtk_account_manager` | Account API/spec follow-up |
| Aggregate provisioning readiness projection | `hkt999rtk/rtk_account_manager` | Account API/spec follow-up |
| Video-side lifecycle worker hardening | `hkt999rtk/rtk_video_cloud` | Worker/runtime hardening |
| Public provisioning availability wording | `hkt999rtk/rtk_cloud_frontend` | Website content alignment |

## Issues To Open After Docs Are Pushed

### `hkt999rtk/rtk_cloud_contracts_doc`

#### `[Provisioning] Define product-level onboarding boundaries`

Summary: make `PRODUCT_ONBOARDING.md` the shared entry point for product-level
onboarding semantics.

Dependencies:

- pushed `PRODUCT_ONBOARDING.md`
- existing `PROVISION.md`
- existing `CROSS_SERVICE_CHANNEL.md`

Acceptance criteria:

- product onboarding layers are explicitly owned by SDK/app/firmware, account
  manager, video cloud, and cross-service worker
- cloud activation remains separate from local Wi-Fi/BLE onboarding
- no unified `POST /provision` is introduced by contract wording
- README and `PROVISION.md` point to the product onboarding document

#### `[Provisioning] Define product-level readiness state vocabulary`

Summary: establish common product readiness state names for follow-up APIs and
UI wording.

Dependencies:

- pushed `PRODUCT_ONBOARDING.md`

Acceptance criteria:

- states include `registered`, `claim_pending`, `local_onboarding_pending`,
  `cloud_activation_pending`, `activated`, `online`, `failed`,
  `deactivation_pending`, and `deactivated`
- document states that account status, video activation, local onboarding, and
  transport online are different facts
- failure state points to the failing layer when possible

### `hkt999rtk/rtk_cloud_client`

#### `[Provisioning] Add local onboarding SDK interface across packages`

Summary: define SDK concepts for local Wi-Fi/BLE onboarding across native,
Android, iOS, and JavaScript/TypeScript.

Dependencies:

- `rtk_cloud_contracts_doc/PRODUCT_ONBOARDING.md`

Acceptance criteria:

- SDK docs define `OnboardingCapability`, `ClaimMaterial`,
  `ClaimMaterialParser`, `LocalOnboardingAdapter`, `LocalOnboardingSession`,
  `LocalOnboardingResult`, and `OnboardingError` equivalents in package-native
  style
- Android and iOS are marked as primary real implementation targets
- native and JavaScript/TypeScript expose the same concepts or explicit
  `unsupported_capability` behavior
- SDK does not own account binding policy or cross-service provisioning
  orchestration

#### `[Provisioning] Add claim material parsing interface`

Summary: add SDK-level helpers for QR, serial, activation-code, and future
factory identity input.

Dependencies:

- local onboarding SDK interface issue
- `rtk_cloud_contracts_doc/PRODUCT_ONBOARDING.md`

Acceptance criteria:

- parser normalizes supported claim material into a common SDK model
- malformed or unsupported input maps to stable SDK errors
- parser does not decide final ownership or claim authorization
- package behavior is consistent or explicitly unsupported across native,
  Android, iOS, and JavaScript/TypeScript

### `hkt999rtk/rtk_account_manager`

#### `[Provisioning] Define claim and bind ownership policy`

Summary: define account-side ownership rules for user/org/device binding.

Dependencies:

- `rtk_cloud_contracts_doc/PRODUCT_ONBOARDING.md`

Acceptance criteria:

- accepted claim material fields are documented by device category or marked out
  of scope
- already-claimed, transfer, factory-reset, delete, and deactivate semantics are
  documented
- policy keeps registry delete separate from product deactivation unless an
  explicit product decision changes it
- OpenAPI/SPEC follow-up is identified when API shape changes are required

#### `[Provisioning] Add product-level readiness projection`

Summary: expose an aggregate readiness status using existing provisioning
operation, video metadata, and online projection facts.

Dependencies:

- readiness vocabulary in `PRODUCT_ONBOARDING.md`
- existing account-side provisioning/deactivation operations

Acceptance criteria:

- readiness projection does not treat `DeviceProvisionSucceeded` as online
- projection can represent activation succeeded but offline, activation failed,
  deactivation pending, and deactivated states
- duplicate operation replay remains idempotent
- docs and tests identify which source facts drive each aggregate state

### `hkt999rtk/rtk_video_cloud`

#### `[Provisioning] Harden video-side lifecycle worker for end-to-end provisioning`

Summary: complete video-side lifecycle worker behavior required for production
cross-service provisioning.

Dependencies:

- `rtk_cloud_contracts_doc/PRODUCT_ONBOARDING.md`
- `rtk_cloud_contracts_doc/CROSS_SERVICE_CHANNEL.md`
- account-side provisioning/deactivation operation support

Acceptance criteria:

- worker handles `DeviceProvisionRequested` and `DeviceDeactivateRequested`
- invalid payload failures preserve correlation information
- deactivation redelivery is retryable where appropriate
- durable `operation_id` and dead-letter behavior are documented and tested
- runbook explains deploy, observe, retry, and dead-letter recovery paths

### `hkt999rtk/rtk_cloud_frontend`

#### `[Provisioning] Align public provisioning copy with implementation status`

Summary: present Realtek Connect+ provisioning as available, integration-ready,
or roadmap based on the interface-first contract.

Dependencies:

- pushed `PRODUCT_ONBOARDING.md`
- workspace gap analysis

Acceptance criteria:

- provisioning page distinguishes cloud-side registry/activation foundation from
  local Wi-Fi/BLE onboarding
- local onboarding, QR/SoftAP UX, transfer/reset policy, and full product
  readiness are not described as generally available until implementation lands
- copy preserves product vision while making availability clear
- content links or internal references point to the contracts source where
  appropriate

## Commit And Issue Workflow

1. Commit and push `rtk_cloud_contracts_doc` documentation first.
2. Commit and push workspace planning docs with the new contracts submodule
   pointer.
3. Open issues using GitHub links to the pushed contract and workspace docs.
4. Do not duplicate the full design text in every issue; link to source docs and
   keep issues focused on acceptance criteria.
