# Realtek Cloud Status Report Framework

Status: source.

Owner: `rtk_cloud_workspace`.

This directory defines the reusable weekly status report framework for Realtek
Video / IoT Cloud and Connect+ work. The framework is tracked in git; generated
Word/PPTX files, rendered pages, and copied figure assets stay under
`.artifacts/`.

## Output Model

Use the Word builder from the workspace root:

```sh
  tools/status-report/build_cloud_status_report.py
```

Use the PowerPoint builder from the workspace root:

```sh
  NODE_PATH="$HOME/.cache/codex-runtimes/codex-primary-runtime/dependencies/node/node_modules" \
    "$HOME/.cache/codex-runtimes/codex-primary-runtime/dependencies/node/bin/node" \
    tools/status-report/build_cloud_status_report_pptx.mjs
```

The builder writes:

```text
.artifacts/status-reports/YYYY-MM-DD/
  realtek_video_iot_cloud_status_report.docx
  realtek_video_iot_cloud_status_report.pptx
  figures/
  rendered/
  pptx-rendered/
  pptx-layout/
```

Do not commit generated report output. Commit only framework changes, source
material indexes, and builder changes.

## Recreate The Same PPTX Next Time

The current PowerPoint status report is generated from tracked source files,
not from conversation memory. To recreate a deck with the same guideline,
context, slide order, and Realtek master style, start from these files in this
order:

1. [`guidelines.md`](guidelines.md) - report-writing rules, business context,
   schedule policy, security policy, and evidence rules.
2. [`templates/cloud-status-report-pptx-layout.md`](templates/cloud-status-report-pptx-layout.md)
   - fixed slide sequence, topic transitions, proof-object expectations, and
   layout rules.
3. [`materials.md`](materials.md) - design assets, screenshots, deployment
   evidence, and source index.
4. [`master_slide/design-guidelines.md`](master_slide/design-guidelines.md) -
   designer-facing Realtek master rules.
5. [`master_slide/SKILL.md`](master_slide/SKILL.md) - AI/LLM-facing slide
   generation rules.
6. [`../../tools/status-report/report_model.py`](../../tools/status-report/report_model.py)
   - shared report data model: `CORE_MESSAGE`, schedule data, material list,
   Linode checks, portal web screenshot path, and generated figures.
7. [`../../tools/status-report/build_cloud_status_report_pptx.mjs`](../../tools/status-report/build_cloud_status_report_pptx.mjs)
   - actual PPTX builder and slide implementation.

Stable context that must be preserved unless the report owner explicitly
changes it:

- Core purpose: the cloud supports bottom-up business module sales by giving
  users, developers, and customer PoC teams a convenient and secure cloud
  network architecture.
- Audience: cross-unit leaders; assume some leaders may not already know why
  this cloud exists.
- Narrative order: presenter context, major-topic map, cloud relationship,
  module product path, two cloud types, operational progress, portal web,
  technical/security design, deployment/operation/evidence, review gate.
- Schedule context: project start is 2026-05-01; the Aug.1 milestone is
  completing the 50,000-device IoT loading test and the 5,000 video-camera
  loading test. After that, plan one month of alpha test including SDK, one
  month of beta test including SDK and pilot customer, then public release.
- Scaling context: scaling architecture is designed in, but autoscaling/dynamic
  scaling implementation is deferred until after loading-test evidence.
- Security context: explain STRIDE visually before PKI implementation; frame
  PKI as identity, entitlement, audit, revocation, and lifecycle management.
- Portal context: `rtk_cloud_frontend` is the public website, documentation /
  manual portal, SEO/content layer, CTA/lead capture, and sales follow-up
  surface. Keep it separate from operational cloud runtime.

Generated PPTX and QA images remain under `.artifacts/status-reports/YYYY-MM-DD/`.
If a fresh portal screenshot is needed, capture `https://webtest.mgmeet.io`,
crop the first viewport to a 16:9 image, and save it as
`.artifacts/status-reports/YYYY-MM-DD/figures/portal-webtest-home-hero.png`
before running the PPTX builder. If the screenshot is absent, the builder falls
back to the tracked frontend hero image.

