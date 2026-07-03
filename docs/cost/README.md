# RTK Cloud Cost Materials

Status: Index
Owner: `rtk_cloud_workspace`

This directory keeps cloud cost-estimation inputs together so pricing
assumptions, service mappings, support-plan adders, and Linode scale estimates
can be reviewed as one package.

| Document | Classification | Purpose |
| --- | --- | --- |
| [aws-service-mapping.md](aws-service-mapping.md) | Supporting note | Maps current RTK Cloud private-cloud components to AWS service candidates and cost drivers. |
| [aws-cost-estimate-worksheet.csv](aws-cost-estimate-worksheet.csv) | Supporting artifact | Quantity-first worksheet for the 100,000-device `ap-southeast-1` commercial pilot and robust profile. |
| [aws-pricing-sources.md](aws-pricing-sources.md) | Source pricing snapshot | Public AWS pricing snapshot, support-plan references, original rough monthly estimate, and scenario totals. Keep this as the collected source snapshot unless public pricing is refreshed. |
| [aws-review-adjustments.md](aws-review-adjustments.md) | Derived review model | Applies AWS review feedback and Realtek architecture clarifications on top of the pricing snapshot. This is the current source for review-adjusted AWS totals used by the status-report PPTX. |
| [linode-100k-estimate.md](linode-100k-estimate.md) | Supporting note | K8S self-managed cluster estimate for 5,000 users and 100,000 usually-online MQTT devices. |

## Document Relationship

Use this order when reading or updating the cost model:

1. `aws-service-mapping.md` explains how current RTK Cloud components map to
   possible AWS services and what cost drivers must be collected.
2. `aws-pricing-sources.md` records the original public AWS pricing snapshot and
   first-pass estimate. Do not rewrite it when only the architecture assumption
   changes; create or update a derived adjustment instead.
3. `../../aws_report/Realtek_IoT_Cost_Review_Reply_v1.10 - 20260630.pdf`
   is the internal AWS-team review input. It is not a public pricing source.
4. `aws-review-adjustments.md` combines the source pricing snapshot, AWS review
   feedback, and Realtek clarifications into the review-adjusted estimate.
5. `tools/status-report/report_model.py` reads the pricing snapshot and derived
   adjustment model, recalculates totals, and feeds the PPTX builder.
6. `tools/status-report/build_cloud_status_report_pptx.mjs` renders the report.
   Generated PPTX files under `.artifacts/status-reports/` are outputs, not
   source-of-truth documents.

Current AWS review-adjusted headline:

- `2,676.12 USD/month` revised infra base.
- `3,109.12 USD/month` with ACM PCA / hybrid CA.
- `3,388.94 USD/month` with ACM PCA / hybrid CA plus Business Support+.
- `0.034 USD/device-month` for 100,000 devices.

If these numbers change, update the derived model and regenerate the PPTX from
the builder. Do not hand-edit generated PPTX text as the only record of the
change.

## Current Estimate Scope

- AWS region: `ap-southeast-1` (Asia Pacific, Singapore).
- Linode/Akamai Cloud region: `us-sea` planning profile.
- Currency: USD.
- Baseline fleet for the latest report: 5,000 users, 20 devices per user,
  100,000 registered devices.
- Camera/WebRTC/TURN relay: excluded from the first estimate.
- Default support adder: AWS Business Support+ using the public monthly support
  fee formula.

## Update Rules

- Re-check public AWS pricing before changing source pricing assumptions in
  `aws-pricing-sources.md`.
- Use `aws-review-adjustments.md` for architecture-review corrections that
  preserve the original public pricing snapshot.
- Keep one source URL or official reference for each priced service family.
- Keep quote-only services, such as AWS Marketplace Professional Services,
  separate from baseline recurring infrastructure cost.
- Validate CSV syntax after editing the worksheet.
- Rebuild the status-report PPTX after changing either pricing inputs or the
  derived adjustment model.

## Future Automation References

| Use case | Official AWS source |
| --- | --- |
| Export a reviewed estimate from AWS Pricing Calculator | <https://docs.aws.amazon.com/pricing-calculator/latest/userguide/export-estimate.html> |
| Query actual AWS account spend after workloads run on AWS | <https://docs.aws.amazon.com/aws-cost-management/latest/APIReference/API_GetCostAndUsage.html> |
| Refresh public unit prices for worksheet automation | <https://docs.aws.amazon.com/awsaccountbilling/latest/aboutv2/using-price-list-query-api.html> |
