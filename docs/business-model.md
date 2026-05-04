# Realtek Connect+ Business Model

Status: source-of-truth note.

Owner: `rtk_cloud_workspace`.

Last reviewed: 2026-05-04.

## Purpose

This document captures the commercial model, evaluation tier limits, SDK
licensing posture, and support boundaries for Realtek Connect+. It is the
workspace-level source of truth that customer-facing copy in
`rtk_cloud_frontend` and SDK posture in `rtk_cloud_client` must align with.

The model is intentionally close to Espressif RainMaker's two-tier structure
(public evaluation / private commercial), with two deliberate differentiators:

1. Infrastructure model — VM/container on any cloud or on-premises, not
   serverless-locked-in (see `docs/private-cloud-deployment.md`).
2. Evaluation device ceiling — 200 devices on request (vs RainMaker's 100).

Reference baseline: `docs/deep-research-report.md` (RainMaker pricing and
deployment analysis).

## Tier Structure

### Public Evaluation Tier

| Aspect | Decision |
| --- | --- |
| Default device quota | 5 devices |
| Maximum device quota (on request) | 200 devices |
| Quota raise process | Contact us — manual review |
| Commercial use | Not permitted; evaluation, PoC, and development only |
| Time limit | None (indefinite, but subject to fair-use review) |
| Signup | Self-service with email verification |
| Cost | Free |
| Support | Community-tier: documentation + GitHub Issues only |
| Data retention | Subject to evaluation policy; not for production data |

The 200-device ceiling is a positioning differentiator versus RainMaker's 100.
Default-of-5 with manual quota raise keeps abuse and infrastructure cost in
check while giving developers a credible path to a real pilot.

### Private Commercial Tier

| Aspect | Decision |
| --- | --- |
| Minimum scale | None (no floor on device count) |
| Pricing model | One-time platform license fee + annual maintenance fee |
| Pricing transparency | Numbers not published; contact sales for quote |
| Infrastructure cost | Customer pays directly (their own GCP/Azure/AWS/on-prem bill) |
| Deployment substrate | VM or container — customer chooses cloud or runs on-prem |
| Support | Per commercial agreement (SLA tier negotiated case-by-case) |
| Branding | Custom domain and white-label app supported (in commercial scope) |
| Data ownership | Customer-owned; lives in customer-operated infrastructure |

Pricing structure rationale: a one-time license fee + annual maintenance
matches enterprise procurement expectations for VM/container-deployed
software better than per-seat subscription. RainMaker uses a similar
"platform license + customer pays infra" split, but its infra cost goes to
AWS specifically; ours goes wherever the customer hosts.

There is intentionally no minimum-scale floor — this matches RainMaker's
posture and avoids gating early-stage product teams who plan to scale later.

## SDK Licensing

| Component | Current state | Future state |
| --- | --- | --- |
| `rtk_cloud_client` SDK packages (Native C, Android, iOS, JS, Go) | Private repo | Open-sourced at go-to-market |
| `rtk_video_cloud` backend | Closed source | Closed source (permanent) |
| `rtk_account_manager` | Closed source | Closed source (permanent) |
| Other workspace repos | Closed source | Closed source unless explicitly promoted |

The SDK open-source plan is a deferred GTM action, not a current state.
Customer-facing copy must not claim the SDK is open today. Once GTM
triggers it, a separate workspace task covers license-text selection
(Apache 2.0 expected, matching RainMaker's SDK license), git history
review, and public repository setup.

The backend stays permanently closed-source. This is the same posture as
RainMaker, where ESP RainMaker Agent (SDK) is open while the cloud backend
is not.

## Support Posture

### Evaluation tier — community support

- Public documentation portal (`/docs` on the website)
- GitHub Issues on the SDK repo (post-GTM; pre-GTM via direct contact)
- No email or ticket-based response-time commitment
- No phone support

This mirrors RainMaker's evaluation support level.

### Commercial tier — contract-defined support

- SLA tier negotiated per commercial agreement (no published default)
- Response-time, uptime, and escalation paths live in the customer contract
- Realtek-side technical support contacts assigned during onboarding

The website should describe support as "per commercial agreement" without
publishing a tier matrix until SLA tiers are formalised internally.

## Branding And White-Label

| Capability | Tier |
| --- | --- |
| Realtek Connect+ branded eval portal | Evaluation tier (no white-label) |
| Custom domain on private deployment | Commercial tier |
| Branded mobile app (white-label SDK use) | Commercial tier |
| "Powered by Realtek" attribution | Discussed per commercial agreement |

Evaluation users see Realtek Connect+ branding; white-label is a commercial
scope decision tied to the deployment contract.

## Website Disclosure Requirements

Customer-facing copy in `rtk_cloud_frontend` must disclose:

- Evaluation tier device limits (5 default, 200 max on request)
- Evaluation tier non-commercial-use restriction
- Self-service signup with email verification (mark as planned/upcoming until
  shipped — see follow-up issue in `rtk_cloud_frontend`)
- Commercial pricing structure framing (license + maintenance) without
  numbers
- "Contact sales" CTA as the only path to commercial quotation
- SDK currently proprietary; open-source planned at GTM (do not claim it is
  open today)
- Backend permanently closed-source
- Community support boundary for evaluation tier

The website must NOT disclose:

- Specific dollar figures
- Specific SLA percentages until tier structure is formalised
- Internal cost basis (Realtek's own infra, salary, or margin assumptions)

## Decision History

- **2026-05-04** — initial decisions captured in this document, derived from
  comparison with RainMaker's published model. See conversation thread in
  `clever-sammet-595d11` worktree.

## Cross-References

- `docs/deep-research-report.md` — RainMaker pricing and deployment analysis
  used as reference baseline
- `docs/private-cloud-deployment.md` — deployment BOM and operations runbook
  for the commercial tier
- `repos/rtk_cloud_frontend/internal/features/features.go` — private-cloud
  feature page content (must reflect this document)
- `repos/rtk_cloud_frontend/internal/content/content.go` — i18n strings for
  pricing and tier disclosure
