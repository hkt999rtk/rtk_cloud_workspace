# scripts 目錄說明

這個目錄放的是 workspace 層級的操作腳本，主要用途包含文件檢查、部署證據收集、Linode staging provision/deploy、brand cloud 建立，以及 GitHub Actions self-hosted runner 管理。

除非特別註明，以下指令都建議從 workspace 根目錄執行。

## Cloud Environment Root

Environment root 採 `cloud_env/<env>/<provider>` 形式。staging scripts
預設使用本機、git ignored 的 `cloud_env/staging/linode` 作為實際 Linode
environment root。操作時可用 `--env-root cloud_env/staging` 指定 staging
environment directory；script 會依 `CLOUD_PROVIDER`、`RTK_CLOUD_STAGING_PROVIDER`
或 provider stack file 自動解析到 `cloud_env/staging/linode` 或
`cloud_env/staging/lke`。這個目錄集中保存 operator env、topology、service
env、state、keys/certificates、device fixtures、artifacts 與 backups。

目前 workspace provision routing 分成 cloud provider adapter 與 runtime
兩層。`CLOUD_PROVIDER=linode` 保留 legacy VM runtime，仍 dispatch 到 Video
Cloud、Account Manager、Cloud Admin、Cloud Logger 的 VM scripts。
`CLOUD_PROVIDER=lke` 走 Kubernetes runtime，provider adapter 只負責 Linode
LKE cluster discovery/create/kubeconfig；RTK workloads、Namespace、Secret、
Deployment、Service、Ingress、NetworkPolicy、rollout 與 E2E orchestration
由共用 Kubernetes runtime 處理。若沒有 `KUBECONFIG` / current context，LKE
adapter 會依 `LKE_CLUSTER_ID`、`state/lke.env` 或 cluster label
`<CLOUD_STACK_NAME>-lke` 尋找既有 cluster，`provision --apply` 找不到時會建立
LKE cluster，再把 kubeconfig 寫到 git-ignored
`<env-root>/state/lke-kubeconfig.yaml`。之後才走 kubectl
namespace/apply/delete/rollout path。`deploy` 需要 container image；
可以明確提供 `LKE_POSTGRES_IMAGE`、`LKE_VIDEO_CLOUD_IMAGE`、
`LKE_ACCOUNT_MANAGER_IMAGE`、`LKE_CLOUD_ADMIN_IMAGE`、`LKE_FRONTEND_IMAGE`、
`LKE_CLOUD_LOGGER_IMAGE`。
Service images 由各 service repo 的 release workflow 發布到 GHCR；
workspace 只解析 pinned submodule commit、驗證對應 image 是否存在，並輸出
後續 deploy/e2e 需要的 `LKE_*_IMAGE` mapping。GCP/Azure/AWS 的 Kubernetes
service provider id 分別預留為 `gke`、`aks`、`eks`；目前只有 fail-fast
adapter，會在任何 kubectl、cloud API、SSH、DNS 或 state mutation 前停止。
現階段唯一應被 live 驗證的 Kubernetes provider 仍是 `lke`。

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
Grafana 會部署在 observability namespace 作為 private `ClusterIP` dashboard
layer，讀取 internal Prometheus Service；它不會透過 `provision --dns` 建立
public hostname、Ingress 或 TLS SAN。Platform 管理員要從 Cloud Admin 的
Platform View 分頁透過 same-origin iframe 觀看 Grafana，Cloud Admin BFF 會
以 platform-admin session 保護 `/api/admin/grafana/*` proxy 路徑。

可用 `--env-root PATH` 指向另一份 environment directory。舊的 `--secrets-root PATH` 仍保留為相容 alias，但新的操作與文件都應使用 `--env-root`。

`cloud-*` 是目前正式入口。舊的 `staging-*` / `staging_*` 相容 wrapper 已移除；automation 與文件都應使用 `cloud-*` 名稱。

目錄配置請見 `docs/cloud-env-layout.zh-TW.md`。

### `go run ./scripts/go/rtk-cloud -- lke-resolve-images`

解析 workspace 目前 pinned submodule commits 對應的 service images，不建立
cluster、不套用 Kubernetes resources、不 build service images。這個 command
主要給 CI image manifest workflow 使用，也可本機手動產 image manifest：

```sh
go run ./scripts/go/rtk-cloud -- lke-resolve-images \
  --env-root cloud_env/staging/lke \
  --owner hkt999rtk \
  --out .artifacts/lke-images/lke-image-manifest.json
```

