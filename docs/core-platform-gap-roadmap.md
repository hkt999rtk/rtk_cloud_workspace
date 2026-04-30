# Realtek Connect+ Core Platform Gap Roadmap

Status: supporting-note.

Owner: `rtk_cloud_workspace`.

Last reviewed: 2026-04-30.

## Purpose

This roadmap records the next implementation gaps after the Provisioning and OTA
interface-first issues. It is a planning document for GitHub issues and should
not replace service-owned specifications or cross-repo contracts.

The goal is to make Realtek Connect+ credible as a product platform, not only a
set of feature-page claims. These issues focus on foundation work that other
features will depend on: product readiness, deployability, account lifecycle,
fleet primitives, telemetry, SDK/app delivery, WebRTC client integration, and
smart-home ecosystem boundaries.

## Source Of Truth

| Topic | Source of truth | Supporting notes |
| --- | --- | --- |
| Cross-repo contracts | `repos/rtk_cloud_contracts_doc` | This roadmap only maps owner issues. |
| Account/user/device registry | `repos/rtk_account_manager/docs/SPEC.md` | Account manager owns org, user, auth, RBAC, and registry behavior. |
| Video/device runtime | `repos/rtk_video_cloud/docs/architecture.md` | Video cloud owns video activation, transport, firmware, media, and runtime signals. |
| SDK package behavior | `repos/rtk_cloud_client/docs/README.md` | SDK repo owns package-native helper and app integration surfaces. |
| Public wording | `repos/rtk_cloud_frontend/docs/SPEC.md` | Website copy should follow implementation status. |
| Gap evidence | `docs/realtek-connect-plus-gap-analysis.md` | Workspace-level comparison note. |

## Owner Matrix

| Gap | Priority | Owner repository | First deliverable |
| --- | --- | --- | --- |
| Product readiness aggregator contract | P0 | `hkt999rtk/rtk_cloud_contracts_doc` | Cross-service readiness vocabulary and source-fact contract. |
| Account readiness input alignment | P0 | `hkt999rtk/rtk_account_manager` | Stable account-side fields/API behavior for product readiness composition. |
| Video readiness input alignment | P0 | `hkt999rtk/rtk_video_cloud` | Stable video activation and transport source facts for product readiness composition. |
| Private cloud deployment BOM/runbook | P0 | `hkt999rtk/rtk_cloud_workspace` | Product-level deployment bill of materials and runbook. |
| Account/user lifecycle completeness | P1 | `hkt999rtk/rtk_account_manager` | OTP, password recovery/change, account deletion, and social-login decision record. |
| Fleet management core primitives | P1 | `hkt999rtk/rtk_account_manager` | Groups/tags/batch operation registry model and API plan. |
| Telemetry/insights event contract | P1 | `hkt999rtk/rtk_cloud_contracts_doc` | Product telemetry schema for crash/reboot/RSSI/memory/firmware distribution. |
| Telemetry ingestion implementation | P1 | `hkt999rtk/rtk_video_cloud` | Device telemetry ingestion aligned to the contract. |
| SDK/app delivery and push scope | P2 | `hkt999rtk/rtk_cloud_client` | SDK boundary for mobile app delivery, push notification helper scope, and sample/rebrand path. |
| WebRTC client media integration boundary | P2 | `hkt999rtk/rtk_cloud_client` | Decide and document whether SDK owns peer connection/media engine integration or only signaling helpers. |
| Smart-home ecosystem boundary | P3 | `hkt999rtk/rtk_cloud_contracts_doc` | Contract/design boundary for schedules, scenes, sharing, Matter, and voice assistants before implementation. |

## Issues To Open

### `hkt999rtk/rtk_cloud_contracts_doc`

#### `[Readiness] Define cross-service product readiness contract`

Summary: define the product-level readiness model that composes account registry,
claim/bind, local onboarding, video activation, and transport-online source
facts.

