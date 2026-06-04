# Cloud Status Report Outline

Status: template.

Use this outline for weekly Realtek Video / IoT Cloud status reports.
Apply the writing rules in `../guidelines.md` when filling each section.
The generated report body must use Traditional Chinese by default; keep only
literal product, repo, API, endpoint, command, and status-label names in English.

## 封面 / 核心管理訊息

- 報告標題與日期。
- 一句每週可更新的核心管理訊息，說清楚商業理由、目前執行狀態、資源與維運現實。
- 核心管理訊息必須固定出現在第一頁，且要在摘要正文之前。
- 核心管理訊息之後必須放「目前狀態總結」。
- 目前狀態總結格式：三欄表格 `面向` / `目前狀態` / `下一步或風險`，三到五列，每格一句話，至少涵蓋 deployment、product/demo evidence、operations/readiness、下一個 milestone 或 resource gap。
- 一列 schedule snapshot，標示目前在 May 1 到 Aug.1 loading-test milestone，再到 alpha、beta、public 路徑上的位置。
- 一張 product-to-KPI 視覺圖。

## 第一部分：摘要

- 一頁結論。
- 為什麼需要這個 cloud。
- 技術成果如何連到 business KPI。
- Loading test 或 milestone 目標。
- Schedule snapshot：目前位置、本週目標、下個 gate、風險、判定。
- 目前高階架構。
- 目前部署狀態。
- 已完成 foundation 與下一步。

## 第二部分：時程與 Loading Test 路徑

- Project start：2026-05-01。
- Target：2026-08-01 pass 50,000 IoT devices + 5,000 video cameras loading test。
- Post-loading-test release path：August alpha test（including SDK）、September beta test（including SDK and pilot customer）、then public release path。
- Video target：如果本週報告涵蓋 IoT Video / WebRTC / video storage，另外列出 2026-08-01 5,000 video-camera loading-test milestone；video lane 要說明 WebRTC setup、TURN behavior、storage path、stream health 與 metrics。
- Dynamic scaling：目前架構可說明已 design-in scaling-ready boundaries / scale-out direction，但 August release 不實作 dynamic scaling；八月前報告只描述 architecture direction、capacity evidence、multi-host readiness、bottleneck visibility 和 runbook，不可宣稱 autoscaling / elastic scaling implemented。
- 目前位置：依報告日期與實際 evidence 更新，不可只用樂觀日期推進。
- Timeline / Gantt / milestone-lane chart：標示 May 1 到 Aug.1 loading-test milestone，再接 alpha、beta、public，並清楚標出 `目前位置`；schedule 不要只用純數字表格呈現。
- Milestone detail table：只作為輔助明細，內容包含 May kickoff、May foundation、late-May/early-June load-test preparation、June validation、July scale rehearsal、Aug.1 50,000-device + 5,000-camera pass、August alpha、September beta、public path。
- Video schedule lane：June video readiness foundation、July video profile、late-July 5,000-camera rehearsal、Aug.1 5,000-camera pass。
- 本週 gate：本週必須完成或驗證的可量測項目。
- 下個 gate：下一個可驗證 milestone。
- Schedule risk：用 `on track` / `at risk` / `blocked`，並說明原因。
- Loading Test Readiness Matrix：test runner/profile、test fleet/data、metrics/thresholds、infra/multi-host、broker/database/storage、report evidence。

## 第三部分：Cloud / Product / KPI 細節

