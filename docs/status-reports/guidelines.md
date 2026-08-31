# Status Report Writing Guidelines

Status: source.

Owner: `rtk_cloud_workspace`.

This document defines the standard format for Realtek Video / IoT Cloud weekly reports. Reports produced by different people, tools, or language models must use the same sections, assessment vocabulary, and evidence standard.

## 0. One-Page Reference

Every report must answer nine questions:

| Question | Required Answer |
| --- | --- |
| Why are we building this cloud? | How it supports AmebaPRO / IoT modules, SDK/apps, the ecosystem, customer PoCs, design-ins, and commercial KPIs. |
| What can be demonstrated now? | UI, SDK/sample app, API, deployment, health check, design assets, and load-test evidence. |
| Where are we on the schedule? | Current position, next gate, and risk assessment on the path from 2026-05-01 to the early-August 100,000-device load test. |
| Which capabilities are verified? | Use `PASS`, `FAIL`, `SKIP`, `BLOCKED`, or `not verified`; avoid ambiguous descriptions. |
| Who are the main audiences? | Module buyers, solution developers, brand/ODM customers, video IoT customers, and the cloud proof and module-sales path for each. |
| How is each release gate assessed? | Pass criteria, required evidence, and failure status for the Aug. 1 load test, Alpha, Beta, and Public gates. |
| How does technology connect to product and KPIs? | Deployability, online success, OTA success, video setup, MQTT/shadow, load capacity, support effort, and incident response. |
| What cannot yet be called production-ready? | Gaps in release/version, backup/restore, security review, load testing, dynamic scaling, frontend staging, and operations ownership. |
| What must management or colleagues know? | Decisions/support needed, risk burn-down, evidence index, and next gate. |

Writing principles:

- Write the report in English. Preserve literal repository, API, endpoint, command, product, and status-label names.
- Do not write an engineering changelog. Include engineering detail only when it explains capability, evidence, risk, or the next action.
- Present important numbers with charts, timelines, progress visuals, or bar/line charts first. Use tables mainly for evidence, risks, checklists, and action tracking.
- If an item is unverified, write `not verified` or `BLOCKED`. Do not carry an old status forward as this week's status.
- Never include secrets, tokens, DSNs, private keys, raw lead data, raw customer data, or raw media.

## 1. Standard Table of Contents

Every management weekly report uses this structure. A short report may compress content, but the intent of each section must remain.

| Section | Purpose | Must Answer |
| --- | --- | --- |
| Cover / Core Management Message | Tell management this week's focus on the first page. | One core message, current-status summary, schedule snapshot, and product-to-KPI visual. |
| Part 1: Executive Summary | Make the full picture understandable in five minutes. | Why, target customer/use-case fit, completed work, next step, risk, and required decision. |
| Part 2: Schedule / Load-Test Path | Show progress from May 1 to early August. | Current position, this week's gate, next gate, 100,000-device IoT target, 5,000-video-camera target, release-gate definitions, and risk assessment. |
| Part 3: Cloud / Product / KPI Detail | Translate engineering capability into product and business value. | Cloud relationships, customer/use-case fit, KPIs, architecture, portal marketing, MQTT/shadow, WebRTC/storage, Security/PKI, and threat model. |
| Part 4: Operational Screens and User Flows | Explain usage scenarios to non-engineering readers. | Admin overview, device drawer, OTA, stream health, SDK/sample flow, and demo journey. |
| Part 5: Linode Staging Deployment and Configuration | Explain the current staging deployment and limits. | Endpoints, runtime shape, safe configuration, health checks, and production-readiness gaps. |
| Part 6: Decisions, Risks, and Evidence | Centralize management needs, risks, and proof. | Decision/support table, risk burn-down, and evidence index. |
| Review Checklist | Validate the output before release. | Overclaims, secrets, visual use, and explicit gaps. |
| Appendix: Materials and Source Index | Make material reusable next week. | Screenshots, design assets, repository paths, PR/commit, health evidence, and blocked evidence. |

## 2. Report Generation Workflow

Follow this sequence each week:

