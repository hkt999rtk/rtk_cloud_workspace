# Persistence Cache Refactor Roadmap

Status: implementation planning source.

Audience:

- service owners preparing database access refactors
- developers assigned follow-up GitHub issues
- reviewers checking Redis/cache readiness against source-of-truth boundaries

## Purpose

This document records the cross-repository persistence audit and the cache
boundary work needed before adding Redis-compatible cache layers broadly. It is
an internal implementation roadmap, not a client-facing contract. Shared wire,
payload, and API contracts still belong in `repos/rtk_cloud_contracts_doc`.

The original v1 goal was cache readiness rather than Redis implementation. The
account-manager user/auth path now has a scoped Redis-compatible read-through
cache behind the API Store boundary. The broader roadmap still applies to
organization, device, ACL, lifecycle, metrics, and other persistence surfaces.

## Audit Summary

| Repository | Current shape | Cache-readiness risk |
| --- | --- | --- |
| `rtk_account_manager` | API handlers and workers depend on concrete `internal/store.Store`, which owns direct pgx/Postgres SQL access across account, organization, device, auth token, ACL, metrics, and lifecycle operations. The user/auth query path now has a read-through Redis-compatible decorator behind the API Store boundary. | Medium. User/auth cache exists, but broader persistence boundaries still need narrowing before caching organization, ACL, device, lifecycle, or metrics paths. |
| `rtk_video_cloud` | Domain services mostly depend on interfaces such as `device.Store`, `auth.RefreshStore`, `deviceshadow.Store`, firmware stores, and telemetry stores. PostgreSQL adapters are concentrated under `internal/postgres`. | Medium-low. Cache decorators can follow the existing domain interface model, but candidate stores and invalidation rules still need explicit issue scope. |
| `rtk_cloud_admin` | The BFF uses a concrete SQLite `internal/store.Store` for console-local sessions, audit, settings, demo data, and non-authoritative upstream projections. | Medium. SQLite is intentionally local, but session/projection ports should be split before adding Redis-backed session or cache behavior. |
| `rtk_cloud_frontend` | Leads, analytics, and search each use small concrete SQLite repositories. | Low. Current website data is local and low-volume; Redis is not a first-priority dependency unless a specific runtime bottleneck appears. |

## Refactor Principles

- Postgres or SQLite remains the source of truth unless an owner repository
  explicitly documents a hot-state exception.
- Redis-compatible storage should be introduced as read-through,
  write-invalidate, or explicit hot-state storage behind domain interfaces or
  service-level store decorators.
- Public HTTP APIs, payloads, auth semantics, and route behavior must not change
  as part of the boundary refactor.
- Write transactions, lifecycle state transitions, quota mutation, and ACL
  decisions should keep durable database semantics first. Cache invalidation may
  be added around them, but cache reads must not replace correctness-critical
  database checks without a separate design.
- Cache adapters should live beside the owning persistence boundary, not inside
  transport handlers.

## Cache Candidates

Good first candidates:

- session and refresh-token lookup where TTL is already explicit
- user/auth read projections after durable writes commit; account manager uses
  no-TTL read-through Redis cache with Postgres as truth
- organization and device read projections after durable writes commit
- metrics and dashboard summaries that can tolerate short freshness windows
- device shadow hot state when the owner repository defines Redis-first version
  and flush semantics

Use caution or avoid caching:

- multi-row write transactions
- ACL permission decisions and platform-admin authorization
- evaluation quota mutation and quota-raise decisions
- provisioning, deactivation, and cross-service lifecycle transitions
- outbox/inbox claim, retry, and dead-letter state

## Issue Tracker

| Repository | Issue | Status |
| --- | --- | --- |
| `hkt999rtk/rtk_account_manager` | [#201: Refactor persistence boundaries for future Redis cache support](https://github.com/hkt999rtk/rtk_account_manager/issues/201) | Partially implemented for user/auth read-through cache; broader persistence boundary work remains open. |
| `hkt999rtk/rtk_video_cloud` | [#540: Add cache-ready persistence decorator plan around existing domain stores](https://github.com/hkt999rtk/rtk_video_cloud/issues/540) | Open |
| `hkt999rtk/rtk_cloud_admin` | [#181: Extract Admin Console local store interfaces for session and projection cache](https://github.com/hkt999rtk/rtk_cloud_admin/issues/181) | Open |
| `hkt999rtk/rtk_cloud_frontend` | [#121: Document low-priority SQLite repository cache boundaries](https://github.com/hkt999rtk/rtk_cloud_frontend/issues/121) | Open |

## Acceptance For The First Wave

- Each owner repo has a scoped GitHub issue with repository-specific acceptance
  criteria.
- Account Manager user/auth cache uses the existing API Store abstraction and
  keeps Postgres authoritative. The runtime decorator covers
  platform/developer, brand-cloud, and end-user profile/auth projections; the
  `user-cache` rebuild/inspect/delete command is intentionally limited to
  platform users. Remaining account-manager work should prioritize narrower
  ports before adding more cache surfaces.
- Video Cloud issue uses the existing interface/decorator pattern instead of
  moving SQL into domain services.
- Admin Console issue keeps SQLite authoritative only for console-local data and
  keeps upstream projections non-authoritative.
- Frontend issue documents why Redis is not needed until a concrete website
  storage or search bottleneck exists.
