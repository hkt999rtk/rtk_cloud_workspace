# Realtek Connect+ Implementation Gap Notes

Status: discussion note.

Date: 2026-05-07.

Purpose: record the observed differences between Realtek Connect+ public/product
positioning and the current RTK Cloud implementation. This note is for product
and engineering planning. It does not change customer-facing copy.

## Snapshot

Current local discussion snapshot used for this comparison:

| Repository | Commit | Note |
| --- | --- | --- |
| `repos/rtk_cloud_frontend` | `699b5d5` | Website has private-cloud content, analytics, OTA policy promotion, business-model disclosure, and updated feature pages. |
| `repos/rtk_account_manager` | `d0de155` | Account lifecycle, evaluation signup/quota, fleet registry, provisioning, audit metrics, and account-side readiness vocabulary are present. |
| `repos/rtk_cloud_admin` | `32e3e1c` | Admin dashboard has signup UI, fleet/stream health, firmware/OTA, telemetry, customer/platform views, and upstream proxy paths. |
| `repos/rtk_cloud_client` | `2ceee44` | SDK campaign helpers, telemetry typed helpers, Go SDK, Android/iOS PKI work, Pro2 lifecycle/media work, and release-validation docs are present. |
| `repos/rtk_video_cloud` | `e763170` | Video cloud has firmware campaign foundation, product telemetry ingestion, metrics exporter, cert issuer, TURN registry, and packaging assets. |
| `repos/rtk_cloud_contracts_doc` | `bad301f` | Product readiness, telemetry, metrics, onboarding, auth, OTA, and ecosystem contracts are present. |

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
| Product-level provisioning/readiness | `partial`, `foundation-gap` | Contracts define product readiness; account manager and video cloud expose source facts; admin dashboard can consume upstream facts. | Need private-cloud evidence and production-mode admin precedence so readiness is demonstrably derived from authoritative account/video facts. |
| Cross-service provisioning channel | `partial`, `foundation-gap` | Account/video lifecycle workers and broker boundaries exist; private-cloud BOM requires cross-service broker when lifecycle channel is enabled. | Need a broker packaging owner decision and runbook for NATS JetStream or approved equivalent. |
| Account/user management | `implemented` | Account manager includes signup, email verification, forgot/reset password, current-user password change, user disable/delete, evaluation quota, quota raise workflow, audit, and metrics. | Do not reopen baseline account lifecycle; social login remains deferred outside this batch. |
| OTA campaign depth | `implemented`, `roadmap` | Video cloud implements campaign resource/persistence, schedule/time-window/user-consent gates, cancel/archive, pause/resume, group targeting, and analytics foundation; SDK helpers exist. | Do not reopen foundation OTA issues. Approval workflow, staged percentage rollout, and commercial dashboard depth are roadmap. |
| Fleet management | `partial`, `foundation-gap` | Account manager has device groups/tags and registry primitives; admin dashboard has fleet health/device views. | Foundation issue focus is deployability/evidence, not new fleet features. Admin production-mode upstream fact handling remains needed. |
| Insights and telemetry | `implemented`, `foundation-gap` | Contracts, video telemetry ingestion, SDK typed helpers, and admin telemetry/fleet-health display exist. | Baseline ingestion is not a gap; production retention/deployment evidence and admin upstream behavior remain foundation concerns. |
| App SDK release readiness | `partial`, `foundation-gap` | SDK packages and docs are broad, including campaign, telemetry, PKI, Go SDK, and Pro2 docs. | Release evidence still lacks Android/iOS/native coverage exports and Pro2/FreeRTOS live-lab test-program artifacts. |
| WebRTC | `partial` | Server-side signaling/stream issuance exists; SDK boundaries still distinguish signaling from full media-engine integration. | Not part of this foundation batch unless product decides SDK should own media engine integration. |
| MQTT transport and broker | `implemented`, `foundation-gap` | Video cloud has MQTT adapter and EMQX reference broker packaging; client parity varies by package. | Private-cloud package still needs broker/runbook clarity across EMQX and cross-service broker dependencies. |
| Private cloud | `partial`, `foundation-gap` | Workspace BOM exists; video cloud has mature deploy assets; frontend/admin/account manager have partial deployment docs and runtime foundations. | Need service-local runbooks, backup/restore notes, product-level evidence wrapper, and broker packaging decision. |
| Matter, voice assistants, smart-home scenes/schedules | `roadmap`, `out-of-scope-for-current-batch` | Contracts classify these as roadmap or integration-ready boundaries, not generally available service capabilities. | Do not open in this foundation batch. Revisit after deployability/evidence work is closed. |

## May 2026 Foundation Issue Strategy

Open only foundation issues that improve private-cloud deployability, evidence,
production-mode admin behavior, and SDK release validation. The concrete issue
bodies are maintained in `docs/implementation-gap-backlog.md`.

Do not reopen issues for:

- OTA campaign backend or SDK helper foundation
- product telemetry ingestion or SDK typed helper baseline
- account lifecycle baseline
- PKI/mTLS server-side work already tracked by `hkt999rtk/rtk_video_cloud#262`

## Recommended Next Step

Update workspace planning docs, commit and push the refreshed workspace snapshot,
then open the foundation issues listed in `docs/implementation-gap-backlog.md`.
Issue bodies should link to the pushed workspace docs instead of duplicating the
full design text.
