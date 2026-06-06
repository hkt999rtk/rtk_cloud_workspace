# RTK Cloud AWS Service Mapping For Cost Estimation

Status: supporting-note.

Owner: `rtk_cloud_workspace`.

Last reviewed: 2026-06-06.

## Purpose

This document maps the current RTK Cloud private-cloud structure to AWS service
candidates so the team can estimate AWS operating cost in a later pricing pass.
It is not a migration design, commitment to AWS, or customer-facing architecture.

Use this document to build an AWS Pricing Calculator estimate, a spreadsheet, or
a sizing questionnaire. Keep actual unit prices out of this file because AWS
prices, discounts, regions, and support charges change independently from the
codebase.

## Source Documents

| Area | Source document | What it contributes |
| --- | --- | --- |
| Current private-cloud BOM | `docs/private-cloud-deployment.md` | Component inventory, single-node and production-like profiles, infrastructure expectations. |
| Cross-repo boundaries | `docs/architecture.md` | Source-of-truth repository ownership and service boundaries. |
| Account manager | `repos/rtk_account_manager/docs/SPEC.md` | Account, organization, RBAC, device registry, provisioning, and Postgres persistence scope. |
| Admin console | `repos/rtk_cloud_admin/docs/SPEC.md` | Go BFF, React SPA, SQLite-local console state, upstream proxy boundaries. |
| Public website | `repos/rtk_cloud_frontend/README.md` | Go website, SQLite lead/analytics/search persistence, container deployment shape. |
| Video cloud runtime | `repos/rtk_video_cloud/docs/architecture.md` | API and worker entrypoints, WebRTC/TURN, MQTT, blob storage, telemetry, NATS, Postgres. |
| Video cloud config | `repos/rtk_video_cloud/docs/config-map.md` | Concrete env-backed infrastructure knobs for blob, MQTT, TURN, Redis-compatible cache, NATS, and log ingestion. |
| Video cloud database | `repos/rtk_video_cloud/docs/postgres-schema.md` | Database split, table inventory, log DB option, retention notes. |
| Video cloud PKI and enrollment | `repos/rtk_video_cloud/docs/factory-enrollment-server.md`, `repos/rtk_cloud_contracts_doc/AUTH.md` | Factory enrollment, certissuer, device/app certificates, mTLS, revocation, and signing boundaries. |
| Cross-service broker | `docs/cross-service-broker-packaging.md` | NATS JetStream default, acceptable equivalent requirements, broker ownership split. |
| Contract overview | `repos/rtk_cloud_contracts_doc/CONTRACT_OVERVIEW.md` | Device transport, product telemetry, metrics, and runtime log surfaces. |

## Mapping Principles

- Treat current services as long-running HTTP or worker processes unless a future
  migration explicitly refactors them for Lambda-style execution.
- Prefer AWS managed services for cost line-item clarity, but keep
  self-managed/container options when protocol compatibility or runtime behavior
  matters.
- Separate product telemetry, service logs, runtime/debug logs, and metrics.
  They have different retention, volume, query, and cost drivers.
- Separate "candidate AWS service" from "recommended default." The cheapest
  line item is not always the lowest operating risk.
- Size production and evaluation separately. The current single-node evaluation
  profile can be much cheaper than a production-like AWS design with high
  availability, backups, monitoring, and support.

## High-Level Component Mapping

