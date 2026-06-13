# Linode Kubernetes Engine Migration Inventory

Status: provider-aware staging bridge with production migration gates.

Owner: `rtk_cloud_workspace`.

Last reviewed: 2026-06-14.

## Purpose

This document is the source-of-truth inventory and gate checklist for migrating
the current Linode VM deployment model to Linode Kubernetes Engine (LKE). It
does not define production Kubernetes manifests, Helm charts, Dockerfiles, or
CI/CD deployment pipelines. Those implementation artifacts are blocked until
the gates in this document are reviewed and approved.

Existing service behavior should be preserved unless the Kubernetes migration
requires a documented change. When this document cannot confirm a detail from
the repository, it marks the gap as `TODO:`.

This is the only workspace migration inventory and gate checklist for LKE. Do
not create a parallel LKE architecture document unless this file explicitly
routes a service-owned detail there.

## Current Architecture Review

Reviewed workspace documents and configuration:

| File | Classification | Notes |
| --- | --- | --- |
| `docs/private-cloud-deployment.md` | Source | Workspace deployment BOM, orchestration order, upgrade, rollback, backup, support boundaries. |
| `docs/linode-staging-deployment-snapshot.md` | Supporting note | Snapshot of current staging endpoints and VM placement. |
| `docs/deployment-secrets-governance.md` | Source | Deployment secret layout and handling rules. |
| `docs/service-logging-architecture.md` | Source | Central logging target, forwarder behavior, Loki/logger boundaries. |
| `repos/rtk_video_cloud/linode_deploy/docs/ARCHITECTURE.md` | Source | Current five-role Linode VM topology and network boundary. |
| `repos/rtk_video_cloud/linode_deploy/docs/RUNBOOK.md` | Source | Video Cloud provision/deploy/verify operating model. |
| `repos/rtk_video_cloud/linode_deploy/configs/*.yaml.example` | Config evidence | Linode region, VPC, role labels, ports, and deploy manifest shape. |
| `repos/rtk_video_cloud/deploy/systemd/`, `deploy/docker-compose.*.yml`, `deploy/prometheus/` | Config evidence | Installed services, EMQX/coturn/PostgreSQL/Prometheus packaging. |
| `repos/rtk_account_manager/linode_deploy/docs/RUNBOOK.md` | Source | Account Manager public VM deployment, local PostgreSQL, nginx/TLS, backup. |
| `repos/rtk_cloud_admin/docs/private-cloud-deployment.md` | Source | Cloud Admin VM deployment, upstream dependencies, SQLite persistence, backup. |
| `scripts/run-staging-e2e.sh`, `stg.sh`, `tests/staging-*.test.sh` | Operational evidence | Current staging orchestration, validation, and E2E acceptance paths. |

Current Linode VM model:

- Video Cloud uses five Linode roles: `edge`, `api`, `infra`, `mqtt`, and
  `coturn`.
- Public HTTPS enters through VM-local nginx on `edge`; certbot uses DNS-01.
- Account Manager and Cloud Admin currently have their own public VM profiles
  with nginx TLS termination.
- Video Cloud `infra` owns PostgreSQL, Redis-compatible/Valkey, and Prometheus.
- EMQX MQTT is deployed on the `mqtt` VM through Docker Compose.
- coturn runs on a public-only TURN VM.
- Central logging is Loki-backed and currently documented around journald
  forwarders on VMs.
- Deployment secrets are operator-local or GitHub Environment secrets today,
  with OpenBao documented as the staging/production target where available.
- Current staging acceptance is driven by workspace scripts such as
  `scripts/run-staging-e2e.sh`; an LKE replacement must be documented before the
  VM-era acceptance path is retired.

Conflicts and outdated areas:

- Workspace production-like deployment language still describes Linux
  hosts/systemd as the main production path.
- Service-owned runbooks still describe VM-local nginx, certbot, systemd units,
  env files, local data paths, and VM backup scripts as primary deployment
  operations.
- No prior LKE migration inventory or migration gate checklist was found.
- Kubernetes auth for OpenBao, LKE storage choices, PostgreSQL HA, EMQX
  clustering, Redis persistence, and production HSM strategy are not confirmed.

## LKE Target Summary

The target is Linode Kubernetes Engine, not generic Kubernetes.

