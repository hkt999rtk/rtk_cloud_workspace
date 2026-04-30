# Realtek Connect+ Implementation Gap Notes

Status: discussion note.

Date: 2026-04-30.

Purpose: record the observed differences between the external Realtek Connect+
promotion content and the current RTK Cloud implementation. This note is for
future product/engineering discussion. It does not change customer-facing copy.

## Snapshot

Current local discussion snapshot used for this comparison:

| Repository | Commit | Note |
| --- | --- | --- |
| `repos/rtk_cloud_frontend` | `b676c13` | Current local frontend snapshot used for public-copy evidence. |
| `repos/rtk_account_manager` | `16826bc` | Pushed docs-only contracts submodule sync. |
| `repos/rtk_cloud_client` | `8498e85` | Pushed docs-only contracts submodule sync. |
| `repos/rtk_video_cloud` | `5ce1544` | Pushed docs-only contracts submodule sync. |
| `repos/rtk_cloud_contracts_doc` | `43837a7` | Product onboarding interface source of truth. |

This snapshot is intentionally explicit so GitHub issues can link back to the exact
workspace pins used by this planning note.

## Source Files

Primary external-facing source:

- `repos/rtk_cloud_frontend/README.md`
- `repos/rtk_cloud_frontend/docs/SPEC.md`

Primary implementation and contract sources:

- `repos/rtk_cloud_contracts_doc/PROVISION.md`
- `repos/rtk_cloud_contracts_doc/CROSS_SERVICE_CHANNEL.md`
- `repos/rtk_cloud_contracts_doc/DEVICE_TRANSPORT.md`
- `repos/rtk_cloud_contracts_doc/HTTP_API.md`
- `repos/rtk_account_manager/docs/SPEC.md`
- `repos/rtk_account_manager/docs/PROVISIONING_AND_EVENT_CHANNEL_PLAN.md`
- `repos/rtk_video_cloud/docs/architecture.md`
- `repos/rtk_cloud_client/README.md`

## Baseline Reading

The frontend repository already distinguishes the website from the implemented
cloud services. Its README says the project is a `v0.1 Marketing Foundation` and
is not yet a complete IoT console, authentication service, OTA service,
provisioning backend, or telemetry platform
(`repos/rtk_cloud_frontend/README.md:5`, `repos/rtk_cloud_frontend/README.md:7`,
`repos/rtk_cloud_frontend/README.md:9`).

The frontend SPEC also says the public website tracks website v1 representation,
not live cloud-service implementation
(`repos/rtk_cloud_frontend/docs/SPEC.md:177`,
`repos/rtk_cloud_frontend/docs/SPEC.md:186`,
`repos/rtk_cloud_frontend/docs/SPEC.md:206`).

The gap below is therefore not "the website forgot to say it is marketing."
The issue is that some feature pages describe product capabilities that are not
yet present as implemented service capabilities, or that belong to a different
repo/service than the inspected implementation currently provides.

## Classification Legend

| Classification | Meaning |
| --- | --- |
| `implemented` | Implemented service or package behavior exists in the inspected repositories. |
| `partial` | A real implementation exists, but it covers only part of the external capability. |
| `integration-gap` | Contracts and one or more sides exist, but end-to-end deployment or counterpart behavior still needs confirmation/hardening. |
| `roadmap` | Promotion describes a valid product direction, but implementation was not found in the inspected sources. |
| `wording-risk` | Current external wording could be interpreted as generally available even though the implementation is narrower. |
| `out-of-scope-for-current-repos` | Capability may belong to another repo, app, firmware layer, or future service rather than the current backend repos. |

## Gap Matrix

