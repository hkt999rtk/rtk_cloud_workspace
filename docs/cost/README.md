# RTK Cloud Cost Materials

Status: Index
Owner: `rtk_cloud_workspace`

This directory keeps cloud cost-estimation inputs together so pricing
assumptions, service mappings, support-plan adders, and Linode scale estimates
can be reviewed as one package.

| Document | Classification | Purpose |
| --- | --- | --- |
| [aws-service-mapping.md](aws-service-mapping.md) | Supporting note | Maps current RTK Cloud private-cloud components to AWS service candidates and cost drivers. |
| [aws-cost-estimate-worksheet.csv](aws-cost-estimate-worksheet.csv) | Supporting artifact | Quantity-first worksheet for the 10k-device `ap-southeast-1` commercial pilot and robust profile. |
| [aws-pricing-sources.md](aws-pricing-sources.md) | Supporting note | Public AWS pricing snapshot, support-plan references, rough monthly estimate, and scenario totals. |
| [linode-100k-estimate.md](linode-100k-estimate.md) | Supporting note | Linode/Akamai Cloud self-managed cluster estimate for 25,000 users and 100,000 usually-online MQTT devices. |

## Current Estimate Scope

- AWS region: `ap-southeast-1` (Asia Pacific, Singapore).
- Linode/Akamai Cloud region: `us-sea` planning profile.
- Currency: USD.
- Baseline fleet for the latest report: 25,000 users, 4 devices per user,
  100,000 registered devices.
- Camera/WebRTC/TURN relay: excluded from the first estimate.
- Default support adder: AWS Business Support+ using the public monthly support
  fee formula.

## Update Rules

- Re-check public AWS pricing before changing numeric assumptions.
- Keep one source URL or official reference for each priced service family.
- Keep quote-only services, such as AWS Marketplace Professional Services,
  separate from baseline recurring infrastructure cost.
- Validate CSV syntax after editing the worksheet.

## Future Automation References

| Use case | Official AWS source |
| --- | --- |
| Export a reviewed estimate from AWS Pricing Calculator | <https://docs.aws.amazon.com/pricing-calculator/latest/userguide/export-estimate.html> |
| Query actual AWS account spend after workloads run on AWS | <https://docs.aws.amazon.com/aws-cost-management/latest/APIReference/API_GetCostAndUsage.html> |
| Refresh public unit prices for worksheet automation | <https://docs.aws.amazon.com/awsaccountbilling/latest/aboutv2/using-price-list-query-api.html> |
