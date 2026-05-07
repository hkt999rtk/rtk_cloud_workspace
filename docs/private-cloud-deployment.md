# Realtek Connect+ Private Cloud Deployment BOM And Runbook

Status: supporting-note.

Owner: `rtk_cloud_workspace`.

Last reviewed: 2026-05-01.

## Purpose

This document turns the Realtek Connect+ private-cloud positioning into a
concrete deployment bill of materials and operations runbook. It is a
workspace-level coordination document. It does not replace service-owned deploy
specifications, systemd units, environment examples, or release procedures.

Use this document to scope a private-cloud evaluation or production-like
customer deployment before opening service-specific work.

## Source Documents

| Area | Source document | Notes |
| --- | --- | --- |
| Business model and tier definitions | `docs/business-model.md` | Evaluation/commercial tier structure, pricing framing, SDK licensing, and website disclosure rules. |
| Workspace gap evidence | `docs/realtek-connect-plus-gap-analysis.md` | Tracks public-copy versus implementation gaps. |
| Core platform roadmap | `docs/core-platform-gap-roadmap.md` | Routes private-cloud work to owner repositories. |
| Product-level evidence wrapper | `docs/product-level-evidence.md` | Defines the workspace evidence artifact, redaction rules, and wrapper command. |
| Cross-service broker packaging | `docs/cross-service-broker-packaging.md` | Defines NATS JetStream ownership, profiles, streams, retention, and evidence. |
| Video cloud runtime deploy | `repos/rtk_video_cloud/docs/automation.md` | Release, deploy, staging evidence, and runner model. |
| Video cloud release bundle | `repos/rtk_video_cloud/docs/release.md` | Release artifact contents and intended handoff shape. |
| Video cloud host setup | `repos/rtk_video_cloud/docs/deployment-instance-setup.md` | Linux host bootstrap, PostgreSQL, systemd, EMQX, runner setup. |
| Video cloud promotion/rollback | `repos/rtk_video_cloud/docs/deployment-promotion-rollback.md` | Staging, PM sign-off, production deploy, rollback. |
| Video cloud observability | `repos/rtk_video_cloud/docs/observability-baseline.md` | Metrics, logs, EMQX, dead-letter, and evidence signals. |
| Video cloud config | `repos/rtk_video_cloud/docs/config-map.md` | Runtime env map including Postgres, blob, MQTT, and cross-service settings. |
| Video cloud deploy assets | `repos/rtk_video_cloud/deploy/README.md` | Systemd units, EMQX compose, verifier, evidence collector. |
| Account manager behavior | `repos/rtk_account_manager/docs/SPEC.md` | Account, org, auth, registry, fleet, and provisioning scope. |
| Frontend deployment | `repos/rtk_cloud_frontend/README.md` | Container packaging, SQLite persistence, reverse-proxy TLS assumption. |
| Frontend product copy | `repos/rtk_cloud_frontend/docs/SPEC.md` | Private cloud website wording and availability boundaries. |

## Deployment Product Boundary

Private cloud means a customer or Realtek-operated private environment can run a
bounded Realtek Connect+ stack with explicit ownership for infrastructure,
service operation, data, upgrades, and support.

It does not mean the workspace repo owns every service deploy script. The
workspace defines the product-level package; each service repo owns its local
runtime details.

## Required Components