Dependencies:

- `PRODUCT_ONBOARDING.md`
- `PROVISION.md`
- account-manager readiness projection
- video-cloud activation and transport state

Acceptance criteria:

- define product readiness states and required source facts
- specify which service owns each source fact
- state how `registered`, `claim_pending`, `local_onboarding_pending`,
  `cloud_activation_pending`, `activated`, `online`, `failed`,
  `deactivation_pending`, and `deactivated` compose
- document failure attribution fields and retryability expectations
- avoid defining a single implementation repo unless ownership is agreed

#### `[Telemetry] Define product telemetry and insights event schema`

Summary: define a product telemetry contract for insights content that goes
beyond operational Prometheus/logging metrics.

Dependencies:

- current video-cloud metrics/logging surfaces
- account-manager device registry and org ownership model
- frontend Insights copy

Acceptance criteria:

- define event categories for crash report, reboot reason, RSSI, memory, firmware
  distribution, activation statistics, and device health signals
- define required identity fields: org, account device id, video/device id,
  model, firmware version, timestamp, and source
- define privacy/data-retention notes for customer-visible telemetry
- distinguish product telemetry from service operational metrics
- provide examples that backend, SDK, and frontend can reference

#### `[Smart Home] Define ecosystem boundary for schedules, scenes, Matter, and voice assistants`

Summary: prevent smart-home and ecosystem copy from becoming implementation
ambiguous before there is an owner service.

Dependencies:

- Realtek Connect+ frontend smart-home and integrations copy
- current account/video/client implemented surfaces

Acceptance criteria:

- classify schedules, scenes, grouping, household sharing, push alerts, Matter,
  Alexa, and Google Assistant as implemented, integration-ready, or roadmap
- identify which capabilities need new service ownership versus SDK/app-only work
- define minimum contract terms for device group, scene, automation, and external
  ecosystem binding if they become implementation scope
- explicitly state what should not be advertised as generally available yet

### `hkt999rtk/rtk_account_manager`

#### `[Readiness] Align account-side readiness inputs for product readiness composition`

Summary: make account-manager readiness facts stable enough for a product-level
readiness aggregator to consume.

Dependencies:

- cross-service product readiness contract issue
- current provisioning/readiness API

Acceptance criteria:

- confirm which account-side fields are stable source facts for product readiness
- document how org/device disabled state, latest provisioning operation,
  projected video metadata, and account device status should be consumed
- add or update tests for readiness source facts if API behavior changes
- avoid treating account-manager `status=online` as full product readiness

#### `[Account] Complete user lifecycle baseline for Realtek Connect+`

Summary: close the gap between website user-management claims and implemented
account-manager capability.

Dependencies:

- current auth/session/RBAC implementation
- frontend user-management copy

Acceptance criteria:

- decide and document first-phase scope for OTP verification, password recovery,
  password change, account deletion, and third-party/social login
- implement API routes and tests for selected first-phase capabilities
- explicitly document deferred capabilities and avoid frontend availability claims
  for unimplemented ones
- update OpenAPI and service docs when behavior lands

#### `[Fleet] Define groups, tags, and batch operation registry model`

Summary: add the fleet primitives needed by targeting, operations, and future
fleet console workflows.

Dependencies:

- current organization-owned device registry
- OTA targeting needs
- frontend fleet-management copy

Acceptance criteria:

- define groups/tags metadata model and ownership rules
- define batch operation model or explicitly defer it with owner rationale
- specify API and RBAC behavior for group/tag assignment and batch selection
- add tests for org isolation, permissions, and device selection behavior when
  implemented

### `hkt999rtk/rtk_video_cloud`

#### `[Readiness] Expose video activation and transport readiness facts for product readiness`

Summary: make video-cloud activation and owner-transport facts stable enough for
product-level readiness composition.

Dependencies:

- cross-service product readiness contract issue
- current cross-service lifecycle worker and transport ownership model