輸出的 manifest 會包含 `LKE_POSTGRES_IMAGE`、`LKE_VIDEO_CLOUD_IMAGE`、
`LKE_ACCOUNT_MANAGER_IMAGE`、`LKE_CLOUD_ADMIN_IMAGE`、`LKE_FRONTEND_IMAGE`、
`LKE_CLOUD_LOGGER_IMAGE` mapping。`scripts/run-staging-e2e.sh` 在 LKE provider
且缺少任一 `LKE_*_IMAGE` 時會自動執行 image resolve，寫入
`<env-root>/artifacts/lke-images/<timestamp>/`，並同步 latest manifest 到
`<env-root>/artifacts/lke-images/lke-image-manifest.json`。手動 export
`LKE_*_IMAGE` 只作為 override。

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

K8s staging 的 factory enrollment 設定由 K8s runtime secret/config 提供。E2E 會透過 K8s service query/port-forward 取得 `FACTORY_ENROLL_URL` 與 `FACTORY_ENROLL_AUTH_KEY`，不再透過 VM provision/deploy 修改 service env。

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

舊 VM staging env 匯入工具已退役。staging runtime 已改為 K8s-only，不再從 submodule VM deploy 目錄匯入 state/env。請使用 `sync-env` 維護 K8s/domain metadata，並讓 E2E 透過 K8s service query/port-forward 取得 runtime endpoint。

用法：

```sh
go run ./scripts/go/rtk-cloud -- migrate-env --env-root cloud_env/staging
# error: migrate-env is retired with the staging VM toolkit
```

常用選項：

- `--env-root PATH`：指定 environment directory；必填。可傳 `cloud_env/staging`，script 會依 provider 自動使用其下的 `linode/` 或 `lke/`。
- `--force`：保留為相容 flag，但命令一律回報 retired。

### `go run ./scripts/go/rtk-cloud -- sync-env`

依照 `cloud_env/<env>/linode/env/stack.env` 內的 root metadata 產生所有命名欄位。`CLOUD_ENV_NAME` 是唯一 stack slug/root；stack name、domain、topology label、Linode VM/firewall label、VPC/subnet label，以及 Account Manager、Cloud Admin、Cloud Logger service env 的 domain/label 都由它推演，不要手動分別修改。

目前 `sync-env` 仍是 Linode naming helper：產出的 `*_LINODE_*` 欄位是 VM
provider metadata，不是 LKE 或未來 AWS/GCP/Azure 的跨 provider 欄位。LKE
env root 可以使用相同 root inputs 讓 runtime CLI 推導 domain/stack name，但
production Kubernetes manifests、Helm charts、CI/CD pipeline 仍受
`docs/lke-migration-inventory.md` 的 gate 管制。

Root inputs 固定為：

- `CLOUD_ENV_NAME`
- `CLOUD_PROVIDER`
- `CLOUD_REGION`
- `CLOUD_DNS_ROOT_DOMAIN`

以 `CLOUD_ENV_NAME=stg` 為例，generated naming 會包含 `video-cloud-stg-edge`、`rtk-account-manager-stg`、`rtk-cloud-admin-stg`、`rtk-cloud-logger-stg`。

用法：

```sh
go run ./scripts/go/rtk-cloud -- sync-env --env-root cloud_env/staging
go run ./scripts/go/rtk-cloud -- sync-env --env-root cloud_env/staging --check
```

`--check` 不改檔，只檢查 `stack.env` generated block、topology YAML、service env 是否已和 root metadata 同步。staging runtime 已改為 K8s-only；若不同步請先執行 `sync-env --env-root ...`。

### `go run ./scripts/go/rtk-cloud -- provision-k8s`

Linode staging runtime 只支援 K8s。`provision-k8s` 不建立 VM，也不部署 VM binary；它會取得 LKE kubeconfig、確認 staging namespaces 存在，並等待 deployment/statefulset rollout ready。

Provider-aware `provision`/`deploy` command 仍保留給 legacy VM 與 LKE image/runtime tooling；完整 staging E2E 目前走 `provision-k8s`，不走 VM deploy hooks。