| Component | Required | Owner repo | Current source/status | Private-cloud role |
| --- | --- | --- | --- | --- |
| Public website / lead portal | Yes for external-facing deployments | `rtk_cloud_frontend` | Container recipe and SQLite lead storage exist. | Presents Realtek Connect+ pages, docs portal, contact leads, admin lead review. |
| Admin dashboard | Yes for operator deployments | `rtk_cloud_admin` | Go BFF, React SPA, SQLite demo/cache storage, and dashboard docs exist. | Provides tenant/customer and platform operations views for fleet, provisioning, lifecycle, service health, and audit workflows. |
| Account manager API | Yes | `rtk_account_manager` | Go/Postgres backend with auth, orgs, devices, groups/tags, provisioning projection. | Owns users, orgs, RBAC, registry devices, fleet primitives, account-side readiness facts. |
| Video cloud API | Yes for camera/device runtime | `rtk_video_cloud` | Linux release bundle, systemd units, Postgres, media, firmware, transport, metrics. | Owns activation, tokens, media, firmware lifecycle, WebSocket/MQTT transport, runtime signals. |
| Video cloud workers | Deployment-dependent | `rtk_video_cloud` | `ping`, `cleaner`, `statistics`, relay, RTSP relay, cross-service units exist. | Runs background lifecycle, cleanup, metrics/statistics, relay, cross-service workers. |
| EMQX MQTT broker | Required when MQTT transport is enabled | `rtk_video_cloud` packaging / EMQX upstream | Packaged Docker Compose and `video_cloud-emqx.service` exist. | Self-hosted reference MQTT broker for device transport. |
| PostgreSQL | Yes | platform/operator | Required by account manager and video cloud. | Persistent account, registry, runtime, media metadata, outbox/inbox, and projections. |
| Object/blob storage | Yes for media/snapshots | platform/operator with `rtk_video_cloud` config | Local blob root and S3-style settings documented. | Stores snapshot/clip/firmware objects depending on runtime configuration. |
| Reverse proxy / TLS | Yes for production-like profile | platform/operator | Frontend and video cloud assume TLS can terminate outside app processes. | DNS, TLS certificates, routing, compression, request size limits, security headers. |
| Secrets manager | Yes | platform/operator | GitHub Environment secrets or host-side manager are currently documented patterns. | Stores DSNs, auth secrets, MQTT credentials, webhook secrets, deploy keys, private keys. |
| Observability stack | Yes for production-like profile | platform/operator | Video cloud exposes Prometheus endpoints and evidence collectors. | Scrapes metrics, collects logs, stores alerts, keeps readiness evidence. |
| Cross-service broker | Required when account/video lifecycle channel is enabled | platform/operator, with workspace product requirements | `docs/cross-service-broker-packaging.md` selects NATS JetStream as the default and defines acceptable equivalents. | Carries account-to-video lifecycle commands and video-to-account events. |
| Backup storage | Yes for production-like profile | platform/operator | Not packaged as a single workspace script. | Stores database dumps, object storage backups, env/secrets escrow metadata, release manifests. |

## Deployment Profiles

### Single-Node Evaluation Profile

Use this profile for demos, engineering evaluation, and internal customer
workshops. It optimizes for fast bring-up over high availability.

Required host:

- one Linux host with `systemd`
- Docker with Compose plugin for EMQX
- local PostgreSQL
- local filesystem or S3-compatible object store
- reverse proxy optional but recommended when exposing outside localhost

Recommended component placement:

| Component | Placement |
| --- | --- |
| `rtk_cloud_frontend` | Container on the same host, HTTP on `8080`, SQLite volume under `/data`. |
| `rtk_cloud_admin` | Go server on the same host, HTTP on a separate internal port, SQLite demo/cache volume. |
| `rtk_account_manager` | Native service or container on the same host, Postgres local DSN. |
| `rtk_video_cloud` | Release bundle installed under `/opt/video_cloud`, systemd units. |
| EMQX | `video_cloud-emqx.service` using packaged Docker Compose, MQTT on `127.0.0.1:1883` unless exposed deliberately. |
| PostgreSQL | Local cluster with separate databases/users for account manager and video cloud. |
| Object storage | Local blob root for evaluation; S3-compatible bucket if media retention matters. |
| Reverse proxy/TLS | Optional local Caddy/Nginx/ingress; required if external users access the host. |
| Observability | Local `/metrics/prometheus` snapshots plus journald; Prometheus optional. |

Evaluation acceptance bar:

- frontend health and admin lead persistence survive restart
- account manager auth/org/device smoke passes
- video cloud `verify.sh` or smoke parity wrapper passes for selected services
- EMQX publish-subscribe smoke passes when MQTT is enabled
- readiness evidence collector produces an artifact without secrets
- documented skipped services are explicit, for example no cross-service channel
  if NATS JetStream is not deployed

### Production-Like Private Profile

Use this profile for customer pilots, private commercial deployments, and any
environment where operations, rollback, and support commitments matter.

Required infrastructure:

- Linux hosts or Kubernetes nodes with explicit ownership and patching policy
- managed or self-managed PostgreSQL with backup/restore procedures
- object storage with retention and lifecycle policy
- EMQX broker deployment when MQTT transport is enabled
- NATS JetStream or equivalent broker when account/video cross-service channel
  is enabled
- reverse proxy/load balancer with TLS, DNS, request limits, and access logs
- secrets manager or GitHub Environment secrets with rotation process
- metrics/logging/alerting stack
- backup target independent from the primary runtime host

Recommended separation:

| Layer | Production-like expectation |
| --- | --- |
| Edge | Reverse proxy/TLS in front of frontend, account API, video API, relay surfaces as needed. |
| Frontend | Containerized service with persistent lead DB or migrated production storage. |
| Account manager | Dedicated service and database/schema; migrations controlled by release. |
| Video cloud API/workers | Release bundle or equivalent artifact; systemd/Kubernetes supervision; selected units only. |
| MQTT | EMQX as managed/self-hosted broker with auth/TLS policy, logs, and health checks. |
| Cross-service broker | NATS JetStream or approved equivalent with retention, replicas, and dead-letter handling. |
| Storage | Postgres backups plus object storage lifecycle/replication according to customer policy. |
| Observability | Prometheus-compatible metrics, service logs, broker logs, dead-letter evidence, alert routing. |

Production-like acceptance bar:

- all selected services have health or metrics endpoints monitored
- deployment uses immutable release artifacts or pinned container images
- upgrade and rollback are rehearsed before customer traffic
- database and object storage backups are restorable in a test environment
- secrets are not stored in git or public workflow logs
- EMQX and cross-service broker operations are included in runbooks when enabled
- frontend private-cloud wording matches the actually deployed package, not a
  roadmap superset

## Network And TLS Boundary

Production deployments should not expose raw service ports directly to the
public internet unless that is an explicit product decision.

Recommended defaults:

| Surface | Exposure guidance |
| --- | --- |
| Frontend website | Public HTTPS through reverse proxy/CDN/ingress. |
| Account manager API | HTTPS through reverse proxy; scope CORS and auth policy deliberately. |
| Video cloud API | HTTPS through reverse proxy; route only required external APIs. |
| WebSocket device transport | HTTPS/WSS through reverse proxy only if device runtime requires external owner transport. |
| MQTT | Prefer TLS, auth, and explicit firewall rules; expose only broker listener required by devices. |
| Prometheus metrics | Private network or authenticated scrape path only. |
| EMQX dashboard | Private/admin network only. |
| PostgreSQL | Private network only. |
| NATS JetStream | Private network only. |

TLS ownership belongs to the platform/operator layer. The frontend explicitly
assumes production TLS termination is handled by a reverse proxy, ingress, or
deployment platform.

## Secrets And Configuration Boundary

Do not put deploy secrets in workspace docs, service docs, or issue bodies.

Required secret categories:

- database DSNs and passwords
- JWT/auth signing secrets
- OAuth or social-login secrets when implemented
- MQTT usernames/passwords and TLS private keys
- webhook, Device Hub, Event Hub, or cloud-provider credentials
- object storage access keys
- GitHub deploy keys and environment secrets
- private clip/certificate assets

Current accepted storage patterns:

- GitHub Environment secrets for GitHub Actions deploy workflows
- host-side secret manager or root-owned env files for long-lived self-hosted
  deployments
- production secret manager when available from the customer environment

