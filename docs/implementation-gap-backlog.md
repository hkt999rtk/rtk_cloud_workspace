# Realtek Connect+ Implementation Gap Backlog

Status: supporting-note.

Owner: `rtk_cloud_workspace`.

Last reviewed: 2026-04-30.

## Purpose

This note records the implementation and test gaps that remain after the
Provisioning and OTA interface-first issues were merged. It is a planning index
for GitHub issues. It is not the normative contract source.

Normative sources:

- `repos/rtk_cloud_contracts_doc/PRODUCT_ONBOARDING.md`
- `repos/rtk_cloud_contracts_doc/FIRMWARE_CAMPAIGN.md`
- `repos/rtk_cloud_contracts_doc/HTTP_API.md`

Supporting sources:

- `docs/realtek-connect-plus-gap-analysis.md`
- `docs/provisioning-issue-roadmap.md`
- `docs/ota-issue-roadmap.md`
- `repos/rtk_video_cloud/docs/firmware-campaign-alignment.md`
- `repos/rtk_cloud_client/docs/LOCAL_ONBOARDING_SDK.md`
- `repos/rtk_account_manager/docs/SPEC.md`

## Verified Snapshot

| Repository | Commit | Evidence summary |
| --- | --- | --- |
| `repos/rtk_cloud_contracts_doc` | `1efa524` | Product onboarding and firmware campaign contracts are present. |
| `repos/rtk_cloud_client` | `1bc6309` | Local onboarding concepts, claim parser, campaign vocabulary, and unsupported-policy helpers are present across packages. |
| `repos/rtk_account_manager` | `66e6a91` | Account-side readiness projection and claim/bind ownership policy are present. |
| `repos/rtk_video_cloud` | `ce80840` | Cross-service lifecycle worker hardening and current firmware lifecycle tests are present; full OTA campaign engine remains future work. |
| `repos/rtk_cloud_frontend` | `0eb8403` | Public Provisioning and OTA copy is aligned to implementation status. |

## Validation Run

Local verification on 2026-04-30:

| Repository | Command | Result |
| --- | --- | --- |
| workspace | `./scripts/docs-check.sh` | Passed. |
| `rtk_account_manager` | `go test ./...` | Passed. |
| `rtk_video_cloud` | `go test ./...` | Passed. |
| `rtk_cloud_frontend` | `go test ./...` | Passed. |
| `rtk_cloud_client` native | `cmake -S . -B build && cmake --build build && ctest --test-dir build --output-on-failure` | Passed, 9/9 tests. |
| `rtk_cloud_client` JavaScript | `npm ci && npm test` | Passed, 25/25 tests. |
| `rtk_cloud_client` iOS | `swift test` | Passed, 26/26 tests. |
| `rtk_cloud_client` Android | `gradle test` | Passed. |

## Gap Status Summary

| Area | Current status | Remaining implementation gap | Owner repo |
| --- | --- | --- | --- |
| Provisioning contracts | Completed for interface-first scope. | None for the interface-first documentation phase. | `rtk_cloud_contracts_doc` |
| SDK local onboarding | Interface and explicit `unsupported_capability` behavior are present. | Real BLE/SoftAP credential handoff adapters are not implemented. | `rtk_cloud_client` |
| Claim material parsing | Implemented and tested across SDK packages. | Account-side raw QR/serial/activation-code claim endpoints are not implemented; current account API accepts existing-device video claim material only. | `rtk_account_manager` |
| Product readiness | Account-side readiness projection exists. | A unified cross-service product-readiness API is not implemented. | future integration owner, with `rtk_account_manager` and `rtk_video_cloud` inputs |
| Video lifecycle worker | Current hardening is implemented and tested. | Continue treating lifecycle worker regressions as owner-repo bugs, not a remaining interface gap. | `rtk_video_cloud` |
| OTA contracts | Completed for interface-first scope. | None for the interface-first documentation phase. | `rtk_cloud_contracts_doc` |
| OTA SDK helpers | Campaign vocabulary and unsupported-policy behavior are present. | First-class campaign API helpers should wait for backend campaign resources. | `rtk_cloud_client` |
| OTA backend | Current firmware lifecycle foundation is implemented. | First-class campaign resource, schedule/time-window/user-consent policy enforcement, archive, and campaign-level cancel are not implemented. | `rtk_video_cloud` |
| OTA frontend | Copy is aligned to current implementation status. | Promote campaign policies from roadmap/integration-ready wording only after backend and SDK support land. | `rtk_cloud_frontend` |

## Issues To Open

### `hkt999rtk/rtk_video_cloud`

#### `[OTA] Add first-class firmware campaign resource and persistence`

Summary: introduce backend campaign state without removing the existing firmware
lifecycle routes.

Dependencies:

- `rtk_cloud_contracts_doc/FIRMWARE_CAMPAIGN.md`
- `rtk_video_cloud/docs/firmware-campaign-alignment.md`
- existing publish, enable, whitelist, rollout query/report/cancel, and download
  routes

Acceptance criteria:

- add a first-class `FirmwareCampaign` domain model with id, model, target
  version, selector, policy, lifecycle state, timestamps, and audit metadata
- persist campaign records and link rollout records to campaign id where
  applicable
- keep current firmware lifecycle routes working for compatibility
- document how legacy `enable_firmware` and `set_download_whitelist` map to
  campaign behavior or remain legacy paths
- add tests for campaign create/query lifecycle and compatibility with existing
  firmware lifecycle behavior

