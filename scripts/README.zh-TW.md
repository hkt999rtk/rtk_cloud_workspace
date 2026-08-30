# scripts 目錄說明

這個目錄放的是 workspace 層級的操作腳本，主要用途包含文件檢查、部署證據收集、Linode staging provision/deploy、brand cloud 建立，以及 GitHub Actions self-hosted runner 管理。

除非特別註明，以下指令都建議從 workspace 根目錄執行。

## Environment、Architecture 與 Adapter

`cloud_env/dev`、`cloud_env/staging`、`cloud_env/prod` 是 environment instances；
共用 Kubernetes intent 位於 `cloud_deploy/architectures/kubernetes`，LKE 實作位於
`cloud_deploy/adapters/lke`。Environment 透過 `deployment.env` 選擇 architecture
與 adapter，不再使用 `cloud_env/<env>/<provider>`。

正式操作入口是：

```sh
scripts/check-deployment-credentials.sh --environment staging
go run ./scripts/go/rtk-cloud -- deployment plan --environment staging
go run ./scripts/go/rtk-cloud -- deployment provision --environment staging --confirm video-cloud-staging
go run ./scripts/go/rtk-cloud -- deployment acceptance --environment staging
```

Environment rehearsal 一律使用同一入口：

```sh
scripts/deploy-environment.sh test \
  --environment dev \
  --confirm video-cloud-dev
```

把 `dev` 與 confirmation stack 換成 `staging` 或 `prod` 即可；script 本身不得
複製或分叉。`test` 在成功或失敗後都會清除該 stack 擁有的 cloud/DNS resources，
並保留 sanitized evidence。

Generated kubeconfig、services、secrets、devices 與 artifacts 位於 git-ignored 的
`cloud_env/<environment>/runtime/`。LKE cluster/pool/resource IDs 位於 adapter-private
runtime state；shared Kubernetes runtime 與 load test 只讀 normalized runtime。
若同名 cluster 已存在但 environment 缺少相符的 OpenBao/PostgreSQL operator state，
provision 會在 mutation 前停止，避免新 secrets 配上舊 PVC。

LKE adapter 負責 Linode LKE cluster discovery/create/kubeconfig；RTK workloads、
Namespace、Secret、Deployment、Service、Ingress、NetworkPolicy、rollout 與 E2E
orchestration 由共用 Kubernetes runtime 處理。`deploy` 需要 container image；
可以明確提供 `LKE_POSTGRES_IMAGE`、`LKE_VIDEO_CLOUD_IMAGE`、
`LKE_ACCOUNT_MANAGER_IMAGE`、`LKE_CLOUD_ADMIN_IMAGE`、`LKE_FRONTEND_IMAGE`、
`LKE_CLOUD_LOGGER_IMAGE`。
Service images 由各 service repo 的 release workflow 發布到 GHCR；
workspace 只解析 pinned submodule commit、驗證對應 image 是否存在，並輸出
後續 deploy/e2e 需要的 `LKE_*_IMAGE` mapping。Generic Kubernetes、
GCP/Azure/AWS 的 provider id 分別預留為 `k8s`、`gke`、`aks`、`eks`；目前只有
fail-fast adapter，會在任何 cloud API、DNS 或 state mutation 前停止。
現階段唯一應被 live 驗證的 Kubernetes provider 仍是 `lke`。

Private GHCR image pull 使用 environment SecretStore 中的 operator credentials：

```env
GHCR_PULL_USERNAME=你的 GitHub username
GHCR_PULL_TOKEN=具備 read:packages 的 GitHub token
GODADDY_KEY=GoDaddy API key
GODADDY_SECRET=GoDaddy API secret
LINODE_OBJ_ACCESS_KEY_ID=Object Storage access key
LINODE_OBJ_SECRET_ACCESS_KEY=Object Storage secret key
LINODE_OBJ_ENDPOINT=https://REGION.linodeobjects.com
LINODE_OBJ_BUCKET=artifact bucket name
```

`GHCR_PULL_TOKEN` 不得寫入 tracked `env/stack.env`、Git、PR 或 log。部署前
先執行統一的唯讀 credential preflight：

```sh
scripts/check-deployment-credentials.sh --environment staging
```

預設只讀取 `~/.config/rtk_cloud/<environment>/operator/env/` 的逐項 `0600` 檔案；
不接受 shared profile、`--env-file` 或 process environment 覆寫，缺少時直接 fail closed。
檢查內容會依 environment 自動包含 Linode profile/LKE read、
五個 service GHCR repository pull、GoDaddy domain read，以及啟用 clip direct
upload 時的 Object Storage inventory、limited-key scope、signed list 與
write/read/delete canary。任何一項失敗都回傳 non-zero；
`deployment provision`、`deployment test` 與 legacy `staging-provision` 也會在
建立 runtime 檔案、解析 image 或建立 cloud resource 前自動執行同一檢查。
檢查不會輸出 token/key 值，並把 redacted receipt 寫到 ignored runtime state。

如果唯一失敗是 configured Object Storage bucket 回應 HTTP 404，可明確要求
同一個 script 使用 Object Storage access key 建立該 bucket，並立即重做 signed
read validation：

```sh
scripts/check-deployment-credentials.sh \
  --environment staging \
  --create-missing-object-storage-bucket
```

此旗標只允許用於 `credentials-check`。預設檢查、`deployment provision` 與
`deployment test` 不會自動建立 bucket；建立失敗或建立後無法讀取仍回傳
non-zero，deployment 不會繼續。

Linode limited access key 可以建立新 bucket，但不會自動取得該 bucket 的內容
權限。如果 bucket 已存在而 signed read 回應 HTTP 403，可明確要求 script 使用
`LINODE_TOKEN` 建立一把只授權 configured bucket、權限為 `read_write` 的 replacement
limited key：

```sh
scripts/check-deployment-credentials.sh \
  --environment staging \
  --grant-object-storage-bucket-access
```

script 會先用 replacement key 完成 read/write/delete validation，成功後才以 `0600`
權限原子更新 environment profile 內的 `LINODE_MEDIA_OBJ_ACCESS_KEY_ID` 與
`LINODE_MEDIA_OBJ_SECRET_ACCESS_KEY`。驗證失敗時不會改寫 profile。舊 key 不會自動撤銷，
因為它可能仍供原本授權的 bucket 使用；後續需獨立確認無 consumer 後再 revoke。
bucket creation 與 access grant 是兩個明確操作，不允許在同一次 invocation 合併。

若要另外確認 Docker CLI login，可執行：

```sh
printf '%s' "$GHCR_PULL_TOKEN" | \
  docker login ghcr.io \
    --username "$GHCR_PULL_USERNAME" \
    --password-stdin
```

若放在上述 shared/environment profiles，deployment command 會自動讀取；不需要每次手動
`export`。完整 storage lifecycle 見 `docs/storage-credential-lifecycle.md`。GitHub Actions 則使用 repository secrets
`GHCR_PULL_USERNAME` 與 `GHCR_PULL_TOKEN`。

Kubernetes runtime manifest 的新增規則：非 secret YAML 優先放在
`scripts/go/rtk-cloud/templates/k8s/*.yaml.tmpl`，透過 Go template renderer
產生；共用 labels/selectors/namespace/imagePullSecret metadata 由 Go helper
提供。Secret 不應用 `fmt.Sprintf` 直接拼 YAML string；新 secret path 應建立
typed Kubernetes object，透過 JSON `kubectl apply -f -` 套用，並避免錯誤訊息
或測試 log 暴露 raw token/password。

Kubernetes workload 的新增規則：先更新 provider-neutral workload registry，
不要分別手寫 deployment list、service list、image validation、rollout target
或 Prometheus target。Registry 是 image env key、namespace、port、metrics
path、resource override prefix 與 rollout timeout 的來源；LKE 只是目前 live
validated provider，未來 GKE/AKS/EKS 應重用同一份 workload registry。

`.github/workflows/lke-image-artifacts.yml` 是 workspace 的 LKE image
manifest workflow。PR 會先跑不需要 secret 的 tooling validation；
`workflow_dispatch` 會 checkout pinned submodules、用 `sha-<12 char commit>`
規則解析各 service repo 應發布的 GHCR image、驗證 image manifest 存在，
並上傳 `lke-image-manifest.json` 與 `lke-image-env.sh`。workflow 需要 repo
secret `CI_RUNNER_GITHUB_WORK_KEY` 來讀取 `git@github.com-work:` private
submodules；產出的 `lke-image-env.sh` 可用來設定後續
`run-staging-e2e.sh` / `rtk-cloud provision --deploy` 需要的 `LKE_*_IMAGE`。

