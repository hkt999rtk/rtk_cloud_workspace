# AWS Pricing Snapshot For Cost Estimation

Status: Supporting note
Region: `ap-southeast-1` (Asia Pacific, Singapore)
Currency: USD
Collected: 2026-06-06T06:05:06Z
Sizing input: [aws-cost-estimate-worksheet.csv](aws-cost-estimate-worksheet.csv)
Service mapping: [aws-service-mapping.md](aws-service-mapping.md)

This document records the public AWS unit prices used for the first-pass cost
brief. It is a tracking snapshot, not a committed quote. Prices exclude tax,
support plans, enterprise discounts, Savings Plans, Reserved Instances, and
AWS Marketplace charges.

## Retrieval Method

Prices were collected from the AWS Bulk Price List API regional CSV files for
`ap-southeast-1`.

| AWS offer | Regional price list | Publication date |
| --- | --- | --- |
| AmazonECS | <https://pricing.us-east-1.amazonaws.com/offers/v1.0/aws/AmazonECS/current/ap-southeast-1/index.csv> | 2026-06-03T21:57:01Z |
| AmazonEC2 | <https://pricing.us-east-1.amazonaws.com/offers/v1.0/aws/AmazonEC2/current/ap-southeast-1/index.csv> | 2026-06-04T22:00:57Z |
| AmazonCloudFront | <https://pricing.us-east-1.amazonaws.com/offers/v1.0/aws/AmazonCloudFront/current/index.csv> | 2025-07-01T21:16:47Z |
| AWSLambda | <https://pricing.us-east-1.amazonaws.com/offers/v1.0/aws/AWSLambda/current/ap-southeast-1/index.csv> | 2026-05-21T18:47:54Z |
| AmazonRDS | <https://pricing.us-east-1.amazonaws.com/offers/v1.0/aws/AmazonRDS/current/ap-southeast-1/index.csv> | 2026-06-05T18:53:43Z |
| AmazonElastiCache | <https://pricing.us-east-1.amazonaws.com/offers/v1.0/aws/AmazonElastiCache/current/ap-southeast-1/index.csv> | 2026-06-05T04:59:31Z |
| AmazonS3 | <https://pricing.us-east-1.amazonaws.com/offers/v1.0/aws/AmazonS3/current/ap-southeast-1/index.csv> | 2026-05-28T22:27:23Z |
| AWSIoT | <https://pricing.us-east-1.amazonaws.com/offers/v1.0/aws/AWSIoT/current/ap-southeast-1/index.csv> | 2026-05-28T20:49:00Z |
| CloudHSM | <https://pricing.us-east-1.amazonaws.com/offers/v1.0/aws/CloudHSM/current/ap-southeast-1/index.csv> | 2026-04-16T19:05:28Z |
| AWSCertificateManager | <https://pricing.us-east-1.amazonaws.com/offers/v1.0/aws/AWSCertificateManager/current/ap-southeast-1/index.csv> | 2025-08-28T15:37:21Z |
| ACM | <https://pricing.us-east-1.amazonaws.com/offers/v1.0/aws/ACM/current/ap-southeast-1/index.csv> | 2026-02-18T14:13:16Z |
| awskms | <https://pricing.us-east-1.amazonaws.com/offers/v1.0/aws/awskms/current/ap-southeast-1/index.csv> | 2025-08-28T15:39:13Z |
| AWSSecretsManager | <https://pricing.us-east-1.amazonaws.com/offers/v1.0/aws/AWSSecretsManager/current/ap-southeast-1/index.csv> | 2025-08-28T15:38:04Z |
| AWSELB | <https://pricing.us-east-1.amazonaws.com/offers/v1.0/aws/AWSELB/current/ap-southeast-1/index.csv> | 2026-05-07T19:01:31Z |
| AmazonCloudWatch | <https://pricing.us-east-1.amazonaws.com/offers/v1.0/aws/AmazonCloudWatch/current/ap-southeast-1/index.csv> | 2026-03-13T17:42:19Z |
| AmazonVPC | <https://pricing.us-east-1.amazonaws.com/offers/v1.0/aws/AmazonVPC/current/ap-southeast-1/index.csv> | 2026-06-04T17:34:26Z |

