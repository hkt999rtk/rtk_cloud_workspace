# AWS Pricing Snapshot For Cost Estimation

Status: Supporting note
Region: `ap-southeast-1` (Asia Pacific, Singapore)
Currency: USD
Collected: 2026-06-06T06:05:06Z
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
| IoT messaging | AWS IoT Core 5 KB-metered messages, first 1B/month | 1.20 USD per million metered messages |
| IoT state | AWS IoT Device Shadow/Registry operations | 1.50 USD per million operations |
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
| HSM | CloudHSM new usage | 1.86 USD per HSM-hour |
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
plan for production workloads and the existing monthly usage is below the first
10,000 USD support-pricing tier.

| Scenario basis | Gross monthly AWS charges | Business Support+ calculation | Monthly support estimate |
| --- | ---: | --- | ---: |
| Base services only, excluding CloudHSM | 3,088.89 | max(29, 3,088.89 * 9%) | 278.00 |
| Default estimate with one CloudHSM | 4,446.69 | max(29, 4,446.69 * 9%) | 400.20 |
| Robust redundant design, excluding CloudHSM | 3,833.11 | max(29, 3,833.11 * 9%) | 344.98 |
| Robust redundant design with two CloudHSMs | 6,548.71 | max(29, 6,548.71 * 9%) | 589.38 |

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
| Runtime model | S3/CloudFront plus optional Lambda for public frontend, ECS Fargate for backend services; coturn/TURN excluded until camera/WebRTC profile is enabled |
| Database model | One shared Single-AZ RDS PostgreSQL `db.t4g.large` server for account and video schemas |
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
| Public frontend CloudFront CDN | 48.29 | 400 GB CloudFront egress plus 240,000 HTTPS requests/month. |
| Public frontend Lambda | 0.25 | 240,000 requests/month at 256 MB and 200 ms average duration. |
| Public frontend S3 static origin | 0.03 | 1 GB static asset storage and small deployment PUT allowance. |
| Amazon Cognito User Pools | 0.00 | 10,000 MAUs with 10,000 free MAUs: 0 billable MAUs * 0.015 USD/MAU; SMS, SES, M2M token requests, and SAML/OIDC federation are not included. |
| RDS PostgreSQL | 493.19 | One shared `db.t4g.large` DB server plus 2,500 GB account/video storage; logs go to CloudWatch. |
| ElastiCache for Valkey | 28.03 | One non-redundant `cache.t4g.small` node for the original Redis-compatible cache. |
| S3 storage and PUT requests | 67.80 | Firmware binaries, backups, CI/release artifacts, and non-camera object storage scaled to the 100,000-device commercial case; camera snapshots excluded. |
| AWS IoT Core | 1,649.52 | Connection minutes, MQTT messages, and Device Shadow operations for 100,000 usually-online devices. |
| Application Load Balancer | 24.24 | One ALB and one LCU assumption. |
| NAT Gateway | 161.07 | One gateway plus 2,000 GB data processed. |
| EC2 TURN assumption | 0.00 | Camera/WebRTC profile excluded from first estimate. |
| CloudWatch Logs | 48.18 | 66.0 GB/month ingestion plus 30-day retention: 30.0 GB service logs plus 36.0 GB device runtime logs. |
| Secrets Manager | 20.50 | 50 secrets plus 100,000 API calls. |
| KMS | 8.00 | Five customer managed keys plus 1,000,000 requests. |
| Base subtotal before HSM/Private CA | 3,088.89 | Application, data, cache, storage, MQTT, logging, Cognito, basic network, frontend hosting, and key API surface; camera/WebRTC excluded. |
| CloudHSM | 1,357.80 | One HSM running 730 hours/month; no HSM redundancy assumed for early stage. |
| AWS Business Support+ | 400.20 | Default support-plan adder for the one-CloudHSM scenario, calculated as 9% of 4,446.69 USD gross monthly AWS charges. |
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

| Scenario | Estimated monthly cost |
| --- | ---: |
| Base services only, excluding CloudHSM | 3,088.89 USD |
| Base services plus Business Support+ | 3,366.89 USD |
| Default estimate with one CloudHSM and self-managed certissuer | 4,446.69 USD |
| Default estimate with one CloudHSM plus Business Support+ | 4,846.89 USD |
| Robust redundant design, excluding CloudHSM | 3,833.11 USD |
| Robust redundant design excluding CloudHSM plus Business Support+ | 4,178.09 USD |
| Robust redundant design with two CloudHSMs | 6,548.71 USD |
| Robust redundant design with two CloudHSMs plus Business Support+ | 7,138.09 USD |

Per-unit calculation:

The raw per-user and per-device rows below are alternate views of the same
monthly cost pool. Do not add them together.

