# Realtek Connect+ Core Platform Gap Roadmap

Status: supporting-note.

Owner: `rtk_cloud_workspace`.

Last reviewed: 2026-06-06.

## Purpose

This roadmap records the next foundation gaps after the latest code refresh. It
is a planning document for GitHub issues and should not replace service-owned
specifications or cross-repo contracts.

The current priority is not to reopen already-landed OTA, telemetry, account
lifecycle, admin UI, PKI/mTLS, TURN registry, WebRTC-only backend, or
admin-production-proxy work. The remaining priority is to keep release evidence
fresh and to make live environment skips explicit when staging, hardware, or
artifact credentials are unavailable.

## Source Of Truth

| Topic | Source of truth | Supporting notes |
| --- | --- | --- |
| Cross-repo contracts | `repos/rtk_cloud_contracts_doc` | This roadmap only maps owner issues. |
| Private-cloud BOM | `docs/private-cloud-deployment.md` | Product-level deployment package, runbook, and follow-up routing. |
| Product-level evidence | `docs/product-level-evidence.md` | Workspace wrapper command, redaction rules, and artifact layout. |
| Cross-service broker packaging | `docs/cross-service-broker-packaging.md` | Broker ownership decision, profiles, streams, retention, and evidence expectations. |
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
| Account-manager private-cloud deployability | Done | `hkt999rtk/rtk_account_manager` | Deployment packaging and operations runbook. |
| Account-manager readiness evidence | Done | `hkt999rtk/rtk_account_manager` | Read-only smoke/evidence script for auth, org, device, and provisioning facts. |
| Admin dashboard production profile | Done | `hkt999rtk/rtk_cloud_admin` | Production deployment profile for Go/React dashboard, persistence, upstreams, and rollback. |
| Admin upstream readiness/telemetry mode | Done | `hkt999rtk/rtk_cloud_admin` | Production-mode use of authoritative account/video readiness, telemetry, stream, and firmware facts with stable unavailable states when upstreams are absent. |
| Brand-cloud backend management | Done | `hkt999rtk/rtk_account_manager`, `hkt999rtk/rtk_cloud_admin` | Account Manager owns `organization_kind=brand_cloud`, platform-admin APIs, bootstrap root, and audit; Admin provides backend/BFF proxy routes for future WebUI. |
| Frontend private-cloud deployment docs | Evidence follow-up | `hkt999rtk/rtk_cloud_frontend` | Production deployment profile exists; backup/restore and legal/contact polishing remain launch evidence, not backend foundation work. |
| Product-level evidence collector | Done | `hkt999rtk/rtk_cloud_workspace` | `go run ./scripts/go/rtk-cloud -- collect-evidence` gathers service evidence into a redacted bundle. |
| Cross-service broker packaging | Retired | `hkt999rtk/rtk_cloud_workspace` | `docs/cross-service-broker-packaging.md` records that shared broker packaging is not part of the current runtime; future async coordination should use explicit APIs plus DB-backed outbox/retry unless a real multi-consumer event bus requirement appears. |
| SDK release coverage artifacts | Done | `hkt999rtk/rtk_cloud_client` | Android/iOS/native coverage exports and release validation artifacts. |
| Pro2/FreeRTOS live-lab validation | Done | `hkt999rtk/rtk_cloud_client` | Live hardware release test program with clean skip/block reporting. |
| Video Cloud local backend reports | Done | `hkt999rtk/rtk_video_cloud` | `docs/TEST_REPORT.md`, `docs/READINESS_TEST_REPORT.md`, and `docs/RELEASE_TEST_REPORT.md` record local backend validation and explicit live-environment skips. |

## Completed Or No-Reopen Baseline

| Area | Current decision |
| --- | --- |
| OTA campaign backend | Latest video cloud has campaign resource/persistence, policy gates, cancel/archive, pause/resume, group targeting, and analytics foundation. Do not reopen old campaign foundation issues. |
| OTA SDK helpers | Latest SDK packages expose campaign helper surfaces. Treat future issues as package-specific regressions or advanced policy work. |
| Product telemetry baseline | Contracts, video ingestion, SDK typed helpers, and admin telemetry display are present. Do not reopen baseline ingestion issues. |
| Account lifecycle baseline | Signup, verification, password reset/change, delete/disable, evaluation quota, audit, and metrics are present. Social login remains deferred and outside this batch. |
| Brand-cloud backend management | Account Manager has brand-cloud schema/API/audit/bootstrap; Admin has BFF proxy routes. WebUI remains deferred. |
| PKI/mTLS server-side | Server-side mTLS, revocation, renewal route, and contracts are present. Do not duplicate old PKI issue work. |
| TURN registry | Video cloud TURN registry runtime and contracts are present for multi-node coturn discovery. |
| WebRTC-only streaming migration | Active backend/product paths are WebRTC-only. Legacy RTSP/relay terms remain only in migration, historical, or negative-test context; WebRTC/TURN remains the product video path. |
| Account-manager private-cloud evidence | Deployment runbook and readiness smoke are present; workspace wrapper can aggregate service-local output. |
| SDK release validation tooling | Coverage export docs and Pro2 live-lab wrapper/report templates are present; live runs still depend on release-candidate environment and hardware. |
| Product-level evidence collector | Workspace wrapper is implemented; service-local reports must distinguish local evidence from live staging, hardware, and artifact-packaging skips. |
| Cross-service broker packaging | Retired for the current runtime; no broker deployment evidence is required unless a future event-bus design is approved. |
| Smart-home/Matter/voice assistants | Valid roadmap area, but intentionally excluded from this foundation batch. |

## Issue Ordering

1. Keep this roadmap and submodule snapshot current after each foundation merge.
2. Do not reopen completed account-manager deployment/evidence, workspace
   evidence/broker, SDK release validation, PKI/mTLS, or TURN registry issues.
3. Keep admin production-mode upstream readiness/telemetry behavior in the
   no-reopen baseline unless a regression is found in route tests or live
   evidence.
4. Prioritize frontend production backup/restore and launch-polish docs only
   where website operation differs from the existing private-cloud BOM.
5. Revisit smart-home/Matter/voice-assistant roadmap only after the remaining
   deployability and evidence items are closed.

## Non-Goals

- Do not modify service implementation from the workspace repo.
- Do not make the workspace the source of truth for service-local deploy details.
- Do not advertise one-click private cloud until service-local runbooks, evidence,
  backups, and support boundaries are implemented.
- Do not open broad roadmap issues for smart-home, Matter, or voice assistants in
  this batch.