## Selected Unit Prices

| Area | AWS service | Unit price used |
| --- | --- | --- |
| Container compute | ECS Fargate Linux/x86 vCPU | 0.05056 USD per vCPU-hour |
| Container memory | ECS Fargate Linux/x86 memory | 0.00553 USD per GB-hour |
| Frontend CDN egress | CloudFront Asia Pacific outbound, first 10 TB/month | 0.120 USD per GB |
| Frontend CDN requests | CloudFront Asia Pacific proxy HTTPS requests | 0.012 USD per 10,000 requests |
| Frontend dynamic requests | AWS Lambda requests, Singapore | 0.0000002 USD per request |
| Frontend dynamic compute | AWS Lambda x86 duration tier 1, Singapore | 0.0000166667 USD per GB-second |
| TURN host assumption | EC2 `t4g.small` Linux on demand | 0.0212 USD per instance-hour |
| Database compute | RDS PostgreSQL `db.t4g.medium`, Single-AZ | 0.102 USD per DB-hour |
| Database compute | RDS PostgreSQL `db.t4g.large`, Single-AZ | 0.203 USD per DB-hour |
| Database storage | RDS PostgreSQL gp2/gp3 storage, Single-AZ | 0.138 USD per GB-month |
| Cache | ElastiCache for Valkey `cache.t4g.small` | 0.0384 USD per node-hour |
| Object storage | S3 Standard, first 50 TB | 0.025 USD per GB-month |
| Object requests | S3 PUT/COPY/POST/LIST | 0.000005 USD per request |
| IoT connection | AWS IoT Core connection minutes | 0.096 USD per million minutes |
| IoT messaging | AWS IoT Core messages, first 1B/month | 1.20 USD per million messages |
| IoT state | AWS IoT Device Shadow/Registry operations | 1.50 USD per million operations |
| Load balancer | Application Load Balancer | 0.0252 USD per ALB-hour |
| Load balancer capacity | Application Load Balancer LCU | 0.008 USD per LCU-hour |
| NAT | NAT Gateway | 0.059 USD per gateway-hour |
| NAT data processing | NAT Gateway data processed | 0.059 USD per GB |
| Logs | CloudWatch custom log ingestion, Standard | 0.70 USD per GB ingested |
| Logs retention | CloudWatch log storage | 0.03 USD per GB-month |
| Metrics | CloudWatch custom metric, first 10k | 0.30 USD per metric-month |
| Secrets | Secrets Manager secret | 0.40 USD per secret-month |
| Secrets API | Secrets Manager API requests | 0.000005 USD per request |
| Key management | KMS customer managed key version | 1.00 USD per key-month |
| Key API | KMS requests | 0.000003 USD per request |
| HSM | CloudHSM new usage | 1.86 USD per HSM-hour |
| Private PKI | ACM Private CA general-purpose CA | 400 USD per CA-month |
| Private PKI | ACM Private CA short-lived CA | 50 USD per CA-month |
| Private certificate issuance | ACM Private CA general-purpose certs, first 1,000 | 0.75 USD per certificate |
| Private certificate issuance | ACM Private CA general-purpose certs, next 1,001-10,000 | 0.35 USD per certificate |
| Private certificate issuance | ACM Private CA short-lived certs | 0.058 USD per certificate |
| Revocation evidence | ACM Private CA OCSP query | 0.20 USD per 100,000 queries |

## Rough Estimate

The calculation below uses the commercial-pilot worksheet assumptions:

| Assumption | Value |
| --- | --- |
| End users | 2,500 |
| Devices per user | 4 |
| Registered devices | 10,000 |
| Average connected MQTT devices | 10,000 |
| Camera-capable devices | 0 in first estimate; camera/WebRTC profile excluded |
| Runtime model | S3/CloudFront plus optional Lambda for public frontend, ECS Fargate for backend services; coturn/TURN excluded until camera/WebRTC profile is enabled |
| Database model | One shared Single-AZ RDS PostgreSQL `db.t4g.large` server for account and video schemas |
| Key and certificate model | One CloudHSM plus CloudHSM-backed certissuer; ACM Private CA excluded from default estimate |
| NAT assumption | One NAT Gateway, 200 GB processed per month |
| Availability posture | Single-region pilot, production-like but not full multi-region HA |

The baseline uses 100% average connected MQTT devices because no credible
general public benchmark was found for a 30% online ratio, and AWS IoT pricing
examples commonly model connected-device fleets as continuously connected. Use
measured device telemetry to reduce this assumption later.

| Cost area | Monthly estimate | Notes |
| --- | ---: | --- |
| ECS Fargate application services | 539.79 | Account Manager, Video Cloud, Admin BFF, Client/backends, MQTT/logger bridge, cert issuer, API adapters, workers; public frontend Fargate removed. |
| Public frontend CloudFront CDN | 12.07 | 100 GB CloudFront egress plus 60,000 HTTPS requests/month. |
| Public frontend Lambda | 0.06 | 60,000 requests/month at 256 MB and 200 ms average duration. |
| Public frontend S3 static origin | 0.03 | 1 GB static asset storage and small deployment PUT allowance. |
| RDS PostgreSQL | 182.69 | One shared `db.t4g.large` DB server plus 250 GB account/video storage; logs go to CloudWatch. |
| ElastiCache for Valkey | 28.03 | One non-redundant `cache.t4g.small` node for the original Redis-compatible cache. |
| S3 storage and PUT requests | 6.78 | Firmware binaries, backups, CI/release artifacts, and non-camera object storage; camera snapshots excluded. |
| AWS IoT Core | 164.95 | Connection minutes, MQTT messages, and Device Shadow operations for 10,000 usually-online devices. |
| Application Load Balancer | 24.24 | One ALB and one LCU assumption. |
| NAT Gateway | 54.87 | One gateway plus 200 GB data processed. |
| EC2 TURN assumption | 0.00 | Camera/WebRTC profile excluded from first estimate. |
| CloudWatch Logs | 24.53 | 33.6 GB/month ingestion plus 30-day retention. |
| Secrets Manager | 20.05 | 50 secrets plus 10,000 API calls. |
| KMS | 5.30 | Five customer managed keys plus 100,000 requests. |
| Base subtotal before HSM/Private CA | 1,063.38 | Application, data, cache, storage, MQTT, logging, basic network, frontend hosting, and key API surface; camera/WebRTC excluded. |
| CloudHSM | 1,357.80 | One HSM running 730 hours/month; no HSM redundancy assumed for early stage. |
| ACM Private CA | 0.00 | Excluded from default estimate because certificates are signed by CloudHSM-backed certissuer. |

Frontend calculation:

| Item | Calculation | Monthly estimate |
| --- | --- | ---: |
| CloudFront data transfer out | 100 GB * 0.120 USD/GB | 12.00 |
| CloudFront HTTPS requests | 2,000 hits/day * 30 days * 0.0000012 USD/request | 0.07 |
| Lambda requests | 2,000 hits/day * 30 days * 0.0000002 USD/request | 0.01 |
| Lambda duration | 60,000 requests * 0.256 GB * 0.2 seconds * 0.0000166667 USD/GB-second | 0.05 |
| S3 static origin | 1 GB storage plus small deployment PUT allowance | 0.03 |

AWS IoT Core calculation:

| Item | Calculation | Monthly estimate |
| --- | --- | ---: |
| Connection minutes | 10,000 devices * 24 * 60 * 30 * 0.096 USD / 1M minutes | 41.47 |
| Telemetry/status messages | 10,000 devices * 12/hour * 24 * 30 * 1.20 USD / 1M messages | 103.68 |
| Downlink command messages | 10,000 devices * 1/day * 30 * 1.20 USD / 1M messages | 0.36 |
| Shadow update messages | 10,000 devices * 1/hour * 24 * 30 * 1.20 USD / 1M messages | 8.64 |
| Shadow operations | 7.2M operations * 1.50 USD / 1M operations | 10.80 |