LKE Prometheus targets 由 workspace Go deployer 的 Kubernetes workload
registry 產生，不直接手寫 Prometheus `scrape_configs`。新增 Kubernetes
workload 或 exporter 時，必須在 workload metadata 宣告 metrics service、
namespace、port 與 `/metrics/prometheus` path；`provision --deploy` 會用這份 registry 產生
`video-cloud-prometheus-config`。第一版維持 workspace-managed Prometheus
ConfigMap，不導入 Prometheus Operator、ServiceMonitor 或 PodMonitor。
Redis/Valkey engine metrics 由 LKE 內建 `redis-exporter` Service 暴露，
Prometheus 會 scrape `redis-exporter.<platform namespace>:9121/metrics`。
Grafana 會部署在 observability namespace 作為 private `ClusterIP` dashboard
layer，讀取 internal Prometheus Service；它不會透過 `provision --dns` 建立
public hostname、Ingress 或 TLS SAN。Platform 管理員要從 Cloud Admin 的
Platform View 分頁透過 same-origin iframe 觀看 Grafana，Cloud Admin BFF 會
以 platform-admin session 保護 `/api/admin/grafana/*` proxy 路徑。

可用 `--env-root PATH` 指向另一份非敏感 environment runtime directory。`--secrets-root` 已移除；敏感資料根目錄用 `RTK_CLOUD_CONFIG_ROOT` 或 secrets 子命令的 `--config-root` 指定。

K8s staging 驗收入口是 `scripts/run-staging-e2e.sh` 與
`scripts/setup-staging-e2e-data.sh`；一般 environment 操作使用
`rtk-cloud ... --environment <name>`。舊 VM runtime shortcut 與
service-owned VM runtime path 不再是 active deployment 入口；歷史參考只看
`docs/linode-staging-deployment-snapshot.md`。

目錄配置請見 `docs/cloud-env-layout.zh-TW.md`。

### `go run ./scripts/go/rtk-cloud -- lke-resolve-images`

解析 workspace 目前 pinned submodule commits 對應的 service images，不建立
cluster、不套用 Kubernetes resources、不 build service images。這個 command
主要給 CI image manifest workflow 使用，也可本機手動產 image manifest：

```sh
go run ./scripts/go/rtk-cloud -- lke-resolve-images \
  --env-root cloud_env/staging/runtime \
  --owner hkt999rtk \
  --out .artifacts/lke-images/lke-image-manifest.json
```

輸出的 manifest 會包含 `LKE_POSTGRES_IMAGE`、`LKE_VIDEO_CLOUD_IMAGE`、
`LKE_ACCOUNT_MANAGER_IMAGE`、`LKE_CLOUD_ADMIN_IMAGE`、`LKE_FRONTEND_IMAGE`、
`LKE_CLOUD_LOGGER_IMAGE` mapping。`scripts/run-staging-e2e.sh` 在 LKE provider
且缺少任一 `LKE_*_IMAGE` 時會自動執行 image resolve，寫入
`<env-root>/artifacts/lke-images/<timestamp>/`，並同步 latest manifest 到
`<env-root>/artifacts/lke-images/lke-image-manifest.json`。手動 export
`LKE_*_IMAGE` 只作為 override。若 override 指向官方 GHCR 的 `sha-*` tag，
tag 必須精確對應目前 pinned submodule commit，且 resolver 仍會確認 image
存在；自訂開發 registry image 維持既有 override 相容性。

### `go run ./scripts/go/rtk-cloud -- lke-build-images`

Legacy helper。只保留 PostgreSQL staging image build/push 能力；service
images 不再由 workspace build。正常 LKE staging flow 應使用
`lke-resolve-images` 取得各 service repo 已發布的 GHCR images。

## Runtime 依賴政策

`scripts/` 內的正式操作腳本禁止依賴 Python。不要在這些腳本中新增 `python`、`python3`、`pip`、`boto3`、`conda`、inline Python heredoc，或要求操作者建立 Python/conda 環境。

需要 JSON/YAML、Object Storage、TLS、MQTT、HTTP API 等較複雜邏輯時，優先新增或擴充 Go module/Go helper，再由 shell wrapper 呼叫 Go 工具。簡單文字處理可用 POSIX shell、awk、sed、jq、openssl 等既有 CLI。

Linode Object Storage 的正式 artifact upload/download/verify 必須使用 Go helper。不要安裝或呼叫 `awscli`、`boto3`、`pip install awscli`、`aws s3 cp`，也不要在 shell 中用 inline Python 實作 SigV4 uploader；各 repo 的 CI/release/deploy workflow 應呼叫 repo-local `cmd/linode-object-storage` 或等效 Go 工具。

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

執行 workspace 快速基線驗證，包括 workspace 狀態、submodule 狀態、diff check，以及 Go-based workspace checks；不會執行所有 service 或 product E2E 測試。

用法：

```sh
(cd scripts/go && go run ./rtk-cloud -- test-matrix)
```

### `test-services`

執行各 service、SDK、frontend 與 repository tooling 的本地測試。可用
`--repo NAME` 只測指定 repository；Go 測試會自動使用 `GOWORK=off`，避免被
workspace 根目錄的 `go.work` 干擾。

```sh
(cd scripts/go && go run ./rtk-cloud -- test-services)
(cd scripts/go && go run ./rtk-cloud -- test-services --repo rtk_cloud_admin)
```

### `test-e2e`

執行 workspace-owned 的 deterministic E2E、MQTT harness 與 load-report tooling
tests。加上 `--scripts` 才會執行根目錄 staging script contract tests；其中
`no-deprecated-staging-wrappers.test.sh` 是獨立的 repository governance
migration gate，不會被視為 E2E 測試。

```sh
(cd scripts/go && go run ./rtk-cloud -- test-e2e)
(cd scripts/go && go run ./rtk-cloud -- test-e2e --scripts)
```

### `test-ui`

使用 Playwright + headless Chromium 驗證 UI 行為。預設啟動真正的 Cloud Admin
Go BFF 與 deterministic fixture upstream，測試 browser → BFF → backend contract，
每個案例成功或失敗都保留最終 viewport screenshot；失敗另保留 video、trace、
error context 與所有 retry artifacts。輸出 `results.json`、JUnit、
`evidence-manifest.json`、`TEST_REPORT.md` 與 HTML report。報告逐項記錄測試時間、
目的、手法、結果和 PASS/FAIL 評斷。Desktop `--full` 會自動執行並合併
unavailable、empty、stale、expired、partial-failure 與 lifecycle 等 fixture
phases，確保條件式案例不會只留下 SKIP。`--full` 執行完整 browser
suite；`--install` 先安裝 npm dependencies 與 Chromium。Desktop 與 mobile
是獨立 target，分別輸出 artifacts；未指定 target 時會依序執行兩者。

```sh
(cd scripts/go && go run ./rtk-cloud -- test-ui)
(cd scripts/go && go run ./rtk-cloud -- test-ui --desktop)
(cd scripts/go && go run ./rtk-cloud -- test-ui --mobile)
(cd scripts/go && go run ./rtk-cloud -- test-ui --full)
(cd scripts/go && go run ./rtk-cloud -- test-ui --desktop --run-id local-review-001)
(cd scripts/go && go run ./rtk-cloud -- test-ui --install)
```

對部署中的 staging backend 執行唯讀 UI E2E 時，需提供
`E2E_BASE_URL`、`E2E_PLATFORM_SESSION_ID`、`E2E_CUSTOMER_SESSION_ID`，並明確設定
`E2E_EVIDENCE_SAFE=1`，確認只會截取專用測試帳號與測試資料：

```sh
(cd scripts/go && go run ./rtk-cloud -- test-ui --staging)
```

### `test-catalog`

`tests/catalog.yaml` 是 Test ID、owner、目的、手法、source selector、target、
environment 與 evidence policy 的唯一來源。`check` 驗證編號、重複、來源、
Playwright 登錄與 generated Markdown drift；`render` 更新
`docs/test-catalog.md`。已發布 ID 不得換號或重用，移除時改成 `retired`。

```sh
(cd scripts/go && go run ./rtk-cloud -- test-catalog check)
(cd scripts/go && go run ./rtk-cloud -- test-catalog render)
```

### `test-live`

staging/live E2E 的明確入口。預設只輸出 plan，不會改動環境；執行 live flow
必須明確指定 `--run` 與正確的 `--confirm`。

