# cloud_env 目錄配置

`cloud_env/` 是 workspace 的本機 cloud environment 目錄，整個目錄都被 git ignore。它保存 live deployment 需要的 topology、runtime env、state、keys/certificates、device fixtures、artifacts 與 migration backup。

Environment root 採 `cloud_env/<env>/<provider>` 形式保存 provider-specific
資料。預設 staging environment directory 是 `cloud_env/staging`；目前支援的
provider 是 `linode` 與 `lke`，所以實際資料在 provider 子目錄：

```text
cloud_env/staging/linode/
  env/
  topology/
  services/
  keys/
  certificates/
  devices/
  state/
  artifacts/
  backups/
cloud_env/staging/lke/
  env/
  services/
  state/
  artifacts/
  backups/
```

## 目錄用途

- `env/`：跨服務 operator credential，例如 `operator.env` 內的 Linode、GoDaddy、Object Storage credential。
- `topology/`：環境 topology manifest，例如 `video-cloud-staging.yaml`。
- `services/`：各服務 runtime/deploy env，例如 `video-cloud/`、`account-manager/`、`cloud-admin/`。
- `keys/`：staging/production-like CA、client certificate、factory enrollment certificate，以及服務部署會用到的 private key。
- `certificates/`：公開 HTTPS certificate cache。成功取得 Let's Encrypt certificate 後，staging deploy 會把 `fullchain.pem`、`privkey.pem`、可用時的 `cert.pem`/`chain.pem` 存到 `certificates/<fqdn>/`。下次 provision/deploy 時，如果 certificate 在安全期限內尚未過期，script 會先上傳 cached certificate 到 VM，建立 certbot lineage 與 renewal config，啟用 `certbot.timer`，避免重複向 Let's Encrypt 申請並保留 host 端自動 renew。
- `devices/`：load test 或 factory rehearsal 的模擬 device credentials，例如 `test_device/`。
- `state/`：Linode VM、firewall、VPC 等已建立資源的 state file。
- `artifacts/`：provision、deploy readiness、e2e、runtime health 等輸出。
- `backups/`：migration 或 reset 前留下的 local backup。

## 操作原則

- staging scripts 需要明確指定 `--env-root PATH`，避免操作到錯誤環境。
- 可傳 `cloud_env/staging` 作為 environment directory；script 會依
  `CLOUD_PROVIDER`、`RTK_CLOUD_STAGING_PROVIDER` 或 provider stack file 自動解析
  到 `cloud_env/staging/linode` 或 `cloud_env/staging/lke`。
- `CLOUD_PROVIDER` 目前可用值是 `linode` 與 `lke`。`linode` 代表既有
  VM/systemd/manual deploy path；`lke` 代表 Linode Kubernetes Engine path，
  會使用 Linode LKE API discover/create cluster、下載 kubeconfig 到
  `state/lke-kubeconfig.yaml`，再由 kubectl 建立/刪除 namespaces、套用
  runtime-generated base resources，並在 deploy 時要求明確 container image env
  vars 或 `LKE_IMAGE_REGISTRY` 以自動 build/push images。未來若加入 AWS、GCP 或 Azure，
  應建立平行的 `cloud_env/<env>/<provider>` 目錄；在實作前，其他 provider
  必須在 preflight/provision 一開始就失敗，不可做任何 live mutation。
- `--secrets-root PATH` 只保留為舊參數 alias，新的文件與操作都使用 `--env-root`。
- `env/stack.env` 內的 `CLOUD_ENV_NAME` 是 Linode K8s staging 命名 root。`CLOUD_STACK_NAME`、公開 domain、K8s stack metadata，以及 service env 內的相關 URL 都由 `go run ./scripts/go/rtk-cloud -- sync-env --env-root cloud_env/staging` 產生。
- Generated 欄位不要手動修改。若要把環境改名，例如從 `stg-0529` 改成 `stg`，先確認舊 K8s stack 已不再使用，再修改 `CLOUD_ENV_NAME`，最後執行 `sync-env` 與 `provision-k8s` 檢查。
- HTTPS certificate cache 是 environment-local secret material，包含 private key；只放在 `cloud_env/`，不要複製到 repo 或 artifact。
- 不要 commit `cloud_env/` 裡的任何檔案。

## 命名推演

目前 `sync-env` 的 generated naming 支援 Linode provider routing。
`CLOUD_PROVIDER=linode` 保留既有 Linode VM/firewall/VPC/subnet label；
`CLOUD_PROVIDER=lke` 的 runtime CLI 使用同一組 root metadata 推導
stack/domain/namespace 名稱。Kubernetes 或跨 provider contract 不應重用
`*_LINODE_*` 欄位。production Kubernetes manifests、Helm charts、CI/CD
deployment pipelines 仍受 `docs/lke-migration-inventory.md` gates 管制。

LKE provider 的 live state 放在 `cloud_env/<env>/lke/state/`，包含
`lke.env` 和 `lke-kubeconfig.yaml`。這些檔案是 operator-local secret/runtime
material，必須維持 git ignored；kubeconfig 應以 `0600` 權限保存。

以 `CLOUD_ENV_NAME=stg`、`CLOUD_DNS_ROOT_DOMAIN=realtekconnect.com` 為例，`sync-env` 會產生：

- `CLOUD_STACK_NAME=video-cloud-stg`
- `VIDEO_CLOUD_DOMAIN=video-cloud-stg.realtekconnect.com`
- K8s stack name：`video-cloud-stg`
- Sibling service metadata：Account Manager、Cloud Admin、Video Cloud、logger/observability 會以 K8s service/secret/configmap 呈現

改名流程：

```sh
# edit cloud_env/staging/linode/env/stack.env: CLOUD_ENV_NAME=stg
go run ./scripts/go/rtk-cloud -- sync-env --env-root cloud_env/staging
go run ./scripts/go/rtk-cloud -- sync-env --env-root cloud_env/staging --check
go run ./scripts/go/rtk-cloud -- provision-k8s --env-root cloud_env/staging --confirm video-cloud-stg
```