LKE `provision --apply` 預設會安裝 Kubernetes metrics-server 到 `kube-system`，版本由 `LKE_METRICS_SERVER_VERSION` 控制，預設 `v0.8.1`。metrics-server 提供 `metrics.k8s.io`，讓 `kubectl top nodes` / `kubectl top pods` 與 Kubernetes HPA resource metrics 可用；它不取代 Prometheus 的長期觀測、dashboard 或 alert 用途。

LKE `provision --deploy` 會部署 workspace-managed Prometheus 與 private
Grafana。Grafana 常用環境變數：

- `LKE_GRAFANA_IMAGE`：Grafana image，預設 `grafana/grafana:13.0.2`。
- `LKE_GRAFANA_ADMIN_PASSWORD`：Grafana admin 密碼；未設定時由 runtime secret material 產生。
- `LKE_GRAFANA_PERSISTENCE=true`：啟用 Grafana PVC。預設使用 `emptyDir`，
  staging acceptance 不消耗 Linode block volume quota。
- `LKE_GRAFANA_STORAGE`：啟用 persistence 時的 Grafana PVC 大小，預設 `5Gi`。
- `CLOUD_ADMIN_GRAFANA_BASE_URL`：Cloud Admin 連到 Grafana 的 cluster-internal base URL，例如 `http://video-cloud-grafana.video-cloud-staging-observability.svc.cluster.local:3000`。

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

./stg.sh provision --confirm video-cloud-staging
```

常用選項：

- `--workspace PATH`：指定 workspace 根目錄。
- `--env-root PATH`：指定 environment directory；必填。可傳 `cloud_env/staging`，script 會自動使用其下的 `linode/`。
- `--confirm STACK`：必須符合 `CLOUD_STACK_NAME`，避免操作錯誤 stack。
- `--timeout DURATION`：K8s rollout 等待時間，預設 5 分鐘；也可用 `CLOUD_STAGING_E2E_K8S_ROLLOUT_TIMEOUT` 設定。

### `go run ./scripts/go/rtk-cloud -- provision --dns`

LKE `--dns` 會建立 public HTTPS entry，不再是 no-op。流程是：

1. 用 Helm 安裝或更新 `ingress-nginx` 到 `<stack>-ingress` namespace。
2. ingress-nginx controller service 使用 Linode `LoadBalancer` / NodeBalancer，只開 `443/TCP`，不建立 public `80/TCP` listener。
3. 透過 certbot manual DNS-01 hook 與 GoDaddy TXT record 簽一張 staging multi-SAN certificate，寫入 Kubernetes TLS secret `video-cloud-staging-public-tls`。
4. 建立 ingress namespace 內的 ExternalName bridge services，讓 Ingress 合法轉到各 namespace 的 internal `ClusterIP` services。
5. 建立 HTTPS Ingress rules：
   - `video-cloud-staging.realtekconnect.com` -> `video-cloud-api`
   - `device.video-cloud-staging.realtekconnect.com` -> `video-cloud-api`
   - `certissuer.video-cloud-staging.realtekconnect.com` -> `certissuer`
   - `account-manager.video-cloud-staging.realtekconnect.com` -> `account-manager`
   - `admin.video-cloud-staging.realtekconnect.com` -> `cloud-admin`
   - `frontend.video-cloud-staging.realtekconnect.com` -> `frontend`
6. 等待 NodeBalancer public IP，並用 GoDaddy A records 指向該 IP。
7. 套用 default-deny ingress NetworkPolicy 與必要 allow rules。

必要輸入：

- `GODADDY_KEY` / `GODADDY_SECRET` 或 operator env 內的等效 GoDaddy credentials。
- `CLOUD_DNS_ROOT_DOMAIN`，staging 預設是 `realtekconnect.com`。
- `certbot` CLI，或用 `RTK_CLOUD_CERTBOT` 指到指定 binary。
- `helm` 與 `kubectl` 可操作目標 LKE cluster。

常用驗證：

```sh
kubectl -n video-cloud-staging-ingress get svc ingress-nginx-controller
kubectl get ingress -A
dig +short A video-cloud-staging.realtekconnect.com @ns23.domaincontrol.com
nc -vz video-cloud-staging.realtekconnect.com 80
curl -fsS https://video-cloud-staging.realtekconnect.com/healthz
curl -fsS https://account-manager.video-cloud-staging.realtekconnect.com/v1/health
curl -fsS https://admin.video-cloud-staging.realtekconnect.com/healthz
```

MQTT、TURN、Postgres、OpenBao、Prometheus 不會因 `--dns` 對外公開；MQTT/TURN 需要另行設計 TCP/UDP exposure。
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
只有在 `CLOUD_STAGING_E2E_K8S_DESTRUCTIVE_RESET=1` 且傳 `--yes` 時才會刪除
staging namespaces；`--purge-storage` 會先刪 namespace 內 PVC，再刪 namespace。

```sh
go run ./scripts/go/rtk-cloud -- remove-k8s --env-root cloud_env/staging --yes
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
預設只清 staging K8s resources，不主動 purge storage；只有明確加
`--purge-storage` 才會清 PVC/PV/provider volume 類資料層。`staging-provision`
負責新安裝或停機升級：解析 image、套用 manifests、DNS/artifacts、rollout
readiness。`staging-acceptance` 不 reset、不 deploy，只驗證已部署好的 stack。

