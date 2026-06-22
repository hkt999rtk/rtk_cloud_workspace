# AWS Pricing Snapshot For Cost Estimation

Status: Supporting note
Region: `ap-southeast-1` (Asia Pacific, Singapore)
Currency: USD
Collected: 2026-06-22T15:33:00Z
Sizing input: [aws-cost-estimate-worksheet.csv](aws-cost-estimate-worksheet.csv)
Service mapping: [aws-service-mapping.md](aws-service-mapping.md)

This document records the public AWS unit prices used for the first-pass cost
brief. It is a tracking snapshot, not a committed quote. Prices exclude tax,
enterprise discounts, Savings Plans, Reserved Instances, and AWS Marketplace
charges. Support-plan adders are listed separately because AWS Support is billed
as a monthly plan fee, not as a per-ticket unit price.

## Retrieval Method

Prices were collected from the AWS Bulk Price List API regional CSV files for
`ap-southeast-1`.

| AWS offer | Regional price list | Publication date |
| --- | --- | --- |
| AmazonECS | <https://pricing.us-east-1.amazonaws.com/offers/v1.0/aws/AmazonECS/current/ap-southeast-1/index.csv> | 2026-06-03T21:57:01Z |
| AmazonEC2 | <https://pricing.us-east-1.amazonaws.com/offers/v1.0/aws/AmazonEC2/current/ap-southeast-1/index.csv> | 2026-06-22T14:02:18Z |
| AmazonCloudFront | <https://pricing.us-east-1.amazonaws.com/offers/v1.0/aws/AmazonCloudFront/current/index.csv> | 2025-07-01T21:16:47Z |
| AWSLambda | <https://pricing.us-east-1.amazonaws.com/offers/v1.0/aws/AWSLambda/current/ap-southeast-1/index.csv> | 2026-06-22T04:37:29Z |
| AmazonRDS | <https://pricing.us-east-1.amazonaws.com/offers/v1.0/aws/AmazonRDS/current/ap-southeast-1/index.csv> | 2026-06-19T20:39:21Z |
| AmazonElastiCache | <https://pricing.us-east-1.amazonaws.com/offers/v1.0/aws/AmazonElastiCache/current/ap-southeast-1/index.csv> | 2026-06-19T03:36:22Z |
| AmazonS3 | <https://pricing.us-east-1.amazonaws.com/offers/v1.0/aws/AmazonS3/current/ap-southeast-1/index.csv> | 2026-06-18T21:09:42Z |
| AWSIoT | <https://pricing.us-east-1.amazonaws.com/offers/v1.0/aws/AWSIoT/current/ap-southeast-1/index.csv> | 2026-05-28T20:49:00Z |
| CloudHSM | <https://pricing.us-east-1.amazonaws.com/offers/v1.0/aws/CloudHSM/current/ap-southeast-1/index.csv> | 2026-04-16T19:05:28Z |
| AWSCertificateManager | <https://pricing.us-east-1.amazonaws.com/offers/v1.0/aws/AWSCertificateManager/current/ap-southeast-1/index.csv> | 2025-08-28T15:37:21Z |
| ACM | <https://pricing.us-east-1.amazonaws.com/offers/v1.0/aws/ACM/current/ap-southeast-1/index.csv> | 2026-02-18T14:13:16Z |
| awskms | <https://pricing.us-east-1.amazonaws.com/offers/v1.0/aws/awskms/current/ap-southeast-1/index.csv> | 2025-08-28T15:39:13Z |
| AWSSecretsManager | <https://pricing.us-east-1.amazonaws.com/offers/v1.0/aws/AWSSecretsManager/current/ap-southeast-1/index.csv> | 2025-08-28T15:38:04Z |
| AWSELB | <https://pricing.us-east-1.amazonaws.com/offers/v1.0/aws/AWSELB/current/ap-southeast-1/index.csv> | 2026-06-19T02:47:06Z |
| AmazonCloudWatch | <https://pricing.us-east-1.amazonaws.com/offers/v1.0/aws/AmazonCloudWatch/current/ap-southeast-1/index.csv> | 2026-06-22T03:16:07Z |
| AmazonVPC | <https://pricing.us-east-1.amazonaws.com/offers/v1.0/aws/AmazonVPC/current/ap-southeast-1/index.csv> | 2026-06-04T17:34:26Z |

