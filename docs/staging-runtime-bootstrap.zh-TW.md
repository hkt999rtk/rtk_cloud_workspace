# Staging Runtime 建立與還原

本文件說明另一台 macOS clone workspace 後，如何準備 MQTT/shadow load test
所需的 staging runtime。

## 先了解 Git 邊界

Git 只保存 environment 的宣告設定；`cloud_env/<environment>/runtime/`
只保存非敏感的 generated runtime。kubeconfig、service secret、private key、
device credential 與 SQLite credential DB 全部由
`~/.config/rtk_cloud/<environment>/` 管理，不能 commit 到 Git。

因此，單獨執行 `git clone` 不會得到可直接執行 load test 的完整環境。

## 既有 staging：從另一台 Mac 還原

這是同一個 staging cluster 與既有 device/user identity 的標準流程。先在
原本已完成 setup 的機器確認 SecretStore 與非敏感 runtime：

```sh
cd /path/to/rtk_cloud_workspace

go run ./scripts/go/rtk-cloud -- secrets verify --environment staging
find cloud_env/staging/runtime \
  \( -path '*/state/provider-preflight.env' -o -path '*/env/stack.env' \) -print
```

使用加密且受控的檔案傳輸方式，將整個 staging SecretStore 複製到新機器；
目的 environment 必須尚未存在，且不可與 prod 共用或 fallback：

```sh
rsync -a --delete \
  /secure/source/rtk_cloud/staging/ \
  "$HOME/.config/rtk_cloud/staging/"

find "$HOME/.config/rtk_cloud/staging" -type d -exec chmod 700 {} +
find "$HOME/.config/rtk_cloud/staging" -type f -exec chmod 600 {} +
go run ./scripts/go/rtk-cloud -- secrets verify --environment staging
```

`rsync --delete` 的來源與目的地必須先確認；不要把 workspace root 或其他
廣泛目錄當成目的地。

遠端 deploy runner 可使用 workspace 提供的 restore/check 工具。先將
known-good 的非敏感 runtime 以受控方式放到遠端可讀取的位置，再在遠端 workspace 執行：

```sh
scripts/restore-staging-runtime.sh \
  --source-runtime /secure/path/cloud_env/staging/runtime \
  --target-runtime "$PWD/cloud_env/staging/runtime"
```

若 runtime 已由其他安全管道還原，只做檢查：

```sh
scripts/restore-staging-runtime.sh \
  --check-only \
  --target-runtime "$PWD/cloud_env/staging/runtime"
```

restore 工具只處理非敏感 runtime；SecretStore 必須另外準備並通過
`secrets verify`。GitHub Actions 應把 GitHub Secrets 寫入 job 專用的暫時
`RTK_CLOUD_CONFIG_ROOT`，job 結束後刪除，不能靠 `git clone` 取得。

還原後，從新 workspace 執行：

```sh
cd /path/to/new-workspace

export HOME100K_ENV_ROOT="$PWD/cloud_env/staging/runtime"

test -s "$HOME100K_ENV_ROOT/state/provider-preflight.env"
test -s "$HOME100K_ENV_ROOT/env/stack.env"
test -s "$HOME/.config/rtk_cloud/staging/kube/kubeconfig.yaml"
test -s "$HOME/.config/rtk_cloud/staging/test/devices/test_device/loadtest.env"
test -s "$HOME/.config/rtk_cloud/staging/test/databases/rtk-test-data.sqlite"

loadtests/home-100k/scripts/home-100k.sh plan
```

SQLite test data 必須沿用原本 staging identity。不要在新 Mac 隨意重新產生
users/devices 或 rotate credentials，否則資料庫內的 credentials、device
binding 與 staging server state 可能不一致。

### LKE active-service limit

LKE account limit 也不會隨 Git clone 取得。請在 deployment mutation 前建立：

```text
cloud_env/staging/runtime/adapters/lke/account.env
```

Repository 提供可追蹤的範例檔：

```text
cloud_env/staging/runtime/adapters/lke/account.env.example
```

執行者先複製它，再填入 Linode 已確認的實際上限：

