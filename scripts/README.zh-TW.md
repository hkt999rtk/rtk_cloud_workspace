# scripts 目錄說明

這個目錄放的是 workspace 層級的操作腳本，主要用途包含文件檢查、部署證據收集、Linode staging provision/deploy、brand cloud 建立，以及 GitHub Actions self-hosted runner 管理。

除非特別註明，以下指令都建議從 workspace 根目錄執行。

## Cloud Environment Root

Linode staging scripts 預設使用本機、git ignored 的 `cloud_env/staging/linode` 作為實際 Linode environment root。操作時可用 `--env-root cloud_env/staging` 指定 staging environment directory；script 會自動解析到 `cloud_env/staging/linode`。這個目錄集中保存 operator env、topology、service env、state、keys/certificates、device fixtures、artifacts 與 backups。

可用 `--env-root PATH` 指向另一份 environment directory。舊的 `--secrets-root PATH` 仍保留為相容 alias，但新的操作與文件都應使用 `--env-root`。

`cloud-*` 是目前正式入口。舊的 `staging-*` / `staging_*` 相容 wrapper 已移除；automation 與文件都應使用 `cloud-*` 名稱。

目錄配置請見 `docs/cloud-env-layout.zh-TW.md`。

## Runtime 依賴政策

`scripts/` 內的正式操作腳本禁止依賴 Python。不要在這些腳本中新增 `python`、`python3`、`pip`、`boto3`、`conda`、inline Python heredoc，或要求操作者建立 Python/conda 環境。

需要 JSON/YAML、Object Storage、TLS、MQTT、HTTP API 等較複雜邏輯時，優先新增或擴充 Go module/Go helper，再由 shell wrapper 呼叫 Go 工具。簡單文字處理可用 POSIX shell、awk、sed、jq、openssl 等既有 CLI。

## 一般檢查與同步

### `go run ./scripts/go/rtk-cloud -- status-all`

顯示 workspace 與所有 submodule 的 Git 狀態和最後一個 commit。

用法：

```sh
go run ./scripts/go/rtk-cloud -- status-all
```

適合在切 branch、pull、submodule update 前後確認整體狀態。

### `go run ./scripts/go/rtk-cloud -- sync-all`

同步 workspace 與 submodule remote 資訊，並初始化/更新 submodule 到目前 superproject 釘住的 commit。它只 fetch remote，不會改變 superproject 記錄的 submodule commit。

用法：

```sh
go run ./scripts/go/rtk-cloud -- sync-all
```

### `go run ./scripts/go/rtk-cloud -- test-matrix`

執行 workspace 快速驗證矩陣，包括 workspace 狀態、submodule 狀態，以及 Go-based workspace checks。

用法：

```sh
go run ./scripts/go/rtk-cloud -- test-matrix
```

### `go run ./scripts/go/rtk-cloud -- docs-check`

檢查 workspace 文件入口、重要 runbook、e2e 測試目錄、submodule 文件，以及 contracts submodule 是否對齊。

用法：

```sh
go run ./scripts/go/rtk-cloud -- docs-check
```

### `go run ./scripts/go/rtk-cloud -- secrets-check`

檢查 `.secrets` 等敏感路徑是否被 git ignore，並掃描 tracked workspace 檔案，避免誤提交 private key、token、password、DSN 等敏感字串。

用法：

```sh
go run ./scripts/go/rtk-cloud -- secrets-check
```

## 證據與報告

### `go run ./scripts/go/rtk-cloud -- collect-evidence`

收集 private cloud / product readiness 相關證據，輸出到 `evidence/` 或指定目錄。腳本會產生 manifest、服務 commit 狀態、健康檢查、報告摘要，並對敏感資訊做 redaction。

常用用法：

```sh
go run ./scripts/go/rtk-cloud -- collect-evidence
```

可用環境變數：

```sh
RTK_EVIDENCE_ENVIRONMENT=evaluation \
RTK_EVIDENCE_OUTPUT_DIR=./evidence \
RTK_EVIDENCE_RUN_SERVICE_COLLECTORS=0 \
RTK_EVIDENCE_TARBALL=1 \
go run ./scripts/go/rtk-cloud -- collect-evidence
```

## Linode cloud environment 操作

### `go run ./scripts/go/rtk-cloud -- generate-load-devices`

依照量產流程產生 staging/load-test 用的 device 身分。每台 device 會先在本機產生 private key 與 CSR，然後預設呼叫真的 factory enrollment API，讓 server 簽發 client certificate 並寫入 entitlement。這模擬「沒有真實 security chip、但量產 enroll 流程是真的」的 loading test 情境。

metadata 會同時記錄 inventory 用的 `device_type` 與 ACL 用的 `service_options`；`device_type` 不作為 ACL 來源。script 會針對每一台 device 印出 `enroll start` / `enroll ok` / `enroll failed`，並把逐台結果寫到 `manifests/factory-enroll-results.jsonl`。

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
go run ./scripts/go/rtk-cloud -- generate-load-devices --env-root cloud_env/staging

# 指定數量與配比
go run ./scripts/go/rtk-cloud -- generate-load-devices \
  --env-root cloud_env/staging \
  --count 200 \
  --mix camera=80,light=50,air_conditioner=40,smart_meter=30

# 指定輸出目錄；若目錄已存在，用 --force 重建
go run ./scripts/go/rtk-cloud -- generate-load-devices \
  --env-root cloud_env/staging \
  --out-dir cloud_env/staging/linode/devices/manual \
  --force

# 只做離線 key/cert 材料產生，不呼叫 server；測試 script 本身時使用
go run ./scripts/go/rtk-cloud -- generate-load-devices \
  --env-root cloud_env/staging \
  --generate-only
