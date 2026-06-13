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
| LKE migration inventory and gates | `docs/lke-migration-inventory.md` | Source-of-truth current architecture review, service inventory, LKE target summary, and implementation gates. |
| Cross-service broker packaging | `docs/cross-service-broker-packaging.md` | Records that shared broker packaging is retired for the current runtime. |
| Video cloud runtime deploy | `repos/rtk_video_cloud/docs/automation.md` | Release, deploy, staging evidence, and runner model. |
| Video cloud release bundle | `repos/rtk_video_cloud/docs/release.md` | Release artifact contents and intended handoff shape. |
| Video cloud host setup | `repos/rtk_video_cloud/docs/deployment-instance-setup.md` | Linux host bootstrap, PostgreSQL, systemd, EMQX, runner setup. |
| Video cloud promotion/rollback | `repos/rtk_video_cloud/docs/deployment-promotion-rollback.md` | Staging, PM sign-off, production deploy, rollback. |
| Video cloud observability | `repos/rtk_video_cloud/docs/observability-baseline.md` | Metrics, logs, EMQX, dead-letter, and evidence signals. |
| Cross-service service logging | `docs/service-logging-architecture.md` | Central logger, journald forwarder, correlation, retention, and provisioning dependency plan. |
| Video cloud config | `repos/rtk_video_cloud/docs/config-map.md` | Runtime env map including Postgres, blob, MQTT, TURN, and certissuer settings. |
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
runtime details. For production-like deployments, the target deployment model is
Linode Kubernetes Engine (LKE). Existing Linode VM and systemd instructions are
retained as migration reference and rollback context, not as the primary future
production path.

## Required Components

| Component | Required | Owner repo | Current source/status | Private-cloud role |
| --- | --- | --- | --- | --- |
| Public website / lead portal | Yes for external-facing deployments | `rtk_cloud_frontend` | Container recipe and SQLite lead storage exist. | Presents Realtek Connect+ pages, docs portal, contact leads, admin lead review. |
| Admin dashboard | Yes for operator deployments | `rtk_cloud_admin` | Go BFF, React SPA, SQLite demo/cache storage, and dashboard docs exist. | Provides tenant/customer and platform operations views for fleet, provisioning, lifecycle, service health, and audit workflows. |
| Account manager API | Yes | `rtk_account_manager` | Go/Postgres backend with auth, orgs, devices, groups/tags, provisioning projection. | Owns users, orgs, RBAC, registry devices, fleet primitives, account-side readiness facts. |
| Video cloud API | Yes for camera/device runtime | `rtk_video_cloud` | Linux release bundle, systemd units, Postgres, media, firmware, transport, metrics. | Owns activation, tokens, media, firmware lifecycle, WebSocket/MQTT transport, runtime signals. |
| Video cloud workers | Deployment-dependent | `rtk_video_cloud` | `cleaner`, `statistics`, TURN registry, certissuer/factory enrollment, and WebRTC/TURN units exist. Legacy relay and RTSP relay are WebRTC-only migration removal targets. | Runs background cleanup, metrics/statistics, WebRTC/TURN, and certificate/factory workers. |
| EMQX MQTT broker | Required when MQTT transport is enabled | `rtk_video_cloud` packaging / EMQX upstream | Packaged Docker Compose and `video_cloud-emqx.service` exist. | Self-hosted reference MQTT broker for device transport. |
| PostgreSQL | Yes | platform/operator | Required by account manager and video cloud. | Persistent account, registry, runtime, media metadata, outbox/inbox, and projections. |
| Object/blob storage | Yes for media/snapshots | platform/operator with `rtk_video_cloud` config | Local blob root and S3-style settings documented. | Stores snapshot/clip/firmware objects depending on runtime configuration. |
| Reverse proxy / TLS | Yes for production-like profile | platform/operator | Frontend and video cloud assume TLS can terminate outside app processes. | DNS, TLS certificates, routing, compression, request size limits, security headers. |
| Secrets manager | Yes | platform/operator | GitHub Environment secrets or host-side manager are currently documented patterns. | Stores DSNs, auth secrets, MQTT credentials, webhook secrets, deploy keys, private keys. |
| Observability stack | Yes for production-like profile | platform/operator | Video cloud exposes Prometheus endpoints and evidence collectors. | Scrapes metrics, collects logs, stores alerts, keeps readiness evidence. |
| Central service logger | Yes for production-like profile | `rtk_cloud_logger` with workspace provisioning | Implemented in staging native flow: logger resource provisioning, Loki-backed backend service, journald forwarders, readiness checks, artifacts, cleanup, and Cloud Admin dashboard wiring. Container/file sources such as EMQX broker per-publish logs still need a source adapter. | Stores queryable service logs across account, video, admin, frontend, and systemd-managed host sources without requiring Grafana for the v1 dashboard. |
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
- documented skipped services are explicit, for example no shared broker because
  lifecycle coordination uses explicit service APIs

