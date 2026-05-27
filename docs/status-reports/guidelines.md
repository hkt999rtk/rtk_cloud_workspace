# Status Report Writing Guidelines

Status: source.

Owner: `rtk_cloud_workspace`.

These guidelines define how Realtek Video / IoT Cloud status reports should be
written, regardless of where the report is generated. The goal is consistency:
the report should connect engineering progress to product/commercial outcomes,
use reusable evidence, and avoid overclaiming production readiness.

## Audience And Intent

Write for a mixed management, product, and engineering audience.

Every report must answer these questions quickly:

- Why are we doing this cloud work?
- What is working now?
- What can be demonstrated through UI, SDK, API, or deployment evidence?
- What business or product KPI does the work support?
- What remains blocked, risky, or not production-ready?
- What decision or resource implication should management understand?

Do not write the report as a raw engineering changelog. Engineering details
matter only when they explain capability, evidence, risk, or next action.

## Required Narrative

Use this narrative spine in every report:

1. Cloud is part of the AmebaPRO / IoT module product path, not an isolated
   server project.
2. The value chain is module -> SDK/app -> cloud onboarding -> video/OTA/
   telemetry -> admin operations -> customer PoC -> design-in/commercial KPI.
3. Technical status must be translated into observable KPI: deployability,
   online success, SDK integration, OTA success, video setup success, load
   capacity, support effort, and incident response.
4. Operation screenshots should show how a customer, SDK developer, operator,
   or reviewer would actually use the system.
5. Deployment status must separate live staging evidence from production-ready
   claims.

## Section Standards

### Cover / Core Message

- State the one weekly management message in plain language.
- The core message can change every week, but it must always appear on the
  first page of the report before the executive-summary body.
- Treat the core message as an editable weekly input, not as a fixed template
  sentence. Update it before generating the report.
- Immediately after the core message, include a short current-status summary on
  the first page. This status summary is also weekly input and must not become a
  long executive summary.
- Current-status summary format: use a compact table with three columns:
  `面向`, `目前狀態`, and `下一步或風險`. Keep it to three to five rows, one
  sentence per cell, and cover the minimum management scan points: deployment,
  product/demo evidence, operations/readiness, and next milestone or resource
  gap.
- Mention both speed and operations reality: tools can accelerate cloud
  construction, but production maintain, SLA, customer support, monitoring, and
  incident response still need owners and resources.
- Include one product-to-KPI visual.

### Executive Summary

- Keep it readable in five minutes.
- Use bullets and compact tables, not long paragraphs.
- Include a clear "completed foundation / next step" table.
- If the report mentions a milestone such as a loading test, include the target
  number, what will be measured, and why it matters commercially.

### Cloud / Product / KPI Detail

- Use source-of-truth boundaries:
  - Account Manager owns identity, tenant, user, organization, registry, and
    account-side readiness facts.
  - Video Cloud owns runtime activation, device transport, video, OTA,
    telemetry, TURN/WebRTC, and runtime readiness facts.
  - Admin is a dashboard/BFF and evidence aggregator, not the source of truth.
- Describe PKI/mTLS as device trust and enterprise-readiness foundation, not as
  the main commercial headline.
- Mention API/cloud patterns as design alignment, but do not claim equivalence
  with AWS/Azure/GCP unless evidence exists.

### Operation Screenshots

- Prefer existing design assets from submodules over newly invented pictures.
- Body screenshots should be selective: Admin overview, devices/detail,
  firmware/OTA, stream health, SDK/sample flow, and product architecture are
  enough for the standard management report.
- Each screenshot needs a caption and one sentence explaining the operation or
  evidence it supports.
- Put the complete material list in the appendix instead of flooding the body.

### Linode Deployment And Configuration

- Explain what Linode represents in this report: a simpler VM/infrastructure
  service rather than an AWS-style managed-service stack.
- State the portability implication when relevant: PostgreSQL, MQ/message
  queue, broker, storage, reverse proxy, and runtime services are operated by
  us on the VM/service layer instead of depending on AWS-native managed
  architecture, so the deployment model can be moved across AWS, GCP, Azure,
  Alibaba Cloud, and other infrastructure clouds with less vendor lock-in.
- Include public endpoints, live health result, snapshot timestamp, and runtime
  shape.
- Use public HTTPS domains as evidence targets. Do not use raw VM IPs or private
  app ports as report evidence.
- List only non-secret configuration boundaries: environment variable names,
  domain relationships, runtime placement, persistence category, and reverse
  proxy/TLS boundary.
- Mark health checks as `PASS`, `FAIL`, or `BLOCKED`.
- Call out production-ready gaps explicitly.

## Evidence Rules

Allowed evidence:

- public health/version/service-health endpoint output
- source repo and path references
- submodule commit or PR references
- non-secret runtime shape
- screenshots and design assets from tracked repos
- generated report output path under `.artifacts`
- formal evidence bundle references

Forbidden evidence:

- Linode tokens
- DNS provider credentials
- DB DSNs or passwords
- JWT/auth signing secrets
- bearer tokens
- object storage access keys
- private keys or certificate private material
- raw customer data
- raw upstream payloads that expose internal-only fields

If a status cannot be verified from a safe source, write `BLOCKED` or `not yet
verified`; do not copy an old status as if it were current.

## Language And Tone

- The report body must be written in Traditional Chinese by default.
- Keep repository names, API names, endpoint paths, product names, commands,
  status labels such as `PASS`/`FAIL`/`BLOCKED`, and established technical
  terms in English when that is the clearest source-of-truth wording.
- Do not mix English section titles with Chinese prose. Section titles,
  captions, table headers, summaries, and review checklist items should be
  Traditional Chinese unless they are literal product/repo/API names.
- If an English version is needed for external audiences, generate it as a
  separate translation pass rather than mixing languages in the same report.
- Use direct, management-readable language.
- Be explicit about "done", "foundation exists", "integration-ready",
  "staging-only", "blocked", and "production gap".
- Avoid vague optimism such as "almost ready" unless the remaining gate is
  named.
- Avoid overclaiming private cloud, SLA, HA, backup/restore, or customer-ready
  production status without evidence.
- Use English technical terms where they are repo/API names; use Traditional
  Chinese prose for management explanation.

## Production-Readiness Guardrails

Do not call the deployment production-ready unless these are covered:

- release versions are explicit and not `debug`
- selected public health checks pass
- service-local smoke checks pass
- product-level evidence bundle exists
- backup/restore references exist for deployed persistent stores
- broker evidence exists when MQTT/NATS/EMQX is enabled
- support boundary and owner model are clear
- disabled optional components are marked `SKIP` intentionally

For normal weekly status, it is acceptable to say "staging is live" or
"foundation exists" when evidence supports that wording.

## Reuse Checklist

Before generating or sending a report:

- Follow the outline in `templates/cloud-status-report-outline.md`.
- Pull screenshots from `materials.md`.
- Run the builder if a Word deliverable is needed.
- Update live health checks instead of reusing stale results.
- Confirm the generated Word report uses Traditional Chinese for all narrative
  sections, captions, table headers, and checklist items.
- Verify no secrets or raw customer data appear in text, tables, captions, or
  screenshots.
- Keep generated artifacts under `.artifacts/status-reports/YYYY-MM-DD/`.