Support, identity, and professional-services references were checked against
public AWS pages:

| Area | Public reference | Pricing treatment |
| --- | --- | --- |
| Amazon Cognito pricing | <https://aws.amazon.com/cognito/pricing/> | Use Essentials direct/social MAU pricing as the default planning case; show Plus and SAML/OIDC as sensitivity cases. Checked on 2026-06-12. |
| AWS Support plans | <https://aws.amazon.com/premiumsupport/pricing/> | Add Business Support+ as the default recurring support-plan adder; keep Enterprise and Unified Operations as sensitivity cases. |
| AWS Support plan comparison | <https://aws.amazon.com/premiumsupport/plans/> | Business Support+ is the minimum recommended plan for production workloads and includes technical support access. |
| AWS Support plan end of support | <https://docs.aws.amazon.com/awssupport/latest/user/support-plans-eos.html> | Developer Support, Business Support, and Enterprise On-Ramp are being discontinued on 2027-01-01, so the estimate uses Business Support+ naming. |
| AWS IQ end of support | <https://docs.aws.amazon.com/aws-iq/> | Do not use AWS IQ as a future consulting-cost source; AWS IQ ended on 2026-05-28. |
| AWS Marketplace Professional Services | <https://docs.aws.amazon.com/marketplace/latest/userguide/proserv-products.html> | Treat consulting as a quote/private-offer item, not as public fixed unit pricing. |
| AWS IoT Core pricing | <https://aws.amazon.com/iot-core/pricing/> | Cross-check the 100,000-device workload shape for connectivity, messaging, Device Shadow, and Rules Engine pricing. Checked on 2026-06-22. |
| AWS IoT Device Management pricing | <https://aws.amazon.com/iot-device-management/pricing/> | Add fleet-management cost for 100,000 devices: managed integration subscription plus fleet indexing; jobs and secure tunneling remain usage adders. Checked on 2026-06-22. |
| Amazon Managed Service for Prometheus pricing | <https://aws.amazon.com/prometheus/pricing/> | Add Prometheus-compatible metrics cost based on samples ingested, collector hours/samples, storage, and query samples. Checked on 2026-06-22. |
| AWS Lambda pricing | <https://aws.amazon.com/lambda/pricing/> | Cross-check request and GB-second rates for AWS-native application API/workers. Checked on 2026-06-22. |

## Selected Unit Prices