| Scenario | Calculation | Estimate |
| --- | --- | ---: |
| Base services per user | 3,088.89 USD / 10,000 users | 0.31 USD/user-month |
| Base services per device | 3,088.89 USD / 100,000 devices | 0.03 USD/device-month |
| Default with CloudHSM per user | 4,446.69 USD / 10,000 users | 0.44 USD/user-month |
| Default with CloudHSM per device | 4,446.69 USD / 100,000 devices | 0.04 USD/device-month |
| Default with CloudHSM and Business Support+ per user | 4,846.89 USD / 10,000 users | 0.48 USD/user-month |
| Default with CloudHSM and Business Support+ per device | 4,846.89 USD / 100,000 devices | 0.05 USD/device-month |
| Robust with CloudHSM per user | 6,548.71 USD / 10,000 users | 0.65 USD/user-month |
| Robust with CloudHSM per device | 6,548.71 USD / 100,000 devices | 0.07 USD/device-month |
| Robust with CloudHSM and Business Support+ per user | 7,138.09 USD / 10,000 users | 0.71 USD/user-month |
| Robust with CloudHSM and Business Support+ per device | 7,138.09 USD / 100,000 devices | 0.07 USD/device-month |

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
| Base services only, excluding CloudHSM | 308.89 | 2,780.00 | 0.03 USD/user-month | 0.03 USD/device-month | 0.31 USD/month |
| Default estimate with one CloudHSM | 444.67 | 4,002.02 | 0.04 USD/user-month | 0.04 USD/device-month | 0.44 USD/month |
| Default estimate with one CloudHSM plus Business Support+ | 484.69 | 4,362.20 | 0.05 USD/user-month | 0.04 USD/device-month | 0.48 USD/month |
| Robust redundant design with two CloudHSMs | 654.87 | 5,893.84 | 0.07 USD/user-month | 0.06 USD/device-month | 0.65 USD/month |
| Robust redundant design with two CloudHSMs plus Business Support+ | 713.81 | 6,424.28 | 0.07 USD/user-month | 0.06 USD/device-month | 0.71 USD/month |

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
| RDS PostgreSQL | 493.19 | 986.38 | 493.19 |
| ElastiCache for Valkey | 28.03 | 56.06 | 28.03 |
| NAT Gateway | 161.07 | 204.14 | 43.07 |
| CloudHSM | 1,357.80 | 2,715.60 | 1,357.80 |
| AWS Business Support+ | 400.20 | 589.38 | 189.18 |
| Other baseline items | 1,866.81 | 1,866.81 | 0.00 |
| Total with CloudHSM and Business Support+ | 4,846.89 | 7,138.09 | 2,291.20 |

Robust cost behavior:

| Behavior | Items | Reason |
| --- | --- | --- |
| Doubled | CloudHSM, RDS estimate, ElastiCache | These are single-instance or stateful components where the robust profile adds a second node/standby. |
| Partially increased | NAT Gateway, ECS Fargate backend services | NAT gateway-hours double but data processing stays the same; only selected worker/certissuer tasks are duplicated. |
| Increased by percentage of gross AWS charges | AWS Business Support+ | Support is calculated from monthly AWS charges, so robust infrastructure increases the support-plan adder. |
| Unchanged | AWS IoT Core, CloudWatch Logs, ALB, frontend CDN/Lambda/S3, Cognito, Secrets Manager, KMS, S3 storage | Product traffic, log volume, user count, and request volume are unchanged between baseline and robust profiles. |

Top 10 monthly cost items:

| Rank | Cost item | Monthly estimate |
| ---: | --- | ---: |
| 1 | AWS IoT Core MQTT plus Shadow | 1,649.52 |
| 2 | CloudHSM, 1 HSM | 1,357.80 |
| 3 | ECS Fargate backend services | 539.79 |
| 4 | RDS PostgreSQL, shared `db.t4g.large` plus storage | 493.19 |
| 5 | AWS Business Support+ | 400.20 |
| 6 | NAT Gateway | 161.07 |
| 7 | S3 storage and PUT requests | 67.80 |
| 8 | Public frontend CloudFront CDN | 48.29 |
| 9 | CloudWatch Logs | 48.18 |
| 10 | ElastiCache for Valkey | 28.03 |

Robust top 10 monthly cost items:

| Rank | Cost item | Monthly estimate |
| ---: | --- | ---: |
| 1 | CloudHSM, 2 HSMs | 2,715.60 |
| 2 | AWS IoT Core MQTT plus Shadow | 1,649.52 |
| 3 | RDS PostgreSQL, Multi-AZ-style estimate | 986.38 |
| 4 | ECS Fargate backend services | 719.72 |
| 5 | AWS Business Support+ | 589.38 |
| 6 | NAT Gateway, 2 gateways | 204.14 |
| 7 | S3 storage and PUT requests | 67.80 |
| 8 | ElastiCache for Valkey, 2 nodes | 56.06 |
| 9 | Public frontend CloudFront CDN | 48.29 |
| 10 | CloudWatch Logs | 48.18 |

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
| Technical support | AWS Business Support+ as default production support-plan adder; Enterprise Support and Countdown Premium as optional sensitivity items |

## Cost Drivers

CloudHSM dominates the default estimate. The base application and data plane is
roughly 1.1k USD/month under the pilot assumptions, while one CloudHSM adds
1,357.80 USD/month. Adding a second CloudHSM later would add another
1,357.80 USD/month at the current collected unit price.

AWS Business Support+ becomes a material recurring adder once it is included.
For the current one-CloudHSM default estimate, it adds 217.91 USD/month. For the
robust two-CloudHSM estimate, it adds 379.14 USD/month. Enterprise Support is a
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
| Managed Prometheus/Grafana | Current observability may stay on CloudWatch or use managed observability services. |
| RDS Multi-AZ, replicas, Aurora, or split DB instances | Pilot estimate uses one shared Single-AZ RDS server; production HA or isolation will cost more. |
| VPC endpoints | Can reduce NAT traffic but add hourly and data-processing charges. |
| WAF/Shield | Not included in the first-pass security perimeter cost. |
| Professional services consulting | Marketplace Professional Services and AWS partner consulting are quote/private-offer items and need a scoped proposal before budget approval. |
| Committed-use discounts | Savings Plans, Reserved Instances, and enterprise discounts may reduce compute/database cost. |
