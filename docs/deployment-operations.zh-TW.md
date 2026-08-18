# RTK Cloud Deployment 操作指南

Status: active

Owner: `rtk_cloud_workspace`

Last reviewed: 2026-08-11

Audience: internal deployment operators and new maintainers

本文件是內部 operator 的 deployment 起點。適用於建立全新 environment、在另一台
controller 接管既有 environment，以及對既有 deployment 執行驗收。Linode staging
只使用 LKE/Kubernetes；舊 VM runtime 不是 active deployment path。

## 先選擇操作情境

| 情境 | 正確入口 | 是否修改 cloud resource |
| --- | --- | --- |
| 檢查 tracked config | `deployment preflight --operation plan` | 否 |
| 建立全新 environment | `deployment plan` → `deployment provision` | provision 會 |
| 接管既有 environment | 還原完整 runtime → `deployment preflight --operation acceptance` | preflight 不會 |
| 驗收既有 environment | `deployment acceptance` | 會建立或更新測試資料，不重建 deployment |
| 一次性 environment rehearsal | `deployment test` | 會建立，最後也會清除所屬資源 |
| 移除 environment | `deployment remove` | 會刪除該 stack 所屬資源 |

所有 active runtime 一律位於：

```text
cloud_env/<environment>/runtime
```

`cloud_env/staging/lke` 與 `cloud_env/staging/linode` 都不是 active input。不要把舊路徑
複製、rename 或 symlink 成 runtime；既有 environment 必須還原與該 cluster 相符的完整
runtime state。

## Deployment 前要準備的資訊

### 帳號與權限

- Workspace 與所有 private submodule 的 Git read access。
- Linode account access，以及可建立 LKE、VM、firewall、VPC、volume 與 Object Storage
  resource 的 `LINODE_TOKEN`。
- Linode account owner 確認的 active-service limit。這個數字無法由 Linode API 查詢，
  不可自行猜測。
- GHCR `read:packages` 權限，用來拉取 pinned service commit 對應的 image。
- Environment 所選 DNS provider 的 record mutation 權限；staging 預設為 GoDaddy。
- 可建立 temporary load-generator VM 時使用的 SSH key pair。

### Controller 工具

Fresh controller 至少需要 Git、Go、`kubectl`、Helm、`certbot`、`curl`、`jq`、OpenSSL
與 SSH。執行 load test 還需要 Ansible；需要本機 image 操作時才需要 Docker。

先初始化完整 checkout：

```sh
git submodule update --init --recursive
```

### Operator-local `~/.env`

範例位於 [`examples/operator.env.example`](examples/operator.env.example)。人工將需要的
key 合併到既有 `~/.env`；不要用 `cp` 覆寫整個檔案，也不要 commit。

目前 LKE + GoDaddy staging 的主要項目是：

```env
LINODE_TOKEN=<redacted>
GHCR_PULL_USERNAME=<github-user>
GHCR_PULL_TOKEN=<redacted>
GODADDY_KEY=<redacted>
GODADDY_SECRET=<redacted>
```

Credential precedence：

1. 目前 process environment。
2. Environment runtime 的 operator-local env（該功能支援時）。
3. `~/.env`。

Route53 不使用 GoDaddy keys，改走 AWS SDK default credential chain。Secret 只可存在
operator secret source 或 ignored runtime，不可寫進 tracked environment config、PR、issue、
聊天訊息或測試報告。

### Tracked environment 與 ignored runtime

| 位置 | 內容 | 可否 commit |
| --- | --- | --- |
| `cloud_env/<env>/environment.env` | stack、DNS root、logical location | 可以 |
| `cloud_env/<env>/deployment.env` | architecture、deployment adapter、DNS adapter | 可以 |
| `cloud_env/<env>/overrides/*.env` | 經審查的 environment 差異 | 可以 |
| `cloud_env/<env>/runtime/` | kubeconfig、provider state、OpenBao、service secrets、test identity、artifacts | 不可以 |
| `runtime/adapters/lke/account.env` | operator 確認的 active-service limit | 不可以 |

建立 active-service limit state：

```sh
mkdir -p cloud_env/staging/runtime/adapters/lke
cp cloud_env/staging/runtime/adapters/lke/account.env.example \
  cloud_env/staging/runtime/adapters/lke/account.env
chmod 600 cloud_env/staging/runtime/adapters/lke/account.env
```

將 example value 改成 Linode account owner 已確認的實際上限。

## 唯讀 preflight

Preflight 不 materialize runtime、不修改 tracked file，也不呼叫 cloud mutation API：