```sh
(cd scripts/go && go run ./rtk-cloud -- test-live --environment staging --plan)
(cd scripts/go && go run ./rtk-cloud -- test-live --environment staging --run --confirm video-cloud-staging)
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
  --out-dir cloud_env/staging/runtime/devices/manual \
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
- `--env-root PATH`：指定 environment directory；必填。可傳 `cloud_env/staging`，script 會自動使用其下的 Kubernetes provider 子目錄，預設 `lke/`。
- `--out-dir PATH`：輸出目錄，預設 `cloud_env/staging/runtime/devices/test_device`。
- `--factory-url URL` / `FACTORY_ENROLL_URL`：覆寫 factory enrollment API base URL；預設從 env-root 的 `FACTORY_ENROLL_URL` 讀取，沒有時用 `VIDEO_CLOUD_DOMAIN` 推導 `https://<domain>`。
- `FACTORY_ENROLL_PRODUCTION_JWT`：正式 staging data setup 由 Account Manager production-run API 短暫注入；不得寫入 env-root、artifact 或 log。直接單獨執行此低階指令時，可由 operator 提供既有的短效 token。
- `--factory-auth-key KEY` / `FACTORY_ENROLL_AUTH_KEY`：只保留給尚未啟用 production JWT 的相容環境；production JWT 存在時不會使用 HMAC。
- `--factory-id`、`--line-id`、`--station-id`、`--fixture-id`、`--operator-id`、`--batch-id`：送到 factory enroll request 的量產欄位。
- `--generate-only`：只用本地 simulation CA 簽發憑證，不寫入 cloud database。
- `--force`：移除既有輸出目錄後重建。

K8s staging 的 factory enrollment verifier 設定由 K8s runtime secret/config 提供。E2E 會透過 K8s service query/port-forward 取得 `FACTORY_ENROLL_URL`，再由 Account Manager production-run API 簽發綁定 Brand Cloud、device item profile、quantity 與有效期的 JWT；測試 harness 不直接讀 signing secret 或自行簽 token。

重要輸出：

- `<env-root>/artifacts/test-data/<brand>-test-data.sqlite`：users/devices/device credentials/bindings 的 active source of truth，檔案權限固定 `0600`。
- `summary.json`：本次產生的數量、配比與主要路徑；這是 run evidence，不是 test-data source。
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

device private key、certificate、bundle PEM 會寫入 SQLite test-data DB；`--generate-only` 的 CA key 仍位於 git ignored 的 `cloud_env/staging/runtime/devices/test_device`，不可 commit，也不可用在 production 或 customer environment。若要重建既有輸出，使用 `--force`。

### `go run ./scripts/go/rtk-cloud -- unprovision-devices`

依照 SQLite test-data DB 內的 bindings，呼叫 Account Manager user-facing unprovision API，釋放 device 的 user/org binding，讓正常 device 回到可轉售或重新 onboarding 的狀態。這個 script 只走 Account Manager API，不 SSH 到遠端主機、不接觸 raw Claim Token、不撤銷 factory certificate，也不操作 Video Cloud denylist。

預設會讀取 `<env-root>/artifacts/test-data/<brand>-test-data.sqlite`，並使用 DB 內的 assigned user password/token 呼叫：

```sh
go run ./scripts/go/rtk-cloud -- unprovision-devices \
  --env-root cloud_env/staging \
  --brandname RTK
```

常用選項：

- `--bind-artifact FILE`：legacy 相容入口；active flow 使用 SQLite test-data DB。
- `--count N`：只處理 artifact 前 N 台 device。
- `--dry-run`：只輸出將呼叫的 account device 清單，不登入、不呼叫 API、不寫 artifact。

重要輸出：

- stdout：redacted summary，包含 `action=unprovisioned`、`count`、`unprovisioned`、`artifact_file`。
- `artifacts/device-unprovision/<brand>-device-unprovision-<timestamp>.json`：redacted unprovision artifact，包含原 device id、account device id、user email、service options、unprovision status 與時間。不包含 password、bearer token、raw Claim Token 或 device private material。

### `go run ./scripts/go/rtk-cloud -- migrate-env`

舊 VM staging env 匯入工具已退役。staging runtime 已改為 K8s-only，不再從 retired submodule runtime 目錄匯入 state/env。請使用 `sync-env` 維護 K8s/domain metadata，並讓 E2E 透過 K8s service query/port-forward 取得 runtime endpoint。

用法：

```sh
go run ./scripts/go/rtk-cloud -- migrate-env --env-root cloud_env/staging
# error: migrate-env is retired with the staging VM toolkit
```

常用選項：

- `--env-root PATH`：指定 environment directory；必填。可傳 `cloud_env/staging`，script 會依 provider 自動使用其下的 Kubernetes provider 子目錄，預設 `lke/`。
- `--force`：保留為相容 flag，但命令一律回報 retired。

### `go run ./scripts/go/rtk-cloud -- sync-env`

依照 `cloud_env/<env>/<provider>/env/stack.env` 內的 root metadata 產生
Kubernetes stack/domain metadata。`CLOUD_ENV_NAME` 是唯一 stack slug/root；
stack name、domain、K8s namespace/label metadata，以及存在的 service env
內相關 URL 都由它推演，不要手動分別修改。

`sync-env --check` 對 LKE/Kubernetes provider 不要求 legacy VM
`topology/video-cloud-staging.yaml`。`CLOUD_PROVIDER=linode` 僅保留為 retired
VM metadata 清理/檢查相容路徑，不是 active deployment provider。

Root inputs 固定為：

- `CLOUD_ENV_NAME`
- `CLOUD_PROVIDER`
- `CLOUD_REGION`
- `CLOUD_DNS_ROOT_DOMAIN`

以 `CLOUD_ENV_NAME=stg` 為例，generated naming 會包含
`CLOUD_STACK_NAME=video-cloud-stg` 與各 public/internal service domain。

用法：

```sh
go run ./scripts/go/rtk-cloud -- sync-env --env-root cloud_env/staging
go run ./scripts/go/rtk-cloud -- sync-env --env-root cloud_env/staging --check
```

`--check` 不改檔，只檢查 `stack.env` generated block 與存在的 service env
是否已和 root metadata 同步。staging runtime 是 K8s-only；若不同步請先執行
`sync-env --env-root ...`。

### `go run ./scripts/go/rtk-cloud -- provision-k8s`

Staging runtime 只支援 K8s。`provision-k8s` 不建立 retired VM runtime，也不部署
service-owned VM binary；它會取得 kubeconfig、確認 staging namespaces 存在，
並等待 deployment/statefulset rollout ready。唯一例外是 TURN data-plane：
coturn 由 workspace LKE flow 管理一台獨立最小 Linode VM，避免把 UDP/TCP
relay traffic 放進 Kubernetes Service/Ingress。

Provider-aware `provision`/`deploy` command 只 dispatch 到 Kubernetes provider。
`CLOUD_PROVIDER=linode` 會回報 retired VM runtime，不會呼叫 VM runtime hooks。

LKE `provision --apply` 預設會安裝 Kubernetes metrics-server 到 `kube-system`，版本由 `LKE_METRICS_SERVER_VERSION` 控制，預設 `v0.8.1`。metrics-server 提供 `metrics.k8s.io`，讓 `kubectl top nodes` / `kubectl top pods` 與 Kubernetes HPA resource metrics 可用；它不取代 Prometheus 的長期觀測、dashboard 或 alert 用途。

LKE `provision --deploy` 會部署 workspace-managed Prometheus 與 private
Grafana，也會在 platform namespace 部署 staging 內建 Valkey 與 Redis
exporter。常用環境變數：

- `LKE_REDIS_IMAGE`：Redis-compatible/Valkey image，預設 `valkey/valkey:8-alpine`。
- `LKE_REDIS_EXPORTER_IMAGE`：Redis exporter image，預設 `oliver006/redis_exporter:v1.74.0`。
- `LKE_REDIS_REQUEST_CPU` / `LKE_REDIS_REQUEST_MEMORY` / `LKE_REDIS_LIMIT_MEMORY`：
  Valkey pod 資源 override，預設 `100m` / `128Mi` / `512Mi`。
- `LKE_REDIS_EXPORTER_REQUEST_CPU` / `LKE_REDIS_EXPORTER_REQUEST_MEMORY` /
  `LKE_REDIS_EXPORTER_LIMIT_MEMORY`：Redis exporter 資源 override，預設
  `50m` / `64Mi` / `256Mi`。

