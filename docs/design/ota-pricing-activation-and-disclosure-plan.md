# OTA 費率生效與服務價格揭露：實作計畫

Status: implementation plan with approved-rate documentation and an authenticated research-price disclosure built; no effective rate-card publication or environment rollout performed.

Owner: rtk_cloud_workspace (cross-repository sequencing). Last reviewed: 2026-09-26.

## 1. 已確定的決策與範圍

- 使用者於 2026-09-26 核准 **OTA 四項未稅單價**：首次裝置指派 NT$96／1,000 次、已驗證下載 NT$0.96／GiB、實體韌體儲存 NT$0.96／GiB-month、成功建立韌體物件 NT$144／百萬次。這是商業單價的核准，**尚非 Billing 資料庫中的生效價卡或部署授權**。稅別、適用客群及正式生效月仍待核定。
- 詳細價格只在**登入後的 Cloud Admin「Billing > Service Pricing」**揭露；公開官網依 [business-model.md](../business-model.md) 只說明 evaluation 免費、private commercial 的授權與維護費需報價，不刊登具體數字。用量費率與 private deployment 的一次性授權／年度維護費必須分開說明。
- 核准價與參考價的數字也必須受 Cloud Billing 權限保護：匿名可下載的 HTML、JavaScript 或翻譯資源不可夾帶完整價表；畫面取得授權後才呼叫受保護的資料端點，失敗時不顯示數值。
- OTA 仍是可註冊、可被 Product 選用的獨立服務。只有 Product 啟用 OTA 才顯示其 dashboard；未啟用時明示「此產品尚未啟用 OTA 服務」。Product 選用服務、費率生效、實際產生用量，是三件不同的事。
- 其他服務目前畫面中的金額屬**參考價／研究草案**。不得把研究價直接當作已生效價，也不得因計量器存在就宣稱正在收費。任何 Cloud／環境的正式費率只由 Billing 的有效 pricing version 決定。

### 執行狀態（2026-09-26）

| 階段 | 目前狀態 |
| --- | --- |
| D0 文件與費率研究（部分完成） | OTA 四項核准價及未生效界線已寫入契約；本文件與研究表列出最高候選參考價、來源、非等價情況及交付順序。Billing 操作 runbook 與 Cloud Admin customer-copy 規格仍待新增。 |
| A2 登入後揭露的過渡版（部分完成） | Cloud Admin 僅於 Cloud owner 通過 `billing_account.read` 授權後，從不快取的 `/billing/pricing-references` 端點取得 15 項參考價與四項 OTA 核准待生效價；匿名前端資產不含數字，取價失敗不顯示價表。此端點是研究快照，仍**不讀取**該 Cloud 的當期 Billing 價卡；正式價、Product 適用狀態與 invoice 明細整合仍待 A1／A2 後續。 |
| P1–P4、A1 正式計費與價格 API | 尚未實作；沒有新增或啟用 OTA pricing version，沒有因本文件改變任何帳單。 |
| Q1、R1 環境資格與正式發佈 | 尚未執行；須通過稅務、適用客群、UTC 月份、CDN 成本／完整性及 staging 對帳關卡。 |

## 2. 現況證據與待補差距