| Current component | Current role | AWS default candidate for costing | Alternate AWS candidate | Cost drivers to collect |
| --- | --- | --- | --- | --- |
| Public website / lead portal (`rtk_cloud_frontend`) | Go-rendered website, docs, contact leads, lightweight analytics/search SQLite. | S3 plus CloudFront for static assets; Lambda for lightweight dynamic rendering or lead/contact endpoints after refactor; shared RDS/API storage only where persistence remains. | ECS Fargate/App Runner if server-side rendering must stay always-on; EC2 with systemd/container for lift-and-shift. | Static asset storage, CloudFront egress, HTTPS requests, Lambda requests/GB-seconds, persistent lead/contact storage, backup retention. |
| Admin dashboard (`rtk_cloud_admin`) | Go BFF, React SPA served by Go, local sessions/settings/audit/demo cache, upstream proxy. | ECS on Fargate behind internal or public ALB; RDS or EFS if SQLite state remains. | EC2/systemd, EKS, App Runner. | Concurrent operators, API calls, task size/count, session/audit storage, upstream call volume, ALB exposure. |
| Account manager API (`rtk_account_manager`) | Authoritative identity, tenants, RBAC, registry, provisioning intent, outbox/inbox. | ECS on Fargate plus Amazon RDS for PostgreSQL. | EC2/systemd plus RDS, EKS plus RDS, Lambda/API Gateway only after substantial handler and migration redesign. | API requests, task count, DB instance class, DB storage/IOPS, read/write rate, backup retention, NAT/VPC costs. |
| Account manager workers | Outbox/inbox, lifecycle publication/consumption, cleanup jobs. | ECS Fargate service or scheduled ECS tasks. | EC2/systemd workers, Lambda consumers if broker is changed to SQS/EventBridge. | Always-on worker count, message throughput, retry/dead-letter volume, schedule frequency. |
| Video cloud API (`rtk_video_cloud cmd/api`) | Device/app HTTP API, auth, activation, WebRTC signaling, firmware/media routes, MQTT adapter wiring. | ECS on Fargate or EC2 Auto Scaling behind ALB/NLB depending on long-lived connection needs. | EKS, EC2/systemd release bundle. Lambda is not a good first-cost default for websocket/MQTT-adjacent and long-lived runtime behavior. | Device count, connected sessions, HTTP/WebSocket request rate, vCPU/memory, egress, ALB/NLB, autoscaling headroom. |
| Video cloud workers (`cleaner`, `statistics`, `metricsexporter`, `logingester`, `turnregistry`, `crossservice`, `certissuer`) | Background cleanup, metrics, log ingestion, TURN registry, cross-service gateway, certificate issuance. | ECS Fargate services for always-on workers; scheduled ECS tasks for batch cleanup. | EC2/systemd, EKS jobs/deployments, Lambda for narrow scheduled cleanup only after proving runtime fit. | Worker count, schedule frequency, MQTT/log volume, DB writes, metrics scrape frequency, certificate issuance rate. |
| PostgreSQL for account manager and video cloud | Persistent account, registry, runtime, metadata, firmware, telemetry, outbox/inbox, logs depending on profile. | Amazon RDS for PostgreSQL, separate databases or instances by environment and isolation requirement. | Aurora PostgreSQL for higher availability/read scaling; self-managed PostgreSQL on EC2 for lowest managed-service spend but higher ops burden. | Instance class, Multi-AZ, storage GB, IOPS, backup retention, read replicas, connection count, data transfer. |
| Optional Video Cloud log database | Separate DB for device runtime/debug logs when `VIDEO_CLOUD_LOG_DB_DSN` differs from primary DSN. | Separate RDS PostgreSQL instance or database when log volume justifies isolation. | CloudWatch Logs / OpenSearch for query-heavy logs after app adapter changes; same RDS DB for low volume. | Runtime log events/day, retention days, query frequency, write IOPS, storage growth. |
| Object/blob storage | Clips, snapshots, firmware binaries, backups, release artifacts. | Amazon S3 with lifecycle policies. | S3 Intelligent-Tiering, Glacier classes for archive, EFS only for filesystem semantics. | Stored GB by object type, PUT/GET/list requests, lifecycle transitions, retrievals, cross-region replication, egress. |
| EMQX MQTT broker | Device transport when MQTT is enabled; MQTT shadows/logs/snapshots/control. | AWS IoT Core for a managed MQTT/device messaging cost scenario, if topic/auth/shadow behavior is adapted and validated. | Self-managed EMQX on ECS/EC2/EKS, or AWS Marketplace EMQX, when protocol compatibility and current ACL behavior matter. | Connected devices, message count, payload size, rules/actions, retained/shadow traffic, TLS auth model, broker node count. |
| Device shadow hot-state cache | Planned Redis-compatible/Valkey hot path for shadow desired/reported state with Postgres flush. | Amazon ElastiCache for Redis/Valkey. | MemoryDB for Redis if durable Redis-compatible semantics are required; self-managed Redis/Valkey on EC2. | Node class/count, memory used, write rate, replication/Multi-AZ, data transfer, backup retention. |
| Cross-service broker | Account-to-video lifecycle commands and video-to-account events. Current default is NATS JetStream or equivalent. | Amazon SQS plus DLQs for a conservative cost model if ordering/partitioning semantics are redesigned around queues. | Amazon MSK, Amazon MQ, EventBridge, or self-managed NATS JetStream on ECS/EC2/EKS. | Messages/month, payload size, consumers, retention, ordering/partitioning need, DLQ volume, broker node count. |
| Reverse proxy / TLS / routing | Public and internal HTTP routing, TLS termination, access logs, request size/security headers. | Application Load Balancer with ACM certificates and Route 53 DNS. | Network Load Balancer for TURN or TCP-heavy surfaces; CloudFront for public website/static caching; API Gateway only after route model review. | ALB/NLB hours, LCUs, TLS cert count, request rate, bandwidth, hosted zones, DNS queries. |
| WebRTC TURN data plane | coturn relay for WebRTC media when direct connectivity fails or strict relay is required. | EC2 or ECS on EC2 with public IP/NLB; AWS Global Accelerator optional for global latency. | Managed third-party TURN provider; EKS DaemonSet/Deployment. | Relay minutes, media bandwidth, public IPs, cross-AZ/data egress, instance/network size, regional distribution. |
| Metrics and alerting | Prometheus-compatible metrics, service health, dashboard queries, readiness evidence. | Amazon Managed Service for Prometheus plus Amazon Managed Grafana and CloudWatch alarms. | Self-managed Prometheus/Grafana on ECS/EC2; CloudWatch custom metrics for smaller deployments. | Samples/sec, metric cardinality, retention, dashboard users, alert evaluations, custom metric count. |
| Central service logger | Queryable service logs from systemd/services and dashboard wiring. | CloudWatch Logs for first AWS cost model. | OpenSearch Service for richer query/search; self-managed Loki on ECS/EC2 if keeping current logger shape. | Log GB ingested/day, retention, query volume, indexes, dashboard users, export/archive to S3. |
| Secrets | DSNs, auth secrets, MQTT credentials, webhook secrets, deploy keys, private keys. | AWS Secrets Manager. | SSM Parameter Store for lower-cost non-rotating parameters; AWS KMS for envelope encryption. | Number of secrets, API calls/month, rotation Lambda usage, KMS requests. |
| Key and certificate management | Token signing, device/app CA, certissuer, factory enrollment mTLS, service certificates, public TLS, and revocation evidence. | AWS CloudHSM plus CloudHSM-backed `certissuer`, AWS KMS, ACM for public TLS, and Secrets Manager. | AWS Private CA only for an AWS-managed CA profile; KMS-only protection only where the signing model allows it. | HSM hours, certificates issued, KMS requests, service cert count, CRL/OCSP/revocation artifact storage. |
| Backups | DB dumps, object backups, env/secrets escrow metadata, release manifests. | RDS automated backups/snapshots plus S3 backup bucket. | AWS Backup for centralized backup plans. | Backup storage GB-month, snapshot frequency, retention, cross-region copy, restore testing. |
| CI/CD runners and artifacts | Build, test, deploy artifacts and release packages. | GitHub Actions plus S3 artifact bucket and IAM/OIDC. | AWS CodeBuild/CodePipeline if moving CI/CD into AWS. | Build minutes, artifact storage, cache size, transfer, runner concurrency. |

