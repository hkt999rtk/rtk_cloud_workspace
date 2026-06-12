# Status Report Writing Guidelines

Status: source.

Owner: `rtk_cloud_workspace`.

本文件定義 Realtek Video / IoT Cloud 週報的固定寫法。目標是讓不同
人、不同工具、或不同 LLM 產出的報告仍然使用同一套章節、同一種判斷
語言、同一個 evidence 標準。

## 0. 一頁速查

每份報告先回答九個問題：

| 問題 | 報告要給出的答案 |
| --- | --- |
| 為什麼做這個 cloud？ | 它如何支援 AmebaPRO / IoT module、SDK/app、生態系、customer PoC、design-in 與商業 KPI。 |
| 現在有什麼能展示？ | UI、SDK/sample app、API、deployment、health check、design asset、load-test evidence。 |
| 時程走到哪裡？ | 從 2026-05-01 到 early-August 50,000-device loading test 的目前位置、下一個 gate、風險判定。 |
| 哪些能力已驗證？ | 用 `PASS`、`FAIL`、`SKIP`、`BLOCKED`、`not verified` 標明，不用模糊描述。 |
| 主要對象是誰？ | Module buyer、solution developer、brand/ODM customer、video IoT customer 等，每一類要連到 cloud proof 與 module selling path。 |
| 每個 release gate 怎麼判定？ | Aug.1 loading test、Alpha、Beta、Public 的通過條件、需要 evidence、以及未過時的標示方式。 |
| 技術如何連到產品與 KPI？ | deployability、online success、OTA success、video setup、MQTT/shadow、load capacity、support effort、incident response。 |
| 哪些地方還不能宣稱 production-ready？ | release/version、backup/restore、security review、load-test、dynamic scaling、frontend staging、operations owner 等缺口。 |
| 管理層或同事需要知道什麼？ | Decision/support needed、risk burn-down、evidence index、next gate。 |

寫作原則：

- 報告用 Traditional Chinese；repo、API、endpoint、command、status label 保持英文原文。
- 不寫成工程 changelog；工程細節只在能解釋 capability、evidence、risk、next action 時出現。
- 重要數字優先用 chart、timeline、progress visual、bar/line chart 呈現；表格主要用於 evidence、risk、checklist、action tracking。
- 未驗證就寫 `not verified` 或 `BLOCKED`；不要沿用舊狀態當成本週狀態。
- 不放 secrets、tokens、DSN、private keys、raw lead data、raw customer data、raw media。

## 1. 固定 Table Of Contents

每份管理週報使用下列結構。短報告可以壓縮內容，但章節意圖不可消失。

| 章節 | 目的 | 必須回答 |
| --- | --- | --- |
| Cover / 核心管理訊息 | 第一頁讓管理層知道本週重點。 | 本週一句核心訊息、目前狀態總結、schedule snapshot、product-to-KPI visual。 |
| Part 1：主管摘要 | 五分鐘內看懂全局。 | 為什麼做、target customer / use case fit、目前完成什麼、下一步、風險、需要什麼決策。 |
| Part 2：Schedule / Loading Test 路徑 | 說明從 May 1 到 early August 的進度。 | 目前位置、本週 gate、下個 gate、50,000-device IoT target、5,000-video-camera target、release gate definition、風險判定。 |
| Part 3：Cloud / Product / KPI Detail | 把工程能力翻譯成產品與商業價值。 | Cloud relationship、customer/use-case fit、KPI、architecture、portal marketing、MQTT/shadow、WebRTC/storage、Security/PKI、threat model。 |
| Part 4：操作畫面與使用流程 | 讓非工程讀者看懂使用情境。 | Admin overview、device drawer、OTA、stream health、SDK/sample flow、demo journey。 |
| Part 5：Linode Staging Deployment & Configuration | 說明目前 staging 部署與限制。 | Endpoint、runtime shape、safe config、health check、production-ready gap。 |
| Part 6：決策、風險與 Evidence | 把管理需求、風險、證據集中。 | Decision/support table、risk burn-down、evidence index。 |
| Review Checklist | 產出前檢查。 | 是否過度宣稱、是否有 secrets、是否用 chart、是否標明缺口。 |
| Appendix：素材與來源索引 | 讓下週可重複使用。 | Screenshots、design assets、repo paths、PR/commit、health evidence、blocked evidence。 |

## 2. Report Generation Workflow

每週產生報告時照這個順序做：

