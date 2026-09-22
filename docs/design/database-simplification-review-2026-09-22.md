# Database ER model 簡化調查報告

調查日期：2026-09-22。狀態：設計建議，尚未實施 schema 或程式變更。

## 結論

**確實有可有效簡化的設計。最值得先做的是清掉失去用途的表、重複索引，以及完成已經開始的新舊模型切換。** 不建議以「表數越少越好」為目標：跨服務交付、帳務歷史與權限範圍有些刻意重複，是正確性的一部分。

目前可列出 **7 張優先退役候選表、3 個重複索引**；後續完成身分與 OTA 遷移、合併同服務的 audit 儲存，可再減少 **8 張業務表**。合計 15 張是有條件的結構簡化潛力，不代表現在可以直接刪除，也不是預估效能提升百分比。

真正的效益依序是：減少錯用舊模型的機會、減少維護與測試分支、降低索引寫入成本、讓狀態只有一個權威來源。沒有線上資料量與查詢統計，不能承諾節省多少容量或延遲。

## 範圍與證據限制

以本機 [ER atlas 說明](/Users/kevinhuang/work/rtk_cloud_workspace/docs/design/database-er-diagrams.md)、DDL、migration、非測試程式的 SQL 與呼叫點為主，對照重要測試和既有契約。未連線到 dev/staging/prod，也未更新遠端 checkout。

| Schema owner | atlas 表數 | 本機基底 commit |
| --- | ---: | --- |
| Account Manager | 92 | `9a37682550be` |
| Billing | 55 | `026016a9e797` |
| Video Cloud | 71 | `627edc4e201c` |
| Cloud Admin | 16 | `03da0578dccc` |
| Cloud Frontend | 5 | `20a59c7f75e9` |
| 合計 | 239 | 包含 migration metadata |

本機有既存未提交修改，ER atlas 與產生器也是未追蹤檔案；結論描述的是「目前工作目錄」，不是以上 commit 的純快照。239 是產生器來源集合的靜態結果，不是部署實例的實測表數。

「未找到業務讀寫」表示在目前 repository 的非測試程式中沒有找到；不能排除舊版部署、外部工具、人工 SQL 或未納入來源的服務仍使用。退役前仍需確認部署版本、實際資料及相依物件。

## 優先順序

工作量為相對估計：小＝局部清理；中＝需遷移及流程回歸；大＝跨客戶端或跨服務切換。風險描述的是實施變更的風險。

| 順序 | 項目 | 可簡化量 | 主要收益 | 工作量／風險 |
| --- | --- | --- | --- | --- |
| 1 | 移除完全同鍵的重複索引 | 3 個索引 | 降低多餘寫入與索引維護 | 小／低 |
| 2 | 清理 Admin 未接上業務流程的 schema | 4 張表 | 移除假性資料來源及過時設計 | 小／低至中 |
| 3 | 清理 Video Cloud 舊 relay/state schema | 3 張表 | 移除相容性殘留與錯誤依賴 | 小至中／中 |
| 4 | 完成 Brand Cloud 身分模型退役 | 3 張表及舊 Store 方法 | 只維護一套管理者身分流程 | 中／中 |
| 5 | OTA 欄位與 JSON 指定單一權威 | 不以減表為目標 | 降低狀態不一致與更新負擔 | 中／中 |
| 6 | 合併 Account Manager 的 audit 儲存 | 淨減 1 張表 | 統一查詢、保留與寫入實作 | 中／中 |
| 7 | 完成舊 firmware → Product OTA 遷移 | 4 張舊表 | 移除兩套生命週期與 tenant 判斷 | 大／高 |

## 1. 三個重複索引：最直接的改善

| 可刪的顯式索引 | 已存在的相同鍵 | 證據 |
| --- | --- | --- |
| `identity_providers_provider_id_idx` | `identity_providers.provider_id UNIQUE` | [OIDC migration](/Users/kevinhuang/work/rtk_cloud_workspace/repos/rtk_account_manager/migrations/015_oidc_identity_persistence.sql:1) |
| `device_tag_catalog_org_tag_idx` | `PRIMARY KEY (organization_id, tag)` | [tag catalog migration](/Users/kevinhuang/work/rtk_cloud_workspace/repos/rtk_account_manager/migrations/069_device_tag_catalog.sql:1) |
| `idx_readiness_facts_device` | `UNIQUE(device_id, layer)` | [Admin schema](/Users/kevinhuang/work/rtk_cloud_workspace/repos/rtk_cloud_admin/internal/store/store.go:213) |

