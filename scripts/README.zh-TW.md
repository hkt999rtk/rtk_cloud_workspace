# scripts 目錄說明

這個目錄放的是 workspace 層級的操作腳本，主要用途包含文件檢查、部署證據收集、Linode staging provision/deploy、brand cloud 建立，以及 GitHub Actions self-hosted runner 管理。

除非特別註明，以下指令都建議從 workspace 根目錄執行。

## Cloud Environment Root

Linode staging scripts 預設使用本機、git ignored 的 `cloud_env/staging/linode` 作為實際 Linode environment root。操作時可用 `--env-root cloud_env/staging` 指定 staging environment directory；script 會自動解析到 `cloud_env/staging/linode`。這個目錄集中保存 operator env、topology、service env、state、keys/certificates、device fixtures、artifacts 與 backups。

可用 `--env-root PATH` 指向另一份 environment directory。舊的 `--secrets-root PATH` 仍保留為相容 alias，但新的操作與文件都應使用 `--env-root`。

`cloud-*` 是目前正式入口。舊的 `staging-*` / `staging_*` script 名稱暫時保留為相容 wrapper，會顯示 deprecated warning 後轉呼叫對應的 `cloud-*` script；新 automation 與文件應使用 `cloud-*` 名稱。

目錄配置請見 `docs/cloud-env-layout.zh-TW.md`。

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

## Linode cloud environment 操作

### `scripts/cloud-generate-load-devices.sh`

依照量產流程的素材形狀，產生 staging/load-test 用的模擬 device 身分。每台 device 會有 private key、CSR、由本地 simulation CA 簽出的 client certificate、metadata、PEM bundle，以及 load test 可直接 source 的 env 檔。metadata 會同時記錄 inventory 用的 `device_type` 與 ACL 用的 `service_options`；`device_type` 不作為 ACL 來源。

預設產生 100 台，類型只使用目前 load runner 已實作的模擬種類：

- `camera`
- `light`
- `air_conditioner`
- `smart_meter`

預設 service options：

- `camera`：`mqtt`, `video_streaming`, `video_storage`
- `light` / `air_conditioner` / `smart_meter`：`mqtt`

用法：

```sh
# 預設產生 100 台：camera=40,light=25,air_conditioner=20,smart_meter=15
scripts/cloud-generate-load-devices.sh --env-root cloud_env/staging

# 指定數量與配比
scripts/cloud-generate-load-devices.sh \
  --env-root cloud_env/staging \
  --count 200 \
  --mix camera=80,light=50,air_conditioner=40,smart_meter=30

# 指定輸出目錄；若目錄已存在，用 --force 重建
scripts/cloud-generate-load-devices.sh \
  --env-root cloud_env/staging \
  --out-dir cloud_env/staging/linode/devices/manual \
  --force
```

常用選項：

- `--count N`：產生 device 數量，預設 `100`。
- `--mix SPEC`：類型權重，例如 `camera=40,light=25,air_conditioner=20,smart_meter=15`。
- `--prefix PREFIX`：device id prefix，預設 `load-device`，輸出如 `load-device-0001`。
- `--env-root PATH`：指定 environment directory；必填。可傳 `cloud_env/staging`，script 會自動使用其下的 `linode/`。
- `--out-dir PATH`：輸出目錄，預設 `cloud_env/staging/linode/devices/test_device`。
- `--force`：移除既有輸出目錄後重建。

重要輸出：

- `summary.json`：本次產生的數量、配比與主要路徑。
- `manifests/devices.json`：完整 device inventory。
- `manifests/devices.csv`：簡表。
- `manifests/device_ids.txt`：load test 可用的 device id 清單。
- `loadtest.env`：可 `source` 的 load test 參數，不包含 bearer token。

輸出的 private key 與 CA key 預設位於 git ignored 的 `cloud_env/staging/linode/devices/test_device`，不可 commit，也不可用在 production 或 customer environment。若要重建既有輸出，使用 `--force`。

### `scripts/cloud-migrate-env.sh`

將目前分散在 `.secrets/`、`keys/`、以及各 submodule deploy 目錄的 staging local environment 檔案複製到 `cloud_env/staging/linode`。來源檔案會保留，並在 `cloud_env/staging/linode/backups/migration-<timestamp>` 產生 backup 與 migration manifest。

用法：

```sh
scripts/cloud-migrate-env.sh --env-root cloud_env/staging
scripts/cloud-migrate-env.sh --env-root cloud_env/staging --force
```

常用選項：

- `--env-root PATH`：指定 environment directory；必填。可傳 `cloud_env/staging`，script 會自動使用其下的 `linode/`。
- `--force`：覆蓋已存在的 target 檔案。