| Area | AWS service | Unit price used |
| --- | --- | --- |
| Container compute | ECS Fargate Linux/x86 vCPU | 0.05056 USD per vCPU-hour |
| Container memory | ECS Fargate Linux/x86 memory | 0.00553 USD per GB-hour |
| Frontend CDN egress | CloudFront Asia Pacific outbound, first 10 TB/month | 0.120 USD per GB |
| Frontend CDN requests | CloudFront Asia Pacific proxy HTTPS requests | 0.012 USD per 10,000 requests |
| Frontend dynamic requests | AWS Lambda requests, Singapore | 0.0000002 USD per request |
| Frontend dynamic compute | AWS Lambda x86 duration tier 1, Singapore | 0.0000166667 USD per GB-second |
| Application API requests | AWS Lambda requests | 0.20 USD per 1,000,000 requests |
| Application API compute | AWS Lambda duration | 0.0000166667 USD per GB-second |
| TURN host assumption | EC2 `t4g.small` Linux on demand | 0.0212 USD per instance-hour |
| Database compute | RDS PostgreSQL `db.r7g.xlarge`, Single-AZ | 0.574 USD per DB-hour |
| Database compute robust | RDS PostgreSQL `db.r7g.xlarge`, Multi-AZ | 1.148 USD per DB-hour |
| Database storage | RDS PostgreSQL gp2/gp3 storage, Single-AZ | 0.138 USD per GB-month |
| Database storage robust | RDS PostgreSQL gp2/gp3 storage, Multi-AZ | 0.276 USD per GB-month |
| Cache | ElastiCache for Valkey `cache.t4g.small` | 0.0384 USD per node-hour |
| Object storage | S3 Standard, first 50 TB | 0.025 USD per GB-month |
| Object requests | S3 PUT/COPY/POST/LIST | 0.000005 USD per request |
| IoT connection | AWS IoT Core connection minutes | 0.096 USD per million minutes |
| IoT messaging | AWS IoT Core 5 KB-metered messages, first 1B/month | 1.20 USD per million metered messages |
| IoT state | AWS IoT Device Shadow/Registry operations | 1.50 USD per million operations |
| Device management subscription | AWS IoT Device Management managed integrations, direct-connected device | 0.01 USD per device-month |
| Device management indexing | AWS IoT Device Management fleet indexing updates | 2.25 USD per million index updates |
| Prometheus ingestion | Amazon Managed Service for Prometheus samples ingested | 0.90 USD per 10,000,000 samples for first 2B samples |
| Prometheus collector hours | Amazon Managed Service for Prometheus collector | 0.04 USD per collector-hour |
| Prometheus collector samples | Amazon Managed Service for Prometheus collector samples | 0.03 USD per 10,000,000 samples collected |
| Prometheus query samples | Amazon Managed Service for Prometheus query samples processed | 0.10 USD per billion samples processed |
| Prometheus storage | Amazon Managed Service for Prometheus metrics stored | 0.03 USD per GB-month |
| User authentication | Amazon Cognito Essentials direct/social sign-in | 10,000 MAUs free tier per month; 0.015 USD per MAU above free tier |
| User authentication sensitivity | Amazon Cognito Plus direct/social sign-in | No free tier; 0.020 USD per MAU in first pricing tier |
| Federated user sensitivity | Amazon Cognito SAML/OIDC federation | 50 MAUs free tier per month; 0.015 USD per MAU above free tier |
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
| HSM | CloudHSM usage, Singapore | 3.20 USD per HSM-hour |
| Private PKI | ACM Private CA general-purpose CA | 400 USD per CA-month |
| Private PKI | ACM Private CA short-lived CA | 50 USD per CA-month |
| Private certificate issuance | ACM Private CA general-purpose certs, first 1,000 | 0.75 USD per certificate |
| Private certificate issuance | ACM Private CA general-purpose certs, next 1,001-10,000 | 0.35 USD per certificate |
| Private certificate issuance | ACM Private CA short-lived certs | 0.058 USD per certificate |
| Revocation evidence | ACM Private CA OCSP query | 0.20 USD per 100,000 queries |
| Support plan | AWS Business Support+ | Greater of 29 USD/month per account or tiered percentage of monthly AWS charges: 9% up to 10k, 7% from 10k to 80k, 5% from 80k to 250k, 3% over 250k |
| Support plan sensitivity | AWS Enterprise Support | Greater of 5,000 USD/month or tiered percentage of monthly AWS charges |
| Launch support sensitivity | AWS Countdown Premium | 10,000 USD per project per month for Business Support+ and Enterprise Support customers; included with Unified Operations |

## Support And Consulting Treatment

The estimate treats AWS technical support as a recurring support-plan line item,
not as a cost per ticket. For the current commercial-scale scenarios, Business
Support+ is the default support adder because it is the AWS-recommended minimum
plan for production workloads. Current scenarios are below 10,000 USD/month and
use the first 9% tier.

| Scenario basis | Gross monthly AWS charges | Business Support+ calculation | Monthly support estimate |
| --- | ---: | --- | ---: |
| Base services only, excluding CloudHSM | 4,463.52 | max(29, 4,463.52 * 9%) | 401.72 |
| Default estimate with one CloudHSM | 6,799.52 | max(29, 6,799.52 * 9%) | 611.96 |
| Robust redundant design, excluding CloudHSM | 5,271.57 | max(29, 5,271.57 * 9%) | 474.44 |
| Robust redundant design with two CloudHSMs | 9,943.57 | max(29, 9,943.57 * 9%) | 894.92 |