## Upgrade Runbook

Use immutable versions. Do not rebuild on the target host during normal upgrade.

1. Confirm source commit and release artifact or container image tag.
2. Confirm CI and packaging checks passed for that exact artifact.
3. Confirm database migration notes and compatibility boundaries.
4. Deploy to staging or evaluation profile first.
5. Run service smoke checks and collect readiness evidence.
6. Review metrics, logs, dead letters, and broker health.
7. Record current version and rollback target.
8. Promote to production-like profile with required approvals.
9. Monitor baseline signals for the agreed observation window.

Video cloud already has a release-bundle deployment model and promotion runbook.
Other services need equivalent release/rollback documentation before they can be
claimed as private-cloud production-ready.

## Rollback Runbook

Rollback must use a previous known-good artifact, not a source rebuild.

1. Identify the previous known-good version before upgrade.
2. Confirm the previous artifact still exists and passes bundle/image integrity
   checks.
3. Stop or drain affected services according to service-local guidance.
4. Deploy the previous artifact.
5. Confirm service health and version endpoints.
6. Re-run smoke checks and readiness evidence.
7. Confirm database compatibility. If a migration is not backward-compatible,
   follow the database restore procedure instead of blindly rolling back binary
   artifacts.
8. Record incident timeline, failed version, rollback version, and residual data
   risk.

Video cloud supports rollback through redeploying a previous published release
bundle. Workspace private-cloud readiness requires similar documented rollback
behavior for account manager and frontend before production commitments.

## Backup And Restore Boundary

Backups are mandatory for production-like profiles and optional but recommended
for single-node evaluation.

Minimum backup set:

| Data | Owner | Backup guidance | Restore check |
| --- | --- | --- | --- |
| Account manager PostgreSQL | platform/operator with `rtk_account_manager` schema ownership | Scheduled logical dump or managed database backup. | Restore into test DB and run auth/org/device smoke. |
| Video cloud PostgreSQL | platform/operator with `rtk_video_cloud` schema ownership | Scheduled logical dump or managed database backup. | Restore into test DB and run video-cloud smoke. |
| Object/blob storage | platform/operator | Versioned bucket, lifecycle snapshot, or filesystem backup. | Restore representative snapshot/clip/firmware objects. |
| Frontend leads SQLite | `rtk_cloud_frontend` / platform | Backup mounted `/data/connectplus.db` or migrate to production DB. | Restore and verify admin lead listing/export. |
| Runtime env and non-secret manifest | platform/operator | Store redacted env key inventory and release manifest. | Confirm required keys are known without leaking secrets. |
| Secrets | platform/operator | Secret-manager backup/escrow according to customer policy. | Rotation drill, not plaintext restore in git. |
| EMQX config | platform/operator | Back up broker config, auth policy, TLS material references. | Broker smoke after restore. |
| NATS JetStream state | platform/operator | Back up stream config/state if cross-service channel requires persistence. | Publish/consume smoke and dead-letter inspection. |

Restore drills should run before a production-like deployment is called
customer-ready.

## Observability And Evidence

The production-like profile should collect:

- service status and health checks for selected units/containers
- Prometheus snapshots from video cloud services
- frontend health and lead persistence checks
- account manager auth/org/device smoke output
- EMQX broker status and MQTT publish-subscribe smoke when MQTT is enabled
- cross-service worker metrics and dead-letter files when lifecycle channel is
  enabled
- PostgreSQL backup job status
- object storage availability and lifecycle policy evidence
- release version manifest and source commits for each service

Video cloud already has `collect-readiness-evidence.sh`. Workspace private-cloud
readiness now has `scripts/collect-private-cloud-evidence.sh` and the wrapper
contract in `docs/product-level-evidence.md`. Account manager, admin dashboard,
and frontend still own their service-local smoke/evidence commands; the
workspace wrapper records them as `SKIP` until configured or implemented.

## Support Boundaries

