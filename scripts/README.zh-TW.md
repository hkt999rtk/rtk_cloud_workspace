# scripts 目錄說明

這個目錄放的是 workspace 層級的操作腳本，主要用途包含文件檢查、部署證據收集、Linode staging provision/deploy、brand cloud 建立，以及 GitHub Actions self-hosted runner 管理。

除非特別註明，以下指令都建議從 workspace 根目錄執行。

## 一般檢查與同步

### `scripts/status-all.sh`

顯示 workspace 與所有 submodule 的 Git 狀態和最後一個 commit。

用法：

```sh
scripts/status-all.sh
```

適合在切 branch、pull、submodule update 前後確認整體狀態。

### `scripts/sync-all.sh`

同步 workspace 與 submodule remote 資訊，並初始化/更新 submodule 到目前 superproject 釘住的 commit。它只 fetch remote，不會改變 superproject 記錄的 submodule commit。

用法：

```sh
scripts/sync-all.sh
```

### `scripts/test-matrix.sh`

執行 workspace 快速驗證矩陣，包括 workspace 狀態、submodule 狀態，以及 `rtk_cloud_client` 的基本 Python unittest。

用法：

```sh
scripts/test-matrix.sh
```

### `scripts/docs-check.sh`

檢查 workspace 文件入口、重要 runbook、e2e 測試目錄、submodule 文件，以及 contracts submodule 是否對齊。

用法：

```sh
scripts/docs-check.sh
```

### `scripts/secrets-check.sh`

檢查 `.secrets` 等敏感路徑是否被 git ignore，並掃描 tracked workspace 檔案，避免誤提交 private key、token、password、DSN 等敏感字串。

用法：

```sh
scripts/secrets-check.sh
```

## 證據與報告

### `scripts/collect-private-cloud-evidence.sh`

收集 private cloud / product readiness 相關證據，輸出到 `evidence/` 或指定目錄。腳本會產生 manifest、服務 commit 狀態、健康檢查、報告摘要，並對敏感資訊做 redaction。

常用用法：

```sh
scripts/collect-private-cloud-evidence.sh
```

可用環境變數：

```sh
RTK_EVIDENCE_ENVIRONMENT=evaluation \
RTK_EVIDENCE_OUTPUT_DIR=./evidence \
RTK_EVIDENCE_RUN_SERVICE_COLLECTORS=0 \
RTK_EVIDENCE_TARBALL=1 \
scripts/collect-private-cloud-evidence.sh
```

## Linode staging 操作

### `scripts/staging-provision.sh`

Linode staging 的主要編排腳本。它可以做 preflight、plan、reset、apply、DNS、deploy、artifact collection、e2e smoke。預設不變更環境，只做 `--plan`。

常用用法：

```sh
# 只看目前狀態與預期資源，不做變更
scripts/staging-provision.sh

# 檢查工具、env、credential、SSH key、release artifact
scripts/staging-provision.sh --preflight

# 建立/更新 staging VM、DNS、部署三個服務、收集 artifacts、跑 e2e
scripts/staging-provision.sh \
  --all \
  --video-release VIDEO_RELEASE \
  --account-release ACCOUNT_RELEASE \
  --admin-release ADMIN_RELEASE

# 先刪除 staging VM/firewall/VPC，再重建與部署
scripts/staging-provision.sh \
  --reset-and-all \
  --confirm rtk-cloud-staging \
  --video-release VIDEO_RELEASE \
  --account-release ACCOUNT_RELEASE \
  --admin-release ADMIN_RELEASE
```

常用選項：

- `--workspace PATH`：指定 workspace 根目錄。
- `--secrets-root PATH`：指定 staging secrets 根目錄，預設是 `.secrets/staging/linode`。
- `--operator-env PATH`：指定含 `LINODE_TOKEN`、GoDaddy/Object Storage 設定的 env 檔。
- `--ssh-key PATH`：指定連線 Linode 的 SSH key。
- `--dns-wait-ttl SECONDS`：DNS converge 等待期間使用的 TTL。
- `--dns-final-ttl SECONDS`：DNS converge 後恢復的 TTL。
- `--verbose`：輸出更多 debug 訊息。