## Report Shape

The current report structure is documented in
[`templates/cloud-status-report-outline.md`](templates/cloud-status-report-outline.md).
The PPTX slide sequence is documented in
[`templates/cloud-status-report-pptx-layout.md`](templates/cloud-status-report-pptx-layout.md).
Writing standards are documented in [`guidelines.md`](guidelines.md).
It is intentionally stable so the same skeleton can be reused for weekly
management reports:

- 封面 / 核心管理訊息
- Presenter context：Kevin Huang IoT cloud / video cloud / digital marketing background
- 開場：本次報告 major topics
- 第一部分：Cloud business context
  - Cloud relationship / tenant structure
  - Cloud as module product path
  - Operational IoT / Video Cloud vs Portal Web / Marketing Cloud
- 第二部分：Operational Cloud 目前進度與 8 月路徑
  - 目前狀態總結
  - Schedule path
  - Loading test readiness
  - Video schedule lane
  - Current staging vs production target architecture
- 第三部分：Portal Web / Digital Marketing
  - Portal web / digital marketing
- 第四部分：Operational Cloud 技術設計與安全管理
  - WebRTC / Video Storage Management
  - MQTT / Device Shadow Management
  - STRIDE security implementation explainer
  - Security / PKI trust management
  - Threat model / cyber security review
- 第五部分：Deployment、操作流程與 Evidence
  - Linode Staging 部署與設定
  - 操作畫面與使用流程
  - 決策、風險與 Evidence
- 審閱清單
- Appendix：素材與來源索引

## Schedule Policy

Every report must show the schedule path from project start on 2026-05-01 to
the 2026-08-01 loading-test milestone: 50,000 IoT devices plus 5,000 video
cameras. The schedule section must state the current position, weekly gate,
next gate, risk, and `on track` / `at risk` / `blocked` judgment based on
evidence.

After the Aug.1 loading-test milestone, the report should show the release path:
August alpha test including SDK, September beta test including SDK and pilot
customer, then public release. If the report covers IoT Video / WebRTC / video
storage, include a separate video schedule lane that leads to the same Aug.1
5,000-camera loading-test milestone.

The current architecture may be described as scaling-ready when evidence
supports scale-out boundaries, service separation, multi-host readiness, and
metrics. Dynamic scaling implementation is still deferred until after the
Aug.1 loading test. Before that point, reports should discuss scaling
architecture direction, capacity evidence, bottleneck visibility, and operator
runbook status; do not claim autoscaling or elastic scaling is implemented for
the August release.

## Language Policy

Generated management reports use Traditional Chinese by default. Keep literal
repository names, API names, endpoint paths, commands, product names, and status
labels such as `PASS`/`FAIL`/`BLOCKED` in English when those terms are the
source-of-truth wording.

## Material Policy

Use existing submodule design assets before creating new diagrams. The report
body should contain only the screenshots needed to explain current operations;
the appendix should carry the complete source index so future weekly reports can
reuse the same materials.

Tracked source indexes:

- [`guidelines.md`](guidelines.md)
- [`materials.md`](materials.md)
- [`master_slide/README.md`](master_slide/README.md)
- [`master_slide/design-guidelines.md`](master_slide/design-guidelines.md)
- [`master_slide/SKILL.md`](master_slide/SKILL.md)
- `repos/rtk_cloud_admin/docs/assets/webui-design/`
- `repos/rtk_cloud_client/docs/mockups/`
- `repos/rtk_cloud_frontend/static/assets/`

## Master Slide Policy

When producing PowerPoint or slide-style status reports, follow the company
master in [`master_slide/powerpoint_master.pptx`](master_slide/powerpoint_master.pptx).
Use [`master_slide/design-guidelines.md`](master_slide/design-guidelines.md) for
designer-facing rules and [`master_slide/SKILL.md`](master_slide/SKILL.md) for
AI/LLM slide-generation instructions. Reusable assets extracted from the master
live under [`master_slide/assets/`](master_slide/assets/).

## Cloud Relationship Policy