Enterprise Support remains an optional sensitivity case. Its 5,000 USD/month
minimum is larger than the current pilot infrastructure estimates, so it should
be added only when designated TAM coverage or business-critical escalation is a
project requirement.

AWS Marketplace Professional Services can cover consulting, migration, support,
managed services, or training work through negotiated private offers. Because
Marketplace Professional Services pricing is quote-based and scope-specific, it
is tracked in the worksheet as `quote_required` rather than folded into the
recurring infrastructure baseline.

## Rough Estimate

The calculation below uses the commercial-scale worksheet assumptions requested
for 10,000 users and 100,000 registered devices:

| Assumption | Value |
| --- | --- |
| End users | 10,000 |
| Devices per user | 10 |
| Registered devices | 100,000 |
| Average connected MQTT devices | 100,000 |
| Camera-capable devices | 0 in first estimate; camera/WebRTC profile excluded |
| Runtime model | AWS-native managed-service profile: Lambda/API Gateway-style application APIs/workers, AWS IoT Core, AWS IoT Device Management, CloudWatch, RDS, and Managed Prometheus; residual long-running services still need ECS Fargate or equivalent runtime until refactored; coturn/TURN excluded until camera/WebRTC profile is enabled |
| Database model | Single-AZ RDS PostgreSQL `db.r7g.xlarge` server for account and MQTT/device metadata; robust profile can move the same class to Multi-AZ. Video/WebRTC/TURN media remains excluded. |
| Key and certificate model | Local/CI PKCS#11 validation uses SoftHSM2; production estimate assumes one CloudHSM plus CloudHSM-backed certissuer; ACM Private CA excluded from default estimate; OpenHSM is not assumed |
| NAT assumption | One NAT Gateway, 200 GB processed per month |
| Availability posture | Single-region pilot, production-like but not full multi-region HA |

The baseline uses 100% average connected MQTT devices because no credible
general public benchmark was found for a 30% online ratio, and AWS IoT pricing
examples commonly model connected-device fleets as continuously connected. Use
measured device telemetry to reduce this assumption later.