```

常用選項：

- `--count N`：產生 device 數量，預設 `100`。
- `--mix SPEC`：類型權重，例如 `camera=40,light=25,air_conditioner=20,smart_meter=15`。
- `--prefix PREFIX`：device id prefix，預設 `load-device`，輸出如 `load-device-0001`。
- `--env-root PATH`：指定 environment directory；必填。可傳 `cloud_env/staging`，script 會自動使用其下的 `linode/`。
- `--out-dir PATH`：輸出目錄，預設 `cloud_env/staging/linode/devices/test_device`。
- `--factory-url URL` / `FACTORY_ENROLL_URL`：覆寫 factory enrollment API base URL；預設從 env-root 的 `FACTORY_ENROLL_URL` 讀取，沒有時用 `VIDEO_CLOUD_DOMAIN` 推導 `https://<domain>`。
- `--factory-auth-key KEY` / `FACTORY_ENROLL_AUTH_KEY`：覆寫 factory enrollment HMAC key；預設從 env-root 的 video-cloud service env 讀取。
- `--factory-id`、`--line-id`、`--station-id`、`--fixture-id`、`--operator-id`、`--batch-id`：送到 factory enroll request 的量產欄位。
- `--generate-only`：只用本地 simulation CA 簽發憑證，不寫入 cloud database。
- `--force`：移除既有輸出目錄後重建。

`go run ./scripts/go/rtk-cloud -- provision --preflight` / `--apply` / `--deploy` 會在 video-cloud service env 缺少 factory enrollment 設定時補齊 `FACTORY_ENROLL_URL`、`FACTORY_ENROLL_AUTH_KEY`、audit log path，以及 factory enrollment bridge 呼叫 certissuer 所需的 client cert/key/CA source path。補齊後需要重新 deploy Video Cloud，server 端才會使用同一組 key。

重要輸出：

- `summary.json`：本次產生的數量、配比與主要路徑。
- `manifests/devices.json`：完整 device inventory。
- `manifests/devices.csv`：簡表。
- `manifests/device_ids.txt`：load test 可用的 device id 清單。
- `manifests/factory-enroll-results.jsonl`：逐台 enroll 狀態；失敗時用這個檔案對照 log。
- `loadtest.env`：可 `source` 的 load test 參數，不包含 bearer token。

正常 factory enroll 成功後，cloud database 預期可在 `video_cloud.factory_device_entitlements` 找到每台 device 的 `device_id`、`factory_id`、`serial_number`、`certificate_serial`、`certificate_sha256`、`csr_sha256`、`entitlement_state`、`metadata`，以及 storage 欄位 `allowed_services`（內容為 canonical `service_options`），並在 `video_cloud.cert_issue_requests` 找到每次簽發 request 的 `request_status=succeeded`、`signed_serial`、`cert_sha256` 與憑證 PEM。`video_cloud.devices` 通常要等後續 activation/claim/runtime inventory 流程才會出現；不要用它判斷 factory enrollment 是否成功。

100 台預設配比的 staging 驗證重點：

```sql
SELECT count(*)
FROM factory_device_entitlements
WHERE device_id LIKE 'load-device-%';

SELECT count(*)
FROM cert_issue_requests
WHERE device_id LIKE 'load-device-%';

SELECT metadata->>'device_type' AS device_type,
       allowed_services::text AS service_options,
       count(*)
FROM factory_device_entitlements
WHERE device_id LIKE 'load-device-%'
GROUP BY metadata->>'device_type', allowed_services
ORDER BY device_type, service_options;
```

預期結果是 `factory_device_entitlements=100`、`cert_issue_requests=100`、全部 `entitlement_state=active`、全部 cert 欄位非空、缺號為 `0`；預設配比應為 camera `40` 台帶 `["mqtt", "video_storage", "video_streaming"]`，light `25`、air_conditioner `20`、smart_meter `15` 台各帶 `["mqtt"]`。

輸出的 private key 與 `--generate-only` 的 CA key 預設位於 git ignored 的 `cloud_env/staging/linode/devices/test_device`，不可 commit，也不可用在 production 或 customer environment。若要重建既有輸出，使用 `--force`。

### `go run ./scripts/go/rtk-cloud -- unprovision-devices`

依照前一次 `go run ./scripts/go/rtk-cloud -- bind-devices` 產生的 redacted bind artifact，呼叫 Account Manager user-facing unprovision API，釋放 device 的 user/org binding，讓正常 device 回到可轉售或重新 onboarding 的狀態。這個 script 只走 Account Manager API，不 SSH 到遠端主機、不接觸 raw Claim Token、不撤銷 factory certificate，也不操作 Video Cloud denylist。

預設會讀取最新的 `cloud_env/staging/linode/artifacts/device-bind/<brand>-device-bind-*.json`，並使用 artifact 內記錄的 `inputs.users_file` 登入原 assigned user 呼叫：

```sh
go run ./scripts/go/rtk-cloud -- unprovision-devices \
  --env-root cloud_env/staging \
  --brandname RTK
```

常用選項：

- `--bind-artifact FILE`：指定要解除綁定的 device bind artifact。
- `--count N`：只處理 artifact 前 N 台 device。
- `--dry-run`：只輸出將呼叫的 account device 清單，不登入、不呼叫 API、不寫 artifact。

重要輸出：

- stdout：redacted summary，包含 `action=unprovisioned`、`count`、`unprovisioned`、`artifact_file`。
- `artifacts/device-unprovision/<brand>-device-unprovision-<timestamp>.json`：redacted unprovision artifact，包含原 device id、account device id、user email、service options、unprovision status 與時間。不包含 password、bearer token、raw Claim Token 或 device private material。

### `go run ./scripts/go/rtk-cloud -- migrate-env`

將目前分散在 `.secrets/`、`keys/`、以及各 submodule deploy 目錄的 staging local environment 檔案複製到 `cloud_env/staging/linode`。來源檔案會保留，並在 `cloud_env/staging/linode/backups/migration-<timestamp>` 產生 backup 與 migration manifest。

用法：

