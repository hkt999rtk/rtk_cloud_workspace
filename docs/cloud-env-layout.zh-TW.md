# Cloud environment 與 deployment 目錄配置

RTK Cloud deployment 分成三個互相獨立的層次：

1. `cloud_deploy/architectures/` 保存跨環境、跨 provider 的 workload 與 topology intent。
2. `cloud_deploy/adapters/` 保存 LKE、EKS、GKE 等 deployment adapter contract。
3. `cloud_env/<environment>/` 保存 dev、staging、prod 等環境 instance 與本機 runtime。

Architecture 不屬於 staging；LKE 也不是 staging 的子目錄。任一 environment 都可選擇任一與 architecture 相容的 adapter。

## 版控目錄

```text
cloud_deploy/
  architectures/kubernetes/
    architecture.env
    capacity.env
    topology.env
    workloads.env
  adapters/
    lke/{defaults.env,schema.env}
    eks/schema.env
    gke/schema.env

cloud_env/<environment>/
  environment.env
  deployment.env
  overrides/
    architecture.env
    adapter.env
```

`environment.env` 定義 environment identity、stack 與 DNS root。`deployment.env` 只選擇 architecture 與 adapter。`overrides/architecture.env` 只可覆寫 provider-neutral keys；`overrides/adapter.env` 只可覆寫被選 adapter 的 keys。

## 本機 runtime

`cloud_env/<environment>/runtime/` 全部 git ignored：

```text
runtime/
  resolved/{deployment.env,deployment-plan.json}
  state/{kubeconfig.yaml,topology.json}
  adapters/<adapter>/{state.env,resources.json}
  services/
  secrets/
  devices/
  artifacts/
  backups/
```

Shared Kubernetes runtime 與 load test 只讀 normalized `runtime/state`、`runtime/services`、`runtime/devices` 與 `runtime/artifacts`。Cluster ID、node-pool ID、Linode resource ID 等 provider state 只能存在 `runtime/adapters/lke/`。

## 設定解析順序

Resolver 依序合成 architecture defaults、adapter defaults、environment identity/selection、environment overrides，以及明確允許的 CLI/process override。Adapter keys 不得覆寫 architecture keys；architecture 不得出現 `LKE_*`、`EKS_*` 或 `GKE_*`。

正式 operator 介面使用 environment 名稱：

```sh
go run ./scripts/go/rtk-cloud -- deployment plan --environment staging
go run ./scripts/go/rtk-cloud -- deployment provision --environment staging --confirm video-cloud-staging
go run ./scripts/go/rtk-cloud -- deployment acceptance --environment staging
```

舊 `cloud_env/staging/lke` 不搬移、不刪除，也不再是 active input。新 runtime 從空白重新產生；命令收到舊路徑時必須 fail fast。

空白 runtime 只能搭配全新的 cluster/storage。若 adapter 發現同名 LKE cluster 已存在，但新 environment 缺少 OpenBao operator state 或 PostgreSQL secret state，provision 必須在 mutation 前失敗。Operator 必須明確選擇還原該 environment state，或依核准的 destructive runbook 重建 cluster 與 persistent storage；不得混用新 secrets 與舊 PVC。