1. Update the report date, snapshot timestamp, and current position.
2. Collect live evidence: public health endpoint, deployment status, repository/PR/commit, design screenshots, and load-test artifacts.
3. Assess the schedule as `on track`, `at risk`, or `blocked` from evidence, not optimistic dates.
4. Select four to six images for the body and put the rest in the appendix.
5. Convert important numbers into a timeline, milestone lane, progress bar, bar chart, or line chart.
6. Write the executive summary and core message.
7. Check secrets and redaction.
8. Generate the `.docx` under `.artifacts/status-reports/YYYY-MM-DD/`.
9. Render the DOCX for visual QA.
10. Update the evidence index and missing/blocked items.

Generated output remains under `.artifacts/` and must not be committed. The following source files may be committed:

- `docs/status-reports/guidelines.md`
- `docs/status-reports/materials.md`
- `docs/status-reports/templates/cloud-status-report-outline.md`
- `docs/status-reports/master_slide/`
- `tools/status-report/build_cloud_status_report.py`

## 3. Narrative Spine

Every report must follow the same narrative:

1. Cloud is the productization path for AmebaPRO / IoT modules, not an isolated server project.
2. The value chain is module -> SDK/app -> cloud onboarding -> video / OTA / telemetry / MQTT shadow -> admin operations -> customer PoC -> design-in / commercial KPI.
3. Translate technical progress into observable KPIs: deployability, online success, SDK integration, OTA success, video-setup success, load capacity, support effort, and incident response.
4. Operational screenshots show how customers, SDK developers, operators, and reviewers use the system.
5. Keep staging evidence distinct from production-ready claims.
6. Explain PKI's security-management value: identity, factory issuance, entitlement, audit, revocation, and lifecycle governance.
7. Distinguish traditional MQTT transport from IoT Device Shadow.
8. Distinguish WebRTC live video from video-storage/media operations.
9. Explain relationships among the Realtek Platform Root, Brand Cloud, brand users, end users, and devices.
10. Explain that the portal website is a marketing, documentation, and lead-generation layer, not the operational cloud console.
11. Threat modeling and cybersecurity review are risk-management tracks, not health checks.
12. Dynamic scaling is an architecture direction, but the August release baseline does not implement autoscaling. Decide after the load test from evidence.
13. Present customer/use-case fit early so readers know the problem solved for module buyers, solution developers, brand/ODM customers, and video IoT customers.
14. Assess release gates from evidence. If the date arrives before conditions are met, use `at risk`, `blocked`, or `not verified`.

## 4. Section Playbook

Fill every section using the same fields: `Purpose`, `Required Content`, `Suggested Visual`, `Sources`, and `Avoid`.

### 4.1 Cover / Core Management Message

| Item | Guidance |
| --- | --- |
| Purpose | Let management immediately understand weekly status, business significance, next step, and operations reality. |
| Required Content | Report title, date, one core management message, current-status summary, schedule snapshot, and product-to-KPI visual. |
| Suggested Visual | Product-to-KPI flow, status traffic light, or small progress visual. |
| Sources | Weekly evidence, schedule section, deployment health, and PR/commit. |
| Avoid | Long executive summaries, lists of technical completions alone, or omitted operations/SLA/support realities. |

Use a three-column current-status summary:

| Area | Current Status | Next Step or Risk |
| --- | --- | --- |
| Deployment | One sentence | One sentence |
| Product / demo evidence | One sentence | One sentence |
| Operations / readiness | One sentence | One sentence |
| Next milestone | One sentence | One sentence |

### 4.2 Part 1: Executive Summary

| Item | Guidance |
| --- | --- |
| Purpose | Let management understand why, now, next, and risk within five minutes. |
| Required Content | Why cloud, target customer/use-case fit, completed foundation, current evidence, next gate, risk, and decision/support needed. |
| Suggested Visual | Foundation-versus-next-step table, small schedule snapshot, or KPI bridge. |
| Sources | Part 2 schedule, Part 3 capability, Part 5 deployment, and Part 6 risks. |
| Avoid | Excessive repository detail or presenting unverified items as complete. |

The summary must include current phase, target date, next measurable gate, target customer/use-case fit, `on track` / `at risk` / `blocked`, and required management decisions/support.

#### 4.2.1 Customer / Use-Case Fit

| Item | Guidance |
| --- | --- |
| Purpose | Show which customer scenarios the cloud serves and connect customer needs to module sales, PoCs, and design-ins. |
| Required Content | Target customer, customer need, cloud proof object, and module-sales/PoC linkage. |
| Suggested Visual | 2x2 customer-fit cards, segment bar, or use-case-to-sales bridge. |
| Sources | Customer/FAE discussions, portal content, SDK/sample app, Admin screenshots, and Video/MQTT/OTA evidence. |
| Avoid | Generic market vision or customer types without corresponding cloud proof. |

