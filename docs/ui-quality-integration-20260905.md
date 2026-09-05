# Realtek Web UI — 保留改版與上游整合紀錄

日期：2026-09-05。範圍：將既有 Console、Service login、Portal 設計保留在最新上游程式上，包含遞迴子模組；將新增頁面套用同一套 Realtek 設計規範。這是本機整合交付，不是部署或 GitHub CI 通過聲明。

## 交付位置與保留方式

- 整合工作目錄：`/Users/kevinhuang/work/rtk_cloud_workspace1/.artifacts/ui-integration-20260905/workspace`。
- 主工作區及三個修改中的子模組使用本機分支 `codex/ui-quality-integration-20260905`。
- 原工作目錄 `/Users/kevinhuang/work/rtk_cloud_workspace1` 保持原有版本與未提交內容；沒有重設、覆寫或隱性暫存其修改。
- [原始修改備份](../../backup/) 包含各程式庫的基準版本、狀態、二進位相容差異檔與未追蹤檔案封存。完成後再次比對三個原始子模組的 tracked diff SHA-256，均與備份一致。
- 同一備份目錄另存 Workspace、Admin、Portal、Contracts 四個已驗證的 integration Git bundle；它們包含新增本機提交，復原時仍需上表所列的上游基準提交。
- 原主工作區的 `tools/ci-dashboard/README.md` 與 `tools/ci-dashboard/deploy/` 是無關工作，保留在原位置且另有備份，未混入這次 UI 整合。

最新上游是整合基底，再以三方合併套回原有設計。Admin 衝突逐處處理，保留新路由、cloud scope、社群登入處理與 Test Lab／燒錄工作流程，不以整份舊檔覆蓋新功能。

## 版本與子模組

| 程式庫 | 上游整合基準 | 本機整合版本 |
| --- | --- | --- |
| Workspace | `13a7a3715447ef30d0e6472dd3497d87911cc1e6` | 本報告所在分支；gitlinks 固定下列版本 |
| Account Manager | `c670821b36f9e0bbf961015d83f3a59ccffc52a9` | 未修改 |
| Ameba WebRTC | `cfc095585abebb05dda85427872ab8f5ee801a73` | 未修改 |
| Billing | `026016a9e7974fd07e24b95a97bed913a1de7639` | 未修改 |
| Cloud Admin | `68e976ced5b6722bb43d5d6ecdd675d53c6ac572` | `9738b730118c31fb76ecf6d3ee50ec99006f731f` |
| Cloud Client | `0ae797a92424a47f2e753630e9fb2e47de273eed` | 未修改 |
| Contracts | `64101b9929a04c8747496b3bb8b475c95880874c` | `9c75c1e17cfbc2495de947ffda8c9ba7ad85e604` |
| Portal / Frontend | `302cb315478fb25ebf5d165c1c72316998cc90b0` | `e850b49bca16255ed4055be1d29c8113a49199b5` |
| Cloud Logger | `8f45811a76190b946de0890400f6b943e69ccbfa` | 未修改 |
| Video Cloud | `c8046b27f24a89156831c7387b01e14d8ebd3d25` | 未修改 |
| Video Cloud → EMQX Docker | `cddc2e939a0ec978fb381aaed4bf2cd172c625f6` | 未修改 |

九個直接子模組與一個巢狀子模組均已初始化及同步 URL。Portal 比 Workspace 原指標更新，已包含 PR #147。所有本機整合提交尚未推送；不能把這些未發佈 gitlinks 當作其他電腦可直接下載的上游版本。

收尾時再次從 GitHub 取得 Workspace 與全部遞迴子模組的 main；各程式庫皆沒有尚未納入的 main 提交（`HEAD..origin/main` 計數均為 0）。

## UI 整合內容

- **共同風格**：保留 Realtek 官方藍 `#0068b7`／深藍 `#035390`、白色與中性色面板；一致的字級、間距、邊框、按鈕與焦點樣式。GitHub／Microsoft 作為清晰度與企業品質參考，不使用其品牌識別。
- **Console**：保留精簡資源列表、情境導覽、cloud 切換、文件閱讀版面及 Billing 等頁面的一致樣式。平台管理身分標示仍可見，切換檢視位於 Account 選單。
- **Service login**：保留 Realtek 品牌／表單雙欄，手機為精簡單欄。最新社群登入按鈕、provider 不可用及 callback 錯誤套用同一風格；不改 OAuth、session 或 next 跳轉契約。
- **Cloud Test Lab**：保留上游裝置管理與 mapping 流程；簡化介紹層級、統一 protocol tabs、資料表與結果區。新增方向鍵／Home／End 操作，確認視窗限制焦點、Escape 取消及回復觸發按鈕焦點；不因頁面瀏覽自動執行 live action。
- **PRO2 瀏覽器燒錄工具**：保留完整新功能；以共同 task card、字級與按鈕搭配功能性的深色 UART terminal。保留安全環境警告、本機資料處理、操作確認及停用狀態。
- **Portal**：保留首頁、功能頁、文件／Manual／SDK、Contact、法律頁共用風格、三語版型與無障礙導覽，整合最新 Docs 上游變更。私人管理頁不套用新公開網站主題。

