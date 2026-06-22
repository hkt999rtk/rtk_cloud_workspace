# cloud_env 目錄配置

`cloud_env/` 是 workspace 的本機 cloud environment 目錄，整個目錄都被 git
ignore。active deployment 只支援 Kubernetes runtime；Linode VM/systemd/SSH
runtime deployment 已退役。

Environment root 採 `cloud_env/<env>/<provider>` 形式保存 provider-specific
資料。staging 的 active LKE environment root 是：

```text
cloud_env/staging/lke/
  env/
  services/
  state/
  artifacts/
  backups/
```

未來若加入 generic Kubernetes、GKE、EKS 或 AKS，應建立平行目錄，例如
`cloud_env/staging/k8s/`、`cloud_env/staging/gke/`、`cloud_env/staging/eks/`
或 `cloud_env/staging/aks/`。第一階段只有 `lke` 具備 Linode LKE
discover/create kubeconfig 的 cloud API 流程；其他 Kubernetes provider 必須使用
既有 kubeconfig/context，尚未支援的 create/DNS/provider API 操作必須 fail fast。

## LKE 必需檔案

- `env/stack.env`：provider、stack、domain、region metadata。`CLOUD_PROVIDER`
  必須是 `lke`；`CLOUD_STACK_NAME` 是 namespace、label、domain 的命名 root。
- `state/lke.env`：LKE cluster id、label、region、version。這是
  operator-local runtime state，不可 commit。
- `state/lke-kubeconfig.yaml`：LKE kubeconfig，權限應維持 `0600`。
- `services/*/*.env`：service runtime/deploy env 與 bootstrap secret material。
  常見檔案包含 `services/account-manager/account-manager.env`、
  `services/account-manager/account-manager-platform-admin.env`、以及
  `services/video-cloud/video-cloud.env`。
- `artifacts/lke-images/lke-image-manifest.json` 和
  `artifacts/lke-images/lke-image-env.sh`：pinned submodule commits 對應的
  GHCR image mapping。`scripts/run-staging-e2e.sh` 在缺少 `LKE_*_IMAGE`
  process env 時會自動 resolve 並更新 latest artifact。

## 目錄用途

- `env/`：環境 metadata。LKE operator credential 來源是 shell env 或 `~/.env`，
  不是 env-root 內的 `operator.env`。
- `services/`：各服務 runtime env 與 bootstrap secrets。
- `state/`：Kubernetes/LKE runtime state、kubeconfig、OpenBao/certissuer
  local secret material。
- `artifacts/`：image manifest、provision/e2e/load-test/report output。
- `devices/`：load test 或 factory rehearsal 的模擬 device credentials。
- `backups/`：migration 或 reset 前留下的 local backup。

## 操作原則

- staging scripts 需要明確指定 `--env-root PATH`，避免操作到錯誤環境。
- 可傳 `cloud_env/staging` 作為 staging directory；script 會依
  `CLOUD_PROVIDER`、`RTK_CLOUD_STAGING_PROVIDER` 或 provider stack file 自動解析到
  `cloud_env/staging/lke` 或其他 Kubernetes provider 子目錄。沒有設定時預設
  使用 `lke`。
- `CLOUD_PROVIDER=linode` 只代表已退役的 VM runtime，active deployment 不可使用。
- `--secrets-root PATH` 只保留為舊參數 alias，新的文件與操作都使用 `--env-root`。
- `sync-env --check` 對 LKE 只檢查 stack metadata 與存在的 service env，不要求
  legacy VM `topology/video-cloud-staging.yaml`。
- 不要 commit `cloud_env/` 裡的任何檔案。

## 常用指令

```sh
go run ./scripts/go/rtk-cloud -- sync-env --env-root cloud_env/staging/lke --check
go run ./scripts/go/rtk-cloud -- provision --env-root cloud_env/staging/lke --preflight --plan --confirm video-cloud-staging
scripts/run-staging-e2e.sh --plan --env-root cloud_env/staging/lke
```

Legacy Linode VM deployment snapshot 只作歷史參考；不要從 active runbook 或
automation 重新導入 `topology/`、`operator.env`、systemd、SSH deploy、VM state
或 service-owned VM runtime path。歷史資料只看
`docs/linode-staging-deployment-snapshot.md`。
