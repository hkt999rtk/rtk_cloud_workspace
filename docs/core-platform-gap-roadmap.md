# Realtek Connect+ Core Platform Gap Roadmap

Status: supporting-note.

Owner: `rtk_cloud_workspace`.

Last reviewed: 2026-05-07.

## Purpose

This roadmap records the next foundation gaps after the latest code refresh. It
is a planning document for GitHub issues and should not replace service-owned
specifications or cross-repo contracts.

The current priority is not to reopen already-landed OTA, telemetry, account
lifecycle, or admin UI work. The priority is to make the platform deployable,
evidence-producing, and release-verifiable for private-cloud evaluation and
production-like deployments.

## Source Of Truth

| Topic | Source of truth | Supporting notes |
| --- | --- | --- |
| Cross-repo contracts | `repos/rtk_cloud_contracts_doc` | This roadmap only maps owner issues. |
| Private-cloud BOM | `docs/private-cloud-deployment.md` | Product-level deployment package, runbook, and follow-up routing. |
| Gap evidence | `docs/realtek-connect-plus-gap-analysis.md` | Workspace-level comparison note. |
| Issue backlog | `docs/implementation-gap-backlog.md` | Concrete foundation issue bodies and ordering. |
| Account/user/device registry | `repos/rtk_account_manager/docs/SPEC.md` | Account manager owns org, user, auth, RBAC, registry, signup, quota, and account-side readiness behavior. |
| Video/device runtime | `repos/rtk_video_cloud/docs/architecture.md` | Video cloud owns activation, transport, firmware campaign, telemetry, metrics, media, and runtime signals. |
| Admin dashboard | `repos/rtk_cloud_admin/docs/SPEC.md` | Admin dashboard owns the B2B operator UI/BFF and must not replace upstream systems of record. |
| SDK package behavior | `repos/rtk_cloud_client/docs/README.md` | SDK repo owns package-native helpers, release validation, and package evidence. |
| Public wording | `repos/rtk_cloud_frontend/docs/SPEC.md` | Website copy should follow implementation and deployment status. |

## Owner Matrix

| Gap | Priority | Owner repository | First deliverable |
| --- | --- | --- | --- |
| Account-manager private-cloud deployability | P0 | `hkt999rtk/rtk_account_manager` | Deployment packaging and operations runbook. |
| Account-manager readiness evidence | P0 | `hkt999rtk/rtk_account_manager` | Read-only smoke/evidence script for auth, org, device, and provisioning facts. |
| Admin dashboard production profile | P0 | `hkt999rtk/rtk_cloud_admin` | Production deployment profile for Go/React dashboard, persistence, upstreams, and rollback. |
| Admin upstream readiness/telemetry mode | P0 | `hkt999rtk/rtk_cloud_admin` | Production-mode use of authoritative account/video readiness and telemetry facts. |
| Frontend private-cloud deployment docs | P1 | `hkt999rtk/rtk_cloud_frontend` | Production deployment profile and backup/restore notes for website lead/analytics storage. |
| Product-level evidence collector | P1 | `hkt999rtk/rtk_cloud_workspace` | Read-only wrapper that gathers service evidence into a redacted bundle. |
| Cross-service broker packaging | P1 | `hkt999rtk/rtk_cloud_workspace` | Owner decision and packaging/runbook plan for NATS JetStream or approved equivalent. |
| SDK release coverage artifacts | P1 | `hkt999rtk/rtk_cloud_client` | Android/iOS/native coverage exports and release validation artifacts. |
| Pro2/FreeRTOS live-lab validation | P1 | `hkt999rtk/rtk_cloud_client` | Live hardware release test program with clean skip/block reporting. |

## Completed Or No-Reopen Baseline

| Area | Current decision |
| --- | --- |
| OTA campaign backend | Latest video cloud has campaign resource/persistence, policy gates, cancel/archive, pause/resume, group targeting, and analytics foundation. Do not reopen old campaign foundation issues. |
| OTA SDK helpers | Latest SDK packages expose campaign helper surfaces. Treat future issues as package-specific regressions or advanced policy work. |
| Product telemetry baseline | Contracts, video ingestion, SDK typed helpers, and admin telemetry display are present. Do not reopen baseline ingestion issues. |
| Account lifecycle baseline | Signup, verification, password reset/change, delete/disable, evaluation quota, audit, and metrics are present. Social login remains deferred and outside this batch. |
| PKI/mTLS server-side | Existing `rtk_video_cloud#262` remains the owner issue. Do not duplicate. |
| Smart-home/Matter/voice assistants | Valid roadmap area, but intentionally excluded from this foundation batch. |

## Issue Ordering

1. Commit and push this workspace roadmap plus submodule snapshot.
2. Open account-manager deployment and evidence issues first; other private-cloud
   evidence depends on those service-local outputs.
3. Open admin dashboard production/upstream issues next; admin is the operator
   surface that consumes account/video facts.
4. Open frontend deployment docs issue after private-cloud wording has a pushed
   workspace source.
5. Open workspace evidence wrapper and broker packaging issues after service
   owners have references to cite.
6. Open SDK release validation issues independently; they are not blockers for
   private-cloud packaging.

## Non-Goals

- Do not modify service implementation from the workspace repo.
- Do not make the workspace the source of truth for service-local deploy details.
- Do not advertise one-click private cloud until service-local runbooks, evidence,
  backups, and support boundaries are implemented.
- Do not open broad roadmap issues for smart-home, Matter, or voice assistants in
  this batch.