## Cost Estimation Profiles

### Profile A: Lift Current Production-Like Shape

This profile keeps the current process boundaries and maps each service or
worker to containers or EC2-hosted processes.

Likely AWS line items:

- S3/CloudFront plus optional Lambda for `rtk_cloud_frontend`; ECS Fargate or
  EC2 for `rtk_cloud_admin`, `rtk_account_manager`, `rtk_video_cloud`, and
  selected workers.
- RDS PostgreSQL for account manager and video cloud, plus optional separate log
  database.
- S3 for media, firmware, release artifacts, and backups.
- Self-managed EMQX on ECS/EC2/EKS unless AWS IoT Core compatibility is proven.
- Self-managed NATS JetStream, Amazon MSK, Amazon MQ, SQS, or EventBridge for
  the cross-service broker after message-semantics review.
- ALB/NLB, Route 53, ACM, NAT Gateway, CloudWatch Logs, metrics, secrets, and
  backup storage.

Use this as the first pricing pass because it reflects the current runtime with
the least application redesign.

### Profile B: Managed AWS Services Where Practical

This profile prices a more AWS-native target and identifies where code or
operations changes may be required.

Likely AWS line items:

- ECS Fargate/App Runner for HTTP services where long-lived connection behavior
  is acceptable.
- AWS IoT Core for MQTT/device messaging only after validating topic namespaces,
  ACLs, retained/shadow behavior, and device credential provisioning.
