# Realtek Connect+ Implementation Gap Notes

Status: discussion note.

Date: 2026-06-06.

Purpose: record the observed differences between Realtek Connect+ public/product
positioning and the current RTK Cloud implementation. This note is for product
and engineering planning. It does not change customer-facing copy.

## Snapshot

Current local discussion snapshot used for this comparison:

| Repository | Commit | Note |
| --- | --- | --- |
| `repos/rtk_cloud_frontend` | `39d5bb4` | Website has private-cloud content, analytics, OTA policy promotion, business-model disclosure, and updated feature pages. |
| `repos/rtk_account_manager` | `796988e` | Account lifecycle, evaluation signup/quota, fleet registry, provisioning, audit metrics, app-certificate bootstrap, and account-side readiness vocabulary are present. |
| `repos/rtk_cloud_admin` | `7f14fca` | Admin dashboard has signup UI, fleet/stream health, firmware/OTA, telemetry, customer/platform views, upstream proxy paths, and stable unavailable states for missing production sources. |
| `repos/rtk_cloud_client` | `7f06f49` | SDK campaign helpers, telemetry typed helpers, Go SDK, Android/iOS PKI work, Pro2 lifecycle/media work, and release-validation docs are present. |
| `repos/rtk_video_cloud` | `972a0f5` | Video cloud has firmware campaign foundation, product telemetry ingestion, fleet health, stream stats, per-device telemetry, metrics exporter, cert issuer, TURN registry, WebRTC-only backend path, canonical local reports, and packaging assets. |
| `repos/rtk_cloud_contracts_doc` | `22c3eee` | Product readiness, telemetry, metrics, onboarding, auth, OTA, WebRTC-only streaming, and ecosystem contracts are present. |

This snapshot is intentionally explicit so GitHub issues can link back to the
workspace pins used by this planning note.

## Source Files

Primary external-facing source:

- `repos/rtk_cloud_frontend/README.md`
- `repos/rtk_cloud_frontend/docs/SPEC.md`

Primary implementation and contract sources:

- `repos/rtk_cloud_contracts_doc/PRODUCT_READINESS.md`
- `repos/rtk_cloud_contracts_doc/TELEMETRY_INSIGHTS.md`
- `repos/rtk_cloud_contracts_doc/METRICS_EXPORT.md`
- `repos/rtk_cloud_contracts_doc/FIRMWARE_CAMPAIGN.md`
- `repos/rtk_account_manager/docs/SPEC.md`
- `repos/rtk_video_cloud/docs/architecture.md`
- `repos/rtk_video_cloud/docs/firmware-campaign-alignment.md`
- `repos/rtk_cloud_admin/docs/SPEC.md`
- `repos/rtk_cloud_client/docs/TESTING.md`
- `docs/private-cloud-deployment.md`

## Classification Legend

| Classification | Meaning |
| --- | --- |
| `implemented` | Implemented service or package behavior exists in the inspected repositories. |
| `partial` | A real implementation exists, but it covers only part of the external capability. |
| `foundation-gap` | Implementation exists, but deployment, evidence, packaging, or release validation is not yet sufficient for private-cloud/product commitments. |
| `roadmap` | Promotion describes a valid product direction, but implementation is not generally available in the inspected sources. |
| `wording-risk` | Current external wording could be interpreted as generally available even though the implementation is narrower. |
| `out-of-scope-for-current-batch` | Capability is intentionally excluded from the May 2026 foundation issue batch. |

## Gap Matrix

