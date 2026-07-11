# LKE Deployment Adapter Inventory

Status: current K8s/LKE staging runtime with production hardening gates.

Owner: `rtk_cloud_workspace`.

Last reviewed: 2026-06-14.

## Purpose

This document is the source-of-truth inventory and gate checklist for the
reusable Linode Kubernetes Engine (LKE) deployment adapter and the remaining
production hardening work. Provider-neutral architecture and environment
contracts live in `docs/cloud-deployment-architecture.md`. The old Linode VM deployment model is retained only
as legacy migration context. This document does not define production
Kubernetes manifests, Helm charts, Dockerfiles, or CI/CD deployment pipelines.
Those implementation artifacts are blocked until the gates in this document are
reviewed and approved.

Existing service behavior should be preserved unless the Kubernetes runtime
requires a documented change. When this document cannot confirm a detail from
the repository, it marks the gap as `TODO:`.

This is the only workspace LKE runtime inventory and gate checklist. Do
not create a parallel LKE architecture document unless this file explicitly
routes a service-owned detail there.

## Architecture Review

Reviewed workspace documents and configuration:

| File | Classification | Notes |
| --- | --- | --- |
| `docs/private-cloud-deployment.md` | Source | Workspace deployment BOM, orchestration order, upgrade, rollback, backup, support boundaries. |
| `docs/linode-staging-deployment-snapshot.md` | Historical note | Snapshot of retired VM staging endpoints and placement. |
| `docs/deployment-secrets-governance.md` | Source | Deployment secret layout and handling rules. |
| `docs/service-logging-architecture.md` | Source | Central logging target, forwarder behavior, Loki/logger boundaries. |
| `repos/rtk_video_cloud/linode_deploy/docs/ARCHITECTURE.md` | Legacy source | Retired five-role Linode VM topology and network boundary. |
| `repos/rtk_video_cloud/linode_deploy/docs/RUNBOOK.md` | Legacy source | Retired Video Cloud VM provision/deploy/verify operating model. |
| `repos/rtk_video_cloud/linode_deploy/configs/*.yaml.example` | Legacy config evidence | Retired Linode VM region, VPC, role labels, ports, and deploy manifest shape. |
| `repos/rtk_video_cloud/deploy/systemd/`, `deploy/docker-compose.*.yml`, `deploy/prometheus/` | Legacy config evidence | Historical systemd, EMQX/coturn/PostgreSQL/Prometheus packaging. |
| `repos/rtk_account_manager/linode_deploy/docs/RUNBOOK.md` | Legacy source | Retired Account Manager public VM deployment, local PostgreSQL, nginx/TLS, backup. |
| `repos/rtk_cloud_admin/docs/private-cloud-deployment.md` | Legacy source | Retired Cloud Admin VM deployment, upstream dependencies, SQLite persistence, backup. |
| `scripts/run-staging-e2e.sh`, `rtk-cloud ... --environment staging`, `tests/staging-*.test.sh` | Operational evidence | Current K8s/LKE staging orchestration, validation, and E2E acceptance paths. |

Legacy Linode VM model:

- Video Cloud uses five Linode roles: `edge`, `api`, `infra`, `mqtt`, and
  `coturn`.
- Public HTTPS enters through VM-local nginx on `edge`; certbot uses DNS-01.
- Account Manager and Cloud Admin had their own public VM profiles with nginx
  TLS termination.
- Video Cloud `infra` owns PostgreSQL, Redis-compatible/Valkey, and Prometheus.
- EMQX MQTT is deployed on the `mqtt` VM through Docker Compose.
- coturn runs on a public-only TURN VM.
- Central logging had Loki-backed VM journald forwarders in the legacy flow.
- Deployment secrets are operator-local or GitHub Environment secrets today,
  with OpenBao documented as the staging/production target where available.
- The VM-era acceptance path is retired. Current staging acceptance is driven by
  the workspace K8s/LKE path in `scripts/run-staging-e2e.sh`.