Acceptance criteria:

- document stable source facts for activation success/failure, deactivation,
  online/offline owner transport, and transport type
- expose or confirm APIs/events that an aggregator can consume
- include MQTT and WebSocket owner transport semantics without implying one equals
  full product readiness by itself
- add tests if source fact behavior changes

#### `[Telemetry] Implement device telemetry ingestion for product insights schema`

Summary: implement product telemetry ingestion after the telemetry contract is
stable.

Dependencies:

- product telemetry and insights event schema issue
- current video-cloud metrics/logging surfaces

Acceptance criteria:

- accept contract-defined device telemetry events for crash, reboot, RSSI,
  memory, firmware/version, and health signals as applicable
- validate org/device identity and model/firmware metadata
- persist or forward telemetry according to the agreed storage/retention design
- add tests for valid events, invalid identity, malformed payloads, and retention
  or forwarding behavior

### `hkt999rtk/rtk_cloud_workspace`

#### `[Private Cloud] Define deployment BOM and operations runbook`

Summary: turn private-cloud positioning into a concrete deployment package plan.

Dependencies:

- service deployment docs from account manager, video cloud, frontend, contracts,
  and EMQX reference broker packaging

Acceptance criteria:

- define required components: frontend, account manager, video cloud API/workers,
  EMQX, Postgres, object storage, reverse proxy/TLS, secrets, and observability
- define single-node evaluation and production-like deployment profiles
- document upgrade, rollback, backup, restore, and support boundaries
- identify repo-specific follow-up issues for missing deployment scripts or docs
- update frontend private-cloud wording only after package status is clear

### `hkt999rtk/rtk_cloud_client`

#### `[SDK] Define mobile app delivery, push notification, and sample/rebrand scope`

Summary: clarify what the SDK repo owns for the App SDK product story beyond
HTTP helpers.

Dependencies:

- frontend App SDK copy
- current native, Android, iOS, and JavaScript/TypeScript packages
- future push notification and smart-home/app decisions

Acceptance criteria:

- document which app-delivery pieces are SDK-owned, sample-app-owned, or product
  service-owned
- define push notification helper scope or explicitly defer it
- define sample/rebrand path expectations if the SDK repo owns any reference app
  artifacts
- identify package parity gaps such as MQTT support across Android/iOS/JS
- add tests only for SDK-owned helpers, not app-store or branding process docs

#### `[WebRTC] Define client media integration boundary beyond signaling helpers`

Summary: decide whether `rtk_cloud_client` should own WebRTC peer
connection/media engine integration or remain signaling-helper only.

Dependencies:

- current video-cloud WebRTC stream/signaling routes
- current SDK signaling helpers
- frontend video/app SDK copy

Acceptance criteria:

- document current signaling-helper boundary and what is not implemented
- decide whether to add package-native peer connection/media engine helpers,
  sample integration only, or no SDK ownership
- if implementation is selected, define package-specific acceptance tests
- ensure frontend copy does not imply complete client media streaming SDK until
  the selected scope lands

## Issue Ordering

1. Start with readiness contract plus account/video readiness source alignment.
2. In parallel, create private-cloud BOM/runbook because it is mostly
   cross-repo documentation and deployment architecture.
3. Start account lifecycle and fleet primitives before deeper frontend console or
   mobile app promises.
4. Define telemetry contract before backend ingestion or dashboard work.
5. Treat SDK/app delivery and WebRTC boundary as product-scope clarifications
   before implementation.
6. Keep smart-home/Matter/voice assistant work as boundary/design until an owner
   service exists.

## Non-Goals

- Do not reopen already-created OTA campaign or Provisioning local onboarding
  issues from `docs/implementation-gap-backlog.md`.
- Do not claim marketing availability before source repos implement and test the
  behavior.
- Do not move service-owned specs into the workspace repo; use workspace docs as
  issue routing and coordination only.
