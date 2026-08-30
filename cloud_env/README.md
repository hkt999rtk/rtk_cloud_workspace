# 建立與設定 environment

這是新增 `dev`、`staging`、`prod`、`qa` 或其他 deployment environment 的操作入口。架構責任與解析原理見 [`docs/cloud-deployment-architecture.md`](../docs/cloud-deployment-architecture.md)；共用 defaults 與 adapter keys 見 [`cloud_deploy/README.md`](../cloud_deploy/README.md)；多環境 Object Storage 與憑證生命週期見 [`docs/storage-credential-lifecycle.md`](../docs/storage-credential-lifecycle.md)。

要從全新 clone 建立 LKE staging、完成服務驗收並執行 1K MQTT/Device
Shadow 測試，請依序執行 [`staging-from-scratch.md`](../docs/staging-from-scratch.md)。不要把該流程用於
已存在的 cluster；既有環境應先安全還原其 ignored `runtime/`。

## 建立最小設定

Environment identity 直接取自 `cloud_env/` 下的目錄名稱；名稱使用小寫英數字與 `-`。不要複製既有 environment 的 `runtime/`。

```sh
environment=qa
mkdir -p "cloud_env/$environment/overrides"
```

建立 `cloud_env/qa/environment.env`：

```env
CLOUD_STACK_NAME=video-cloud-qa
CLOUD_DNS_ROOT_DOMAIN=realtekconnect.com
DEPLOYMENT_LOCATION=us-west
```

| Key | 必填 | 用途 |
| --- | --- | --- |
| `CLOUD_STACK_NAME` | 是 | Cluster、namespace、DNS 與 destructive confirmation 使用的唯一 stack 名稱。 |
| `CLOUD_DNS_ROOT_DOMAIN` | 是 | Public service hostname 的根網域。 |
| `DEPLOYMENT_LOCATION` | 是 | Provider-neutral logical location；adapter 會轉換成實際 provider region。 |

建立 `cloud_env/qa/deployment.env`：

```env
DEPLOYMENT_ARCHITECTURE=kubernetes
DEPLOYMENT_ADAPTER=lke
DNS_ADAPTER=godaddy
```

| Key | 必填 | 用途 |
| --- | --- | --- |
| `DEPLOYMENT_ARCHITECTURE` | 是 | 選擇共用 architecture；目前為 `kubernetes`。 |
| `DEPLOYMENT_ADAPTER` | 是 | 選擇 provider adapter；目前只有 `lke` 支援 mutation，`eks`、`gke` 只會 validate 後 fail fast。 |
| `DNS_ADAPTER` | 是 | 獨立選擇 DNS provider；`godaddy` 與 `route53` 都支援 mutation。 |

## 選填 overrides

沒有差異時不需要建立 override 檔。需要調整 workload、capacity 或 topology 時，將 [`cloud_deploy/architectures/kubernetes/`](../cloud_deploy/architectures/kubernetes/) 已存在的 key 寫入 `overrides/architecture.env`：

```env
CAPACITY_TARGET_CONNECTIONS=1000
CAPACITY_ACTIVE_DEVICES=1000
MQTT_MIN_REPLICAS=2
VIDEO_CLOUD_API_MIN_REPLICAS=2
NODE_CLASS_BROKER_MIN_COUNT=2
NODE_CLASS_BROKER_MIN_VCPU=4
NODE_CLASS_BROKER_MIN_MEMORY_GIB=8
```

Environment 不指定 LKE region、Linode type 或 provider account quota。Adapter 根據 logical location 與 node-class resource minima 選擇 provider resources，實際結果只寫入 ignored runtime evidence。

`overrides/adapter.env` 只保留給經審查的 provider escape hatch，不是建立 environment 的標準設定。一般 dev、staging、prod 應保持空白。Provider account limit 由 operator 放在 ignored runtime：

```env
# cloud_env/qa/runtime/adapters/lke/account.env
LKE_ACTIVE_SERVICE_LIMIT=20
```

Staging 的實際檔案位置是：

```text
cloud_env/staging/runtime/adapters/lke/account.env
```

`LKE_ACTIVE_SERVICE_LIMIT` 是 Linode account 允許的 active-service 上限，
不是另一個 API secret，也不是 architecture/default config。Linode API
目前不提供可由 `LINODE_TOKEN` 查詢這個上限的 endpoint，因此 operator 必須
依 Linode account confirmation 手動設定。Deployment 會在任何付費資源建立前
比較 `current active services + planned resources` 與這個上限；值不明時應
停止，不要猜測。

建立 staging account state：

```sh
mkdir -p cloud_env/staging/runtime/adapters/lke
cp cloud_env/staging/runtime/adapters/lke/account.env.example \
  cloud_env/staging/runtime/adapters/lke/account.env

chmod 600 cloud_env/staging/runtime/adapters/lke/account.env
```

也可以直接編輯 `account.env`，將 example 裡的 `20` 換成 Linode 確認的實際
上限。

如果另一台已完成 staging setup 的 workspace 已有這個檔案，可以安全複製其
operator state；不要把它 commit：

```sh
cp /path/to/known-good-workspace/cloud_env/staging/runtime/adapters/lke/account.env \
  cloud_env/staging/runtime/adapters/lke/account.env
chmod 600 cloud_env/staging/runtime/adapters/lke/account.env
```