Account Manager 會重用同一個 platform namespace 的 Valkey endpoint 作為
platform/developer、brand-cloud、end-user profile/auth read-through cache。LKE
產生的 `account-manager-runtime` secret 會設定 `ACCOUNT_MANAGER_USER_CACHE_ENABLED=true`、
`ACCOUNT_MANAGER_USER_CACHE_ADDR=redis.<platform namespace>.svc.cluster.local:6379`
與 `ACCOUNT_MANAGER_USER_CACHE_PREFIX=account_manager:user`。Postgres 仍是
user truth；Redis miss 或 Redis unavailable 時 API 會讀 Postgres 並在可用時
回填 Redis。此 cache 不設定 TTL，因此 direct DB repair 後需要用
Account Manager image 內的 `/app/rtk-account-manager-user-cache rebuild` 修復
platform users，或刪除 brand-cloud/end-user 相關 Redis key 讓下一次 query
回填。

- `LKE_GRAFANA_IMAGE`：Grafana image，預設 `grafana/grafana:13.0.2`。
- `LKE_GRAFANA_ADMIN_PASSWORD`：Grafana admin 密碼；未設定時由 runtime secret material 產生。
- `LKE_GRAFANA_PERSISTENCE=true`：啟用 Grafana PVC。預設使用 `emptyDir`，
  staging acceptance 不消耗 Linode block volume quota。
- `LKE_GRAFANA_STORAGE`：啟用 persistence 時的 Grafana PVC 大小，預設 `5Gi`。
- `CLOUD_ADMIN_GRAFANA_BASE_URL`：Cloud Admin 連到 Grafana 的 cluster-internal base URL，例如 `http://video-cloud-grafana.video-cloud-staging-observability.svc.cluster.local:3000`。
- `LKE_LOKI_IMAGE`：Loki image，預設 `grafana/loki:3.5.1`。
- `RTK_CLOUD_LOGGER_LOKI_URL`：cloud-logger 寫入的 Loki base URL；未設定時由
  LKE 指到 private `video-cloud-loki` service。cloud-logger 一律使用 Loki，
  不支援 in-process store fallback。

Grafana 第一版 dashboard 會優先呈現平台管理者關心的穩定度與流量：
targets down/up、每個 `brand_cloud_id` 的 MQTT publish/delivery rate 與
delivery/publish ratio、device online/offline/connected/attached、API request
rate/5xx/latency、runtime log queue/drop/write failures、cross-service backlog
與 dead letters、TURN registry active/expired nodes、blob capacity 與 clips
總量。MQTT `mqtt_brand_*` counters 只作 dashboard 用；billing source of truth
仍是 PostgreSQL `mqtt_usage_windows` ledger。

常用用法：

```sh
go run ./scripts/go/rtk-cloud -- provision-k8s \
  --env-root cloud_env/staging \
  --confirm video-cloud-staging

go run ./scripts/go/rtk-cloud -- deployment provision \
  --environment staging \
  --confirm video-cloud-staging
```

常用選項：

- `--workspace PATH`：指定 workspace 根目錄。
- `--env-root PATH`：指定 environment directory；必填。可傳 `cloud_env/staging`，script 會自動使用其下的 Kubernetes provider 子目錄，預設 `lke/`。
- `--confirm STACK`：必須符合 `CLOUD_STACK_NAME`，避免操作錯誤 stack。
- `--timeout DURATION`：K8s rollout 等待時間，預設 5 分鐘；也可用 `CLOUD_STAGING_E2E_K8S_ROLLOUT_TIMEOUT` 設定。

### `go run ./scripts/go/rtk-cloud -- provision --dns`

LKE public edge 目前已實作為 external HAProxy VM，不再使用 Linode
NodeBalancer。`--dns` / `staging-provision` 都走同一條 HAProxy edge 路徑：

1. 用 Helm 安裝或更新 `ingress-nginx` 到 `<stack>-ingress` namespace。
2. ingress-nginx controller service 使用 `NodePort`，不是 `LoadBalancer`；目前 HTTPS NodePort 預設為 `30443`。
3. 透過 shared certbot DNS-01 flow 與 environment 選擇的 GoDaddy/Route53 adapter 簽一張 staging multi-SAN certificate，寫入 Kubernetes TLS secret `video-cloud-staging-public-tls`。
4. 建立 ingress namespace 內的 ExternalName bridge services，讓 Ingress 合法轉到各 namespace 的 internal `ClusterIP` services。
5. 建立 HTTPS Ingress rules：
   - `video-cloud-staging.realtekconnect.com` -> `video-cloud-api`
   - `device.video-cloud-staging.realtekconnect.com` -> `video-cloud-api`
   - `certissuer.video-cloud-staging.realtekconnect.com` -> `certissuer`
   - `turnregistry.video-cloud-staging.realtekconnect.com` -> `video-cloud-turnregistry`
   - `account-manager.video-cloud-staging.realtekconnect.com` -> `account-manager`
   - `admin.video-cloud-staging.realtekconnect.com` -> `cloud-admin`
   - `frontend.video-cloud-staging.realtekconnect.com` -> `frontend`
6. provision 或更新 host-installed HAProxy edge VM；HAProxy 用 TCP mode 與 `balance roundrobin` 將 public `443/TCP` forward 到 ingress-nginx NodePort，將 `8883/TCP` round-robin forward 到三台 LKE node 的 EMQX/MQTT NodePort。
7. DNS adapter A records 指向 HAProxy edge VM public IP，包含
   `turnregistry.<VIDEO_CLOUD_DOMAIN>`；coturn VM registrar 會透過這個
   signed control-plane endpoint 註冊與 heartbeat。
8. provision 或更新 host-installed coturn VM。預設短名 `turn01`、Linode label
   `<stack>-turn01`，type `g6-nanode-1`，Ubuntu 24.04，host package +
   systemd，不跑 Docker。VM 同時安裝 `video-cloud-turnregistrar.service`，
   讓 K8s 內的 `video-cloud-turnregistry` 知道此 TURN node 的 public host、
   ports、region 與 heartbeat 狀態。
9. `turn.<VIDEO_CLOUD_DOMAIN>`
   或 `LKE_COTURN_DOMAIN` 指向 coturn VM public IP；不等待 NodeBalancer IP。
10. MQTT public exposure 使用 `NodePort`，不是 `LoadBalancer`；目前 MQTTS NodePort 預設為 `31883`，`mqtt` 預設是 3 pod EMQX StatefulSet cluster，使用 stable `mqtt-0..2` pod DNS 與 required pod anti-affinity 分散到三台 node。
11. 套用 default-deny ingress NetworkPolicy 與必要 allow rules。

必要輸入：

- `DNS_ADAPTER=godaddy` 時，`GODADDY_KEY` / `GODADDY_SECRET` 只從 `~/.config/rtk_cloud/<environment>/operator/env/` 讀取。
- `CLOUD_DNS_ROOT_DOMAIN`，staging 預設是 `realtekconnect.com`。
- `certbot` CLI，或用 `RTK_CLOUD_CERTBOT` 指到指定 binary。
- `helm` 與 `kubectl` 可操作目標 LKE cluster。
- `LKE_PUBLIC_EDGE_MODE=external-haproxy`，這是唯一支援的 public edge mode。
- HAProxy edge VM operator inputs，包含 VM label/region/type、SSH key，以及 `LKE_EDGE_HAPROXY_MAXCONN`。`maxconn` 預設從 `400000` 開始；100K MQTTS 經 TCP proxy 會接近 200K HAProxy-side sockets，還需要替 API `/request_token` 與 WebRTC signaling 保留 headroom，loading test 時再依 memory/FD/CPU 使用量調整。
- coturn VM operator inputs：`LKE_COTURN_VM_NAME` 預設 `turn01`，
  `LKE_COTURN_VM_LABEL` 預設 `<stack>-turn01`，`LKE_COTURN_VM_TYPE`
  預設 `g6-nanode-1`，`LKE_COTURN_DOMAIN` 預設
  `turn.<VIDEO_CLOUD_DOMAIN>`，`LKE_COTURN_MIN_PORT` / `LKE_COTURN_MAX_PORT`
  預設 `49152` / `49200`。目前 `LKE_COTURN_VM_COUNT` 固定為 `1`；
  `LKE_TURN_REGISTRY_PUBLIC_DOMAIN` 預設
  `turnregistry.<VIDEO_CLOUD_DOMAIN>`。未來多節點命名保留 `turn02`、
  `turn03` 這類短名。