1. 更新本週報告日期、snapshot timestamp、current position。
2. 收集 live evidence：public health endpoint、deployment status、repo/PR/commit、design screenshots、load-test artifact。
3. 判斷 schedule：`on track`、`at risk`、`blocked`，判斷依 evidence，不依樂觀日期。
4. 選 4 到 6 張正文圖片，其餘放 appendix。
5. 把重要數字轉成 visual：timeline、milestone lane、progress bar、bar chart、line chart。
6. 寫主管摘要與 core message。
7. 檢查 secrets/redaction。
8. 產生 `.docx` 到 `.artifacts/status-reports/YYYY-MM-DD/`。
9. Render DOCX 做視覺 QA。
10. 更新 evidence index 與 missing/blocked items。

Generated output 留在 `.artifacts/`，不要 commit。可 commit 的是：

- `docs/status-reports/guidelines.md`
- `docs/status-reports/materials.md`
- `docs/status-reports/templates/cloud-status-report-outline.md`
- `docs/status-reports/master_slide/`
- `tools/status-report/build_cloud_status_report.py`

## 3. Narrative Spine

每份報告都要維持同一條主線：

1. Cloud 是 AmebaPRO / IoT module 的產品化路徑，不是孤立 server project。
2. 價值鏈是 module -> SDK/app -> cloud onboarding -> video / OTA / telemetry / MQTT shadow -> admin operations -> customer PoC -> design-in / commercial KPI。
3. 技術進度要翻譯成 observable KPI：deployability、online success、SDK integration、OTA success、video setup success、load capacity、support effort、incident response。
4. 操作截圖要展示 customer、SDK developer、operator、reviewer 會如何使用系統。
5. Staging evidence 和 production-ready claim 必須分開。
6. Security 要講 PKI 的安全管理價值：identity、factory issuance、entitlement、audit、revocation、lifecycle governance。
7. MQTT 要分成 traditional MQTT transport 與 IoT device shadow。
8. Video 要分成 WebRTC live video 與 video storage/media operations。
9. Cloud structure 要說清楚 Realtek Platform Root、Brand Cloud、brand users、end users、devices 的關係。
10. Portal web 要說清楚它是 marketing / documentation / lead-generation layer，不是 operational cloud console。
11. Threat model / cyber security review 是風險管理軌道，不等於健康檢查。
12. Dynamic scaling 是 architecture design-in 的方向，但 August release baseline 不實作 autoscaling；loading test 後再依證據決策。
13. Customer / use-case fit 要提早說明，讓讀者知道 cloud 對 module buyer、solution developer、brand/ODM customer、video IoT customer 各自解決什麼問題。
14. Release gate 要用 evidence 判斷；日期到了但條件未滿足，報告要標 `at risk`、`blocked` 或 `not verified`。

## 4. Section Playbook

以下每章用同一個格式填寫：`目的`、`必填內容`、`建議視覺`、`資料來源`、`避免事項`。

### 4.1 Cover / 核心管理訊息

| 項目 | 指引 |
| --- | --- |
| 目的 | 第一頁讓管理層立即知道本週狀態、商業意義、下一步與維運現實。 |
| 必填內容 | 報告標題、日期、一句核心管理訊息、目前狀態總結、schedule snapshot、product-to-KPI visual。 |
| 建議視覺 | Product-to-KPI flow、status traffic-light、small progress visual。 |
| 資料來源 | 本週 evidence、schedule section、deployment health、PR/commit。 |
| 避免事項 | 不要放長篇 executive summary；不要只寫技術完成項；不要省略 operations/SLA/support 現實。 |

目前狀態總結使用三欄表格：

| 面向 | 目前狀態 | 下一步或風險 |
| --- | --- | --- |
| Deployment | 一句話 | 一句話 |
| Product / demo evidence | 一句話 | 一句話 |
| Operations / readiness | 一句話 | 一句話 |
| Next milestone | 一句話 | 一句話 |

### 4.2 Part 1：主管摘要

| 項目 | 指引 |
| --- | --- |
| 目的 | 讓管理層五分鐘內理解 why, now, next, risk。 |
| 必填內容 | Why cloud、target customer / use-case fit、completed foundation、current evidence、next gate、risk、decision/support needed。 |
| 建議視覺 | Foundation vs next-step table、small schedule snapshot、KPI bridge。 |
| 資料來源 | Part 2 schedule、Part 3 capability、Part 5 deployment、Part 6 risks。 |
| 避免事項 | 不要塞滿 repo 細節；不要把未驗證項目寫成已完成。 |

摘要必須包含：

- 目前 phase。
- Target date。
- Next measurable gate。
- Target customer / use-case fit。
- `on track` / `at risk` / `blocked`。
- 需要管理層知道的 decision/support。

### 4.2.1 Customer / Use Case Fit