```sh
cp cloud_env/staging/runtime/adapters/lke/account.env.example \
  cloud_env/staging/runtime/adapters/lke/account.env
chmod 600 cloud_env/staging/runtime/adapters/lke/account.env
```

內容使用 Linode 已確認的 account limit，例如：

```env
LKE_ACTIVE_SERVICE_LIMIT=20
```

這是 safety number，不是另一個 secret。Linode API 目前不能透過
`LINODE_TOKEN` 查詢 account limit；值不明時不要猜測，應先向 Linode/account
owner 確認。Deployment 會在建立任何付費資源前，比較現有 active services
加上 planned resources 與這個 limit。

如果原本 workspace 已經完成 staging setup，可複製 operator state：

```sh
mkdir -p cloud_env/staging/runtime/adapters/lke
cp /path/to/known-good-workspace/cloud_env/staging/runtime/adapters/lke/account.env \
  cloud_env/staging/runtime/adapters/lke/account.env
chmod 600 cloud_env/staging/runtime/adapters/lke/account.env
```

這個檔案位於被 Git 忽略的 runtime；不要 commit，也不要放進 tracked
`cloud_env/staging/overrides/*.env`。

## 全新 staging：重新建立 runtime

只有在要建立新的 staging environment、或原本 runtime 已不可恢復時，才走
這條路徑。這些命令可能建立或修改雲端資源：

```sh
go run ./scripts/go/rtk-cloud -- deployment plan --environment staging
go run ./scripts/go/rtk-cloud -- deployment provision \
  --environment staging \
  --confirm video-cloud-staging
```

完成 deployment 後，依 staging runbook 取得 kubeconfig、同步 normalized
environment metadata，並產生 load-test data：

```sh
go run ./scripts/go/rtk-cloud -- sync-env \
  --env-root cloud_env/staging

go run ./scripts/go/rtk-cloud -- generate-load-devices \
  --env-root cloud_env/staging \
  --brandname RTK
```

`generate-load-devices` 會把 device credentials 與 bindings 使用的 SQLite
寫入 runtime artifacts；完整的 users/devices 建立與 bind 順序請依
[`scripts/README.zh-TW.md`](../scripts/README.zh-TW.md) 執行。不要把全新產生的
資料混用到原本 staging cluster。

## Load test 啟動前檢查

預設 runtime 路徑是：

```text
cloud_env/staging/runtime
```

若使用其他位置，必須設定 `HOME100K_ENV_ROOT`。以下五項都必須存在：

| Path | 來源 | 性質 |
| --- | --- | --- |
| `state/provider-preflight.env` | deployment plan/provision | generated runtime |
| `env/stack.env` | sync/deployment flow | generated runtime |
| `adapters/lke/account.env` | Linode account confirmation | operator safety state |
| `~/.config/rtk_cloud/staging/kube/kubeconfig.yaml` | Kubernetes/provider access | secret |
| `~/.config/rtk_cloud/staging/test/devices/...` | load-device identity | secret credentials |
| `~/.config/rtk_cloud/staging/test/databases/...` | users/devices/bind flow | secret credentials |

Home MQTT runner 的 credential input 必須位於 SecretStore 的
`test/devices/`；workspace runtime 不得保留第二份副本。

若收到 `required prerequisites are missing`，先檢查 `HOME100K_ENV_ROOT` 是否
指向 runtime 目錄，而不是 `cloud_env/staging` 或舊的
`cloud_env/staging/linode` path。舊的 provider-specific path 不能取代目前的
normalized `cloud_env/staging/runtime`。

## 安全要求

- 不要 commit runtime、kubeconfig、SQLite、private key、certificate 或 service secrets。
- 不要在 issue、PR、聊天訊息或 log 貼出 SQLite 內容或 kubeconfig 內容。
- 傳輸 SecretStore 前後都確認目錄為 `0700`、檔案為 `0600`。
- 若 runtime 遺失，先判斷是否能從原 workspace 還原；只有確定要建立新 staging 時才重新 provision 或重新 enroll。