這三組都是同欄位、同順序的索引，來源沒有額外 predicate、排序或 covering 欄位差異。**保留 PK/UNIQUE 約束，移除額外建立的普通索引。** 不應為了刪索引而刪除唯一約束。

驗證：在一次性的記憶體 SQLite database 執行 Admin 的 11 個 migration SQL 區塊，確認 `readiness_facts` 存在兩個同鍵索引；移除顯式索引後，`EXPLAIN QUERY PLAN` 仍使用 `sqlite_autoindex_readiness_facts_1` 查詢 `(device_id, layer)`。這只驗證此查詢與索引替代關係，不是線上效能 benchmark。PostgreSQL 兩項目前以 DDL 比對確認，部署前應核對實際 catalog。

效益大小取決於寫入量；identity provider 表可能很小，收益主要是設計整潔。這批不需要配合更大的資料模型重構。

## 2. Admin：四張表只有宣告，實際路徑走別處

退役候選：`platform_admins`、`upstream_organizations`、`upstream_devices`、`upstream_operations`。

在 Admin 的非測試 Go 程式中，這四個表名只出現在 migration／index 定義，沒有找到業務 SQL 讀寫。實際平台裝置／操作列表會呼叫 Account Manager，必要時使用另一組本機 `devices`／`operations` 作 fallback。證據：[migration](/Users/kevinhuang/work/rtk_cloud_workspace/repos/rtk_cloud_admin/internal/store/store.go:144)、[平台列表組裝](/Users/kevinhuang/work/rtk_cloud_workspace/repos/rtk_cloud_admin/internal/app/app.go:6234)。

建議新增退役 migration，移除這四張未使用表，並同步修正文檔中對 upstream cache 的描述。先檢查歷史資料是否需要保留及舊部署是否仍使用；不要只刪 migration 原文，否則既有 DB 仍保留舊表。

**`devices`／`operations` 不是這一批直接刪除候選。** `SeedDemoData` 會寫入，列表、摘要及 fallback 也會讀取。可另外把 demo seed 限於明確的 demo 模式，釐清正式環境缺上游 API 時是否應回報不可用；這涉及產品行為，不能混成「刪兩張 cache 表」。證據：[seed 與列表](/Users/kevinhuang/work/rtk_cloud_workspace/repos/rtk_cloud_admin/internal/store/store.go:372)。

完成條件：正式登入、上游 inventory、缺上游 API 的既定行為都通過；四張表無部署端讀寫需求，且 migration 後重新啟動不會重建。

## 3. Video Cloud：三張舊相容表值得退役

退役候選：`relay_nodes`、`relay_geolinks`、`legacy_device_states`。

目前非測試程式沒有找到這三張表的業務 INSERT/UPDATE/SELECT repository；前兩者只剩建立／清理 schema，`legacy_device_states` 另外仍出現在 device transfer fence 的 `EXISTS` 和 control-plane trigger 表清單。這是「缺少實際業務使用，但仍有相依檢查」，不是可直接 DROP 的孤立表。證據：[schema](/Users/kevinhuang/work/rtk_cloud_workspace/repos/rtk_video_cloud/internal/postgres/postgres.go:239)、[轉移檢查](/Users/kevinhuang/work/rtk_cloud_workspace/repos/rtk_video_cloud/internal/postgres/device_transfer_fence.go:54)、[trigger 清單](/Users/kevinhuang/work/rtk_cloud_workspace/repos/rtk_video_cloud/internal/postgres/video_control_plane_fence_schema.go:85)。

建議先核對舊部署／資料，再一起清掉建立語句、fence 查詢、trigger 清單及舊說明。若 `legacy_device_states` 尚有資料，必須先判定它是否仍代表有效連線／轉移阻擋條件，不能把移除檢查當作解決阻擋的方法。

目前 [postgres-schema 文件](/Users/kevinhuang/work/rtk_cloud_workspace/repos/rtk_video_cloud/docs/postgres-schema.md:78) 仍把它們描述成 compatibility storage，甚至列出 `internal/postgres.RelayRegistry`；需與現有實作一起校正。

保留 `device_socket_sessions`：它實際用於 websocket/MQTT owner routing 與 presence reconcile。[session repository](/Users/kevinhuang/work/rtk_cloud_workspace/repos/rtk_video_cloud/internal/postgres/websocket.go:88) 有活躍寫入，不能與 `legacy_device_states` 一起刪。