Every management-facing report should explain the cloud relationship when it
discusses platform strategy, brand-cloud administration, customer onboarding, or
end-user product flow. Use the model `System Root / Realtek Platform -> Brand
Cloud -> brand users / end users / devices`. Account Manager is the source of
truth for platform-admin/root identity, brand-cloud organizations, users,
memberships, registry devices, provisioning operations, and authoritative audit.
Video Cloud owns runtime activation, device transport, WebRTC/video,
MQTT/shadow, OTA/media/telemetry/log runtime behavior, and readiness facts.
Admin Console is a dashboard/BFF and must not be described as the authoritative
brand-cloud or device store.

## Portal Web / Digital Marketing Policy

When the report discusses `rtk_cloud_frontend`, describe it as the public
marketing website, documentation/manual portal, and lead-generation layer. It
should connect SEO readiness, content development, visitor behavior analytics,
CTA conversion, contact lead capture, and sales follow-up to sales improvement.
Keep this separate from product telemetry and operational cloud-console
readiness. Use aggregate analytics/lead evidence only; do not expose raw lead
payloads, lead emails, raw analytics rows, full referrer URLs, search query
text, `ADMIN_TOKEN`, OpenAI keys, or other secrets.

## WebRTC / Video Storage Policy

When the report discusses Video Cloud, camera demo, stream health, media, or
storage readiness, separate WebRTC live-video readiness from video-storage
readiness. WebRTC covers APP-offer/device-answer signaling,
`/api/request_webrtc`, `/answer`, `/close`, owner transport delivery,
TURN/ICE configuration, session lifecycle, and stream-health evidence. Video
storage covers snapshot/media upload, metadata and clip id, listing/info
lookup, download authorization, byte-range behavior, delete behavior,
retention/backup status, and storage configuration category. Use
`repos/rtk_cloud_contracts_doc/STREAMING.md` and
`repos/rtk_cloud_contracts_doc/SNAPSHOT_AND_MEDIA.md` as the primary source
documents.

## Security / PKI Policy

When the report discusses device onboarding, SDK authentication, enterprise
readiness, or production-readiness trust controls, include the Security / PKI
trust-management section from
[`guidelines.md`](guidelines.md#security--pki-management). Frame PKI as
security management: device identity, factory enrollment, service entitlement,
audit, revocation, and lifecycle governance. Do not include private keys, raw
CSR PEM, raw certificate PEM, CA signing material, bearer tokens, or secret
paths.

## Threat Model / Cyber Security Policy

When the report discusses production readiness, public endpoints, deployment,
device identity, customer data, MQTT, WebRTC, media, portal analytics, or release
risk, include threat model / cyber security review progress. Use
`cyber_security/` as the workspace source for STRIDE assumptions, source index,
threat model, risk matrix, and evidence notes. Report method, scope, top
critical/high risks, open questions, next review focus paths, and mitigation or
evidence status. Do not treat endpoint health as security sign-off, and do not
include raw logs, secrets, tokens, DSNs, private keys, raw certificates,
customer data, raw lead data, or unredacted artifacts.

## MQTT / Device Shadow Policy

When the report discusses IoT Cloud MQTT, device state, command routing, or
large-scale IoT loading tests, separate traditional MQTT transport from IoT
device shadow. MQTT transport covers broker/topic connectivity, owner
transport, command delivery, event/log ingress, and broker evidence. Device
shadow covers cloud-held state management: `state.desired`, `state.reported`,
`state.delta`, `version`, `clientToken`, lifecycle bootstrap, deactivation
behavior, and unprovision tombstones. Use
`repos/rtk_cloud_contracts_doc/DEVICE_TRANSPORT.md` and
`repos/rtk_cloud_contracts_doc/DEVICE_SHADOW.md` as the primary source
documents.

## Evidence Policy

Live Linode health checks are acceptable for a status report, but they are not a
replacement for formal sign-off. Production or private-cloud readiness must use
the workspace evidence wrapper in
[`../product-level-evidence.md`](../product-level-evidence.md).

Never include secrets, DSNs, bearer tokens, Linode tokens, DNS credentials,
object storage keys, private keys, or raw customer data in a status report.