| Target Customer | What They Need | Module Sales Linkage |
| --- | --- | --- |
| Module buyer | Onboarding, SDK/app, OTA, video, MQTT/shadow, and Admin operations beyond the module itself. | Shorter evaluation and greater design-in confidence. |
| Solution developer | Testable cloud APIs, sample app, device flow, debug reports, and documentation entry points. | Self-service PoCs and less repeated FAE explanation. |
| Brand / ODM customer | Clear Brand Cloud and tenant/user/device relationships and explicit Realtek platform support. | Move private/brand-cloud discussions toward verifiable architecture earlier. |
| Video IoT customer | Evidence for live-video relay, storage/media, stream health, future scaling, and cost. | Support commercial evaluation of camera/sensor solutions. |

### 4.3 Part 2: Schedule / Load-Test Path

| Item | Guidance |
| --- | --- |
| Purpose | Show project progress from 2026-05-01 to the early-August target. |
| Required Content | `Current Position`, `This Week's Target`, `Next Gate`, `Risk`, and `Assessment`. |
| Suggested Visual | Timeline, Gantt-style chart, milestone lane, or progress bar. |
| Sources | Load-test plan, runner output, deployment status, metric thresholds, and weekly evidence. |
| Avoid | Tables alone, claiming on-track status because the date has not arrived, or combining the IoT 100,000 and Video 5,000 targets. |

Schedule constants:

| Item | Fixed Value |
| --- | --- |
| Project start | 2026-05-01 |
| IoT target | Pass the 100,000-device load test in early August 2026 |
| Video target | Pass the 5,000-video-camera load test on 2026-08-01 |
| Post-load-test release path | August alpha, September beta, then public path |
| Dynamic scaling | Not implemented in the August release; decide after the load test from evidence |

Baseline milestone path:

| Period | Milestone | Evidence |
| --- | --- | --- |
| 2026-05-01 to 2026-05-10 | Project kickoff and scope lock | Cloud purpose, source-of-truth boundaries, deployment target, and 100,000-device target. |
| 2026-05-11 to 2026-05-24 | Foundation buildout | Linode staging, Account Manager / Video Cloud / Admin integration, SDK/sample, OTA/telemetry, and status-report framework. |
| 2026-05-25 to 2026-06-07 | Load-test preparation | Runner boundary, safe staging profile, fleet assumptions, metrics, thresholds, and operator runbook. |
| 2026-06-08 to 2026-06-21 | Small-to-medium validation | 100 / 1,000 / 5,000-device runs and classification of API, broker, DB, resource, credential, and test-data failures. |
| 2026-06-22 to 2026-07-05 | Multi-host and capacity expansion | Multi-instance/multi-host work, aggregation, resource dashboard, and bottleneck fixes. |
| 2026-07-06 to 2026-07-19 | 10,000-to-30,000-device rehearsal | p95/p99 latency, success rate, broker/database capacity, recovery behavior, and operator response. |
| 2026-07-20 to 2026-07-31 | 100,000-device dry run and hardening | Near-final dry run, soak test, rollback/retry plan, monitoring, and report packaging. |
| 2026-08-01 | 100,000-device + 5,000-video-camera load-test pass | Final run passes agreed thresholds and produces management-ready evidence. |
| August 2026 | Alpha test | SDK included; internal developers run onboarding, sample app, and debug/report flow. |
| September 2026 | Beta test | SDK plus pilot customer; collect customer feedback and support evidence. |
| After beta | Public path | Operations, account, support, and security baseline ready. |
| After load test | Dynamic-scaling implementation assessment | Decide from bottlenecks, traffic profile, cost, operating model, and production direction. |

Video lane:

| Period | Video Milestone | Evidence |
| --- | --- | --- |
| 2026-06-01 to 2026-06-21 | Video-readiness foundation | WebRTC signaling, owner transport, TURN/ICE, stream health, and snapshot/media upload/download evidence. |
| 2026-06-22 to 2026-07-12 | Video small-scale validation | Representative app/device signaling, media upload, download authorization, and stream-health pass. |
| 2026-07-13 to 2026-07-31 | 5,000-camera rehearsal | Fleet, media profile, TURN/coturn capacity, metrics, storage/retention, and runbook. |
| 2026-08-01 | 5,000-camera load-test pass | Validate WebRTC/video-storage readiness at the same gate as 100,000 IoT devices. |
| After load-test pass | Alpha/beta release support | Use evidence to size operations cost, production scaling, and customer-pilot boundaries. |