### `scripts/staging-deploy.sh`

只做 staging deploy/verify，不負責建立 VM。它會依序部署與驗證 Account Manager、Video Cloud、Cloud Admin，失敗時會停止後續步驟並寫 readiness report。

用法：

```sh
scripts/staging-deploy.sh \
  --video-release VIDEO_RELEASE \
  --account-release ACCOUNT_RELEASE \
  --admin-release ADMIN_RELEASE
```

常用選項：

- `--admin-release-bundle PATH`：使用本機 Cloud Admin release bundle，不從 Object Storage 下載。
- `--artifact-dir PATH`：指定 readiness report 和 logs 輸出目錄。
- `--dns-ttl SECONDS`：傳給 Video Cloud deploy 的 GoDaddy DNS TTL。
- `--verbose`：輸出更多 debug 訊息。

### `scripts/staging-remove-all-vm.sh`

刪除 Linode 上 label 含 `staging` 的 VM。這是破壞性操作，腳本會要求輸入 `yes` 才會送出刪除請求。

用法：

```sh
scripts/staging-remove-all-vm.sh
```

需要 `LINODE_TOKEN`。通常 token 會放在 operator env 或 shell 環境。

### `scripts/staging-update-ssh-whitelist.sh`

更新所有 staging VM firewall 的 SSH allowlist。預設會自動偵測目前這台操作機器對外的 public IPv4，並把它以 `/32` 加到 7 個 staging firewall 的 port 22 rule：

- `video-cloud-staging-edge`
- `video-cloud-staging-api`
- `video-cloud-staging-infra`
- `video-cloud-staging-mqtt`
- `video-cloud-staging-coturn`
- `rtk-account-manager-staging-fw`
- `rtk-cloud-admin-staging-firewall`

用法：

```sh
# 自動偵測目前 public IP，加入 SSH allowlist
scripts/staging-update-ssh-whitelist.sh

# 手動指定 CIDR
scripts/staging-update-ssh-whitelist.sh --cidr 203.0.113.10/32

# 只看會更新哪些 firewall，不呼叫 Linode API 修改
scripts/staging-update-ssh-whitelist.sh --cidr 203.0.113.10/32 --dry-run
```

腳本只會 append CIDR，不會移除既有白名單。成功更新 Linode firewall 後，也會同步更新本地 ignored staging config/env，避免之後重新 provision 時又回到舊白名單。

### `scripts/staging_create_brandname_cloud.sh`

在 Account Manager staging 上建立 brand cloud。腳本會先確保 platform-admin bootstrap env 可用，再用 Account Manager API 建立 brand cloud；若 API 建立遇到已知 server error，會用 PostgreSQL fallback upsert，最後再透過 API 驗證結果。

用法：

```sh
scripts/staging_create_brandname_cloud.sh --brandname RTK
```

常用選項：

- `--workspace PATH`：指定 workspace 根目錄。
- `--secrets-root PATH`：指定 `.secrets/staging/linode` 位置。
- `--skip-bootstrap`：不要更新/restart 遠端 Account Manager bootstrap admin env。

腳本的進度訊息會寫到 stderr，最後 JSON 結果會寫到 stdout，方便其他工具解析。

### `scripts/staging_list_brandname_clouds.sh`

查詢 Account Manager staging 目前有哪些 brand cloud。腳本會使用 staging platform-admin 帳密登入，呼叫唯讀的 Account Manager admin API，不會修改資料。

用法：

```sh
# 顯示數量與摘要表格
scripts/staging_list_brandname_clouds.sh

# 查詢特定 brandname
scripts/staging_list_brandname_clouds.sh --brandname RTK

# 輸出完整 JSON，包含每個 brand cloud 的 metadata 等設定
scripts/staging_list_brandname_clouds.sh --json
```

常用選項：

- `--workspace PATH`：指定 workspace 根目錄。
- `--secrets-root PATH`：指定 `.secrets/staging/linode` 位置。
- `--brandname NAME`：只顯示 `name` 或 `metadata.brandname` 符合的 brand cloud。
- `--limit N`：指定 API list limit，預設 `200`。
- `--json`：輸出完整 API JSON，適合用 `jq` 進一步查詢。