- SQS/EventBridge for cross-service lifecycle events only after confirming
  ordering, redelivery, dead-letter, idempotency, and stream naming contracts.
- ElastiCache/Valkey for planned shadow hot-state cache.
- CloudWatch Logs, Managed Prometheus, Managed Grafana, Secrets Manager, RDS, and
  S3.

Use this profile for a managed-service cost comparison. Do not treat it as a
drop-in migration plan.

### Profile C: Robust Redundant Managed AWS Services

This profile keeps the same product traffic assumptions as the first AWS
baseline but prices redundant infrastructure where the baseline intentionally
avoids single-failure protection.

Likely AWS line items:

- Two CloudHSMs instead of one HSM.
- Multi-AZ-style RDS PostgreSQL estimate for the shared account/video database.
- Two ElastiCache/Valkey cache nodes instead of one node.
- Two NAT Gateways for two-AZ private subnet routing.
- Two tasks for each Video Cloud worker service and certissuer/factory
  enrollment runtime.
- Camera/WebRTC/TURN and ACM Private CA remain excluded unless a later profile
  explicitly enables them.

Use this profile for a first robust-production cost comparison after the
baseline. It improves resilience inside one region, but it is not a multi-region
disaster-recovery estimate.

### Profile D: Low-Cost Evaluation Environment

This profile approximates the current single-node evaluation setup on AWS.

Likely AWS line items:

- One EC2 instance running systemd/containerized services, local or RDS
  PostgreSQL, local filesystem or S3 blob storage, and optional EMQX.
- Optional ALB or direct EC2 public endpoint behind TLS reverse proxy.
- S3 for backups and release artifacts.
- CloudWatch Logs and basic alarms.

Use this profile for demos, internal validation, or a short customer workshop.
It should not be used as the production estimate unless availability, backup,
security, and operations expectations are deliberately reduced.

## Service-Specific Notes

### Public Website

Current shape:

- Go HTTP service with `net/http`, `html/template`, SQLite, static CSS, and no
  Node runtime.
- Stores contact leads, analytics, and optional search data in SQLite.
- Not authoritative for IoT telemetry, account state, fleet data, OTA execution,
  or production mobile app users.

AWS costing choices:

- For minimal change, run the Go service as ECS/EC2 and preserve SQLite on EFS or
  an attached volume. This is simple but requires operational care for backups
  and concurrency.
- For production-like AWS costing, model migration of lead/analytics/search
  persistence to RDS PostgreSQL or another managed store.
- Static assets can remain served by the app or be moved to S3 plus CloudFront.

Sizing inputs:

- Monthly public page views.
- Contact form submissions/month.
- Analytics events/day and retention days.
- Search query volume and whether OpenAI-backed search remains enabled.
- Static asset and video egress.

### Admin Console

Current shape:

- Go BFF with a React SPA served from the Go backend.
- SQLite is authoritative only for console-local platform admins, sessions,
  settings, audit, preferences, and demo data.
- Account Manager and Video Cloud remain authoritative for customer, device,
  provisioning, firmware, telemetry, stream, and readiness facts.

AWS costing choices:

- ECS/Fargate plus ALB is the cleanest first estimate.
- SQLite persistence can be priced as EFS/volume for lift-and-shift, but a
  production AWS estimate should include RDS or another managed store if the
  console-local state becomes shared across tasks.
- CloudWatch Logs is the default logging estimate.

Sizing inputs:

- Operator users and concurrent sessions.
- Dashboard refresh rate and upstream query rate.
- Audit/session/settings storage size.
- Whether the console is internet-facing or private-only.

### Account Manager

Current shape:

- Go REST API using Gin.
- Postgres-backed identity, organization, RBAC, registry, device groups/tags,
  provisioning operations, outbox/inbox, retry, and dead-letter state.
- Cross-service stream names are `account.video.commands` and
  `video.account.events`.

AWS costing choices:

- ECS/Fargate plus RDS PostgreSQL is the first production-like AWS estimate.
- Lambda/API Gateway should be priced only as a future refactor because the
  current system assumes a long-running Go API and workers with database-backed
  lifecycle state.
- SQS/EventBridge can be considered for lifecycle messages, but only after
  replacing or adapting the broker contract.

Sizing inputs:

- Registered users, organizations, devices, groups, and tags.
- Login/token refresh rate.
- Device registry reads/writes.
- Provision/deactivate operations/day.
- Cross-service message throughput and dead-letter retention.
- RDS storage, IOPS, connection count, and backup retention.