## 4. Brand Cloud 身分：已經切換，舊模型尚未收尾

舊表：`brand_cloud_users`、`brand_cloud_memberships`、`brand_cloud_refresh_tokens`。新權威：`users`、`organization_members`、全域使用者登入流程。

這項比單純看到相似欄位更有依據：

- [049 migration](/Users/kevinhuang/work/rtk_cloud_workspace/repos/rtk_account_manager/migrations/049_unify_human_identity.sql:1) 已搬移 tenant-scoped identity、membership 與 role assignment，撤銷舊 refresh token，並保留 `brand_cloud_user_migrations` 對照表。
- [Admin API](/Users/kevinhuang/work/rtk_cloud_workspace/repos/rtk_account_manager/internal/api/brand_clouds_admin.go:368) 現在呼叫 `ProvisionBrandCloudAccount`／`ListBrandCloudAccounts` 及全域 member 的停用／啟用流程。
- [身分邊界測試](/Users/kevinhuang/work/rtk_cloud_workspace/repos/rtk_account_manager/internal/api/identity_auth_boundary_test.go:14) 明確要求拒絕 retired tenant token。
- 舊 Store 仍保留 [CreateBrandCloudUser 等實作](/Users/kevinhuang/work/rtk_cloud_workspace/repos/rtk_account_manager/internal/store/brand_clouds.go:247) 與登入 activation helper；代表仍有清理工作，不能只把表刪掉。

建議刪除不可達的舊 API helper／Store／cache 型別，核對舊資料搬移與 activation holds，再退役三張舊表。保留 migration mapping 作稽核證據，直到另訂保留期限。歷史 migration 049/051 仍引用舊表，新安裝須先跑完歷史 migration，再執行末端清理 migration。

必驗：email 相同但密碼衝突的使用者、pending verification、停用 member、owner 存在性與 tenant token 拒絕行為。不要把 `end_users` 一起併入；這次證據支持的是管理者身分舊模型退役，未證明消費者身分與管理者有相同生命週期。

## 5. OTA：同一筆狀態存在欄位和完整 JSON

`ota_releases`／`ota_campaigns`／`ota_deployments` 同時有關聯式欄位與 `payload JSONB`。例如 `SaveRelease`／`SaveCampaign` 將完整 struct 序列化成 JSON，又把 ID、scope、state 等寫入欄位；Get/List 常直接從 payload 還原。Dispatcher 更新 campaign state 時還需要同步 `jsonb_set(payload, '{state}', ...)`。證據：[repository](/Users/kevinhuang/work/rtk_cloud_workspace/repos/rtk_video_cloud/internal/postgres/productota.go:20)、[排程狀態更新](/Users/kevinhuang/work/rtk_cloud_workspace/repos/rtk_video_cloud/internal/postgres/productota.go:203)。

這是有用途的索引投影，但目前存在兩份可寫的狀態表示；遺漏其中一份會導致 SQL 篩選與 API 回傳不同。現有程式刻意同步，**本次未證明線上已發生不一致**。

建議將 tenant/product/release identity、state、version 等核心欄位設為權威；JSON 只存政策、schedule、manifest 等結構化擴充內容，讀取時組合 response。先集中 read/write helper 及比對既有資料，再縮減 JSON，避免一次改掉所有 wire format。

`SaveCampaignIfUnchanged` 現在把完整 payload 相等當作並行更新條件；移除 payload 中的核心欄位時，必須同時換成明確 version/CAS，並讓每個 writer 遵守。不能只刪 JSON key 而削弱並行保護。

另有一個已經做對的地方：`Campaign.TargetSnapshot` 標記為 `json:"-"`，targets 從 `ota_campaign_targets` 還原，不是把整份 device ID 清單重複塞進 JSON。[型別](/Users/kevinhuang/work/rtk_cloud_workspace/repos/rtk_video_cloud/internal/productota/ota.go:144)、[讀取 targets](/Users/kevinhuang/work/rtk_cloud_workspace/repos/rtk_video_cloud/internal/postgres/productota.go:92)。不應將這部分誤列為重複儲存。

## 6. Account Manager audit：可合併儲存，但不是刪重複事件

`audit_events` 與 `acl_audit_events` 有相同的十個欄位及近似索引／序列化程式。建議在 `audit_events` 增加明確分類欄位，例如 `audit_domain`，將 ACL 寫入整合到同一套 Store，API 仍保留原有 ACL 權限及篩選行為。若需要相容查詢，可短期保留唯讀 view。