預設摘要會顯示 `brand_clouds`、`api_total`、`id`、`name`、`status`、`tier`、`evaluation_device_quota`、`metadata.brandname`、`created_at` 與完整 `metadata`。若要確認「每個 brandname cloud 的內容設定」，建議使用 `--json`。

## Linode CI runner 管理

這些腳本位於 `scripts/linode-ci-runners/`，用來管理 repo-scoped GitHub Actions self-hosted runner VM。

### `scripts/linode-ci-runners/runner-specs.sh`

共用設定檔，定義 dedicated runner VM、runner name、目標 GitHub repo、Linode type、runner label。通常不直接執行，而是被其他 runner 腳本 `source`。

### `scripts/linode-ci-runners/provision-ci-runners.sh`

建立 dedicated Linode runner VM、防火牆，並註冊 GitHub Actions self-hosted runner。

用法：

```sh
scripts/linode-ci-runners/provision-ci-runners.sh
```

需要的設定通常來自：

- `.secrets/shared/linode/env/ci-runners.env`
- `.secrets/shared/github/env/runner-registration.env`

必要變數包含 `LINODE_TOKEN`、`GITHUB_TOKEN`、`CI_RUNNER_ALLOWED_SSH_CIDRS`、SSH key 路徑等。

### `scripts/linode-ci-runners/power-ci-runners.sh`

啟動、關閉或列出 dedicated runner VM 狀態。

用法：

```sh
scripts/linode-ci-runners/power-ci-runners.sh status
scripts/linode-ci-runners/power-ci-runners.sh start
scripts/linode-ci-runners/power-ci-runners.sh stop
```

### `scripts/linode-ci-runners/wait-runners-online.sh`

等待 GitHub Actions runner 進入 online 狀態。常和 `power-ci-runners.sh start` 搭配使用。

用法：

```sh
scripts/linode-ci-runners/wait-runners-online.sh
```

可用環境變數：

- `CI_RUNNER_ONLINE_TIMEOUT_SECONDS`：等待 timeout，預設 900 秒。
- `CI_RUNNER_ONLINE_POLL_SECONDS`：輪詢間隔，預設 15 秒。

### `scripts/linode-ci-runners/list-ci-runners.sh`

列出 Account Manager、Cloud Admin、Video Cloud repo 的 GitHub Actions self-hosted runner 狀態、busy 狀態與 labels。

用法：

```sh
scripts/linode-ci-runners/list-ci-runners.sh
```

需要已登入的 `gh`。

### `scripts/linode-ci-runners/run-ci-session.sh`

完整 CI session 編排：啟動 runner VM、等待 runner online、可選擇 rerun 指定 GitHub Actions run、watch 到結束、封存 artifacts 到 Linode Object Storage，最後依 policy 關閉 VM。

用法：

```sh
scripts/linode-ci-runners/run-ci-session.sh \
  --account-run-id RUN_ID \
  --admin-run-id RUN_ID \
  --video-run-id RUN_ID
```

常用選項：

- `--rerun true|false`：是否先 rerun 指定 run，預設 true。
- `--shutdown-policy always|on-success|never`：何時關閉 runner VM，預設 always。
- `--smoke-only true`：只測試 VM start -> runner online -> shutdown，不需要 run id。

### `scripts/linode-ci-runners/archive-ci-artifacts.sh`

下載某個 GitHub Actions run 的 artifacts，並上傳到 Linode Object Storage。

用法：

```sh
scripts/linode-ci-runners/archive-ci-artifacts.sh \
  --repo hkt999rtk/rtk_video_cloud \
  --run-id RUN_ID
```

可加 `--prefix PREFIX` 指定 Object Storage prefix。需要 `gh`、`LINODE_OBJ_BUCKET`、`LINODE_OBJ_ENDPOINT`、`LINODE_OBJ_ACCESS_KEY_ID`、`LINODE_OBJ_SECRET_ACCESS_KEY`。若本機有 `aws` CLI 會用 `aws s3 sync`；否則使用 Python `boto3` fallback。