```sh
go run ./scripts/go/rtk-cloud -- migrate-env --env-root cloud_env/staging
go run ./scripts/go/rtk-cloud -- migrate-env --env-root cloud_env/staging --force
```

常用選項：

- `--env-root PATH`：指定 environment directory；必填。可傳 `cloud_env/staging`，script 會自動使用其下的 `linode/`。
- `--force`：覆蓋已存在的 target 檔案。

### `go run ./scripts/go/rtk-cloud -- provision`

Linode staging 的主要編排腳本。它可以做 preflight、plan、reset、apply、DNS、deploy、artifact collection、e2e smoke。預設不變更環境，只做 `--plan`。

service logging 的目標 provisioning model 記在 `docs/service-logging-architecture.md`：logger backend 要在 application services 前 provision，然後每台 VM 安裝 journald forwarder。private-cloud v1 需要 Loki 作為集中 log storage/query backend；dashboard 由 Cloud Admin 查 Loki query API 或 workspace/logger query adapter，不需要 Grafana。forwarder 或 logger backend degraded 時，不應阻塞 account/video/admin/frontend service 啟動；readiness report 會標示 `logging: degraded`。

`cloud_env/<env>/linode/env/stack.env` 可設定 logger backend 與 forwarder 的環境 metadata：

- `CLOUD_LOGGER_LINODE_LABEL` / `CLOUD_LOGGER_LINODE_FIREWALL_LABEL`：logger backend VM 與 firewall label。
- `CLOUD_LOGGER_DOMAIN`：logger backend ingest/query endpoint domain。
- `CLOUD_LOGGER_FORWARDER_TARGETS`：plan 中列出的 forwarder target，預設包含 edge/api/infra/mqtt/coturn/account-manager/cloud-admin/frontend/non-Go host sources。
- `CLOUD_LOGGER_JOURNALD_SYSTEM_MAX_USE`、`CLOUD_LOGGER_JOURNALD_SYSTEM_KEEP_FREE`、`CLOUD_LOGGER_JOURNALD_MAX_RETENTION_SEC`：journald retention guidance，會傳給 forwarder install hook。
- `CLOUD_LOGGER_EMQX_VERBOSE_TRACE`：設成 `true` 時，`deploy` 會在 MQTT host 額外安裝 `rtk-cloud-emqx-log-forwarder.service`，從 `video-cloud-emqx` Docker logs 轉送 broker-side verbose trace；預設關閉，避免高流量 MQTT message trace 直接進 Loki。
- `CLOUD_SERVICE_LOG_LEVEL`：staging application service 預設 log level，合法值為 `debug`、`info`、`warn`、`error`，預設 `info`。大量測試太囉唆時可用 `warn` 降噪。
- `VIDEO_CLOUD_LOG_LEVEL`、`ACCOUNT_MANAGER_LOG_LEVEL`、`CLOUD_ADMIN_LOG_LEVEL`：各 service override；未設定時使用 `CLOUD_SERVICE_LOG_LEVEL`。

目前 staging centralized logger 已納入 native flow：`provision --plan/--all` 會處理 logger VM/firewall/DNS/env/state，artifact/cleanup 會納入 logger 並 redacted token；`provision --apply` 不安裝 runtime service。`deploy` 會先 best-effort 在 logger VM 安裝 Loki 與 `rtk-cloud-logger` backend systemd service，再 best-effort 在 service hosts 安裝 `rtk-cloud-log-forwarder`，即使後續 Video Cloud、Account Manager 或 Cloud Admin deploy 失敗，也會保留 logger readiness evidence。`deploy --logger-only` 可只重跑 logger backend、forwarder install 與 readiness，不部署 application services；full deploy 會在 application deploy 後再 refresh 一次 forwarder。readiness 會檢查 backend health、ingest/idempotency、sample query 與每台 forwarder status；`CLOUD_LOGGER_SCRIPT` 保留為 override/debug hook。logger degraded 時不會阻塞服務 deploy，但 readiness 必須標示 `logging: degraded`。Cloud Admin v1 dashboard 不依賴 Grafana。

目前 native forwarder 的 v1 預設範圍是 journald systemd units；target 必須對應實際 staging unit，例如 `video_cloud-api.service`、`video_cloud-logingester.service`、`nats-server.service`、`video_cloud-turnregistrar.service`。EMQX broker 的 per-publish / per-subscribe detail 不一定會進 journald，因為 `video_cloud-emqx.service` 是 Docker Compose oneshot wrapper；`CLOUD_LOGGER_EMQX_VERBOSE_TRACE=true` 會在 MQTT host 額外安裝 Docker-log forwarder，轉送 `video-cloud-emqx` broker trace。`./stg.sh mqtt` 仍會寫入 operator-side `workspace-mqtt-test` trace event，可用來確認 centralized logger ingest/query path。

broker-side 每筆 publish/subscribe trace 必須是 opt-in verbose mode。預設不開，避免大量 MQTT traffic 直接灌進 Loki。開啟後，readiness report 會顯示 `logger-forwarder:emqx-broker-trace`，broker event 會標成 `service=emqx-broker`、`source=emqx`、`component=mqtt-broker`、`operation_id=mqtt-broker-trace`。forwarder 會 redacted token/secret/payload-like content，不會把 logger token 或完整 MQTT payload 寫到 stdout/report/artifact。

常用用法：