### `go run ./scripts/go/rtk-cloud -- staging-e2e-test`

Linode K8s staging E2E compatibility orchestrator。它仍可把 K8s reset、K8s rollout readiness、K8s service query/port-forward、staging E2E data setup、home MQTT simulation，以及 persisted MQTT runtime log verification 串成單一流程，最後輸出 sanitized `summary.json` 與 `TEST_REPORT.md`。建立 RTK brand cloud、建立測試 users、產生並 factory-enroll devices、device bind/provision、bulk bind validation 已拆到 `scripts/setup-staging-e2e-data.sh` / `rtk-cloud staging-e2e-data-setup`，完整 E2E 會呼叫這個獨立步驟。

正式 operator 入口是 `scripts/run-staging-e2e.sh`。這個 shell 檔只是一層
POSIX wrapper，實際流程在 Go command `rtk-cloud run-staging-e2e`：它會依序
執行 reset、provision、acceptance phase。因此完整 staging acceptance 可直接執行：

```sh
scripts/run-staging-e2e.sh --plan
scripts/run-staging-e2e.sh --confirm video-cloud-staging
```

LKE acceptance profile 預設以單節點可排程為優先：`mqtt`、
`account-manager`、`video-cloud-api` replicas 都是 `1`。容量測試或
production-like smoke 可用 `LKE_MQTT_REPLICAS`、
`LKE_ACCOUNT_MANAGER_REPLICAS`、`LKE_VIDEO_CLOUD_REPLICAS` 拉高；常用資源
override 包含 `LKE_POSTGRES_REQUEST_CPU`、`LKE_POSTGRES_REQUEST_MEMORY`、
`LKE_POSTGRES_LIMIT_MEMORY`、`LKE_VIDEO_CLOUD_API_REQUEST_CPU`、
`LKE_VIDEO_CLOUD_API_REQUEST_MEMORY`、`LKE_VIDEO_CLOUD_API_LIMIT_MEMORY`、
`LKE_INGRESS_REPLICAS` 與 `LKE_INGRESS_REQUEST_CPU`。

`run-staging-e2e.sh --confirm` 預設會先 reset K8s，因此也預設重建
users/devices/bind artifacts，不重用舊本機 artifact；這可避免 fresh database
搭配舊 bind artifact 造成 validation 失敗。只有在明確加 `--skip-remove` 或
手動傳 `--resume` 時才會重用既有 artifact。

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

可用 `./stg.sh mqtt-report RTK` 從最新 RTK `home-mqtt-loadtest` artifact 產出獨立 trace chain 報告 `E2E_TRACE_CHAIN_REPORT.md`。若要指定特定 run，可用 `./stg.sh raw mqtt-trace-report --results-file <results.json>`；預設輸出到該 `results.json` 同目錄。報告只列 actor、action、topic、status 與 sanitized detail，不包含 token、private key、CSR 或 certificate PEM。

`./stg.sh mqtt RTK` 的 console runtime trace 可用 `--trace-detail` 控制：`summary` 是預設，只顯示 publish/receive 與資料摘要；`full` 顯示 token/connect/subscribe/publish/receive 全鏈條；`none` 關閉 console trace。資料摘要包含 timestamp、actor、topic、message_type、message_id、command_id、device_id、payload action/status，以及 selected `desired.*` / `reported.*` state 欄位，不輸出 payload body、`clientToken` 或 credential material。

### `scripts/setup-staging-e2e-data.sh`