- 架構細節。
- Cloud relationship diagram：Realtek Platform Root -> Brand Cloud -> brand users / end users / devices，並標明 Account Manager、Video Cloud、Admin Console、Frontend、SDK/app/firmware 的 source-of-truth 邊界。
- Portal Web / Digital Marketing：說明 `rtk_cloud_frontend` 是 marketing website、docs/manual portal、lead generation layer；涵蓋 SEO、content development、visitor behavior analytics、CTA conversion、lead capture、sales improvement loop。
- Portal funnel / content map：traffic/source -> page engagement -> CTA click -> contact lead -> sales follow-up；homepage -> features -> docs/manual -> contact。
- Current-vs-target architecture：目前 staging/runtime/evidence/operations readiness 對照 Production Target；scaling architecture 可說明已 design-in，但 autoscaling 只在 production deployment 評估。
- Module-to-cloud-to-commercial KPI 路徑。
- KPI framework：技術、產品、商業、維運。
- WebRTC / Video Storage Management：分開說明 live WebRTC signaling readiness 和 stored-media/video-storage readiness；WebRTC 是 APP-offer/device-answer、TURN/ICE、owner transport、session lifecycle，video storage 是 snapshot/media upload、metadata、download auth、byte range、retention/backup。
- WebRTC flow visual：app offer -> `/api/request_webrtc` -> owner transport `webrtc_offer` -> device answer -> `/answer` -> app media negotiation -> `/close`。
- Media capability table：live stream、snapshot upload、clip/media upload、media listing、download、delete、retention、backup/restore 分開列 current status / evidence / gap / risk。
- MQTT / Device Shadow Management：分開說明傳統 MQTT transport 和 IoT shadow state management；MQTT 是 broker/topic/owner transport，shadow 是 desired/reported/delta/version/lifecycle state governance。
- MQTT/shadow topic-surface table：`devices/<device_id>/...` command/event/log topics 與 `$vc/devices/{devid}/shadow/...` get/update/delete/accepted/rejected/delta/documents topics 分開列。
- Security / PKI trust management：把 PKI 說明成 device identity、factory enrollment、service entitlement、audit、revocation、lifecycle governance，而不是只寫 mTLS 技術。
- PKI trust-chain visual：factory/MES or fixture -> factory enrollment -> certissuer -> device certificate -> mTLS token bootstrap -> service-options ACL -> runtime services。
- Security management matrix：identity、key custody、certificate issuance、entitlement、token binding、revocation、audit、lifecycle handling。
- PKI readiness evidence：以 `implemented` / `staging` / `not verified` / `blocked` 標示，不可把未驗證設計寫成 production-ready。
- Threat Model / Cyber Security Review：列出 STRIDE threat model 進度、trust boundaries、top critical/high risks、open questions、review focus paths、mitigation/evidence status。
- Cyber security review table：area、current status、evidence、gap / next check、risk；至少涵蓋 secrets、auth subject binding、PKI/mTLS、MQTT auth、WebRTC capacity、media/download、Admin BFF、public listener exposure、evidence redaction。
- API / cloud pattern。
- Product features。
- SDK / reference app 狀態。
- Onboarding / provisioning flow。
- Loading test plan。
- 維護與維運現實。

## 第四部分：操作畫面與使用流程

- Admin Fleet Health Overview.
- Admin Devices + Detail Drawer.
- Admin Firmware & OTA.
- Admin Stream Health.
- SDK/sample app screen flow.
- Product/frontend architecture visual if useful for external positioning.
- Demo flow / user journey：Admin overview -> abnormal device -> device drawer -> firmware/stream/telemetry/readiness -> SDK sample provisioning/config/debug -> loading test scale validation.

Keep the body selective. Put the full material catalog in the appendix.

## 第五部分：Linode Staging 部署與設定

- Linode 在本報告中的定位：較基礎的 VM / infrastructure 服務，不是 AWS-style managed-service stack。
- 說明可搬移性：PostgreSQL、MQ/message queue、broker、reverse proxy、runtime 等服務由我們在 VM/service layer 自行架設與管理，避免過度依賴 AWS-native 架構，未來較容易移動到 AWS、GCP、Azure、阿里雲或其他平台雲。
- Public endpoints 與目前 runtime shape。
- 非敏感 configuration boundaries。
- Dynamic scaling status：預設 `architecture supports future scaling; implementation deferred until after loading test`，除非已有實作與 loading-test evidence。
- 附 timestamp 的 live health check table。
- Production-ready gaps。

## 第六部分：決策、支援、風險與 Evidence

- Alpha readiness support board：account/payment ownership、operation backup、temporary alpha internal testers、temporary beta pilot customer。
- Account/payment ownership 必須明確寫出 DNS、Linode billing、credit-card payment、mail/service accounts 是否已從 Kevin personal account 轉到 company-managed 或 approved third-party account；alpha 前不能留成 single point failure。
- Temporary alpha internal testers：建議 4-6 位 real human testers，其中至少 3-4 位 developer / firmware / app 類型；auto test 補 quantity，不取代 human developer feedback。
- Temporary beta pilot customer：beta 前需要 1-2 個 pilot customer 或 partner use case；這是 beta window 的外部驗證，不等於長期營運人頭。
- Ongoing operation/development coverage：另用一頁估算 public 前後需要留下來的 backend/service owner、DevOps/SRE、SDK support、QA/load test、security review、FAE/pilot support coverage。
- Risk burn-down table：risk、current status、mitigation、owner/dependency、trend。
- Evidence index：live endpoint、repo/PR/commit、screenshot/design、load-test report、deployment/configuration、production-readiness、missing/blocked evidence。
- Resource plan 預設不展開成詳細人力或預算表；Page 30 只列 alpha/beta 前會影響 milestone 的 ownership/support。

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
- signed media URLs with secret query material
- raw customer-visible media unless sanitized and approved
- raw lead payloads, lead emails, analytics event rows, full referrer URLs, search query text
- raw customer data