```sh
go run ./scripts/go/rtk-cloud -- deployment preflight \
  --environment staging \
  --operation plan

go run ./scripts/go/rtk-cloud -- deployment preflight \
  --environment staging \
  --operation provision
```

`plan` 驗證 tracked environment schema 與基本工具；`provision` 另外驗證 provider、DNS、
GHCR、SSH key、active-service limit 與 existing-cluster safety state。輸出只顯示
`PASS/WARN/FAIL`，不顯示 credential value。任一 `FAIL` 都必須先修正，不可直接跳到
provision。

## 建立全新 environment

只有確定目標 cluster/storage 不存在時才走這條路徑。若同名 cluster 已存在，停止並改走
「接管既有 environment」。

1. 依 [`../cloud_env/README.md`](../cloud_env/README.md) 建立或審查 tracked config。
2. 執行 `preflight --operation provision`。
3. 產生 sanitized plan：

   ```sh
   go run ./scripts/go/rtk-cloud -- deployment plan --environment staging
   ```

4. 審查 plan 內的 stack、provider region、resolved SKU、node class、replicas、storage、DNS、
   image 與 projected active services。Topology 數字只以本次 resolved plan 為準。
5. 明確核准後才 mutation：

   ```sh
   go run ./scripts/go/rtk-cloud -- deployment provision \
     --environment staging \
     --confirm video-cloud-staging
   ```

6. Provision 完成後執行 acceptance：

   ```sh
   go run ./scripts/go/rtk-cloud -- deployment preflight \
     --environment staging --operation acceptance
   go run ./scripts/go/rtk-cloud -- deployment acceptance \
     --environment staging --confirm video-cloud-staging
   ```

完整的 staging + billing + 1K MQTT/Device Shadow sequence 見
[`staging-from-scratch.md`](staging-from-scratch.md)。

## 接管既有 environment

Git clone 不包含 runtime。接管同一個 cluster 時，必須從 current operator 或核准的 encrypted
backup 還原完整 `cloud_env/<env>/runtime/`，不能只複製 kubeconfig，也不能從空白 runtime
重新 provision。

```sh
scripts/restore-staging-runtime.sh \
  --source-runtime /secure/path/cloud_env/staging/runtime \
  --target-runtime "$PWD/cloud_env/staging/runtime"

scripts/restore-staging-runtime.sh \
  --check-only \
  --target-runtime "$PWD/cloud_env/staging/runtime"
```

接著執行 acceptance preflight。它會檢查 runtime identity、provider metadata、kubeconfig、
OpenBao/PostgreSQL state 及 Kubernetes API access；任何 missing state 都代表交接不完整。
更詳細的傳輸與權限要求見
[`staging-runtime-bootstrap.zh-TW.md`](staging-runtime-bootstrap.zh-TW.md)。

## Rehearsal、移除與證據

`deployment test` 固定執行 `provision → acceptance → cleanup`，只可用於原本不存在的
ephemeral stack：

```sh
go run ./scripts/go/rtk-cloud -- deployment preflight \
  --environment dev --operation ephemeral-test
scripts/deploy-environment.sh test \
  --environment dev --confirm video-cloud-dev
```

移除既有 environment 是 destructive operation：

```sh
go run ./scripts/go/rtk-cloud -- deployment remove \
  --environment dev --confirm video-cloud-dev
```

執行前先備份需要保留的 runtime/evidence，並確認 stack identity。不要把 staging、CI runner、
release bucket 或不相關 resource 當成 rehearsal cleanup target。

成功交接至少要保存：resolved deployment plan、exact workspace/submodule commits、acceptance
report、Kubernetes rollout/health 結果，以及所有 skipped/blocked checks。Report 只能包含
sanitized evidence。

## 故障分流

- `LINODE_TOKEN`／DNS／GHCR missing：補 operator-local credential，禁止寫入 tracked env。
- existing cluster 缺 OpenBao/PostgreSQL state：停止 provision，還原 matching runtime。
- kubeconfig 不可用：確認 runtime 來源與權限，不要任意建立新 credential 配舊 storage。
- image resolution 失敗：確認 pinned service commit 的 GHCR image 已發布且 token 有
  `read:packages`。
- active-service limit 未知：向 Linode account owner 確認；不可用預設值猜測。
- deployment 成功但測試失敗：依 [`testing-operations.zh-TW.md`](testing-operations.zh-TW.md)
  先跑 acceptance，再判斷 data、MQTT、API、database 或 generator bottleneck。
