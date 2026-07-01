# AWS Review Adjusted Cost Model

Status: Derived planning note
Source pricing snapshot: `aws-pricing-sources.md`
AWS review source: `../../aws_report/Realtek_IoT_Cost_Review_Reply_v1.10 - 20260630.pdf`
Review date: 2026-06-30

This note records the adjusted AWS managed-service estimate after reviewing AWS
feedback and Realtek architecture clarification. It does not replace the source
pricing snapshot. It is the derived cost model used by the status-report PPTX.

## Authority And Update Flow

- Public unit prices and the original first-pass estimate live in
  `aws-pricing-sources.md`.
- AWS-team feedback lives in
  `../../aws_report/Realtek_IoT_Cost_Review_Reply_v1.10 - 20260630.pdf`.
- Realtek architecture clarifications are captured in this file as derived
  adjustments, not by rewriting the original pricing snapshot.
- The status-report generator reads the pricing snapshot plus this derived
  model through `tools/status-report/report_model.py`.
- The generated PPTX under `.artifacts/status-reports/` is an output. If a cost
  number changes, update this derived model or the source snapshot first, then
  regenerate the deck.

## Architecture Clarifications

| Area | Clarification |
| --- | --- |
| CloudHSM | CloudHSM is only relevant to device certificate issuance / CA signing. Device certificate validation is handled elsewhere, so always-on CloudHSM is not part of the default runtime cost. |
| Certificate authority | Default planning option is ACM Private CA or hybrid offline CloudHSM Root CA plus ACM Private CA. |
| Telemetry | High-volume telemetry is not written directly to RDS and does not trigger Lambda per message. |
| K8S telemetry path | K8S self-managed profile uses Redis for hot state and Loki/Grafana/Prometheus for logs and observability. |
| AWS telemetry path | AWS managed-service profile uses IoT Core Basic Ingest / Rules, queue or worker fan-out, CloudWatch Logs, and/or S3/Athena-style storage. |
| RDS / Aurora | Operational metadata only: user profile, device registry/lifecycle, metadata, and selected offline sync. |
| Lambda | 30M invocations/month remains an API/control-plane planning assumption, not per-message MQTT telemetry processing. |
| Video/WebRTC/TURN | Excluded from this estimate. |

## Revised Cost Inputs

| Cost item | Original monthly | Revised monthly | Delta | Adjustment |
| --- | ---: | ---: | ---: | --- |
| CloudHSM / CA signing | 2,336.00 | 433.00 | -1,903.00 | Replace always-on CloudHSM with ACM PCA / hybrid offline Root CA option. |
| AWS IoT Core | 1,649.52 | 924.00 | -725.52 | Use Basic Ingest as default for telemetry topics that do not require app-side MQTT subscription; include Rules Engine effect. |
| AWS IoT Device Management | 1,135.00 | 135.00 | -1,000.00 | Remove Managed Integrations; keep Fleet Indexing only. |
| RDS / telemetry storage | 557.02 | 384.00 | -173.02 | Move high-volume telemetry/logs out of RDS; keep operational DB plus S3/Firehose-style telemetry storage placeholder. |
| ECS Fargate + Lambda | 645.79 | 645.79 | 0.00 | Keep unchanged until Fargate/Lambda workload split is finalized. |

## Revised Scenario Totals

| Scenario | Monthly estimate |
| --- | ---: |
| Revised infra base, excluding CA and support | 2,676.12 |
| Revised default with ACM PCA / hybrid CA | 3,109.12 |
| Revised default with ACM PCA / hybrid CA plus Business Support+ | 3,388.94 |
| Revised robust, excluding CA and support | 3,484.17 |
| Revised robust with ACM PCA / hybrid CA | 3,917.17 |
| Revised robust with ACM PCA / hybrid CA plus Business Support+ | 4,269.72 |

## Unit Cost

| Basis | Calculation | Estimate |
| --- | --- | ---: |
| Budget headline per device | 3,388.94 / 100,000 devices | 0.034 USD/device-month |
| Budget headline per user | 3,388.94 / 5,000 users | 0.68 USD/user-month |
| Infra-only per device | 2,676.12 / 100,000 devices | 0.027 USD/device-month |
| Infra-only per user | 2,676.12 / 5,000 users | 0.54 USD/user-month |

## Caveats

- Basic Ingest assumes high-volume telemetry topics do not require app-side MQTT
  subscription, retained message behavior, or broker-side topic fan-out.
- ACM PCA / hybrid CA cost uses the AWS review estimate of about 433 USD/month,
  including amortized 100K certificate issuance.
- RDS/Aurora and S3/Athena split remains a planning placeholder until retention,
  query pattern, and offline sync requirements are confirmed.
- The values are planning estimates, not AWS actual billing.
