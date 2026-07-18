# Staging Runtime 建立與還原

本文件說明另一台 macOS clone workspace 後，如何準備 MQTT/shadow load test
所需的 staging runtime。

## 先了解 Git 邊界

Git 只保存 environment 的宣告設定；`cloud_env/<environment>/runtime/`
是本機產生的 runtime，整個目錄都被 `.gitignore` 忽略。它包含 kubeconfig、
service state、device credentials、certificates 與 SQLite test data，不能
commit 到 Git。

因此，單獨執行 `git clone` 不會得到可直接執行 load test 的完整環境。

## 既有 staging：從另一台 Mac 還原

這是同一個 staging cluster 與既有 device/user identity 的標準流程。先在
原本已完成 setup 的 workspace 確認 runtime：

```sh
cd /path/to/rtk_cloud_workspace

find cloud_env/staging/runtime \
  \( -path '*/state/provider-preflight.env' \
  -o -path '*/state/kubeconfig.yaml' \
  -o -path '*/env/stack.env' \
  -o -path '*/devices/test_device/loadtest.env' \
  -o -path '*/artifacts/test-data/*-test-data.sqlite' \) \
  -print
```

使用加密且受控的檔案傳輸方式，將整個 runtime 複製到新 workspace；不要把
這些檔案加入 Git：

```sh
rsync -a --delete \
  /path/to/old-workspace/cloud_env/staging/runtime/ \
  /path/to/new-workspace/cloud_env/staging/runtime/

chmod 600 \
  /path/to/new-workspace/cloud_env/staging/runtime/state/kubeconfig.yaml \
  /path/to/new-workspace/cloud_env/staging/runtime/state/provider-preflight.env \
  /path/to/new-workspace/cloud_env/staging/runtime/artifacts/test-data/*-test-data.sqlite
```

`rsync --delete` 的來源與目的地必須先確認；不要把 workspace root 或其他
廣泛目錄當成目的地。

還原後，從新 workspace 執行：

```sh
cd /path/to/new-workspace

export HOME100K_ENV_ROOT="$PWD/cloud_env/staging/runtime"

test -s "$HOME100K_ENV_ROOT/state/provider-preflight.env"
test -s "$HOME100K_ENV_ROOT/state/kubeconfig.yaml"
test -s "$HOME100K_ENV_ROOT/env/stack.env"
test -s "$HOME100K_ENV_ROOT/devices/test_device/loadtest.env"
test -s "$HOME100K_ENV_ROOT/artifacts/test-data/rtk-test-data.sqlite"

loadtests/home-100k/scripts/home-100k.sh plan
```

SQLite test data 必須沿用原本 staging identity。不要在新 Mac 隨意重新產生
users/devices 或 rotate credentials，否則資料庫內的 credentials、device
binding 與 staging server state 可能不一致。

## 全新 staging：重新建立 runtime

只有在要建立新的 staging environment、或原本 runtime 已不可恢復時，才走
這條路徑。這些命令可能建立或修改雲端資源：

```sh
go run ./scripts/go/rtk-cloud -- deployment plan --environment staging
go run ./scripts/go/rtk-cloud -- deployment provision \\
  --environment staging \\
  --confirm video-cloud-staging
```

完成 deployment 後，依 staging runbook 取得 kubeconfig、同步 normalized
environment metadata，並產生 load-test data：

```sh
go run ./scripts/go/rtk-cloud -- sync-env \\
  --env-root cloud_env/staging

go run ./scripts/go/rtk-cloud -- generate-load-devices \\
  --env-root cloud_env/staging \\
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
| `state/kubeconfig.yaml` | Kubernetes/provider access | secret |
| `env/stack.env` | sync/deployment flow | generated runtime |
| `devices/test_device/loadtest.env` | load-device generation flow | generated test config |
| `artifacts/test-data/<brand>-test-data.sqlite` | users/devices/bind flow | secret credentials |

目前 `main` 的 Home MQTT runner 使用 `devices/test_device/loadtest.env`；
`generate-load-devices` 會將 `loadtest.env` 寫入 `--out-dir`，預設就是這個
runtime 目錄。若某個外部 wrapper 額外列出 `env/loadtest.env`，必須依該
wrapper 的 contract 產生或同步該檔案，但不要以它取代 canonical path。

若收到 `required prerequisites are missing`，先檢查 `HOME100K_ENV_ROOT` 是否
指向 runtime 目錄，而不是 `cloud_env/staging` 或舊的
`cloud_env/staging/linode` path。舊的 provider-specific path 不能取代目前的
normalized `cloud_env/staging/runtime`。

## 安全要求

- 不要 commit runtime、kubeconfig、SQLite、private key、certificate 或 service secrets。
- 不要在 issue、PR、聊天訊息或 log 貼出 SQLite 內容或 kubeconfig 內容。
- 傳輸 runtime 前後都確認檔案權限為 `0600`。
- 若 runtime 遺失，先判斷是否能從原 workspace 還原；只有確定要建立新 staging 時才重新 provision 或重新 enroll。
