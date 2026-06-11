# Cloud Status Report PPTX Layout

Status: template.

Use this slide layout for weekly Realtek Video / IoT Cloud PPTX status reports.
Apply the writing rules in `../guidelines.md` and the company master rules in
`../master_slide/design-guidelines.md`. Generated slide body content uses
Traditional Chinese by default; literal repo, API, endpoint, command, and status
labels remain English.

## Builder

Run from the workspace root:

```sh
NODE_PATH="$HOME/.cache/codex-runtimes/codex-primary-runtime/dependencies/node/node_modules" \
  "$HOME/.cache/codex-runtimes/codex-primary-runtime/dependencies/node/bin/node" \
  tools/status-report/build_cloud_status_report_pptx.mjs
```

Output:

```text
.artifacts/status-reports/YYYY-MM-DD/
  realtek_video_iot_cloud_status_report.pptx
  pptx-rendered/
    slide-01.png
    ...
    contact-sheet.png
    manifest.json
  pptx-layout/
```

## Content Contract

This deck is a weekly management status deck, not a generic cloud feature
presentation. When recreating the deck, keep this story spine:

1. Start with what the leaders need to understand: why this cloud exists and
   what the report will cover.
2. Explain the cloud relationship early: Realtek Platform Root -> Brand Cloud
   -> brand users / end users / devices.
3. Explain that the cloud exists to support bottom-up business module sales and
   to connect backend technology with target customer fit, customer PoC, and
   sales enablement.
4. Separate the two cloud topics: Operational IoT / Video Cloud and Portal Web
   / Marketing Cloud.
5. Define who the cloud is for and what each release gate means before going
   deep into implementation details.
6. Show where the project is on the May 1 to Aug.1 loading-test milestone path,
   then the alpha, beta, and public release path before going deep into
   implementation details.
7. Use transition slides when changing topics: operational progress, portal
   web, technical/security design, deployment/evidence.
8. Present important numbers as timelines, milestone lanes, readiness visuals,
   or charts before using tables.
9. Treat security as management controls: STRIDE explainer graphic, PKI trust
   lifecycle, HSM/PKCS#11 signer custody boundary, then cyber-security review
   progress.
10. Treat Linode health checks, screenshots, and sample flows as status evidence,
   not production or security sign-off.

Do not remove the major-topic map, cloud relationship page, two-cloud
explanation, schedule path, portal web introduction, STRIDE graphic, PKI page,
HSM signer design page, threat-model progress page, Linode evidence pages, or
appendix source index unless the report owner explicitly changes the framework.

## Slide Sequence

| # | Slide | Primary proof object |
| --- | --- | --- |
| 1 | Cover / 核心管理訊息 | Master cover background, Realtek logo, core message. |
| 2 | 本次報告要先建立共同上下文 | Major topic map: why cloud exists, schedule/release path, portal/sales loop, technical/security design, deployment/cost/support. |
| 3 | Cloud Relationship / Tenant Structure | Realtek Platform Root -> Brand Cloud -> users/devices diagram. |
| 4 | Why We Need This Cloud | Business-driver chart plus first-phase priority chart; no long goal list. |
| 5 | Customer / Use Case Fit | Target-customer cards linking customer need, cloud proof, and IoT module sales path. |
| 6 | Cloud 是 module product path | Executive summary claims plus product-to-KPI flow. |
| 7 | Two Cloud Types in This Report | Difference between Operational IoT/Video Cloud and Portal Web / Marketing Cloud. |
| 8 | Transition：Operational Cloud 目前進度與 8 月路徑 | Topic break before status, schedule, loading test, video gate, and current-vs-production target. |
| 9 | 目前狀態總結 | Status summary table plus schedule snapshot. |
| 10 | Schedule Path：May 1 到 Public | Timeline / milestone lane with `目前位置`, Aug.1 loading pass, August alpha, September beta, and public path. |
| 11 | Release Gate Definition | Visual gate definition for Aug.1 loading pass, alpha, beta, and public path. |
| 12 | Loading Test Readiness | Aug.1 50,000-device + 5,000-video-camera readiness matrix. |
| 13 | Video Schedule Lane | 5,000 video-camera loading-test path to Aug.1. |
| 14 | Current vs Target Architecture | Current staging vs Production Target, with scaling facility designed in staging and auto scaling reserved for production deployment. |
| 15 | Transition：Portal Web / Digital Marketing | Chapter title page explaining the switch from operational cloud to marketing/portal cloud. |
| 16 | Portal Web：市場入口與開發者導流 | Live `webtest.mgmeet.io` screenshot plus why-we-need-it and feature summary. |
| 17 | Portal Web / Digital Marketing | Observation -> content decision -> sales action -> result/learning loop, with explicit linkage back to IoT module selling. |
| 18 | Transition：Operational Cloud 技術設計與安全管理 | Topic break before runtime capability, PKI/security, and threat-model evidence. |
| 19 | WebRTC / Video Storage | WebRTC signaling flow plus video-storage readiness matrix. |
| 20 | MQTT / Device Shadow | MQTT transport vs IoT shadow state-management table. |
| 21 | STRIDE：Security implementation 的檢查語言 | Hub-and-spoke STRIDE graphic mapping six threat categories to cloud implementation controls. |
| 22 | Security / PKI Management | Device trust-chain and security-management matrix. |
| 23 | HSM / PKCS#11 Signer Design | Key-custody boundary for certissuer and token signing; service gets signing capability, not raw private key material. |
| 24 | Threat Model / Cyber Security Review | STRIDE risk matrix and next review focus. |
| 25 | Transition：Deployment、操作流程與 Evidence | Topic break before Linode runtime, health/config boundary, operation screenshots, and SDK flow. |
| 26 | Linode Staging Runtime Shape | Runtime topology plus component responsibility table. |
| 27 | Initial Operation Cost View | Current Linode monthly run-rate estimate plus AWS commercial-pilot difference view from `docs/cost`, focused on CloudHSM on/off and Robust Design on/off. |
| 28 | AWS Unit Cost Per Month | Raw monthly total divided by users/devices plus weighted 10% user / 90% device unit-cost view. |
| 29 | AWS Cost Calculation Detail 1/2 | Base-service line-item math from `docs/cost`: ECS, RDS, IoT Core, NAT, CloudWatch Logs, Secrets, S3, KMS, and subtotal. |
| 30 | AWS Cost Calculation Detail 2/2 | Scenario equations, CloudHSM/robust deltas, optional support-plan calculation, and per-user/per-device formulas. |
| 31 | Linode Health & Configuration Boundary | Live health table and safe configuration boundary. |
| 32 | Operation Flow Overview | Demo journey flow. |
| 33 | Admin Operation Screenshots | 2x2 Admin screenshot grid. |
| 34 | SDK / Sample App Flow | Sample app flow screenshot plus evidence-purpose table. |
| 35 | Decision / Support Needed | Alpha-readiness support board: account/payment ownership, official Android/iOS market publishing accounts, operation backup, alpha internal testers, beta pilot customer, and milestone impact. |
| 36 | Ongoing Operation / Development Coverage | Ongoing baseline estimate separate from temporary alpha/beta testers: backend/service owner, DevOps/SRE, SDK support, QA/load test, security review, and FAE/pilot support. |
| 37 | Appendix：素材與來源索引 | Dense material/source/status table. |
| 38 | Thank You / Review Gate | Master-style closing slide and checklist summary. |