| Cost area | Monthly estimate | Notes |
| --- | ---: | --- |
| ECS Fargate application services | 539.79 | Account Manager, Video Cloud, Admin BFF, Client/backends, MQTT/logger bridge, cert issuer, API adapters, workers; public frontend Fargate removed. |
| AWS Lambda application APIs/workers | 106.00 | AWS-native account/device/admin/API and light worker profile: 30.0M invocations/month at 1 GB and 200 ms average duration. This is a planning replacement/add-on for refactored user/device management APIs, not proof that all current long-running Go services can run unchanged in Lambda. |
| AI-assisted operations | 0.00 | Operation workflow assumes AI assistance for runbook execution, log triage, incident summary, and status-report drafting. This is not counted as AWS recurring infrastructure; add Bedrock or external AI seat/token cost separately if selected. |
| Public frontend CloudFront CDN | 48.29 | 400 GB CloudFront egress plus 240,000 HTTPS requests/month. |
| Public frontend Lambda | 0.25 | 240,000 requests/month at 256 MB and 200 ms average duration. |
| Public frontend S3 static origin | 0.03 | 1 GB static asset storage and small deployment PUT allowance. |
| Amazon Cognito User Pools | 0.00 | 10,000 MAUs with 10,000 free MAUs: 0 billable MAUs * 0.015 USD/MAU; SMS, SES, M2M token requests, and SAML/OIDC federation are not included. |
| RDS PostgreSQL | 557.02 | One shared Single-AZ `db.r7g.xlarge` DB server plus 1,000 GB account/device metadata storage; logs go to CloudWatch. Video/WebRTC/TURN media is excluded. |
| ElastiCache for Valkey | 28.03 | One non-redundant `cache.t4g.small` node for the original Redis-compatible cache. |
| S3 storage and PUT requests | 67.80 | Firmware binaries, backups, CI/release artifacts, and non-camera object storage scaled to the 100,000-device commercial case; camera snapshots excluded. |
| AWS IoT Core | 1,649.52 | Connection minutes, MQTT messages, and Device Shadow operations for 100,000 usually-online devices. |
| AWS IoT Device Management | 1,135.00 | 100,000 managed direct-connected devices * 0.01 USD/device-month plus 60.0M fleet index updates * 2.25 USD/M. Bulk registration is one-time; jobs, commands, and secure tunneling remain usage adders. |
| Amazon Managed Service for Prometheus | 69.80 | 432.0M samples/month plus one collector, light query load, and small storage allowance. |
| Application Load Balancer | 24.24 | One ALB and one LCU assumption. |
| NAT Gateway | 161.07 | One gateway plus 2,000 GB data processed. |
| EC2 TURN assumption | 0.00 | Camera/WebRTC profile excluded from first estimate. |
| CloudWatch Logs | 48.18 | 66.0 GB/month ingestion plus 30-day retention: 30.0 GB service logs plus 36.0 GB device runtime logs. |
| Secrets Manager | 20.50 | 50 secrets plus 100,000 API calls. |
| KMS | 8.00 | Five customer managed keys plus 1,000,000 requests. |
| Base subtotal before HSM/Private CA | 4,463.52 | Application, larger Single-AZ RDS, cache, storage, MQTT, device management, managed metrics, logging, Cognito, basic network, frontend hosting, and key API surface; camera/WebRTC excluded. |
| CloudHSM | 2,336.00 | One HSM running 730 hours/month at 3.20 USD/HSM-hour; no HSM redundancy assumed for early stage. |
| AWS Business Support+ | 611.96 | Default support-plan adder for the one-CloudHSM scenario, calculated as 9% of 6,799.52 USD gross monthly AWS charges. |
| ACM Private CA | 0.00 | Excluded from default estimate because certificates are signed by CloudHSM-backed certissuer. |

Frontend calculation:

| Item | Calculation | Monthly estimate |
| --- | --- | ---: |
| CloudFront data transfer out | 400 GB * 0.120 USD/GB | 48.00 |
| CloudFront HTTPS requests | 8,000 hits/day * 30 days * 0.0000012 USD/request | 0.29 |
| Lambda requests | 8,000 hits/day * 30 days * 0.0000002 USD/request | 0.05 |
| Lambda duration | 240,000 requests * 0.256 GB * 0.2 seconds * 0.0000166667 USD/GB-second | 0.20 |
| S3 static origin | 1 GB storage plus small deployment PUT allowance | 0.03 |

AWS IoT Core calculation:

| Item | Calculation | Monthly estimate |
| --- | --- | ---: |
| Connection minutes | 100,000 devices * 24 * 60 * 30 * 0.096 USD / 1M minutes | 414.72 |
| Telemetry/status messages | 100,000 devices * 12/hour * 24 * 30 * ceil(1 KB / 5 KB) * 1.20 USD / 1M metered messages | 1,036.80 |
| Downlink command messages | 100,000 devices * 1/day * 30 * ceil(1 KB / 5 KB) * 1.20 USD / 1M metered messages | 3.60 |
| Shadow update messages | 100,000 devices * 1/hour * 24 * 30 * ceil(1 KB / 5 KB) * 1.20 USD / 1M metered messages | 86.40 |
| Shadow operations | 72.0M operations * 1.50 USD / 1M operations | 108.00 |

AWS-native application and fleet-management calculation:

| Item | Calculation | Monthly estimate |
| --- | --- | ---: |
| Lambda application API requests | 30.0M account/device/admin/API invocations * 0.20 USD / 1M requests | 6.00 |
| Lambda application API duration | 30.0M invocations * 1 GB * 0.2 seconds * 0.0000166667 USD/GB-second | 100.00 |
| AI-assisted operations | Runbook/log triage/status report/incident summary assisted by AI tools | 0.00 AWS infra |
| IoT Device Management subscription | 100,000 direct-connected managed devices * 0.01 USD/device-month | 1,000.00 |
| IoT Device Management fleet indexing | 100,000 devices * 300 monthly updates * 2 KB-metered updates * 2.25 USD / 1M updates | 135.00 |

