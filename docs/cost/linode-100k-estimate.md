# AWS Self-Operated K8s 100K Device Reference Estimate

Status: Planning estimate
Region: `ap-southeast-1`
Currency: USD
Collected: 2026-06-23T00:00:00Z
Sizing: 5,000 users, 20 devices per user, 100,000 registered devices, 100,000 usually-online MQTT devices

This is an AWS infrastructure estimate for a self-operated K8s deployment.
Deployment and operation are handled by Realtek. AWS billing is modeled from
the EKS control plane, EC2 worker nodes, EBS, load balancers, NAT/VPC endpoints,
ECR, and S3. Pods describe workload placement on those nodes; pods are not the
billing unit in this EC2 worker-node profile. MQTT runs on self-hosted EMQX
pods, PostgreSQL runs as self-managed PostgreSQL pods, and observability runs on
self-hosted Loki, Grafana, and Prometheus pods. MQTT 100K has passed on the K8s
cloud; video/WebRTC/TURN media remains excluded and still needs separate sizing.
Use this page for budget discussion, not as an AWS quote.

This K8s view intentionally excludes AWS IoT Core, RDS PostgreSQL, CloudWatch
Logs, Amazon Managed Service for Prometheus, Lambda-as-primary-runtime,
ElastiCache, SQS, external managed operations, and camera/WebRTC/TURN media.

## Public Pricing Inputs

| Area | Reference service | Unit price used |
| --- | --- | --- |
| K8s control plane | Amazon EKS standard support | 0.10 USD/cluster-hour, about 73.00 USD/month |
| EC2 worker nodes | Mixed Graviton worker pool | Planning rounded monthly unit by node role |
| Persistent volumes | EBS gp3 | 0.08 USD/GB-month planning baseline |
| Public ingress | ALB/NLB | Planning allowance for HTTPS API and MQTT TCP/TLS ingress |
| Private networking | NAT Gateway / VPC endpoints | Planning allowance for private subnet outbound and AWS service access |
| Container registry / backup objects | ECR and S3 | Planning allowance for images, DB backup, Loki/archive, firmware, and release artifacts |

## 100k Cluster Configuration

| Role | Count | Plan | Monthly unit | Monthly subtotal | Rationale |
| --- | ---: | --- | ---: | ---: | --- |
| EKS control plane | 1 | EKS standard support | 73.00 | 73.00 | AWS-managed Kubernetes API/control plane only; app deployment and operations remain self-operated. |
| System / ingress / small apps | 2 | m7g.large workers | 85.00 | 170.00 | Ingress controller, cert-manager, account/admin/frontend, and low-traffic utility pods. |
| API / backend services | 3 | m7g.xlarge workers | 170.00 | 510.00 | User/device/admin APIs, Video Cloud APIs, certissuer, log ingester, cleaner, statistics, and workers. |
| MQTT / EMQX broker pool | 3 | c7g.xlarge workers | 150.00 | 450.00 | Self-hosted EMQX pods for 100,000 usually-online MQTT devices; not using AWS IoT Core. |
| PostgreSQL data pods | 2 | r7g.xlarge workers | 240.00 | 480.00 | Self-managed PostgreSQL primary/standby pods for account/video metadata; not using RDS. |
| Observability stack | 2 | m7g.xlarge workers | 170.00 | 340.00 | Self-hosted Loki, Grafana, and Prometheus pods; not using CloudWatch Logs or Managed Prometheus. |
| Cache / NATS infra | 2 | m7g.large workers | 85.00 | 170.00 | Self-hosted Redis/Valkey and NATS pods. |
| Persistent volumes | 1,500 GB | EBS gp3 | 0.08 | 120.00 | PostgreSQL 1,000 GB plus Prometheus/Loki/broker/cache PVC allowance; video media excluded. |
| Public ingress | 2 | ALB/NLB allowance | 35.00 | 70.00 | HTTPS API ingress plus MQTT TCP/TLS ingress. |
| Network / registry / backup allowance | 1 | NAT, VPC endpoints, ECR, S3 | 350.00 | 350.00 | Private subnet outbound, image storage/pulls, DB backups, Loki/archive objects, firmware, and release artifacts. |

## Scenario Totals

| Scenario | Calculation | Monthly estimate |
| --- | --- | ---: |
| K8s 100K self-managed cluster | 73.00 + 170.00 + 510.00 + 450.00 + 480.00 + 340.00 + 170.00 + 120.00 + 70.00 + 350.00 | 2,733.00 USD |

Node count: one EKS control plane plus 14 EC2 worker nodes. Worker-node subtotal
is 2,120.00 USD/month before EBS, ingress, NAT/VPC endpoint, ECR, S3, and
backup allowances.

## Per-Unit View

| Scenario | Per user | Per device | 1 user + 20 devices |
| --- | ---: | ---: | ---: |
| K8s 100K self-managed cluster | 2,733.00 USD / 5,000 = 0.55 USD/user-month | 2,733.00 USD / 100,000 = 0.03 USD/device-month | 0.55 USD/month |

## Caveats

- MQTT 100K has passed; use packaged run evidence to right-size API, broker,
  database, and observability nodes.
- Excludes camera/WebRTC/TURN relay media traffic, object-media retention,
  taxes, DNS, email, security appliances, and external monitoring vendors.
- This self-operated K8s profile does not include AWS IoT Core, RDS
  PostgreSQL, CloudWatch Logs, Amazon Managed Service for Prometheus, Lambda as
  primary runtime, ElastiCache, SQS, CloudHSM, or external managed operations.
- Self-managed K8s can be cost-efficient, but operational effort moves to
  the platform team: patching, HA, backup/restore, incident response, capacity
  planning, broker clustering, and database failover.