常用驗證：

```sh
kubectl -n video-cloud-staging-ingress get svc ingress-nginx-controller
kubectl -n video-cloud-staging-video-cloud get svc mqtt-public
kubectl get ingress -A
dig +short A video-cloud-staging.realtekconnect.com @ns23.domaincontrol.com
dig +short A turnregistry.video-cloud-staging.realtekconnect.com @ns23.domaincontrol.com
nc -vz video-cloud-staging.realtekconnect.com 443
nc -vz video-cloud-staging.realtekconnect.com 8883
curl -fsS https://video-cloud-staging.realtekconnect.com/healthz
curl -fsS https://account-manager.video-cloud-staging.realtekconnect.com/v1/health
curl -fsS https://admin.video-cloud-staging.realtekconnect.com/healthz
```

HAProxy edge VM 不跑 Docker。它是 host package + systemd service，只做 L4
TCP passthrough；TLS/mTLS/SNI/HTTP routing 留在 K8s 內的 ingress-nginx /
EMQX / service pod。詳細 design contract 見
`docs/lke-external-haproxy-edge.md`。

Postgres、OpenBao、Prometheus 不會因 `--dns` 對外公開。MQTT v1 只公開
MQTTS `8883/TCP`，經 HAProxy TCP passthrough 到 EMQX/MQTT NodePort；TURN
由獨立 coturn VM 對外提供 `3478/UDP`、`3478/TCP` 與 relay UDP range。
Grafana 也不會因 `--dns` 對外公開；Platform 管理員從 Cloud Admin iframe
入口使用，operator debug 可用：

```sh
kubectl -n video-cloud-staging-observability port-forward svc/video-cloud-grafana 3000:3000
curl -fsS http://127.0.0.1:3000/api/health
```

Kubeconfig 來源順序：

- `CLOUD_STAGING_K8S_KUBECONFIG`
- `KUBECONFIG`
- 透過 `LINODE_TOKEN` 與 `CLOUD_STAGING_LKE_CLUSTER_ID` 下載 LKE kubeconfig
- 若未指定 cluster ID，使用 `CLOUD_STAGING_LKE_CLUSTER_LABEL`，預設 `<CLOUD_STACK_NAME>-lke`

### `go run ./scripts/go/rtk-cloud -- remove-k8s`

Low-level K8s reset compatibility helper。正式 operator 入口請使用
`scripts/reset-staging-k8s.sh` / `rtk-cloud staging-reset-k8s`。`remove-k8s`
只有在 `CLOUD_STAGING_E2E_K8S_DESTRUCTIVE_RESET=1` 且傳 `--yes` 時才會清除
staging K8s resources。預設會刪除 workload/service/config/secret/policy
等 runtime resources，但保留 namespace 與 PVC/PV/provider storage；
`--purge-storage` 才會先刪 namespace 內 PVC，再刪 namespace。

```sh
go run ./scripts/go/rtk-cloud -- remove-k8s --env-root cloud_env/staging --yes
```

### `scripts/destroy-linode-staging-resources.sh`

危險的 Linode staging teardown helper。預設只做 dry-run：它會列出將刪除的
LKE clusters、Linode instances、firewalls、VPCs，以及符合 stack/prefix 的
Object Storage buckets；沒有 `--yes` 與正確確認字串時不會送出 DELETE。

```sh
scripts/destroy-linode-staging-resources.sh --env-root cloud_env/staging/runtime
scripts/destroy-linode-staging-resources.sh --env-root cloud_env/staging/runtime --yes --confirm-text "destroy video-cloud-staging"
```

Object Storage buckets 會列出但預設略過。若確定 matched buckets 已清空且也要刪除，
才加 `--include-object-storage`；Linode API 會拒絕刪除非空 bucket。

Linode CSI 可能留下 Kubernetes `Retain` policy 造成的 unattached `pvc-*`
Block Storage volumes。這些 volume 也會列在 dry-run 裡，但預設一律 SKIP。
若要刪除，必須先從 dry-run 輸出複製精確的 Linode volume id，再同時傳
`--include-orphan-volumes` 與 `--orphan-volume-ids`。不要只靠 `pvc-*`
名稱批次刪除，避免誤刪其他環境或其他 cluster 的 Retained PVC。

```sh
scripts/destroy-linode-staging-resources.sh --env-root cloud_env/staging/runtime
scripts/destroy-linode-staging-resources.sh \
  --env-root cloud_env/staging/runtime \
  --yes \
  --confirm-text "destroy video-cloud-staging" \
  --include-orphan-volumes \
  --orphan-volume-ids 16311688,16303493
```

### Staging K8s lifecycle phases

正式 staging runtime 已拆成三個可獨立執行的 K8s lifecycle phase。Shell
檔只做 POSIX thin wrapper，實際邏輯在 Go command 內：

```sh
# 1. Reset K8s resources. Default preserves PV/PVC/provider storage.
scripts/reset-staging-k8s.sh --plan
scripts/reset-staging-k8s.sh --confirm video-cloud-staging

# Only when the data layer must be wiped too.
scripts/reset-staging-k8s.sh --confirm video-cloud-staging --purge-storage

# 2. Provision or update server workloads. LKE resolves missing GHCR images automatically.
scripts/provision-staging.sh --plan
scripts/provision-staging.sh --confirm video-cloud-staging

# 3. Acceptance only: create/update test users/devices and run smoke/MQTT/log verification.
scripts/run-staging-acceptance.sh --plan
scripts/run-staging-acceptance.sh --confirm video-cloud-staging
```

`staging-reset-k8s` 是 destructive phase，必須 `--confirm <CLOUD_STACK_NAME>`。
預設會清 staging K8s runtime resources 並讓後續 provision 重建 pods，
但不主動 purge storage；只有明確加 `--purge-storage` 才會清
PVC/PV/provider volume 類資料層。`staging-provision`
負責新安裝或停機升級：解析 image、套用 manifests、DNS/artifacts、rollout
readiness。`staging-acceptance` 不 reset、不 deploy，只驗證已部署好的 stack。

`staging-e2e-test` / `run-staging-e2e` 支援以 `--steps` 選擇要執行的階段，預設
`all` 會從 reset、provision、data setup、MQTT 流量、runtime log 到 billing
log/ledger 全部跑完。可單獨重跑某一段而沿用既有 load-test artifacts：

```sh
# 只重跑 billing logger 與 PostgreSQL ledger 驗證
scripts/run-staging-e2e.sh --confirm video-cloud-staging \
  --skip-remove --steps billing

# 只驗證 billing log
scripts/run-staging-e2e.sh --confirm video-cloud-staging \
  --skip-remove --steps billing-log \
  --out-dir cloud_env/staging/runtime/artifacts/staging-e2e/<existing-run>

# 只跑 MQTT 流量與 billing，不重建 users/devices
scripts/run-staging-e2e.sh --confirm video-cloud-staging \
  --skip-remove --steps mqtt,billing
```

可用 step 為 `reset`、`provision`、`data`、`mqtt`、`runtime-logs`、
`billing-log`、`billing-db`；`billing` 等同於 `billing-log,billing-db`。
`billing-log` 會使用專用 billing logger token 查詢 `billing_usage`，
`billing-db` 會查詢同一個 Brand Cloud 的 `usage_facts`。因此 billing 驗證
使用 load-test 已產生的 Brand Cloud/device credentials，不會另行建立一套
不同的憑證流程。

PostgreSQL capacity expansion 不屬於一般 `staging-provision` rollout。LKE
persistent mode 使用 PostgreSQL StatefulSet 的 PVC；`LKE_POSTGRES_STORAGE`
描述新建 manifest 的 claim size，不會自動改變已經 Bound 的
`data-postgresql-0`。既有 PVC 的擴充、Linode CSI online expansion 前置條件、
filesystem resize、backup、fallback 與驗證流程，請依
[`docs/postgres-capacity-expansion-runbook.md`](../docs/postgres-capacity-expansion-runbook.md)
執行。`LKE_POSTGRES_STORAGE_MODE=emptydir` 只適合明確的 ephemeral validation，
不得用來承載需要保留的 PostgreSQL data。

### `go run ./scripts/go/rtk-cloud -- staging-e2e-test`

