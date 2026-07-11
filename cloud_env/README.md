# 建立與設定 environment

這是新增 `dev`、`staging`、`prod`、`qa` 或其他 deployment environment 的操作入口。架構責任與解析原理見 [`docs/cloud-deployment-architecture.md`](../docs/cloud-deployment-architecture.md)；共用 defaults 與 adapter keys 見 [`cloud_deploy/README.md`](../cloud_deploy/README.md)。

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

Architecture override 不得包含 provider key；adapter override 不得包含 workload、capacity 或 topology key。Unknown key、錯誤型別與跨層 key 會在 plan 階段失敗。

DNS provider 的選填 escape hatch 使用 `overrides/dns.env`。一般 environment 不設定 hosted-zone ID、API endpoint、AWS access key 或 GoDaddy key。GoDaddy credentials 由 operator secret source 提供；Route53 使用 AWS SDK default credential chain並依 `CLOUD_DNS_ROOT_DOMAIN` 自動尋找唯一 public hosted zone。詳細設定與切換流程見 [`docs/dns-adapter-architecture.md`](../docs/dns-adapter-architecture.md)。

## 驗證與 provision

先確認 tracked config 不會被 Git ignore，再產生 sanitized plan：

```sh
git check-ignore "cloud_env/qa/environment.env" || true
go run ./scripts/go/rtk-cloud -- deployment plan --environment qa
```

檢查 generic plan 中的 environment、logical location、minimum/effective replicas、每類 node 的 aggregate requests 與 effective count，再檢查 adapter-private `runtime/adapters/lke/resolved-resources.env` 的實際 region/SKU。Load test 只使用 normalized `runtime/state/provider-preflight.env`，不讀 adapter-private state。確認無誤後才執行 mutation：

```sh
go run ./scripts/go/rtk-cloud -- deployment provision \
  --environment qa \
  --confirm video-cloud-qa
```

LKE mutation 需要 operator 提供 `LINODE_TOKEN`。Token、kubeconfig、certificates、service secrets、device credentials、SQLite、state 與 artifacts 只能存在 `cloud_env/qa/runtime/` 或 operator secret source，不得寫入上述 tracked `.env` 檔，也不得 commit。

## Review checklist

- Environment 目錄名稱清楚且使用小寫英數字與 `-`。
- `CLOUD_STACK_NAME` 與 DNS 名稱不會碰撞其他 environment。
- 只覆寫與此 environment 確實不同的值。
- Architecture override 沒有 `LKE_*`、`EKS_*`、`GKE_*`。
- dev、staging、prod 的 adapter override 不包含正常 region、SKU 或 quota。
- Provider account limit 只存在 ignored adapter runtime，不進版控。
- DNS adapter 已選擇，且 environment 沒有 provider zone ID 或 DNS credentials。
- `deployment plan` 成功且 resolved plan 不含 secret。
- `runtime/` 保持 ignored，沒有從其他 environment 複製 state。