| 項目 | 指引 |
| --- | --- |
| 目的 | 讓主管和同事知道這個 cloud 服務哪些客戶情境，並把客戶需求連回 module selling、PoC、design-in。 |
| 必填內容 | Target customer、customer need、cloud proof object、module sales / PoC linkage。 |
| 建議視覺 | 2x2 customer-fit cards、segment bar、use-case-to-sales bridge。 |
| 資料來源 | 客戶/FAE 討論、Portal Web 內容、SDK/sample app、Admin screenshot、Video/MQTT/OTA evidence。 |
| 避免事項 | 不要寫成泛泛的市場願景；不要列沒有對應 cloud proof 的客戶類型。 |

建議 customer / use-case 分類：

| Target customer | What they need | Module sales linkage |
| --- | --- | --- |
| Module buyer | 看到 module 之外的 onboarding、SDK/App、OTA、video、MQTT/shadow、Admin operation。 | 縮短評估時間，增加 design-in 信心。 |
| Solution developer | 可直接測試的 cloud API、sample app、device flow、debug report、文件入口。 | 讓開發者能自己跑 PoC，減少 FAE 重複解釋。 |
| Brand / ODM customer | Brand Cloud、tenant/user/device 關係清楚，知道哪些可由 Realtek platform 支援。 | 把 private / brand cloud 討論提前到可驗證架構。 |
| Video IoT customer | Live video relay、storage/media、stream health、future scaling/cost 的判斷基礎。 | 支援 camera / sensor solution 的商業化評估。 |

### 4.3 Part 2：Schedule / Loading Test 路徑

| 項目 | 指引 |
| --- | --- |
| 目的 | 說清楚專案從 2026-05-01 到 early-August target 的進度。 |
| 必填內容 | `目前位置`、`本週目標`、`下個 gate`、`風險`、`判定`。 |
| 建議視覺 | Timeline、Gantt-style chart、milestone lane、progress bar。 |
| 資料來源 | Load-test plan、runner output、deployment status、metrics threshold、weekly evidence。 |
| 避免事項 | 不要只用純表格；不要因為日期未到就宣稱 on track；不要把 IoT 50,000 target 和 Video 5,000 target 混在一起。 |

Schedule constants：

| 項目 | 固定值 |
| --- | --- |
| Project start | 2026-05-01 |
| IoT target | Early August 2026 pass 50,000-device loading test |
| Video target | 2026-08-01 pass 5,000-video-camera loading test |
| Post-load-test release path | August alpha test, September beta test, then public path |
| Dynamic scaling | August release 不實作；loading test 後依 evidence 決定 |

Baseline milestone path：

| 時段 | Milestone | Evidence |
| --- | --- | --- |
| 2026-05-01 to 2026-05-10 | Project kickoff and scope lock | Cloud purpose、source-of-truth boundaries、deployment target、50,000-device target。 |
| 2026-05-11 to 2026-05-24 | Foundation buildout | Linode staging、Account Manager / Video Cloud / Admin integration、SDK/sample、OTA/telemetry、status-report framework。 |
| 2026-05-25 to 2026-06-07 | Load-test preparation | Runner boundary、safe staging profile、fleet assumptions、metrics、thresholds、operator runbook。 |
| 2026-06-08 to 2026-06-21 | Small-to-medium validation | 100 / 1,000 / 5,000-device runs，並分類 API、broker、DB、resource、credential、test-data failure。 |
| 2026-06-22 to 2026-07-05 | Multi-host and capacity expansion | Multi-instance / multi-host、aggregation、resource dashboard、bottleneck fixes。 |
| 2026-07-06 to 2026-07-19 | 10,000 to 30,000-device rehearsal | p95/p99 latency、success rate、broker/database capacity、recovery behavior、operator response。 |
| 2026-07-20 to 2026-07-31 | 50,000-device dry run and hardening | Near-final dry run、soak test、rollback/retry plan、monitoring、report packaging。 |
| 2026-08-01 | 50,000-device + 5,000-video-camera loading-test pass | Final run passes agreed thresholds and produces management-ready evidence。 |
| August 2026 | Alpha test | SDK included；internal developers use onboarding、sample app、debug/report flow。 |
| September 2026 | Beta test | SDK + pilot customer；collect customer feedback and support evidence。 |
| After beta | Public path | Operation, account, support, security baseline ready。 |
| After loading test | Dynamic scaling implementation assessment | 依 bottleneck、traffic profile、cost、operating model、production direction 決定是否實作。 |

Video lane：

| 時段 | Video milestone | Evidence |
| --- | --- | --- |
| 2026-06-01 to 2026-06-21 | Video readiness foundation | WebRTC signaling、owner transport、TURN/ICE、stream health、snapshot/media upload/download evidence。 |
| 2026-06-22 to 2026-07-12 | Video small-scale validation | Representative app/device signaling、media upload、download auth、stream-health pass。 |
| 2026-07-13 to 2026-07-31 | 5,000-camera rehearsal | Fleet、media profile、TURN/coturn capacity、metrics、storage/retention、runbook。 |
| 2026-08-01 | 5,000-camera loading-test pass | Validate WebRTC/video-storage readiness at the same gate as 50,000 IoT devices。 |
| After loading-test pass | Alpha / beta release support | Use evidence to size operation cost, production scaling, and customer pilot boundary。 |

