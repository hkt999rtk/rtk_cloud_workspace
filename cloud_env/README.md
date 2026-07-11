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
```

| Key | 必填 | 用途 |
| --- | --- | --- |
| `CLOUD_STACK_NAME` | 是 | Cluster、namespace、DNS 與 destructive confirmation 使用的唯一 stack 名稱。 |
| `CLOUD_DNS_ROOT_DOMAIN` | 是 | Public service hostname 的根網域。 |

建立 `cloud_env/qa/deployment.env`：

```env
DEPLOYMENT_ARCHITECTURE=kubernetes
DEPLOYMENT_ADAPTER=lke
```

| Key | 必填 | 用途 |
| --- | --- | --- |
| `DEPLOYMENT_ARCHITECTURE` | 是 | 選擇共用 architecture；目前為 `kubernetes`。 |
| `DEPLOYMENT_ADAPTER` | 是 | 選擇 provider adapter；目前只有 `lke` 支援 mutation，`eks`、`gke` 只會 validate 後 fail fast。 |

## 選填 overrides

沒有差異時不需要建立 override 檔。需要調整 workload、capacity 或 topology 時，將 [`cloud_deploy/architectures/kubernetes/`](../cloud_deploy/architectures/kubernetes/) 已存在的 key 寫入 `overrides/architecture.env`：

```env
CAPACITY_TARGET_CONNECTIONS=1000
MQTT_REPLICAS=2
NODE_CLASS_BROKER_MIN_COUNT=2
```

需要調整 LKE region、node type 或 quota 時，將 [`cloud_deploy/adapters/lke/defaults.env`](../cloud_deploy/adapters/lke/defaults.env) 已存在的 key 寫入 `overrides/adapter.env`：

```env
LKE_REGION=us-sea
LKE_GENERAL_NODE_TYPE=g6-standard-4
LKE_BROKER_NODE_TYPE=g6-standard-4
LKE_DATABASE_NODE_TYPE=g6-standard-8
LINODE_ACTIVE_SERVICE_LIMIT=20
```

Architecture override 不得包含 provider key；adapter override 不得包含 workload、capacity 或 topology key。Unknown key、錯誤型別與跨層 key 會在 plan 階段失敗。

## 驗證與 provision

先確認 tracked config 不會被 Git ignore，再產生 sanitized plan：

```sh
git check-ignore "cloud_env/qa/environment.env" || true
go run ./scripts/go/rtk-cloud -- deployment plan --environment qa
```

檢查 plan 中的 environment、stack、architecture、adapter、capacity、replicas 與 node classes。確認無誤後才執行 mutation：

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
- Adapter override 的 key 已存在於所選 adapter defaults。
- `deployment plan` 成功且 resolved plan 不含 secret。
- `runtime/` 保持 ignored，沒有從其他 environment 複製 state。