Linode K8s staging E2E compatibility orchestrator。它仍可把 K8s reset、K8s rollout readiness、K8s service query/port-forward、staging E2E data setup、home MQTT simulation，以及 persisted MQTT runtime log verification 串成單一流程，最後輸出 sanitized `summary.json` 與 `TEST_REPORT.md`。建立 RTK brand cloud、建立測試 users、產生並 factory-enroll devices、device bind/provision、bulk bind validation 已拆到 `scripts/setup-staging-e2e-data.sh` / `rtk-cloud staging-e2e-data-setup`，完整 E2E 會呼叫這個獨立步驟。

正式 operator 入口是 `scripts/run-staging-e2e.sh`。這個 shell 檔只是一層
POSIX wrapper，實際流程在 Go command `rtk-cloud run-staging-e2e`：它會依序
執行 reset、provision、acceptance phase。因此完整 staging acceptance 可直接執行：

Account Manager 的驗證信與密碼重設信只透過 Realtek Connect Send Mail
**HTTP API** 寄送。完整 E2E 會先 reset workloads；執行前必須把
以下設定放在 operator process environment（secret 不可寫入 Git、PR 或 log）：

```sh
export AUTH_TOKEN_BASE_URL=https://admin.video-cloud-staging.realtekconnect.com
export SENDMAIL_HTTP_BASE_URL=https://sm.realtekconnect.com
export SENDMAIL_HTTP_TIMEOUT=15s
export SENDMAIL_HTTP_BEARER_TOKEN='<從 operator secret store 載入>'
```

Send Mail HTTP URL/token 缺失或無效時，reset 會在刪除任何 workload 前
fail fast。這些值必須存在於啟動部署 command
的同一個 shell；staging reset/provision 會重建 runtime secret，不能只依賴
cluster 內原有的 Secret。

```sh
scripts/run-staging-e2e.sh --plan
scripts/run-staging-e2e.sh --confirm video-cloud-staging
```

若既有環境缺少 Send Mail HTTP 設定，可用 targeted repair，
只更新 Account Manager runtime secret、API、migration job 與 email worker；不會
reset PostgreSQL、DNS 或其他 shared workloads：

```sh
go run ./scripts/go/rtk-cloud -- account-manager-email-deploy \
  --workspace "$PWD" \
  --env-root cloud_env/staging/runtime \
  --kubeconfig "$HOME/.config/rtk_cloud/staging/kube/kubeconfig.yaml" \
  --confirm video-cloud-staging

RUN_LIVE_EMAIL_E2E=1 python3 scripts/staging_email_signup_e2e.py \
  --confirm video-cloud-staging --skip-deploy
```

驗證時除了確認 `account-manager` 與 `account-manager-email-worker` rollout ready，
也要跑 live email E2E；只有 API 回 202 不代表外部信件已實際送達。

LKE staging capacity 以 `cloud_env/staging/runtime/env/stack.env` 為 source of
truth。`rtk-cloud provision --plan` 會先印出 capacity plan；
`--preflight`、`--apply`、`--deploy` 會在 mutation 前檢查：
`required_mqtt_replicas = ceil(LKE_TARGET_CONNECTS / LKE_MQTT_CONNECTIONS_PER_POD)`，
`usable_node_capacity = node_allocatable - per_node_system_reserved`，
`required_nodes = max(cpu nodes, memory nodes, required_mqtt_replicas)`。
目前 1K validation profile 在 `stack.env` 明確設定
`LKE_TARGET_CONNECTS=1000`、`LKE_MQTT_CONNECTIONS_PER_POD=20000`、
`LKE_MQTT_REPLICAS=2`、`LKE_NODE_COUNT=2`。100K 或更高目標應調整
`LKE_TARGET_CONNECTS`、`LKE_MQTT_REPLICAS=auto` 或明確 replicas、
`LKE_NODE_COUNT=auto` 或明確 node count，再用 plan/preflight review。
常用資源 override 包含 `LKE_POSTGRES_REQUEST_CPU`、
`LKE_POSTGRES_REQUEST_MEMORY`、`LKE_POSTGRES_LIMIT_MEMORY`、
`LKE_VIDEO_CLOUD_API_REQUEST_CPU`、`LKE_VIDEO_CLOUD_API_REQUEST_MEMORY`、
`LKE_VIDEO_CLOUD_API_LIMIT_MEMORY`、`LKE_MQTT_REQUEST_CPU`、
`LKE_MQTT_REQUEST_MEMORY`、`LKE_MQTT_LIMIT_MEMORY`、
`LKE_INGRESS_REPLICAS` 與 `LKE_INGRESS_REQUEST_CPU`。
100K MQTT/shadow + WebRTC baseline 曾在 Postgres 專用
`g6-standard-4` node 上觀察到 Postgres node CPU p95/max 約 `102%`，
且 device token bootstrap 還有 `request_token context deadline exceeded`。
100K baseline 因此建議使用 Postgres 專用 `g6-standard-8`、
`LKE_POSTGRES_REQUEST_CPU=4`、`LKE_POSTGRES_REQUEST_MEMORY=4Gi`、
`LKE_POSTGRES_LIMIT_MEMORY=8Gi`，除非新的 run report 證明較小配置已通過。
若 Linode account type limit 暫時無法建立 `g6-standard-8`，可先用
`g6-standard-6` 做受限 rerun，但報告必須保留這個 sizing constraint。
`video-100k-turn-v1` 會設定 `HOME100K_DEVICE_TOKEN_REQUEST_RETRIES=1`，
讓 device MQTT bootstrap 的 transient `/request_token` timeout 有一次 bounded
retry。這只用來排除短暫 token/bootstrap 抖動，不放寬 100K client target
completeness；若 retry 後仍有 device token timeout，應先歸因到
API/token-bootstrap/Postgres path，而不是 coturn/TURN 容量。

容量係數必須來自可 review 的實驗紀錄，不直接把 `20000` 當作永久真值。
要產生一筆完整紀錄，使用：

```sh
scripts/run-lke-capacity-experiment.sh \
  --target-devices 10000 \
  --mqtt-pods 1 \
  --node-count 2 \
  --node-type g6-standard-2 \
  --mqtt-request-memory 2Gi \
  --mqtt-limit-memory 4Gi \
  --cloud-logger-request-memory 4Gi \
  --cloud-logger-limit-memory 16Gi \
  --live \
  --confirm video-cloud-staging
```

這個 wrapper 只串接既有 cleanup、provision、data setup 與 home-100k scripts；
每次 run 會把 request、applied stack config、load-test report，以及
`capacity-run-summary.json` 寫到 `<env-root>/artifacts/capacity-experiments/<run-id>/`。
若實驗調整 MQTT pod 資源，請用 wrapper 參數記錄
`--mqtt-request-cpu`、`--mqtt-request-memory`、`--mqtt-limit-memory` 與
`--emqx-force-shutdown-max-heap-size`；不要只手動改 cluster，否則下一輪
review 看不到實驗條件。
若失敗點在 runtime evidence/log correlation，cloud-logger sizing 也要由
wrapper 記錄：`--cloud-logger-request-memory`、`--cloud-logger-limit-memory`。
100K evidence collection 曾在 8Gi limit 下觸發 cloud-logger OOMKilled，因此
100K with WebRTC sizing baseline 不應低於 limit 16Gi；若 node sizing 允許，可再把
request 提高到 8Gi，讓 scheduler 更保守地保留記憶體。
若 runtime log correlation 顯示 `central logger runtime query window budget exhausted`，
同一輪 100K rerun 應提高 `HOME100K_CENTRAL_LOGGER_RUNTIME_QUERY_MAX_WINDOWS`
並保留報告中的 logger pod restart/resource evidence；不要把 skipped correlation
當成完整 E2E trace。
request 也會記錄 `live_runner_timeout_grace`；大型 live shard 需要額外時間
完成 MQTT disconnect、cleanup 與 `results.json` 寫檔，不能只用 stage duration
加固定短 buffer。
wrapper 也會記錄 user/device/bind concurrency；目前 device enrollment 預設先用
`16`，避免 local factory-enroll port-forward 在 64-way concurrency 下出現
client-side header timeout。capacity wrapper 也會預設設定並記錄
`factory_enroll_ports=18443,18444,18445,18446`，讓 data setup 透過多個
factory-enroll port-forward endpoint 分散 device enrollment；這只是避免本機
port-forward 成為 setup 瓶頸，不可拿來當 MQTT pod 或 LKE node 容量係數。
容量公式、100K 初始預測與二分實驗策略見 `docs/lke-capacity-sizing.md`。