Current-position rule：

- 2026-06-03 附近的報告應標示在 `Load-test preparation`。
- 後續報告必須根據 evidence 推進；日期到了但 gate 沒過，要標 `at risk` 或 `blocked`。
- Dynamic scaling 可寫成 `architecture supports future scaling`，但不能寫成 August release 已支援 autoscaling。

Release gate definition：

| Gate | Scope | Evidence to pass |
| --- | --- | --- |
| Aug.1 loading-test pass | 50,000 IoT devices + 5,000 video cameras | Success rate、p95/p99、error taxonomy、resource use、recovery behavior、report package。 |
| Alpha test | SDK + internal developer real use | 4-6 internal testers；至少 3-4 位 developer/firmware/app testers 實際跑 onboarding、SDK sample、debug/report。 |
| Beta test | SDK + pilot customer | 1-2 pilot customers 或 partner use cases；確認 PoC feedback、support flow、deployment/cost assumptions。 |
| Public path | Operation, account, support, security baseline | 公司/核准第三方帳務、backup operator、release version、backup/restore、security review gate。 |

### 4.4 Part 3：Cloud / Product / KPI Detail

| 項目 | 指引 |
| --- | --- |
| 目的 | 把 cloud engineering 轉成產品、商業、維運可理解的 capability。 |
| 必填內容 | Source-of-truth boundaries、current-vs-target architecture、KPI framework、cloud relationship、portal marketing、WebRTC/storage、MQTT/shadow、Security/PKI、threat model。 |
| 建議視覺 | Architecture diagram、cloud relationship diagram、KPI bridge、readiness matrix、capability table。 |
| 資料來源 | `docs/architecture.md`、contracts docs、submodule docs、design assets、deployment evidence。 |
| 避免事項 | 不要把 Admin 當 source of truth；不要用 health endpoint 代替 capability evidence。 |

Source-of-truth boundaries：

| Layer | Source-of-truth |
| --- | --- |
| Account Manager | Identity、tenant、user、organization、membership、registry devices、provisioning operations、authoritative audit。 |
| Video Cloud | Runtime activation、device transport、WebRTC/video、MQTT/shadow、OTA/media/telemetry/log runtime facts。 |
| Admin Console | Dashboard/BFF、evidence aggregator、operation surface；不是 authoritative store。 |
| Frontend / portal | Marketing website、documentation/manual portal、lead-generation layer；不是 operational console。 |
| SDK/app/firmware | Onboarding、claim material handling、local setup、device transport、end-user flow；不決定 tenant policy。 |

### 4.5 Cloud Relationship / Tenant Structure

| 項目 | 指引 |
| --- | --- |
| 目的 | 避免讀者混淆 Realtek platform cloud、brand-name cloud、end user/device experience。 |
| 必填內容 | Realtek Platform Root -> Brand Cloud -> brand users / end users / devices。 |
| 建議視覺 | Three-layer diagram、source-of-truth map、end-user onboarding flow、role/audience table。 |
| 資料來源 | `BRAND_CLOUD_ADMIN.md`、`PRODUCT_ONBOARDING.md`、`AUTHORIZATION.md`、`PROVISION.md`、Admin SPEC。 |
| 避免事項 | 不要把 Brand Cloud 寫成 Admin SQLite local record；不要暗示每個 Brand Cloud 都是獨立 physical cloud，除非有部署證據。 |

Required relationship model：

```text
System Root / Realtek Platform
  -> Brand Cloud
      -> brand users / operators
      -> end users
      -> registry devices / activated cloud devices
      -> lifecycle operations, service options, runtime evidence
```

### 4.6 Portal Web / Digital Marketing

| 項目 | 指引 |
| --- | --- |
| 目的 | 說明 `rtk_cloud_frontend` 如何支援 marketing、documentation、lead generation、sales improvement。 |
| 必填內容 | SEO、content development、visitor behavior analytics、CTA conversion、lead capture、sales follow-up loop。 |
| 建議視覺 | Funnel chart、content map、SEO readiness matrix、aggregate behavior chart、lead conversion chart。 |
| 資料來源 | `rtk_cloud_frontend/README.md`、`docs/SPEC.md`、`docs/ANALYTICS.md`、`docs/API_REFERENCE.md`、`docs/MANUAL_CONTENT_SYSTEM.md`。 |
| 避免事項 | 不要把 website analytics 當 device telemetry；不要放 raw lead email、raw analytics rows、full referrer、search query、`ADMIN_TOKEN`。 |