| Area | Target direction |
| --- | --- |
| Cluster | LKE cluster with environment-specific node pools. TODO: confirm region, node types, autoscaling limits, and maintenance window. |
| Namespaces | `platform`, `video-cloud`, `account-manager`, `admin`, `frontend`, `observability`, and `secrets` unless a later platform standard chooses different names. |
| Public HTTP(S) | Linode NodeBalancer fronting Ingress or Gateway API; cert-manager owns TLS automation. |
| DNS | Existing GoDaddy DNS-01 can be represented as cert-manager issuer config. TODO: confirm whether Linode DNS should replace GoDaddy for LKE. |
| Internal traffic | Kubernetes Services and NetworkPolicy replace VM private IP allowlists. |
| Stateful storage | Linode Block Storage-backed PVCs where in-cluster persistence is selected. |
| Object storage | Linode Object Storage remains the preferred artifact/media/backup target where applicable. |
| Secrets | OpenBao plus Kubernetes auth or an External Secrets-style sync/injection path. Kubernetes Secrets hold only runtime material, never root tokens, unseal keys, HSM PINs, or private signing keys in Git. |
| Observability | Prometheus-compatible scraping, Loki/logger integration, Kubernetes probes, alerts, and readiness evidence. |
| Rollback | Roll back by pinned release/image plus data restore procedure; no production cutover without a tested restore path. |

## Migration Inventory