### Video Cloud

Current shape:

- Multiple Go entrypoints: API, certificate issuer, cleaner, cross-service
  gateway, metrics exporter, log ingester, TURN registry, and statistics-related
  workers.
- PostgreSQL stores activation, device state, clip metadata, runtime config,
  refresh tokens, firmware, telemetry, notification state, TURN registry, and
  optional runtime/debug logs.
- Blob payloads for clips, snapshots, and firmware binaries use local or
  S3-backed adapters.
- Device transport supports websocket and MQTT. WebRTC signaling is relayed
  through websocket or MQTT transport. coturn remains the TURN data plane.

AWS costing choices:

- ECS/Fargate or EC2 for API and workers, depending on long-lived connection and
  network behavior.
- RDS PostgreSQL for primary runtime state; separate RDS/log store or CloudWatch
  Logs/OpenSearch for high-volume runtime logs if query/write volume is high.
- S3 for clips, snapshots, and firmware objects.
- EC2/ECS-on-EC2 for coturn/TURN due to public UDP/TCP relay behavior.
- AWS IoT Core is a candidate for MQTT costing, but self-managed EMQX may be
  more faithful to current broker ACL/topic assumptions.

Sizing inputs:

- Active devices and concurrently connected devices.
- Websocket/MQTT sessions.
- WebRTC session count, average duration, and TURN relay ratio.
- Snapshot/clip count, average object size, retention, download rate, and egress.
- Firmware binary sizes, release frequency, target devices per rollout.
- Runtime log events/day and retention.
- Product telemetry events/day.
- Certificate issuance rate.

### MQTT / AWS IoT Core

Current shape:

- The repo packages EMQX as the reference broker.
- MQTT is used for device transport, command/status paths, snapshots, runtime
  logs, and `$vc` device shadow synchronization.
- Device/app broker identities and ACLs are separate from server-side broker
  credentials.

AWS costing choices:

- Price AWS IoT Core as a managed MQTT candidate when estimating an AWS-native
  architecture.
- Price EMQX on ECS/EC2/EKS as the compatibility-preserving candidate.

Compatibility questions before treating AWS IoT Core as final:

- Can existing topic roots and `$vc` shadow namespace map cleanly to AWS IoT
  policies and reserved topics?
- Will device credentials use X.509 registry, custom authorizers, or another
  identity model?
- Are retained messages, QoS behavior, shared subscriptions, and broker metrics
  equivalent enough for the current tests and support workflows?
- Are AWS IoT Device Shadow and Jobs being adopted, or is the current RTK shadow
  and OTA model retained over MQTT topics?

Sizing inputs:

- Number of registered things/devices.
- Simultaneously connected devices.
- Messages/day by direction and payload size.
- Shadow updates/day and document size.
- Rules/actions, if messages are routed to Lambda, SQS, Timestream, or other AWS
  services.

### Cross-Service Broker

Current shape:

- Workspace default is NATS JetStream, with durable streams, redelivery,
  dead-letter handling, and observable publish/consume outcomes.
- Logical streams are account-to-video commands and video-to-account events.

AWS costing choices:

- Self-managed NATS JetStream is the most direct semantic mapping.
- SQS plus DLQs may be the lowest-friction AWS managed estimate if stream
  semantics can be adapted.
- EventBridge can model event routing but may not directly replace durable
  command stream behavior.
- Amazon MSK or Amazon MQ may be considered if operations prefer a managed broker
  family with stronger stream/broker semantics.

Sizing inputs:

- Lifecycle messages/day.
- Required retention window.
- Ordering or partitioning requirement by account device id.
- Retry count, dead-letter volume, and consumer count.
- Whether broker state must be backed up/restored.

### Key And Certificate Management

Current shape:

- Video Cloud supports token signing through an `ed25519-secret`, PEM key files,
  or a PKCS#11 provider suitable for SoftHSM/local CI or production HSM modules.
- Device and app/user mTLS certificates are part of the auth model.
- Factory enrollment accepts CSRs from factory fixtures, audits the request, and
  calls `cmd/certissuer`; `cmd/api` must not hold CA signing keys.
- Public TLS, service-to-service mTLS, CA bundles, CRL/OCSP inputs, and local
  revocation evidence are separate cost and operations concerns.

AWS costing choices:

- Price AWS CloudHSM for HSM/HMS-backed signing and CA key protection when the
  deployment requires non-exportable signing keys or PKCS#11 behavior.
- Default estimate excludes AWS Private CA because `certissuer` signs through
  CloudHSM. Price AWS Private CA only as a separate AWS-managed CA profile.
- Price ACM for public TLS certificates and AWS KMS for envelope encryption of
  secrets, logs, backups, and object-storage keys.
- Price Secrets Manager for HSM PIN references, HMAC/shared keys, DSNs, webhook
  secrets, MQTT credentials, and non-key secret material.

Sizing inputs:

- Number of HSMs and required availability posture.
- Number of CA hierarchies and active private CAs.
- Initial device and app/user certificates issued.
- Monthly certificate rotation, replacement, and revocation volume.
- KMS key count and KMS request volume.
- CRL/OCSP or revocation artifact storage and fetch volume.

## Cost Questionnaire

Fill these values before producing the first AWS estimate:

| Question | Needed for |
| --- | --- |
| AWS region and currency | All AWS services. |
| Evaluation, production-like single-region, or HA/multi-AZ target | Compute, RDS, broker, load balancer, backup costs. |
| Expected organizations, users, and registered devices | Account Manager, Admin, RDS. |
| Simultaneously connected devices by websocket and MQTT | Video Cloud compute, broker, IoT Core, load balancers. |
| MQTT messages/day by topic family and average payload size | AWS IoT Core or EMQX sizing. |
| WebRTC sessions/day, average duration, and TURN relay percentage | TURN compute and data transfer. |
| Clip/snapshot count/day, average size, retention, and download rate | S3 storage, requests, data transfer, cleaner workload. |
| Firmware binary size, releases/month, target devices/release | S3, egress, firmware APIs/workers. |
| Product telemetry events/day and retention | RDS, metrics exporter, dashboard queries. |
| Runtime/debug log events/day and retention | RDS log DB, CloudWatch Logs, OpenSearch. |
| Required RPO/RTO and backup retention | RDS, S3, AWS Backup, cross-region copy. |
| Public internet exposure versus private/VPN-only | ALB/NLB, WAF, NAT, Route 53, CloudFront. |
| Required compliance/log retention | CloudWatch/OpenSearch/S3 archive. |
| CI/CD location | GitHub Actions/S3 artifacts or CodeBuild/CodePipeline. |

## Pricing Worksheet Columns

Use these columns in the later spreadsheet or AWS Pricing Calculator export:

| Column | Meaning |
| --- | --- |
| Current component | Name from the private-cloud BOM or service docs. |
| Current owner repo | Repository responsible for the current implementation. |
| AWS service candidate | Service used for this estimate row. |
| Deployment profile | Evaluation, production-like, managed-native, or HA. |
| Required for MVP | Yes, no, or deployment-dependent. |
| Sizing input | Device count, GB/month, messages/month, vCPU/memory, etc. |
| Pricing unit | AWS billing dimension to fill later. |
| Monthly quantity | Estimated usage before applying unit price. |
| Unit price source | AWS Pricing Calculator, AWS price page, EDP/private quote, or measured bill. |
| Monthly cost | Quantity multiplied by current unit price. |
| Confidence | High, medium, or low based on measured usage quality. |
| Notes / blockers | Compatibility gaps, missing measurements, or required refactor. |

## Open Decisions Before Final Costing

1. Choose the baseline AWS compute model: ECS/Fargate, EC2/systemd, EKS, or a
   mixed model.
2. Decide whether AWS IoT Core is only a comparison point or the target MQTT
   service to validate.
3. Decide whether cross-service lifecycle messages remain on NATS JetStream or
   are redesigned for SQS/EventBridge/MSK/Amazon MQ.
4. Decide whether Admin and Frontend SQLite state is lifted to EFS/volumes or
   migrated to managed database storage.
5. Decide whether device runtime/debug logs stay in PostgreSQL, move to
   CloudWatch Logs/OpenSearch, or split by retention class.
6. Decide production availability level: single-AZ, Multi-AZ, multi-region, or
   customer-specific private region.
7. Decide whether TURN relay is self-operated on AWS or delegated to a managed
   TURN provider.

## Next Step

Create a pricing worksheet from the `Pricing Worksheet Columns` table, then fill
one row per selected AWS candidate under Profile A. After Profile A is priced,
repeat only the components that change under Profile B so the team can compare
lift-and-shift cost against AWS-managed-service cost.