```sh
# 只看目前狀態與預期資源，不做變更
go run ./scripts/go/rtk-cloud -- provision --env-root cloud_env/staging

# 檢查工具、env、credential、SSH key、release artifact
go run ./scripts/go/rtk-cloud -- provision --env-root cloud_env/staging --preflight

# 建立/更新 staging VM、DNS、部署三個服務、收集 artifacts、跑 e2e
go run ./scripts/go/rtk-cloud -- provision \
  --env-root cloud_env/staging \
  --all \
  --video-release VIDEO_RELEASE \
  --account-release ACCOUNT_RELEASE \
  --admin-release ADMIN_RELEASE

# 先刪除 staging VM/firewall/VPC，再重建與部署
go run ./scripts/go/rtk-cloud -- provision \
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

HTTPS certificate cache 由 `go run ./scripts/go/rtk-cloud -- deploy` 處理：如果 `cloud_env/staging/linode/certificates/<fqdn>/fullchain.pem` 與 `privkey.pem` 存在且在安全期限內未過期，就會先上傳到新 VM，建立 certbot lineage/renewal config、啟用 `certbot.timer`，並跳過新憑證申請；如果沒有可用 cache，deploy 仍會走 certbot，成功後再把 VM 上的 certificate/key 拉回 `cloud_env`。預設要求 certificate 至少還有 7 天有效期，可用 `--cert-cache-min-valid-seconds` 調整。

`--plan` 會顯示 logger backend、logger env/state、forwarder credentials、每台 host 的 forwarder targets、journald retention guidance，以及 backend/forwarder/sample trace readiness evidence 項目。


### `go run ./scripts/go/rtk-cloud -- staging-e2e-test`

Linode staging 一站式整合測試編排腳本。它把 remove VM、provision all、建立 RTK brand cloud、建立測試 users、產生並 factory-enroll devices、device bind/provision、bulk bind validation，以及 home MQTT simulation 串成單一流程，最後輸出 sanitized `summary.json` 與 `TEST_REPORT.md`。

預設是 safe plan，不會刪 VM、不會 provision、不會呼叫 API：

```sh
go run ./scripts/go/rtk-cloud -- staging-e2e-test --env-root cloud_env/staging --plan
```

真正執行完整 staging reset + E2E 需要顯式 `--run`，且 `--confirm` 必須等於 env root 內的 `CLOUD_STACK_NAME`：

```sh
go run ./scripts/go/rtk-cloud -- staging-e2e-test \
  --env-root cloud_env/staging \
  --run \
  --confirm video-cloud-stg-0529 \
  --brandname RTK \
  --user-count 10 \
  --device-count 100 \
  --device-mix camera=40,light=25,air_conditioner=20,smart_meter=15