## 審閱清單

- 摘要可在五分鐘內看懂。
- Schedule path 和目前位置清楚。
- 重要數字優先用 chart / timeline / progress visual 呈現，純表格只作為 evidence 或明細。
- Loading Test Readiness Matrix 有列出 50,000-device + 5,000-video-camera target 前的 gates。
- 細節符合目前 repo 與 deployment 狀態。
- 技術工作有連到 AmebaPRO / module commercial KPI。
- WebRTC live video 與 video storage/media 沒有混在一起；signaling、TURN/ICE、owner transport、stream-health、snapshot/media upload、download auth、retention/backup evidence 各自清楚。
- Platform cloud / Brand Cloud / end user cloud relationship 清楚，且沒有把 Admin Console 當成 Account Manager 或 Video Cloud 的 source of truth。
- Portal web / digital marketing 章節有說明 SEO、content development、behavior analytics、lead conversion 與 sales improvement，而且沒有暴露 raw lead 或個資。
- MQTT transport 與 IoT device shadow 沒有混在一起；broker/topic evidence、owner transport、desired/reported/delta/version/lifecycle evidence 各自清楚。
- 操作截圖能證明 demo 與 customer workflow readiness。
- Deployment / configuration 狀態避免 secrets 與 overclaiming。
- Decision/support、risk burn-down、evidence index 能讓管理層知道下一步與缺口。
- Security / PKI 章節有說明安全管理意義：identity、factory issuance、entitlement、audit、revocation、unprovision vs deactivation。
- Threat model / cyber security review 章節有列 STRIDE 覆蓋、top risks、open questions、review progress，且未把 health check 當成 security sign-off。
- Production-ready gaps 明確列出。

## Appendix：素材與來源

- Screenshot / material source table。
- 完整 reusable material directories。
- Internal references and runbooks。
- Cloud relationship source references：`rtk_cloud_contracts_doc/BRAND_CLOUD_ADMIN.md`、`PRODUCT_ONBOARDING.md`、`AUTHORIZATION.md`、`PROVISION.md`、`rtk_cloud_admin/docs/SPEC.md`、`platform-brand-cloud-management-design.md`。
- Portal web / digital marketing source references：`rtk_cloud_frontend/README.md`、`docs/SPEC.md`、`docs/ANALYTICS.md`、`docs/API_REFERENCE.md`、`docs/MANUAL_CONTENT_SYSTEM.md`。
- WebRTC / video storage source references：`rtk_cloud_contracts_doc/STREAMING.md`、`SNAPSHOT_AND_MEDIA.md`、`DEVICE_TRANSPORT.md`、`AUTH.md`、`AUTHORIZATION.md`、`rtk_cloud_client/docs/RTK_VIDEO_CLOUD_MANUAL_INTEGRATION.md`。
- MQTT / shadow source references：`rtk_cloud_contracts_doc/DEVICE_TRANSPORT.md`、`DEVICE_SHADOW.md`、`PROVISION.md`、`API_USAGE.md`、`rtk_cloud_client/docs/TRANSPORTS.md`。
- PKI / security source references：`rtk_cloud_contracts_doc/AUTH.md`、`PROVISION.md`、`rtk_video_cloud/docs/cert-issuer-server-design.md`、`factory-enrollment-server.md`、`rtk_cloud_client/docs/PKI_DEVICE_AUTH.md`。
- Threat model / cyber security source references：`cyber_security/README.md`、`cyber_security/assumptions.md`、`cyber_security/sources.md`、`cyber_security/threat_models/rtk_video_cloud-stride-threat-model.md`、`cyber_security/analysis/stride-matrix.md`。