Current-position rules:

- A report around 2026-06-03 should mark `Load-test preparation`.
- Later reports advance only from evidence. If a date arrives before the gate passes, mark `at risk` or `blocked`.
- You may write `architecture supports future scaling`, but must not claim that the August release supports autoscaling.

Release-gate definitions:

| Gate | Scope | Evidence to Pass |
| --- | --- | --- |
| Aug. 1 load-test pass | 100,000 IoT devices + 5,000 video cameras | Success rate, p95/p99, error taxonomy, resource use, recovery behavior, and report package. |
| Alpha test | SDK + actual internal-developer use | Four to six internal testers; at least three to four developer/firmware/app testers run onboarding, SDK sample, and debug/report. |
| Beta test | SDK + pilot customer | One to two pilot customers or partner use cases; validate PoC feedback, support flow, and deployment/cost assumptions. |
| Public path | Operations, account, support, and security baseline | Company/approved third-party billing, backup operator, release version, backup/restore, and security-review gate. |

### 4.4 Part 3: Cloud / Product / KPI Detail

| Item | Guidance |
| --- | --- |
| Purpose | Turn cloud engineering into product, business, and operations capabilities. |
| Required Content | Source-of-truth boundaries, current-versus-target architecture, KPI framework, cloud relationships, portal marketing, WebRTC/storage, MQTT/shadow, Security/PKI, and threat model. |
| Suggested Visual | Architecture diagram, cloud-relationship diagram, KPI bridge, readiness matrix, or capability table. |
| Sources | `docs/architecture.md`, contract docs, submodule docs, design assets, and deployment evidence. |
| Avoid | Treating Admin as a source of truth or using a health endpoint as capability evidence. |

Source-of-truth boundaries:

| Layer | Source of Truth |
| --- | --- |
| Account Manager | Identity, tenant, user, organization, membership, registry devices, provisioning operations, and authoritative audit. |
| Video Cloud | Runtime activation, device transport, WebRTC/video, MQTT/shadow, OTA/media/telemetry/log runtime facts. |
| Admin Console | Dashboard/BFF, evidence aggregator, and operation surface; not an authoritative store. |
| Frontend / portal | Marketing website, documentation/manual portal, and lead-generation layer; not an operational console. |
| SDK/app/firmware | Onboarding, claim-material handling, local setup, device transport, and end-user flow; does not determine tenant policy. |

### 4.5 Cloud Relationship / Tenant Structure

| Item | Guidance |
| --- | --- |
| Purpose | Prevent confusion among the Realtek platform cloud, brand-name cloud, and end-user/device experience. |
| Required Content | Realtek Platform Root -> Brand Cloud -> brand users / end users / devices. |
| Suggested Visual | Three-layer diagram, source-of-truth map, end-user onboarding flow, or role/audience table. |
| Sources | `brand_cloud_admin.md`, `product_onboarding.md`, `authorization.md`, `provision.md`, and Admin spec. |
| Avoid | Describing Brand Cloud as an Admin SQLite local record or implying that each Brand Cloud is a separate physical cloud without deployment evidence. |

```text
System Root / Realtek Platform
  -> Brand Cloud
      -> brand users / operators
      -> end users
      -> registry devices / activated cloud devices
      -> lifecycle operations, service options, runtime evidence
```

### 4.6 Portal Web / Digital Marketing

| Item | Guidance |
| --- | --- |
| Purpose | Explain how `rtk_cloud_frontend` supports marketing, documentation, lead generation, and sales improvement. |
| Required Content | SEO, content development, visitor-behavior analytics, CTA conversion, lead capture, and sales follow-up loop. |
| Suggested Visual | Funnel chart, content map, SEO-readiness matrix, aggregate-behavior chart, or lead-conversion chart. |
| Sources | `rtk_cloud_frontend/README.md`, `docs/spec.md`, `docs/analytics.md`, `docs/api_reference.md`, and `docs/manual_content_system.md`. |
| Avoid | Treating website analytics as device telemetry or exposing raw lead email, analytics rows, full referrers, search queries, or `ADMIN_TOKEN`. |