### `scripts/cloud-provision.sh`

Linode staging 的主要編排腳本。它可以做 preflight、plan、reset、apply、DNS、deploy、artifact collection、e2e smoke。預設不變更環境，只做 `--plan`。

常用用法：

```sh
# 只看目前狀態與預期資源，不做變更
scripts/cloud-provision.sh --env-root cloud_env/staging

# 檢查工具、env、credential、SSH key、release artifact
scripts/cloud-provision.sh --env-root cloud_env/staging --preflight

# 建立/更新 staging VM、DNS、部署三個服務、收集 artifacts、跑 e2e
scripts/cloud-provision.sh \
  --env-root cloud_env/staging \
  --all \
  --video-release VIDEO_RELEASE \
  --account-release ACCOUNT_RELEASE \
  --admin-release ADMIN_RELEASE

# 先刪除 staging VM/firewall/VPC，再重建與部署
scripts/cloud-provision.sh \
  --env-root cloud_env/staging \
  --reset-and-all \
  --confirm rtk-cloud-staging \
  --video-release VIDEO_RELEASE \
  --account-release ACCOUNT_RELEASE \
  --admin-release ADMIN_RELEASE
```

常用選項：

- `--workspace PATH`：指定 workspace 根目錄。
- `--env-root PATH`：指定 environment directory；必填，避免操作錯誤環境。可傳 `cloud_env/staging`，script 會自動使用其下的 `linode/`。
- `--operator-env PATH`：指定含 `LINODE_TOKEN`、GoDaddy/Object Storage 設定的 env 檔。
- `--ssh-key PATH`：指定連線 Linode 的 SSH key。
- `--dns-wait-ttl SECONDS`：DNS converge 等待期間使用的 TTL。
- `--dns-final-ttl SECONDS`：DNS converge 後恢復的 TTL。
- `--dns-wait-max-seconds SECONDS`：每個 hostname 最長 DNS convergence 等待時間，預設 `700` 秒；每 10 秒檢查 Google DNS 與 authoritative DNS 一次。
- `--verbose`：輸出更多 debug 訊息。

HTTPS certificate cache 由 `cloud-deploy.sh` 處理：如果 `cloud_env/staging/linode/certificates/<fqdn>/fullchain.pem` 與 `privkey.pem` 存在且在安全期限內未過期，就會先上傳到新 VM，建立 certbot lineage/renewal config、啟用 `certbot.timer`，並跳過新憑證申請；如果沒有可用 cache，deploy 仍會走 certbot，成功後再把 VM 上的 certificate/key 拉回 `cloud_env`。預設要求 certificate 至少還有 7 天有效期，可用 `--cert-cache-min-valid-seconds` 調整。

### `scripts/cloud-deploy.sh`

只做 staging deploy/verify，不負責建立 VM。它會依序部署與驗證 Account Manager、Video Cloud、Cloud Admin，失敗時會停止後續步驟並寫 readiness report。

用法：

```sh
scripts/cloud-deploy.sh \
  --env-root cloud_env/staging \
  --video-release VIDEO_RELEASE \
  --account-release ACCOUNT_RELEASE \
  --admin-release ADMIN_RELEASE
```

常用選項：

- `--admin-release-bundle PATH`：使用本機 Cloud Admin release bundle，不從 Object Storage 下載。
- `--env-root PATH`：指定 environment directory；必填，避免部署到錯誤環境。可傳 `cloud_env/staging`，script 會自動使用其下的 `linode/`。
- `--artifact-dir PATH`：指定 readiness report 和 logs 輸出目錄。
- `--cert-cache-root PATH`：指定 HTTPS certificate cache 根目錄，預設 `<env-root>/certificates`。
- `--cert-cache-min-valid-seconds N`：cached certificate 至少還要有效多久才可重用，預設 `604800` 秒。
- `--dns-ttl SECONDS`：傳給 Video Cloud deploy 的 GoDaddy DNS TTL。
- `--verbose`：輸出更多 debug 訊息。

### `scripts/cloud-check-certificates.sh`

檢查 staging 對外 HTTPS host 的 certificate 是否合法、hostname 是否符合、是否尚未過期，並確認剩餘效期高於指定門檻。預設會同時檢查 live HTTPS endpoint 與 `cloud_env` 內的 certificate cache。

檢查目標：

- `video-cloud-staging.<root-domain>`
- `certissuer.video-cloud-staging.<root-domain>`
- `account-manager.video-cloud-staging.<root-domain>`
- `admin.video-cloud-staging.<root-domain>`

用法：

