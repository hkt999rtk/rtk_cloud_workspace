# Persistence Cache Refactor Roadmap

Status: implementation planning source.

Audience:

- service owners preparing database access refactors
- developers assigned follow-up GitHub issues
- reviewers checking Redis/cache readiness against source-of-truth boundaries

## Purpose

This document records the cross-repository persistence audit and the first
refactor wave needed before adding Redis-compatible cache layers. It is an
internal implementation roadmap, not a client-facing contract. Shared wire,
payload, and API contracts still belong in `repos/rtk_cloud_contracts_doc`.

The v1 goal is not to add Redis. The v1 goal is to make database access
cache-ready by tightening persistence interfaces, keeping SQL adapters in
repository-owned packages, and avoiding direct handler or workflow coupling to
concrete SQL stores where a narrower port is enough.

## Audit Summary

| Repository | Current shape | Cache-readiness risk |
| --- | --- | --- |
| `rtk_account_manager` | API handlers and workers depend on concrete `internal/store.Store`, which owns direct pgx/Postgres SQL access across account, organization, device, auth token, ACL, metrics, and lifecycle operations. | High. Redis cannot be inserted cleanly without either changing many handlers or putting cache behavior inside the monolithic store. |
| `rtk_video_cloud` | Domain services mostly depend on interfaces such as `device.Store`, `auth.RefreshStore`, `deviceshadow.Store`, firmware stores, and telemetry stores. PostgreSQL adapters are concentrated under `internal/postgres`. | Medium-low. Cache decorators can follow the existing domain interface model, but candidate stores and invalidation rules still need explicit issue scope. |
| `rtk_cloud_admin` | The BFF uses a concrete SQLite `internal/store.Store` for console-local sessions, audit, settings, demo data, and non-authoritative upstream projections. | Medium. SQLite is intentionally local, but session/projection ports should be split before adding Redis-backed session or cache behavior. |
| `rtk_cloud_frontend` | Leads, analytics, and search each use small concrete SQLite repositories. | Low. Current website data is local and low-volume; Redis is not a first-priority dependency unless a specific runtime bottleneck appears. |

## Refactor Principles

- Postgres or SQLite remains the source of truth unless an owner repository
  explicitly documents a hot-state exception.
- Redis-compatible storage should be introduced as read-through,
  write-invalidate, or explicit hot-state storage behind domain interfaces.
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
- user, organization, and device read projections after durable writes commit
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
| `hkt999rtk/rtk_account_manager` | [#201: Refactor persistence boundaries for future Redis cache support](https://github.com/hkt999rtk/rtk_account_manager/issues/201) | Open |
| `hkt999rtk/rtk_video_cloud` | [#540: Add cache-ready persistence decorator plan around existing domain stores](https://github.com/hkt999rtk/rtk_video_cloud/issues/540) | Open |
| `hkt999rtk/rtk_cloud_admin` | [#181: Extract Admin Console local store interfaces for session and projection cache](https://github.com/hkt999rtk/rtk_cloud_admin/issues/181) | Open |
| `hkt999rtk/rtk_cloud_frontend` | [#121: Document low-priority SQLite repository cache boundaries](https://github.com/hkt999rtk/rtk_cloud_frontend/issues/121) | Open |

## Acceptance For The First Wave

- Each owner repo has a scoped GitHub issue with repository-specific acceptance
  criteria.
- Account Manager issue prioritizes narrow persistence ports over cache
  implementation.
- Video Cloud issue uses the existing interface/decorator pattern instead of
  moving SQL into domain services.
- Admin Console issue keeps SQLite authoritative only for console-local data and
  keeps upstream projections non-authoritative.
- Frontend issue documents why Redis is not needed until a concrete website
  storage or search bottleneck exists.