Conflicts and outdated areas:

- Some workspace and service-owned docs may still describe Linux hosts/systemd
  as the active path; those references should be corrected to legacy/historical
  context when encountered.
- Service-owned runbooks still describe VM-local nginx, certbot, systemd units,
  env files, local data paths, and VM backup scripts as primary deployment
  operations.
- This file supersedes the earlier VM migration checklist language; keep future
  updates focused on the active LKE runtime.
- Kubernetes auth for OpenBao, LKE storage choices, PostgreSQL HA, EMQX
  clustering, Redis persistence, and production HSM strategy are not confirmed.

## Provider-neutral adapter migration

Tracked environments no longer set `LKE_REGION`, `LKE_GENERAL_NODE_TYPE`, `LKE_BROKER_NODE_TYPE`, `LKE_DATABASE_NODE_TYPE`, or `LINODE_ACTIVE_SERVICE_LIMIT`. They set `DEPLOYMENT_LOCATION` and provider-neutral node-class vCPU/memory minima. LKE maps `us-west` to `us-sea` and selects the smallest matching Linode type using memory surplus, then vCPU surplus, then type name.

Before the first mutation after this change, the operator must create `cloud_env/<environment>/runtime/adapters/lke/account.env` with `LKE_ACTIVE_SERVICE_LIMIT`. There is no automatic migration from tracked config. The adapter writes selected region and types to `resolved-resources.env`; provider IDs remain in the existing adapter-private state files.

The shared planner now owns workload replicas and logical node counts. `MQTT_REPLICAS` and `VIDEO_CLOUD_API_REPLICAS` are replaced once by `MQTT_MIN_REPLICAS` and `VIDEO_CLOUD_API_MIN_REPLICAS`; generated effective values feed the LKE compatibility renderer. LKE capacity code may check provider quota and current resources but must not recalculate generic workload or node capacity.

DNS is no longer an LKE responsibility. LKE returns normalized public edge and TURN targets; the independently selected GoDaddy or Route53 DNS adapter owns zone discovery, record mutation and DNS-01 challenges. LKE code must not read DNS credentials, invoke vendor APIs, or store DNS provider IDs. See `docs/dns-adapter-architecture.md`.

## Kubernetes Runtime Target Summary

The current validated staging target is Linode Kubernetes Engine (LKE). The
workspace deployer is now structured as provider adapter plus shared
Kubernetes runtime: LKE owns cluster discovery/create/kubeconfig and Linode
API behavior, while the runtime owns RTK workload manifests, secret handling,
rollouts, and E2E orchestration. GKE, AKS, and EKS are reserved provider ids
for future GCP/Azure/AWS adapters; they intentionally fail fast before any
mutation until real provider credentials, cluster semantics, and rollout gates
are implemented.