- `rtk_cloud_frontend` is the public marketing website, documentation/manual portal, and lead-generation layer.
- It is not the IoT console, authentication service, OTA backend, device-provisioning backend, telemetry platform, or production mobile app.
- Connect the report to sales improvement: identify which pages, features, demos, keywords, and CTAs must improve to create more qualified sales conversations.

### 4.7 WebRTC / Video Storage Management

| Item | Guidance |
| --- | --- |
| Purpose | Separate live-video readiness from stored-media readiness. |
| Required Content | WebRTC signaling, owner transport, TURN/ICE, session lifecycle, and stream health; snapshot/media upload, metadata, download authorization, byte range, delete, retention, and backup. |
| Suggested Visual | WebRTC flow diagram, media-capability table, or readiness matrix. |
| Sources | `streaming.md`, `snapshot_and_media.md`, `device_transport.md`, `auth.md`, `authorization.md`, and client-integration docs. |
| Avoid | Using API health to claim end-to-end live video or exposing object-storage keys, signed-URL secrets, bearer tokens, or raw media. |

- `WebRTC signaling readiness`: ICE preflight, offer creation, answer wait, device answer, close route, auth, and signaling-transport evidence exist.
- `live-video readiness`: signaling, device-owner transport, and representative media-path/stream-health evidence all exist.
- `video storage/media readiness`: upload, metadata, download authorization, and storage/retention evidence all exist.

```text
GET /api/request_webrtc/ice
  -> app offer
  -> POST /api/request_webrtc
  -> signaling transport webrtc_offer
  -> device answer
  -> POST /api/request_webrtc/answer
  -> GET /api/request_webrtc
  -> app media negotiation
  -> POST /api/request_webrtc/close
```

### 4.8 MQTT / Device Shadow Management

| Item | Guidance |
| --- | --- |
| Purpose | Explain that IoT Cloud includes both traditional MQTT transport and IoT Device Shadow. |
| Required Content | Broker/topic connectivity, owner transport, command delivery, events/logs; desired/reported/delta/version/lifecycle/tombstone/ACL. |
| Suggested Visual | Two-lane MQTT/shadow diagram, topic-surface table, or state-management matrix. |
| Sources | `device_transport.md`, `device_shadow.md`, `provision.md`, `api_usage.md`, and client transport docs. |
| Avoid | Using broker health to claim shadow readiness or treating MQTT as an activation/deactivation API. |

| Layer | Purpose | Evidence |
| --- | --- | --- |
| MQTT transport | Broker connectivity, command routing, event/log ingress, owner transport, and QoS/topic delivery. | Topic delivery, owner transport, and command/event/log evidence. |
| Device Shadow | Cloud-held state: `state.desired`, `state.reported`, `state.delta`, `version`, and `clientToken`. | Shadow API/topic behavior, version conflict, delta, bootstrap, deactivation, and tombstone evidence. |

### 4.9 Security / PKI Management

| Item | Guidance |
| --- | --- |
| Purpose | Present PKI as a security-management system, not just mTLS technology. |
| Required Content | Device identity, factory enrollment, CSR policy, certissuer, service entitlement, token bootstrap, revocation, audit, and unprovision versus deactivation. |
| Suggested Visual | Trust-chain diagram, security-management matrix, or PKI-readiness table. |
| Sources | `auth.md`, `provision.md`, `contract_overview.md`, certissuer design, factory-enrollment design, and client PKI docs. |
| Avoid | Private keys, raw CSR/certificate PEM, CA signing material, bearer tokens, or claims that a CSR itself is authentication. |

- PKI/mTLS is the target production authentication model.
- Device identity comes from the verified client-certificate subject and cannot be overridden by request-body `devid`.
- Factory enrollment is a manufacturing and security-management flow.
- `service_options` is the canonical service-access ACL; `device_type` is not an authorization source.
- Device certificates are bootstrap credentials. Runtime routes still use scoped, subject-bound tokens and ACL checks.
- Revocation, deactivation, and unprovision are distinct lifecycle controls.
- HSM / PKCS#11 signer design must describe the key-custody boundary: a service receives signing capability but never raw private key material.
- A report may list provider type, signer boundary, and audit/fail-closed behavior. It must not list PKCS#11 module paths, PINs, slot IDs, token labels, key labels, CA key paths, or raw signer configuration.