100K 7/7 loading baseline 的 test data distribution 由
`loadtests/home-100k/scenarios/brand-plan-100k.json` 定義：10 個 brand
clouds、5,000 個 normal member users、10 個 developer users（每個 brand
cloud 1 個 owner）、100,000 devices。developer users 只做 setup/validation，
不參與 runtime MQTT/app command traffic。loader VM 數量仍由
`ceil(devices / HOME100K_LOAD_GENERATOR_DEVICES_PER_VM)` 計算；100K / 20,000
devices per VM 會得到 5 台，不在 script 或文件寫死。

`run-staging-e2e.sh --confirm` 預設會先 reset K8s runtime resources，因此也
預設重建 users/devices/bind SQLite test data，不重用舊本機資料；這可避免
fresh deployment 搭配舊 bindings 造成 validation 失敗。只有在明確加
`--skip-remove` 或手動傳 `--resume` 時才會重用既有 artifact。

目前 live staging 驗證使用預設 acceptance 規模：`10` 個 users、`100` 個
devices，device mix 為 `camera=40`、`light=25`、`air_conditioner=20`、
`smart_meter=15`。最近一次完整驗證通過的 report 路徑為
`cloud_env/staging/runtime/artifacts/staging-e2e/20260618T085249Z/TEST_REPORT.md`。

預設是 safe plan，不會 reset K8s、不會呼叫 API：

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

- `--env-root PATH`：指定 environment directory；必填。可傳 `cloud_env/staging`，script 會依 provider 自動使用其下的 `linode/` 或 `lke/`。
- `--plan`：只列出將執行的步驟，預設模式。
- `--run --confirm STACK`：執行完整流程。`STACK` 必須符合 `CLOUD_STACK_NAME`，避免刪錯 staging stack。
- `--skip-remove`：不先執行 `rtk-cloud remove-k8s`，直接走 `rtk-cloud provision-k8s` 與後續流程。
- `--skip-provision`：只跑 acceptance/data/MQTT/log verification，不更新 K8s workloads；正式入口通常使用 `scripts/run-staging-acceptance.sh`。
- `--out-dir PATH`：指定報告輸出目錄；預設在 `<env-root>/artifacts/staging-e2e/<timestamp>/`。
- `--skip-mqtt-probe`：略過 live MQTT broker probe；MQTT flow 仍會產生 E2E artifact 供 log verification 使用。

輸出：

- `summary.json`：整體結果、stack、brand、redacted artifact path，以及每個 top-level segment 的狀態、exit code、耗時和 log path。Data setup 內部 create/bind/validate 子步驟記錄在 `data-setup/summary.json`。
- `TEST_REPORT.md`：人工閱讀用測試報告。
- `logs/*.log`：各步驟 stdout/stderr。這些 log 留在 git ignored cloud env artifacts；提交或分享前仍應視為 operator artifact 審查。

報告檔會掃描常見敏感字串，避免 password、bearer token、private key 或服務 secret 被寫入 summary/report。完整 per-step log 不會自動清洗，不應 commit。Production-like APP actor 不能只做 Account Manager login；第一次 login 後如果回傳 `app_certificate.status=csr_required`，要模擬 app 本機產生 private key 與 subject `app-user:<user_id>` 的 CSR，透過 Account Manager 交給 certissuer 簽發 app certificate，pin certificate identity，並用該 certificate 透過 mutual TLS 呼叫 Video Cloud `POST /request_token` 換 subject-bound `app` token，後續 APP command/subscribe 才能用 token 執行。MQTT 子測試使用 actor-separated IoT 模式：device actor 用 device certificate/key 透過 mutual TLS 呼叫 Video Cloud `POST /request_token` 換 `device` token，再用 token credential 連 broker；app actor 用 users artifact 內的 app private key 與 app certificate 透過 mutual TLS 呼叫 `POST /request_token` 換指定 device 的 `app` token，再用 token credential 連 broker。Telemetry path 由 app observer 先訂閱 `devices/<device_id>/up/messages`，device 再 publish sample home-device envelope，只有 app observer 收到 matching `message_id` 才算 PASS。Command path 由 device 先訂閱 `devices/<device_id>/down/commands`，app controller publish AWS IoT Shadow-style command envelope，以 `payload.clientToken` 對應 `command_id`，並用 `payload.state.desired` 表示 desired state，例如 smart light `power=true`、air conditioner `mode=cool/target_temperature_c=24/fan=auto`；device 收到 matching `command_id` 後 publish `command_result` ack 到 `devices/<device_id>/up/messages`，ack 以 `payload.state.reported` 回報 device 已接受的狀態，只有 app observer 收到 matching ack 才算 PASS。broker-side per-message log 目前不是 PASS 來源；PASS 來源是分離 MQTT connections 的 publish/subscribe/receive 證據。只做 local artifact 檢查、或同一 client 自己 publish 後自己收到，不能算通過。

可用 `go run ./scripts/go/rtk-cloud -- mqtt-trace-report --environment staging --brandname RTK` 從最新 RTK `home-mqtt-loadtest` artifact 產出獨立 trace chain 報告 `E2E_TRACE_CHAIN_REPORT.md`。若要指定特定 run，可用 `go run ./scripts/go/rtk-cloud -- mqtt-trace-report --results-file <results.json>`；預設輸出到該 `results.json` 同目錄。報告只列 actor、action、topic、status 與 sanitized detail，不包含 token、private key、CSR 或 certificate PEM。

`rtk-cloud mqtt-test --environment staging --brandname RTK` 的 console runtime trace 可用 `--trace-detail` 控制：`summary` 是預設，只顯示 publish/receive 與資料摘要；`full` 顯示 token/connect/subscribe/publish/receive 全鏈條；`none` 關閉 console trace。資料摘要包含 timestamp、actor、topic、message_type、message_id、command_id、device_id、payload action/status，以及 selected `desired.*` / `reported.*` state 欄位，不輸出 payload body、`clientToken` 或 credential material。

### `scripts/setup-staging-e2e-data.sh`

獨立的 staging E2E data setup 腳本，只建立/更新 E2E 測試資料，不 remove provider resources、不 provision servers、不跑 live MQTT。完整 `scripts/run-staging-e2e.sh` 會透過 `rtk-cloud staging-e2e-test` 呼叫這個腳本；operator 也可以單獨跑它來重建 brand/users/devices/bind SQLite test data。

```sh
scripts/setup-staging-e2e-data.sh \
  --brandname RTK \
  --user-count 10 \
  --device-count 100 \
  --out-dir cloud_env/staging/runtime/artifacts/staging-e2e/manual-data-setup

scripts/setup-staging-e2e-data.sh \
  --env-root cloud_env/staging/runtime \
  --brand-plan loadtests/home-100k/scenarios/brand-plan-100k.json \
  --plan
```

常用選項：

- `--plan`：只列出 data setup 會執行的步驟。
- `--workspace PATH`：指定 workspace root；預設目前 checkout。
- `--env-root PATH`：指定 environment directory；預設 `cloud_env/staging`，會依 provider 自動 resolve 到 Kubernetes provider 子目錄，預設 `cloud_env/staging/runtime`。
- `--brandname NAME`：brand cloud 名稱；預設 `RTK`。
- `--brand-plan FILE`：multi-brand data setup plan；指定後會依 plan 建立每個 brand cloud、owner/admin developer users、member users、devices、bindings 與 validation。
- `--user-count N` / `--device-count N`：建立 user/device 數量。
- `--device-mix MIX` / `--device-prefix PREFIX`：轉傳給 `generate-load-devices`。
- `--out-dir PATH`：輸出 `summary.json`、`logs/*.log` 與 `bind-validation/` 的位置；未指定時使用 `<env-root>/artifacts/staging-e2e-data/<timestamp>/`。

內建 device generator 在建立 device 前，會以 platform admin 呼叫 Account Manager：先為本次 Brand Cloud 建立或重用 run-scoped device item profile，再建立 production run 並取得短效 production JWT。若上游 fixture provider 已透過同一正式 API 簽發並傳入 `FACTORY_ENROLL_PRODUCTION_JWT`，共用 setup 只傳遞該 token，不重複建立 production run。JWT 只傳入 device generator 子程序，不寫入 log、summary 或 evidence；由共用 setup 簽發時，`factory-production.json` 只保留 Brand、profile、production-run ID 與已簽發狀態。production run 建立失敗時會在任何 device enrollment／bind 前停止，不回退到 legacy HMAC。

輸出 `summary.json` 會包含 `test_data_db`、`bind_validation_dir`，以及 create brand、create users、prepare factory production、create devices、bind devices、validate bind 每段的 status、exit code、duration seconds 和 log path。這個腳本只支援 Kubernetes provider；`CLOUD_PROVIDER=linode` 會在任何 mutation 前 fail fast。