| 項目 | 現況證據 | 必須補齊 |
| --- | --- | --- |
| OTA 價格 | Billing 的 `ProposedOTARates()` 只回傳四筆規劃值，沒有自動建立／啟用價卡；見 [OTA pricing helper](../../repos/rtk_billing/internal/billing/ota.go)。 | 可審核的完整 TWD 價卡、精確四價校驗、稅務決策、發佈紀錄。 |
| 版次與切月 | [pricing store](../../repos/rtk_billing/internal/billingstore/pricing.go) 拒絕未來生效日，發佈時立即將舊版標為 retired；invoice 依期間起點選版。 | 可在 UTC 月初排程生效、無重疊／缺口、前月不被改價、發佈與月結同步鎖定。 |
| 月份與移轉 | OTA 的 storage fact／兩份 period seal 要求完整 UTC 月；[current usage API](../../repos/rtk_billing/internal/api/billing.go) 用 Cloud 時區切月，所有權移轉又可能把起點往後裁切。 | 明確區分「完整 UTC 月計量證明」與「現任 owner 可見／應付的期間」；跨月、月中移轉及關閉 Cloud 均不得錯收。 |
| 生效前 OTA 事實 | immutable receipt/outbox 可先進 Billing；[invoice builder](../../repos/rtk_billing/internal/billing/invoice.go) 遇到缺費率的事實會中止整張發票。 | 生效前 OTA 事實保留稽核但一次性排除計費，其他缺價仍 fail closed；不得日後補收。已關帳後才送到的舊事實必須由 OTA 來源 ledger 保全，不能假設 Billing 可以接受遲到輸入。 |
| 前端揭露 | [ServicePricing.jsx](../../repos/rtk_cloud_admin/web/src/ServicePricing.jsx) 只在授權後呼叫 Cloud Admin [研究價端點](../../repos/rtk_cloud_admin/internal/app/service_pricing.go)，數值存於 Go 內嵌 [研究價快照](../../repos/rtk_cloud_admin/internal/app/service-pricing-reference.json)，不進匿名 JavaScript／翻譯包。15 項最高候選參考價與四項 OTA 核准待生效價分欄顯示；仍無 Cloud 當期 Billing 價卡查詢路由。 | tenant-safe 的 current/upcoming **正式**價卡 API、Product 適用狀態、當期費率／稅／合約、正式 invoice 明細連結。研究價端點不能充當實際費率。 |
| 正式環境 | [TWD 進度](../billing-twd-currency-progress.md) 證明先前 staging 的 MQTT 價卡與 invoice，**不證明 production 或 OTA 已收費**。 | 逐環境查核實際 active 版次、CDN 與兩份 seal、正式發佈與第一張發票對帳。 |

本計畫沿用 [正式 Pricing and Invoicing contract](../../repos/rtk_cloud_contracts_doc/pricing_and_invoicing.md) 的 immutable version、整數金額、按 invoice line 彙總後取整及歷史發票不變性；OTA 的計量、CDN、Product gate 和完整性規則以 [OTA contract](../../repos/rtk_cloud_contracts_doc/ota_delivery_and_billing.md) 為準。

## 3. 交付順序與實作方式

