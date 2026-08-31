# Cloud Status Report Outline

Status: template.

Use this outline for weekly Realtek Video / IoT Cloud status reports. Apply the writing rules in `../guidelines.md` when filling each section. The generated report body must use English. Preserve literal product, repository, API, endpoint, command, and status-label names.

## Cover / Core Management Message

- Report title and date.
- One weekly core management message explaining the business rationale, current execution status, and resource and operations reality.
- The core message must always appear on the first page before the summary body.
- Place a current-status summary immediately after the core message.
- Use a three-column current-status table: `Area` / `Current Status` / `Next Step or Risk`. Include three to five rows, one sentence per cell, covering deployment, product/demo evidence, operations/readiness, and the next milestone or resource gap.
- Add one schedule snapshot that marks the current position on the path from May 1 to the early-August 100,000-device load-test target.
- Add one product-to-KPI visual.

## Part 1: Summary

- One-page conclusion.
- Why this cloud is needed.
- How technical outcomes connect to business KPIs.
- Load-test or milestone target.
- Schedule snapshot: current position, this week's target, next gate, risk, and assessment.
- Current high-level architecture.
- Current deployment status.
- Completed foundation and next steps.
- Target customer and use-case fit: explain the cloud proof needed by module buyers, solution developers, brand/ODM customers, and video IoT customers, and how it connects to module sales, PoCs, and design-ins.

## Part 2: Schedule and Load-Test Path

- Project start: 2026-05-01.
- Target: IoT 100K Device Shadow capacity validation is complete; the 5,000-video-camera load test and production-readiness evidence still must be completed by 2026-08-01.
- Dynamic scaling: the current architecture may be described as having scaling-ready boundaries and a scale-out direction, but the August release does not implement dynamic scaling. Before August, report only architecture direction, capacity evidence, multi-host readiness, bottleneck visibility, and runbooks. Do not claim that autoscaling or elastic scaling is implemented.
- Current position: update it from the report date and actual evidence, not optimistic calendar progress alone.
- Timeline, Gantt, or milestone-lane chart: show the May 1 to early-August target and clearly mark `Current Position`. Do not present the schedule only as a numeric table.
- Milestone detail table: use only as supporting detail. Include May kickoff, May foundation, late-May/early-June load-test preparation, June small/medium validation, late-June multi-host/capacity work, 100K IoT validation, video-camera validation, and the early-August final pass.
- Video schedule lane: June video-readiness foundation, July video profile and 5,000-camera rehearsal, and the 2026-08-01 gate validating 5,000 video cameras alongside 100,000 IoT devices.
- Release gate definition: define pass criteria, evidence, and failure status for the Aug. 1 load-test pass, Alpha, Beta, and Public path.
- This week's gate: measurable items that must be completed or validated this week.
- Next gate: the next verifiable milestone.
- Schedule risk: use `on track`, `at risk`, or `blocked`, and explain why.
- Load-Test Readiness Matrix: test runner/profile, test fleet/data, metrics/thresholds, infrastructure/multi-host, capacity baseline, and video-camera path.
- Load-Test Capacity Evidence: use the 8/8, 7/7, and 6/6 100K runs to show COMPLETE/SUCCESS, MQTT connections, APP ACK, server/runtime correlation, CPU p95/max, memory p95/max, payload throughput, the recommended 7/7 baseline, and the 6/6 lower-bound memory risk.
- Resource charts: show CPU and memory utilization history or trends. Bandwidth may currently show only application payload throughput; if no NIC/link-utilization sample exists, label it explicitly as the next evidence gap.

## Part 3: Cloud, Product, and KPI Detail