#### `[OTA] Enforce schedule, time-window, and user-consent rollout policies`

Summary: implement campaign policy gates that are currently vocabulary only.

Dependencies:

- first-class campaign resource issue
- `rtk_cloud_contracts_doc/FIRMWARE_CAMPAIGN.md` rollout policy vocabulary

Acceptance criteria:

- scheduled campaigns block eligibility before start and allow eligibility after
  start
- time-window campaigns block check/download outside the configured window
- user-consent-required campaigns return a stable waiting-for-user state until
  consent is recorded
- unsupported or malformed policy fields fail explicitly instead of being ignored
- tests cover eligible, waiting-for-window, waiting-for-user, already-current,
  failed, canceled, and compatibility-gate behavior

#### `[OTA] Add campaign-level cancel and archive behavior`

Summary: distinguish per-device rollout cancel from campaign-level cancel and
archive management.

Dependencies:

- first-class campaign resource issue
- policy evaluator issue

Acceptance criteria:

- campaign-level cancel prevents future eligibility and cancels pending eligible
  rollout records
- archive hides closed campaigns from active query results without deleting audit
  history
- per-device rollout cancel continues to work for current lifecycle compatibility
- query/report/download surfaces include campaign id where available
- tests cover campaign cancel, archive filtering, audit lookup, and legacy
  per-device cancel behavior

### `hkt999rtk/rtk_cloud_client`

#### `[OTA] Add SDK helpers for backend firmware campaign APIs after backend lands`

Summary: add package-native campaign helpers after `rtk_video_cloud` exposes
first-class campaign APIs.

Dependencies:

- backend campaign resource and policy issues in `rtk_video_cloud`
- `rtk_cloud_contracts_doc/FIRMWARE_CAMPAIGN.md`

Acceptance criteria:

- native, Android, iOS, and JavaScript/TypeScript expose package-native helpers
  for campaign query/report/cancel/download surfaces that exist in the backend
- helpers preserve backend policy decisions and do not decide eligibility locally
- unsupported policy surfaces keep explicit `unsupported_capability` behavior
- tests cover campaign id propagation, rollout state vocabulary, report payloads,
  and unsupported capability behavior across packages

### `hkt999rtk/rtk_cloud_frontend`

#### `[OTA] Promote campaign policy wording only after backend and SDK support land`

Summary: keep public Realtek Connect+ OTA copy synchronized with actual campaign
implementation status.

Dependencies:

- backend campaign resource and policy issues in `rtk_video_cloud`
- SDK helper issue in `rtk_cloud_client`

Acceptance criteria:

- OTA page continues to label incomplete policies as contract-defined or roadmap
  until backend and SDK support are merged
- when backend and SDK support land, update availability wording for the exact
  policies that are implemented
- do not describe approval workflow, dashboards, analytics, or staged percentage
  rollout as available unless separate implementation issues land
- tests cover the public copy snippets that distinguish available,
  integration-ready, and roadmap scope

### `hkt999rtk/rtk_cloud_client`

#### `[Provisioning] Implement real BLE/SoftAP local onboarding adapters`

Summary: move beyond interface stubs for mobile local onboarding.

Dependencies:

- `rtk_cloud_contracts_doc/PRODUCT_ONBOARDING.md`
- `rtk_cloud_client/docs/LOCAL_ONBOARDING_SDK.md`
- platform-specific BLE/SoftAP design decisions from app/firmware owners

Acceptance criteria:

- Android and iOS implement real BLE and/or SoftAP adapter paths behind the
  existing local onboarding interfaces
- unsupported platforms continue returning explicit `unsupported_capability`
- local onboarding success reports local setup result only; it does not claim
  account binding or video activation success
- timeout, cancel, invalid claim material, transport failure, and device rejected
  setup are covered by tests
- docs clarify which transports are implemented per package/runtime

### `hkt999rtk/rtk_account_manager`

#### `[Provisioning] Design raw claim-material endpoint for QR/serial/activation-code flows`

Summary: define whether account manager should accept raw normalized claim
material directly instead of only existing-device video claim payloads.

Dependencies:

- `rtk_cloud_contracts_doc/PRODUCT_ONBOARDING.md`
- SDK claim material parser behavior in `rtk_cloud_client`
- product policy for transfer, reuse, factory reset, and already-claimed devices

Acceptance criteria:

- document whether raw QR, serial number, activation code, MAC address, and
  factory identity should map to an existing registry device or create a pending
  claim flow
- define authorization and organization ownership rules for already-claimed,
  cross-organization, transfer, and reset cases
- if implemented, expose API schema and tests for accepted/rejected claim
  material cases
- if not implemented in this repo, document the owner service and keep current
  API wording explicit

## Issue Ordering

1. Open `rtk_video_cloud` campaign resource issue first; policy and archive/cancel
   issues depend on it.
2. Open `rtk_cloud_client` OTA campaign helper issue as blocked by backend APIs.
3. Open `rtk_cloud_frontend` OTA wording follow-up as blocked by backend and SDK.
4. Open Provisioning adapter and raw-claim design issues independently; they are
   not blockers for OTA.

## Non-Goals For This Backlog

- Do not reopen already completed interface-first contract issues.
- Do not duplicate full contract text in issue bodies.
- Do not claim local BLE/SoftAP onboarding or OTA campaign policies are generally
  available until implementation and tests land.
- Do not move service-owned implementation design into the workspace repo.