| 階段／owner | 實作 | 驗收證據 |
| --- | --- | --- |
| D0 文件／Contracts、Billing、Cloud Admin | 在 OTA contract 將四價標為「已核准未稅單價，尚未生效」，在 pricing contract 記錄 UTC 月初發佈及舊事實不追收；本文件維護跨 repo 順序。把登入後揭露、評估帳戶與 private quote 邊界寫進 business model。新增 Billing 操作 runbook 與 Cloud Admin customer-copy 規格。 | 文件無互相矛盾的「proposal／active」文字；links、docs-check、contracts-check 通過。 |
| P1 價卡資料／Billing | 建立經審核的四價 manifest（service/metric/unit、`96@scale3`、`96@scale2`、`96@scale2`、`144@scale6`、TWD、round-half-up、核准日期／稅別／適用範圍）。從目標環境當期版次複製**全部非 OTA 費率**，再加入 OTA 四筆，檢查唯一性、數量、單位、金額、tax 與前後差異；產生 immutable draft。不得用只有 OTA 的版本覆蓋原有 MQTT 等費率。 | manifest digest、舊／新價卡 diff、審核人、環境及版本 ID 可重現；錯價、漏價、重複 metric、未定稅別一律拒絕。 |
| P2 UTC 生效／Billing | 把 publication 和「此刻適用」分開：允許預先發佈**未來完整 UTC 月第一天 00:00:00Z**的版本；交易中截斷前版 `effective_until`，新版自該時間適用。對客戶輸出的 current/upcoming 以有效區間算，不直接把資料庫 `active/retired` 欄位當顯示狀態。跨 currency 範圍防止重疊／缺口，保護已開立發票；價卡發佈和 invoice close 共用序列化點。保留既有非月初歷史版次與發票，不重寫。 | PostgreSQL 交易測試：切月前舊價、當刻新價、後續月份新價；併發發佈／月結、重複發佈、重疊、空檔、已開票期間皆安全。 |
| P3 月份與事實／Billing、Account Manager、OTA producer | 定義新收費期間的 UTC month 契約，修正 usage 預覽與 invoice close；OTA close 僅接受完整 UTC 月，兩份 seal、outbox high-water、CDN 異常對帳都齊全。owner 月中移轉時，完整月 seal 仍證明來源總量；可見性和責任只使用 owner 授權期間。**在可驗證的分攤規則完成之前，月中移轉所涉及的 OTA 月份不自動開立 OTA 費用**，人工審核且不跨 owner 洩露資料。舊時區月份需有一次性的切換／截斷方案，不能產生漏算或重複區間。 | UTC／Asia-Taipei 邊界、月中 Cloud 轉移／關閉、零用量 seal、儲存整月、晚到回報、來源缺口、其他服務同張發票測試；不合格 close 保持 incomplete。 |
| P4 不追收／Billing | 在 invoice input 邊界識別 OTA 正式生效月之前的 fact，保留已接受的 immutable fact 與全部來源 receipt，但從所有 draft/rebuild/close/usage estimated charge 排除；不得為此刪 fact 或重寫歷史。訂定關帳前的 outbox high-water／遲到證據規則：既有 Billing close barrier 拒絕已關帳月份的遲到 fact 時，來源 ledger 仍保存未送達 payload 與拒收原因，不能默默丟棄或轉到新月份。其他服務缺對應費率仍拒絕開票。 | 生效前 OTA + 已收費 MQTT 的混合月可正常開 MQTT 發票、OTA 金額 0 且留稽核；生效後四項按 Product×meter 開列；重跑不改舊單；晚到 fact 可由來源 ledger 對帳。 |
| A1 客戶價格 API／Billing、Cloud Admin | Billing 提供登入租戶可讀的 current/upcoming TWD price book：`version_id`、有效 UTC 區間、currency、tax policy、rate identity、unit/scale/rounding、適用 tier／合約狀態；Cloud Admin BFF 依 Brand Cloud 授權代理。公開網站不調此 API。若 Billing 查詢失敗，畫面顯示「目前無法確認適用費率」並禁止用靜態參考價替代。 | 權限隔離、錯誤回應、環境不同版次、跨月快取失效、未登入拒絕、無適用價與未生效版次 API 測試。 |
| A2 詳細前端／Cloud Admin | 將「Billing > Service Pricing」改成正式生效、已核准待生效、參考價／尚待核准三種明確區塊；顯示第 4 節完整表格與計算說明。核准與參考數字由受 Cloud Billing 權限保護的端點提供，不放進匿名可下載的前端資產；Product 選服務與 OTA disabled 畫面連到對應費率；invoice/usage 明細顯示實際 quantity、unit、rate、未稅／稅／總額、pricing version、UTC 期間。持續禁止已移除的固定 NT$232 範例，未來估算器只用當期有效價卡。 | 繁中／英文、窄螢幕表格、鍵盤／螢幕閱讀器、匿名資產與 API 無數字／未授權拒絕、無價／API outage／切月、Product 切換與 invoice drilldown E2E。 |
| Q1 staging 資格／各 owner | 在隔離資料與固定版次下實測 Product grant、OTA 註冊、CDN 直接下載與 Range、四 receipt/outbox、雙 seal、月結、帳單及價格頁；核對 Billing DB 當期／下期版次。 | 完整且可追溯的 staging 報告；發現 CDN／period seal 差異就不發佈。 |
| R1 production 發佈／Finance、營運 | 取得稅務／合約／適用客群決定和 staging 簽核後，選**下一個尚未開始的完整 UTC 月**為生效月，預先發佈審核過的完整版本；在邊界前後核對 API/UI、第一月對帳、舊客帳單與異常回退程序。 | 發佈紀錄、客戶告知及正式環境版次、第一張 OTA invoice 的四項來源與金額對帳。不得回填已過月份。 |

此表是實作順序與驗收條件，**不是已完成清單**。staging／production 的操作依 [Billing staging qualification](../billing-staging-qualification.md) 與部署治理另外執行。

## 4. 登入後價格頁的資訊架構與每項服務說明

頁首先選定 Brand Cloud／Product，列出「本帳戶合約類型」「正式計費幣別 TWD」「目前有效價卡版本及 UTC 生效區間」「稅務說明」「下一版與生效日」。價格列不得只靠 Product checkbox 推導：Product 啟用控制功能可用性；帳務還要看合約適用、有效價卡及實際用量。Evaluation 顯示免費條款；Private Cloud 的授權／維護費以合約報價，與下表 managed-cloud 用量費分開。