| Area | Target direction |
| --- | --- |
| Cluster | Provider adapter supplies a Kubernetes cluster and kubeconfig. LKE maps logical location and node-class resource minima to LKE region and deterministic Linode types. Account quota is ignored runtime state, not tracked environment config. TODO: confirm autoscaling limits and maintenance window for production. |
| Namespaces | `platform`, `video-cloud`, `account-manager`, `admin`, `frontend`, `observability`, and `secrets` unless a later platform standard chooses different names. |
| Public HTTP(S) | External HAProxy edge VM in TCP mode forwards public `443/TCP` to ingress-nginx NodePort; staging default NodePort is `30443`; public `80/TCP` remains closed. |
| Public MQTT | External HAProxy edge VM in TCP mode forwards public `8883/TCP` to EMQX/MQTT NodePort; staging default NodePort is `31883`, with a three-pod EMQX StatefulSet cluster spread one per node for HAProxy round-robin. |
| DNS | Shared DNS orchestration publishes LKE edge/TURN targets through the environment-selected GoDaddy or Route53 adapter and uses the same adapter for ACME DNS-01. |
| Internal traffic | Kubernetes Services and NetworkPolicy replace VM private IP allowlists. |
| Stateful storage | Linode Block Storage-backed PVCs where in-cluster persistence is selected. |
| Object storage | Linode Object Storage remains the preferred artifact/media/backup target where applicable. |
| Secrets | OpenBao plus Kubernetes auth or an External Secrets-style sync/injection path. Kubernetes Secrets hold only runtime material, never root tokens, unseal keys, HSM PINs, or private signing keys in Git. |
| Workloads | Shared Kubernetes runtime owns a provider-neutral workload registry. Image env keys, namespace keys, ports, rollout targets, resource override prefixes, Services, and Prometheus scrape targets should be derived from that registry rather than separate handwritten lists. |
| Manifests | Shared Kubernetes runtime should use provider-neutral templates and metadata helpers for non-secret YAML. New secret paths should use typed Kubernetes objects instead of formatting secret payloads into raw YAML strings. |
| Observability | Kubernetes workload registry generates Prometheus scrape config for private `/metrics/prometheus` targets; Loki/logger integration, Kubernetes probes, alerts, and readiness evidence remain operator-owned. |
| Rollback | Roll back by pinned release/image plus data restore procedure; no production cutover without a tested restore path. |

## Migration Inventory