### Production-Like LKE Profile

Use this profile for customer pilots, private commercial deployments, and any
environment where operations, rollback, and support commitments matter.

Required infrastructure:

- LKE cluster with documented region, node pools, upgrade policy, and ownership
- Linode NodeBalancer plus Kubernetes Ingress or Gateway API for public HTTP(S)
- cert-manager for TLS automation; existing DNS-01 behavior must be represented
  as issuer configuration
- PostgreSQL deployment choice documented before cutover: external/VM bridge,
  in-cluster operator, in-cluster StatefulSet, or managed/external service
- Linode Object Storage or approved object storage for artifacts, media, and
  backups
- EMQX MQTT deployment or external broker path when MQTT transport is enabled;
  MQTT/MQTTS must not be treated as normal HTTP-only ingress
- OpenBao plus Kubernetes auth, External Secrets, or reviewed secret injection
  path for runtime secrets
- metrics, logs, alerts, probes, backup, restore, and rollback evidence
- backup target independent from the primary LKE runtime storage

Recommended separation:

| Layer | Production-like expectation |
| --- | --- |
| Edge | NodeBalancer in front of Ingress/Gateway for frontend, account API, video API, and required mTLS hostnames. |
| Frontend | Deployment with persistent lead storage or migrated production database. |
| Account manager | Deployment with Service/Ingress; database migrations controlled by release. |
| Video cloud API/workers | Deployments for API, certissuer/factory enrollment, and long-running workers; Jobs/CronJobs only for explicitly one-shot or scheduled flows. |
| MQTT | EMQX operator/StatefulSet or external broker with explicit TCP exposure, auth/TLS policy, logs, and health checks. |
| TURN | LKE staging can run coturn as a Kubernetes Deployment/Service; production public TURN still requires explicit Linode LoadBalancer/NodeBalancer UDP/TCP exposure, scaling, TLS, and rollback approval. |
| Storage | PostgreSQL restore-tested before migration; object storage lifecycle/replication according to customer policy. |
| Observability | Prometheus-compatible metrics, Loki/logger service logs, broker logs, dead-letter evidence, alert routing. |

Production-like acceptance bar:

- all selected services have health or metrics endpoints monitored
- deployment uses immutable release artifacts or pinned container images
- upgrade and rollback are rehearsed before customer traffic
- database and object storage backups are restorable in a test environment
- secrets are not stored in git or public workflow logs
- EMQX operations are included in runbooks when enabled
- frontend private-cloud wording matches the actually deployed package, not a
  roadmap superset
- the migration inventory and gates in `docs/lke-migration-inventory.md` are
  complete and human-approved before production implementation
- production Kubernetes YAML, Helm charts, Kustomize overlays, CI/CD deployment
  pipelines, DNS changes, secret changes, and data movement remain blocked until
  those gates are approved

### Legacy Linode VM Migration Reference

The current VM/systemd deployment remains useful for staging, rollback, and
architecture discovery while the LKE migration is being designed. It must not be
treated as the final production target after the LKE gates are approved.

Legacy VM reference shape:

| Layer | Current VM reference |
| --- | --- |
| Video Cloud | Five Linode roles: `edge`, `api`, `infra`, `mqtt`, `coturn`; see `repos/rtk_video_cloud/linode_deploy/docs/ARCHITECTURE.md`. |
| Account Manager | Dedicated public VM with nginx TLS and local PostgreSQL; see `repos/rtk_account_manager/linode_deploy/docs/RUNBOOK.md`. |
| Cloud Admin | Dedicated public+VPC VM with nginx TLS and SQLite persistence; see `repos/rtk_cloud_admin/docs/private-cloud-deployment.md`. |
| Logging | Loki-backed backend and VM journald forwarders; see `docs/service-logging-architecture.md`. |
| Secrets | Operator-local `.secrets/` and protected GitHub Environment secrets; see `docs/deployment-secrets-governance.md`. |

## Deployment Orchestration Order

Cross-service deployments must follow a fixed order. Service-local runbooks
describe how to deploy each component; this workspace section defines when a
component is allowed to be deployed or promoted relative to the rest of the
stack. Do not deploy the Admin dashboard as the first component in a fresh
environment, because Admin is an aggregator and depends on both Account Manager
and Video Cloud upstreams.

### Dependency Graph

```text
platform prerequisites
  -> Video Cloud runtime
  -> Account Manager API
  -> Admin dashboard
  -> Public frontend / promotion site

Video Cloud runtime ----\
                       +--> Admin dashboard service health and operations
Account Manager API ---/
```

`rtk_cloud_admin` must use service DNS names or public HTTPS upstream domains,
not raw VM IPs or private app ports. During VM-to-LKE migration, the current
Linode staging profile still uses these public HTTPS upstreams:

```env
ACCOUNT_MANAGER_BASE_URL=https://account-manager.video-cloud-staging.realtekconnect.com
VIDEO_CLOUD_BASE_URL=https://video-cloud-staging.realtekconnect.com
```

### Ordered Gates

| Order | Component | Owner repo | Gate before next step | LKE target / current bridge |
| --- | --- | --- | --- | --- |
| 0 | Platform prerequisites | platform/operator | LKE cluster, node pools, namespaces, RBAC, NetworkPolicy, DNS, cert-manager issuer, OpenBao/secret injection, storage classes, and backup target are documented and approved. | Current VM bridge still requires Linode token, DNS credentials, SSH key, operator CIDR, and service secrets. |
| 1 | Video Cloud runtime | `rtk_video_cloud` | Public API health/version pass; PostgreSQL, MQTT broker, coturn/TURN, certissuer/factory path, Prometheus scrape path, and selected workers are healthy for the chosen profile. | Runtime-generated LKE staging resources now cover API, certissuer, factory enrollment, workers, MQTT broker, coturn, ephemeral PostgreSQL, and Prometheus; production Ingress, persistent database/storage, MQTT/TURN public exposure, and OpenBao remain gated. |
| 2 | Account Manager API | `rtk_account_manager` | `GET /v1/health` passes; auth/register/login/`/v1/me` smoke passes; database migration and public TLS route are valid. | Target is Deployment/Service/Ingress; current bridge is `https://account-manager.video-cloud-staging.realtekconnect.com`. |
| 3 | Admin dashboard | `rtk_cloud_admin` | `/healthz` passes and `/api/service-health` reports Account Manager, Video Cloud, and local persistence as `ok`. | Target is Deployment with PVC or approved database migration; current bridge is `https://admin.video-cloud-staging.realtekconnect.com`. |
| 4 | Public frontend / promotion site | `rtk_cloud_frontend` | Website content matches deployed capability status; API links and contact/lead persistence are verified for the selected profile. | Public-facing Realtek Connect+ website; deployment profile remains service-owned. |
| 5 | Product-level evidence | `rtk_cloud_workspace` | Workspace evidence records exact submodule commits, deployed versions, health results, skipped checks, and residual blockers. | `docs/product-level-evidence.md` defines the wrapper contract. |

### Fresh Environment Sequence