## Layout Rules

- Use one main claim and one dominant proof object per slide.
- Apply the non-AI sense writing rules in `../guidelines.md`: avoid formulaic
  contrast sentences such as "這不是 A，而是 B"; write direct management
  claims tied to evidence, risk, or next action.
- Put a major-topic map immediately after the cover when the report is for
  leaders who may not already know why the cloud exists.
- Put one explicit "Why We Need This Cloud" page before detailed module/product
  path slides. Use a business-driver chart and first-phase priority visual;
  highlight PoC onboarding, core runtime services, and demo/sales handoff.
- Put one customer/use-case fit page near the beginning. It should identify
  likely customer types, what each one needs from the cloud, and how that links
  back to module selling, PoC, and design-in.
- Put one release-gate definition page after the schedule timeline. It should
  define Aug.1 loading-test pass, alpha, beta, and public path using evidence
  criteria, not only dates.
- Use transition slides between different topics so the audience can tell when
  the narrative changes from business context, to schedule, to portal marketing,
  to technical/security design, and then to deployment/evidence.
- Use charts, timelines, progress visuals, or diagrams before numeric tables.
- Keep dense tables for readiness, health, risk, decision, and appendix slides.
- Preserve screenshot aspect ratio; do not stretch Admin, SDK, or frontend
  assets.
- Use a real Portal Web screenshot when introducing public-facing web purpose,
  and explain why the site exists before showing SEO/content/lead funnel.
- Present Portal Web metrics as a relationship loop, not as weak standalone
  indexes. The slide should connect customer observation, required content,
  sales action, result/learning, and IoT module sales message refinement.
- Treat endpoint health as status evidence only, not production or security
  sign-off.
- Keep operation-cost discussion to one or two lightweight slides unless requested:
  current Linode staging monthly run-rate estimate, AWS pilot estimate from
  `docs/cost/aws-pricing-sources.md`, clear CloudHSM and Robust Design cost
  differences, per-user/per-device unit cost per month, post-loading-test
  estimate timing, and caveats that AWS numbers are a planning snapshot rather
  than an actual AWS bill or committed quote.
- When presenting security implementation, add one STRIDE explainer page before
  PKI implementation details so non-security leaders understand what is being
  checked and how controls map to implementation.
- Prefer a graphic for STRIDE, not a dense table: six outer risk categories
  around central implementation controls such as PKI identity, ACL, audit,
  revocation, rate limiting, and evidence scrubbing.
- Mark failed or timed-out live checks as `FAIL` or `BLOCKED`; do not reuse old
  snapshots.
- Keep dynamic scaling as architecture direction only unless implementation and
  load-test evidence exist.
- Do not include secrets, DSNs, tokens, private keys, raw CSR/cert PEM, raw lead
  data, raw customer data, or raw media.
