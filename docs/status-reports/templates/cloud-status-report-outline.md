# Cloud Status Report Outline

Status: template.

Use this outline for weekly Realtek Video / IoT Cloud status reports.
Apply the writing rules in `../guidelines.md` when filling each section.

## Cover / Core Management Message

- Report title and date.
- One management message that states the business reason, current execution
  posture, and resource/operations reality.
- One product-to-KPI visual.

## Part 1: Executive Summary

- One-page conclusion.
- Why this cloud exists.
- Technical results to business KPI chain.
- Loading-test or milestone target.
- Current high-level architecture.
- Current deployment state.
- Completed foundation and next steps.

## Part 2: Cloud / Product / KPI Detail

- Architecture detail.
- Module-to-cloud-to-commercial KPI path.
- KPI framework: technical, product, commercial, operations.
- Security and device trust.
- API/cloud pattern.
- Product features.
- SDK/reference app status.
- Onboarding/provisioning flow.
- Loading-test plan.
- Maintain/operation reality.

## Part 3: Operation Screenshots And Usage Flows

- Admin Fleet Health Overview.
- Admin Devices + Detail Drawer.
- Admin Firmware & OTA.
- Admin Stream Health.
- SDK/sample app screen flow.
- Product/frontend architecture visual if useful for external positioning.

Keep the body selective. Put the full material catalog in the appendix.

## Part 4: Linode Staging Deployment And Configuration

- Public endpoints and current runtime shape.
- Non-secret configuration boundaries.
- Live health check table with timestamp.
- Production-ready gaps.

Allowed configuration detail:

- public HTTPS domains
- non-secret environment variable names
- runtime placement
- persistence category
- reverse proxy/TLS boundary
- evidence command names

Forbidden configuration detail:

- DB DSNs
- JWT/auth secrets
- Linode tokens
- DNS provider credentials
- object storage access keys
- private keys
- bearer tokens
- raw customer data

## Review Checklist

- Executive summary can be understood in five minutes.
- Details match current repo and deployment state.
- Technical work is connected to AmebaPRO/module commercial KPI.
- Operation screenshots prove demo and customer workflow readiness.
- Deployment/configuration status avoids secrets and overclaiming.
- Production-ready gaps are explicit.

## Appendix: Materials And Sources

- Screenshot/material source table.
- Full reusable material directories.
- Internal references and runbooks.