獨立的 staging E2E data setup 腳本，只建立/更新 E2E 測試資料，不 remove provider resources、不 provision servers、不跑 live MQTT。完整 `scripts/run-staging-e2e.sh` 會透過 `rtk-cloud staging-e2e-test` 呼叫這個腳本；operator 也可以單獨跑它來重建 brand/users/devices/bind artifact。

```sh
scripts/setup-staging-e2e-data.sh \
  --brandname RTK \
  --user-count 10 \
  --device-count 100 \
  --out-dir cloud_env/staging/linode/artifacts/staging-e2e/manual-data-setup
```

也可以用 staging shortcut：

```sh
./stg.sh data --brandname RTK --user-count 10 --device-count 100
```

常用選項：

- `--plan`：只列出 data setup 會執行的步驟。
- `--workspace PATH`：指定 workspace root；預設目前 checkout。
- `--env-root PATH`：指定 environment directory；預設 `cloud_env/staging`，會依 provider 自動 resolve 到 `cloud_env/staging/linode` 或 `cloud_env/staging/lke`。
- `--brandname NAME`：brand cloud 名稱；預設 `RTK`。
- `--user-count N` / `--device-count N`：建立 user/device 數量。
- `--device-mix MIX` / `--device-prefix PREFIX`：轉傳給 `generate-load-devices`。
- `--out-dir PATH`：輸出 `summary.json`、`logs/*.log` 與 `bind-validation/` 的位置；未指定時使用 `<env-root>/artifacts/staging-e2e-data/<timestamp>/`。

輸出 `summary.json` 會包含 `users_file`、`device_bind_file`、`bind_validation_dir`，以及 create brand、create users、create devices、bind devices、validate bind 每段的 status、exit code、duration seconds 和 log path。這個腳本支援 `CLOUD_PROVIDER=linode` 與 `CLOUD_PROVIDER=lke`；其他 provider 會在任何 mutation 前 fail fast。

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

可用 `./stg.sh video RTK` 執行 staging WebRTC RTP relay smoke。這個測試只選最新 bind artifact 內具備 `video_streaming` service option 的 camera device，使用 device certificate mTLS 換 device token，使用 users artifact 內 app private key + app certificate mTLS 換 device-bound app token，然後重用 `e2e_test/video_cloud/load` runner。PASS 代表 device websocket owner online、viewer 建立 WebRTC session、server 回 SDP offer 與 ICE servers、device 送 SDP answer、ICE connected/completed、device 以 2s 1080p `testsrc2` Annex-B H.264 fixture loop 10 次送出 20s H.264 RTP，payload validation 看到 SPS/PPS/IDR/non-IDR NAL types，且 session close 成功。這不是 legacy raw RTP relay 測試；PASS 來源是 WebRTC signaling + H.264 RTP payload evidence。輸出在 `<env-root>/artifacts/video-relay-test/<timestamp>/results.json` 與 `TEST_REPORT.md`，console/report 會 redacted bearer token、TURN credential、private key、CSR 與 certificate PEM。

### K8s runtime 設定與 log level

staging runtime 已改為 K8s-only，不再提供舊的 VM deploy shortcut。application service log level 應透過 K8s manifest/secret/config 管理，再用 `provision-k8s` 檢查 rollout readiness。

Staging 的平台管理頁登入帳密和 Account Manager automation token 帳密不同：

- Cloud Admin `/login?next=/admin` 使用 `cloud_env/staging/linode/services/cloud-admin/admin-staging.env` 內的 `ADMIN_BOOTSTRAP_EMAIL` / `ADMIN_BOOTSTRAP_PASSWORD`。
- `./stg.sh token` 和 e2e brand/user/bind automation 使用 `cloud_env/staging/linode/services/account-manager/account-manager-platform-admin.env` 內的 Account Manager bootstrap platform-admin 帳密。

不要用 Account Manager bootstrap 帳號登入 Cloud Admin `/admin` UI。詳細邊界見 `docs/account-manager-admin-boundary.md#staging-login-credential-boundary`。

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

- `--env-root PATH`：指定 environment directory；必填，避免檢查錯誤環境。可傳 `cloud_env/staging`，script 會自動使用其下的 `linode/`。
- `--dns-root-domain NAME`：root DNS domain，預設 `realtekconnect.com`。
- `--min-valid-days N`：要求 certificate 至少還要有效幾天，預設 `7`。
- `--skip-live`：只檢查 `cloud_env/staging/linode/certificates` 內的 cache。
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