| Service / surface | Current method | Exposure / ports | Persistent data | Target Kubernetes model | Storage / ingress target | Risk | Rollback / TODO |
| --- | --- | --- | --- | --- | --- | --- | --- |
| Video Cloud public API | `cmd/api` on `api` VM behind `edge` nginx | Public HTTPS via `edge:443`, private app `18080` | PostgreSQL, object/blob storage | Deployment + Service | Ingress/Gateway via NodeBalancer; preserve `/healthz`, `/version`, API routes | Medium | Roll back to VM release bundle until LKE cutover is approved. |
| Video Cloud workers | systemd units on `api` VM (`cleaner`, `statistics`, `metricsexporter`, `mqttusage`, `logingester`) | Private metrics endpoints | PostgreSQL and runtime stores | Runtime-generated LKE staging bridge deploys the long-running workers as Deployments; production manifests remain gated | ClusterIP Services for metrics where needed | Medium | Validate worker startup and metrics in `scripts/run-staging-e2e.sh`; classify any future one-shot/scheduled worker before converting to Job/CronJob. |
| CRS / certissuer | `cmd/certissuer` on `api` VM, edge mTLS trusted headers | `certissuer.<domain>:443` via nginx SNI to private `9443` | CA public chains, signing audit DB state | Deployment + Service | Separate mTLS Ingress/Gateway hostname or TCP/TLS gateway path | High | Preserve CSR validation and audit behavior; signer migration blocked on key-management gate. |
| Factory enrollment | Optional systemd service and smoke script | Factory/API HTTP surface, mTLS through certissuer boundary | Enrollment audit and generated device material | Deployment, or Job for controlled factory batch flows if confirmed | Internal Service plus explicit external route only when required | High | TODO: confirm factory/MES network source and auth model. |
| EMQX MQTT | Docker Compose on `mqtt` VM | MQTT `1883`, MQTTS `8883`, dashboard private | Broker config, retained/session state if enabled | EMQX operator/StatefulSet, or external broker | LoadBalancer/NodeBalancer for MQTT(S); not normal HTTP-only Ingress | High | TODO: confirm retained messages/session persistence and cluster requirements. |
| coturn | public-only VM | `3478/tcp+udp`, `5349/tcp`, relay UDP range | Config and TLS material | Runtime-generated LKE staging bridge deploys coturn as a Deployment/Service with internal ClusterIP by default; production public TURN needs approved exposure and scaling design | `LKE_COTURN_SERVICE_TYPE=LoadBalancer` is available for explicit public exposure testing; Linode UDP/TCP behavior and relay range must be confirmed before production | High | Prove LKE TURN data-plane behavior and rollback before removing VM coturn fallback. |
| PostgreSQL | Local VM databases for Video Cloud infra and Account Manager | Private `5432` | Primary relational state | Runtime-generated LKE staging bridge deploys PostgreSQL with ephemeral `emptyDir` by default and `LKE_POSTGRES_STORAGE_MODE=pvc` opt-in; production must compare external/VM retention, operator, StatefulSet, or managed/external PostgreSQL | Production requires PVC or external database plus Object Storage backup target | High | Do not move production data before backup restore drill and rollback plan. |
| Redis / Valkey | `infra` VM Redis-compatible service | Private `6379` | TODO: cache vs queue vs persistent store | Deployment/StatefulSet or external cache depending on persistence | PVC only if persistence is required | Medium | TODO: confirm usage from config/code before selecting HA mode. |
| Prometheus | `infra` VM | Private `9090`; scrapes node/nginx/postgres/redis/EMQX/app targets | TSDB | Runtime-generated LKE staging bridge deploys a Prometheus Deployment/Service with generated scrape config; production stack/operator remains gated | Staging bridge uses ephemeral storage; production requires PVC/retention and private-only operator access | Medium | Preserve private-only access and readiness evidence; add PVC/alerting only after observability gate approval. |
| Central logger / Loki | Logger VM/backend plus journald forwarders | Private ingest/query; Cloud Admin BFF reads query path | Loki/log store, forwarder cursor/spool | Loki/logger backend Deployment/StatefulSet plus log agent DaemonSet or sidecar-free stdout collection | PVC/Object Storage per retention policy | Medium | VM journald forwarder remains legacy reference. |
| Account Manager API | Public VM, nginx, local PostgreSQL, systemd | Public HTTPS `443`, app `18081` | PostgreSQL | Deployment + Service | Ingress/Gateway hostname; private metrics Service | Medium | Keep existing VM path until DB migration and smoke pass. |
| Cloud Admin | Public+VPC VM, nginx, Go app, SQLite | Public HTTPS `443`, app `8080`, private Prometheus upstream | SQLite sessions/cache/audit | Deployment + PVC for SQLite, or TODO migration to production DB | Ingress/Gateway hostname | Medium | Restore SQLite PVC snapshot with known-good release. |
| Frontend | Container recipe with SQLite lead storage | Public HTTPS | SQLite lead DB or migrated store | Deployment + PVC, or migrate lead persistence to database | Ingress/Gateway hostname | Medium | TODO: confirm production persistence target. |
| Nginx / TLS edge | VM-local nginx and certbot DNS-01 | Public HTTPS and mTLS SNI hostnames | Certificates on VM disk | Ingress controller or Gateway API plus cert-manager | NodeBalancer + cert-manager issuer | High | VM nginx may remain temporary bridge during DNS cutover only. |
| OpenBao | Target secret manager; VM details not fully confirmed in main docs | Internal HTTPS | Storage backend, audit logs, PKI state | StatefulSet/operator or external OpenBao | PVC/storage backend; Kubernetes auth | High | TODO: confirm storage backend, HA, seal/unseal, audit, backup, policy migration. |
| SoftHSM / PKCS#11 | VM-local SoftHSM/PKCS#11 documented in service configs | Local library/token access | Token DB/private keys | Development/staging only unless explicit production risk approval; external signer/HSM preferred | PVC-backed token storage only for non-production or approved risk | High | Never put PINs/tokens/private keys in images or Git. |
| Backup jobs | VM scripts and manual artifact collection | Operator initiated | Database dumps, SQLite, object storage, manifests | CronJob/Job only after storage targets and retention are confirmed | Linode Object Storage or approved backup target | High | Restore drill required before production cutover. |
| Staging E2E / CI image artifacts | `scripts/run-staging-e2e.sh`, `rtk-cloud lke-build-images`, and workspace orchestration | Operator/local runner or GitHub Actions workflow | Generated fixtures/artifacts and GHCR image tags | Current wrapper is provider-aware for `linode` and `lke`; LKE path can discover/create the cluster, fetch kubeconfig through Linode API, build/push missing service images when `LKE_IMAGE_REGISTRY` is set, deploy runtime-generated Kubernetes resources, and use service port-forwards for account, video, factory enrollment, and MQTT tests. `.github/workflows/lke-image-artifacts.yml` validates the image artifact tooling on PRs; `workflow_dispatch` builds/pushes the four required LKE images and uploads a manifest/env artifact for later provision runs. | Artifact output remains redacted; image tags are recorded in `lke-image-manifest.json` | Medium | Production LKE smoke Job remains gated; current acceptance for this phase is a passing `scripts/run-staging-e2e.sh` run against LKE. |

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
- Ingress/Gateway, NodeBalancer, DNS, cert-manager, mTLS hostname, and MQTT/TURN
  exposure plans documented.
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