| Area | Classification | Current implementation evidence | Gap to discuss |
| --- | --- | --- | --- |
| Local onboarding | `partial`, `roadmap` | SDK local onboarding interfaces and explicit unsupported behavior exist, but real BLE/SoftAP credential handoff depends on app/firmware decisions. | Keep local Wi-Fi/BLE onboarding out of foundation private-cloud issues unless app/firmware owners are ready. |
| Product-level provisioning/readiness | `implemented`, `evidence-dependent` | Contracts define product readiness; account manager has Claim Token resolve, registry-only readiness, and readiness smoke; video cloud exposes source facts; admin derives customer-safe readiness from account/video facts; workspace has a product-level evidence wrapper. | Live staging and hardware sign-off still need explicit report evidence when environments are available. |
| Cross-service provisioning channel | `partial`, `foundation-gap` | Account/video lifecycle workers and broker boundaries exist; workspace now documents NATS JetStream as the default cross-service broker packaging decision. | Need operator deployment evidence and service-local broker smoke output when lifecycle channel is enabled. |
| Account/user management | `implemented` | Account manager includes signup, email verification, forgot/reset password, current-user password change, user disable/delete, evaluation quota, quota raise workflow, audit, and metrics. | Do not reopen baseline account lifecycle; social login remains deferred outside this batch. |
| OTA campaign depth | `implemented`, `roadmap` | Video cloud implements campaign resource/persistence, schedule/time-window/user-consent gates, cancel/archive, pause/resume, group targeting, and analytics foundation; SDK helpers exist. | Do not reopen foundation OTA issues. Approval workflow, staged percentage rollout, and commercial dashboard depth are roadmap. |
| Fleet management | `implemented`, `evidence-dependent` | Account manager has device groups/tags and registry primitives; video cloud has fleet health/stream/firmware read models; admin dashboard has fleet health/device views backed by upstream proxy behavior. | Foundation issue focus is deployability/evidence, not new fleet features. Live production evidence remains environment-owned. |
| Insights and telemetry | `implemented`, `evidence-dependent` | Contracts, video telemetry ingestion/query endpoints, SDK typed helpers, and admin telemetry/fleet-health display exist. | Baseline ingestion and admin proxy behavior are not gaps; production evidence remains a release/readiness report item. |
| App SDK release readiness | `implemented`, `evidence-dependent` | SDK packages and docs include campaign, telemetry, PKI, Go SDK, Pro2 docs, coverage export paths, and live-lab wrapper/report templates. | Actual release sign-off still needs environment-specific live runs and hardware/credential availability. |
| WebRTC / TURN | `implemented`, `migration-validated` | Server-side signaling, TURN credential policy, TURN registry control plane, and WebRTC-only stream stats exist. Active backend/product paths use WebRTC Video over TURN only; legacy RTSP relay and legacy video relay terms are restricted to migration/history/negative-test context. | Do not open new RTSP/legacy relay enhancement work. Keep final grep evidence in `docs/backend-release-readiness.md`. |
| MQTT transport and broker | `implemented`, `foundation-gap` | Video cloud has MQTT adapter and EMQX reference broker packaging; workspace has cross-service broker packaging guidance. | Remaining work is operator evidence and service-local smoke/runbook depth, not the workspace broker decision. |
| Private cloud | `implemented`, `evidence-dependent` | Workspace BOM/evidence wrapper exists; video cloud has mature deploy assets and local canonical reports; account manager and admin have private-cloud deployment docs; frontend has production deployment notes and multilingual public content. | Need live environment evidence, frontend backup/restore polish where it differs from the BOM, and operator-owned artifact/cutover sign-off. |
| Matter, voice assistants, smart-home scenes/schedules | `roadmap`, `out-of-scope-for-current-batch` | Contracts classify these as roadmap or integration-ready boundaries, not generally available service capabilities. | Do not open in this foundation batch. Revisit after deployability/evidence work is closed. |

## May 2026 Foundation Issue Strategy

Open only issues that improve live evidence, release sign-off, frontend launch
polish, or roadmap capabilities with named owners. Backend foundation issues for
private-cloud deployability, production-mode admin behavior, telemetry
ingestion, WebRTC/TURN, PKI/mTLS, and SDK release validation are no-reopen
unless a regression is found.

Do not reopen issues for:

- OTA campaign backend or SDK helper foundation
- product telemetry ingestion or SDK typed helper baseline
- account lifecycle baseline
- PKI/mTLS server-side foundation work now present in video cloud and contracts

## Recommended Next Step

Use `docs/backend-release-readiness.md` as the release checklist for this
snapshot. New issues should target concrete live evidence or roadmap work rather
than reopening completed backend foundation items.
