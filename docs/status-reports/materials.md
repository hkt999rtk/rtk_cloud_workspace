# Status Report Materials

Status: source.

Owner: `rtk_cloud_workspace`.

This index records the reusable materials for weekly Realtek Video / IoT Cloud
status reports.

## Operation Screenshots

| Material | Source | Use in report |
| --- | --- | --- |
| Admin Fleet Health Overview | `repos/rtk_cloud_admin/docs/assets/webui-design/customer-overview.png` | Show customer/operator fleet health, online rate, attention queue, and health distribution. |
| Admin Devices + Detail Drawer | `repos/rtk_cloud_admin/docs/assets/webui-design/customer-devices.png` | Show device search/filter, health facts, source facts, active stream status, provisioning, and deactivation. |
| Admin Firmware & OTA | `repos/rtk_cloud_admin/docs/assets/webui-design/customer-firmware-ota.png` | Show firmware distribution, rollout progress, failed rollout, and device firmware risk. |
| Admin Stream Health | `repos/rtk_cloud_admin/docs/assets/webui-design/customer-stream-health.png` | Show WebRTC stream success, request volume, device failure risk, and stream attention workflow. |
| Sample Ops Lab Screen Flows | `repos/rtk_cloud_client/docs/mockups/sample-ops-lab-screen-flows.png` | Show SDK/sample operation flow: environment setup, provisioning, device config, camera monitor, debug report. |
| Sample Ops Lab UI Mockup | `repos/rtk_cloud_client/docs/mockups/sample-ops-lab-ui-mockup.png` | Optional appendix or deeper SDK/demo section. |

## Product / Website Visuals

| Material | Source | Use in report |
| --- | --- | --- |
| Connect+ Architecture Visual | `repos/rtk_cloud_frontend/static/assets/connectplus-architecture-diagram-corporate-v2.jpg` | Explain external product architecture or ecosystem story. |
| Connect+ Operations Console | `repos/rtk_cloud_frontend/static/assets/connectplus-operations-console-corporate-v2.jpg` | Optional product-facing operations console visual. |
| Connect+ Platform Surfaces | `repos/rtk_cloud_frontend/static/assets/connectplus-platform-surfaces-corporate-v2.jpg` | Optional product-facing surface overview. |
| Feature App SDK | `repos/rtk_cloud_frontend/static/assets/feature-app-sdk.png` | Optional SDK/app feature visual. |
| Feature Fleet Management | `repos/rtk_cloud_frontend/static/assets/feature-fleet-management.png` | Optional fleet management visual. |
| Feature Provision Flow | `repos/rtk_cloud_frontend/static/assets/feature-provision-flow.jpg` | Optional onboarding/provisioning visual. |
| Feature OTA Control Center | `repos/rtk_cloud_frontend/static/assets/feature-ota-control-center.jpg` | Optional OTA feature visual. |

## Portal Web / Digital Marketing Sources

| Source | Use in report |
| --- | --- |
| `repos/rtk_cloud_frontend/README.md` | Portal positioning, marketing foundation, SEO/social metadata, sitemap/robots, contact lead capture, analytics, multilingual routes, and public website boundaries. |
| `repos/rtk_cloud_frontend/docs/SPEC.md` | Current portal implementation, visual direction, content system, routes, and product marketing scope. |
| `repos/rtk_cloud_frontend/docs/ANALYTICS.md` | First-party analytics purpose, privacy guardrails, event model, CTA/engagement tracking, and admin aggregate behavior. |
| `repos/rtk_cloud_frontend/docs/API_REFERENCE.md` | `/api/event`, `/api/search`, `/contact`, `/admin/leads`, and admin export surfaces. |
| `repos/rtk_cloud_frontend/docs/MANUAL_CONTENT_SYSTEM.md` | File-backed manual/content system and managed content workflow. |

## Master Slide / Presentation Design