Positioning：

- `rtk_cloud_frontend` 是 public marketing website、documentation/manual portal、lead-generation layer。
- 它不是 IoT console、authentication service、OTA backend、device provisioning backend、telemetry platform、production mobile app。
- 報告應連到 sales improvement：哪些頁面、功能、demo、keyword、CTA 需要改善，才能增加 qualified sales conversations。

### 4.7 WebRTC / Video Storage Management

| 項目 | 指引 |
| --- | --- |
| 目的 | 分清楚 live-video readiness 與 stored-media readiness。 |
| 必填內容 | WebRTC signaling、owner transport、TURN/ICE、session lifecycle、stream-health；snapshot/media upload、metadata、download auth、byte range、delete、retention/backup。 |
| 建議視覺 | WebRTC flow diagram、media capability table、readiness matrix。 |
| 資料來源 | `STREAMING.md`、`SNAPSHOT_AND_MEDIA.md`、`DEVICE_TRANSPORT.md`、`AUTH.md`、`AUTHORIZATION.md`、client integration docs。 |
| 避免事項 | 不要用 API health claim end-to-end live video；不要放 object storage key、signed URL secret、bearer token、raw media。 |

WebRTC wording：

- `WebRTC signaling readiness`：只有 offer / answer / close route、auth、owner transport evidence。
- `live-video readiness`：signaling、device owner transport、representative media path / stream-health evidence 都存在。
- `video storage/media readiness`：upload、metadata、download authorization、storage/retention evidence 都存在。

Required WebRTC flow：

```text
app offer -> POST /api/request_webrtc
  -> owner transport webrtc_offer
  -> device answer
  -> POST /api/request_webrtc/answer
  -> app media negotiation
  -> POST /api/request_webrtc/close
```

### 4.8 MQTT / Device Shadow Management

| 項目 | 指引 |
| --- | --- |
| 目的 | 說明 IoT Cloud 同時有 traditional MQTT transport 與 IoT device shadow。 |
| 必填內容 | Broker/topic connectivity、owner transport、command delivery、events/logs；desired/reported/delta/version/lifecycle/tombstone/ACL。 |
| 建議視覺 | Two-lane MQTT/shadow diagram、topic-surface table、state-management matrix。 |
| 資料來源 | `DEVICE_TRANSPORT.md`、`DEVICE_SHADOW.md`、`PROVISION.md`、`API_USAGE.md`、client transports docs。 |
| 避免事項 | 不要用 broker health claim shadow readiness；不要把 MQTT 當 activation/deactivation API。 |

分層說法：

| 層 | 用途 | Evidence |
| --- | --- | --- |
| MQTT transport | Broker connectivity、command routing、event/log ingress、owner transport、QoS/topic delivery。 | Topic delivery、owner transport、command/event/log evidence。 |
| Device shadow | Cloud-held device state：`state.desired`、`state.reported`、`state.delta`、`version`、`clientToken`。 | Shadow API/topic behavior、version conflict、delta、bootstrap、deactivation、tombstone evidence。 |

### 4.9 Security / PKI Management

| 項目 | 指引 |
| --- | --- |
| 目的 | 把 PKI 寫成安全管理制度，而不是單純 mTLS 技術。 |
| 必填內容 | Device identity、factory enrollment、CSR policy、certissuer、service entitlement、token bootstrap、revocation、audit、unprovision vs deactivation。 |
| 建議視覺 | Trust-chain diagram、security-management matrix、PKI readiness table。 |
| 資料來源 | `AUTH.md`、`PROVISION.md`、`CONTRACT_OVERVIEW.md`、cert issuer design、factory enrollment design、client PKI docs。 |
| 避免事項 | 不要放 private key、raw CSR PEM、raw certificate PEM、CA signing material、bearer token；不要說 CSR 本身就是 authentication。 |

Management message：

- PKI/mTLS 是 target production authentication model。
- Device identity 來自 verified client certificate subject，不可由 request body `devid` 覆蓋。
- Factory enrollment 是 manufacturing/security-management flow。
- `service_options` 是 canonical service-access ACL，不用 `device_type` 當權限來源。
- Device certificates 是 bootstrap credentials；runtime routes 仍使用 scoped、subject-bound tokens 與 ACL checks。
- Revocation、deactivation、unprovision 是不同 lifecycle control。
- HSM / PKCS#11 signer design 要描述 key custody boundary：service 取得 signing capability，但不持有 raw private key material。
- 報告可列 provider type、signer boundary、audit/fail-closed behavior；不可列 PKCS#11 module path、PIN、slot id、token label、key label、CA key path 或任何 raw signer config。

Trust-chain visual：

```text
factory/MES or fixture
  -> factory enrollment
  -> certissuer
  -> device certificate
  -> mTLS token bootstrap
  -> service-options ACL
  -> runtime services
```