Managed Prometheus calculation:

| Item | Calculation | Monthly estimate |
| --- | --- | ---: |
| Metric sample ingestion | 432.0M samples/month * 0.90 USD / 10M samples | 38.88 |
| Collector hours | 1 collector * 730 hours * 0.04 USD/collector-hour | 29.20 |
| Collector samples | 432.0M samples collected * 0.03 USD / 10M samples | 1.30 |
| Query samples and storage allowance | 4.0B query samples * 0.10 USD / 1B plus small metrics storage | 0.42 |

| Scenario | Estimated monthly cost |
| --- | ---: |
| Base services only, excluding CloudHSM | 4,463.52 USD |
| Base services plus Business Support+ | 4,865.24 USD |
| Default estimate with one CloudHSM and self-managed certissuer | 6,799.52 USD |
| Default estimate with one CloudHSM plus Business Support+ | 7,411.48 USD |
| Robust redundant design, excluding CloudHSM | 5,271.57 USD |
| Robust redundant design excluding CloudHSM plus Business Support+ | 5,746.01 USD |
| Robust redundant design with two CloudHSMs | 9,943.57 USD |
| Robust redundant design with two CloudHSMs plus Business Support+ | 10,838.49 USD |

Per-unit calculation:

The raw per-user and per-device rows below are alternate views of the same
monthly cost pool. Do not add them together.

| Scenario | Calculation | Estimate |
| --- | --- | ---: |
| Base services per user | 4,463.52 USD / 10,000 users | 0.45 USD/user-month |
| Base services per device | 4,463.52 USD / 100,000 devices | 0.04 USD/device-month |
| Default with CloudHSM per user | 6,799.52 USD / 10,000 users | 0.68 USD/user-month |
| Default with CloudHSM per device | 6,799.52 USD / 100,000 devices | 0.07 USD/device-month |
| Default with CloudHSM and Business Support+ per user | 7,411.48 USD / 10,000 users | 0.74 USD/user-month |
| Default with CloudHSM and Business Support+ per device | 7,411.48 USD / 100,000 devices | 0.07 USD/device-month |
| Robust with CloudHSM per user | 9,943.57 USD / 10,000 users | 0.99 USD/user-month |
| Robust with CloudHSM per device | 9,943.57 USD / 100,000 devices | 0.10 USD/device-month |
| Robust with CloudHSM and Business Support+ per user | 10,838.49 USD / 10,000 users | 1.08 USD/user-month |
| Robust with CloudHSM and Business Support+ per device | 10,838.49 USD / 100,000 devices | 0.11 USD/device-month |

Weighted allocation model:

Default allocation uses a device-dominant split because most RTK Cloud cost
drivers scale with device fleet size: MQTT connection minutes, messages, shadow
operations, device logs, firmware delivery, storage, certificates, and device
API traffic. A 10% user pool is kept for account/app/admin/audit/session costs.

| Allocation item | Weight | Rationale |
| --- | ---: | --- |
| User pool | 10% | Account, auth/session, app/API, admin, audit, reporting, and user-driven support surfaces. |
| Device pool | 90% | MQTT, shadow, telemetry/logs, certificates, firmware, storage, and device API workload. |
| Device-heavy sensitivity case | 5% user / 95% device | Use only when modeling a fleet-first deployment with minimal user/app activity. |

| Scenario | User pool | Device pool | Per user | Per device | Effective 1 user + 10 devices |
| --- | ---: | ---: | ---: | ---: | ---: |
| Base services only, excluding CloudHSM | 446.35 | 4,017.17 | 0.04 USD/user-month | 0.04 USD/device-month | 0.45 USD/month |
| Default estimate with one CloudHSM | 679.95 | 6,119.57 | 0.07 USD/user-month | 0.06 USD/device-month | 0.68 USD/month |
| Default estimate with one CloudHSM plus Business Support+ | 741.15 | 6,670.33 | 0.07 USD/user-month | 0.07 USD/device-month | 0.74 USD/month |
| Robust redundant design with two CloudHSMs | 994.36 | 8,949.21 | 0.10 USD/user-month | 0.09 USD/device-month | 0.99 USD/month |
| Robust redundant design with two CloudHSMs plus Business Support+ | 1,083.85 | 9,754.64 | 0.11 USD/user-month | 0.10 USD/device-month | 1.08 USD/month |