`20` 只是已確認 account limit 的例子；執行者必須以自己 Linode account
收到的實際上限替換。檔案位於被 Git 忽略的 `runtime/`，不會隨 `git clone`
取得。

Architecture override 不得包含 provider key；adapter override 不得包含 workload、capacity 或 topology key。Unknown key、錯誤型別與跨層 key 會在 plan 階段失敗。

### Certificate algorithm policy

Kubernetes environments resolve three provider-neutral certificate settings from
the architecture layer. An environment may pin them in
`overrides/architecture.env` when its policy must remain explicit:

```env
CERTIFICATE_INTERNAL_TLS_KEY_ALGORITHM=ed25519
CERTIFICATE_APP_CSR_KEY_ALGORITHMS=ed25519,p256
CERTIFICATE_DEVICE_CSR_KEY_ALGORITHMS=ed25519,p256
```

The internal TLS setting controls new certissuer service, OpenBao TLS, and MQTT
server keys. Changing it rotates those environment-owned TLS trust sets during
the next provision. The app and device settings are ordered CSR preferences;
they affect only newly generated credentials and do not revoke existing client
certificates. Supported canonical values are `ed25519` and `p256`; aliases,
empty lists, and duplicate list entries fail deployment preflight.

OpenBao's staging device/app issuer CAs may use RSA while issuing certificates
for Ed25519 or P-256 subject keys. These settings do not control public ACME
certificates, JWT/EdDSA token signing, OTA signing, SSH keys, or PKCS#11 signer
selection.

每個 environment 也必須追蹤 `storage.env`，宣告 runtime media policy、bucket 與 environment-owned prefix；範例與 lifecycle 見 [`docs/storage-credential-lifecycle.md`](../docs/storage-credential-lifecycle.md)。

DNS provider 的選填 escape hatch 使用 `overrides/dns.env`。一般 environment 不設定 hosted-zone ID、API endpoint、AWS access key 或 GoDaddy key。GoDaddy credentials 只從 `~/.config/rtk_cloud/<environment>/operator/env/` 讀取；Route53 所需 credentials 也必須存入同一 environment store。詳細設定見 [`docs/secret-store.md`](../docs/secret-store.md) 與 [`docs/dns-adapter-architecture.md`](../docs/dns-adapter-architecture.md)。

## 驗證與 provision

先確認 tracked config 不會被 Git ignore，再執行不寫入 runtime、不修改 cloud resource
的 preflight：

```sh
git check-ignore "cloud_env/qa/environment.env" || true
go run ./scripts/go/rtk-cloud -- deployment preflight \
  --environment qa \
  --operation provision
go run ./scripts/go/rtk-cloud -- deployment plan --environment qa
```

檢查 generic plan 中的 environment、logical location、minimum/effective replicas、每類 node 的 aggregate requests 與 effective count，再檢查 adapter-private `runtime/adapters/lke/resolved-resources.env` 的實際 region/Product。Load test 只使用 normalized `runtime/state/provider-preflight.env`，不讀 adapter-private state。確認無誤後才執行 mutation：

```sh
go run ./scripts/go/rtk-cloud -- deployment provision \
  --environment qa \
  --confirm video-cloud-qa
```

## 統一的 environment test lifecycle

所有 environment 使用同一支 script 與相同參數格式；不要為 dev、staging 或
prod 複製 deployment script：

```sh
scripts/deploy-environment.sh plan --environment dev
scripts/deploy-environment.sh test --environment dev --confirm video-cloud-dev
scripts/deploy-environment.sh test --environment staging --confirm video-cloud-staging
scripts/deploy-environment.sh test --environment prod --confirm video-cloud-prod
```

`test` 固定執行 `provision -> acceptance -> cleanup`。Provision 或 acceptance
失敗時仍會執行 cleanup。Cleanup 只依該 environment 的 stack ownership 移除
DNS、LKE、VM、firewall、VPC 與 environment-owned empty Object Storage bucket；
sanitized test evidence 保留在 ignored `runtime/artifacts/`。如果任何 provider
resource 無法移除，整個 test 必須回報失敗，不得把 environment 視為已清除。
執行 public HTTPS DNS-01 前，operator host 必須已安裝 `certbot`，且 GoDaddy
credential 必須能讀寫 `CLOUD_DNS_ROOT_DOMAIN` 的 DNS records。

LKE mutation 需要 operator 提供 `LINODE_TOKEN`。Token、kubeconfig、certificates、service secrets、device credentials、SQLite、state 與 artifacts 只能存在 `cloud_env/qa/runtime/` 或 operator secret source，不得寫入上述 tracked `.env` 檔，也不得 commit。

## Review checklist

- Environment 目錄名稱清楚且使用小寫英數字與 `-`。
- `CLOUD_STACK_NAME` 與 DNS 名稱不會碰撞其他 environment。
- 只覆寫與此 environment 確實不同的值。
- Architecture override 沒有 `LKE_*`、`EKS_*`、`GKE_*`。
- dev、staging、prod 的 adapter override 不包含正常 region、Product 或 quota。
- Provider account limit 只存在 ignored adapter runtime，不進版控。
- DNS adapter 已選擇，且 environment 沒有 provider zone ID 或 DNS credentials。
- `deployment plan` 成功且 resolved plan 不含 secret。
- `runtime/` 保持 ignored，沒有從其他 environment 複製 state。
