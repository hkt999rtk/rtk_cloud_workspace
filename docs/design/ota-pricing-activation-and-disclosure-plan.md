# OTA 費率生效與服務價格揭露：實作計畫

Status: implementation plan with approved-rate documentation, authenticated research-price disclosure, preactivation OTA invoice protection, rate precision/tax metadata, late-fact rejection evidence, read-only complete-card and UTC-cutover inventories, non-OTA future cutover groundwork, a conservative UTC-month/owner close guard, source-side historical Product grant evidence for all four OTA meters, immutable per-object storage evidence, and Billing grant/byte-time verification built. The merged code is deployed to development. The dev Service intermediate successor was activated on 2026-09-29; by 2026-09-30 six separate Product registrar identities had live leases and their bootstrap session was sealed. The independent OTA service is registered and its v2 manifest published in development. Product writes are enabled on the dev Account Manager API and outbox worker. A controlled OTA Product and test device completed direct-object download and three source-to-Billing meter checks; physical installation and complete-month storage remain unverified. The first non-OTA development card was published on 2026-09-30 for the next UTC month. OTA remains suspended and no effective OTA rate card has been published; development does not charge OTA.

Owner: rtk_cloud_workspace (cross-repository sequencing). Last reviewed: 2026-09-30.

### Development registration checkpoint（2026-09-30）

Account Manager 的私有 mTLS listener、六張各自核准的 Service 身分、六個有效註冊 lease 已完成；有效 MQTT 憑證冒用 OTA service tuple 時被 403 拒絕。原 bootstrap session 到期後，只對原簽署期限內已成功且未撤銷的六筆發行回執，核對實際運作中的 service 再補 ACK 並 seal；到期後沒有新簽憑證。臨時 bootstrap 設定、私鑰與 1 GiB PVC 已清理，唯讀憑證檢查通過。詳細證據與復原規則見 [dev PKI runbook](../product-services-dev-pki.md)。

截至該日，此進度只完成註冊前置條件：僅 MQTT 在 service catalog 為 active，OTA、Shadow、WebRTC、Storage、Logger 仍 suspended。當時獨立 OTA runtime、裝置路由、核心切流、實際 receipt／outbox／Billing 投遞均尚未驗收；Storage、WebRTC、Shadow 也有各自的路由切換條件。Product writes 與正式 OTA 價卡維持關閉，六個 lease 不能取代收費上線驗收。PKI 授權紀錄的控制資料存在 PostgreSQL，操作事件供 Loki 搜尋；兩者的目的與故障處理見 [operator authority design](pki-operator-authority-test-plan.md)。

### Development 獨立 OTA runtime checkpoint（2026-09-30）