Cognito sensitivity:

| Scenario | Calculation | Monthly estimate |
| --- | --- | ---: |
| Essentials direct/social sign-in, 10,000 MAUs | max(0, 10,000 MAUs - 10,000 free MAUs) * 0.015 USD/MAU | 0.00 |
| Essentials direct/social sign-in, 25,000 MAUs | max(0, 25,000 MAUs - 10,000 free MAUs) * 0.015 USD/MAU | 225.00 |
| Essentials direct/social sign-in, 100,000 MAUs | max(0, 100,000 MAUs - 10,000 free MAUs) * 0.015 USD/MAU | 1,350.00 |
| Plus direct/social sign-in, 25,000 MAUs | 25,000 MAUs * 0.020 USD/MAU | 500.00 |
| SAML/OIDC federation, 25,000 MAUs | max(0, 25,000 MAUs - 50 free MAUs) * 0.015 USD/MAU | 374.25 |

The default estimate uses Cognito Essentials direct/social sign-in as a planning
assumption. If enterprise SAML/OIDC federation, Plus threat-protection features,
SMS MFA, SES email volume, machine-to-machine token requests, or higher API RPS
quota are required, add the corresponding Cognito/SNS/SES adders.

Robust-profile changes:

| Area | Baseline | Robust profile |
| --- | --- | --- |
| CloudHSM | 1 HSM | 2 HSMs |
| RDS PostgreSQL | Single-AZ `db.r7g.xlarge` larger initial DB | Multi-AZ `db.r7g.xlarge` optional robust posture |
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
| RDS PostgreSQL | 557.02 | 1,114.04 | 557.02 |
| ElastiCache for Valkey | 28.03 | 56.06 | 28.03 |
| NAT Gateway | 161.07 | 204.14 | 43.07 |
| CloudHSM | 2,336.00 | 4,672.00 | 2,336.00 |
| AWS Business Support+ | 611.96 | 894.92 | 282.96 |
| Other baseline items | 3,177.61 | 3,177.61 | 0.00 |
| Total with CloudHSM and Business Support+ | 7,411.48 | 10,838.49 | 3,427.01 |

Robust cost behavior:

| Behavior | Items | Reason |
| --- | --- | --- |
| Doubled | CloudHSM, RDS estimate, ElastiCache | These are single-instance or stateful components where the robust profile adds a second node/standby. |
| Partially increased | NAT Gateway, ECS Fargate backend services | NAT gateway-hours double but data processing stays the same; only selected worker/certissuer tasks are duplicated. |
| Increased by percentage of gross AWS charges | AWS Business Support+ | Support is calculated from monthly AWS charges, so robust infrastructure increases the support-plan adder. |
| Unchanged | AWS IoT Core, AWS IoT Device Management, Managed Prometheus, CloudWatch Logs, ALB, frontend CDN/Lambda/S3, Cognito, Secrets Manager, KMS, S3 storage | Product traffic, log volume, user count, request volume, and metrics volume are unchanged between baseline and robust profiles. |

Top 10 monthly cost items:

| Rank | Cost item | Monthly estimate |
| ---: | --- | ---: |
| 1 | CloudHSM, 1 HSM | 2,336.00 |
| 2 | AWS IoT Core MQTT plus Shadow | 1,649.52 |
| 3 | AWS IoT Device Management | 1,135.00 |
| 4 | AWS Business Support+ | 611.96 |
| 5 | RDS PostgreSQL, Single-AZ `db.r7g.xlarge` plus storage | 557.02 |
| 6 | ECS Fargate backend services | 539.79 |
| 7 | NAT Gateway | 161.07 |
| 8 | AWS Lambda application APIs/workers | 106.00 |
| 9 | Amazon Managed Service for Prometheus | 69.80 |
| 10 | S3 storage and PUT requests | 67.80 |

