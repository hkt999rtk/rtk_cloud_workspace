# Linode 100k Device Cluster Estimate

Status: Planning estimate
Region: `us-sea`
Currency: USD
Collected: 2026-06-12T06:45:00Z
Sizing: 25,000 users, 4 devices per user, 100,000 registered devices, 100,000 usually-online MQTT devices

This is a Linode/Akamai Cloud planning profile for extending the current
staging deployment shape to a 100,000-device commercial case. It is not a
validated capacity result and it is not the current Linode bill. Use the current
Linode run-rate only as staging evidence; use this page for budget discussion.

The estimate uses public Akamai Cloud pricing examples from
<https://www.akamai.com/cloud/pricing> and the current deployment roles from
`repos/rtk_video_cloud/linode_deploy/docs/ARCHITECTURE.md`. It assumes a
self-managed cluster: Realtek/platform operators still own broker, database,
cache, observability, patching, backup, restore, scale-out, and incident
response.

## Public Pricing Inputs

| Area | Akamai/Linode service | Unit price used |
| --- | --- | --- |
| Dedicated compute, small control plane | G8 Dedicated General 8x2, 2 vCPU / 8 GB | 70.00 USD/month |
| Dedicated compute, app or broker node | G8 Dedicated Compute 32x16, 16 vCPU / 32 GB | 360.00 USD/month |
| High-memory data node | High Memory 150 GB | 480.00 USD/month |
| NodeBalancer | NodeBalancer | 10.00 USD/month |
| Block Storage | Block Storage | 0.10 USD/GB-month |
| Object Storage | Object Storage | 0.02 USD/GB-month |
| Optional managed service | Akamai Managed Service | 100.00 USD per compute instance/month |

## 100k Cluster Configuration

| Role | Count | Plan | Monthly unit | Monthly subtotal | Rationale |
| --- | ---: | --- | ---: | ---: | --- |
| Edge TLS / HTTP gateway | 2 | G8 Dedicated General 8x2 | 70.00 | 140.00 | Active/standby or active/active edge pair for public HTTPS and device mTLS entry. |
| API / backend services | 3 | G8 Dedicated Compute 32x16 | 360.00 | 1,080.00 | Horizontally scaled Video Cloud/API workers, certissuer, log ingester, cleaner, statistics, and control-plane services. |
| MQTT / EMQX broker cluster | 3 | G8 Dedicated Compute 32x16 | 360.00 | 1,080.00 | Three-broker cluster for 100,000 usually-online MQTT devices; exact capacity must be proven by load test. |
| PostgreSQL data nodes | 2 | High Memory 150 GB | 480.00 | 960.00 | Primary/standby self-managed PostgreSQL for account/video data. |
| Cache / NATS / Prometheus infra | 2 | G8 Dedicated Compute 32x16 | 360.00 | 720.00 | Split self-managed Valkey/Redis, NATS JetStream, and observability from API and broker nodes. |
| Account Manager / Admin / Frontend | 3 | G8 Dedicated General 8x2 | 70.00 | 210.00 | Keep account, admin, and public frontend as separate small production nodes. |
| NodeBalancers | 2 | NodeBalancer | 10.00 | 20.00 | Public HTTP/API and MQTT/TLS balancing. |
| Database block storage | 5,000 GB | Block Storage | 0.10 | 500.00 | 2,500 GB primary plus 2,500 GB standby planning storage. |
| Object storage | 500 GB | Object Storage | 0.02 | 10.00 | Firmware, release artifacts, backups, and non-camera objects; camera/WebRTC media excluded. |

## Scenario Totals

| Scenario | Calculation | Monthly estimate |
| --- | --- | ---: |
| Linode 100k self-managed cluster | 140.00 + 1,080.00 + 1,080.00 + 960.00 + 720.00 + 210.00 + 20.00 + 500.00 + 10.00 | 4,720.00 USD |
| Linode 100k with optional Managed Service | 4,720.00 + 15 compute instances * 100.00 | 6,220.00 USD |

## Per-Unit View

| Scenario | Per user | Per device | 1 user + 4 devices |
| --- | ---: | ---: | ---: |
| Linode 100k self-managed cluster | 4,720.00 USD / 25,000 = 0.19 USD/user-month | 4,720.00 USD / 100,000 = 0.05 USD/device-month | 0.19 USD/month |
| Linode 100k with optional Managed Service | 6,220.00 USD / 25,000 = 0.25 USD/user-month | 6,220.00 USD / 100,000 = 0.06 USD/device-month | 0.25 USD/month |

## Caveats

- Not load-tested yet; use 10k/50k/100k MQTT load evidence to right-size API,
  broker, database, and observability nodes.
- Excludes camera/WebRTC/TURN relay media traffic, object-media retention,
  taxes, support escalation beyond optional Managed Service, DNS, email,
  security appliances, and external monitoring vendors.
- Linode estimate is not service-equivalent to AWS: no AWS IoT Core managed
  broker/shadow, no Cognito managed user pool, no CloudHSM equivalent, and no
  managed RDS/ElastiCache in the base self-managed profile.
- Self-managed Linode can be cost-efficient, but operational effort moves to
  the platform team: patching, HA, backup/restore, incident response, capacity
  planning, broker clustering, and database failover.