```text
factory/MES or fixture
  -> factory enrollment
  -> certissuer
  -> device certificate
  -> mTLS token bootstrap
  -> service-options ACL
  -> runtime services
```

```text
HSM-backed token / non-exportable key
  -> PKCS#11 signer adapter
  -> certissuer CA signing and Ed25519 token signing
  -> certificate/token output with audit
```

### 4.10 Threat Model / Cybersecurity Review

| Item | Guidance |
| --- | --- |
| Purpose | Explain cybersecurity review progress, gaps, and next steps without declaring the system "secure." |
| Required Content | Method/scope, current status, top critical/high risks, open questions, next review focus, and mitigation/evidence status. |
| Suggested Visual | Cybersecurity review table, risk heatmap, or risk burn-down. |
| Sources | `cyber_security/README.md`, `assumptions.md`, `sources.md`, STRIDE threat model, STRIDE matrix, and evidence notes. |
| Avoid | Treating health checks as security sign-off or exposing raw logs, tokens, DSNs, private keys, certificates, customer data, or raw lead data. |

| Status | Meaning |
| --- | --- |
| `drafted` | Threat model or matrix exists but is not reviewed. |
| `reviewing` | Manual, code, or deployment review is in progress. |
| `evidence-needed` | Command, code, deployment, or artifact evidence is required. |
| `mitigation-needed` | A risk is known, but the fix or owner is not closed. |
| `blocked` | Required evidence or owner is unavailable. |
| `closed` | Mitigation or verification is complete and has an evidence reference. |

Review at least these top risk themes:

- `I2`: secrets leaking through Git, logs, artifacts, evidence, or issue bodies.
- `S1/E1`: tokens, subject binding, route scope, legacy credentials, and certificate-header confusion.
- `S2`: MQTT auth/TLS/device-identity spoofing.
- `D1`: WebRTC, MQTT, media, database, storage, and TURN capacity exhaustion.
- `E2`: Admin BFF proxy/cache expanding privileges beyond upstream authority.

### 4.11 Load-Test Readiness

| Item | Guidance |
| --- | --- |
| Purpose | Track verifiable readiness for the early-August 100,000-device target. |
| Required Content | Runner/profile, fleet/data, metrics/thresholds, infrastructure/multi-host, broker/database/storage visibility, and report evidence. |
| Suggested Visual | Readiness matrix, progress bar, risk burn-down, or scale-target chart. |
| Sources | Load-test runner output, JSON/Markdown reports, metrics dashboard, and deployment evidence. |
| Avoid | Vague prose; use `ready`, `partial`, `blocked`, or `not verified`. |

| Area | Current Status | Needed Before 100K | Owner / Dependency | Risk |
| --- | --- | --- | --- | --- |

### 4.12 Part 4: Operational Screens and User Flows

| Item | Guidance |
| --- | --- |
| Purpose | Use screens to help non-engineering readers understand how the cloud is operated and validated. |
| Required Content | Admin Fleet Overview, Devices Drawer, Firmware OTA, Stream Health, SDK/sample-app flow, and frontend/product architecture. |
| Suggested Visual | Four to six selected body images; put the complete material index in the appendix. |
| Sources | `rtk_cloud_admin/docs/assets/webui-design/`, `rtk_cloud_client/docs/mockups/`, `rtk_cloud_frontend/static/assets/`, and `docs/status-reports/master_slide/assets/`. |
| Avoid | Turning the report into a material dump; every image needs a caption and stated purpose. |

Standard demo flow:

1. Admin reviews the fleet overview.
2. Admin finds an abnormal device or one requiring attention.
3. Admin opens the device-detail drawer.
4. Admin checks firmware, stream, telemetry, and readiness/source facts.
5. SDK/sample app demonstrates provisioning, configuration, camera monitoring, and debug reporting.
6. The load test validates the same capability path at scale.

### 4.13 Part 5: Linode Staging Deployment and Configuration

| Item | Guidance |
| --- | --- |
| Purpose | Explain staging deployment, runtime shape, configuration boundaries, health status, and production gaps. |
| Required Content | Public endpoints, snapshot timestamp, live-health table, runtime placement, non-secret environment-key names, persistence category, reverse-proxy/TLS boundary, and production-readiness gaps. |
| Suggested Visual | Runtime topology, health-status table, or configuration-boundary diagram. |
| Sources | Public health endpoints, deployment docs, Linode runbooks, and evidence bundles. |
| Avoid | Raw VM IP/private ports as report evidence or any DB DSN, JWT secret, Linode token, DNS credential, or object-storage key. |

