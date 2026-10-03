# 部署檢查腳本改善與驗證報告

Classification: supporting-note

Status: active — 改善已實作於本次 PR；本文件記錄本機驗證，不代表已合併或線上部署證據。

Owner: rtk_cloud_workspace

Last reviewed: 2026-10-03

Applies to: 既有環境的部署檢查器；排除新環境建立與完整 release 編排。

## 一、結果

新增 `rtk-cloud deployment check`，統一 SecretStore、live PKI 與所選 credential checks。Shell 建置一次、執行一次 Go 程式；使用同一程序中的 goroutine 與有限並行處理獨立查詢。

必要的 SecretStore／live PKI 證據仍是門檻。檢查成功代表本輪列出的必驗項目通過；完整發布仍依 [部署操作指南](../deployment-operations.md) 的 release gates 判定。

本次優先修正錯誤通過與模糊診斷，再加入期限、共享查詢與並行。直接呼叫 `deployment credentials-check` 的本機 TLS 與既有部署用途保留原範圍。

## 二、模式與相容性

| 模式 | Provider 寫入 | 指定 `--image` |
| --- | --- | --- |
| 預設 | 保留適用的 DNS／storage canary 與 receipt | 完整 `linux/amd64` Docker pull |
| `--read-only` | 不建立 canary／receipt | 仍執行完整 pull |
| `--fast` | 唯讀；與 repair 或明確的 `--read-only=false` 衝突 | 驗證認證、digest、manifest、config 與平台，略過 layer pull |

`--checks` 只篩選 provider／TLS／mount 項目，保留前段基礎檢查。參數錯誤、缺少輸入及模式衝突在外部檢查前拒絕。Shell 與編譯後 CLI 的成功／檢查失敗／參數錯誤分別回傳 0／1／2；`go run` 可能把程式的 exit 2 包成 1，因此自動化使用 Shell 入口。

## 三、判斷與覆蓋改善

- **GoDaddy：** 執行所選 zone 的 authenticated records GET，驗證 HTTP 狀態及 JSON 格式；零 API 請求不能產生認證 PASS。
- **Live 證據：** 缺 kubeconfig、必要權限、有效 inventory 或必要 workload 時回報 ERROR／BLOCKED；必驗項目未完成不能整體成功。
- **映像：** 快速模式驗證 index／manifest／config 的內容 digest 及 `linux/amd64` 平台；HEAD 成功不足以取得此證據。Layer 下載、控制端 pull 與叢集 rollout 各有不同證據範圍。
- **CertIssuer：** 修正 socket fixture，選取 Ready、Running 且未終止的 Pod；用 JSON parser 解析，保留精確 `400 + user_id_required` 成功條件；加入 TLS hostname／chain 驗證及 socket／TLS／認證／5xx／格式分類。
- **Route53：** 尚未完成 credential qualification 的路徑明列未支援，不能回報通過。
- **結果清單：** 執行前列出適用項目與相依；未選取或不適用項目附略過原因。統一 PASS／FAIL／ERROR／BLOCKED／SKIPPED。

## 四、速度、期限與清理

最多四個獨立唯讀 worker、兩個 Docker pull。映像內 token、manifest、pull 保留相依順序；provider 寫入及 repair 保持同步，受 SecretStore／PKI 前置門檻控制。

同輪共用 Kubernetes Secret、Deployment、ConfigMap、DB Pod 與 Linode bucket／key inventory；Secret 資料僅在記憶體保留選定 stack。Worker 啟動前完成 credential normalization。同輪相同映像輸入重用結果；下次執行重新驗證。

| 項目 | 預設期限 |
| --- | --- |
| 快速／標準模式整體 | 2 分鐘／10 分鐘；`--timeout` 可調整 |
| 單次 HTTP | 15 秒 |
| Kubernetes request／命令 | 10 秒／20 秒 |
| SQL statement／exec | 10 秒／30 秒 |
| 每張 Docker pull | 5 分鐘 |
| 取消後 canary 清理 | 額外最多 30 秒 |

單項期限受整體剩餘時間限制。整體期限從 CLI／設定驗證後開始，不包含 Shell 建置。GET／HEAD 遇暫時性連線失敗或 429／502／503／504，最多重試一次，遵守 `Retry-After` 及剩餘預算。認證、設定與格式錯誤不重試。

取消後停止派發、終止受控子程序並保留部分結果。已建立 canary 使用獨立有界清理 context；清理失敗另列 `CANARY_CLEANUP_FAILED`，清理期限可能延長整體執行時間。

文字與 JSON 共用結果來源。逐項顯示開始／完成，長步驟每 10 秒顯示進度，最後固定排序並列出根因與最慢三項。`--report` 產生經遮蔽、權限 0600 的 JSON；不輸出原始敏感 body／stderr。

## 五、驗證與證據限制

- 本機 fake server／kubectl／SQL／Docker 案例涵蓋無效憑證、403／503、錯誤 JSON、TLS hostname／CA、逾時、取消、清理失敗、inventory 共用與最大並行數。
- 受影響 Go 回歸與 `go test -race` 通過；CertIssuer Shell fixture 25 個案例、Bash／Perl 語法、文件檢查與 Linux amd64 建置通過。
- 固定延遲測試：六個 repository 各兩次 40ms 查詢，串行約 500ms、並行約 168ms，耗時降低 66.4%，符合本機 fixture 至少降低 50% 的驗收條件。
- 程式版本 `2d2211118d2f5b22f581fb0947ee033eafff2a56` 的預設 `pre-pr --base origin/main` 通過，包含完整 workspace policy matrix 與受影響的 workspace-tooling coverage。覆蓋率測試中的 1,332 個頂層 CLI 案例全部通過；live smoke 依預設略過。後續僅補充本報告與文件索引，沿用未變更程式碼的驗證結果並重新檢查文件。
- Workspace-tooling 的受治理整體覆蓋率為 **78.15%**，差異覆蓋率為 **84.17%**；分別通過既有 70%／80% 門檻。測試紀錄為 `local-pre-pr-20261003T072953Z-go`。
- 未執行線上環境操作或線上 p50／p95 測量。Docker layer cache、映像大小、網路及相同 scope／digest 條件仍會影響實際耗時；本機百分比不代表線上效能承諾。

完整選項、修復限制與例子見 [Scripts 操作文件](../../scripts/README.md#existing-environment-deployment-checks)。實作入口為 [deployment_check.go](../../scripts/go/rtk-cloud/deployment_check.go)，provider 執行器為 [deployment_check_providers.go](../../scripts/go/rtk-cloud/deployment_check_providers.go)。
