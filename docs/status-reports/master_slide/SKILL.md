# Realtek Master Slide Skill

Use this skill when generating Realtek Cloud status-report slides, executive
updates, or presentation artifacts that must follow the company master in
[`powerpoint_master.pptx`](powerpoint_master.pptx).

## Source Of Truth

- Master deck: `docs/status-reports/master_slide/powerpoint_master.pptx`
- Designer guideline:
  `docs/status-reports/master_slide/design-guidelines.md`
- Report guideline:
  `docs/status-reports/guidelines.md`
- PPTX status-report layout:
  `docs/status-reports/templates/cloud-status-report-pptx-layout.md`
- Report data model:
  `tools/status-report/report_model.py`
- PPTX builder:
  `tools/status-report/build_cloud_status_report_pptx.mjs`
- Extracted assets: `docs/status-reports/master_slide/assets/`
- Asset contact sheet:
  `docs/status-reports/master_slide/assets/media-contact-sheet.png`

## Required Visual System

- Canvas: 16:9 widescreen, 10.00 x 5.62 in.
- Traditional Chinese font: `微軟正黑體` / Microsoft JhengHei.
- Simplified Chinese font: `微软雅黑` / Microsoft YaHei.
- English and numbers: Arial.
- Primary colors: `#4A66AC`, `#242852`, `#ACCBF9`, `#629DD1`, `#297FD5`,
  `#5AA2AE`, `#7F8FA9`, `#FFFFFF`, `#000000`.
- Style: light Realtek semiconductor / AI technology, white content space,
  blue/cyan accents, precise technical diagrams, restrained corporate tone.

## Required Slide Patterns

- Cover: Realtek logo, light technology background, title, name/date, copyright.
- Executive summary: compact status table and one management message.
- Schedule: timeline, Gantt, or milestone-lane chart showing current position
  and next gate. Do not default to a plain schedule table.
- Readiness/KPI: blue-header table, concise evidence, status labels.
- Architecture/security: one dominant diagram with short supporting notes.
- Operation screenshots: screenshot plus caption and evidence note.
- Appendix: dense but readable source/evidence table.

## Numeric Presentation Rule

If numbers need to be presented, use a chart or visual encoding before using a
number table. This includes schedules. Use tables only for evidence matrices,
endpoint checks, risk/decision tracking, or exact audit detail.

## Realtek Cloud Content Rules

For status reports, preserve the report guideline structure:

- Major-topic map immediately after the cover when the audience may not already
  know why the cloud exists.
- Cloud relationship / tenant structure before detailed status: Realtek
  Platform Root -> Brand Cloud -> brand users / end users / devices.
- Cloud as the module product path for bottom-up business module sales.
- Two-cloud explanation: Operational IoT / Video Cloud versus Portal Web /
  Marketing Cloud.
- Transition slides between operational progress, portal web, technical/security
  design, and deployment/evidence.
- Schedule and loading-test path.
- Portal web / digital marketing: `rtk_cloud_frontend` as marketing website,
  documentation/manual portal, SEO/content layer, first-party behavior
  analytics, CTA/lead conversion, and sales improvement loop.
- Threat model / cyber security review: STRIDE scope, trust boundaries, top
  critical/high risks, open questions, review focus paths, mitigation/evidence
  status, and redaction rules.
- Dynamic scaling architecture direction as already designed in where evidence
  supports it, while dynamic scaling implementation is deferred until after the
  loading test and is not an August-release commitment.
- WebRTC / video storage readiness.
- MQTT transport / device shadow management.
- STRIDE explainer graphic before Security / PKI management.
- Security / PKI management as identity, entitlement, audit, revocation, and
  lifecycle governance.
- Linode deployment/configuration evidence.
- Operation screenshots and material appendix.
- Decision/support, risk burn-down, and evidence index.

Do not collapse these into generic "cloud features". Keep each capability and
its evidence separate.

## Asset Rules

- Use `assets/image1.png` or `assets/image7.jpg` for cover/chapter backgrounds.
- Use `assets/image2.png`, `assets/image3.png`, or `assets/image6.png` for the
  Realtek logo. Preserve aspect ratio.
- Use `assets/image10.jpeg` or `assets/image13.jpeg` for blue footer/header
  strips.
- Use side imagery (`image4.png`, `image5.jpg`, `image8.jpg`, `image9.png`,
  `image12.png`) only as framing, not under critical text.

## Safety Rules

- Do not include secrets, DSNs, tokens, private keys, raw CSR/certificate PEM,
  object storage keys, customer data, or unapproved raw media.
- Do not claim production readiness without evidence.
- Do not use a dark theme, decorative gradients, or generic SaaS cards.
- Do not change the company fonts or logo proportions.

## AI / LLM Recreation Rules

When an AI or LLM recreates the weekly PPTX status report, read these files
before changing slide order or report context:

1. `docs/status-reports/README.md`
2. `docs/status-reports/guidelines.md`
3. `docs/status-reports/templates/cloud-status-report-pptx-layout.md`
4. `docs/status-reports/materials.md`
5. `tools/status-report/report_model.py`
6. `tools/status-report/build_cloud_status_report_pptx.mjs`

Do not rely on prior chat context. The tracked files above are the source of
truth for the report purpose, core message, schedule, portal web context,
security implementation context, and slide sequence.

Before exporting, run a tone pass using the non-AI sense rules in
`docs/status-reports/guidelines.md#91-non-ai-sense-writing-rules`. Remove
formulaic contrast sentences such as "這不是 A，而是 B", empty marketing
adjectives, and repeated slide-openers. Prefer direct management claims tied to
evidence, risk, owner, or next action.

Use the builder when possible instead of hand-making a new deck. Generated
output belongs under `.artifacts/status-reports/YYYY-MM-DD/`; commit only
guidelines, source indexes, builders, templates, and master assets.