設計文件：

- [共用 Frontend Style Contract](../repos/rtk_cloud_contracts_doc/frontend_style.md)
- [Console／Service login／Developer tools UI guideline](../repos/rtk_cloud_admin/docs/web-ui-style-guideline.md)
- [Portal UI guideline](../repos/rtk_cloud_frontend/docs/portal-ui-style-guideline.md)

## 本機驗證與證據

| 檢查 | 結果 | 證據 |
| --- | --- | --- |
| Admin production build | 通過；保留既有大型 JS chunk 提示 | [build log](../../admin-build.log) |
| Admin 前端單元測試 | 181 passed，0 failed | [unit log](../../admin-unit-final.log) |
| Admin Go 測試 | `GOWORK=off go test ./...` 通過 | [Admin Go log](../../admin-go.log) |
| Portal Go 測試 | `GOWORK=off go test ./...` 通過 | [Portal Go log](../../portal-go.log) |
| Admin 桌面／手機完整瀏覽器測試 | 158 passed，13 skipped，0 failed；全新隔離測試環境 | [clean run log](../../admin-clean.log)、[HTML report](../../evidence/clean/playwright-report/main/index.html) |
| Darwin SDK／PRO2 視覺基準 | 6 項於完整測試中通過 | [clean evidence](../../evidence/clean/) |
| Linux SDK／PRO2 視覺基準 | 6 項更新後、不帶 update 選項再次比對通過 | [Linux log](../../linux-final.log) |
| Portal 三語、四種尺寸版面與互動 | 144 page/viewport cases 及選單、Escape、tabs、accordion、目錄、表單驗證通過 | [Portal log](../../portal-visual.log)、[screenshots](../../evidence/portal/) |
| 測試目錄與規格追溯 | 313 cases；47 features、431 requirements、0 blocking findings | 已更新 `tests/catalog.yaml` 及產生文件 |
| Workspace 共用工具測試 | `go test ./scripts/go/rtk-cloud` 通過 | [workspace tools log](../../workspace-tools.log) |

新整合頁面測試涵蓋 1440、1024、768、390、320px。Console 既有頁面另涵蓋 1280px；Portal 使用 1440、768、390、320px。以實際截圖檢視登入桌面／窄螢幕、Test Lab、SDK 與 PRO2 連線前後，以及 Portal 文件與首頁等代表畫面。12 張受設計影響的 Darwin／Linux 桌面與手機基準經檢視後更新，沒有提高容許差異門檻。

第一次 Linux 測試使用 Docker host 名稱而觸發 Web Serial 非安全來源警告；改以容器內 localhost 轉接同一 fixture，維持 secure-context 語意後再檢查，未修改產品的安全判斷。

重複使用常駐預覽資料曾造成兩項測試失敗：先前案例已改變雲端啟用狀態／權限。改由標準測試啟動器建立全新 fixture 後完整重跑通過；沒有刪除斷言或以重試掩蓋。正式證據採用 `evidence/clean`，較早的失敗紀錄保留供追溯。

**略過不代表通過。** 13 項原有情境測試需要 unavailable、empty、stale、unconfigured 或特定資源模式。自訂 evidence reporter 將這些 SKIP 的 assessment 設為 FAIL，因此單一預設情境的 `results.json` aggregate 仍是 FAIL；不能當作完整發版 gate 已通過。另有 91 個既有 unspecified normative candidates，規格檢查視為非阻擋發現。未更新單元測試數量契約、降低 coverage ratchet 或修改 gate。

重跑 Admin 完整瀏覽器測試時請使用未占用的新 port，讓啟動器建立新測試資料，不要指向已操作過的常駐預覽：

```sh
# 在 repos/rtk_cloud_admin/web 中執行；不設定 E2E_BASE_URL
E2E_APP_PORT=18089 npx playwright test --project=chromium --project=mobile --workers=2
```

## 預覽與發佈邊界

- [整合版 Service login](http://127.0.0.1:18087/login)：獨立本機 fixture，並非 development 網站或真實社群登入服務。
- [整合版 Portal Docs](http://127.0.0.1:18088/docs)：獨立本機資料庫，analytics 關閉。原 18082／18084 預覽未被替換。
- 沒有推送 GitHub、建立或合併 PR、部署、修改真實客戶資料、實際燒錄硬體或操作真實付款／OAuth provider。
- 發佈前仍需設計擁有人驗收、正式 accessibility／200% zoom 檢查、特定環境情境測試及適用的 pre-PR／GitHub gates。這次本機驗證不是 CI 或硬體相容性認證。
- Portal 部分既有文件仍有 placeholder／示範內容；法律與隱私聯絡資料需內容擁有人核准。搜尋及可下載 SDK catalog 啟用狀態的視覺驗收仍依 Portal guideline 執行。
- 若之後授權發佈，先完成 Contracts、Admin、Portal 各 leaf repository 的 PR 流程，再以最終已合併的提交更新一次 Workspace gitlinks；不要直接發佈尚未存在於上游的本機指標。