- Architecture details.
- Cloud relationship diagram: Realtek Platform Root -> Brand Cloud -> brand users / end users / devices. Mark source-of-truth boundaries for Account Manager, Video Cloud, Admin Console, Frontend, SDK/app, and firmware.
- Customer and use-case fit: use 2x2 cards or another segment visual to explain target customer, customer need, cloud proof object, and module-sales/PoC linkage.
- Portal Web / Digital Marketing: explain that `rtk_cloud_frontend` is the marketing website, docs/manual portal, and lead-generation layer. Cover SEO, content development, visitor-behavior analytics, CTA conversion, lead capture, and the sales-improvement loop.
- Portal funnel / content map: traffic/source -> page engagement -> CTA click -> contact lead -> sales follow-up; homepage -> features -> docs/manual -> contact.
- Current-versus-target architecture: compare current staging/runtime/evidence/operations readiness with the early-August load-test-ready target.
- Module-to-cloud-to-commercial-KPI path.
- KPI framework: technical, product, commercial, and operational.
- WebRTC / Video Storage Management: distinguish live WebRTC signaling readiness from stored-media/video-storage readiness. WebRTC covers ICE preflight, APP offer/device answer, TURN/ICE, device signaling transport, and session lifecycle. Video storage covers snapshot/media upload, metadata, download authorization, byte ranges, retention, and backup.
- WebRTC flow visual: `/api/request_webrtc/ice` -> app offer -> `POST /api/request_webrtc` -> signaling transport `webrtc_offer` -> device answer -> `POST /answer` -> `GET /api/request_webrtc` -> app media negotiation -> `POST /close`.
- Media capability table: list current status, evidence, gap, and risk separately for live stream, snapshot upload, clip/media upload, media listing, download, delete, retention, and backup/restore.
- MQTT / Device Shadow Management: distinguish traditional MQTT transport from IoT shadow-state management. MQTT covers broker/topic/owner transport; shadow covers desired/reported/delta/version/lifecycle state governance.
- MQTT/shadow topic-surface table: list `devices/<device_id>/...` command/event/log topics separately from `$vc/devices/{devid}/shadow/...` get/update/delete/accepted/rejected/delta/documents topics.
- Security / PKI trust management: describe PKI as device identity, factory enrollment, service entitlement, audit, revocation, and lifecycle governance, not merely mTLS technology.
- PKI trust-chain visual: factory/MES or fixture -> factory enrollment -> certissuer -> device certificate -> mTLS token bootstrap -> service-options ACL -> runtime services.
- HSM / PKCS#11 signer design: explain the key-custody boundary for certissuer CA signing and Ed25519 token signing. Services receive signing capability, not raw private key material.
- Security management matrix: identity, key custody, certificate issuance, entitlement, token binding, revocation, audit, and lifecycle handling.
- PKI readiness evidence: use `implemented`, `staging`, `not verified`, or `blocked`. Do not present unverified design as production-ready.
- Threat Model / Cyber Security Review: list STRIDE threat-model progress, trust boundaries, top critical/high risks, open questions, review-focus paths, and mitigation/evidence status.
- Cybersecurity review table: area, current status, evidence, gap/next check, and risk. Cover at least secrets, auth subject binding, PKI/mTLS, MQTT auth, WebRTC capacity, media/download, Admin BFF, public-listener exposure, and evidence redaction.
- API / cloud pattern.
- Product features.
- SDK / reference-app status.
- Onboarding / provisioning flow.
- Load-test plan.
- Maintenance and operations reality.

## Part 4: Operational Screens and User Flows

- Admin Fleet Health Overview.
- Admin Devices + Detail Drawer.
- Admin Firmware & OTA.
- Admin Stream Health.
- SDK/sample-app screen flow.
- Product/frontend architecture visual when useful for external positioning.
- Demo flow / user journey: Admin overview -> abnormal device -> device drawer -> firmware/stream/telemetry/readiness -> SDK sample provisioning/config/debug -> load-test scale validation.

Keep the body selective. Put the full material catalog in the appendix.

## Part 5: Linode Staging Deployment and Configuration

- Linode's role in this report: foundational VM/infrastructure services, not an AWS-style managed-service stack.
- Explain portability: we deploy and manage PostgreSQL, MQ/message queues, brokers, reverse proxies, runtime, and other services at the VM/service layer. This avoids excessive dependence on AWS-native architecture and makes future moves to AWS, GCP, Azure, Alibaba Cloud, or another platform cloud easier.
- Public endpoints and current runtime shape.
- Non-sensitive configuration boundaries.
- Dynamic-scaling status: default to `architecture supports future scaling; implementation deferred until after loading test` unless implementation and load-test evidence already exist.
- Timestamped live health-check table.
- Production-readiness gaps.