```sh
# 檢查 live endpoint 與本機 certificate cache
scripts/cloud-check-certificates.sh --env-root cloud_env/staging

# 只檢查 cloud_env 裡的 certificate cache，不連線到 live host
scripts/cloud-check-certificates.sh --env-root cloud_env/staging --skip-live

# JSON 輸出，方便 CI 或 jq 使用
scripts/cloud-check-certificates.sh --env-root cloud_env/staging --json
```

常用選項：

- `--env-root PATH`：指定 environment directory；必填，避免檢查錯誤環境。可傳 `cloud_env/staging`，script 會自動使用其下的 `linode/`。
- `--dns-root-domain NAME`：root DNS domain，預設 `realtekconnect.com`。
- `--min-valid-days N`：要求 certificate 至少還要有效幾天，預設 `7`。
- `--skip-live`：只檢查 `cloud_env/staging/linode/certificates` 內的 cache。
- `--skip-cache`：只檢查 live HTTPS endpoint。
- `--json`：輸出完整 JSON 結果。

如果任一 certificate 缺失、過期、hostname 不符合、live chain 驗證失敗，或低於剩餘效期門檻，script 會顯示 `status=fail` 並以 non-zero exit code 結束。

### `scripts/cloud-remove-all-vm.sh`

刪除 Linode 上 label 含 `staging` 的 VM，等待 VM 消失後清除 staging firewalls 與 `video-cloud-staging-vpc`，並把 active local state 移到 `cloud_env/staging/linode/backups/remove-vm-<timestamp>`。這是破壞性操作，腳本會要求輸入 `yes` 才會送出刪除請求。

用法：

```sh
scripts/cloud-remove-all-vm.sh --env-root cloud_env/staging
```

需要明確指定 `--env-root`，避免刪到錯誤環境。可以傳 `cloud_env/staging`，script 會自動解析到其下的 `linode/`。需要 `LINODE_TOKEN`；通常 token 會放在 operator env 或 shell 環境。若 Linode 回報資源已不存在，script 會視為已清除並繼續。

### `scripts/cloud-update-ssh-whitelist.sh`

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
scripts/cloud-update-ssh-whitelist.sh --env-root cloud_env/staging

# 手動指定 CIDR
scripts/cloud-update-ssh-whitelist.sh --env-root cloud_env/staging --cidr 203.0.113.10/32

# 只看會更新哪些 firewall，不呼叫 Linode API 修改
scripts/cloud-update-ssh-whitelist.sh --env-root cloud_env/staging --cidr 203.0.113.10/32 --dry-run
```

腳本只會 append CIDR，不會移除既有白名單。成功更新 Linode firewall 後，也會同步更新本地 ignored staging config/env，避免之後重新 provision 時又回到舊白名單。

### `scripts/cloud-create-brandname-cloud.sh`

在 Account Manager staging 上建立 brand cloud。腳本會先確保 platform-admin bootstrap env 可用，再用 Account Manager API 建立 brand cloud；若 API 建立遇到已知 server error，會用 PostgreSQL fallback upsert，最後再透過 API 驗證結果。

用法：

```sh
scripts/cloud-create-brandname-cloud.sh --env-root cloud_env/staging --brandname RTK
```

常用選項：

- `--workspace PATH`：指定 workspace 根目錄。
- `--env-root PATH`：指定 environment directory；必填，避免建立到錯誤環境。可傳 `cloud_env/staging`，script 會自動使用其下的 `linode/`。
- `--skip-bootstrap`：不要更新/restart 遠端 Account Manager bootstrap admin env。

腳本的進度訊息會寫到 stderr，最後 JSON 結果會寫到 stdout，方便其他工具解析。

### `scripts/cloud-list-brandname-clouds.sh`

查詢 Account Manager staging 目前有哪些 brand cloud。腳本會使用 staging platform-admin 帳密登入，呼叫唯讀的 Account Manager admin API，不會修改資料。

用法：

```sh
# 顯示數量與摘要表格
scripts/cloud-list-brandname-clouds.sh --env-root cloud_env/staging

# 查詢特定 brandname
scripts/cloud-list-brandname-clouds.sh --env-root cloud_env/staging --brandname RTK

# 輸出完整 JSON，包含每個 brand cloud 的 metadata 等設定
scripts/cloud-list-brandname-clouds.sh --env-root cloud_env/staging --json
```

常用選項：

- `--workspace PATH`：指定 workspace 根目錄。
- `--env-root PATH`：指定 environment directory；必填，避免誤查到錯誤環境。可傳 `cloud_env/staging`，script 會自動使用其下的 `linode/`。
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