| Responsibility | Realtek-owned private deployment | Customer-operated private deployment |
| --- | --- | --- |
| Release artifact creation | Realtek | Realtek provides artifacts; customer may mirror them. |
| Infrastructure provisioning | Realtek or agreed hosting partner | Customer/platform team. |
| DNS/TLS/reverse proxy | Realtek/platform team | Customer/platform team. |
| Secrets storage/rotation | Realtek/platform team | Customer/platform team, with Realtek guidance. |
| Database and object backups | Realtek/platform team | Customer/platform team. |
| Runtime monitoring | Realtek/platform team | Customer/platform team with Realtek support playbooks. |
| Incident response | Realtek primary | Shared; customer owns infrastructure, Realtek owns app guidance. |
| Upgrade execution | Realtek or approved operator | Customer operator using Realtek runbooks. |

Do not promise full managed private cloud support until these boundaries are
agreed in the deployment contract or customer SOW.

## Frontend Wording Rule

The frontend private-cloud page may describe private deployment as a supported
product direction and website content area, but availability labels must follow
the actual package status:

- available foundation: website container recipe, video cloud release bundle and
  deploy runbooks, account manager service behavior, EMQX reference broker
- integration-ready: combined private-cloud BOM, single-node evaluation profile,
  production-like checklist
- roadmap or customer-specific: HA topology, managed upgrades across all
  services, unified product-level evidence collector, cross-service broker
  packaging, production backup automation

Update public copy only when the deployed package and support boundary are
clear. Do not imply all components are one-click deployable today.

## Repo-Specific Follow-Up Routing

| Follow-up | Repository | Reason |
| --- | --- | --- |
| Add account-manager deployment packaging/runbook | `hkt999rtk/rtk_account_manager` | Account manager has service/API docs, but private-cloud package needs deploy, migration, backup, and rollback instructions comparable to video cloud. |
| Add account-manager readiness evidence script | `hkt999rtk/rtk_account_manager` | Product-level evidence needs auth/org/device/provisioning smoke output. |
| Add frontend production deployment profile | `hkt999rtk/rtk_cloud_frontend` | Current container recipe is enough for evaluation; production-like profile needs backup/restore, reverse-proxy, and operational notes. |
| Add admin-dashboard production deployment profile | `hkt999rtk/rtk_cloud_admin` | Current admin dashboard is a Go/React console with local demo/cache persistence; production-like profile needs upstream integration, backup/restore, reverse-proxy, and operational notes. |
| Add admin-dashboard authoritative readiness/telemetry production mode | `hkt999rtk/rtk_cloud_admin` | Production dashboard views should prefer Account Manager and Video Cloud source facts over demo/cache projections, with stable stale/partial/upstream-failure states. |
| Define product-level evidence wrapper | `hkt999rtk/rtk_cloud_workspace` | Implemented by `scripts/collect-private-cloud-evidence.sh` and documented in `docs/product-level-evidence.md`; service-local collectors remain owner-repo follow-ups. |
| Define cross-service broker packaging decision | `hkt999rtk/rtk_cloud_workspace` | Decided in `docs/cross-service-broker-packaging.md`: workspace owns product requirements, service repos own client/runtime behavior, platform/operator owns broker installation and operations. |
| Add private-cloud copy status update | `hkt999rtk/rtk_cloud_frontend` | Public wording should reflect this BOM and avoid one-click private-cloud claims until follow-ups land. |
| Add SDK release validation coverage and live-lab evidence | `hkt999rtk/rtk_cloud_client` | Private-cloud/customer handoff needs package release evidence for Android/iOS/native coverage exports and Pro2/FreeRTOS live-lab validation artifacts. |

## Issue #1 Acceptance Checklist

- Required components are defined above.
- Single-node evaluation and production-like deployment profiles are defined.
- Upgrade, rollback, backup, restore, and support boundaries are documented.
- Repo-specific follow-up work is routed above.
- Frontend wording should be updated only after package status and support
  boundaries are clear.