| Service / surface | Current method | Exposure / ports | Persistent data | Target Kubernetes model | Storage / ingress target | Risk | Rollback / TODO |
| --- | --- | --- | --- | --- | --- | --- | --- |
| Video Cloud public API | K8s Deployment/ClusterIP Service through ingress-nginx; legacy reference was `cmd/api` on `api` VM behind `edge` nginx | Public HTTPS through ingress-nginx behind HAProxy TCP passthrough, app service `18080` | PostgreSQL, object/blob storage | Runtime-generated LKE staging bridge deploys Deployment + ClusterIP Service | HAProxy TCP passthrough to ingress-nginx NodePort via workspace `--dns`; preserve `/healthz`, `/version`, API routes | Medium | Manual rollback is DNS back to the previous known-good edge endpoint; do not reintroduce VM release bundles as the normal staging path. |
| Video Cloud workers | K8s Deployments for long-running workers; legacy reference was systemd units on `api` VM (`cleaner`, `statistics`, `metricsexporter`, `mqttusage`, `logingester`) | Private metrics endpoints | PostgreSQL and runtime stores | Runtime-generated LKE staging bridge deploys the long-running workers as Deployments; production manifests remain gated | ClusterIP Services for metrics where needed | Medium | Validate worker startup and metrics in `scripts/run-staging-e2e.sh`; classify any future one-shot/scheduled worker before converting to Job/CronJob. |
| CRS / certissuer | K8s Deployment/ClusterIP Service; legacy reference was `cmd/certissuer` on `api` VM with edge mTLS trusted headers | `certissuer.<domain>:443` through ingress, backend service `9443` | CA public chains, signing audit DB state | Runtime-generated LKE staging bridge deploys Deployment + ClusterIP Service | HAProxy TCP passthrough to ingress-nginx NodePort; separate ingress hostname keeps HTTPS backend protocol and mTLS policy inside K8s | High | Preserve CSR validation and audit behavior; signer migration blocked on key-management gate. |
| Factory enrollment | K8s Deployment/Service in staging; legacy reference was optional systemd service and smoke script | Factory/API HTTP surface, mTLS through certissuer boundary | Enrollment audit and generated device material | Deployment, or Job for controlled factory batch flows if confirmed | Internal Service plus explicit external route only when required | High | TODO: confirm factory/MES network source and auth model. |
| EMQX MQTT | K8s StatefulSet cluster in staging; legacy reference was Docker Compose on `mqtt` VM | MQTT `1883`, MQTTS `8883`, dashboard private | Broker config, retained/session state if enabled | EMQX StatefulSet now, EMQX operator or external broker remains a later production option | HAProxy TCP passthrough to EMQX/MQTT NodePort for MQTTS; not normal HTTP-only Ingress. Staging defaults to three clustered EMQX pods spread one per node so HAProxy round-robins across all NodePort backends. | High | TODO: confirm retained messages/session persistence requirements before production. |
| coturn | External Linode VM managed by the LKE staging flow; legacy reference was public-only VM in the retired five-role topology | `3478/tcp+udp`, relay UDP range `49152-49200` by default; no TURNS in v1 | `/etc/turnserver.conf` plus runtime shared secret and TURN registry node auth key | Runtime-generated LKE staging bridge removes stale K8s coturn resources, configures Video Cloud API with static STUN/TURN fallback URLs pointing at `turn.<VIDEO_CLOUD_DOMAIN>`, and runs `video-cloud-turnregistrar.service` on the VM so K8s `video-cloud-turnregistry` receives register/heartbeat updates through `turnregistry.<VIDEO_CLOUD_DOMAIN>` | Host package + systemd on a minimal VM. Default short name is `turn01`, Linode label `<stack>-turn01`; `turn02+` naming is reserved for later multi-node TURN. The public registry route is signed with `VIDEO_CLOUD_TURN_REGISTRY_NODE_AUTH_KEY`; it is control-plane only, not TURN media. | High | Roll back by reverting Video Cloud API TURN env/DNS to the previous known-good TURN endpoint and disabling the registrar service; do not reintroduce the retired service-owned VM runtime. |
| PostgreSQL | K8s StatefulSet in staging or external database in production-like deployments; legacy reference was local VM databases for Video Cloud infra and Account Manager | Private `5432` | Primary relational state | Runtime-generated LKE staging bridge deploys PostgreSQL as a Kubernetes StatefulSet using a Docker image from `LKE_POSTGRES_IMAGE`, or the default `postgres:16-alpine`; `LKE_IMAGE_REGISTRY` / `rtk-cloud lke-build-images` can build and publish the `postgresql` image artifact. Staging uses ephemeral `emptyDir` by default and `LKE_POSTGRES_STORAGE_MODE=pvc` opt-in; production must compare external retention, operator, StatefulSet, or managed/external PostgreSQL | Production requires PVC or external database plus Object Storage backup target | High | Do not move production data before backup restore drill and rollback plan. |
| Redis / Valkey | K8s Valkey StatefulSet plus Redis exporter in the platform namespace; legacy reference was `infra` VM Redis-compatible service | Private `6379`; exporter private `9121` | Staging uses a Redis PVC by default; production can switch to external/managed cache or a HA StatefulSet design after HA decision | Runtime-generated LKE staging bridge deploys `redis` and `redis-exporter` ClusterIP Services. Prometheus scrapes the exporter for `redis_*` engine metrics. Video Cloud shadow cache and Account Manager user cache reuse the same private Redis endpoint. | Production requires external/managed cache or PVC/HA design if Redis becomes durable state | Medium | Keep exporter private and validate `up{job="redis-exporter"} == 1` before relying on Redis dashboard/alerts. |
| Prometheus | Workspace-managed K8s Deployment/Service | Private `9090`; scrape config generated from workload metrics registry | TSDB | Runtime-generated LKE staging bridge deploys a Prometheus Deployment/Service with scrape config generated from the LKE metrics registry. V1 scrape scope is Video Cloud API, Video Cloud auxiliary exporters, factory enrollment, Account Manager, Cloud Admin, Frontend, Redis exporter, Prometheus self-scrape, and Grafana self metrics. Prometheus Operator, ServiceMonitor, and PodMonitor remain gated to avoid adding CRD/operator lifecycle before the LKE runtime is stable. | Staging bridge uses ephemeral storage; production requires PVC/retention and private-only operator access | Medium | Preserve private-only access and readiness evidence; add PVC/alerting only after observability gate approval. |
| Grafana | Private K8s observability Deployment/Service; legacy reference was optional VM or operator workstation | Private dashboard access | Grafana SQLite/PVC plus provisioned dashboards | Runtime-generated LKE staging bridge deploys Grafana as a private observability Deployment/Service/PVC. It reads the internal Prometheus Service and is embedded for Platform Admins through Cloud Admin's same-origin BFF proxy. | `ClusterIP` only; no public DNS, Ingress, or TLS SAN. Cloud Admin is the only browser-facing path. | Medium | First dashboard covers platform stability, per-brand-cloud MQTT publish/delivery, device fleet health, API RED metrics, runtime log ingestion, cross-service backlog/dead letters, TURN registry health, capacity, and basic infrastructure when exporters exist. |
| Central logger / Loki | K8s logger backend/service integration; legacy reference was Logger VM/backend plus journald forwarders | Private ingest/query; Cloud Admin BFF reads query path | Loki/log store, forwarder cursor/spool | Loki/logger backend Deployment/StatefulSet plus log agent DaemonSet or sidecar-free stdout collection | PVC/Object Storage per retention policy | Medium | VM journald forwarder remains legacy reference. |
| Account Manager API | K8s Deployment/Service/Ingress; legacy reference was public VM, nginx, local PostgreSQL, systemd | Public HTTPS `443`, app service `18081` | PostgreSQL | Deployment + Service | Ingress/Gateway hostname behind the shared HAProxy edge; private metrics Service | Medium | Verify DB migration and smoke on the current K8s route before promotion. |
| Cloud Admin | K8s Deployment/Service/Ingress; legacy reference was public+VPC VM, nginx, Go app, SQLite | Public HTTPS `443`, app service `8080`, private Prometheus upstream | SQLite sessions/cache/audit | Deployment + PVC for SQLite, or TODO migration to production DB | Ingress/Gateway hostname behind the shared HAProxy edge | Medium | Restore SQLite PVC snapshot with known-good release. |
| Frontend | Container recipe with SQLite lead storage | Public HTTPS | SQLite lead DB or migrated store | Deployment + PVC, or migrate lead persistence to database | Ingress/Gateway hostname behind the shared HAProxy edge | Medium | TODO: confirm production persistence target. |
| Public edge | ingress-nginx in LKE with Kubernetes TLS Secret generated by workspace DNS-01 flow; legacy reference was VM-local nginx and certbot DNS-01 | Public HTTPS, MQTTS, and mTLS SNI hostnames | Kubernetes TLS Secret plus HAProxy host runtime config | HAProxy edge VM forwards TCP to ingress-nginx and EMQX NodePorts; ingress-nginx in LKE owns Kubernetes TLS Secret generated by workspace DNS-01 flow | HAProxy host install + systemd, no Docker; cert-manager/webhook remains a later operator-owned option | High | First rollback is manual DNS back to the previous known-good edge endpoint; failover automation is deferred but artifacts must be multi-VM shaped. |
| OpenBao | Target secret manager; VM details not fully confirmed in main docs | Internal HTTPS | Storage backend, audit logs, PKI state | StatefulSet/operator or external OpenBao | PVC/storage backend; Kubernetes auth | High | TODO: confirm storage backend, HA, seal/unseal, audit, backup, policy migration. |
| SoftHSM / PKCS#11 | VM-local SoftHSM/PKCS#11 documented in service configs | Local library/token access | Token DB/private keys | Development/staging only unless explicit production risk approval; external signer/HSM preferred | PVC-backed token storage only for non-production or approved risk | High | Never put PINs/tokens/private keys in images or Git. |
| Backup jobs | VM scripts and manual artifact collection | Operator initiated | Database dumps, SQLite, object storage, manifests | CronJob/Job only after storage targets and retention are confirmed | Linode Object Storage or approved backup target | High | Restore drill required before production cutover. |
| Staging E2E / CI image artifacts | `scripts/run-staging-e2e.sh`, `rtk-cloud lke-resolve-images`, service repo image workflows, and workspace orchestration | Operator/local runner or GitHub Actions workflow | Generated fixtures/artifacts and GHCR image tags | Current wrapper is provider-aware for `linode` and `lke`; LKE path can discover/create the cluster, fetch kubeconfig through Linode API, deploy runtime-generated Kubernetes resources, and use service port-forwards for account, video, factory enrollment, and MQTT tests. Service images are owned and published by their source repos using `sha-<12 char commit>` tags. `.github/workflows/lke-image-artifacts.yml` validates workspace image manifest tooling on PRs; `workflow_dispatch` checks out pinned submodules, verifies the expected GHCR service images exist, and uploads a manifest/env artifact for later provision runs. Default acceptance creates `10` users and `100` devices. | Artifact output remains redacted; image tags are recorded in `lke-image-manifest.json` | Medium | Production LKE smoke Job remains gated; current acceptance for this phase is a passing `scripts/run-staging-e2e.sh` run against LKE. Last validated staging run: `cloud_env/staging/lke/artifacts/staging-e2e/20260618T085249Z/TEST_REPORT.md`. |