固定版 workspace [#616](https://github.com/hkt999rtk/rtk_cloud_workspace/pull/616) 已合併，使用 Video Cloud [#736](https://github.com/hkt999rtk/rtk_video_cloud/pull/736) 的獨立 OTA 映像 `sha256:096d91197cd0e372aaf5ca02014e1c8f4f8dd736a930c04483283c8054e9b97a`。舊 `video-cloud-otaregistrar` 先縮至零並確認無 Pod，新的 `video-cloud-otaservice` 以 `service:ota` 身分取得 ready v2 lease、1/1 Ready Pod 與私有 Service endpoint。Account Manager [#359](https://github.com/hkt999rtk/rtk_account_manager/pull/359) 修正 suspended 服務不能發布 ready manifest 的錯誤；規範文字見 contracts [#188](https://github.com/hkt999rtk/rtk_cloud_contracts_doc/pull/188)。dev Account Manager 更新至固定映像 `sha256:a00bb92eb06a63d1fc16d151af2e565c69258481bb51b2c41bb268ef5def9222` 後，用 OTA 專屬 mTLS 身分將目錄發布到 revision 28、manifest v2（endpoint `ota-service`）；資料庫核對狀態仍是 `suspended`。兩個映像的 GHCR 唯讀檢查通過，持久 operator 設定已釘選 digest，PKI 管理 sidecar 未改。詳細程序與 rollback 見 [deployment operations](../deployment-operations.md) 及 [dev PKI runbook](../product-services-dev-pki.md)。

這是**獨立服務註冊與版本選擇**驗收，還不是 OTA 收費驗收。當次唯讀計數顯示 dev 沒有 OTA Product profile 或 entitlement snapshot；Video Cloud 的 OTA task/download receipt、artifact object、producer period seal 與 OTA outbox fact 均為 0，Billing 的 OTA usage fact 與 pricing publication 也均為 0。Product writes、裝置 edge 與核心切流仍關閉，OTA catalog 仍 suspended。下一步需以受控 OTA-enabled Product 和裝置驗證授權、直連物件 URL／Range、完成回執、outbox 到 Billing、月封存，再核定未來完整 UTC 生效月與發佈已核准價卡；不能因 v2 lease 或 publication 就收費。

### Development OTA 裝置入口 checkpoint（2026-09-30）

固定版 workspace [#618](https://github.com/hkt999rtk/rtk_cloud_workspace/pull/618) 合併後，dev 的唯讀 SecretStore／環境憑證檢查通過 10/10，operator 保存 edge 旗標原值並只把 `LKE_OTA_SERVICE_EDGE_ENABLED` 設為 `true`。受限部署指令確認舊 registrar 已停止、獨立 OTA Service endpoint Ready、Product OTA 授權檢查維持嚴格模式；只新增 OTA Pod 的 18084 ingress policy、私有 bridge Service，以及既有 device mTLS ingress 的 `/v1/device/ota/` Prefix 路徑。現場讀回顯示原核心 `/` 路徑保留、app CA pin 與 client certificate 驗證深度 2 不變，核心 API 與獨立 OTA Service 都是 1/1 Ready。公開入口對沒有 client certificate 的請求回應 ingress HTTP 400；私有 OTA handler 對無身分請求回應 HTTP 401。

這些是**入口設定與拒絕路徑**驗收，不是合法裝置的成功下載、OTA 用量或 Billing 收據。OTA service 仍 suspended、Product writes 仍關閉、核心 cutover 仍關閉，正式 OTA 價卡尚未生效；已建立的入口本身不會開始收費。下一關要以受控 OTA-enabled Product 和具有效裝置憑證的測試資料驗證正向流程、Object Storage 簽名 URL／Range、四類 receipt 與 outbox／Billing 對帳，再決定是否切除核心舊 handler。

### Development 簽章準備 checkpoint（2026-09-30）

Video Cloud [#737](https://github.com/hkt999rtk/rtk_video_cloud/pull/737) 已合併到指定的固定基線，提供與服務共用 `CanonicalManifestV1` 的 operator 工具、獨立簽章驗證測試及[操作文件](../../repos/rtk_video_cloud/docs/ota-manifest-operator.md)；本機 OTA 相關測試與一次完整 Video Cloud PR coverage 通過。dev operator 在本機 SecretStore 建立獨立 Ed25519 私鑰（檔案權限 `0600`），其公開信任項目已加入 dev 環境 override 並保留舊公開金鑰。此 checkpoint 尚未把新公開金鑰部署到 workload、建立 OTA Product／裝置、簽署韌體或收集用量；私鑰不進 Git、Kubernetes 或服務程序。這是正向驗收前置條件，**不是 OTA 開始計費的證明**。

既有 `ota-service-rollout` 僅供 edge 啟用前的首次註冊部署；edge 已啟用後，dev 使用新增的 `deployment ota-manifest-trust` 限定更新路徑，只在確認實際 mTLS route、獨立服務 endpoint 與固定映像後，以 resourceVersion 比對增加公開信任金鑰，不重套註冊網路政策或碰核心 API。核心切流後的金鑰更新仍需協調兩個 operator 路徑。

### Development Product 裝置正向驗收 checkpoint（2026-09-30）

dev Account Manager API 與 outbox worker 已釘選同一固定映像，兩個 Pod 均啟用 Product writes；唯讀 backfill 顯示 39 個既有 Product 已版本化、待補零個。operator 暫時把 OTA catalog 從 `suspended` 設為 `active`，建立一個只選 `mqtt`、`ota` 的隔離測試 Product，確認不可變 grant revision 1 與兩個 binding 後立刻恢復 `suspended`（catalog revision 42）；價卡未啟用。該 Product 的 PKI 轉為 ready，受控 factory run 簽出一台測試裝置，其私鑰只在本機 dev SecretStore。

公開 Account Manager 網域的 Let’s Encrypt YE1 伺服器憑證鏈經 OpenSSL、系統 `curl` 驗證成功。曾失敗的 Python.org 3.13 `urllib` 用戶端，其預設 OpenSSL CA 路徑不存在；明確載入 `/etc/ssl/cert.pem` 或 `certifi` 後同一 HTTPS health 請求回 200。這不是公開伺服器的簽證缺陷。

測試裝置首次呼叫公開 OTA mTLS 入口則被 ingress 以 HTTP 400 拒絕。入口只含舊 Root／Device CA／App CA，且驗證深度 2；新 Product 裝置鏈的已釘選 Device Root 指紋為 `1faac429c8b91ed85b120a6918ee957db4e91bf960f60ef32c73f4e59a28cf9c`，需深度 3。離線驗證深度 2 失敗、深度 3 成功。公開 Root 已從 PKI 發布的 ConfigMap 與 operator ID／指紋雙重核對，存入 dev SecretStore。受限部署指令把此 Root 加入現有 device ingress client CA bundle 並設定深度 3；舊 CA 保留。更新前唯讀檢查為 `CA_ready=false depth_ready=false`，更新後同一檢查 PASS。使用 Product 裝置完整憑證鏈呼叫公開 `POST /v1/device/ota/check` 得 HTTP 200、`no_eligible_campaign`，伺服器 TLS 驗證成功；不附裝置憑證仍得 ingress HTTP 400。這證明裝置入口與 OTA check 正向授權可用，**尚未**證明任務、物件下載、收據、outbox 或 Billing 用量。流程見 [deployment operations](../deployment-operations.md)。

### Development 受控來源與 Billing 驗收 checkpoint（2026-09-30）

獨立 OTA Service 用上述 Product 建立 2,048-byte 測試 release，實際上傳物件、核對大小與 SHA-256，驗證 operator Ed25519 manifest 並發布。單一測試裝置 campaign 啟用後凍結目標數為 1；公開 mTLS check 回 `assigned`。artifact-token 產生私有 Linode Object Storage HTTPS 短效 URL（非 API GET 代理），獨立 client 的 Range GET 回 206、完整 GET 回 200，雜湊和簽署 release 相同。測試 client 在核對 bytes 後回報 `downloading`、`downloaded`；這是**模擬裝置下載回報**，不是實體裝置韌體安裝成功。未送 `installing`／`rebooting`／`verifying`／`succeeded`，campaign 隨後暫停以防繼續派送。

唯讀資料庫核對該 Product 的 `ota_task_receipts`、`ota_download_receipts`、`ota_artifact_objects` 各一筆，Product grant revision 均為 1；canonical outbox 的 `device_task`、`successful_download_gib`、`artifact_write` 各一筆且全部已投遞，Billing 三種 meter 各接受一筆並保留 revision 1。儲存 meter 需完整 UTC 月封存，本月結束前不能產生正式月 fact。dev 生效 OTA 價卡、OTA pricing publication、OTA invoice line 均為 0，目錄仍 suspended、核心 cutover 仍關閉。這完成三種 meter 的受控來源與跨服務投遞驗證；完整月儲存、實體安裝、seal／關帳、顧客畫面和正式價卡仍待驗收。

同日 dev Billing 唯讀盤點顯示 7 個 active TWD 帳戶、0 個 `pricing_plan_versions`、0 筆 `pricing_rates`、0 張 `billing_invoices`、0 筆 OTA draft／publication，已有三種各一筆 OTA usage fact。價卡在該環境是全帳戶共用；任何發佈都影響所有 dev TWD 帳戶。**2026-09-30 使用者已核准將 11 項非 OTA 最高研究參考數字作為 dev 專用正式測試費率，OTA 四項仍保留原核准價並另列參考價。**這是價格決策，研究快照本身仍不是帳單依據；只有 Billing 的有效 version 才會收費。完整非 OTA 首版候選放在 `cloud_env/dev/pricing-initial-rates.json`，另含兩項 MQTT bytes 零元診斷列，精確費率、單位與比例由受控首版發佈流程綁定摘要。非 OTA 的 Shadow、WebRTC、Storage、Logger 與其他 API 目前缺合格的 Billing facts；即使有單價，未經來源驗收也不生成這些費用。

固定版 workspace [#622](https://github.com/hkt999rtk/rtk_cloud_workspace/pull/622) 已合併並在 dev 執行受限核心切流：舊 registrar 停止、獨立 OTA Service 與裝置 mTLS edge Ready、Product 嚴格授權開啟，核心 API 換至固定映像 `sha256:096d91197cd0e372aaf5ca02014e1c8f4f8dd736a930c04483283c8054e9b97a`，只新增 OTA upstream 設定；rollout 1/1 Ready、操作旗標持久化，事後唯讀檢查 PASS，公開 `https://video-cloud-dev.realtekconnect.com/healthz` 經 TLS 驗證回 200。這完成 dev core 路由切換，**沒有**啟用 OTA 價卡或 catalog；先前受控模擬下載不能代表實體韌體安裝。

首版 dev 基準價卡採台灣營業稅 5% 的整張帳單計稅，透過受審核的首版發佈 API 建立不可變摘要／核准紀錄，排程在**2026-10-01 00:00 UTC** 生效；先前已開始的月不追收。候選檔依 Billing canonical 格式重新計算的 SHA-256 摘要是 `10dac416e791da9e59c487486f12c6e44e5133cbf11a0d215ceb2f1fc83ff03a`，與發佈審核紀錄相同。待四種 OTA 月度來源與封存證據完整後，再依既有 OTA draft／review／兩位審核者／未來 UTC 月發佈流程加上四項原核准價。dev 對象與正式價仍須在登入後 Cloud Admin「Billing > Service Pricing」完成線上驗收；價格頁的研究參考數字保持獨立欄位，不能因數字相同而冒充有效費率。

### Development 首版正式測試價卡發佈 checkpoint（2026-09-30）

固定版 workspace [#623](https://github.com/hkt999rtk/rtk_cloud_workspace/pull/623) 已合併到原指定基線 `f0ea783eb58b9ed434b4266f15be9c9b01812541`；Billing [#46](https://github.com/hkt999rtk/rtk_billing/pull/46)、[#47](https://github.com/hkt999rtk/rtk_billing/pull/47) 與 Cloud Admin [#441](https://github.com/hkt999rtk/rtk_cloud_admin/pull/441)、[#442](https://github.com/hkt999rtk/rtk_cloud_admin/pull/442)、[#443](https://github.com/hkt999rtk/rtk_cloud_admin/pull/443) 已先合併。workspace 的桌面／手機瀏覽器驗收、Billing PostgreSQL、Cloud Admin Go／JavaScript、價格目錄與合約檢查均通過。dev credential 唯讀檢查 10/10 通過；更新前 Billing schema 為 070，價卡、帳期、發票均為 0。

dev 執行一次性的 Billing migration 071 後，核對 `reviewed_initial_pricing_publications` 不可修改觸發器存在；Billing API、payment worker、settlement collector、payment simulator 均更新至固定映像 `sha256:8e941d8a58c29abe2f048b3f8c18f56d05aa6b464047597f804e09b6fedfa3bc`，Cloud Admin 更新至 `sha256:0b9cc51ee095bb653e350ac30768738fce7d8f040ed2df6a1b42204d58a7b7e1`。五個部署與實際 Pod 均為 1/1 Ready，公開 Billing／Admin health 通過；兩個映像引用已寫入 dev operator SecretStore。更新採既有映像與 Kubernetes resourceVersion 比對，Cloud Admin 保持 `Recreate` 策略。

首張 TWD 價卡 `400fff59-0142-428d-9eca-d620860042f5` 經 authenticated `publish-reviewed-initial` 發佈，從 **2026-10-01 00:00 UTC** 才可作為當期價格。資料庫唯讀核對為 1 個已發佈版次、13 筆費率（11 筆非 OTA 定價與 2 筆 MQTT bytes 零元診斷列）、0 筆 OTA 費率、1 筆不可修改審核紀錄；紀錄摘要與上述候選完全相同，帳期與發票仍為 0。台灣營業稅採 `invoice_total`／500 basis points／`half_up`，不逐項加稅或把 OTA 視為免稅。沒有合格來源 fact 的其他服務不會因安裝價卡而憑空產生費用，OTA catalog 與 OTA 價卡仍維持關閉。

公開 dev 登入頁回 HTTP 200，未登入查詢參考價與正式價 API 均回 HTTP 401；此次沒有可用的 dev 登入瀏覽器 session，因此**登入後正式價格頁與預告價卡的 live 畫面仍待驗收**。合併前桌面與手機 E2E 已驗證相關顯示和授權路徑；這不取代實際帳戶的線上驗收，也不代表 OTA 四項來源或實體韌體安裝已完成。

dev 首版候選的**未稅** rate identity 與展示單位如下；每個 request／unit 都按實際數量比例計價，百萬次不是最低級距。只有 MQTT 計數已能產出一般 Billing fact；其餘收費項目須先完成各自的來源與授權驗收，不能從定價表推斷已產生帳款。

| Service / metric | 展示價 | Billing unit / scale | dev 來源狀態 |
| --- | ---: | --- | --- |
| `mqtt.publish_count`、`mqtt.delivery_count` | 各 NT$48／百萬次 | `requests`／0 | 已有一般 fact |
| `mqtt.publish_bytes`、`mqtt.delivery_bytes` | NT$0／bytes | `bytes`／0 | 診斷 fact；訊息費仍照收，流量不另收 |
| `shadow.operation_units` | NT$60／百萬個 1 KiB 單位 | `units`／0 | 計量待驗收；AWS 比較值用 1 KB，兩者不等價 |
| `webrtc.turn_relay_gib` | NT$4.80／GiB | `GiB`／9 | relay 出口 fact 待驗收 |
| `storage.clip_storage_gib_month` | NT$1.30／GiB-month | `GiB-month`／9 | 實物件 byte-time 待驗收 |
| `storage.clip_object_write` | NT$224／百萬次 | `requests`／0 | 成功寫入 fact 待驗收 |
| `storage.clip_object_read` | NT$17.92／百萬次 | `requests`／0 | 成功讀取 fact 待驗收 |
| `storage.clip_download_gib` | NT$4.80／GiB | `GiB`／9 | 送達位元組 fact 待驗收 |
| `logger.ingest_gib` | NT$28.80／GiB | `GiB`／9 | 收集總量已有，通用發票 fact 待驗收 |
| `logger.retention_gib_month` | NT$1.31／GiB-month | `GiB-month`／9 | 原始 bytes×設定保留天數／30 的計量待驗收；AWS 比較值按壓縮 bytes |
| `api.data_request` | NT$136／百萬次 | `requests`／0 | 成功路由分類與 fact 待驗收 |

## 1. 已確定的決策與範圍

- 使用者於 2026-09-26 核准 **OTA 四項單價**：首次裝置指派 NT$96／1,000 次、已驗證下載 NT$0.96／GiB、實體韌體儲存 NT$0.96／GiB-month、成功建立韌體物件 NT$144／百萬次。這是商業單價的核准，**尚非 Billing 資料庫中的生效價卡**；正式生效月仍待核定。development 的程式部署已完成，但不代表價格生效。
- 付費 Managed Cloud 中，**Product 選用 OTA feature 才適用 OTA 用量費**，不另要求合約逐項選購 OTA。2026-09-27 確定付費資格由該 UTC 月全程 `commercial` 層級證據加上結算時 `active` Billing 帳戶判定，**不另設合約／方案核准標記**；缺少任一證據時 OTA 金額暫緩審核，不自動收費。計費須以該次用量的 Product 與授權證據為準；沒有可驗證 Product 授權的 OTA fact 不得自動計入帳單。Evaluation 免費及 Private Cloud 另依既有商業模型與合約處理。Product 關閉 OTA 後，原授權建立的任務若後續完成，仍計至任務完成；韌體儲存仍計至物件實體刪除。關閉後不得建立新 OTA 任務或物件。授權撤銷前後的事實需保留原始 grant revision／digest 供稽核。
- 使用者決定**OTA 不另設一筆稅金**：OTA 與其他服務先合計未稅費用，再按台灣現行一般營業稅率 **5%（500 basis points）**對**帳單未稅總額一次計稅**，金額以 TWD 按既有 `half_up` 規則取整，並確定性分攤到明細。財政部[目前稅率說明](https://www.etax.nat.gov.tw/etwmain/tax-info/innotative-tax-e-reference/filing/business-tax/wMDMRl7)列示 5%。不得將 OTA rate 的預設 `tax_rate_basis_points=0` 解讀為 OTA 免稅，也不得在 OTA line 上再加一次稅。此階段只處理 Billing 計算與帳單揭露，**不實作政府電子發票開立／申報流程**；既有內部 Billing invoice 紀錄及明細仍保留。稅率變更需建立新價卡版本，不重算已開立帳單。
- 詳細價格只在**登入後的 Cloud Admin「Billing > Service Pricing」**揭露；公開官網依 [business-model.md](../business-model.md) 只說明 evaluation 免費、private commercial 的授權與維護費需報價，不刊登具體數字。用量費率與 private deployment 的一次性授權／年度維護費必須分開說明。
- 核准價與參考價的數字也必須受 Cloud Billing 權限保護：匿名可下載的 HTML、JavaScript 或翻譯資源不可夾帶完整價表；畫面取得授權後才呼叫受保護的資料端點，失敗時不顯示數值。
- OTA 仍是可註冊、可被 Product 選用的獨立服務。只有 Product 啟用 OTA 才顯示其 dashboard；未啟用時明示「此產品尚未啟用 OTA 服務」。Product 選用服務、費率生效、實際產生用量，是三件不同的事。
- 其他服務目前畫面中的金額屬**參考價／研究草案**。不得把研究價直接當作已生效價，也不得因計量器存在就宣稱正在收費。任何 Cloud／環境的正式費率只由 Billing 的有效 pricing version 決定。

### 執行決策（2026-09-29）

使用者已授權由執行者決定剩餘技術與過渡規則，採用以下做法：

- **UTC 銜接**：最後一個舊帳期從原時區的前一個月初開始，結束時間延長或截短到 OTA 正式生效的 UTC 月初。整個舊帳期沿用原完整價卡與原稅規則；生效前 OTA 事實仍保留但不收費。不得把 8 小時差額單獨套用新價，也不得按時間比例拆分已封存的用量事實。
- **執行介面**：Billing 提供先唯讀預覽、再以相同摘要執行的銜接關帳命令。預覽核對正式發佈紀錄、舊／新價卡、帳戶與 owner、profile、完整用量及帳期衝突，列出期間與舊價估算；執行時重新檢查並與一般關帳共用交易鎖。尚未到切點、已開票或有其他重疊帳期、跨邊界不可切分用量、profile 或 owner 證據不足時停止，保留原資料供人工處理。切換完成後使用完整 UTC 月。
- **月中移轉／關閉**：暫不自動按人頭或天數分攤 OTA。該月保留完整來源證據，暫停自動開立 OTA 帳單，依既有待審原因交人工核對。此為本次交付規則，後續自動分攤是獨立功能。
- **PKI（2026-09-29 修訂目標）**：沿用環境專屬 Root 與受控工具。dev Service 中繼憑證只增加已審閱的七個 client subject 及一個 DNS，保留現有允許項目。新環境建立及後續 Service 憑證更新皆由該環境 operator 依設定中的固定身分、簽署政策、申請摘要與稽核執行；不要求第二位人員或獨立 `pki_admin` 核准。dev 控制器已於 2026-09-29 完成單一 operator 授權切換、簽署與 consumer 回報；staging／production 仍須各自遷移和驗證，不能以假核准或資料庫改寫繞過。
- **下載資料路徑（2026-09-29 更新）**：首版讓裝置使用短效 HTTPS 物件儲存簽名 GET URL，直接從私有韌體 bucket 下載；OTA API 只檢查 Product／裝置授權、簽發 URL 並保存 artifact grant，不轉送韌體位元組。URL 最長十分鐘，受 manifest 到期時間限制，先驗證實際物件 endpoint、Range、到期和隔離。客戶下載量仍以裝置驗證完整大小與 SHA-256 後的首次 `downloaded` receipt 計算，URL、物件 GET 或失敗重試都不直接計費。Akamai CDN、DataStream 收集與成本對帳留作後續擴充，不是首版啟用、月封存或價格生效的前置條件。若將來切至 CDN 模式，須另行驗證該模式的完整日誌、金鑰和封存規則，不混用同一帳期的兩種證據。

上述決策不等於已完成實作或已發佈正式價卡。正式生效月在環境驗收完成後選擇未來完整 UTC 月，仍不追收舊月份。

### 本次程式交付（2026-09-29）

- Billing [#44](https://github.com/hkt999rtk/rtk_billing/pull/44) 已合併至選定固定基線：新增 `ota-cutover-bridge` 的唯讀審核及摘要核對執行介面、不可變回執與 migration 070。最後一個舊本地帳期沿用舊完整價卡及稅規則，普通關帳不能略過橋接；帳單用量預覽也顯示同一橋接期間。以隔離 PostgreSQL 16 執行完整 Billing 本機測試，驗證舊價、切點前保留、重放、過期審核摘要、遲到事實與回執不可變。這項程式交付**未選擇生效月、未發佈 OTA 價卡，也未對線上帳戶關帳**。
- Cloud Admin [#438](https://github.com/hkt999rtk/rtk_cloud_admin/pull/438) 已合併至選定固定基線：登入後「Service Pricing」對下一版已發佈價卡列出每項單價、單位與精度，和當期有效價及研究參考價分開。前端 build、匿名資產價格掃描及未來 OTA 費率的本機瀏覽器驗收通過；這不是實際已啟用 OTA Product 或帳單的驗收。
- Deployment identity 的契約 [#185](https://github.com/hkt999rtk/rtk_cloud_contracts_doc/pull/185)、Video Cloud [#727](https://github.com/hkt999rtk/rtk_video_cloud/pull/727) 及 workspace [#606](https://github.com/hkt999rtk/rtk_cloud_workspace/pull/606) 已合併至各自固定基線，CI 均通過。憑證檢查可拒絕 inventory 後附加資料，並辨識 `_IDENTITY_STATE_FILE` 管理者。這些是部署身分工具與文件，**未代替 dev／staging 實際簽發或安裝身分**。

### Development 真實驗收與服務目錄門檻（2026-09-29）

以既有 development 平台測試帳戶完成受保護 API 與實際瀏覽器唯讀驗收：未登入取價為 401；登入後三個可讀 Cloud 的參考價、正式價與 Product API 均為 200。參考價各有 15 項，其中 OTA 四筆核准價與本文件一致；Billing 回傳無當期／預告價卡、`ota_eligibility=not_priced`。瀏覽器價格頁顯示四筆「已核准待生效」，選到未啟用 OTA 的 Product 時顯示停用提示；OTA 頁不載入儀表板。這只證明**未啟用 Product 的呈現**，尚無已選 OTA Product、真實用量或已開立 OTA 帳單的驗收。

同一帳戶可讀的 32 個 Product 均未選 OTA。三個 Cloud 的 `/service-options` 都回傳空 `options` 與 `product_writes_enabled=false`；dev operator 的 `ACCOUNT_MANAGER_PLATFORM_SERVICE_PRODUCT_WRITES` 也是 `false`，Account Manager Service 沒有私有 `service-registry` port，Video Cloud 沒有獨立 `video-cloud-otaservice` Deployment。Account Manager 封裝的第一次唯讀 grant 回填報告對全環境 39 個既有 Product 回報 `ready=true`、`needs_backfill=39`、`already_versioned=0`、`issue_count=0`。**同日後續已完成 development 回填**：短暫凍結五個 Account Manager 寫入工作負載、建立 FileVault 保護的私有 PostgreSQL dump／globals 及 SHA-256 manifest，完整解析歸檔後以相同的審核 digest 執行單次交易；後驗報告 `already_versioned=39`、`needs_backfill=0`、`issue_count=0`，獨立 SQL 核對 39 個 Product 各有一筆 legacy revision 1 grant。五個工作負載恢復 5/5 Ready，登入及 Product 讀取通過。這次只保存**原有選項**，沒有替舊 Product 加上 OTA。

同日對 development 39 筆 legacy revision 1 grant 做跨服務唯讀查核：Account Manager 受保護的歷史 `/ota-grants/1` 查詢全部回應 200，逐筆重算的 digest 均與資料庫不可變 snapshot 相符，且授權有效起點存在；目前 `/ota-grant` 查詢亦全部回應 200，revision／digest 相符且 OTA 均為停用。兩個路徑均回傳 `Cache-Control: no-store`，匿名歷史查詢回應 401。這證明**現有未啟用 Product 的 grant 回填與查詢**，不證明 OTA-enabled Product、任務原授權、四項用量或 Billing 收據。

七個 service-registration 身分 Secret 仍不存在；因此「可註冊 OTA」在程式與部署渲染器已具備，**development 的實際服務目錄與 Product 寫入尚未切換**。受控 Service 中繼憑證已於 2026-09-29 完成；依 [dev Product cutover](../product-services-dev-cutover.md) 接著完成七個身分，再依序啟用 registry／MQTT／選用服務及嚴格授權；不能用 grant 回填或前端測試替代註冊 lease、物件下載與 Billing 驗收。沒有啟用 Product 寫入、OTA 價卡或計費。

### 固定版 development 更新與 Service PKI 申請／切換（2026-09-29）

在已合併的固定版上，development 的 Billing 先以 `billing-ota-070-2ecb67e3` Job 套用唯一待補的 migration 070，確認 `schema_migrations` 和 `ota_cutover_bridges`；Billing API、payment worker、settlement collector、payment simulator 更新到 PR #44 提交的 GHCR digest `sha256:66f4aa70990285e4e02712bdf62cf5c3bbe4564b0992893742497c45ed22f5a3`。Cloud Admin 更新到 PR #438 的 `sha256:2d3e5d372cecbce18c83776aa4776bb047dfcfc4ffc15ad6adc8c4edf31282ea`；Video Cloud API 在新版映像的唯讀 OTA schema verify Job 完成後更新到 PR #728 的 `sha256:957d2ce7649ff0e69d57e46a16963b560e0003524537b5de507dddecf887dc5c`。上述六個 Deployment 都是 1/1 Ready，三個公開 `/healthz` 為 200，三個映像參照已寫回 dev `operator/env` 並維持 0600。Billing 發布工作在映像與 manifest 上傳成功後因 `ci-0` 失聯而標示失敗；digest 已由 GHCR 獨立驗證。Video Cloud 同提交的重新發佈成功。

更新後再次確認：匿名價格 API 為 401；平台帳戶可讀三個 Cloud，各有 15 筆參考資料及四筆正確的 OTA 核准／參考價，當期與預告價卡皆空、`ota_eligibility=not_priced`。Video Cloud 的 task/download receipt、artifact object、OTA outbox 和 Billing OTA usage fact／pricing publication 均為 0。核心仍是 `VIDEO_CLOUD_OTA_ENTITLEMENTS_REQUIRED=false`，Product 寫入與獨立 OTA 服務仍關閉，故此更新沒有啟用 OTA 或開始計費。

已由 registry 確認 dev active Service 中繼 v9 的政策不含 `service:ota` 或新的 Account Manager 註冊 DNS。依 [dev PKI 前置程序](../product-services-dev-pki.md) 向現有 Service Root `697e8e86-5af6-4580-8456-7f91d17634f2` 建立後繼中繼 v10 的申請：operation `b4d42f12-6f21-45c6-b8d2-3df4930a88bd`，審核摘要 `d010a3b9a5193a0e001c6ed95b3f70a42e1d1b82c544fc639a50a6b1100edd99`，含文件指定的 16 個 client subject 與 3 個 DNS。這筆申請當時為 `requested`，部署的舊控制器仍要求另一位 `pki_admin`。同日後續已將單一環境 operator 授權流程部署到 dev，原操作保留相同 ID／摘要，經明確授權、OpenBao 單一內部金鑰與原操作 reconcile、Service Root 簽署、公開憑證匯入，以及 `certissuer`／`pki-controller` 對精確信任版本的回報後，v10 issuer `cf348f82-f4cc-434e-a59d-c37eee8222cf` 成為 active；v9 保持 retiring。資料庫保留不可改寫的授權列與狀態稽核，Loki 收到不含密鑰的可搜尋事件。完整恢復、指紋與部署驗證見 [dev PKI 前置程序](../product-services-dev-pki.md) 及 [operator 權限測試計畫](pki-operator-authority-test-plan.md)。新的 bootstrap session、七個工作負載身分、私有註冊 listener、服務 lease 與 Product 寫入仍未完成，OTA 價卡與計費也維持關閉。

### 執行狀態（2026-09-28）

下表是當日固定版本的歷史盤點；其中 CDN property、DataStream、edge review 的啟用門檻已由上述 2026-09-29 直連物件 URL 決策取代。其他 PKI、服務註冊、Product 授權、收據、雙 seal、價卡及帳單驗收門檻仍有效。

| 階段 | 目前狀態 |
| --- | --- |
| D0 文件與費率研究（完成） | OTA 四項核准價及未生效界線已寫入契約；本文件與研究表列出最高候選參考價、來源、非等價情況及交付順序。Billing 操作 runbook 與 Cloud Admin customer-copy 規格已合併；受控發佈與正式價 API 已部署到 development，但沒有發佈 OTA 價卡。 |
| A2 登入後揭露的過渡版（部分完成） | Cloud Admin 僅於 Cloud owner 通過 `billing_account.read` 授權後，從不快取的 `/billing/pricing-references` 端點取得 15 項參考價與四項 OTA 核准待生效價；匿名前端資產不含數字，取價失敗不顯示價表。另由受保護的 `/billing/pricing-effective` 代理讀取 Billing 當期與預告價卡，分開呈現正式價、待生效價及研究價。當 Billing 回報 `ota_estimate_status=held_for_review`，總覽與用量頁把合計標為「不含 OTA」並顯示原因，月底全額預測標為待審。已開立 invoice 的逐項用量、未稅單價、未稅小計、分攤稅額、明細總額及 Product ID 已揭露。登入後價格頁可分頁選擇目前可讀的 Product，從現有受保護 Product API 讀取 `ota` 選用狀態與 grant revision，並在四項 OTA 價格列標示目前啟用／未啟用；歷史計費仍以原 grant 證據決定。這項 Product 顯示已部署 development；已開立 invoice 的明細頁可直接連至同一 Cloud 的 Service Pricing，並提示目前價與舊帳單快照可能不同。連結已部署 development；真實登入後的已開立 OTA 帳單仍待驗收。 |
| P4 生效前 OTA 事實保護（技術部分） | Billing 已在選定版次沒有 OTA 費率時保留 immutable 事實、排除其帳單與用量估算，並阻擋目前即時 API 啟用任何 OTA 價卡；混合 MQTT 月份與不追收已有本地測試。Video Cloud 來源 outbox 對已關帳拒收保留 payload／digest、明確 `INVOICE_IMMUTABLE` 原因與重試紀錄；本機 OTA／資料庫測試已通過，跨服務 staging 對帳仍待驗收。此改動不會開始 OTA 收費。 |
| P1 費率欄位與驗證（部分完成） | Billing 已新增可為 null 的 rate `quantity_scale`、`tax_category`，保存舊版「未知」狀態；rate 宣告精度時會拒絕不符的 fact。只讀工具能在一致快照核對完整非 OTA 底卡、四項核准值／單位／精度及明示的稅務欄位，輸出確定性 rate-set digest。唯讀工具不驗證 Finance 簽核；Billing 原子建卡命令已合併 main 並部署 development，能在鎖定交易中重驗當期完整底卡、UTC 月初、四項核准價與 5% 帳單稅務政策，保存不可變 draft manifest。正式適用範圍仍須依各筆帳戶與 Product 證據判定，且沒有建立或發佈實際價卡。 |
| P2 UTC 排程（部分完成） | Billing 的一般非 OTA activation 可預先排程單一未來 UTC 月初版次，舊版於切點前仍被選取；月結與發佈共用交易鎖。一般 activation 仍阻擋 OTA；受控 OTA 發佈要求審核 digest、雙人覆核、固定稅規則與未來 UTC 月初。完整價卡的原子建卡工具已部署 development。固定 Billing 分支 [#41](https://github.com/hkt999rtk/rtk_billing/pull/41) 另提供生效前的雙人覆核取消交易：保留不可修改的發佈／取消審核紀錄、拒絕已有跨切點帳期，並原子恢復舊版價卡；已於 2026-09-28 部署 development 並完成 schema／路由驗證；月中 ownership 政策仍待完成。 |
| P3 OTA 月結與預覽保護（部分完成） | 當選定價卡含 OTA 時，Billing 只允許完整 UTC 月結算，要求目前 owner 的責任期間從該月開始前即存在，並核對帳務 profile 的 ownership version；缺失時留下明確 incomplete 原因。目前用量預覽在 OTA 有價時採 UTC 月，責任期間不足或非完整 UTC 期間則排除 OTA 估算，回報 `held_for_review` 和原因。唯讀切月工具能按帳戶列出本地時區邊界與 UTC 邊界間的空檔／重疊風險、相關 fact／帳期／發票數及 owner 證明。2026-09-28 已在 staging 以**假設** `2026-11-01T00:00:00Z` 切點執行完整逐帳戶唯讀工具：9 個 active TWD 帳戶中有 8 個 `gap_risk`、1 個缺 profile；當時橋接區間的 fact／已關帳期／發票數與目標首月帳期／發票數均為 0。這不是已核定生效月或未來用量保證；仍未完成橋接、缺 profile 及月中 owner 移轉的責任政策與正式遷移／分攤。 |
| P3 Product 歷史授權與儲存證據（程式與本機驗證完成，development 已部署） | Account Manager 的受保護內部查詢可依 Brand Cloud、Product、revision 讀出不可變 grant，重新驗證包含 Logger retention 設定的 digest，回報 `ota` 是否選用及從該版建立至下一版建立的時間區間。Video Cloud 將任務、已驗證下載、上傳預約與物件原授權保存於 receipt／來源事件／月度快照；關閉後舊任務可完成，但不得建立新任務或物件。storage 封存前會拒絕沒有原授權或相符已完成上傳預約的物件。四項 Billing fact 均可攜帶原 grant；儲存按每個實體物件、每個 UTC 月送一筆，附物件摘要與精確 byte-microseconds，分配後同 Product 合計仍與原月度積分取整值相同。Billing 以 Account Manager 歷史 revision／digest／授權時間查核各筆 grant，並重算 Product 儲存總量；缺欄位、重複物件或查詢失敗會保留月結待審，本月預估亦隱藏未驗證 OTA 金額。本機 Go、PostgreSQL 與工作區整合檢查通過；development 已部署，跨服務 grant 查詢與 staging 對帳尚未驗收。Account Manager 的歷史 grant 不能單獨重建當時 Product active 狀態或裝置授權，仍須依來源 receipt／封存證據。 |
| P3 OTA 執行程序與 outbox 投遞（部署程式已合併，實際計量未完成） | Video Cloud 的 `cmd/otaservice` 及 canonical CI 映像已有獨立執行程序；其 `RouteScopeOTA` 路徑會啟用 billable artifact namespace、不可變來源 receipt 和 `ProductOTABilling.DeliverPending`。workspace [#545](https://github.com/hkt999rtk/rtk_cloud_workspace/pull/545) 已把獨立 `video-cloud-otaservice` 納入 LKE opt-in 工作負載，接上私有物件儲存／Billing sink／Product grant／專用 `service:ota` 身分、服務註冊、核心 API 私有轉送與必要網路規則；部署前檢查阻止舊 `otaregistrar` 和獨立服務同時持有同一 instance lease，核心切流要求獨立服務有 Ready 的私有 endpoint。development 的新開關仍關閉，獨立 Pod 尚未建立；不能因 manifest 與本機測試通過就宣稱 OTA outbox 已送達 Billing。首版啟用前須驗證 HTTPS 私有物件端點、短效簽名 GET URL 的 Range／到期／隔離、服務身分、schema 及授權設定，先確認獨立程序 Ready／註冊 lease／四項 outbox 投遞／Billing 收據，再切換核心流量；任一條件失敗維持 fail-closed。CDN edge 與 DataStream 留待後續擴充。 |
| P3 帳戶層級的歷史資格（服務 main 已合併，development 已部署） | Account Manager 增加 Brand Cloud `evaluation`／`commercial` 的不可變事件及受保護的完整 UTC 月查詢；既有 Cloud 僅自遷移當下開始有證據，較早月份不可推定。完整月份若包含層級變更，便不宣稱全月商業層級。本機完整 Go／PostgreSQL、OpenAPI 契約及 Account Manager PR CI 已通過。Billing 已接入整月 `commercial_for_full_period=true` 與結算時 `commercial_accounts.state=active` 一起核對；這兩項就是已核准的帳戶付費資格，不另加合約或方案標記。Billing 月結與預覽已接入此證據並部署 development；跨服務驗收尚未完成，不得據此開始計費。 |
| P1 後續、P3 後續、A1 正式價卡／月份／價格 API | Billing 已合併受控的 OTA UTC 月初發佈路徑：以完整底卡、四項核准價、rate-set digest、兩名審核者及帳單總額 5% 稅規則驗證草案，在同一交易保存發佈稽核與版次切換；一般 activation 仍拒絕 OTA。完整審核資料的原子化建卡工具已合併 Billing main 並部署 development；目標環境的完整月份遷移尚未實作。Billing 客戶當期／預告價 API 及 Cloud Admin 代理已部署 development；該環境尚無任何當期／預告 TWD 版次，也沒有啟用 OTA pricing version。 |
| Q1、R1 環境資格與正式發佈 | staging 固定 CI 映像的 Video Cloud OTA／PKI schema 與權限 Job 已完成；環境專屬 Device Root 已建立於 `ready`，`pki-controller` 已裝入 Root ID／指紋 pin 並 1/1 Ready。三個預定 PVC 已 Bound，API／MQTT 消費端的環境專屬管理 TLS、MQTT 設定、最小權限資料庫登入及獨立 callback／runtime Secret 已備妥。**仍為 NO-GO**：Service／MQTT 受控根未完成，兩個消費端尚未部署並 ACK，Device Root 尚未 `active`；獨立 OTA 程序與 DataStream collector 未啟用，也沒有真實 CDN 日誌、四項 outbox／Billing 收據或 staging 端到端對帳。Billing 完整 UTC 月份遷移、正式價卡與首月帳單驗收仍未完成；OTA 尚未收費。先前 2026-09-26／27 唯讀盤點和 2026-09-28 執行前快照保留於下文作歷史證據，不代表現在仍缺 OTA schema 或 PVC。 |

### staging 底卡的唯讀盤點（2026-09-27 歷史快照）

固定 workspace 版本 `ac44a6d2ecf8ac4ed8f9531a00516263348312f4` 的 staging Billing 仍在 schema 061；目前有效 TWD 底卡含四筆 MQTT 及一筆 `qualification/staging_units`，沒有 OTA rate。五筆舊 rate 均缺 `quantity_scale` 與 `tax_category`；現有 staging 用量的五種 metric 都以精度 0 記錄，但仍須逐筆確認正式語義。舊版費率有早期重疊區間，四張已結算帳單保留各自版次；不得改寫歷史底卡或已出帳紀錄。可分享的彙總與快照雜湊記於 [staging Billing evidence](../billing-twd-staging-evidence-20260924.md)，完整底卡僅留受限操作證據，不在本文件刊登實際價格。

Billing [#39](https://github.com/hkt999rtk/rtk_billing/pull/39) 已在原定 Billing commit `78572b91bb2f4806c5c8f02ab62610e80e2c4a25` 上補足舊底卡的受審核草案路徑並通過 CI：完整候選卡只能明示填補原本為 null 的精度與稅別，不能更動已知欄位、單價或其他非 OTA 費率；原子建卡時重驗唯讀審核的 rate-set digest。此 leaf 修正已由 workspace [#558](https://github.com/hkt999rtk/rtk_cloud_workspace/pull/558) 納入固定分支並部署 development；沒有建立或發佈 staging 價卡。`qualification/staging_units` 是否保留，以及五筆 rate 的精度 0／`standard` 稅別如何正式核准，仍是建卡前的明確決策。staging PKI 的 API／MQTT 消費端和 Device Root 信任也未就緒，維持 **NO-GO**。

### staging Product PKI 消費端缺口（2026-09-27 歷史快照）

固定版本的 staging Video Cloud namespace 有一般 API、一般 MQTT 及 PKI controller，但沒有 `video-cloud-api-pki`、`mqtt-pki` 工作負載，也沒有匹配的專用 Secret、ConfigMap 或 PVC。development 的現行實例證實 API 消費端另需伺服器 TLS 身分、唯一 Device Root 公開信任、Service/MQTT 信任與持久狀態；MQTT 消費端由 EMQX host 與 `pkibroker` 兩個容器構成，另需 broker runtime／management 身分、配置、Root 信任及各自的資料／狀態 PVC。development 的私鑰、憑證和環境信任不得轉用 staging。

依 [PKI staging rollout](../../repos/rtk_video_cloud/docs/pki-staging-rollout.md) 先建立 staging 專屬的消費端身分與儲存、核對 CI 映像及 OpenBao role／policy，再以預留的 staging Root UUID 執行一次性 bootstrap；取得真正的 Root 公開憑證後，兩個消費端必須安裝同一 Root、驗證 registry／裝置生命週期並回報 receipt，最後才可啟用 Root 和進行 OTA 裝置驗收。僅寫入 controller pin 或複製 development Deployment 都不能通過此關卡。這些資源在目前 staging 仍不存在，因此沒有執行 Root bootstrap 或 staging 服務部署。

### 固定版本的部署與 staging 門檻（2026-09-28）

本次 development 部署的 workspace 固定快照為 `0066152b8b965a6ed3d54db3ff2d1de2989263af`（固定分支 `codex/ota-fixed-base-ac44a6d`），釘選 Billing `37f97e2afd12ae035147cdfb9aa6d5f10e395c24`、Cloud Admin `62e8a4b8eb816616512af2801430d40b726e5bb9`、Video Cloud `890952217ee02ed1dcd9ef5802868c55cdf35f97`。此分支延續原選定的 workspace `ac44a6d2`，沒有匯入之後的 `main`。Cloud Admin 與 Billing 固定版已部署 development；Billing 的 API、payment worker、settlement collector、payment simulator 均 1/1 Ready，持久化 operator 映像與 live imageID 同為 canonical CI digest `sha256:d3dd21e0…`。公開 Billing `/healthz` 回應 200；實際登入後價格／帳單操作仍未驗收，OTA 價卡也未生效。

staging 目前仍為 **NO-GO**。Billing schema 為 061，現行映像比選定固定版新；固定版 062–068 的 Job 已以 canonical Billing 映像 `sha256:d3dd21e0…` 做 Kubernetes client-side dry-run，**沒有套用**。Video Cloud 的 staging 專屬 Device Root、API／MQTT PKI consumer 身分與信任資源均未建立。固定 Video Cloud commit 的官方 image-only release [run 36338096862](https://github.com/hkt999rtk/rtk_video_cloud/actions/runs/36338096862) 已發布 API、EMQX-PKI、OpenBao-PKI 映像；三者的 `linux/amd64` digest 拉取和 staging 唯讀憑證檢查通過。這只證明映像可用，不證明 PKI 或 OTA 計量可用。

PKI consumer 儲存已另訂 [三卷部署環境計畫](pki-consumer-storage-plan.md)：dev 遷移至三卷並重用現有三卷，staging／prod 記錄三卷 `plan-only` 佈局（各 10 GiB，合計 30 GiB）。固定 Video Cloud renderer 已由 PR #722 合併至選定的 `codex/staging-pki-consumers` 分支（`916bc8b`），要求輸入 staging 三卷設定並驗證掛載隔離；workspace 尚待 PR 納入該 gitlink，且 staging 額度仍不足，因此不得先建立新卷。Device Root bootstrap 要等環境專屬身分、trust、consumer、卷配置與容量均備妥後才執行。

2026-09-28 新一輪 Linode API 唯讀盤點為 **44** 個 active services：11 台 VM、33 個 volume、0 個 NodeBalancer；先前報告的 54 是清理前快照。33 個 volume 中，32 個對應目前 dev/staging 叢集 Bound PVC，且各有 Pod 或 Deployment/StatefulSet/Job 引用；其餘 1 個未掛載的 `openbao-recovery-20260818-1948` 是復原卷，處置前須核對備份／還原責任。Linode 支援單 #26820507 唯一確認的上限是 20，#27347355 僅關閉重複請求；CDN 單 #27523436 未確認提高 active-service 上限。原有部署的 `additional_required=0` 例外只容許不增加服務數的更新。若採三卷計畫，至少投影到 **47** 個；須先確認實際核定額度或經審查釋放資源，並重新通過包含最終卷數的前置檢查，才可建立 consumer PVC。

2026-09-28 隨後完成 development PKI consumer 三卷重用與舊卷清理：已刪除確定不再被引用的兩個 legacy PVC、其 Retain PV 與 Linode volume。Linode active services 由 44 降至 **42**（11 台 VM、31 個 volume）；剩餘未掛載的 OpenBao recovery volume 因保留責任尚未核定而保留。依 staging 三個新 10 GiB PVC 計畫，若其他資源不變，服務數至少達 **45**。工單仍只明確證實 20 的舊上限，故需取得可驗證的新額度或再釋放資源，通過前置檢查後才可在 staging 建卷。此現況更新不改寫上方清理前的歷史快照。

Billing 對歷史 Product OTA grant 的跨服務查詢採 `LKE_BILLING_OTA_GRANT_HISTORY_ENABLED=true` 明確啟用：renderer 同時設定 `BILLING_OTA_GRANT_HISTORY_BASE_URL`、沿用 Account Manager 既有內部授權 token，並只允許 Billing namespace 中 `app.kubernetes.io/name=billing` 的 Pod 連至 Account Manager TCP 8080。未啟用時不渲染連線憑證或專用 NetworkPolicy；啟用前驗證 token 長度及與 Billing token 不同。固定版 workspace PR #570 已合併到選定分支；development 已套用該入口 NetworkPolicy、Billing runtime 的 endpoint／token，並對原映像做有範圍的 rollout。Billing Deployment generation 22 已被觀測，1/1 Pod Ready；新欄位與 Account Manager live token 相符，其餘 Secret 欄位未變。另以 Billing namespace 中相同網路政策標籤、readiness 永遠失敗的短暫探測 Pod 驗證：Account Manager `/v1/health` 回 200；同一個不存在的歷史 OTA grant，無 token 回 401，使用 Billing runtime 中的 token 回 404。探測 Pod 已刪除，Billing 仍 1/1 Ready。這證明私有路徑與內部授權可用，尚未證明 Billing 用真實 grant 查核 OTA fact，亦不代表 OTA 價卡已生效。

2026-09-28 development Billing 對**假設**的 `2026-11-01T00:00:00Z` 執行唯讀切月盤點：7 個 TWD 帳戶中，4 個為本地月份邊界先於 UTC 的 `gap_risk` 且目前 owner 證據覆蓋切點，3 個缺 Billing profile；橋接區間與目標首月的 usage fact、已關帳期及 invoice 均為 0。這是當時的一致快照，不是正式生效月份或未來用量保證。Billing 固定版 [#40](https://github.com/hkt999rtk/rtk_billing/pull/40) 補上同 Brand Cloud／幣別帳期的交疊拒絕：新帳期即使遇到 `incomplete` 舊期也不能重疊開單；完全相同的舊期仍可重試，已開立的歷史發票即使資料庫存在早期重疊列也可原樣讀取。這項防重複保護**不會補齊 gap 或決定橋接區間由誰付費**；橋接、缺 profile、owner 移轉與正式生效月仍須逐環境審核。

2026-09-28 staging Billing 另以固定版 `ota-cutover-audit` 在單一 read-only repeatable-read 快照，對**假設** `2026-11-01T00:00:00Z` 執行完整逐帳戶盤點（觀測時間 `2026-09-28T03:21:21Z`）。9 個 active TWD 帳戶中，8 個 Asia/Taipei profile 的舊月邊界是 `2026-10-31T16:00:00Z`，均為 `gap_risk`，且現有 owner 證據在切點為 `complete_at_cutover`；另 1 個沒有 Billing profile。9 個帳戶的橋接區間 usage fact／已關帳期／invoice 及目標首月帳期／invoice 全為 0。完整去識別化逐帳戶 JSON 僅保存在 staging 環境受忽略的 `cloud_env/staging/runtime/state/ota-cutover-audit-20260928.json`（權限 600；SHA-256 `9930fbf6f7c911720fc46da1082166d6a27049c9fd7be8909c017760ed030a09`），不納入公開儲存庫。此盤點沒有修改資料庫；因切點尚未核定且未來可能新增帳期、事實或 ownership 事件，正式遷移前必須對核定月份重新執行並逐帳戶處理 8 個空檔與 1 個缺 profile。

2026-09-28 固定 workspace [#575](https://github.com/hkt999rtk/rtk_cloud_workspace/pull/575) 釘選 Billing `edf19ba8fcacf04404a1de032112774fb1cb0bb5`，並未匯入之後的 `main`。Billing [Release Bundle run 36371914882](https://github.com/hkt999rtk/rtk_billing/actions/runs/36371914882) 從該提交發布 `billing-twd` 映像 digest `sha256:17095a10fa3507dfc3f85cba89c617c1422ff32be667a2fc2cac75a3362f6ff6`。development 唯讀部署憑證檢查 10/10 PASS；先以此映像的一次性 Job 套用 Billing schema 069，確認 `ota_pricing_cancellations` 表及發佈／取消審核紀錄的兩個不可修改 trigger，再更新 Billing API Deployment 與持久化 operator 映像。migration Job 完成，API 1/1 Ready 且無重啟，`/healthz` 與 `/readyz` 均回 200；取消端點未授權回 401、已授權但不存在版次回 404。新 Pod 引用的 `billing-runtime` Secret 已設定 `BILLING_DB_MIGRATE_ON_STARTUP=false`，operator 亦保留一次性 migration Job 設定。development 的 OTA rate、發佈與取消紀錄數仍各為 0；這是流程與路由驗證，**沒有建立或生效 OTA 價卡，也沒有驗證真實取消財務交易**。

2026-09-28 staging 唯讀比對釐清 Billing 切換方向：live API 映像 `sha-f1614c0bb59d`（imageID `sha256:c308cb8987d18dde893178e06e31301178683c8e2fe91c872f1816bcd3affcfa`）的來源提交是固定 Billing `edf19ba8fcacf04404a1de032112774fb1cb0bb5` 的祖先，因此選定映像是向前升級，並非回退。staging 資料庫仍有 22 個 migration、最高 `061_twd_currency_policy_and_pricing_history.sql`；live `billing-runtime` 尚無 `BILLING_DB_MIGRATE_ON_STARTUP` 欄位。固定版 069 一次性 migration Job（明確使用 CI digest `sha256:17095a10fa3507dfc3f85cba89c617c1422ff32be667a2fc2cac75a3362f6ff6`）已通過 staging Kubernetes server dry-run，**未建立 Job、未變更 Secret 或資料庫**。正式部署時仍須先通過受保護環境 Go/No-Go，保留 rollback 映像與 schema 基線，在固定版 API rollout 前先設定新 Pod 的 startup migration 為 false、執行 062–069 一次性 Job 並驗證；這是部署順序，server dry-run 不是資料遷移成功或 OTA 收費資格。staging 規則不要求完整本機資料庫匯出；本次沒有匯出歷史帳單資料，production 的備份要求不受影響。staging PKI／CDN、底卡語義與收費生效月仍未核定，故目前不執行 staging migration、價卡發佈或實際計費。

### OTA 裝置路由切換順序

獨立 OTA 服務先取得 `service:ota` 身分、私有物件儲存 HTTPS endpoint／簽名權限及 Product 授權，再啟用 `LKE_OTA_SERVICE_REGISTRATION_ENABLED`，確認 Pod Ready、lease 與私有 Service endpoint。之後單獨啟用 `LKE_OTA_SERVICE_EDGE_ENABLED`，使 `device.<VIDEO_CLOUD_DOMAIN>` 上的 `/v1/device/ota/` 經要求裝置憑證的 ingress 送到獨立服務；須實測憑證有效、無憑證拒絕、check／artifact-token／events 成功及物件 URL 直下載、Range、到期。最後才啟用 `LKE_OTA_CORE_CUTOVER_ENABLED`；部署前檢查必須看見**實際已生效**的 mTLS ingress path 和 Ready OTA endpoint。一般 public API host 不能代替裝置 mTLS 入口。回復時先恢復核心 handler，再移除裝置 edge route；若核心 Deployment 暫時不存在但線上 ingress 仍有 OTA 路由，必須保留該路由，待核心 handler 恢復後才可移除。OTA 裝置模擬器的控制請求須用裝置 mTLS host 與各裝置憑證；物件下載用不帶裝置憑證的獨立 client。以上路徑程式碼完成、PR 與 live 驗收前仍屬待完成項。

### development 部署與計費狀態（2026-09-27）

- 已合併 contracts #177、Account Manager #352、Billing #35／#36、Cloud Admin #427／#428 及 workspace #534／#535／#536／#537。development 初次以最新 main immutable digest 更新 25 個 Deployment，隨後將 Cloud Admin 更新至 `a163fdc63ec4`（`sha256:6eb0a7ce…`），將 Billing API、payment worker、settlement collector 與 payment simulator 更新至 `440d18c419b5`（`sha256:2171f712…`）。各目標 Pod 均 ready，執行中 image ID 與目標 digest 相符；Video Cloud、Billing、Cloud Admin、Frontend 健康端點及 Account Manager `/v1/health` 回應 200。
- development Account Manager migration 093 與 Billing migration 068 已套用；`ota_pricing_drafts` 表存在且筆數為 0。Cloud Admin 已部署逐項 invoice 金額與 Product ID 揭露，但因沒有已開立的 OTA 帳單，真實 OTA 帳單畫面尚未驗收。先前登入授權的 Billing 當期／預告價 API 回應 `current_version=null`、`current_rate_count=0`、`upcoming_version=null`、`ota_eligibility=not_priced`；更新後資料庫再次查得 active TWD 價卡、OTA 草案及 OTA 發佈紀錄皆為 0。這證明程式部署與價格尚未生效，**不證明 OTA 端到端計量或發票已驗收**。
- 2026-09-27 workspace #545 合併後，從其 main 版次執行 development 唯讀憑證檢查為 **10/10 PASS**；部署計畫所列六組主要服務 image digest 與 live Deployment／持久化 operator 設定一致。該 PR 只增加預設關閉的獨立 OTA 服務部署入口，沒有新的服務映像，因此未重啟單副本 `Recreate` API。唯讀盤點確認 development 沒有 `video-cloud-otaservice` Deployment、`ota-cdn-runtime` 和 `ota-service-platform-identity` Secret，也沒有 CDN base URL 或新 OTA 切流開關；核心 API 仍為 `VIDEO_CLOUD_OTA_ENTITLEMENTS_REQUIRED=false`。Video Cloud 的 OTA 任務 receipt、下載 receipt、artifact object 及 OTA outbox 候選筆數皆為 0；Billing 的 `service_code=ota` usage fact、OTA period seal、pricing draft／publication 也皆為 0。目前沒有執行中程序能把新 OTA outbox 事實送入 Billing，**不能宣稱已收集實際收費 log**。
- 2026-09-27 workspace [#547](https://github.com/hkt999rtk/rtk_cloud_workspace/pull/547) 已合併：獨立 OTA 服務可在裝置 mTLS ingress 取得 `/v1/device/ota/`，核心切流前驗證線上路由與 Ready endpoint，回復時若核心 Deployment 暫缺仍保留既有 OTA 路由；裝置模擬器以各裝置憑證呼叫控制 API，CDN 下載不帶裝置憑證。PR 最終 head `95b442fe3fc1` 的本地差異覆蓋率為 83.70%（77/92），PR CI 全部通過；合併提交為 `fa09b9910adf`。合併後再次以唯讀憑證檢查取得 **10/10 PASS**，核對六組主要服務的 canonical CI 映像 digest，均與 main 釘選的服務提交、dev live Deployment 及持久化 operator override 一致，因此沒有對相同映像做無益重啟。dev 仍無 `video-cloud-otaservice` Deployment、OTA CDN／身分 Secret 或裝置 OTA edge 路由，`VIDEO_CLOUD_OTA_ENTITLEMENTS_REQUIRED=false` 與 `ACCOUNT_MANAGER_PLATFORM_SERVICE_PRODUCT_WRITES=false`；程式已合併不等於獨立服務已啟用。完整平台 provision 的唯讀 preflight 因缺少已確認的 `LKE_ACTIVE_SERVICE_LIMIT` 為 NO-GO；不得猜值或用它取代有範圍的 dev 啟用程序。
- Cloud Admin #429、workspace #539 已合併；development `cloud-admin` 已更新至 Cloud Admin main `2e04a9a3a264` 的 canonical CI 映像 `ghcr.io/hkt999rtk/rtk_cloud_admin/cloud-admin@sha256:c7a9781293c8dc3a31665c1f5386d19183d8b8878b75f3bcb4d4cf8bf7790954`。部署前 read-only 憑證檢查 10 項通過；使用 resourceVersion、容器名稱及舊映像測試的 JSON Patch 更新，單一 writer `Recreate` rollout 完成，Pod Ready 且實際 imageID 與目標 digest 相符；operator 的映像 override 已同步且維持 0600。公開健康端點回應 200；匿名研究價及 Product API 均回應 401。桌面／手機本機 E2E 覆蓋 Product OTA 啟用與未啟用，尚無 development 真人登入、實際 Product 的瀏覽器驗收；本次只更新 Cloud Admin，沒有建立、發佈或啟用 OTA 價卡。
- Cloud Admin #430、workspace #541 已合併；development `cloud-admin` 再更新至 Cloud Admin main `d65ed2a82e75` 的 canonical CI 映像 `ghcr.io/hkt999rtk/rtk_cloud_admin/cloud-admin@sha256:595695b402a888b8cae284783d0ee2aae9a5fe25095d97dd42803680343befe6`。部署前 read-only 憑證檢查 10 項通過；以 resourceVersion、容器名稱和舊映像測試的 JSON Patch 更新，`Recreate` rollout 成功，Deployment 1/1 Ready，Pod imageID 為目標 digest，operator 映像 override 維持 0600。公開 `/healthz` 回應 200；匿名 pricing-references 與 Product API 均回應 401。已開立 invoice 到同 Cloud Service Pricing 的連結在桌面／手機本機 E2E 通過；development 尚無真實登入後 OTA invoice 驗收。本次沒有建立、發佈或啟用 OTA 價卡。
- Billing→Account Manager 的歷史 grant 查詢入口規則及 Billing endpoint／token 已在 development 接線，詳見上方固定版部署證據；Account Manager 私有服務註冊的 `8443/TCP` 是另一條路徑。私有網路與 token 的一次性 dev 探測已通過；仍須用真實歷史 Product grant 驗證 Billing 實際查詢、失敗時待審、四項 OTA fact 與跨服務對帳；在這之前不發佈 OTA 價卡。
- 2026-09-27 development 唯讀複核：`video-cloud-api` 雖執行含 OTA 程式的映像，實際 `VIDEO_CLOUD_OTA_ENTITLEMENTS_REQUIRED=false`，`video-cloud-otaregistrar` Deployment 不存在，`ota-service-platform-identity` Secret 也不存在。`--require-product-pki` 憑證前置檢查通過，但這不能替代 `service:ota` 身分簽發、registrar lease 與 Product 授權強制檢查。故「程式映像已部署」不得解讀為 OTA 註冊服務已在 development 啟用；需依 Product services cutover 程序補齊專用身分、嚴格授權與實際註冊驗收，才能驗證來源計量。
- 2026-09-27 development 資料庫唯讀計數：`ota_task_receipts`、`ota_download_receipts`、`ota_artifact_objects`、`ota_cdn_period_reviews`、`ota_producer_period_seals`、`billing_usage_fact_outbox` 中 OTA 事實、Billing `ota_period_seals`、`ota_pricing_drafts`、`ota_pricing_publications` 與 `billing_usage_facts` 中 OTA 事實均為 0。這是「尚未產生實際營運 OTA log」的直接證據；表存在與本機測試不能代替來源程序、投遞及帳單端到端驗收。現行 LKE 未啟動 `cmd/otaservice`，而 outbox 投遞迴圈只在其 `RouteScopeOTA` 啟動，單獨啟用 `otaregistrar` 不會完成計費資料流。
- 2026-09-27 staging 唯讀複核仍為 **NO-GO**：`--require-product-pki` 在 controller 缺環境專屬 Device Root ID／SHA-256 綁定時失敗，registry 中 active staging Device Root 數為 0；Video Cloud 的 `ota_task_receipts`、`ota_download_receipts`、`ota_artifact_objects`、`ota_cdn_period_reviews`、`ota_producer_period_seals` 及 Billing 的 `ota_period_seals`、`ota_pricing_drafts`、`ota_pricing_publications` 均不存在。須先依受保護環境 Go/No-Go 程序完成 Device Root 與對應升級，再做 OTA 跨服務對帳；development 健康檢查不能取代 staging 簽核。P1 目標環境建卡審核、P3 月份切換與 ownership 處理、A2 實際 Product／invoice 的登入驗收、R1 正式生效月與正式發佈仍未完成。Product 當前適用狀態與 invoice 到價格頁連結均已部署 development。
- 2026-09-28 staging 的三個 PKI consumer PVC 已依核定三卷設計建立並綁定，每卷 10 GiB，Linode active services 從 42 增至 45；卷 ID、`Retain` 回收策略及 PVC-only 檢查見 [PKI 儲存設計](pki-consumer-storage-plan.md)。這證明本次三卷配置成功，不代表 Device Root、consumer mounts、OTA 來源計量或 Billing seal 已通過。operator 仍記錄舊的額度 20，正式新上限待 Linode 確認；後續成長不得以觀察到的 45 當作核准上限。Account Manager [#354](https://github.com/hkt999rtk/rtk_account_manager/pull/354) 的批次 Platform seal 工具已通過測試與映像 CI 並合併至固定基底 `5054b7c9`，本次 workspace 指標將該提交納入；月結排程在三個 environment 仍預設關閉，告警及 OTA producer seal 自動提交仍是正式收費前的缺口。
- 2026-09-28 staging 再次唯讀檢查：LKE adapter 預設值補齊 OTA Platform seal 開關後，一般憑證／供應商檢查 11 項全數 PASS；Account Manager→certissuer mTLS PASS，三個 PKI PVC 均 Bound 且 storage plan 為 `existing=3 new=0`。Product PKI 檢查仍因 controller 缺完整環境專屬 Device Root pin 而 NO-GO；`video-cloud-api-pki` 和 `mqtt-pki` 尚未建立。這些一般檢查通過不構成 OTA 計量或 staging rollout 的 Go。
- 2026-09-28 重新執行 staging 唯讀檢查：一般憑證／供應商檢查仍為 11/11 PASS，Account Manager→certissuer mTLS PASS；Product PKI 因缺 staging Device Root ID／指紋 pin 仍為 NO-GO。live `pki-controller` 1/1 Ready，三個既定 PVC Bound，但 `pki-device-bootstrap` ServiceAccount／Job、`video-cloud-api-pki` 與 `mqtt-pki` Deployment 均不存在；不能開始裝置 OTA staging E2E。

### UTC 月結封存排程

固定版部署腳本為 Account Manager 增加預設關閉的 `LKE_OTA_PLATFORM_SEAL_SCHEDULE_ENABLED`。三個 environment 都明確設為 `false`；啟用時使用各環境 SecretStore 的獨立 `ota-platform-seal-token`，在 Account Manager 命名空間建立僅含資料庫連線、Billing HTTPS URL 與此 token 的 `ota-platform-seal-runtime` Secret，並把同一 token 接入 Billing 的 Platform seal endpoint。CronJob 在每月 3 日 03:00 UTC 對前一個完整 UTC 月逐 Cloud 提交 deterministic seal，允許 24 小時執行及遲啟；整批失敗會讓 Job 失敗，Kubernetes 重試後仍保留失敗 Job。錯過排程或失敗時須明確指定月份重試與調查。停用排程只移除 CronJob，歷史 Job 不隨之刪除；Billing token 保留到該月份結案且沒有歷史 Job 須提交時，再另行撤銷。封存命令本身包含已停用 Cloud，以保留關閉前任務／物件的可計費來源證明。

啟用順序必須先完成固定版 Account Manager CI 映像與 Billing 069 migration、在兩端安全放入專用 token、驗證 Account Manager Pod 可經 HTTPS 到達 Billing seal endpoint，再於目標 environment 選擇 Billing 與 Account Manager 工作負載更新並檢查首個 Job 的雙方回執。此排程只涵蓋 **Platform grant seal**；OTA producer 的 CDN／物件／下載來源封存及自動提交、失敗告警、完整 UTC 月 ownership 處理仍是獨立上線關卡。不得把 CronJob 存在或成功提交一半 seal 解讀為 OTA 價卡可發佈。

OTA producer 的批次清單須以 Account Manager 的 Brand Cloud 歷史作為權威來源，而非從 Video Cloud 已發生的任務、物件、下載或 CDN review 反推。後者會漏掉零用量、已停用或軟刪除但仍須完成該月封存的 Cloud。Account Manager 應提供經內部 token 驗證、不快取、只讀且限制完整 UTC 月的清單 API，沿用 Platform seal 查詢的 `created_at < period_end` 規則；OTA producer 在每月封存時取得完整清單，對每個 Cloud 以既有 immutable `SealPeriod` 重試。清單取得不完整、某個 Cloud 缺正向 CDN delivery review、來源／物件不一致或 Billing 未確認任何一筆 seal，整批 Job 必須非零退出並留下可辨識但不洩露 Cloud ID 的失敗摘要。排程只在 CDN collector 與人工／自動正向 review 的完整性驗收之後啟用；不能由零筆 DataStream row 自動推定 review 成功。Producer 與 Platform 兩個 Job 的完成率、最舊未完成 UTC 月、重試及 Billing 雙 seal 差異須有告警和人工重放程序。這是實作規格，尚未通過跨服務 staging 驗收。

Account Manager PR #356 與 Video Cloud PR #724 已合併到選定固定版：Account Manager 的 migration 094 以資料庫鎖等待進行中的 Cloud 建立交易，對完整 UTC 月原子保存一次固定清單；Platform 與 OTA producer 都讀同一份清單，往後重試不會因補寫或跨月交易得到不同 Cloud 集合。新建組織的預設時間改用資料庫插入時間，避免長交易將月界線後才插入的 Cloud 回填到上月。Video Cloud 批次連線使用既有 `pkitrust` 私有 CA 與受管理 Service 身分路徑；受保護環境須提供 Job 自己的 `service:ota` 身分狀態與更新信任，不能共用 API 程序的身分狀態。這些程式通過本地整合測試與 PR CI，但排程仍預設關閉；CDN 記錄完整性、專用身分部署、staging 對帳與告警仍須完成。

### CDN DataStream 收集與月底審核規格

各環境使用專用、私有的 S3 相容 DataStream 目的地與讀取身分；CDN property 只記錄 OTA 韌體路徑，DataStream 採 **JSON、100% sampling、gzip**，30 或 60 秒交付一次。必要欄位為 stream/version、request ID、request time、host、method、path、status、response `bytes`、Akamai `totalBytes`、Range 與 cache status；缺欄、格式改變或來源 stream/host 不符即停止該批並告警。Akamai [欄位定義](https://techdocs.akamai.com/datastream2/reference/data-set-parameters-api)區分 response body `bytes` 與其計費流量 `totalBytes`；[JSON 範例](https://techdocs.akamai.com/datastream2/v2/reference/log-format)顯示時間和數值通常以字串送出，不能假設原生 JSON number。S3 目的地[預設交付 gzip](https://techdocs.akamai.com/datastream2/v2/docs/stream-s3-compatible)，收集程序須逐檔串流解壓、限制大小與列長，不保存 token query、Cookie 或 client IP。

收集程序對每個目的地物件記錄 key／ETag／原始 SHA-256／大小及處理時間；解析後在同一交易保存逐筆 edge request 的 stream/version、request ID、UTC 時間、OTA Cloud/Product/Release 路徑、狀態、Range、response bytes、total bytes、cache 狀態及來源檔案／行號／列摘要。重讀相同物件須冪等；同一 key 的內容變動、跨檔重複且內容衝突、無法歸屬的 OTA 路徑和欄位溢位都產生待調查異常，不得以部分成功批次標記 complete。成功與失敗、200 與 206、連線中斷狀態 `0`、Range 重試均保留；成本分析使用 `totalBytes`，不把任何 edge request 轉成客戶 `successful_download_gib`。客戶 meter 仍只由已驗證裝置 `downloaded` receipt 產生。Akamai 文件對 `reqTimeSec` 的單位描述與十位數秒數範例不一致；收集器依欄位名稱與範例解讀為 Unix 秒，遇十三位數值停止並要求現場確認，絕不默默歸到另一個月份。

月底按完整 UTC 月及 Cloud 彙總 edge 請求、response bytes、`totalBytes`、200/206／非成功狀態、Range 與 cache 結果，對照 OTA 下載 receipt、授權 URL 和物件生命週期；跨月完成與重試須人工解釋，不能要求一個下載恰好對應一筆 CDN 請求。S3 列舉與最後一筆時間**不能單獨證明交付完整**：需另保留 DataStream stream/property 啟用、100% sampling、目的地交付健康／告警、最後交付 watermark 及獨立 edge traffic 對照。Akamai [故障說明](https://techdocs.akamai.com/datastream2/docs/troubleshooting)指出目的地連續上傳錯誤可能丟資料；零筆檔案或零筆 edge row 不能自動核准零用量。只有收集資料與外部完整性證據均通過、未解異常為零，授權 reviewer 才可建立 immutable CDN period review。缺口一律讓 producer seal 和 Billing close 保持 incomplete。

固定版的新 `cmd/otacdncollect` 已實作 S3 gzip 讀取、路徑/欄位驗證、原始檔案與逐筆請求原子入庫、重跑去重和來源衝突阻擋；`cmd/otacdnreview` 與寫入 review 的資料層會核對同月 Cloud 的實際 edge request 數與 response bytes，封存前再檢查一次。這些是**程式碼與文件狀態**，不是營運資料已到齊的證明。各環境尚須建立實際 DataStream property、bucket 及唯讀憑證，部署收集排程、驗證 PostgreSQL 整合和外部完整性證據，才能進入商業收費資格審查。

Video Cloud [#725](https://github.com/hkt999rtk/rtk_video_cloud/pull/725) 已合併到選定固定分支，提交 `4ab5b382a21bda2c34650648a6edaae25e180b16`；單元、隔離 PostgreSQL 整合、完整 PR coverage 與三項遠端 CI 均通過。工作區映像產生器加入 collector／review／object 操作命令，dev、staging、prod 均明列 `LKE_OTA_CDN_COLLECTOR_ENABLED=false`；啟用時要求專用唯讀 DataStream Secret、精確 stream/host/path、S3 目的地與 Video Cloud 映像，部署五分鐘一次的 CronJob，不新增 PVC。Akamai [檔名範例](https://techdocs.akamai.com/datastream2/docs/dynamic-time-variables)不保證 `.gz` 副檔名，收集器以 gzip 內容驗證。以上排程與設定目前仍預設關閉，尚未取得實際 edge 日誌或 CDN 完整性證據。

### 2026-09-28 固定版 development 部署證據

Workspace [#595](https://github.com/hkt999rtk/rtk_cloud_workspace/pull/595) 已合併至指定固定分支 `codex/ota-fixed-base-ac44a6d`，merge commit `9644288f43fc5fbdb8caebb4bd0e7721de5035db`。本次 [Go coverage run](https://github.com/hkt999rtk/rtk_cloud_workspace/actions/runs/36410265089) 在 8 分 35 秒內通過所有選定模組、PostgreSQL／EMQX 整合、catalog 與 aggregate/redaction gate。先前一次整合 fixture 的 10 秒 PKI 逾時在單獨重跑後通過；同一次 failed-only 重跑暴露 aggregate 只下載本輪 artifact 的缺陷，#595 已修正為跨 attempt 取回並選同模組最新結果，針對性本機測試與新的完整 CI 均通過。

Development 以固定 Account Manager `c4295b5c` 與 Video Cloud `12bc763c` 來源建置；Account Manager [映像發布](https://github.com/hkt999rtk/rtk_account_manager/actions/runs/36411180798) 成功，migration 094 專用 Job `account-manager-migrate-ota-094-20260928` 完成，API 使用 `sha256:12a720effe3effd7bf8bde2846c983436b3e19884e98fa865b30261fdacaf779`。Video Cloud [映像發布](https://github.com/hkt999rtk/rtk_video_cloud/actions/runs/36411201713) 的 API smoke 與 API 映像推送成功，API 使用 `sha256:f0bf634e376e83d08c2baec4f39335a1be68385e31ef4f7898b8eeebdf6f7667`；整體 workflow 因**另外的** `video-cloud-emqx-pki` GHCR package 推送回 403 而失敗，該映像與相關工作負載未更新。兩個 API Deployment 均為 1/1 Ready，公開 `/v1/health`、`/healthz` 均回 200，且 dev operator 的兩個映像參照已持久化並與 live digest 一致。這些結果證明固定程式版次已部署到 API，**不證明**獨立 OTA service、CDN collector、producer CronJob、Billing outbox 投遞或真實 OTA 用量已啟用；上述開關仍關閉，沒有發佈 OTA 正式價卡。

2026-09-28 後續 CDN 收集器整合由 workspace [#598](https://github.com/hkt999rtk/rtk_cloud_workspace/pull/598) 合併至同一固定分支，merge commit `1288696bb55dbb4754ef27cc20991ab385f8d029`，釘選 Video Cloud `4ab5b382a21bda2c34650648a6edaae25e180b16`。本地完整 PR 門檻通過，workspace-tooling 差異覆蓋率為 48/55（87.27%）；遠端覆蓋率、PostgreSQL／EMQX、catalog、契約與敏感資料掃描均通過。審查發現的首次部署 Secret 檢查順序已修正：先檢查外部 DataStream 讀取憑證，待部署程序建立 `video-cloud-runtime` 後再核對資料庫密碼。

Video Cloud 固定提交的 [CI image-only 發布](https://github.com/hkt999rtk/rtk_video_cloud/actions/runs/36431651968) 成功；development `video-cloud-api` 已更新至 `ghcr.io/hkt999rtk/rtk_video_cloud/video-cloud-api@sha256:d879f8c0d173476e05cd7f206d5e34969eaebe5f62dd6eee340d2a361911153e`，映像 revision 標籤為上述 `4ab5b382`，Pod 1/1 Ready，公開 `/healthz` 回應 200，dev operator 映像設定與 live digest 一致。映像已驗證包含 `otacdncollect`、`otacdnreview` 與 `otaobject`。development PostgreSQL 已在單一交易建立 `ota_cdn_stream_objects`、`ota_cdn_edge_requests`、月份索引及兩個不可變觸發器；兩表目前各為 0 筆。`LKE_OTA_CDN_COLLECTOR_ENABLED=false`，沒有 collector CronJob，不能將程式／空表視為已取得 CDN 營運用量或開始 OTA 收費。外部 DataStream property、日誌目的地、專用唯讀憑證、完整性證據與 staging 對帳仍待完成。

同日固定版 staging PKI 唯讀複核：上述 CI run 的 API、EMQX PKI、OpenBao PKI 三個 `linux/amd64` digest 均通過 staging 專用 GHCR 憑證拉取；三個預定的 10 GiB PVC 都已 Bound，OpenBao 以 staging CA 驗證的 HTTPS 健康查詢回應 200。現行 namespace 只有一般 `video-cloud-api` 與 `pki-controller`，缺少 `video-cloud-api-pki`／`mqtt-pki`、各自的 staging 身分與信任物件，且未設 OpenBao 網路政策要求的 `rtk.cloud/pki-client-access=enabled` namespace 標籤；OpenBao 的 `pki-controller-staging` Kubernetes role 雖綁定正確 service account／namespace／audience，`token_policies` 仍為空，尚無經審核的 PKI 操作權限。Product PKI 檢查也因 Device Root ID／指紋 pin 不完整而 **NO-GO**。即時 Linode 基礎拓樸容量顯示 44 個 active services、此次新增需求 0；操作記錄的上限仍為 20，此結果只適用於不新增資源的既有拓樸，而且該一般部署計畫仍選到舊 Video Cloud API 映像，不能視為固定版上線批准。已在受限操作區保存完整且去敏的 preflight 報告與 staging API 基準；未建立 Root、未套用消費端、未啟用 OTA 計費。

上述是**執行前**快照；同日後續依受保護的階段式 Go/No-Go，固定 CI API 映像的一次性 staging PKI/runtime migration 與權限 Job 均完成，唯讀資料庫查核確認 OTA task、download、artifact、CDN review／edge、producer seal 表和三個 NOLOGIN PKI 群組權限已存在。staging 專屬 Device Root 經一次性 Job 建立於 registry `ready` 狀態；公開憑證自我驗證並與 registry 指紋一致，且存入單張憑證的 immutable ConfigMap。OpenBao 的一次性 bootstrap 與 controller 角色已分別綁定最小 Device-domain 政策、精確 ServiceAccount／namespace／audience，實際短效 workload 登入核對通過；Video Cloud namespace 所需網路標籤已設定。`pki-controller` 已更新至上述固定 CI API digest、裝入同一 Root ID／SHA-256 pin 與新消費端 ID，Deployment 1/1 Ready，Account Manager→certissuer mTLS 複核通過。

後續另建立 staging 專屬 API-PKI serverAuth 管理 CA／憑證及 `video-cloud-api-pki-tls` Secret，憑證僅涵蓋 staging Service DNS；另建立 MQTT-PKI clientAuth 管理 CA／憑證、公開 callback CA ConfigMap、管理 Secret、無環境秘密的 `mqtt-pki-config`，以及僅屬 `rtk_pki_verifier` 群組的 staging 專用資料庫 LOGIN／Secret。金鑰、鏈、用途、SAN、有效期和實際資料庫登入／最小權限已個別核對。又在 staging SecretStore 產生獨立 callback bearer key、EMQX API key／secret、cookie、Dashboard password 和 HTTPS／mTLS authenticator，建立 `mqtt-pki-callback-auth`、`mqtt-pki-runtime`、`mqtt-pki-worker` 三個 Secret；唯讀逐位元組檢查確認 Kubernetes key set 與 staging 來源一致。renderer 已由 [Video Cloud PR #726](https://github.com/hkt999rtk/rtk_video_cloud/pull/726) 合併至選定固定分支 `e0f24f79239120e18d03b9725a13578eb5995048`，覆寫 API-PKI 的 broker key Secret 參照，避免沿用一般 API token；本地固定版 pre-PR 與遠端 CI 均通過。上述資源**尚未被消費端 Pod 掛載**。Root **尚未 active**：`video-cloud-api-pki`／`mqtt-pki` 工作負載和 staging 專屬受控 Service／MQTT 根仍未安裝，兩份真實消費端 ACK 缺失；`--require-product-pki` 仍因 Root 未啟用而失敗。這些 staging schema 與 PKI 基礎變更不會啟動獨立 OTA 程序、CDN 收集、Billing 價卡或客戶計費。

## 2. 現況證據與待補差距

| 項目 | 現況證據 | 必須補齊 |
| --- | --- | --- |
| OTA 價格 | Billing 的 `ProposedOTARates()` 回傳四筆**已核准但未生效**的未稅值，沒有自動建立／啟用價卡；見 [OTA pricing helper](../../repos/rtk_billing/internal/billing/ota.go)。 | 可審核的完整 TWD 價卡、精確四價校驗、稅務決策、發佈紀錄。 |
| 稅務與 Product 適用 | [價卡資料模型](../../repos/rtk_billing/internal/billing/types.go) 與 [遷移](../../repos/rtk_billing/migrations/064_invoice_total_tax_policy.sql) 已支援 `invoice_total` 模式：各 line 先算未稅小計，所有服務合計後計稅一次並確定性分攤至 line；既有 `line` 模式供舊版保留。`tax_rate_basis_points=0` 不能代表 OTA 免稅。[有效價卡選擇](../../repos/rtk_billing/internal/billingstore/pricing.go) 仍只看時間／幣別，尚無逐筆 Product grant 條件。Account Manager 已有歷史 revision 查詢；Video Cloud 的任務、已驗證下載及物件寫入來源紀錄保存原 grant，Billing fact 也會傳送並不可變地保存同組欄位。storage 來源已改為每物件每 UTC 月一筆 fact，保留原授權、物件摘要與精確 byte-microseconds；Billing 保存不可變欄位，對每筆 grant 查詢 Account Manager 歷史資料，並按 Product 重算儲存總量。 | 新 OTA 價卡須固定已決定的帳單層 `invoice_total`／台灣營業稅 5%（500 basis points）／`half_up` 與完整價卡審核紀錄；所有選價／估算／關帳／客戶 API 需使用同一稅務規則。四項 meter 的歷史授權與來源證據仍須在 staging 跨服務驗收；每物件儲存 fact 的不可變證據、唯一性和 Product 總量核對已有本機測試。缺失或衝突一律不自動收費。關閉後的舊任務／儲存沿用其原授權。正式電子發票處理暫不在本次範圍。 |
| 版次與切月 | [pricing store](../../repos/rtk_billing/internal/billingstore/pricing.go) 可排程未來 UTC 月初版次，發佈時將舊版標為 retired；invoice 仍依有效區間的期間起點選版，月結共用交易鎖。OTA 另有要求完整價卡 digest、雙人審核與固定稅規則的受控發佈路徑，尚無已發佈 OTA 版次。 | 在目標環境以已合併的原子建卡工具保存審核證據，盤點完整月份與 ownership，取得正式生效月及發佈紀錄；既有一般 activation API 不能用來發佈 OTA。 |
| 月份與移轉 | OTA 的 storage fact／兩份 period seal 要求完整 UTC 月；[invoice close](../../repos/rtk_billing/internal/billingstore/invoices.go) 在 OTA 有價時已拒絕非 UTC 完整月、缺少整月現任 owner 責任證明與過期 ownership profile，保留 incomplete 原因。[current usage API](../../repos/rtk_billing/internal/api/billing.go) 在 OTA 有價時改用 UTC 月，對 owner 裁切或非完整 UTC 期間排除 OTA 估算並回報 `held_for_review`；非 OTA 既有期間不變。[cutover audit](../../repos/rtk_billing/cmd/ota-cutover-audit/main.go) 只讀取單一一致快照，列出舊時區邊界與 UTC 邊界間的潛在空檔／重疊，以及已存在的 fact、帳期、發票與 owner 證明。 | 在目標環境執行切月盤點並保存證據；明確區分「完整 UTC 月計量證明」與「現任 owner 可見／應付的期間」；完成歷史月份切換、月中移轉及關閉 Cloud 人工審核流程，不得錯收。 |
| 生效前 OTA 事實 | immutable receipt/outbox 可先進 Billing；[invoice builder](../../repos/rtk_billing/internal/billing/invoice.go) 與用量估算在該月價卡沒有 OTA 費率時排除 OTA 計費、保留原始事實，且非 OTA 缺價或部分 OTA 價卡仍 fail closed；目前即時 activation API 不允許 OTA 價卡。[OTA 來源 outbox](../../repos/rtk_video_cloud/internal/postgres/usage_outbox.go) 已在已關帳拒收時保存 `INVOICE_IMMUTABLE`、原始 payload／digest 與嘗試紀錄。 | 結合未來 UTC 月排程後驗證不追收；在 staging 演練晚到事實跨關帳、來源／Billing 高水位與人工調整流程，不能假設 Billing 可以接受遲到輸入。 |
| 前端揭露 | [ServicePricing.jsx](../../repos/rtk_cloud_admin/web/src/ServicePricing.jsx) 只在授權後呼叫 Cloud Admin 的研究價與正式價端點；數值不進匿名 JavaScript／翻譯包。15 項最高候選參考價、四項 OTA 核准待生效價、當期及預告正式價分區顯示；[Billing 頁](../../repos/rtk_cloud_admin/web/src/main.jsx) 對暫緩 OTA 估算明示不含 OTA 的小計與原因。此程式已部署 development，授權 API 已通過唯讀 smoke；已開立 invoice 的逐項金額與 Product ID 已部署 development。受保護的 Product 列表提供當前 OTA 選用狀態，Cloud Admin main 與 development 已更新；未登入的 Product 與研究價 API 均回應 401。invoice 明細到價格頁的連結與快照提醒已合併 main 並部署 development；實際登入瀏覽器驗收尚未執行。 | tenant-safe current/upcoming **正式**價卡 API 已於 development 通過授權唯讀查詢，仍需有實際價卡的跨服務驗收；正式 invoice 可逐項顯示用量、單價、稅額及 Product ID；連結與快照提醒已有桌面／手機本機 E2E，仍需 development 登入驗收。研究價端點不能充當實際費率。 |
| 正式環境 | [TWD 進度](../billing-twd-currency-progress.md) 證明先前 staging 的 MQTT 價卡與 invoice，**不證明 production 或 OTA 已收費**。 | 逐環境查核實際 active 版次、直連物件交付與兩份 seal、正式發佈與第一張發票對帳。 |

本計畫沿用 [正式 Pricing and Invoicing contract](../../repos/rtk_cloud_contracts_doc/pricing_and_invoicing.md) 的 immutable version、整數金額、按 invoice line 彙總後取整及歷史發票不變性；新增 OTA 的帳單稅規則需先更新該契約：line 先取整未稅小計、所有服務小計加總後對**帳單總額計稅一次**，明確記錄計稅模式與稅率，並以確定性分攤維持 line／invoice 總額一致。OTA 的計量、直連物件交付、Product gate 和完整性規則以 [OTA contract](../../repos/rtk_cloud_contracts_doc/ota_delivery_and_billing.md) 為準。

## 3. 交付順序與實作方式

| 階段／owner | 實作 | 驗收證據 |
| --- | --- | --- |
| D0 文件／Contracts、Billing、Cloud Admin | 在 OTA contract 將四價標為「已核准未稅單價，尚未生效」，在 pricing contract 記錄 UTC 月初發佈及舊事實不追收；本文件維護跨 repo 順序。把登入後揭露、評估帳戶與 private quote 邊界寫進 business model。新增 Billing 操作 runbook 與 Cloud Admin customer-copy 規格。 | 文件無互相矛盾的「proposal／active」文字；links、docs-check、contracts-check 通過。 |
| P1 價卡資料／Billing | 保留既有 rate `quantity_scale`／`tax_category` 及舊版計稅模式；新 OTA 完整價卡明示 Product OTA 授權適用規則、台灣營業稅 5% 的帳單總額計稅模式、不可變 base version／manifest digest／審核證據。建立四價 manifest（service/metric/unit、`96@scale3`、`96@scale2`、`96@scale2`、`144@scale6`、TWD、round-half-up、Product gate／歷史 grant、核准日期／帳單稅務政策）。在目標環境的唯讀一致快照中複製當期版次**全部非 OTA 費率**，只加入 OTA 四筆並輸出 deterministic diff/digest；建 immutable draft 時同交易重驗 base／Product scope／tax／差異，拒絕 stale 或只含 OTA 的卡。此階段不啟用費率。 | 缺 5% 帳單稅務設定／帳戶資格／Product grant、重複／漏價、舊費率被改、base 漂移、已有預定版次皆拒絕；manifest digest、舊／新全價卡 diff、審核人、環境、版本與唯讀快照可重現；OTA 未選用 Product、關閉後新工作、關閉前工作完成與儲存未刪除皆有 fail-closed／歷史授權測試。 |
| P2 UTC 生效／Billing | 把 publication 和「此刻適用」分開：允許預先發佈**未來完整 UTC 月第一天 00:00:00Z**的版本；交易中截斷前版 `effective_until`，新版自該時間適用。對客戶輸出的 current/upcoming 以有效區間算，不直接把資料庫 `active/retired` 欄位當顯示狀態。跨 currency 範圍防止重疊／缺口，保護已開立發票；價卡發佈和 invoice close 共用序列化點。保留既有非月初歷史版次與發票，不重寫。 | PostgreSQL 交易測試：切月前舊價、當刻新價、後續月份新價；併發發佈／月結、重複發佈、重疊、空檔、已開票期間皆安全。 |
| P3 月份與事實／Billing、Account Manager、OTA producer | 定義新收費期間的 UTC month 契約，修正 usage 預覽與 invoice close；OTA close 僅接受完整 UTC 月、兩份 seal、outbox high-water 與物件盤點對帳。直連物件模式不要求 CDN 日誌；若未來改 CDN，同月不得混用模式且需獨立 edge review。owner 月中移轉時，完整月 seal 仍證明來源總量；可見性和責任只使用 owner 授權期間。**在可驗證的分攤規則完成之前，月中移轉所涉及的 OTA 月份不自動開立 OTA 費用**，人工審核且不跨 owner 洩露資料。舊時區月份需有一次性的切換／截斷方案，不能產生漏算或重複區間。 | UTC／Asia-Taipei 邊界、月中 Cloud 轉移／關閉、零用量 seal、儲存整月、晚到回報、來源缺口、其他服務同張發票測試；不合格 close 保持 incomplete。 |
| P4 不追收／Billing | 在 invoice input 邊界識別 OTA 正式生效月之前的 fact，保留已接受的 immutable fact 與全部來源 receipt，但從所有 draft/rebuild/close/usage estimated charge 排除；不得為此刪 fact 或重寫歷史。訂定關帳前的 outbox high-water／遲到證據規則：既有 Billing close barrier 拒絕已關帳月份的遲到 fact 時，來源 ledger 仍保存未送達 payload 與拒收原因，不能默默丟棄或轉到新月份。其他服務缺對應費率仍拒絕開票。 | 生效前 OTA + 已收費 MQTT 的混合月可正常開 MQTT 發票、OTA 金額 0 且留稽核；生效後四項按 Product×meter 開列；重跑不改舊單；晚到 fact 可由來源 ledger 對帳。 |
| A1 客戶價格 API／Billing、Cloud Admin | Billing 提供登入租戶可讀的 current/upcoming TWD price book：`version_id`、有效 UTC 區間、currency、tax policy、rate identity、unit/scale/rounding、整月 commercial 證據／結算時 active Billing 帳戶狀態；Cloud Admin BFF 依 Brand Cloud 授權代理。公開網站不調此 API。若 Billing 查詢失敗，畫面顯示「目前無法確認適用費率」並禁止用靜態參考價替代。 | 權限隔離、錯誤回應、環境不同版次、跨月快取失效、未登入拒絕、無適用價與未生效版次 API 測試。 |
| A2 詳細前端／Cloud Admin | 將「Billing > Service Pricing」改成正式生效、已核准待生效、參考價／尚待核准三種明確區塊；顯示第 4 節完整表格與計算說明。核准與參考數字由受 Cloud Billing 權限保護的端點提供，不放進匿名可下載的前端資產；Product 選服務與 OTA disabled 畫面連到對應費率；invoice/usage 明細顯示實際 quantity、unit、rate、未稅／稅／總額、pricing version、UTC 期間。持續禁止已移除的固定 NT$232 範例，未來估算器只用當期有效價卡。 | 繁中／英文、窄螢幕表格、鍵盤／螢幕閱讀器、匿名資產與 API 無數字／未授權拒絕、無價／API outage／切月、Product 切換與 invoice drilldown E2E。 |
| Q1 staging 資格／各 owner | 在隔離資料與固定版次下實測 Product grant、OTA 註冊、私有物件簽名 URL 直接下載與 Range、四 receipt/outbox、雙 seal、月結、帳單及價格頁；核對 Billing DB 當期／下期版次。 | 完整且可追溯的 staging 報告；發現物件盤點／period seal 差異就不發佈。 |
| R1 production 發佈／Finance、營運 | 核對已決定的 5% 帳單稅務／commercial 加 active 帳戶適用規則並取得 staging 簽核後，選**下一個尚未開始的完整 UTC 月**為生效月，預先發佈審核過的完整版本；在邊界前後核對 API/UI、第一月對帳、舊客帳單與異常回退程序。 | 發佈紀錄、客戶告知及正式環境版次、第一張 OTA invoice 的四項來源與金額對帳。不得回填已過月份。 |

此表是實作順序與驗收條件，**不是已完成清單**。staging／production 的操作依 [Billing staging qualification](../billing-staging-qualification.md) 與部署治理另外執行。

## 4. 登入後價格頁的資訊架構與每項服務說明

頁首先選定 Brand Cloud／Product，列出「本帳戶 commercial／evaluation 層級與 Billing 帳戶狀態」「正式計費幣別 TWD」「目前有效價卡版本及 UTC 生效區間」「稅務說明」「下一版與生效日」。價格列不得只靠 Product checkbox 推導：Product 啟用控制功能可用性；帳務還要看整月 commercial 證據、active Billing 帳戶、有效價卡及實際用量。Evaluation 顯示免費條款；Private Cloud 的授權／維護費以合約報價，與下表 managed-cloud 用量費分開。

主表固定欄位：**服務／收費項目、何時記一筆、最高參考價、已核准價或當期有效價、計算公式與排除情況、此 Product 是否啟用、狀態與生效日**。可展開看實際量的來源、失敗與重試處理、稅及四捨五入；篩選和行動版不能隱藏「尚未生效」標籤。正式金額只取 Billing API 的有效版次；下列研究快照只可用醒目「參考價，非帳單依據」標籤顯示。OTA 四項已核准單價保持不變，最高外部參考價另外列出。下表「待核定」是指 staging／production 的正式價格；dev 非 OTA 版次已發佈為 2026-10-01 起的正式測試價，仍須依時間與實際來源 fact 才會形成帳款。

| 服務／項目 | 最高參考價；OTA 另列核准價（未稅） | 使用者要看到的計量與不計費規則 | 現階段揭露狀態 |
| --- | --- | --- | --- |
| MQTT publish | 參考 **NT$48／百萬則** | broker 接受的 publish 各計一次；不另收 MQTT payload 頻寬；連線與 keepalive 不另計。 | 依當期 Billing 版次判定；既有 staging 有正式 MQTT 版次，價格不因參考價更新而改變。 |
| MQTT delivery | 參考 **NT$48／百萬次** | 每個 subscriber 的實際 delivery 各計一次；一則 publish 投遞五個訂閱者是 1 publish + 5 deliveries。 | 依當期 Billing 版次判定。 |
| Device Shadow | **AWS 1 KB 操作參考 NT$60／百萬單位**；RTK 1 KiB 正式單價待核定 | RTK 擬按成功讀取／更新等的記錄或回應大小向上取 1 KiB 單位；若走 MQTT，其訊息依 MQTT 規則另計。AWS 的 1 KB 與 RTK 的 1 KiB 不能視為同一計量單位。 | 計量與費率尚待核定。 |
| WebRTC TURN relay | 傳輸部分參考 **NT$4.80／GiB**；TURN 分鐘費另列不可換算 | 雲端 relay 實際送到每位觀看者的 bytes；直接 P2P 媒體不產生 TURN relay 費；信令本身無獨立 channel 費，MQTT 訊息另依 MQTT 計。 | 計量與費率尚待核定；NT$4.80 不是完整 TURN 成本。 |
| Clip／snapshot storage | 參考 **NT$1.30／GiB-month** | 影片物件實體 bytes 乘儲存時間；OTA 韌體與 logs 不混入。 | 計量與費率尚待核定。 |
| Clip object write | 參考 **NT$224／百萬次** | 成功建立 clip／snapshot 物件才計；OTA 寫入另表，失敗寫入不計。 | 計量與費率尚待核定。 |
| Clip object read | 參考 **NT$17.92／百萬次** | 成功的影片 GET／HEAD 次數；OTA origin read、CDN request 不當作客戶影片讀取。 | 計量與費率尚待核定。 |
| Clip video download | 參考 **NT$4.80／GiB** | 送往 app 的實際影片 bytes（含已送出的重試 bytes）；OTA 下載另表。 | 計量與費率尚待核定。 |
| OTA first device assignment | **已核准 NT$96／1,000 次**；最高參考 **NT$144／1,000 次** | campaign 對該裝置第一次持久化指派計一次；通知／poll 重試不重計。 | 核准價待正式生效，最高參考價不替換核准價。 |
| OTA verified download | **已核准 NT$0.96／GiB**；CDN 參考 **NT$3.84／GiB** | 同一 deployment／精確 artifact 第一次經身分驗證的 `downloaded` 回報，以 artifact 大小計；URL 發放、失敗、Range 重試、原始 CDN egress 不計。 | 核准價待正式生效；CDN 實際出口與驗證下載不是同一計量。 |
| OTA physical artifact storage | **已核准 NT$0.96／GiB-month**；最高參考 **NT$1.30／GiB-month** | 韌體物件實際 bytes×UTC 儲存時間，至物理刪除；revoke／disable 不等於刪除。 | 核准價待正式生效。 |
| OTA artifact write | **已核准 NT$144／百萬次**；最高參考 **NT$224／百萬次** | 物件 key/version 的成功持久建立計一次；失敗 PUT 或同物件重試不計；GET 不另收客戶費。 | 核准價待正式生效。 |
| Device／app log ingest | 參考 **NT$28.80／GiB** | 已接受的未壓縮 log bytes（含 metadata）；走 MQTT 的傳輸訊息另依 MQTT 計。 | 有用量資料；計費整合尚待核定。 |
| Log retention | 壓縮封存成本參考 **NT$1.31／GiB-month** | 現行草案以接收 bytes×設定保留天數／30 估算；上線前要明定與實際保存量的差異。 | 有用量資料；計費整合尚待核定；參考基礎不同。 |
| Other data APIs | REST proxy 參考 **NT$136／百萬次** | 成功的其他資料 API 呼叫；排除 Shadow、OTA control、物件操作等已列項目；登入與控制台管理不另計。 | 路由分類與計量尚待核定。 |

每列分欄顯示**當期有效價、已核准待生效價、最高研究參考價**；沒有資料的欄位寫「未設定」，不以另一欄代填。帳單一律只用當期有效價。即使參考價可見，也要寫明「本價格尚未生效，不會依此金額計入帳單」；無有效費率時不得顯示為 NT$0 或宣稱永久免費。對已選用但無價的服務顯示「服務可用；目前尚無適用用量費率，實際費用依合約與生效價卡」，並提供帳單／客服入口。

頁面底部用一個具體範例解釋：`同一 Product 同一項目當月數量 × 單價 → line 未稅小計；所有 Product 與服務 line 未稅小計加總 → 帳單未稅總額；按帳單核准稅率對總額計稅一次；未稅總額＋稅＝應付總額`。line 小計按 NT$1 取整，稅額也只在帳單層取整一次，再確定性分攤到明細以供查核；OTA 沒有另加一筆稅。千次／百萬次只是展示分母，**不是最低計費級距**。GiB = 1,073,741,824 bytes；GiB-month 按該 UTC 月實際時間積分。帳單期間用 UTC 正式標示，旁邊可加使用者本地時間換算。估算不等於發票，正式發票要附數量、版本與來源參照；付款與餘額狀態依既有 Billing 頁面。

## 5. 參考價來源、限制與文件歸屬

最高參考價採 2026-09-26 已查核的 **AWS 現行 Price List（美東、愛爾蘭、東京、聖保羅四區）**、CloudFront global pay-as-you-go 表內**八個非中國交付區域**、Cloudflare R2／Akamai 同類標準儲存，以及舊 RTK 草案中**可按相同或近似計量維度比較**的最高值。中國 CloudFront 另有人民幣價目，未納入本次美元候選集。這是明確候選集的最大值，**不是全球所有供應商與區域的最高價**；計量基礎不同的值明列為成本代理。固定規劃換算為 US$1＝NT$32，不用即時匯率；台幣展示價向上取至 NT$0.01。AWS S3 [明定計費 GB 為 GiB](https://aws.amazon.com/s3/pricing/)，[CloudFront 亦以 GiB 計量](https://aws.amazon.com/pt/blogs/aws-brasil/ensaios-sobre-transferencia-de-dados-na-aws-parte-3/)；[CloudWatch 現行價目範例](https://aws.amazon.com/cloudwatch/pricing/)將 KB 除以 1,024² 得到 GB、TB 乘 1,024 得到 GB，但其**壓縮封存量**與 RTK 原始接收量不同。

| 對應服務 | 候選集中最高官方基準與來源 | 比較限制 |
| --- | --- | --- |
| MQTT、Shadow | [AWS IoT Core São Paulo 現行價目](https://pricing.us-east-1.amazonaws.com/offers/v1.0/aws/AWSIoT/current/sa-east-1/index.csv)：訊息 US$1.50／百萬、Shadow US$1.875／百萬個 AWS 1 KB 單位，折 NT$48／NT$60。 | AWS MQTT 按 5 KB 單位，RTK 按原始訊息數；[AWS Shadow 按 1 KB 向上計](https://aws.amazon.com/iot-core/pricing/)，不能直接當作 RTK 1 KiB 單位售價。 |
| OTA 指派 | [AWS IoT Device Management São Paulo 現行價目](https://pricing.us-east-1.amazonaws.com/offers/v1.0/aws/IoTDeviceManagement/current/sa-east-1/index.csv)：Jobs US$0.0045／remote action，折 NT$144／千次。 | AWS 動作與 RTK 第一次持久化 assignment 不完全相同；OTA **核准 NT$96／千次不變**。 |
| TURN relay、影片直接外送 | [AWS Data Transfer São Paulo 現行價目](https://pricing.us-east-1.amazonaws.com/offers/v1.0/aws/AWSDataTransfer/current/sa-east-1/index.csv)：第一級對 Internet US$0.150／GB；[AWS 帳務文件以 1 TB＝1,024 GB 計資料傳輸](https://docs.aws.amazon.com/awsaccountbilling/latest/aboutv2/useconsolidatedbilling-effective.html)，折 NT$4.80／GiB。 | [AWS Kinesis Video Streams](https://aws.amazon.com/kinesis/video-streams/pricing/) 的 TURN US$0.12／千分鐘另加外送，分鐘缺位元率／觀看者數，不能加進每 GiB 參考價。 |
| Clip／韌體物件儲存與讀寫 | [AWS S3 Standard São Paulo 現行價目](https://pricing.us-east-1.amazonaws.com/offers/v1.0/aws/AmazonS3/current/sa-east-1/index.csv)：儲存 US$0.0405／GiB-month、PUT US$0.007／千次、GET US$0.0056／萬次，折 NT$1.30／GiB-month、NT$224／百萬次寫入、NT$17.92／百萬次讀取。 | S3 原始請求不必然等於 RTK 成功的業務物件操作；OTA 核准儲存／寫入價不變。 |
| OTA CDN 下載 | [CloudFront 亞洲現行價目](https://aws.amazon.com/cloudfront/pricing/pay-as-you-go/)：免費額後第一級 US$0.120／GiB，折 NT$3.84／GiB。 | CDN 原始出口 bytes 包含 Range／重試；RTK 只算首次驗證的 artifact 大小，**核准 NT$0.96／GiB 不變**。 |
| Logs | [CloudWatch São Paulo 現行價目](https://pricing.us-east-1.amazonaws.com/offers/v1.0/aws/AmazonCloudWatch/current/sa-east-1/index.csv)：Standard ingest US$0.90／GB、archive US$0.0408／壓縮後 GB-month，研究換算 NT$28.80／GiB、NT$1.31／GiB-month。 | RTK retention 草案用原始接收 bytes×設定天數，不是壓縮後實際保存量；後者僅為成本代理值。 |
| 其他資料 API | [API Gateway REST São Paulo 現行價目](https://pricing.us-east-1.amazonaws.com/offers/v1.0/aws/AmazonApiGateway/current/sa-east-1/index.csv)：US$4.25／百萬次，折 NT$136／百萬次。 | AWS REST 收到的請求與 RTK 成功的「其他資料 API」不同，須完成路由分類。 |

Cloudflare R2 的 Infrequent Access、不同維度的 TURN 分鐘、平價包套、免費額、尚未正式收取的未來項目及歷史舊價格不參與最高價排名；它們仍記在研究候選表並註明排除原因。研究快照本身不能直接充當 Billing 價卡；dev 已完成非 OTA 完整候選、摘要審核與未來生效版次發佈，其他環境仍須各自審核。

**成本關卡：**核准的 OTA 下載售價 NT$0.96／GiB；Finance 在正式啟用直連物件交付前，須以實際儲存供應商合約、物件讀取與出口流量、Range／重試、交付地區、匯率及稅計算成本與毛利並留下簽核記錄。AWS CloudFront 出口價只供未來 CDN 擴充評估，不作為首版啟用門檻。使用者價格頁不展示供應商成本或暗示本服務轉售 AWS。

15 項舊價、官方候選值、選出的最高參考價與排除原因記在 [service-pricing-research.md](../../repos/rtk_cloud_admin/docs/service-pricing-research.md)；非 OTA 的 dev 正式測試數字已核准，其他環境的正式價格仍須各自核定。

| 文件 | 放置內容及維護者 |
| --- | --- |
| 本文件 `docs/design/ota-pricing-activation-and-disclosure-plan.md` | 跨 repo 決策、順序、缺口與驗收；workspace 維護。 |
| `repos/rtk_cloud_contracts_doc/ota_delivery_and_billing.md` 與 `pricing_and_invoicing.md` | 規範性計量／價卡／不追收／月份契約；Billing 與 OTA owners 維護。 |
| [Billing 操作 runbook](../../repos/rtk_billing/docs/pricing-activation-runbook.md) | 環境盤點、完整價卡 diff、tax/approver、UTC 排程、回復與首單對帳；Billing／Finance 維護。公開文件不重列價格數字。 |
| [Cloud Admin 揭露規格](../../repos/rtk_cloud_admin/docs/service-pricing-disclosure.md) | 使用者文案、15 項清單、狀態詞、範例、i18n／無障礙驗收；Cloud Admin 維護。公開文件不重列價格數字。 |
| `repos/rtk_cloud_admin/docs/service-pricing-research.md` | AWS 等官方 benchmark 的查核日期與非等價說明；**非正式費率來源**。 |
| `docs/business-model.md` | 公開官網與登入後揭露界線、evaluation／managed cloud／private quote 適用關係；workspace 商務 owner 維護。 |

## 6. 尚待完成的生效條件

1. 商務規則已定：付費 Managed Cloud 中 Product 選用 OTA 才適用四價；關閉後原授權任務計至完成、儲存計至實體刪除；OTA 不單獨計稅，所有服務合計後在帳單層計稅一次。帳單總額按台灣營業稅 5% 一次計稅，正式電子發票處理暫不實作；仍須完成帳戶資格及歷史 Product grant 查核的跨服務 staging 驗收；`tax_rate_basis_points=0` **不得宣稱 OTA 免稅**。
2. dev 已核准並排程 11 項非 OTA 正式測試價；production 仍須盤點當期完整 TWD 價卡與所有合約特例，再另行核准是否採用這些數字。OTA 四項維持原核准價，尚未發佈 OTA 價卡。
3. UTC 銜接程式已合併固定基線；仍須在目標環境以正式價卡、帳戶與 owner 證據做唯讀預覽及首期關帳驗收。月中 owner 移轉／Cloud closure 維持人工核對，未通過對帳時 OTA 該月不自動收費。
4. 完成直連物件 URL、四項來源事實、物件盤點與雙 seal 的 staging 資格，選定第一個可用的**未來**完整 UTC 月及客戶告知時點，才可發佈 production 價卡；CDN 留待後續擴充。
5. 釐清「登入後才可看具體價格」是否也涵蓋公開 GitHub 原始碼與文件。目前 Cloud Admin、Billing 和 workspace 儲存庫公開，既有原始碼、研究文件及歷史提交含價格數字；這次保護的是應用程式匿名資產和 API，新增的兩份操作／文案文件不重列數字。若要求原始碼層級保密，必須另定私有價目來源、儲存庫可見性及既有公開歷史的處理方式，不能把 UI 授權視為完成該要求。

「參考價」與「已核准待生效」本身皆不構成實際收費；dev 非 OTA 版次須到 2026-10-01 00:00 UTC 才能用於當期帳單。OTA 在上述專屬來源與發佈條件完成前不會計費。
