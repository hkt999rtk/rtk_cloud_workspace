# Realtek Connect+ Implementation Gap Backlog

Status: supporting-note.

Owner: `rtk_cloud_workspace`.

Last reviewed: 2026-05-07.

## Purpose

This note records the remaining implementation and test gaps after the latest
workspace/submodule refresh. It is a planning index for GitHub issues. It is not
the normative contract source.

The May 2026 refresh supersedes older OTA, telemetry, and account-lifecycle gap
entries. Do not reopen those older issues unless a regression is found in the
owning repository.

Normative and primary sources:

- `repos/rtk_cloud_contracts_doc/PRODUCT_ONBOARDING.md`
- `repos/rtk_cloud_contracts_doc/FIRMWARE_CAMPAIGN.md`
- `repos/rtk_cloud_contracts_doc/PRODUCT_READINESS.md`
- `repos/rtk_cloud_contracts_doc/TELEMETRY_INSIGHTS.md`
- `repos/rtk_cloud_contracts_doc/METRICS_EXPORT.md`
- `docs/private-cloud-deployment.md`
- `docs/product-level-evidence.md`
- `docs/cross-service-broker-packaging.md`
- `docs/core-platform-gap-roadmap.md`
- `docs/realtek-connect-plus-gap-analysis.md`

## Verified Snapshot

| Repository | Commit | Evidence summary |
| --- | --- | --- |
| `repos/rtk_cloud_contracts_doc` | `273ef4b` | Product readiness, telemetry insights, metrics export, firmware campaign, frontend style, onboarding, PKI/mTLS, and TURN registry contracts are present. |
| `repos/rtk_account_manager` | `b121676` | Evaluation-tier signup, account lifecycle, quota workflow, fleet registry, provisioning, Claim Token resolve, registry-only readiness, private-cloud runbook, and readiness smoke evidence are present. |
| `repos/rtk_video_cloud` | `39942d8` | Firmware campaign, telemetry ingestion, fleet health, metric exporter, cert issuer, PKI/mTLS, TURN registry, EMQX, and deployment assets are present. |
| `repos/rtk_cloud_client` | `746d7bf` | Firmware campaign helpers, telemetry helpers, Go SDK, Android/iOS PKI work, sample apps, Pro2 lifecycle/media work, coverage export docs, and live-lab validation tooling are present. |
| `repos/rtk_cloud_admin` | `7c452f5` | Admin dashboard has self-service signup UI, fleet health, stream health, firmware/OTA, device telemetry, platform/customer views, upstream account/video proxy paths, demo/cache mode, and private-cloud deployment profile. |
| `repos/rtk_cloud_frontend` | `6bcc5bd` | Public website has privacy-friendly analytics, multilingual content, manual pages, business model disclosure, private cloud content, OTA policy promotion, and SDK sample ecosystem content. |

## Current Gap Status Summary

| Area | Current status | Remaining gap | Owner repo |
| --- | --- | --- | --- |
| OTA campaign backend | Implemented for foundation scope. | Do not reopen campaign resource/policy/cancel/archive issues; advanced approval workflow, staged percentage rollout, and dashboards remain roadmap outside this batch. | `rtk_video_cloud` |
| OTA SDK helpers | Implemented across SDK packages for campaign helper surface. | Treat future gaps as package-specific regressions or advanced policy work, not foundation blockers. | `rtk_cloud_client` |
| Product telemetry baseline | Implemented for backend ingestion, SDK typed helpers, and admin display. | Product retention/analytics depth can evolve later; no baseline ingestion issue is needed now. | `rtk_video_cloud`, `rtk_cloud_client`, `rtk_cloud_admin` |
| Account lifecycle baseline | Implemented for signup, email verification, forgot/reset password, password change, self-service disable/delete, and evaluation quota. | Third-party/social login remains deferred and is not part of this foundation batch. | `rtk_account_manager` |
| PKI/mTLS server-side | Implemented. | Latest video cloud and contracts cover verified device mTLS, certificate revocation, and `/api/device/renew_certificate`; do not duplicate old PKI issue work. | `rtk_video_cloud`, `rtk_cloud_contracts_doc` |
| WebRTC-only streaming migration | Planned breaking cleanup. | Product direction is WebRTC Video over TURN only. Legacy `/request_stream`, RTSP relay, legacy video relay, and `rtsp` scope are removal targets tracked in `docs/webrtc-only-streaming-migration.md`. | `rtk_cloud_contracts_doc`, `rtk_video_cloud`, `rtk_cloud_client`, `rtk_cloud_admin`, `rtk_cloud_frontend`, `rtk_cloud_workspace` |
| TURN registry | Implemented for foundation scope and retained. | Latest video cloud has TURN registry runtime/docs and contracts describe `/v1/turn/nodes/*` as service/operator control-plane routes for WebRTC ICE server selection. | `rtk_video_cloud`, `rtk_cloud_contracts_doc` |
| Private cloud packaging | Partial. | Account manager, admin dashboard, video cloud, and workspace have runbooks/evidence foundations; frontend production backup/restore depth and production operations polish remain. | multiple |
| Product readiness evidence | Partial. | Account-side readiness smoke and workspace aggregation exist; admin production-mode upstream fact precedence remains owner-repo work. | `rtk_cloud_admin` |
| Release validation | Implemented for documentation/tooling foundation. | Coverage export and Pro2 live-lab tooling/report templates exist; actual live evidence still depends on release-candidate environment and hardware availability. | `rtk_cloud_client` |