| Area | Classification | External statement or implication | Current implementation evidence | Gap to discuss |
| --- | --- | --- | --- | --- |
| Local onboarding | `out-of-scope-for-current-repos`, `wording-risk` | Frontend feature scope includes Wi-Fi/BLE onboarding, activation, binding, and user-device association (`repos/rtk_cloud_frontend/docs/SPEC.md:167`). | Contracts explicitly exclude BLE provisioning, SoftAP provisioning, local Wi-Fi credential transport, QR onboarding UX, ECDH/challenge-response, and manufacturing CA policy (`repos/rtk_cloud_contracts_doc/PROVISION.md:39`, `repos/rtk_cloud_contracts_doc/PROVISION.md:43`, `repos/rtk_cloud_contracts_doc/PROVISION.md:48`). Contracts say provisioning means cross-service registry/bootstrap/activation (`repos/rtk_cloud_contracts_doc/PROVISION.md:55`, `repos/rtk_cloud_contracts_doc/PROVISION.md:73`). | Local device onboarding UX and credential exchange are not contractually implemented in the current backend/contracts repos. Decide whether this belongs to mobile app, firmware, or a separate onboarding service. |
| Product-level provisioning | `partial`, `integration-gap` | Promotion can read like a unified provisioning service. | Contracts say there is no unified `POST /provision`; provisioning is composed from account-manager APIs, video activation/token/transport APIs, and cross-service channel (`repos/rtk_cloud_contracts_doc/PROVISION.md:73`, `repos/rtk_cloud_contracts_doc/PROVISION.md:76`). Product-level provisioning needs the cross-service channel to bind account-manager registry state and video activation (`repos/rtk_cloud_contracts_doc/PROVISION.md:489`). | Keep discussion clear: product-level provisioning is multi-service orchestration, not a single backend API today. |
| Cross-service provisioning channel | `integration-gap` | Realtek Connect+ presents provisioning as a platform capability. | Account manager now implements account-side provisioning/deactivation APIs, outbox/inbox, metadata projection, local broker, and Azure Event Hubs adapter (`repos/rtk_account_manager/docs/PROVISIONING_AND_EVENT_CHANNEL_PLAN.md:14`, `repos/rtk_account_manager/docs/PROVISIONING_AND_EVENT_CHANNEL_PLAN.md:18`, `repos/rtk_account_manager/docs/PROVISIONING_AND_EVENT_CHANNEL_PLAN.md:25`). The remaining follow-up is video-side worker hardening for invalid-payload failure correlation, retryable deactivation redelivery, and durable `operation_id` / dead-letter behavior (`repos/rtk_account_manager/docs/PROVISIONING_AND_EVENT_CHANNEL_PLAN.md:27`, `repos/rtk_account_manager/docs/PROVISIONING_AND_EVENT_CHANNEL_PLAN.md:29`). | Account side is implemented. Before claiming end-to-end production completeness, confirm the video-side lifecycle worker hardening and deployment docs. |
| Account/user management | `partial`, `wording-risk` | Frontend lists sign up, sign in, OTP verification, third-party login, password recovery/change, and account deletion (`repos/rtk_cloud_frontend/docs/SPEC.md:171`). | Account manager v1 includes email/password auth, JWT, refresh tokens, org membership, RBAC, and device registry (`repos/rtk_account_manager/docs/SPEC.md:15`, `repos/rtk_account_manager/docs/SPEC.md:20`, `repos/rtk_account_manager/docs/SPEC.md:23`). User identity is email/password based (`repos/rtk_account_manager/docs/SPEC.md:115`). | Basic account backend exists. OTP, social login, password recovery/change, and account deletion are not shown as implemented service capabilities in the inspected docs. |
| Smart-home app experience | `roadmap`, `out-of-scope-for-current-repos` | Frontend lists remote/local control, schedules, scenes, grouping, sharing, push notifications, alerts, and household flows (`repos/rtk_cloud_frontend/docs/SPEC.md:170`). | Video cloud implements video/device cloud surfaces: device lifecycle, stream issuance, clip/snapshot/blob, MQTT/WebSocket transport, firmware, telemetry metrics, notify adapters (`repos/rtk_video_cloud/docs/architecture.md:30`, `repos/rtk_video_cloud/docs/architecture.md:50`). | Smart-home rule engine, schedules, scenes, household sharing, and consumer mobile push flows are product-direction items, not clearly implemented service features. Decide whether they require a separate smart-home/app backend. |
| OTA campaign depth | `partial`, `wording-risk` | Frontend lists firmware upload, campaign rollout, job status, cancel/archive, version validation, and force/normal/scheduled/user-controlled/time-window modes (`repos/rtk_cloud_frontend/docs/SPEC.md:168`). | Contracts expose firmware routes such as publish, upgrade, enum, enable, whitelist, query/report/cancel rollout, and download (`repos/rtk_cloud_contracts_doc/HTTP_API.md:89`, `repos/rtk_cloud_contracts_doc/HTTP_API.md:101`). Video cloud architecture says firmware rollout state tracks per-device target/current/status and only lists `pending`, `applied`, `failed`, `canceled` (`repos/rtk_video_cloud/docs/architecture.md:109`, `repos/rtk_video_cloud/docs/architecture.md:114`). | Basic firmware lifecycle exists. Rich campaign policies, archive/approval flow, and scheduled/user-controlled/time-window semantics are not clearly represented as implemented backend behavior. |
| Fleet management | `partial`, `wording-risk` | Frontend lists registry, groups, tags, batch operations, timezone, and sharing (`repos/rtk_cloud_frontend/docs/SPEC.md:169`). | Account manager includes org-owned devices, categories, CRUD, status, and RBAC (`repos/rtk_account_manager/docs/SPEC.md:21`, `repos/rtk_account_manager/docs/SPEC.md:31`). V1 explicitly excludes telemetry ingestion, command dispatch, cert management, and multi-region concerns (`repos/rtk_account_manager/docs/SPEC.md:35`, `repos/rtk_account_manager/docs/SPEC.md:45`). | Registry and status exist. Batch operations, fleet console widgets, certificate lifecycle, and full operator workflows are not clearly implemented. |
| Insights and telemetry | `partial`, `wording-risk` | Frontend lists activation statistics, firmware distribution, logs, crash reports, reboot reasons, RSSI, and memory metrics (`repos/rtk_cloud_frontend/docs/SPEC.md:173`). | Video cloud has HTTP metrics, JSON snapshots, Prometheus exposition, operational counters, optional CloudWatch sink, logs/events/statistics surfaces (`repos/rtk_video_cloud/docs/architecture.md:50`, `repos/rtk_video_cloud/docs/architecture.md:144`, `repos/rtk_video_cloud/docs/architecture.md:154`). Account manager v1 excludes telemetry ingestion (`repos/rtk_account_manager/docs/SPEC.md:35`, `repos/rtk_account_manager/docs/SPEC.md:40`). | Operational metrics/logging exist. Productized crash/reboot/RSSI/memory telemetry ingestion and dashboard story is not confirmed. |
| App SDK | `partial` | Frontend lists iOS/Android SDK layers, sample app and rebrand path, push notifications, app publishing path (`repos/rtk_cloud_frontend/docs/SPEC.md:172`). | Client repo has native, Android, JS/TS, and iOS packages (`repos/rtk_cloud_client/README.md:3`, `repos/rtk_cloud_client/README.md:9`). Implemented surfaces include token, activation/device/config subset, lifecycle/event/log/command helpers, stream helpers, WebRTC signaling, WebSocket/snapshot helpers; native has MQTT while Android/JS/iOS report MQTT unsupported or adapter-not-configured (`repos/rtk_cloud_client/README.md:159`, `repos/rtk_cloud_client/README.md:166`). Latest local client history includes live MQTT interop work, but the documented public scope still keeps MQTT parity native-only. | SDK core exists, but full mobile app delivery/rebrand/push/app-store path and parity MQTT support across all languages are not confirmed. |
| WebRTC | `partial` | Promotion may imply video streaming feature completeness. | Client SDK explicitly scopes WebRTC to signaling helpers and says peer connection/media-engine WebRTC integration is out of scope (`repos/rtk_cloud_client/README.md:162`, `repos/rtk_cloud_client/README.md:168`). Video cloud exposes formal stream modes including `webrtc` via dedicated routes (`repos/rtk_video_cloud/docs/architecture.md:70`, `repos/rtk_video_cloud/docs/architecture.md:77`). | Server-side signaling/stream issuance exists, but client media engine integration is not included in current SDK package scope. |
| MQTT transport and broker | `implemented`, `partial` | Frontend includes MQTT over TLS as an integration path (`repos/rtk_cloud_frontend/docs/SPEC.md:175`). | Contracts define MQTT as a supported secondary owner transport, with websocket priority and single-owner routing (`repos/rtk_cloud_contracts_doc/DEVICE_TRANSPORT.md:19`, `repos/rtk_cloud_contracts_doc/DEVICE_TRANSPORT.md:24`, `repos/rtk_cloud_contracts_doc/DEVICE_TRANSPORT.md:30`, `repos/rtk_cloud_contracts_doc/DEVICE_TRANSPORT.md:45`). Current local video-cloud architecture says the MQTT adapter is in-tree, EMQX is the self-hosted reference broker, and local broker bring-up is documented (`repos/rtk_video_cloud/docs/architecture.md:81`, `repos/rtk_video_cloud/docs/architecture.md:89`). | MQTT server-side support is valid to discuss as a service component. In the current local video-cloud snapshot, EMQX is documented as the self-hosted reference broker rather than an in-process Go broker. Client-side MQTT parity still differs by SDK language. |
| Private cloud | `partial`, `wording-risk` | Frontend lists public evaluation vs private commercial deployment, data ownership, custom domain, regional placement, upgrade path, deployment FAQ, and commercial support positioning (`repos/rtk_cloud_frontend/docs/SPEC.md:174`). | Video cloud has process entrypoints, Postgres persistence, blob adapters, operational endpoints, workers, EMQX reference broker packaging, and service deployment building blocks (`repos/rtk_video_cloud/docs/architecture.md:6`, `repos/rtk_video_cloud/docs/architecture.md:20`, `repos/rtk_video_cloud/docs/architecture.md:81`, `repos/rtk_video_cloud/docs/architecture.md:109`). | Deployment building blocks exist. A full managed/customer-operated private cloud product story with custom domains, regional placement, upgrade management, support model, and bill of materials needs separate confirmation. |
| Matter and voice assistants | `roadmap`, `out-of-scope-for-current-repos`, `wording-risk` | Frontend lists Matter Fabric, Alexa/Google Assistant, REST APIs, MQTT over TLS, webhooks, and cloud-to-cloud boundaries (`repos/rtk_cloud_frontend/docs/SPEC.md:175`). | Current confirmed implementation sources cover HTTP APIs, MQTT/WebSocket transport, webhooks/Event Hubs-style notification paths, and cross-service channel. No inspected implementation source confirms Matter Fabric or voice assistant integrations. | Matter and voice assistant integrations should remain roadmap/integration-positioning unless implementation is added or sourced elsewhere. |