```

常用選項：

- `--env-root PATH`：指定 environment directory；必填。可傳 `cloud_env/staging`，script 會自動使用其下的 `linode/`。
- `--plan`：只列出將執行的步驟，預設模式。
- `--run --confirm STACK`：執行完整流程。`STACK` 必須符合 `CLOUD_STACK_NAME`，避免刪錯 staging stack。
- `--skip-remove`：不先執行 `rtk-cloud remove-all-vm`，直接走 `rtk-cloud provision --reset-and-all` 後續流程。
- `--video-release` / `--account-release` / `--admin-release`：轉傳給 `rtk-cloud provision` 使用指定 release artifact。
- `--out-dir PATH`：指定報告輸出目錄；預設在 `<env-root>/artifacts/staging-e2e/<timestamp>/`。
- `--skip-mqtt-probe`：略過 live MQTT E2E；這會讓 MQTT 子測試回報 `BLOCKED`，不會當作 PASS。

輸出：

- `summary.json`：整體結果、stack、brand、redacted artifact path，以及每個 step 的狀態、exit code、耗時和 log path。
- `TEST_REPORT.md`：人工閱讀用測試報告。
- `logs/*.log`：各步驟 stdout/stderr。這些 log 留在 git ignored cloud env artifacts；提交或分享前仍應視為 operator artifact 審查。

報告檔會掃描常見敏感字串，避免 password、bearer token、private key 或服務 secret 被寫入 summary/report。完整 per-step log 不會自動清洗，不應 commit。Production-like APP actor 不能只做 Account Manager login；第一次 login 後如果回傳 `app_certificate.status=csr_required`，要模擬 app 本機產生 private key 與 subject `app-user:<user_id>` 的 CSR，透過 Account Manager 交給 certissuer 簽發 app certificate，pin certificate identity，並用該 certificate 透過 mutual TLS 呼叫 Video Cloud `POST /request_token` 換 subject-bound `app` token，後續 APP command/subscribe 才能用 token 執行。MQTT 子測試使用 actor-separated IoT 模式：device actor 用 device certificate/key 透過 mutual TLS 呼叫 Video Cloud `POST /request_token` 換 `device` token，再用 token credential 連 broker；app actor 用 users artifact 內的 app private key 與 app certificate 透過 mutual TLS 呼叫 `POST /request_token` 換指定 device 的 `app` token，再用 token credential 連 broker。Telemetry path 由 app observer 先訂閱 `devices/<device_id>/up/messages`，device 再 publish sample home-device envelope，只有 app observer 收到 matching `message_id` 才算 PASS。Command path 由 device 先訂閱 `devices/<device_id>/down/commands`，app controller publish AWS IoT Shadow-style command envelope，以 `payload.clientToken` 對應 `command_id`，並用 `payload.state.desired` 表示 desired state，例如 smart light `power=true`、air conditioner `mode=cool/target_temperature_c=24/fan=auto`；device 收到 matching `command_id` 後 publish `command_result` ack 到 `devices/<device_id>/up/messages`，ack 以 `payload.state.reported` 回報 device 已接受的狀態，只有 app observer 收到 matching ack 才算 PASS。broker-side per-message log 目前不是 PASS 來源；PASS 來源是分離 MQTT connections 的 publish/subscribe/receive 證據。只做 local artifact 檢查、或同一 client 自己 publish 後自己收到，不能算通過。

可用 `./stg.sh mqtt-report RTK` 從最新 RTK `home-mqtt-loadtest` artifact 產出獨立 trace chain 報告 `E2E_TRACE_CHAIN_REPORT.md`。若要指定特定 run，可用 `./stg.sh raw mqtt-trace-report --results-file <results.json>`；預設輸出到該 `results.json` 同目錄。報告只列 actor、action、topic、status 與 sanitized detail，不包含 token、private key、CSR 或 certificate PEM。

`./stg.sh mqtt RTK` 的 console runtime trace 可用 `--trace-detail` 控制：`summary` 是預設，只顯示 publish/receive 與資料摘要；`full` 顯示 token/connect/subscribe/publish/receive 全鏈條；`none` 關閉 console trace。資料摘要包含 timestamp、actor、topic、message_type、message_id、command_id、device_id、payload action/status，以及 selected `desired.*` / `reported.*` state 欄位，不輸出 payload body、`clientToken` 或 credential material。

### `go run ./scripts/go/rtk-cloud -- mqtt-loadtest`

10,000 MQTT-only device capacity test 編排指令。它是兩階段流程，不會自動建立或刪除 Linode VM：

1. `prepare`：建立或驗證 2,500 users、10,000 MQTT-only devices、device bind artifact 與 bind validation。
2. `run`：對已準備好的 fleet 執行 baseline shard load test。
3. `aggregate`：合併多個 shard 的 `results.json`，輸出總報告。

預設 baseline 對齊第一版 AWS cost/capacity 假設：2,500 users、每 user 4 devices、10,000 devices 100% MQTT connected，不含 camera/WebRTC/TURN/media。預設 mix 是 `light=3334,air_conditioner=3333,smart_meter=3333`。

先看 prepare plan：

```sh
go run ./scripts/go/rtk-cloud -- mqtt-loadtest prepare \
  --env-root cloud_env/staging \
  --brandname RTK \
  --plan
```

執行 prepare：

```sh
go run ./scripts/go/rtk-cloud -- mqtt-loadtest prepare \
  --env-root cloud_env/staging \
  --brandname RTK \
  --run
```

本機單 shard 執行：

```sh
go run ./scripts/go/rtk-cloud -- mqtt-loadtest run \
  --env-root cloud_env/staging \
  --brandname RTK \
  --shard-index 0 \
  --shard-count 1
```

多台 load-generator VM 執行時，`--hosts-file` 每行一個 SSH target；script 會一台 host 對應一個 shard，跑完後拉回各 shard `results.json` 並 aggregate：

```sh
go run ./scripts/go/rtk-cloud -- mqtt-loadtest run \
  --env-root cloud_env/staging \
  --brandname RTK \
  --hosts-file load-hosts.txt \
  --remote-workspace /root/rtk_cloud_workspace \
  --remote-env-root /root/rtk_cloud_workspace/cloud_env/staging/linode
```

如果 load-generator VM 尚未有 runner 和 env-root，可加 `--sync-remote`。這會透過 SSH 複製 `scripts/go` 和 env-root；env-root 內含 user artifact、device private key 和 certificate，load-generator VM 必須視為帶 secret 的測試基礎設施。

詳細 runbook 見 `docs/linode-10k-mqtt-loadtest.md`。

可用 `./stg.sh video RTK` 執行 staging WebRTC RTP relay smoke。這個測試只選最新 bind artifact 內具備 `video_streaming` service option 的 camera device，使用 device certificate mTLS 換 device token，使用 users artifact 內 app private key + app certificate mTLS 換 device-bound app token，然後重用 `e2e_test/video_cloud/load` runner。PASS 代表 device websocket owner online、viewer 建立 WebRTC session、server 回 SDP offer 與 ICE servers、device 送 SDP answer、ICE connected/completed、device 以 2s 1080p `testsrc2` Annex-B H.264 fixture loop 10 次送出 20s H.264 RTP，payload validation 看到 SPS/PPS/IDR/non-IDR NAL types，且 session close 成功。這不是 legacy raw RTP relay 測試；PASS 來源是 WebRTC signaling + H.264 RTP payload evidence。輸出在 `<env-root>/artifacts/video-relay-test/<timestamp>/results.json` 與 `TEST_REPORT.md`，console/report 會 redacted bearer token、TURN credential、private key、CSR 與 certificate PEM。

### `go run ./scripts/go/rtk-cloud -- deploy`

只做 staging deploy/verify，不負責建立 VM。它會先 best-effort 安裝與驗證 logger backend/forwarder，再部署與驗證 Video Cloud、Account Manager、Cloud Admin；失敗時會停止後續 application deploy 步驟並寫 readiness report。logger/forwarder degraded 不會阻塞 application deploy。

若 staging log 太囉唆，可在 deploy 時降低 application service log level：

```bash
CLOUD_SERVICE_LOG_LEVEL=warn ./stg.sh deploy
```

也可固定寫在 `cloud_env/staging/linode/env/stack.env`：

```bash
CLOUD_SERVICE_LOG_LEVEL=warn
VIDEO_CLOUD_LOG_LEVEL=info
```

`debug` 只建議短時間診斷使用；logger backend/forwarder 自身不受 `CLOUD_SERVICE_LOG_LEVEL` 影響。

用法：

```sh
go run ./scripts/go/rtk-cloud -- deploy \
  --env-root cloud_env/staging \
  --video-release VIDEO_RELEASE \
  --account-release ACCOUNT_RELEASE \
  --admin-release ADMIN_RELEASE
```

常用選項：

- `--admin-release-bundle PATH`：使用本機 Cloud Admin release bundle，不從 Object Storage 下載。
- `--env-root PATH`：指定 environment directory；必填，避免部署到錯誤環境。可傳 `cloud_env/staging`，script 會自動使用其下的 `linode/`。
- `--logger-only`：只安裝/更新 Loki、`rtk-cloud-logger.service` 與各 host 的 `rtk-cloud-log-forwarder.service`，並執行 logger readiness；不部署 Video Cloud、Account Manager 或 Cloud Admin。
- `--artifact-dir PATH`：指定 readiness report 和 logs 輸出目錄。
- `--cert-cache-root PATH`：指定 HTTPS certificate cache 根目錄，預設 `<env-root>/certificates`。
- `--cert-cache-min-valid-seconds N`：cached certificate 至少還要有效多久才可重用，預設 `604800` 秒。
- `--dns-ttl SECONDS`：傳給 Video Cloud deploy 的 GoDaddy DNS TTL。
- `--verbose`：輸出更多 debug 訊息。

### `go run ./scripts/go/rtk-cloud -- check-certificates`

檢查 staging 對外 HTTPS host 的 certificate 是否合法、hostname 是否符合、是否尚未過期，並確認剩餘效期高於指定門檻。預設會同時檢查 live HTTPS endpoint 與 `cloud_env` 內的 certificate cache。

檢查目標：

- `video-cloud-staging.<root-domain>`
- `certissuer.video-cloud-staging.<root-domain>`
- `account-manager.video-cloud-staging.<root-domain>`
- `admin.video-cloud-staging.<root-domain>`

用法：

```sh
# 檢查 live endpoint 與本機 certificate cache
go run ./scripts/go/rtk-cloud -- check-certificates --env-root cloud_env/staging

# 只檢查 cloud_env 裡的 certificate cache，不連線到 live host
go run ./scripts/go/rtk-cloud -- check-certificates --env-root cloud_env/staging --skip-live

# JSON 輸出，方便 CI 或 jq 使用
go run ./scripts/go/rtk-cloud -- check-certificates --env-root cloud_env/staging --json
```

常用選項：

- `--env-root PATH`：指定 environment directory；必填，避免檢查錯誤環境。可傳 `cloud_env/staging`，script 會自動使用其下的 `linode/`。
- `--dns-root-domain NAME`：root DNS domain，預設 `realtekconnect.com`。
- `--min-valid-days N`：要求 certificate 至少還要有效幾天，預設 `7`。
- `--skip-live`：只檢查 `cloud_env/staging/linode/certificates` 內的 cache。
- `--skip-cache`：只檢查 live HTTPS endpoint。
- `--json`：輸出完整 JSON 結果。

如果任一 certificate 缺失、過期、hostname 不符合、live chain 驗證失敗，或低於剩餘效期門檻，script 會顯示 `status=fail` 並以 non-zero exit code 結束。

### `go run ./scripts/go/rtk-cloud -- remove-all-vm`

刪除 Linode 上 label 含 `staging` 的 VM，等待 VM 消失後清除 staging firewalls 與 `video-cloud-staging-vpc`，並把 active local state 移到 `cloud_env/staging/linode/backups/remove-vm-<timestamp>`。這是破壞性操作，腳本會要求輸入 `yes` 才會送出刪除請求。

用法：

```sh
go run ./scripts/go/rtk-cloud -- remove-all-vm --env-root cloud_env/staging
```

需要明確指定 `--env-root`，避免刪到錯誤環境。可以傳 `cloud_env/staging`，script 會自動解析到其下的 `linode/`。需要 `LINODE_TOKEN`；通常 token 會放在 operator env 或 shell 環境。若 Linode 回報資源已不存在，script 會視為已清除並繼續。

### `go run ./scripts/go/rtk-cloud -- update-ssh-whitelist`

更新所有 staging VM firewall 的 SSH allowlist。預設會自動偵測目前這台操作機器對外的 public IPv4，並把它以 `/32` 加到 7 個 staging firewall 的 port 22 rule。互動執行時會詢問要 append 還是 replace；非互動執行未指定 `--mode` 時維持 append 相容行為：

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
go run ./scripts/go/rtk-cloud -- update-ssh-whitelist --env-root cloud_env/staging

# 手動指定 CIDR
go run ./scripts/go/rtk-cloud -- update-ssh-whitelist --env-root cloud_env/staging --cidr 203.0.113.10/32

# 只保留單一 SSH 入口，移除舊 SSH allowlist CIDR
go run ./scripts/go/rtk-cloud -- update-ssh-whitelist --env-root cloud_env/staging --mode replace

# 只看會更新哪些 firewall，不呼叫 Linode API 修改
go run ./scripts/go/rtk-cloud -- update-ssh-whitelist --env-root cloud_env/staging --mode replace --cidr 203.0.113.10/32 --dry-run
```

`--mode append` 只會加入 CIDR，不會移除既有白名單；`--mode replace` 會把 SSH port 22 allowlist 改成只剩指定 CIDR，不影響其他 inbound rules。成功更新 Linode firewall 後，也會同步更新本地 ignored staging config/env，避免之後重新 provision 時又回到舊白名單。

### `go run ./scripts/go/rtk-cloud -- create-brandname-cloud`

在 Account Manager staging 上建立 brand cloud。腳本會先確保 platform-admin bootstrap env 可用，再用 Account Manager API 建立 brand cloud；若 API 建立遇到已知 server error，會用 PostgreSQL fallback upsert，最後再透過 API 驗證結果。

用法：

```sh
go run ./scripts/go/rtk-cloud -- create-brandname-cloud --env-root cloud_env/staging --brandname RTK
```

常用選項：

- `--workspace PATH`：指定 workspace 根目錄。
- `--env-root PATH`：指定 environment directory；必填，避免建立到錯誤環境。可傳 `cloud_env/staging`，script 會自動使用其下的 `linode/`。
- `--skip-bootstrap`：不要更新/restart 遠端 Account Manager bootstrap admin env。

腳本的進度訊息會寫到 stderr，最後 JSON 結果會寫到 stdout，方便其他工具解析。

### `go run ./scripts/go/rtk-cloud -- list-brandname-clouds`

查詢 Account Manager staging 目前有哪些 brand cloud。腳本會使用 staging platform-admin 帳密登入，呼叫唯讀的 Account Manager admin API，不會修改資料。

用法：

```sh
# 顯示數量與摘要表格
go run ./scripts/go/rtk-cloud -- list-brandname-clouds --env-root cloud_env/staging

# 查詢特定 brandname
go run ./scripts/go/rtk-cloud -- list-brandname-clouds --env-root cloud_env/staging --brandname RTK

# 輸出完整 JSON，包含每個 brand cloud 的 metadata 等設定
go run ./scripts/go/rtk-cloud -- list-brandname-clouds --env-root cloud_env/staging --json
```

常用選項：

- `--workspace PATH`：指定 workspace 根目錄。
- `--env-root PATH`：指定 environment directory；必填，避免誤查到錯誤環境。可傳 `cloud_env/staging`，script 會自動使用其下的 `linode/`。
- `--brandname NAME`：只顯示 `name` 或 `metadata.brandname` 符合的 brand cloud。
- `--limit N`：指定 API list limit，預設 `200`。
- `--json`：輸出完整 API JSON，適合用 `jq` 進一步查詢。

預設摘要會顯示 `brand_clouds`、`api_total`、`id`、`name`、`status`、`tier`、`evaluation_device_quota`、`metadata.brandname`、`created_at` 與完整 `metadata`。若要確認「每個 brandname cloud 的內容設定」，建議使用 `--json`。

### `go run ./scripts/go/rtk-cloud -- create-users`

在既有 brand cloud 下透過 Account Manager platform-admin API 建立已啟用 user account，不走 signup/email verification，也不直接連 PostgreSQL。

用法：

```sh
go run ./scripts/go/rtk-cloud -- create-users --env-root cloud_env/staging --brandname RTK --count 10
```

常用選項：

- `--workspace PATH`：指定 workspace 根目錄。
- `--env-root PATH`：指定 environment directory；必填，避免誤建到錯誤環境。
- `--brandname NAME`：指定既有 brand cloud 名稱或 `metadata.brandname`。
- `--count N`：建立帳號數量，預設 `10`；email 會使用 `<brand>+001@users.local` 這種序號格式。
- `--role ROLE`：`owner`、`admin` 或 `member`，預設 `member`。
- `--rotate-password`：既有 user 也更新初始密碼；預設遇到既有 user 會失敗，避免產生不會生效的新 credentials artifact。若只是要重用既有帳號，請使用前一次成功產生的 users artifact。
- `--dry-run`：只列出將建立的 email，不呼叫建立 user API，也不寫 credentials。

腳本的進度訊息會寫到 stderr，stdout 只輸出 summary JSON，不包含密碼、private key、CSR 或 certificate PEM。初始密碼與 app-local bootstrap material 只寫入 `cloud_env/.../artifacts/users/<brand>-users-<timestamp>.json`，檔案權限為 `0600`。建立或重設密碼後，腳本會依文件模擬第一次 app login：先登入 Account Manager；若回傳 `app_certificate.status=csr_required`，就在本機產生 app private key 與 subject `app-user:<user_id>` 的 CSR，以 `app_csr_pem` 再登入一次，讓 Account Manager 透過 certissuer 簽發 app certificate 並寫入 database。artifact 會記錄每個 user 的密碼、app private key、CSR、certificate/chain、fingerprint、serial、issuer request id 與有效期，供後續 production-like app mTLS/token bootstrap 測試使用。如果 API 回報 user 已存在且未指定 `--rotate-password`，腳本會停止，不會寫新的 credentials artifact。若 user 已有有效 app certificate，腳本會從同 brand 既有 users artifact 重用本機 app key/CSR；找不到既有 key 時會停止，避免產生缺少 mTLS private key 的最新 artifact。

### `go run ./scripts/go/rtk-cloud -- bind-devices`

將已 factory-enrolled 的 device 透過 Account Manager API 綁定到 brand cloud user，並啟動 account-side device provisioning。這個腳本只走 API，不直接連 PostgreSQL；測試 possession proof 採一次性 Claim Token。

典型 staging 順序：

```sh
go run ./scripts/go/rtk-cloud -- create-users --env-root cloud_env/staging --brandname RTK --count 10

go run ./scripts/go/rtk-cloud -- generate-load-devices --env-root cloud_env/staging --count 100

go run ./scripts/go/rtk-cloud -- bind-devices \
  --env-root cloud_env/staging \
  --brandname RTK

go run ./scripts/go/rtk-cloud -- validate-device-bind \
  --bind-artifact cloud_env/staging/linode/artifacts/device-bind/rtk-device-bind-<timestamp>.json
```

流程：

- platform admin 建立每台 device 的 Claim Token，帶入 `category` 與 canonical `service_options`。
- 指派的 member user 登入 Account Manager。
- member user resolve Claim Token，完成 claim/bind。
- member user 呼叫 provision API，啟動同一種 device activation/provision operation。

分配策略會保留 device manifest 順序輸出，同時依 device type 分段輪轉 user，讓預設 100 devices / 10 users 每個 user 取得 10 台，且 camera 與 mqtt-only device 分布平均。

重跑政策是 fail-fast：如果 Account Manager 回報 already-claimed 或 already-bound，腳本會非 0 結束，不 skip、不 reclaim。operator 應改用新的 device fixture 或清楚處理環境狀態後再重跑。

輸出與 secret handling：

- stdout 只輸出 summary JSON，不包含密碼、bearer token、raw Claim Token、private key path。
- 完整 redacted artifact 寫到 `cloud_env/.../artifacts/device-bind/<brand>-device-bind-<timestamp>.json`，檔案權限為 `0600`。
- artifact 只包含 assigned email、device id/type、`service_options`、claim id、account device id、operation id 與 status。
- raw Claim Token、user password、bearer token 只存在 process 暫存檔，腳本結束會移除。
- 未指定 `--users-file` 時，命令會使用 `rtk-cloud create-users` 寫出的最新 `cloud_env/.../artifacts/users/<brand>-users-*.json`。
- 未指定 `--devices-dir` 時，命令會使用 `rtk-cloud generate-load-devices` 的預設輸出 `cloud_env/.../devices/test_device`。
- 未指定 `--count` 時，腳本會綁定 `manifests/devices.json` 內全部 devices。

常用選項：

- `--env-root PATH`：指定 environment directory；必填。
- `--brandname NAME`：指定既有 brand cloud。
- `--users-file FILE`：指定 `rtk-cloud create-users` 產生的 credentials artifact；未指定時使用同 brand 最新 artifact。
- `--devices-dir DIR`：指定 `rtk-cloud generate-load-devices` 產生的 device output directory；未指定時使用 `<env-root>/devices/test_device`。
- `--count N`：只綁定前 N 台 device；未指定時綁定 manifest 內全部 devices。
- `--dry-run`：只輸出 assignment plan，不呼叫 Account Manager API，也不寫 artifact。
- `--skip-bootstrap`：不要更新/restart 遠端 Account Manager bootstrap admin env。

### `go run ./scripts/go/rtk-cloud -- validate-device-bind`

驗證 `rtk-cloud bind-devices` 產生的 redacted artifact，作為 100 devices onboarding staging smoke 的 API-level 結果檢查。這個 profile 不要求 live video streaming 成功；它確認每筆 API claim/bind/provision 結果都有 account device id、provision operation id，且 `service_options` 符合 ACL 預期。

用法：

```sh
go run ./scripts/go/rtk-cloud -- validate-device-bind \
  --bind-artifact cloud_env/staging/linode/artifacts/device-bind/rtk-device-bind-<timestamp>.json \
  --expected-count 100 \
  --expected-devices-per-user 10
```

輸出：

- stdout summary JSON，包含 `overall`、`total_devices`、`users`、report path。
- JSON report：`bulk-bind-validation-results.json`。
- Markdown report：`bulk-bind-validation-report.md`。

預設 report directory 是 `.artifacts/e2e_test/provisioning/bulk_bind_validation/<timestamp>/`。報告只使用 redacted API-level identifiers，不包含 credential material 或 local key paths。

## Linode CI runner 管理

這些腳本位於 `scripts/linode-ci-runners/`，用來管理 repo-scoped GitHub Actions self-hosted runner VM。

### `rtk-cloud ci-runners` runner specs

共用設定檔，定義 shared Linux runner VM、runner name、目標 GitHub repo、Linode type、runner label。通常不直接執行，而是被其他 runner 腳本 `source`。

### `go run ./scripts/go/rtk-cloud -- ci-runners provision`

建立 shared Linode Linux runner VM、防火牆，並在同一台 VM 上註冊多個 repo-scoped GitHub Actions self-hosted runner。

用法：

```sh
go run ./scripts/go/rtk-cloud -- ci-runners provision
```

需要的設定通常來自：

- `.secrets/shared/linode/env/ci-runners.env`
- `.secrets/shared/github/env/runner-registration.env`

必要變數包含 `LINODE_TOKEN`、`GITHUB_TOKEN`、`CI_RUNNER_ALLOWED_SSH_CIDRS`、SSH key 路徑等。

### `go run ./scripts/go/rtk-cloud -- ci-runners power`

啟動、關閉或列出 shared runner VM 狀態。

用法：

```sh
go run ./scripts/go/rtk-cloud -- ci-runners power status
go run ./scripts/go/rtk-cloud -- ci-runners power start
go run ./scripts/go/rtk-cloud -- ci-runners power stop
```

### `go run ./scripts/go/rtk-cloud -- ci-runners wait-online`

等待 GitHub Actions runner 進入 online 狀態。常和 `go run ./scripts/go/rtk-cloud -- ci-runners power start` 搭配使用。

用法：

```sh
go run ./scripts/go/rtk-cloud -- ci-runners wait-online
```

可用環境變數：

- `CI_RUNNER_ONLINE_TIMEOUT_SECONDS`：等待 timeout，預設 900 秒。
- `CI_RUNNER_ONLINE_POLL_SECONDS`：輪詢間隔，預設 15 秒。

### `go run ./scripts/go/rtk-cloud -- ci-runners list`

依照 Go runner specs 列出 Account Manager、Cloud Admin、Cloud Frontend、Cloud Client、Cloud Logger repo 的 GitHub Actions self-hosted runner 狀態、busy 狀態與 labels。

用法：

```sh
go run ./scripts/go/rtk-cloud -- ci-runners list
```

需要已登入的 `gh`。

### `go run ./scripts/go/rtk-cloud -- ci-runners run-session`

完整 CI session 編排：啟動 runner VM、等待 runner online、可選擇 rerun 指定 GitHub Actions run、watch 到結束、封存 artifacts 到 Linode Object Storage，最後依 policy 關閉 VM。

用法：

```sh
go run ./scripts/go/rtk-cloud -- ci-runners run-session \
  --account-run-id RUN_ID \
  --admin-run-id RUN_ID \
  --frontend-run-id RUN_ID \
  --client-run-id RUN_ID \
  --logger-run-id RUN_ID
```

常用選項：

- `--rerun true|false`：是否先 rerun 指定 run，預設 true。
- `--shutdown-policy always|on-success|never`：何時關閉 runner VM，預設 always。
- `--smoke-only true`：只測試 VM start -> runner online -> shutdown，不需要 run id。

### `go run ./scripts/go/rtk-cloud -- ci-runners archive-artifacts`

下載某個 GitHub Actions run 的 artifacts，並上傳到 Linode Object Storage。

用法：

```sh
go run ./scripts/go/rtk-cloud -- ci-runners archive-artifacts \
  --repo hkt999rtk/rtk_video_cloud \
  --run-id RUN_ID
```

可加 `--prefix PREFIX` 指定 Object Storage prefix。需要 `gh`、`go`、`LINODE_OBJ_BUCKET`、`LINODE_OBJ_ENDPOINT`、`LINODE_OBJ_ACCESS_KEY_ID`、`LINODE_OBJ_SECRET_ACCESS_KEY`。