## Foundation Issues To Open

### `hkt999rtk/rtk_account_manager`

#### `[Private Cloud] Add deployment packaging and operations runbook`

Status: implemented in `rtk_account_manager`.

Summary: make account manager deployable as part of the private-cloud package
with service-local install, migration, upgrade, rollback, backup, and restore
instructions.

Dependencies:

- `docs/private-cloud-deployment.md`
- `repos/rtk_account_manager/docs/SPEC.md`
- current migration and service startup model

Acceptance criteria:

- document single-node evaluation and production-like deployment profiles for
  account manager
- document environment variables, database migration sequence, health/smoke
  checks, upgrade, rollback, backup, and restore
- define how auth token delivery, SMTP/log adapters, quota notifications, and
  platform-admin operations are configured for private deployments
- avoid storing secrets in docs; reference secret categories and operator-owned
  storage only

#### `[Private Cloud] Add account-manager readiness evidence smoke script`

Status: implemented in `rtk_account_manager`.

Summary: add a read-only smoke/evidence command that proves account-manager
private-cloud readiness without mutating production data unexpectedly.

Dependencies:

- `docs/private-cloud-deployment.md`
- account manager auth/org/device/provisioning APIs
- current audit and metrics endpoints

Acceptance criteria:

- provide a script or documented command sequence that records service version,
  health, migration status, auth/login smoke, organization/device smoke, and
  provisioning/readiness evidence
- output a redacted artifact safe to attach to a GitHub issue or deployment
  sign-off
- support explicit skip markers for disabled optional features such as SMTP or
  cross-service channel
- include tests or dry-run validation where practical

### `hkt999rtk/rtk_cloud_admin`

#### `[Private Cloud] Add production deployment profile for admin dashboard`

Status: implemented in `rtk_cloud_admin`.

Summary: turn the admin dashboard's demo-capable Go/React app into a documented
private-cloud production service.

Dependencies:

- `docs/private-cloud-deployment.md`
- `repos/rtk_cloud_admin/docs/SPEC.md`
- Account Manager and Video Cloud upstream base URLs

Acceptance criteria:

- document container or native deployment, persistent SQLite/cache storage,
  reverse proxy/TLS assumptions, admin bootstrap secrets, backup/restore, and
  rollback
- document production-mode upstream configuration for Account Manager and Video
  Cloud, including failure behavior when an upstream is unavailable
- distinguish local demo/cache data from authoritative upstream account/video
  data
- add or update smoke checks for `/healthz`, login/session, upstream health, and
  selected dashboard APIs

#### `[Readiness] Use upstream account/video readiness and telemetry facts for production mode`

Summary: ensure production-mode admin dashboard views prefer authoritative
upstream facts instead of demo/cache-only projections.

Dependencies:

- `repos/rtk_cloud_contracts_doc/PRODUCT_READINESS.md`
- `repos/rtk_cloud_contracts_doc/TELEMETRY_INSIGHTS.md`
- Account Manager device/readiness projection APIs
- Video Cloud telemetry and fleet health APIs

Acceptance criteria:

- document and implement the production-mode precedence for account registry,
  video activation/transport, readiness, fleet health, and telemetry facts
- preserve demo mode, but make demo/cache state visibly non-authoritative
- handle upstream unavailable, stale, unauthorized, and partial-data cases with
  stable UI/API states
- add tests for upstream success, upstream failure, org isolation, telemetry
  mapping, and no leakage of raw `video_cloud_devid` when the UI should hide it

### `hkt999rtk/rtk_cloud_frontend`

#### `[Private Cloud] Add production deployment profile and backup/restore notes`

Status: still open for production backup/restore depth in `rtk_cloud_frontend`.

Summary: align the public website/lead portal deployment docs with the
private-cloud BOM.

Dependencies:

- `docs/private-cloud-deployment.md`
- `repos/rtk_cloud_frontend/docs/SPEC.md`
- current container/native deployment and SQLite lead/analytics storage