| Material | Source | Use in report |
| --- | --- | --- |
| Realtek PowerPoint Master | `docs/status-reports/master_slide/powerpoint_master.pptx` | Company master for PowerPoint report decks. |
| Designer Guideline | `docs/status-reports/master_slide/design-guidelines.md` | Designer-facing rules for typography, palette, layout patterns, and report-to-slide mapping. |
| AI Slide Skill | `docs/status-reports/master_slide/SKILL.md` | AI/LLM-facing instruction file for generating slides in the master style. |
| PPTX Layout Template | `docs/status-reports/templates/cloud-status-report-pptx-layout.md` | Fixed management deck structure, topic transitions, proof-object expectations, and layout rules. |
| PPTX Builder | `tools/status-report/build_cloud_status_report_pptx.mjs` | Generates the editable PowerPoint deck and rendered QA PNGs. |
| Extracted Master Assets | `docs/status-reports/master_slide/assets/` | Reusable background, logo, strip, and side-imagery assets extracted from the PPTX. |
| Master Asset Contact Sheet | `docs/status-reports/master_slide/assets/media-contact-sheet.png` | Quick visual inventory for selecting extracted assets. |

## Deployment And Configuration Sources

| Source | Use in report |
| --- | --- |
| `docs/private-cloud-deployment.md` | Deployment order, support boundary, network/TLS, backup/restore, and production-ready gaps. |
| `docs/linode-staging-deployment-snapshot.md` | Linode staging endpoint list, runtime shape, and previous snapshot evidence. |
| `docs/cost/` | AWS commercial-pilot cost estimate material: service mapping, quantity worksheet, public pricing snapshot, scenario totals, support-plan treatment, and caveats. |
| `docs/product-level-evidence.md` | Formal evidence bundle boundary and status semantics. |
| `repos/rtk_video_cloud/deploy/README.md` | Video Cloud packaged runtime inventory, systemd services, EMQX/coturn/PostgreSQL/Prometheus shape. |
| `repos/rtk_video_cloud/docs/config-map.md` | Video Cloud non-secret configuration categories. |
| `repos/rtk_account_manager/linode_deploy/README.md` | Account Manager public VM shape, deployment model, PostgreSQL and nginx boundary. |
| `repos/rtk_cloud_admin/deploy/linode/README.md` | Admin dashboard public VM shape, upstream configuration, Docker/SQLite/nginx boundary. |
| `repos/rtk_cloud_frontend/docs/deployment-linode.md` | Frontend artifact-first Linode deployment model and runtime env categories. |

## Threat Model / Cyber Security Sources

| Source | Use in report |
| --- | --- |
| `cyber_security/README.md` | Threat modeling method, STRIDE categories, directory purpose, and data handling rules. |
| `cyber_security/assumptions.md` | Deployment, auth, authorization, data-sensitivity, and open-question assumptions that affect risk ranking. |
| `cyber_security/sources.md` | Security-relevant source index for architecture, deployment, auth, streaming, MQTT, media, logging, and evidence docs. |
| `cyber_security/threat_models/rtk_video_cloud-stride-threat-model.md` | Executive summary, scope, trust boundaries, assets, attacker model, STRIDE risk summary, and recommendations. |
| `cyber_security/analysis/stride-matrix.md` | Detailed STRIDE rows, priority, gaps, mitigations, detections, and manual review focus paths. |
| `cyber_security/evidence/README.md` | Redacted security evidence notes and artifact handling expectations. |
| `repos/rtk_video_cloud` branch `codex/pkcs11-certissuer-token-signers` | New HSM / PKCS#11 signer design for certissuer CA signing and Ed25519 token signing. Use only design boundaries and safe key-custody statements; never include module paths, PINs, key labels, or raw signer config. |

## Live Health Checks

The builder currently probes these public endpoints when generating a report:

| Component | Endpoint |
| --- | --- |
| Video Cloud runtime | `https://video-cloud-staging.realtekconnect.com/healthz` |
| Video Cloud runtime | `https://video-cloud-staging.realtekconnect.com/version` |
| Account Manager API | `https://account-manager.video-cloud-staging.realtekconnect.com/v1/health` |
| Admin dashboard | `https://admin.video-cloud-staging.realtekconnect.com/healthz` |
| Admin dashboard | `https://admin.video-cloud-staging.realtekconnect.com/api/service-health` |

Record public status, versions, and high-level runtime shape only. Do not record
raw secrets, DSNs, tokens, private app ports as direct evidence targets, or
customer data.