## Provisioning Interface-First Strategy

Provisioning follow-up should be opened only after the interface documents are pushed. The normative source is `repos/rtk_cloud_contracts_doc/PRODUCT_ONBOARDING.md`; the workspace planning source is `docs/provisioning-issue-roadmap.md`. Issues should link to those documents instead of duplicating the full design.

The agreed first phase is documentation and interface only: no backend, SDK, or frontend implementation is required before the issues are opened. SDK local onboarding is expected to cover native, Android, iOS, and JavaScript/TypeScript with consistent concepts or explicit `unsupported_capability` behavior.

## Discussion Questions

1. Should frontend feature pages show availability labels such as `Available now`,
   `Integration-ready`, and `Roadmap`?
2. Is there a separate repository or private branch for mobile app UI, smart-home
   schedules/scenes, push notifications, Matter, voice assistant integrations, or
   local BLE/Wi-Fi onboarding?
3. Should OTA campaign policy semantics be implemented in `rtk_video_cloud`, or
   should the frontend wording be reduced to the current firmware rollout scope?
4. Should cross-service provisioning be described as complete only after the
   video-side lifecycle worker hardening is merged, documented, and deployable?
5. Should private cloud copy be tied to a concrete deployment bill of materials:
   API services, workers, Postgres, object storage, EMQX, reverse proxy/TLS, and
   upgrade procedure?

## Recommended Next Step

Do not change customer-facing copy yet. Use this note as the discussion agenda,
then decide which rows are product roadmap, which need implementation, which need
a separate owner repo, and which only need clearer external wording.