HSM / PKCS#11 signer visual：

```text
HSM-backed token / non-exportable key
  -> PKCS#11 signer adapter
  -> certissuer CA signing and Ed25519 token signing
  -> certificate/token output with audit
```

### 4.10 Threat Model / Cyber Security Review

| 項目 | 指引 |
| --- | --- |
| 目的 | 說明 cyber risk review 的進度、缺口、下一步，而不是宣稱「安全」。 |
| 必填內容 | Method/scope、current status、top critical/high risks、open questions、next review focus、mitigation/evidence status。 |
| 建議視覺 | Cyber security review table、risk heatmap、risk burn-down。 |
| 資料來源 | `cyber_security/README.md`、`assumptions.md`、`sources.md`、STRIDE threat model、STRIDE matrix、evidence notes。 |
| 避免事項 | 不要把 health check 當 security sign-off；不要放 raw logs、tokens、DSNs、private keys、certificates、customer data、raw lead data。 |

Status vocabulary：

| Status | 意義 |
| --- | --- |
| `drafted` | Threat model 或 matrix 已建立但未 review。 |
| `reviewing` | Manual/code/deployment review 進行中。 |
| `evidence-needed` | 需要 command、code、deployment、artifact evidence。 |
| `mitigation-needed` | 風險已知，但修正或 owner 未關閉。 |
| `blocked` | 需要的 evidence 或 owner 不可得。 |
| `closed` | Mitigation 或 verification 完成且有 evidence reference。 |

Top risk themes 至少檢查：

- `I2`: secrets leaking through git, logs, artifacts, evidence, issue bodies。
- `S1/E1`: token、subject binding、route scope、legacy credential、certificate-header confusion。
- `S2`: MQTT auth/TLS/device identity spoofing。
- `D1`: WebRTC、MQTT、media、database、storage、TURN capacity exhaustion。
- `E2`: Admin BFF proxy/cache expanding privileges beyond upstream authority。

### 4.11 Loading Test Readiness

| 項目 | 指引 |
| --- | --- |
| 目的 | 對 early-August 50,000-device target 做可驗證 readiness tracking。 |
| 必填內容 | Runner/profile、fleet/data、metrics/thresholds、infra/multi-host、broker/database/storage visibility、report evidence。 |
| 建議視覺 | Readiness matrix、progress bar、risk burn-down、scale target chart。 |
| 資料來源 | Load-test runner output、JSON/Markdown reports、metrics dashboard、deployment evidence。 |
| 避免事項 | 不要用 vague prose；使用 `ready`、`partial`、`blocked`、`not verified`。 |

Matrix columns：

| Area | Current status | Needed before 50k | Owner / dependency | Risk |
| --- | --- | --- | --- | --- |

### 4.12 Part 4：操作畫面與使用流程

| 項目 | 指引 |
| --- | --- |
| 目的 | 用畫面讓非工程讀者理解 cloud 如何被操作與驗證。 |
| 必填內容 | Admin Fleet Overview、Devices Drawer、Firmware OTA、Stream Health、SDK/sample app flow、Frontend/product architecture。 |
| 建議視覺 | 4 到 6 張正文精選圖片，appendix 放完整素材索引。 |
| 資料來源 | `rtk_cloud_admin/docs/assets/webui-design/`、`rtk_cloud_client/docs/mockups/`、`rtk_cloud_frontend/static/assets/`、`docs/status-reports/master_slide/assets/`。 |
| 避免事項 | 不要放太多圖造成報告像素材 dump；每張圖必須有 caption 和用途說明。 |

Standard demo flow：

1. Admin reviews fleet overview。
2. Admin finds abnormal or attention-needed device。
3. Admin opens device detail drawer。
4. Admin checks firmware、stream、telemetry、readiness/source facts。
5. SDK/sample app demonstrates provisioning、configuration、camera monitor、debug report。
6. Loading test validates the same capability path at scale。

### 4.13 Part 5：Linode Staging Deployment & Configuration

| 項目 | 指引 |
| --- | --- |
| 目的 | 清楚說明 staging 部署、runtime shape、configuration boundaries、health status、production gap。 |
| 必填內容 | Public endpoints、snapshot timestamp、live health table、runtime placement、non-secret env key names、persistence category、reverse proxy/TLS boundary、production-ready gaps。 |
| 建議視覺 | Runtime topology、health status table、configuration boundary diagram。 |
| 資料來源 | Public health endpoints、deployment docs、Linode runbooks、evidence bundle。 |
| 避免事項 | 不要放 raw VM IP/private ports 當報告證據；不要放 DB DSN、JWT secret、Linode token、DNS credential、object storage key。 |

Linode wording：