## Migration Gates

### Gate 1: Current Architecture Confirmed

Required before implementation:

- Existing deployment, architecture, operation, security, backup, service, and
  infrastructure documents reviewed.
- Source-of-truth documents identified.
- Migration inventory created or updated.
- Service dependency map created or updated.
- Current DNS, TLS, routing, backup, restore, and observability processes
  reviewed.

### Gate 2: LKE Target Architecture Confirmed

Required before implementation:

- LKE region, node pools, sizing, autoscaling, and upgrade policy documented.
- Namespace and RBAC plan documented.
- External HAProxy edge VM, NodePort exposure, DNS-01 TLS issuance, mTLS
  hostname, and MQTT/TURN exposure plans documented.
- Storage and PVC plan documented, including Linode Block Storage behavior.
- OpenBao/External Secrets/secret injection plan documented.
- Monitoring, logging, alerting, and evidence plan documented.
- GitOps or CI/CD deployment flow selected.

### Gate 3: Security And Key Management Confirmed

Required before implementation:

- OpenBao storage, HA, seal/unseal, root/recovery key, audit, policy, PKI, and
  backup plan approved.
- SoftHSM/PKCS#11 production risk reviewed; external signer/HSM/KMS migration
  path documented.
- CRS signing key access, CA chain handling, revocation/CRL/OCSP TODOs, and
  audit requirements documented.