證據：[一般 audit DDL](/Users/kevinhuang/work/rtk_cloud_workspace/repos/rtk_account_manager/migrations/011_audit_events.sql:1)、[ACL audit DDL](/Users/kevinhuang/work/rtk_cloud_workspace/repos/rtk_account_manager/migrations/016_acl_persistence.sql:99)、[ACL 寫入及列表](/Users/kevinhuang/work/rtk_cloud_workspace/repos/rtk_account_manager/internal/store/acl.go:779)。

必須處理的差異：一般 audit 在 [038 migration](/Users/kevinhuang/work/rtk_cloud_workspace/repos/rtk_account_manager/migrations/038_audit_event_actor_identity.sql:1) 已移除 actor 的 user FK，ACL audit 仍有該 FK 及較嚴的 nonblank check。合併時需明訂 actor 保存語義、保留 ACL 驗證和查詢隔離，保留 event ID、時間、payload；先驗證 ID 不衝突。不能直接用 UNION 去重，更不能跨 Billing／PKI 一起合併。

此項減少一張表與兩套儲存程式，但不是宣稱同一事件目前一定被寫兩次。若兩類 audit 有不同保存期限、存取政策或負載，先統一寫入介面即可，實體合併可延後。

## 7. 舊 firmware 與新 OTA：收益大，但須完成產品切換

舊模型：`firmware_releases`、`firmware_targets`、`firmware_campaigns`、`firmware_rollouts`。新模型：Product-scoped 的 `ota_releases`／campaigns／deployments／events 與 dispatch tables。

這不是只有舊 DDL：API composition 仍建立兩套 repository/service，cloud handoff／deletion 也仍檢查舊 firmware 表。證據：[組裝](/Users/kevinhuang/work/rtk_cloud_workspace/repos/rtk_video_cloud/internal/apiapp/app.go:364)、[handoff](/Users/kevinhuang/work/rtk_cloud_workspace/repos/rtk_video_cloud/internal/cloudhandoff/video_boundary.go:39)。

建議以 Product OTA 作唯一新寫入模型，舊路由逐步成為相容 adapter；以可信 product/device ownership 搬移歷史資料，無法確認歸屬者隔離處理。最後移除四張舊表及舊 lifecycle 分支，保留必要的 migration 對照證據。

現有 [OTA migration contract](/Users/kevinhuang/work/rtk_cloud_workspace/repos/rtk_cloud_contracts_doc/product_ota_migration.md:1) 是 proposed 文件，不能當成已完成切換的證據。它提出的「兩個 release cycle 沒有必要的舊路由流量、SDK/device 已切換、資料對帳完成」適合作為退役驗收條件。不能只用 `model` 推論 tenant/product，也不能只搬 releases 而遺漏 campaign/deployment 歷史。

這項節省的不只是四張表，而是兩套 eligibility、授權 scope、取消與 ownership handoff 邏輯的維護。風險與客戶端切換成本也最大，排在低成本清理之後。

## 看起來重複，但本輪建議保留