- Linode 在本報告中是較基礎的 VM / infrastructure service，不是 AWS-style managed-service stack。
- PostgreSQL、MQ/message queue、broker、reverse proxy、runtime services 由我們在 VM/service layer 管理。
- 可說明可搬移性：未來較容易移到 AWS、GCP、Azure、Alibaba Cloud 或其他 infrastructure cloud。
- Health checks 是 status evidence，不是 production sign-off。

Dynamic scaling status 預設寫法：

```text
architecture supports future scaling; implementation deferred until after loading test
```

### 4.14 Part 6：決策、風險與 Evidence

| 項目 | 指引 |
| --- | --- |
| 目的 | 讓管理層知道需要什麼決策、風險是否下降、證據在哪裡。 |
| 必填內容 | Decision/support table、risk burn-down、evidence index。 |
| 建議視覺 | Risk trend、burn-down table、evidence status summary。 |
| 資料來源 | 本週 blockers、PR/commit、health evidence、load-test reports、deployment docs。 |
| 避免事項 | 不要把 management asks 藏在段落裡；不要放 resource plan，除非 report owner 明確要求。 |

Decision/support table：

| Decision / support needed | Why now | Impact if delayed | Owner / audience |
| --- | --- | --- | --- |

Risk burn-down table：

| Risk | Current status | Mitigation | Owner / dependency | Trend |
| --- | --- | --- | --- | --- |

Trend values：

- `down`
- `flat`
- `up`
- `new`
- `closed`

Evidence index categories：

- live endpoint evidence
- repo / PR / commit evidence
- screenshot / design evidence
- load-test report evidence
- deployment / configuration evidence
- production-readiness evidence
- missing or blocked evidence

## 5. Visual And Number Rules

重要數字不要只放 number table。優先使用：

| 內容 | 建議 visual |
| --- | --- |
| Schedule path / current position | Timeline、Gantt-style chart、milestone lane。 |
| Loading-test scale target | Progress bar、scale ladder、bar chart。 |
| Success rate / error rate / latency / throughput | Line chart、bar chart、bullet chart。 |
| KPI movement across weeks | Line chart、small multiples。 |
| Online/offline、firmware rollout、stream health distribution | Stacked bar、distribution chart。 |
| Risk trend | Risk burn-down、trend indicator。 |

表格適合用於：

- Evidence index。
- Risk list。
- Decision/support list。
- Endpoint health checks。
- Readiness matrix。
- Appendix material catalog。

## 6. Evidence And Redaction Rules

Allowed evidence：

- public health/version/service-health endpoint output
- source repo and path references
- submodule commit or PR references
- non-secret runtime shape
- screenshots and design assets from tracked repos
- generated report output path under `.artifacts`
- formal evidence bundle references

Forbidden evidence：

- Linode tokens
- DNS provider credentials
- DB DSNs or passwords
- JWT/auth signing secrets
- bearer tokens
- object storage access keys
- signed media URLs with secret query material
- private keys or certificate private material
- raw CSR PEM or raw certificate PEM
- CA signing key paths or CA signing material
- raw media files or customer-visible captured media unless sanitized and approved
- raw lead payloads、lead emails、analytics event rows、full referrer URLs、search query text
- raw customer data
- raw upstream payloads that expose internal-only fields

If a status cannot be verified from a safe source, write `BLOCKED` or
`not verified`; do not copy an old status as if it were current.

## 7. Review Checklist

產出前逐項檢查：

- 第一頁有核心管理訊息、目前狀態總結、schedule snapshot。
- 摘要可在五分鐘內看懂。
- Schedule path 顯示 2026-05-01 到 2026-08-01 50,000-device + 5,000-video-camera target，並標出 `目前位置`。
- Video schedule lane 有把 WebRTC/video-storage evidence 和 IoT telemetry/loading-test evidence 分開。
- Release gate definition 有列 Aug.1、Alpha、Beta、Public 的通過條件。
- 重要數字優先用 chart / timeline / progress visual。
- Loading Test Readiness Matrix 有列出 50,000-device target 前的 gates。
- Cloud relationship 清楚：Realtek Platform Root、Brand Cloud、brand users、end users、devices。
- Admin Console 沒有被描述成 Account Manager 或 Video Cloud 的 source of truth。
- Portal web / digital marketing 有 SEO、content、behavior analytics、lead conversion、sales improvement。
- WebRTC live video 和 video storage/media 分開。
- MQTT transport 和 IoT device shadow 分開。
- Security / PKI 是安全管理敘事，不只是 mTLS 技術。
- Threat model / cyber security review 有 STRIDE、top risks、open questions、review progress。
- 操作截圖能證明 demo 或 customer workflow readiness。
- Linode deployment/configuration 沒有 secrets，也沒有 overclaim production-ready。
- Decision/support、risk burn-down、evidence index 清楚。
- Resource plan 沒有預設加入，除非 report owner 明確要求。
- Dynamic scaling 沒有被宣稱為 August release 已實作。
- Production-ready gaps 明確列出。

