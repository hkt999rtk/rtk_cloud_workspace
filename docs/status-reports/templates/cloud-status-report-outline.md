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
- 一張 product-to-KPI 視覺圖。

## 第一部分：摘要

- 一頁結論。
- 為什麼需要這個 cloud。
- 技術成果如何連到 business KPI。
- Loading test 或 milestone 目標。
- 目前高階架構。
- 目前部署狀態。
- 已完成 foundation 與下一步。

## 第二部分：Cloud / Product / KPI 細節

- 架構細節。
- Module-to-cloud-to-commercial KPI 路徑。
- KPI framework：技術、產品、商業、維運。
- Security 與 device trust。
- API / cloud pattern。
- Product features。
- SDK / reference app 狀態。
- Onboarding / provisioning flow。
- Loading test plan。
- 維護與維運現實。

## 第三部分：操作畫面與使用流程

- Admin Fleet Health Overview.
- Admin Devices + Detail Drawer.
- Admin Firmware & OTA.
- Admin Stream Health.
- SDK/sample app screen flow.
- Product/frontend architecture visual if useful for external positioning.

Keep the body selective. Put the full material catalog in the appendix.

## 第四部分：Linode Staging 部署與設定

- Linode 在本報告中的定位：較基礎的 VM / infrastructure 服務，不是 AWS-style managed-service stack。
- 說明可搬移性：PostgreSQL、MQ/message queue、broker、reverse proxy、runtime 等服務由我們在 VM/service layer 自行架設與管理，避免過度依賴 AWS-native 架構，未來較容易移動到 AWS、GCP、Azure、阿里雲或其他平台雲。
- Public endpoints 與目前 runtime shape。
- 非敏感 configuration boundaries。
- 附 timestamp 的 live health check table。
- Production-ready gaps。

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
- raw customer data

## 審閱清單

- 摘要可在五分鐘內看懂。
- 細節符合目前 repo 與 deployment 狀態。
- 技術工作有連到 AmebaPRO / module commercial KPI。
- 操作截圖能證明 demo 與 customer workflow readiness。
- Deployment / configuration 狀態避免 secrets 與 overclaiming。
- Production-ready gaps 明確列出。

## Appendix：素材與來源

- Screenshot / material source table。
- 完整 reusable material directories。
- Internal references and runbooks。