Acceptance criteria:

- document production profile for public website deployment, persistent lead and
  analytics storage, reverse proxy/TLS, cache headers, health checks, backup,
  restore, and rollback
- document how private-cloud wording should map to actual deployed package status
- keep website lead admin separate from the real admin dashboard in
  `rtk_cloud_admin`
- add or update tests/docs checks for deployment-sensitive paths where practical

### `hkt999rtk/rtk_cloud_workspace`

#### `[Private Cloud] Add product-level evidence collector wrapper`

Status: implemented in `rtk_cloud_workspace`.

Summary: add a workspace-level read-only wrapper that collects per-service
private-cloud readiness evidence into one redacted bundle.

Dependencies:

- `docs/private-cloud-deployment.md`
- service-local evidence/smoke commands from frontend, account manager, admin,
  video cloud, EMQX, and broker components

Acceptance criteria:

- `scripts/collect-private-cloud-evidence.sh` collects pinned service commits, selected service versions, health checks,
  metrics links/snapshots, smoke outputs, broker status, and backup evidence
  references
- never print tokens, DSNs, private keys, or raw customer data
- include explicit `PASS`, `FAIL`, and `SKIP` markers with reasons
- produce a deterministic artifact directory and optional tarball suitable for deployment
  sign-off
- document configuration, redaction, and acceptance rules in
  `docs/product-level-evidence.md`

#### `[Private Cloud] Decide and document cross-service broker packaging`

Status: implemented in `rtk_cloud_workspace`.

Summary: decide how NATS JetStream or an approved equivalent is packaged for
private-cloud account/video lifecycle deployments.

Dependencies:

- `docs/private-cloud-deployment.md`
- `repos/rtk_account_manager/docs/PROVISIONING_AND_EVENT_CHANNEL_PLAN.md`
- `repos/rtk_video_cloud/docs/cross-service-lifecycle-runbook.md`

Acceptance criteria:

- decide whether broker packaging is owned by workspace, video cloud deploy
  assets, account manager deploy docs, or external platform/operator docs:
  `docs/cross-service-broker-packaging.md` assigns product requirements to the
  workspace, service client/runtime behavior to account manager and video cloud,
  and broker install/ops to the platform/operator
- document single-node evaluation and production-like broker profiles in
  `docs/cross-service-broker-packaging.md`
- document required streams, retention, dead-letter handling, backup/restore, and
  smoke checks in `docs/cross-service-broker-packaging.md`
- update private-cloud BOM/runbook links after the owner decision is made

### `hkt999rtk/rtk_cloud_client`

#### `[Release] Add Android/iOS/native coverage export and release validation artifacts`

Status: implemented in `rtk_cloud_client`.

Summary: close deterministic release evidence gaps for package consumers.

Dependencies:

- `repos/rtk_cloud_client/docs/TESTING.md`
- `repos/rtk_cloud_client/docs/DELIVERABLES_AND_TEST_PROGRAMS.md`
- `repos/rtk_cloud_client/docs/RELEASE_VALIDATION_REPORT.md`

Acceptance criteria:

- add native coverage export where supported by the host toolchain
- add Android Jacoco or equivalent coverage export for macOS-capable runner paths
- add iOS `xccov`/`xcrun` coverage export for simulator-capable runner paths
- record generated artifact paths in release validation reports
- preserve explicit `SKIP` behavior on hosts that cannot run mobile toolchains

#### `[Release] Add Pro2/FreeRTOS live-lab release test program`

Status: implemented in `rtk_cloud_client`.

Summary: define and automate the release evidence path for Pro2/FreeRTOS live
hardware validation.

Dependencies:

- `repos/rtk_cloud_client/docs/TESTING.md`
- Pro2 vendor SDK and ASDK artifacts from Git LFS
- a configured video-cloud test server and device credentials

Acceptance criteria:

- define required host, board, firmware, credentials, and server prerequisites
- add a repeatable live-lab command or test-program wrapper for Pro2 board build
  and device/server interop
- capture pass/fail/skip/block summary, logs, artifact checksums, client commit,
  contracts commit, and server commit when applicable
- skip cleanly when hardware, credentials, LFS artifacts, or server setup are not
  available

## Non-Goals For This Batch

- Do not open smart-home schedules/scenes, Matter, Alexa, Google Assistant, or
  consumer household-sharing implementation issues in this batch.
- Do not reopen OTA campaign resource/policy/cancel/archive issues.
- Do not reopen baseline telemetry ingestion or SDK telemetry helper issues.
- Do not reopen PKI/mTLS server-side foundation work; latest video cloud and
  contracts already cover it.
- Do not copy full design docs into issue bodies; link to pushed workspace docs.