1. Prepare platform prerequisites and create required DNS records.
2. Deploy Video Cloud first, because it owns runtime media, transport, firmware,
   TURN/WebRTC, MQTT/EMQX, and video-side readiness facts consumed by Admin.
3. Deploy Account Manager next, because it owns users, organizations, registry
   devices, membership, authentication, and provisioning/account-side readiness
   facts consumed by Admin.
4. Deploy Admin only after both upstreams have passed smoke checks. Admin is a
   dashboard/BFF; it should not be used as evidence that upstream services are
   deployed until `/api/service-health` reports all selected upstreams as `ok`.
5. Deploy the public frontend after backend/admin status is known, so public copy
   and links do not claim capabilities that are not live in the target profile.
6. Collect product-level evidence from the workspace after all selected services
   have passed their service-owned smoke checks.

### Upgrade Sequence

For routine upgrades, deploy lower-level dependencies before aggregators:

1. Upgrade Video Cloud if its API, contracts, runtime paths, or readiness facts
   changed.
2. Upgrade Account Manager if auth/org/device/provisioning/account readiness
   behavior changed.
3. Upgrade Admin after its upstream API expectations are satisfied.
4. Upgrade frontend copy last if user-visible wording or links depend on the new
   backend/admin behavior.
5. Re-run workspace evidence after all service-local verifications pass.

If only Admin UI code changes and upstream contracts are unchanged, Admin may be
redeployed independently, but `/api/service-health` must still be checked after
deploy. If only Account Manager or Video Cloud changes, Admin does not need to
be redeployed unless it has hard-coded endpoint assumptions, DTO expectations,
or cached compatibility behavior affected by that change.

### Rollback Sequence

Rollback aggregators before rolling back their upstreams when an integration
change breaks Admin:

1. If Admin breaks after an upstream upgrade, first rollback or redeploy Admin to
   the previous compatible version if the upstream remains healthy.
2. If the upstream itself is unhealthy, rollback the owning upstream service
   according to its service-local rollback runbook.
3. Re-check Admin `/api/service-health` after any upstream rollback.
4. Update workspace evidence with the failed version, rollback version, and any
   residual compatibility issue.

## Network And TLS Boundary

Production deployments should not expose raw service ports directly to the
public internet unless that is an explicit product decision.

Recommended defaults:

| Surface | Exposure guidance |
| --- | --- |
| Frontend website | Public HTTPS through LKE Ingress/Gateway behind Linode NodeBalancer. |
| Account manager API | HTTPS through Ingress/Gateway; scope CORS and auth policy deliberately. |
| Video cloud API | HTTPS through Ingress/Gateway; route only required external APIs. |
| mTLS device / certissuer hostnames | Separate hostname or Gateway listener with the same CA separation currently enforced by nginx SNI. |
| WebSocket device transport | HTTPS/WSS through Ingress/Gateway only if device runtime requires external owner transport. |
| MQTT | Prefer MQTTS, auth, and explicit NetworkPolicy/firewall rules; expose only broker listeners required by devices through LoadBalancer/NodeBalancer or TCP-capable ingress. |
| TURN | Keep UDP/TCP relay exposure explicit; do not assume HTTP ingress can carry TURN traffic. |
| Prometheus metrics | Private ServiceMonitor/PodMonitor or authenticated scrape path only. |
| EMQX dashboard | Private/admin network only. |
| PostgreSQL | Private Service or external private endpoint only. |

TLS ownership belongs to the platform/operator layer. The frontend explicitly
assumes production TLS termination is handled by Ingress/Gateway or the selected
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
- OpenBao with Kubernetes auth or reviewed External Secrets/injection for LKE
  runtime secrets
- Kubernetes Secrets only for synchronized/runtime material, never for values
  committed to Git
- host-side secret manager or root-owned env files only for the legacy VM bridge
- production secret manager when required by the customer environment

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

Restore drills should run before a production-like deployment is called
customer-ready.

## Observability And Evidence

The production-like profile should collect:

- Kubernetes readiness/liveness status and service health checks for selected
  workloads
- Prometheus snapshots from video cloud services
- central Loki-backed service logger health, Kubernetes log collector status,
  and one sample Loki trace/query result
- frontend health and lead persistence checks
- account manager auth/org/device smoke output
- EMQX broker status and MQTT publish-subscribe smoke when MQTT is enabled
- PostgreSQL backup job status
- object storage availability and lifecycle policy evidence
- release version manifest and source commits for each service

Video cloud already has repo-owned readiness evidence collection. Workspace private-cloud
readiness uses `go run ./scripts/go/rtk-cloud -- collect-evidence`; the evidence
contract is documented in `docs/product-level-evidence.md`. Account manager, admin dashboard,
and frontend still own their service-local smoke/evidence commands; the
workspace wrapper records them as `SKIP` until configured or implemented.

The current `scripts/run-staging-e2e.sh` remains the workspace acceptance
reference. It is provider-aware for `linode` and `lke`; LKE execution requires
an approved kubeconfig context plus explicit container image env vars for the
services being deployed. A future in-cluster LKE smoke Job still requires the
gates in `docs/lke-migration-inventory.md`.

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
  deploy runbooks, account manager deploy runbook/readiness smoke, EMQX
  reference broker, TURN registry runtime, workspace evidence wrapper, and
  retired cross-service broker packaging note
- integration-ready: combined private-cloud BOM, single-node evaluation profile,
  production-like checklist
- roadmap or customer-specific: HA topology, managed upgrades across all
  services and production backup automation

Update public copy only when the deployed package and support boundary are
clear. Do not imply all components are one-click deployable today.

## Repo-Specific Follow-Up Routing

| Follow-up | Repository | Reason |
| --- | --- | --- |
| Add account-manager deployment packaging/runbook | `hkt999rtk/rtk_account_manager` | Implemented by the account-manager private-cloud deployment runbook. |
| Add account-manager readiness evidence script | `hkt999rtk/rtk_account_manager` | Implemented by the account-manager readiness smoke command and documented evidence flow. |
| Add frontend production deployment profile | `hkt999rtk/rtk_cloud_frontend` | Current container recipe is enough for evaluation; production-like profile needs backup/restore, reverse-proxy, and operational notes. |
| Add admin-dashboard production deployment profile | `hkt999rtk/rtk_cloud_admin` | Implemented by the admin dashboard private-cloud deployment profile; upstream authoritative production behavior remains separate below. |
| Add admin-dashboard authoritative readiness/telemetry production mode | `hkt999rtk/rtk_cloud_admin` | Production dashboard views should prefer Account Manager and Video Cloud source facts over demo/cache projections, with stable stale/partial/upstream-failure states. |
| Define product-level evidence wrapper | `hkt999rtk/rtk_cloud_workspace` | Implemented by `go run ./scripts/go/rtk-cloud -- collect-evidence` and documented in `docs/product-level-evidence.md`; service-local collectors remain owner-repo follow-ups. |
| Retire cross-service broker packaging decision | `hkt999rtk/rtk_cloud_workspace` | `docs/cross-service-broker-packaging.md` records that shared broker packaging is retired; future async coordination should use explicit APIs plus DB-backed outbox/retry unless a real multi-consumer event bus requirement appears. |
| Add private-cloud copy status update | `hkt999rtk/rtk_cloud_frontend` | Public wording should reflect this BOM and avoid one-click private-cloud claims until follow-ups land. |
| Add SDK release validation coverage and live-lab evidence | `hkt999rtk/rtk_cloud_client` | Private-cloud/customer handoff needs package release evidence for Android/iOS/native coverage exports and Pro2/FreeRTOS live-lab validation artifacts. |

## Issue #1 Acceptance Checklist

- Required components are defined above.
- Single-node evaluation and production-like deployment profiles are defined.
- Upgrade, rollback, backup, restore, and support boundaries are documented.
- Repo-specific follow-up work is routed above.
- Frontend wording should be updated only after package status and support
  boundaries are clear.