Robust top 10 monthly cost items:

| Rank | Cost item | Monthly estimate |
| ---: | --- | ---: |
| 1 | CloudHSM, 2 HSMs | 4,672.00 |
| 2 | AWS IoT Core MQTT plus Shadow | 1,649.52 |
| 3 | AWS IoT Device Management | 1,135.00 |
| 4 | RDS PostgreSQL, Multi-AZ `db.r7g.xlarge` plus storage | 1,114.04 |
| 5 | AWS Business Support+ | 894.92 |
| 6 | ECS Fargate backend services | 719.72 |
| 7 | NAT Gateway, 2 gateways | 204.14 |
| 8 | AWS Lambda application APIs/workers | 106.00 |
| 9 | Amazon Managed Service for Prometheus | 69.80 |
| 10 | S3 storage and PUT requests | 67.80 |

## Service Set

| Current capability | AWS service candidate |
| --- | --- |
| Public frontend | S3 origin plus CloudFront, with Lambda for lightweight dynamic routes if needed |
| HTTP APIs and backend service runtime | AWS Lambda/API Gateway-style profile for refactored account/device/admin APIs; ECS Fargate or equivalent remains for long-running services until the handlers are migrated |
| Relational persistence | Amazon RDS for PostgreSQL |
| Redis-compatible cache | Amazon ElastiCache for Valkey |
| MQTT broker, device state, and fleet management | AWS IoT Core plus Device Shadow and AWS IoT Device Management |
| Object artifacts, snapshots, firmware, backups | Amazon S3 |
| Device/app key protection | AWS CloudHSM, AWS KMS, AWS Secrets Manager |
| Device/app certificate authority | CloudHSM-backed certissuer; AWS ACM Private CA excluded unless choosing an AWS-managed CA profile |
| Public TLS certificates | ACM public certificates for AWS-integrated endpoints; exportable public certificates only if required |
| Runtime logs and operational metrics | Amazon CloudWatch Logs, CloudWatch metrics/alarms, and Amazon Managed Service for Prometheus |
| Background jobs and async events | SQS/EventBridge or ECS workers; exact split still needs architecture choice |
| TURN relay | Excluded from first estimate; add EC2 or ECS-on-EC2 running coturn when camera/WebRTC profile is enabled |
| DNS | Route 53 |
| Private networking | VPC subnets, security groups, NAT Gateway, VPC endpoints where cost-effective |
| Technical support | AWS Business Support+ as default production support-plan adder; Enterprise Support and Countdown Premium as optional sensitivity items |

## Cost Drivers

CloudHSM dominates the default estimate. The base application and data plane is
roughly 1.1k USD/month under the pilot assumptions, while one CloudHSM adds
2,336.00 USD/month. Adding a second CloudHSM later would add another
2,336.00 USD/month at the current collected unit price.

AWS Business Support+ becomes a material recurring adder once it is included.
For the current one-CloudHSM default estimate, it adds 611.96 USD/month. For the
robust two-CloudHSM estimate, it adds 894.92 USD/month. Enterprise Support is a
separate budget decision because its 5,000 USD/month minimum exceeds the current
pilot infrastructure total.

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
| Amazon Managed Grafana | Managed Prometheus is now priced, but Grafana users and dashboard costs still need a final operations decision. |
| RDS Multi-AZ, replicas, Aurora, or split DB instances | Pilot estimate uses one shared Single-AZ RDS server; production HA or isolation will cost more. |
| VPC endpoints | Can reduce NAT traffic but add hourly and data-processing charges. |
| WAF/Shield | Not included in the first-pass security perimeter cost. |
| Professional services consulting | Marketplace Professional Services and AWS partner consulting are quote/private-offer items and need a scoped proposal before budget approval. |
| Committed-use discounts | Savings Plans, Reserved Instances, and enterprise discounts may reduce compute/database cost. |