| Scenario | Estimated monthly cost |
| --- | ---: |
| Base services only, excluding CloudHSM | 1,063.38 USD |
| Default estimate with one CloudHSM and self-managed certissuer | 2,421.18 USD |
| Robust redundant design, excluding CloudHSM | 1,497.11 USD |
| Robust redundant design with two CloudHSMs | 4,212.71 USD |

Per-unit calculation:

| Scenario | Calculation | Estimate |
| --- | --- | ---: |
| Base services per user | 1,063.38 USD / 2,500 users | 0.43 USD/user-month |
| Base services per device | 1,063.38 USD / 10,000 devices | 0.11 USD/device-month |
| Default with CloudHSM per user | 2,421.18 USD / 2,500 users | 0.97 USD/user-month |
| Default with CloudHSM per device | 2,421.18 USD / 10,000 devices | 0.24 USD/device-month |
| Robust with CloudHSM per user | 4,212.71 USD / 2,500 users | 1.69 USD/user-month |
| Robust with CloudHSM per device | 4,212.71 USD / 10,000 devices | 0.42 USD/device-month |

Robust-profile changes:

| Area | Baseline | Robust profile |
| --- | --- | --- |
| CloudHSM | 1 HSM | 2 HSMs |
| RDS PostgreSQL | Single-AZ shared `db.t4g.large` | Multi-AZ-style writer plus standby estimate |
| ElastiCache/Valkey | 1 `cache.t4g.small` node | 2 `cache.t4g.small` nodes |
| NAT Gateway | 1 gateway plus 200 GB processed | 2 gateways plus 200 GB processed; gateway-hours double, data processing does not |
| Video workers | 1 task per worker service | 2 tasks per worker service |
| Certissuer/factory enrollment | 1 task | 2 tasks |
| Camera/WebRTC | Excluded | Excluded |
| ACM Private CA | Excluded | Excluded |

Robust is not a blanket 2x multiplier. It increases only the components that
are AZ-scoped, single-instance, or explicitly duplicated for service continuity.
Traffic-priced managed services stay flat when product traffic is unchanged.

Robust cost delta:

| Cost area | Baseline | Robust | Delta |
| --- | ---: | ---: | ---: |
| ECS Fargate backend services | 539.79 | 719.72 | 179.93 |
| RDS PostgreSQL | 182.69 | 365.38 | 182.69 |
| ElastiCache for Valkey | 28.03 | 56.06 | 28.03 |
| NAT Gateway | 54.87 | 97.94 | 43.07 |
| CloudHSM | 1,357.80 | 2,715.60 | 1,357.80 |
| Other baseline items | 257.99 | 257.99 | 0.00 |
| Total with CloudHSM | 2,421.18 | 4,212.71 | 1,791.53 |

Robust cost behavior:

| Behavior | Items | Reason |
| --- | --- | --- |
| Doubled | CloudHSM, RDS estimate, ElastiCache | These are single-instance or stateful components where the robust profile adds a second node/standby. |
| Partially increased | NAT Gateway, ECS Fargate backend services | NAT gateway-hours double but data processing stays the same; only selected worker/certissuer tasks are duplicated. |
| Unchanged | AWS IoT Core, CloudWatch Logs, ALB, frontend CDN/Lambda/S3, Secrets Manager, KMS, S3 storage | Product traffic, log volume, and request volume are unchanged between baseline and robust profiles. |

Top 10 monthly cost items:

| Rank | Cost item | Monthly estimate |
| ---: | --- | ---: |
| 1 | CloudHSM, 1 HSM | 1,357.80 |
| 2 | ECS Fargate backend services | 539.79 |
| 3 | RDS PostgreSQL, shared `db.t4g.large` plus storage | 182.69 |
| 4 | AWS IoT Core MQTT plus Shadow | 164.95 |
| 5 | NAT Gateway | 54.87 |
| 6 | ElastiCache for Valkey | 28.03 |
| 7 | CloudWatch Logs | 24.53 |
| 8 | Application Load Balancer | 24.24 |
| 9 | Secrets Manager | 20.05 |
| 10 | Public frontend CloudFront CDN | 12.07 |