## Part 6: Decisions, Risks, and Evidence

- Decision/support-needed table: decision, why now, impact if delayed, and owner/audience.
- Risk burn-down table: risk, current status, mitigation, owner/dependency, and trend.
- Evidence index: live endpoint, repository/PR/commit, screenshot/design, load-test report, deployment/configuration, production readiness, and missing/blocked evidence.
- Omit the resource plan by default; include it only when the user or report owner explicitly requests it.

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
- object-storage access keys
- private keys
- bearer tokens
- signed media URLs with secret query material
- raw customer-visible media unless sanitized and approved
- raw lead payloads, lead emails, analytics event rows, full referrer URLs, or search-query text
- raw customer data

## Review Checklist

- The summary is understandable within five minutes.
- The schedule path and current position are clear.
- Important numbers use charts, timelines, or progress visuals first; plain tables are evidence or detail only.
- The Load-Test Readiness Matrix includes completed 100K IoT capacity gates, the recommended baseline, and gates preceding the 5,000-video-camera target.
- Detail matches current repository and deployment state.
- Technical work connects to AmebaPRO/module commercial KPIs.
- WebRTC live video and video storage/media are not conflated. Signaling, TURN/ICE, owner transport, stream health, snapshot/media upload, download authorization, retention, and backup evidence are distinct.
- The Platform Cloud / Brand Cloud / end-user-cloud relationship is clear, and Admin Console is not presented as the source of truth for Account Manager or Video Cloud.
- The portal-web/digital-marketing section covers SEO, content development, behavior analytics, lead conversion, and sales improvement without exposing raw leads or personal data.
- MQTT transport and IoT Device Shadow are not conflated. Broker/topic evidence, owner transport, and desired/reported/delta/version/lifecycle evidence are distinct.
- Operational screenshots prove demo and customer-workflow readiness.
- Deployment/configuration status avoids secrets and overclaiming.
- Decision/support, risk burn-down, and evidence indexes make next steps and gaps clear to management.
- The Security / PKI section explains the management value of identity, factory issuance, entitlement, audit, revocation, and unprovision versus deactivation.
- The threat-model/cybersecurity-review section lists STRIDE coverage, top risks, open questions, and review progress without treating a health check as security sign-off.
- Production-readiness gaps are explicit.

## Appendix: Materials and Sources

- Screenshot/material source table.
- Complete reusable-material directories.
- Internal references and runbooks.
- Cloud relationship sources: `rtk_cloud_contracts_doc/brand_cloud_admin.md`, `product_onboarding.md`, `authorization.md`, `provision.md`, `rtk_cloud_admin/docs/spec.md`, and `platform-brand-cloud-management-design.md`.
- Portal-web/digital-marketing sources: `rtk_cloud_frontend/README.md`, `docs/spec.md`, `docs/analytics.md`, `docs/api_reference.md`, and `docs/manual_content_system.md`.
- WebRTC/video-storage sources: `rtk_cloud_contracts_doc/streaming.md`, `snapshot_and_media.md`, `device_transport.md`, `auth.md`, `authorization.md`, and `rtk_cloud_client/docs/rtk_video_cloud_manual_integration.md`.
- MQTT/shadow sources: `rtk_cloud_contracts_doc/device_transport.md`, `device_shadow.md`, `provision.md`, `api_usage.md`, and `rtk_cloud_client/docs/transports.md`.
- PKI/security sources: `rtk_cloud_contracts_doc/auth.md`, `provision.md`, `rtk_video_cloud/docs/cert-issuer-server-design.md`, `factory-enrollment-server.md`, and `rtk_cloud_client/docs/pki_device_auth.md`.
- Threat-model/cybersecurity sources: `cyber_security/README.md`, `cyber_security/assumptions.md`, `cyber_security/sources.md`, `cyber_security/threat_models/rtk_video_cloud-stride-threat-model.md`, and `cyber_security/analysis/stride-matrix.md`.
