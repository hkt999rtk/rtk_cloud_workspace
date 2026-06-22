# Self-Managed K8s 100K Device Reference Estimate

Status: Planning estimate
Region: `us-sea`
Currency: USD
Collected: 2026-06-12T06:45:00Z
Sizing: 10,000 users, 10 devices per user, 100,000 registered devices, 100,000 usually-online MQTT devices

This is a self-managed K8s reference profile for extending the current staging
deployment shape to a 100,000-device commercial case. MQTT 100K has passed on
the K8s cloud; video/WebRTC/TURN media remains excluded and still needs
separate sizing. Use this page for budget discussion, not as a provider quote.

The estimate uses a reference self-managed compute/storage price book and should
be treated as a K8s node-role sizing note. If the deployment moves to a specific
managed Kubernetes provider, recalculate from that provider's node, storage,
load-balancer, support, and network-transfer prices.

## Public Pricing Inputs

| Area | Reference service | Unit price used |
| --- | --- | --- |
| Dedicated compute, small control plane | G8 Dedicated General 8x2, 2 vCPU / 8 GB | 70.00 USD/month |
| Dedicated compute, app or broker node | G8 Dedicated Compute 32x16, 16 vCPU / 32 GB | 360.00 USD/month |
| High-memory data node | High Memory 150 GB | 480.00 USD/month |
| Edge HAProxy VM | G8 Dedicated General 8x2, 2 vCPU / 8 GB | 70.00 USD/month |
| Block Storage | Block Storage | 0.10 USD/GB-month |
| Object Storage | Object Storage | 0.02 USD/GB-month |
| Optional managed service | External managed service allowance | 100.00 USD per compute instance/month |

## 100k Cluster Configuration

| Role | Count | Plan | Monthly unit | Monthly subtotal | Rationale |
| --- | ---: | --- | ---: | ---: | --- |
| Edge HAProxy TCP gateway | 2 | G8 Dedicated General 8x2 | 70.00 | 140.00 | Host-installed HAProxy edge pair for TCP passthrough to K8s NodePorts; first implementation uses one VM and keeps multi-VM artifacts for future failover. |
| API / backend services | 3 | G8 Dedicated Compute 32x16 | 360.00 | 1,080.00 | Horizontally scaled Video Cloud/API workers, certissuer, log ingester, cleaner, statistics, and control-plane services. |
| MQTT / EMQX broker cluster | 3 | G8 Dedicated Compute 32x16 | 360.00 | 1,080.00 | Three-broker cluster for 100,000 usually-online MQTT devices; MQTT 100K has passed, use packaged run evidence to right-size final broker count. |
| PostgreSQL data nodes | 2 | High Memory 150 GB | 480.00 | 960.00 | Primary/standby self-managed PostgreSQL for account/video data. |
| Cache / NATS / Prometheus infra | 2 | G8 Dedicated Compute 32x16 | 360.00 | 720.00 | Split self-managed Valkey/Redis, NATS JetStream, and observability from API and broker nodes. |
| Account Manager / Admin / Frontend | 3 | G8 Dedicated General 8x2 | 70.00 | 210.00 | Keep account, admin, and public frontend as separate small production nodes. |
| Database block storage | 1,000 GB | Block Storage | 0.10 | 100.00 | 1,000 GB database metadata storage; video/WebRTC media excluded. |
| Object storage | 500 GB | Object Storage | 0.02 | 10.00 | Firmware, release artifacts, backups, and non-camera objects; camera/WebRTC media excluded. |

## Scenario Totals

| Scenario | Calculation | Monthly estimate |
| --- | --- | ---: |
| K8s 100K self-managed cluster | 140.00 + 1,080.00 + 1,080.00 + 960.00 + 720.00 + 210.00 + 100.00 + 10.00 | 4,300.00 USD |
| K8s 100K with optional Managed Service | 4,300.00 + 15 compute instances * 100.00 | 5,800.00 USD |

## Per-Unit View

| Scenario | Per user | Per device | 1 user + 10 devices |
| --- | ---: | ---: | ---: |
| K8s 100K self-managed cluster | 4,300.00 USD / 10,000 = 0.43 USD/user-month | 4,300.00 USD / 100,000 = 0.04 USD/device-month | 0.43 USD/month |
| K8s 100K with optional Managed Service | 5,800.00 USD / 10,000 = 0.58 USD/user-month | 5,800.00 USD / 100,000 = 0.06 USD/device-month | 0.58 USD/month |

## Caveats

- MQTT 100K has passed; use packaged run evidence to right-size API, broker,
  database, and observability nodes.
- Excludes camera/WebRTC/TURN relay media traffic, object-media retention,
  taxes, support escalation beyond optional Managed Service, DNS, email,
  security appliances, and external monitoring vendors.
- Self-managed K8s is not service-equivalent to AWS: no AWS IoT Core managed
  broker/shadow, no Cognito managed user pool, no CloudHSM equivalent, and no
  managed RDS/ElastiCache in the base self-managed profile.
- Self-managed K8s can be cost-efficient, but operational effort moves to
  the platform team: patching, HA, backup/restore, incident response, capacity
  planning, broker clustering, and database failover.