主表固定欄位：**服務／收費項目、何時記一筆、最高參考價、已核准價或當期有效價、計算公式與排除情況、此 Product 是否啟用、狀態與生效日**。可展開看實際量的來源、失敗與重試處理、稅及四捨五入；篩選和行動版不能隱藏「尚未生效」標籤。正式金額只取 Billing API 的有效版次；下列研究快照只可用醒目「參考價，非帳單依據」標籤顯示。OTA 四項已核准單價保持不變，最高外部參考價另外列出。

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

頁面底部用一個具體範例解釋：`當月同一 Product 同一項目數量 × 單價 = 未稅金額；按 invoice line 合計後以 NT$1 為單位取整；稅按生效價卡再計；多個 Product 分別列明`。千次／百萬次只是展示分母，**不是最低計費級距**。GiB = 1,073,741,824 bytes；GiB-month 按該 UTC 月實際時間積分。帳單期間用 UTC 正式標示，旁邊可加使用者本地時間換算。估算不等於發票，正式發票要附數量、版本與來源參照；付款與餘額狀態依既有 Billing 頁面。

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

Cloudflare R2 的 Infrequent Access、不同維度的 TURN 分鐘、平價包套、免費額、尚未正式收取的未來項目及歷史舊價格不參與最高價排名；它們仍記在研究候選表並註明排除原因。最高參考價不會寫入 Billing 的有效價卡。

**成本關卡：**核准的 OTA 下載售價 NT$0.96／GiB，與 AWS CloudFront 部分地區公開出口價相比可能不足以覆蓋 CDN 成本。這是風險訊號而非重新定價結論；Finance 在正式啟用前要按**實際 CDN 合約、交付地區、流量級距、免費額、快取命中、Range／重試流量、匯率及稅**計算每 GiB 成本與毛利，留下簽核記錄。使用者價格頁不展示供應商成本或暗示本服務轉售 AWS。

15 項舊價、官方候選值、選出的最高參考價與排除原因記在 [service-pricing-research.md](../../repos/rtk_cloud_admin/docs/service-pricing-research.md)；商務仍須另外核准非 OTA 正式價格。

| 文件 | 放置內容及維護者 |
| --- | --- |
| 本文件 `docs/design/ota-pricing-activation-and-disclosure-plan.md` | 跨 repo 決策、順序、缺口與驗收；workspace 維護。 |
| `repos/rtk_cloud_contracts_doc/ota_delivery_and_billing.md` 與 `pricing_and_invoicing.md` | 規範性計量／價卡／不追收／月份契約；Billing 與 OTA owners 維護。 |
| `repos/rtk_billing/docs/pricing-activation-runbook.md`（待新增） | 環境盤點、完整價卡 diff、tax/approver、UTC 排程、回復與首單對帳；Billing／Finance 維護。 |
| `repos/rtk_cloud_admin/docs/service-pricing-disclosure.md`（待新增） | 使用者文案、15 項清單、狀態詞、範例、i18n／無障礙驗收；Cloud Admin 維護。 |
| `repos/rtk_cloud_admin/docs/service-pricing-research.md` | AWS 等官方 benchmark 的查核日期與非等價說明；**非正式費率來源**。 |
| `docs/business-model.md` | 公開官網與登入後揭露界線、evaluation／managed cloud／private quote 適用關係；workspace 商務 owner 維護。 |

## 6. 尚需決策的生效條件

1. Finance／法務核定 OTA 四項的稅別、稅率或免稅依據，以及 evaluation、managed cloud、private commercial 哪些帳戶適用；`TaxRateBasisPoints=0` 的程式預設**不代表已核准免稅**。
2. 盤點正式環境當期完整 TWD 價卡與所有合約特例，再核准是否要把其他 11 項研究價提升為正式單價；這次只有 OTA 四價已獲核准。
3. 定義時區月份轉 UTC 的一次性邊界、月中 owner 移轉／Cloud closure 的責任分配。未通過對帳時 OTA 該月不自動收費。
4. 完成 CDN 與雙 seal 的 staging 資格、第一個可用的**未來**完整 UTC 月，以及客戶告知時點，才可發佈 production 價卡。

在上述條件解決前，本文件及畫面的「參考價／已核准待生效」皆不構成實際收費。
