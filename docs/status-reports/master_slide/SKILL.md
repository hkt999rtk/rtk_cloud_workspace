# Realtek Master Slide Skill

Use this skill when generating Realtek Cloud status-report slides, executive
updates, or presentation artifacts that must follow the company master in
[`powerpoint_master.pptx`](powerpoint_master.pptx).

## Source Of Truth

- Master deck: `docs/status-reports/master_slide/powerpoint_master.pptx`
- Designer guideline:
  `docs/status-reports/master_slide/design-guidelines.md`
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

- Schedule and loading-test path.
- Cloud relationship / tenant structure: Realtek Platform Root -> Brand Cloud
  -> brand users / end users / devices, with source-of-truth boundaries.
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
- Security / PKI management.
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