## 8. Source Reference Map

| 主題 | Primary sources |
| --- | --- |
| Report framework | `docs/status-reports/README.md`、`materials.md`、`templates/cloud-status-report-outline.md`。 |
| Master slide / design | `docs/status-reports/master_slide/powerpoint_master.pptx`、`master_slide/design-guidelines.md`、`master_slide/SKILL.md`、`master_slide/assets/`。 |
| Cloud relationship | `rtk_cloud_contracts_doc/BRAND_CLOUD_ADMIN.md`、`PRODUCT_ONBOARDING.md`、`AUTHORIZATION.md`、`PROVISION.md`、`rtk_cloud_admin/docs/SPEC.md`。 |
| Portal web / digital marketing | `rtk_cloud_frontend/README.md`、`docs/SPEC.md`、`docs/ANALYTICS.md`、`docs/API_REFERENCE.md`、`docs/MANUAL_CONTENT_SYSTEM.md`。 |
| WebRTC / video storage | `rtk_cloud_contracts_doc/STREAMING.md`、`SNAPSHOT_AND_MEDIA.md`、`DEVICE_TRANSPORT.md`、`AUTH.md`、`AUTHORIZATION.md`、client integration docs。 |
| MQTT / shadow | `rtk_cloud_contracts_doc/DEVICE_TRANSPORT.md`、`DEVICE_SHADOW.md`、`PROVISION.md`、`API_USAGE.md`、client transports docs。 |
| PKI / security | `rtk_cloud_contracts_doc/AUTH.md`、`PROVISION.md`、`rtk_video_cloud/docs/cert-issuer-server-design.md`、`factory-enrollment-server.md`、`rtk_cloud_client/docs/PKI_DEVICE_AUTH.md`。 |
| Threat model / cyber security | `cyber_security/README.md`、`assumptions.md`、`sources.md`、`threat_models/rtk_video_cloud-stride-threat-model.md`、`analysis/stride-matrix.md`。 |
| Deployment / evidence | `docs/product-level-evidence.md`、`docs/deployment-secrets-governance.md`、deployment runbooks、public health endpoints。 |

## 9. Language And Tone

- 報告正文用 Traditional Chinese。
- Repo、API、endpoint、command、product name、status label 使用英文原文。
- Section title、caption、table header、summary、checklist 盡量用 Traditional Chinese，除非是固定產品或 API 名稱。
- 對外英文版應另開 translation pass，不要中英混雜。
- 不使用誇大詞，例如 `secure`、`production-ready`、`autoscaling ready`，除非有對應 evidence。

### 9.1 Non-AI Sense Writing Rules

產生正文、slide title、transition text、caption、speaker-facing summary 時，
文案要像實際主管報告，不要像 LLM 自動摘要。遵守下列規則：

- 避免公式化對比句，尤其是「這不是 A，而是 B」、「不只是 A，也是 B」。
  需要區分概念時，直接說明兩者角色或用表格拆開。
- 避免空泛形容詞，例如「完整」、「強大」、「無縫」、「智慧化」、「端到端」
  單獨出現。若要使用，必須接 evidence、範圍或限制。
- 避免過度解釋顯而易見的內容。管理簡報優先寫結論、影響、下一步。
- 避免每頁都用同一種句型開頭，例如「本頁說明」、「重點是」、
  「目的：」。同一份 deck 內要變化語氣。
- 避免把技術名詞堆成口號。技術名詞要連到 capability、control point、
  evidence、risk 或 owner。
- 避免誇張承諾。未驗證內容用 `not verified`、`evidence-needed`、
  `BLOCKED`、`target`、`planned`。
- 過渡頁使用自然口吻，例如「接下來看...」、「這一段聚焦...」、
  「先用...建立共識」。不要用過度戲劇化語句。
- 每頁只保留一個主要訊息；若文字超過兩行仍無法說清楚，改用圖、
  timeline、diagram 或拆頁。

建議替換：

| 避免寫法 | 建議寫法 |
| --- | --- |
| 這不是單一技術進度報告，而是... | 本次報告聚焦四件事：... |
| 這不是 device runtime，而是 public website... | 接下來看 public website、documentation、SEO 與 lead flow。 |
| 它不是只支援 A，而是也支援 B | A 和 B 分別承擔不同角色：... |
| 強大的雲端能力 | 已驗證的 capability：...；待補 evidence：... |
| 完整端到端解決方案 | 目前涵蓋 module、SDK、onboarding、OTA、video、admin；production gaps 包含... |