- NetworkPolicy and RBAC plan reviewed.
- Backup encryption and secret escrow plan documented.
- Human approval recorded before moving production secrets or signing keys.

### Gate 4: Migration And Rollback Confirmed

Required before production cutover:

- Migration runbook and rollback runbook approved.
- Backup and restore runbook tested.
- Smoke test checklist and production readiness checklist approved.
- DNS cutover and rollback windows documented.
- Existing VM fallback or explicit decommission plan approved.
- Human approval recorded.

## Implementation Hold

Until the gates above are complete and approved:

- Do not write production Kubernetes YAML, Helm charts, or Kustomize overlays.
- Do not write production CI/CD deployment pipelines.
- Do not change production DNS, secrets, certificates, signing keys, or data.
- Do not decommission any Linode VM.
- Do not move PostgreSQL, OpenBao, SoftHSM/PKCS#11, MQTT, or TURN production
  state.
- Do not mark LKE production readiness complete until the approved smoke/E2E
  suite passes and restore evidence exists for every stateful dependency.

Runtime-generated kubectl resources used by local staging scripts are allowed
only as a bridge for provider-aware orchestration. The current bridge covers
LKE namespaces, PostgreSQL, Video Cloud API/certissuer/factory enrollment,
Video Cloud workers, staging MQTT, coturn, Prometheus, Account Manager, Cloud
Admin, and Frontend. They are not production manifests and do not satisfy the
gates above.