| 設計 | 為何保留／可以怎樣小幅簡化 |
| --- | --- |
| Account Manager 與 Video Cloud 都有 `devices` | 前者管帳號、歸屬與產品；後者管服務 runtime。`account_device_id` 是映射，不能把兩邊 `id` 當同一個值。可統一欄位責任與更新方向，不建議跨服務共用表。 |
| `commercial_accounts.available_balance_minor`、ledger 的 `balance_after_minor` | 前者是當前餘額，後者是每筆入帳後的歷史狀態。實作在同一交易鎖定 account、檢查 idempotency、寫 ledger 並更新 balance；不是任意兩處各寫一份餘額。[ledger 實作](/Users/kevinhuang/work/rtk_cloud_workspace/repos/rtk_billing/internal/paymentstore/ledger.go:39) |
| invoice recipient、單價、金額 snapshot 與當前 profile/rate | 歷史帳單應保留出帳時內容。不能只留當前 profile/rate FK 後即刪 snapshot。[invoice schema](/Users/kevinhuang/work/rtk_cloud_workspace/repos/rtk_billing/migrations/044_billing_pricing_and_invoices.sql:136) |
| handoff／deletion 的 request、decision、ack、job 表 | request 是意圖、decision 是不可重作的決定、ack 是另一方已完成的證據、job 是排程。Billing 有逐層 FK，AM 有 trigger 檢查收據；合成一張可覆寫 status 表會失去這些保護。[Billing commit schema](/Users/kevinhuang/work/rtk_cloud_workspace/repos/rtk_billing/migrations/051_handoff_commit_protocol.sql:1)、[AM commit schema](/Users/kevinhuang/work/rtk_cloud_workspace/repos/rtk_account_manager/migrations/058_cloud_handoff_commit.sql:15) |
| usage fact、receipt、outbox、Billing usage fact | 分別負責原始事實、去重證據、可靠交付及接收方計價資料。先訂 payload 大小與保留期限；不能直接用遠端 DB join 取代本地可靠交付。[usage evidence schema](/Users/kevinhuang/work/rtk_cloud_workspace/repos/rtk_video_cloud/internal/postgres/usage_evidence_schema.go) |
| 三類 PKI issuance／revocation 表 | 有重複結構，但 server 多 `domain` 與 DNS names，service client 的 TTL 上限也不同。先重用驗證／Store helper；為減表數合併，可能把明確約束變成許多 nullable 欄位與條件分支。[PKI schema](/Users/kevinhuang/work/rtk_cloud_workspace/repos/rtk_video_cloud/internal/pki/schema.sql:174) |
| `device_tag_catalog` 與 `device_tags` | catalog 能保留目前零裝置使用的標籤；binding 不能完整取代 catalog。保留表，只刪本報告列出的重複索引。[列表 LEFT JOIN](/Users/kevinhuang/work/rtk_cloud_workspace/repos/rtk_account_manager/internal/store/store.go:2252) |
| `registry_nodes` 與 `turn_nodes` | 兩者都有活躍 repository；TURN 額外有 port、secret version 與 registration token。這次沒有足夠收益證據支持硬併表。[registry](/Users/kevinhuang/work/rtk_cloud_workspace/repos/rtk_video_cloud/internal/postgres/registry.go:41)、[TURN](/Users/kevinhuang/work/rtk_cloud_workspace/repos/rtk_video_cloud/internal/postgres/turnregistry.go:42) |
| Frontend 的 search documents/chunks | 文件與分塊是一對多，分析、同意紀錄與 leads 也有不同用途。五張表在本輪沒有列出高把握度的刪除候選。 |

Billing／PKI 的評估以重要約束及代表性路徑為主，不代表已形式化驗證所有流程，也不宣稱沒有其他可以簡化之處。

## 執行方式與驗收

1. **先做三個索引。** 核對部署 catalog，以新增 migration 移除顯式索引，保留 constraint backing index；比較重要查詢 plan 與寫入指標。
2. **分服務清理七張候選表。** 確認使用版本、row count、相依 FK/view/trigger、外部 SQL；先移除讀寫／檢查依賴，再執行 schema cleanup。測試新安裝、既有 DB 升級及重啟，不要把舊 schema 建立程式留著讓表復活。
3. **收尾身分遷移。** 先對帳 migration map 和登入／權限行為，再移除舊程式與三張表；保留查核證據。
4. **OTA JSON 與 audit 各自獨立交付。** 先加一致性檢查或統一介面，完成對帳後再切資料格式。避免將 schema、wire format、帳務流程一起改。
5. **最後關閉舊 firmware 模型。** 以實際路由流量、client 支援範圍、歸屬對帳及 handoff/deletion 回歸作決策。

每項至少記錄：實際使用量、資料量／索引大小、遷移前後筆數與關鍵欄位一致性、回歸結果、可回復資料及舊版程式相容期限。刪表後若要回退舊 binary，光重建空表不夠，須事先保存所需資料與相依物件。

**ER 產生器也需一起更新：** 目前 `parse_database` 支援 CREATE、部分 ALTER、UNIQUE INDEX，尚未實作 `DROP TABLE` 的有效 schema 計算；Video Cloud 同檔另有測試清理用的 DROP statements。因此不能簡單把所有 DROP 當 migration 執行，也不能刪表後只重新產生就期待 atlas 正確。應區分 migration/runtime schema 與 reset helper，再表達有效 schema；[產生器](/Users/kevinhuang/work/rtk_cloud_workspace/scripts/generate_database_er_atlas.py:136) 是需要修改的位置。

本次完成來源表數重算、候選表非測試引用核對、代表性 API → Store 追蹤、索引定義比對與一次性 SQLite 查詢計畫驗證。沒有執行 PostgreSQL migrations、線上測量或完整服務回歸，因本次交付只有調查報告。技能附帶的 `references/templates.md` 在本機缺失，因此使用單一報告格式；不影響程式與 schema 證據核對。
