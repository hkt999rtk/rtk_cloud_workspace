# Account Manager And Admin Dashboard Boundary

Status: source-of-truth note.

Owner: `rtk_cloud_workspace`.

Last reviewed: 2026-05-09.

## Purpose

This note records the product and server boundary between
`rtk_account_manager` and `rtk_cloud_admin`.

This is a workspace governance note. Service repositories should keep their own
documentation self-contained and should not link back to this file. Cross-repo
references from service docs should point only to the contracts repository when
they need a normative shared contract.

The two repositories share user-facing nouns such as users, organizations,
devices, provisioning, quota, and dashboards. That overlap is intentional at
the facade/UI layer, but their ownership is different:

- `rtk_account_manager` is the authoritative backend control-plane service.
- `rtk_cloud_admin` is the enterprise/admin dashboard and Backend-for-Frontend
  (BFF).

## One-line Boundary

`rtk_account_manager` owns identity, tenant context, authorization,
entitlement, device registry, and provisioning intent.

`rtk_cloud_admin` owns the enterprise dashboard UX, BFF routes, proxying,
aggregation, console-local session state, and operator-facing views.

## Account Manager Responsibilities

`rtk_account_manager` is a backend API service. It should not own a product
Web UI.

It owns:

- user identity, email/password authentication, email verification, password
  reset, JWT access tokens, and refresh tokens
- organization/tenant records
- organization membership and roles such as `owner`, `admin`, and `member`
- organization-owned device registry records
- device groups and tags used as account-side fleet selection metadata
- evaluation/commercial tier and quota/entitlement fields
- quota-raise workflow APIs
- claim token, bind material, and account-side provisioning APIs
- provisioning/deactivation operation intent and operation state
- account-to-video cross-service command publication
- video-to-account event projection for activation, online state, and selected
  video metadata
- account-domain audit, metrics, readiness smoke evidence, and platform-admin
  domain APIs

It does not own:

- enterprise dashboard UI
- end-user app dashboard UI
- marketing or promotion website content
- video streaming runtime
- video media/session handling
- telemetry ingestion runtime
- firmware campaign execution
- local console preferences or demo dashboard cache

SSO note: `rtk_account_manager` provides SSO-like authentication behavior, but
it is broader than a pure Single Sign-On service because it also owns tenant,
authorization, entitlement, device registry, and provisioning control-plane
state.

## Admin Dashboard Responsibilities

`rtk_cloud_admin` is the enterprise/admin dashboard portal. It is not a
canonical account, organization, device, or provisioning system of record.

It owns:

- enterprise/customer/operator dashboard UX
- React frontend and Go BFF routes used by that dashboard
- customer signup/login page wiring and email verification landing pages
- current dashboard session and active-organization UI state
- platform/operator console entry point
- proxying and aggregating Account Manager and Video Cloud APIs
- device, provisioning, activation, readiness, telemetry, firmware, stream, and
  service-health views
- console-local platform-admin users when used as the dashboard operator entry
  point
- console-local sessions, preferences, settings, audit entries, demo data, and
  non-authoritative projection caches

It does not own:

- canonical user credentials or auth tokens
- canonical organization/tenant records
- canonical organization membership or role policy
- canonical device ownership
- claim token or bind material source of truth
- provisioning/deactivation source of truth
- video activation, streaming, transport, telemetry, or firmware runtime

Production rule: when upstream services are configured, `rtk_cloud_admin` must
prefer upstream Account Manager and Video Cloud facts. It may cache and display
those facts, but cache records must remain non-authoritative.

## Tenant And Dashboard Model

The enterprise tenant context lives in `rtk_account_manager` as organization
and membership data:

```text
User
  belongs to one or more Organizations
    with Role: owner / admin / member
      owns Devices / Quota / Provisioning state
```

`rtk_cloud_admin` may remember which organization is selected in the current
dashboard session, but it must not create a separate enterprise-customer source
of truth.

There are two dashboard concepts:

| Dashboard | Primary users | Scope | Owner |
| --- | --- | --- | --- |
| End-user dashboard or app dashboard | End users / device owners | My account, my home, my devices, streams, alerts, OTA state | App/client or future user portal |
| Enterprise admin dashboard | Enterprise customers, fleet managers, operators, support admins | My organization, fleet, quota, provisioning, service health, cross-device operations | `rtk_cloud_admin` |

`rtk_cloud_admin` is the second category only. It is not the owner of every
possible dashboard in the product.

## Provisioning Flow Boundary

Device provisioning must not require `rtk_cloud_admin`.

Canonical product flow:

```text
Device / App / SDK
    -> rtk_account_manager
    -> cross-service provisioning command
    -> rtk_video_cloud
```

`rtk_cloud_admin` may provide an operator button for provision/deactivate, but
that button is only a dashboard action that proxies to Account Manager and
records console-local audit. It is not the owner of provisioning.

Provisioning ownership:

| Action | Owner |
| --- | --- |
| Claim token / bind material | `rtk_account_manager` |
| User, organization, and device ownership binding | `rtk_account_manager` |
| Device registry record | `rtk_account_manager` |
| Provisioning operation intent | `rtk_account_manager` |
| Video-side activation command execution | `rtk_video_cloud` |
| Provisioning status display | `rtk_cloud_admin` read/proxy view only |
| Manual operator trigger | `rtk_cloud_admin` optional proxy action only |

## Overlap Rules

The following overlap is allowed because it is facade/UI overlap, not ownership
overlap:

| Surface | Correct ownership |
| --- | --- |
| Signup/login pages | `rtk_cloud_admin` owns UI; `rtk_account_manager` owns credentials, tokens, and auth policy. |
| Organization selector | `rtk_cloud_admin` owns selected-org UI/session state; `rtk_account_manager` owns organizations, memberships, and roles. |
| Device list/detail | `rtk_cloud_admin` owns display and aggregation; `rtk_account_manager` owns registry facts; `rtk_video_cloud` owns video/runtime facts. |
| Provision/deactivate button | `rtk_cloud_admin` owns the console action surface; `rtk_account_manager` owns the canonical operation API and intent. |
| Quota display/request | `rtk_cloud_admin` owns UI; `rtk_account_manager` owns entitlement fields and quota workflow. |
| Platform/admin wording | `rtk_cloud_admin` owns the operator console session; `rtk_account_manager` owns account-domain platform admin APIs. |
| Audit | `rtk_cloud_admin` owns console-local audit; `rtk_account_manager` owns account-domain audit. |

## Guardrails

- Do not add canonical customer, organization, membership, device, quota, or
  provisioning state to `rtk_cloud_admin`.
- Do not route normal device/app provisioning through `rtk_cloud_admin`.
- Do not add enterprise dashboard UI to `rtk_account_manager`.
- Do not treat `rtk_cloud_admin` SQLite demo/cache tables as production source
  of truth.
- Do not describe `rtk_account_manager` as only an SSO service; it is an
  identity and account/device control-plane service.
- Do not describe `rtk_cloud_admin` as the product-wide dashboard for all user
  types; it is the enterprise/admin dashboard.