### Home loading test

Home loading test 相關的 scenario、operator guide、report schema、template、
Ansible、scripts 與 legacy MQTT reference 已集中在：

```text
loadtests/home-100k/
loadtests/home-100k/docs/
```

`go run ./scripts/go/rtk-cloud -- mqtt-loadtest` 仍是低階 MQTT transport
參考，但 100K Home IoT Device Shadow 容量測試的文件與操作入口都以
`loadtests/home-100k/` 為準。請不要在 `scripts/README.zh-TW.md` 內新增
Home loading test 的詳細操作步驟。

可用 `go run ./scripts/go/rtk-cloud -- video-relay-test --environment staging --brandname RTK` 執行 staging WebRTC RTP relay smoke。這個測試只選 SQLite test-data DB 內具備 `video_streaming` service option 的 camera device，使用 device certificate mTLS 換 device token，使用 DB 內 app private key + app certificate mTLS 換 device-bound app token，然後重用 `e2e_test/video_cloud/load` runner。PASS 代表 device websocket owner online、viewer 建立 WebRTC session、server 回 SDP offer 與 ICE servers、device 送 SDP answer、ICE connected/completed、device 以 2s 1080p `testsrc2` Annex-B H.264 fixture loop 10 次送出 20s H.264 RTP，payload validation 看到 SPS/PPS/IDR/non-IDR NAL types，且 matching device token 可以關閉 session；functional profile 另以 app token 重送 duplicate close，確認 app 授權也通過 Close 的 authorization boundary。這不是 legacy raw RTP relay 測試；PASS 來源是 WebRTC signaling + H.264 RTP payload evidence。輸出在 `<env-root>/artifacts/video-relay-test/<timestamp>/results.json` 與 `TEST_REPORT.md`，報告會記錄不含秘密的 Close authorization 類型，console/report 會 redacted bearer token、TURN credential、private key、CSR 與 certificate PEM。

### K8s runtime 設定與 log level

staging runtime 已改為 K8s-only，不再提供舊的 VM runtime shortcut。application service log level 應透過 K8s manifest/secret/config 管理，再用 `provision-k8s` 檢查 rollout readiness。

Staging 的平台管理頁登入帳密和 Account Manager automation token 帳密不同：

- Cloud Admin `/login?next=/admin` 使用 Account Manager platform-admin flow，不再使用 legacy Cloud Admin bootstrap credential。
- `rtk-cloud platform-admin-token --environment staging` 和 e2e brand/user/bind automation 使用 `cloud_env/staging/runtime/services/account-manager/account-manager-platform-admin.env` 內的 Account Manager bootstrap platform-admin 帳密。
- `ACCOUNT_MANAGER_BOOTSTRAP_PLATFORM_ADMIN_EMAIL` 可由 deployment environment override；未指定時 LKE 預設為 `platform-admin@<stack-name>.local`。密碼由 deployment runtime secret 的 `LKE_PLATFORM_ADMIN` 提供，未指定時由 runtime secret state 產生／保存；不要把密碼寫入 tracked `.env` 或文件。

Cloud Admin `/admin` UI 應使用上述 Account Manager platform-admin 帳號登入；不要使用已淘汰的 legacy Cloud Admin `ADMIN_BOOTSTRAP_*` 帳密。詳細邊界見 `docs/account-manager-admin-boundary.md#staging-login-credential-boundary`。

常用檢查：

```sh
go run ./scripts/go/rtk-cloud -- provision-k8s \
  --env-root cloud_env/staging \
  --confirm video-cloud-staging
```

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

- `--env-root PATH`：指定 environment directory；必填，避免檢查錯誤環境。可傳 `cloud_env/staging`，script 會自動使用其下的 Kubernetes provider 子目錄，預設 `lke/`。
- `--dns-root-domain NAME`：root DNS domain，預設 `realtekconnect.com`。
- `--min-valid-days N`：要求 certificate 至少還要有效幾天，預設 `7`。
- `--skip-live`：只檢查 env-root 內的 certificate cache。
- `--skip-cache`：只檢查 live HTTPS endpoint。
- `--json`：輸出完整 JSON 結果。

如果任一 certificate 缺失、過期、hostname 不符合、live chain 驗證失敗，或低於剩餘效期門檻，script 會顯示 `status=fail` 並以 non-zero exit code 結束。

### `go run ./scripts/go/rtk-cloud -- create-brandname-cloud`

在 Account Manager staging 上建立 brand cloud。腳本會先確保 platform-admin bootstrap env 可用，再用 Account Manager API 建立 brand cloud；若 API 建立遇到已知 server error，會用 PostgreSQL fallback upsert，最後再透過 API 驗證結果。

用法：

```sh
go run ./scripts/go/rtk-cloud -- create-brandname-cloud --env-root cloud_env/staging --brandname RTK
```

常用選項：

- `--workspace PATH`：指定 workspace 根目錄。
- `--env-root PATH`：指定 environment directory；必填，避免建立到錯誤環境。可傳 `cloud_env/staging`，script 會自動使用其下的 Kubernetes provider 子目錄，預設 `lke/`。
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
- `--rotate-password`：既有 user 也更新初始密碼；預設遇到既有 user 會失敗，避免產生不會生效的新 credentials。若只是要重用既有帳號，請使用 SQLite test-data DB 內已保存的資料。
- `--dry-run`：只列出將建立的 email，不呼叫建立 user API，也不寫 credentials。

腳本的進度訊息會寫到 stderr，stdout 只輸出 summary JSON，不包含密碼、private key、CSR 或 certificate PEM。初始密碼與 app-local bootstrap material 會寫入 `<env-root>/artifacts/test-data/<brand>-test-data.sqlite`，檔案權限為 `0600`。建立或重設密碼後，腳本會依文件模擬第一次 app login：先登入 Account Manager；若回傳 `app_certificate.status=csr_required`，就在本機產生 app private key 與 subject `app-user:<user_id>` 的 CSR，以 `app_csr_pem` 再登入一次，讓 Account Manager 透過 certissuer 簽發 app certificate 並寫入 database。SQLite DB 會記錄每個 user 的密碼、app private key、CSR、certificate/chain、fingerprint、serial、issuer request id 與有效期，供後續 production-like app mTLS/token bootstrap 測試使用。如果 API 回報 user 已存在且未指定 `--rotate-password`，腳本會停止，不會寫新的 credentials。若 user 已有有效 app certificate，腳本會從同 brand SQLite test-data DB 或 legacy import 後的資料重用本機 app key/CSR；找不到既有 key 時會停止，避免產生缺少 mTLS private key 的最新資料。

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
  --env-root cloud_env/staging \
  --brandname RTK
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
- bindings 與 provisioning state 寫到 `<env-root>/artifacts/test-data/<brand>-test-data.sqlite`，檔案權限為 `0600`。
- DB 內包含 assigned email、device id/type、`service_options`、account device id、operation id 與 status。
- raw Claim Token、user password、bearer token 只存在 process 暫存檔，腳本結束會移除。
- 未指定 `--users-file` / `--devices-dir` 時，命令會使用 SQLite test-data DB 內的 users/devices。
- 未指定 `--count` 時，腳本會綁定 DB 內全部 devices。

常用選項：

- `--env-root PATH`：指定 environment directory；必填。
- `--brandname NAME`：指定既有 brand cloud。
- `--users-file FILE`：legacy 相容入口；active flow 使用 SQLite test-data DB。
- `--devices-dir DIR`：legacy 相容入口；active flow 使用 SQLite test-data DB。
- `--count N`：只綁定前 N 台 device；未指定時綁定 DB 內全部 devices。
- `--dry-run`：只輸出 assignment plan，不呼叫 Account Manager API，也不寫 bindings。
- `--skip-bootstrap`：不要更新/restart 遠端 Account Manager bootstrap admin env。

### `go run ./scripts/go/rtk-cloud -- validate-device-bind`

驗證 SQLite test-data DB 內 `rtk-cloud bind-devices` 寫入的 bindings/provisioning state，作為 100 devices onboarding staging smoke 的 API-level 結果檢查。這個 profile 不要求 live video streaming 成功；它確認每筆 API claim/bind/provision 結果都有 account device id、provision operation id，且 `service_options` 符合 ACL 預期。

用法：

```sh
go run ./scripts/go/rtk-cloud -- validate-device-bind \
  --env-root cloud_env/staging \
  --brandname RTK \
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