- In this report, Linode is foundational VM/infrastructure service, not an AWS-style managed-service stack.
- We manage PostgreSQL, MQ/message queues, brokers, reverse proxies, and runtime services at the VM/service layer.
- This portability makes a future move to AWS, GCP, Azure, Alibaba Cloud, or another infrastructure cloud easier.
- Health checks are status evidence, not production sign-off.

Default dynamic-scaling statement:

```text
architecture supports future scaling; implementation deferred until after loading test
```

### 4.14 Part 6: Decisions, Risks, and Evidence

| Item | Guidance |
| --- | --- |
| Purpose | Show management which decisions are needed, whether risks are falling, and where evidence lives. |
| Required Content | Decision/support table, risk burn-down, and evidence index. |
| Suggested Visual | Risk trend, burn-down table, or evidence-status summary. |
| Sources | Weekly blockers, PR/commit, health evidence, load-test reports, and deployment docs. |
| Avoid | Hiding management requests in paragraphs or including a resource plan unless the report owner explicitly requests it. |

| Decision / Support Needed | Why Now | Impact if Delayed | Owner / Audience |
| --- | --- | --- | --- |

| Risk | Current Status | Mitigation | Owner / Dependency | Trend |
| --- | --- | --- | --- | --- |

Trend values: `down`, `flat`, `up`, `new`, and `closed`.

Evidence-index categories:

- live-endpoint evidence
- repository / PR / commit evidence
- screenshot / design evidence
- load-test-report evidence
- deployment / configuration evidence
- production-readiness evidence
- missing or blocked evidence

## 5. Visual and Number Rules

Do not present important numbers only in numeric tables.

| Content | Suggested Visual |
| --- | --- |
| Schedule path / current position | Timeline, Gantt-style chart, or milestone lane. |
| Load-test scale target | Progress bar, scale ladder, or bar chart. |
| Success rate / error rate / latency / throughput | Line chart, bar chart, or bullet chart. |
| KPI movement across weeks | Line chart or small multiples. |
| Online/offline, firmware rollout, stream-health distribution | Stacked bar or distribution chart. |
| Risk trend | Risk burn-down or trend indicator. |

Tables are appropriate for the evidence index, risk list, decision/support list, endpoint health checks, readiness matrix, and appendix material catalog.

## 6. Evidence and Redaction Rules

Allowed evidence:

- public health/version/service-health endpoint output
- source repository and path references
- submodule commit or PR references
- non-secret runtime shape
- screenshots and design assets from tracked repositories
- generated report output paths under `.artifacts`
- formal evidence-bundle references

Forbidden evidence:

- Linode tokens
- DNS-provider credentials
- DB DSNs or passwords
- JWT/auth signing secrets
- bearer tokens
- object-storage access keys
- signed media URLs with secret query material
- private keys or certificate private material
- raw CSR PEM or raw certificate PEM
- CA signing-key paths or signing material
- raw media or customer-visible captured media unless sanitized and approved
- raw lead payloads, lead emails, analytics-event rows, full referrer URLs, or search-query text
- raw customer data
- raw upstream payloads exposing internal-only fields

If a status cannot be verified from a safe source, write `BLOCKED` or `not verified`; never copy an old status as if it were current.

## 7. Review Checklist

- The first page has a core management message, current-status summary, and schedule snapshot.
- The summary is understandable within five minutes.
- The schedule shows the path from 2026-05-01 to the 2026-08-01 target for 100,000 devices plus 5,000 video cameras and marks `Current Position`.
- The video lane separates WebRTC/video-storage evidence from IoT telemetry/load-test evidence.
- Release-gate definitions include pass criteria for Aug. 1, Alpha, Beta, and Public.
- Important numbers use charts, timelines, or progress visuals first.
- The Load-Test Readiness Matrix lists gates preceding the 100,000-device target.
- Cloud relationships among Realtek Platform Root, Brand Cloud, brand users, end users, and devices are clear.
- Admin Console is not described as the source of truth for Account Manager or Video Cloud.
- Portal-web/digital-marketing coverage includes SEO, content, behavior analytics, lead conversion, and sales improvement.
- WebRTC live video is separate from video storage/media.
- MQTT transport is separate from IoT Device Shadow.
- Security/PKI is a security-management narrative, not merely mTLS technology.
- Threat-model/cybersecurity review includes STRIDE, top risks, open questions, and review progress.
- Operational screenshots prove demo or customer-workflow readiness.
- Linode deployment/configuration contains no secrets and does not overclaim production readiness.
- Decision/support, risk burn-down, and evidence index are clear.
- A resource plan is omitted unless the report owner explicitly requests it.
- Dynamic scaling is not claimed as implemented for the August release.
- Production-readiness gaps are explicit.