Robust top 10 monthly cost items:

| Rank | Cost item | Monthly estimate |
| ---: | --- | ---: |
| 1 | CloudHSM, 2 HSMs | 2,715.60 |
| 2 | ECS Fargate backend services | 719.72 |
| 3 | RDS PostgreSQL, Multi-AZ-style estimate | 365.38 |
| 4 | AWS IoT Core MQTT plus Shadow | 164.95 |
| 5 | NAT Gateway, 2 gateways | 97.94 |
| 6 | ElastiCache for Valkey, 2 nodes | 56.06 |
| 7 | CloudWatch Logs | 24.53 |
| 8 | Application Load Balancer | 24.24 |
| 9 | Secrets Manager | 20.05 |
| 10 | Public frontend CloudFront CDN | 12.07 |

## Service Set

| Current capability | AWS service candidate |
| --- | --- |
| Public frontend | S3 origin plus CloudFront, with Lambda for lightweight dynamic routes if needed |
| HTTP APIs and backend service runtime | ECS Fargate behind Application Load Balancer |
| Relational persistence | Amazon RDS for PostgreSQL |
| Redis-compatible cache | Amazon ElastiCache for Valkey |
| MQTT broker and device state | AWS IoT Core plus Device Shadow |
| Object artifacts, snapshots, firmware, backups | Amazon S3 |
| Device/app key protection | AWS CloudHSM, AWS KMS, AWS Secrets Manager |
| Device/app certificate authority | CloudHSM-backed certissuer; AWS ACM Private CA excluded unless choosing an AWS-managed CA profile |
| Public TLS certificates | ACM public certificates for AWS-integrated endpoints; exportable public certificates only if required |
| Runtime logs and operational metrics | Amazon CloudWatch Logs and CloudWatch metrics/alarms |
| Background jobs and async events | SQS/EventBridge or ECS workers; exact split still needs architecture choice |
| TURN relay | Excluded from first estimate; add EC2 or ECS-on-EC2 running coturn when camera/WebRTC profile is enabled |
| DNS | Route 53 |
| Private networking | VPC subnets, security groups, NAT Gateway, VPC endpoints where cost-effective |

## Cost Drivers

CloudHSM dominates the default estimate. The base application and data plane is
roughly 1.1k USD/month under the pilot assumptions, while one CloudHSM adds
1,357.80 USD/month. Adding a second CloudHSM later would add another
1,357.80 USD/month at the current collected unit price.

Reducing HSM/PKI cost requires an explicit security decision. Candidate options
include using KMS without CloudHSM for less sensitive keys or separating
manufacturing CA requirements from cloud runtime mTLS requirements. ACM Private
CA is tracked as an optional AWS-managed CA profile, not part of the default
CloudHSM-backed certissuer estimate.

## Not Yet Fully Priced

These items should be refined before using the estimate as a budget:

| Gap | Why it matters |
| --- | --- |
| Internet data transfer out | Firmware downloads, video relay, and admin/API responses may add material egress cost. |
| TURN relay volume | Coturn compute is cheap; relay bandwidth is the real driver. |
| AWS IoT Rules/Lambda/Timestream actions | The estimate prices MQTT/Shadow only, not downstream rule actions. |
| Managed Prometheus/Grafana | Current observability may stay on CloudWatch or use managed observability services. |
| RDS Multi-AZ, replicas, Aurora, or split DB instances | Pilot estimate uses one shared Single-AZ RDS server; production HA or isolation will cost more. |
| VPC endpoints | Can reduce NAT traffic but add hourly and data-processing charges. |
| WAF/Shield | Not included in the first-pass security perimeter cost. |
| Support plan | Business or Enterprise support can be a percentage-based addition. |
| Committed-use discounts | Savings Plans, Reserved Instances, and enterprise discounts may reduce compute/database cost. |