## 8. Source Reference Map

| Topic | Primary Sources |
| --- | --- |
| Report framework | `docs/status-reports/README.md`, `materials.md`, and `templates/cloud-status-report-outline.md`. |
| Master slide / design | `docs/status-reports/master_slide/powerpoint_master.pptx`, `master_slide/design-guidelines.md`, `master_slide/SKILL.md`, and `master_slide/assets/`. |
| Cloud relationship | `rtk_cloud_contracts_doc/brand_cloud_admin.md`, `product_onboarding.md`, `authorization.md`, `provision.md`, and `rtk_cloud_admin/docs/spec.md`. |
| Portal web / digital marketing | `rtk_cloud_frontend/README.md`, `docs/spec.md`, `docs/analytics.md`, `docs/api_reference.md`, and `docs/manual_content_system.md`. |
| WebRTC / video storage | `rtk_cloud_contracts_doc/streaming.md`, `snapshot_and_media.md`, `device_transport.md`, `auth.md`, `authorization.md`, and client-integration docs. |
| MQTT / shadow | `rtk_cloud_contracts_doc/device_transport.md`, `device_shadow.md`, `provision.md`, `api_usage.md`, and client-transport docs. |
| PKI / security | `rtk_cloud_contracts_doc/auth.md`, `provision.md`, `rtk_video_cloud/docs/cert-issuer-server-design.md`, `factory-enrollment-server.md`, and `rtk_cloud_client/docs/pki_device_auth.md`. |
| Threat model / cybersecurity | `cyber_security/README.md`, `assumptions.md`, `sources.md`, `threat_models/rtk_video_cloud-stride-threat-model.md`, and `analysis/stride-matrix.md`. |
| Deployment / evidence | `docs/product-level-evidence.md`, `docs/deployment-secrets-governance.md`, deployment runbooks, and public health endpoints. |

## 9. Language and Tone

- Write the report body in English.
- Preserve literal repository, API, endpoint, command, product, and status-label names.
- Keep section titles, captions, table headers, summaries, and checklists consistently in English.
- Do not mix languages in one report.
- Avoid claims such as `secure`, `production-ready`, or `autoscaling ready` without corresponding evidence.

### 9.1 Natural Management Writing Rules

Report prose, slide titles, transition text, captions, and speaker-facing summaries must read like an actual management report, not an automated summary.

- Avoid formulaic contrasts such as "This is not A; it is B" or "not only A, but also B." State the distinct roles directly or use a table.
- Avoid unsupported adjectives such as "complete," "powerful," "seamless," "intelligent," or "end-to-end." If one is necessary, attach evidence, scope, or a limit.
- Do not over-explain the obvious. Management slides prioritize conclusions, impact, and next steps.
- Vary sentence openings across the deck. Do not start every page with "This page explains," "The key point is," or "Purpose."
- Do not turn technical terms into slogans. Connect each term to a capability, control point, evidence item, risk, or owner.
- Avoid exaggerated commitments. Use `not verified`, `evidence-needed`, `BLOCKED`, `target`, or `planned` for unverified content.
- Use natural transitions such as "Next, review...", "This section focuses on...", or "Start with... to establish context." Avoid theatrical language.
- Keep one primary message per page. If two lines cannot communicate it clearly, use a chart, timeline, diagram, or another page.

Suggested replacements:

| Avoid | Prefer |
| --- | --- |
| This is not a single technical-progress report; it is... | This report focuses on four items:... |
| This is not device runtime; it is a public website... | Next, review the public website, documentation, SEO, and lead flow. |
| It supports not only A, but also B. | A and B have distinct roles:... |
| Powerful cloud capabilities | Verified capabilities:...; evidence still needed:... |
| Complete end-to-end solution | Current scope covers module, SDK, onboarding, OTA, video, and Admin; production gaps include... |
