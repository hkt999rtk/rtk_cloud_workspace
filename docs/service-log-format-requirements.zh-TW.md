# Service Log Format Requirements for Zap and Loki

Status: draft requirement.

Owner: `rtk_cloud_workspace`.

Last reviewed: 2026-07-07.

## Purpose

這份文件定義 RTK Cloud Go service 使用 `go.uber.org/zap` 輸出 log 時的格式要求，目標是讓 Loki 可以用穩定、低基數的 labels 管理服務 log，同時保留 request、device、user 等細節在 JSON body 裡查詢。

應用程式只負責把結構化 JSON log 寫到 stdout/stderr。應用程式不得在一般 request path 同步寫入 Loki 或 central logger backend。

## Required Output Format

所有 Go service 和 worker 必須輸出單行 JSON log。

必要 root fields：

| Field | 說明 |
| --- | --- |
| `ts` | log 時間，由 zap production encoder 輸出 |
| `level` | `debug`, `info`, `warn`, `error` |
| `msg` | 穩定事件訊息，不要把變動值串進 message |
| `service` | 邏輯服務或程序名稱 |
| `env` | 環境，例如 `dev`, `staging`, `prod` |
| `version` | build 或 release 版本 |

建議 root fields：

| Field | 說明 |
| --- | --- |
| `unit` | 實際 runtime 單位，例如 systemd unit、K8s workload/container 名稱 |
| `component` | process 內部固定子元件，例如 `server`, `scheduler`, `certissuer` |

`unit` 和 `component` 必須是低基數、固定 enum。不要用 request path、device id、user id 或動態 job id 當成 `unit` 或 `component`。

## Loki Labels

預設 Loki stream labels 只允許下列欄位：

| Label | 來源 | 說明 |
| --- | --- | --- |
| `env` | zap JSON 或 collector default | 環境 |
| `service` | zap JSON 或 collector default | 邏輯服務 |
| `unit` | zap JSON、systemd unit、K8s workload/container metadata | 實際執行單位 |
| `level` | zap JSON | log level |

這組 label 是刻意保持最小化的 common set。`host`、`source`、`version`、`component` 預設不得升成 Loki common label，除非有明確查詢需求和基數評估。

禁止成為 Loki label 的高基數欄位：

- `request_id`
- `trace_id`
- `operation_id`
- `device_id`
- `org_id`
- `user_id`
- `actor_id`
- `path`
- `remote_addr`
- raw error message
- token、key、credential、secret 類欄位

這些欄位只能留在 JSON body。Loki 查詢可以先用 `env/service/unit/level` 縮小範圍，再用 JSON field filter 搜尋。

## Zap Logger Construction

service entrypoint 必須建立 root logger，並從 entrypoint 傳入 internal packages。internal packages 不應自行建立 production root logger。

建議寫法：

```go
logger, err := cloudlogger.New(cloudlogger.Config{
	Service: "video-cloud-api",
	Env:     cfg.Env,
	Version: cfg.BuildVersion,
	Level:   cfg.LogLevel,
})
if err != nil {
	return err
}
defer logger.Sync()

logger = logger.With(zap.String("unit", "video_cloud-api.service"))
```

HTTP server 應在 service boundary 套用 request logging middleware：

```go
handler := cloudlogger.HTTPMiddleware(logger)(routes)
```

worker 或 background job 應使用同一個 root logger：

```go
logger.Info("worker started", zap.String("component", "outbox-worker"))
```

禁止寫法：

```go
zap.NewProduction()
zap.S()
zap.L()
fmt.Printf("user %s failed: %v", userID, err)
logger.Infof("request %s failed", requestID)
```

除非是測試或明確的 disabled path，否則不要使用 `zap.NewNop()` 當 service runtime logger。

## Event Message Rules

`msg` 必須穩定，變動資料必須放在 typed fields。

正確：

```go
logger.Warn(
	"upstream request failed",
	zap.String("upstream", "account-manager"),
	zap.String("request_id", requestID),
	zap.Int("status", status),
	zap.Error(err),
)
```

錯誤：

```go
logger.Warn("account-manager request " + requestID + " failed with " + err.Error())
```

穩定 message 的好處是 Loki、Cloud Admin dashboard、alert rule 可以用同一個事件名稱聚合。

## Common JSON Body Fields

下列欄位可以放在 JSON body，供 `/v1/logs` 或 Loki JSON filter 查詢：

| Field | 說明 |
| --- | --- |
| `component` | 固定子元件 |
| `request_id` | inbound request id |
| `trace_id` | end-to-end trace id |
| `operation_id` | idempotent workflow 或 business operation id |
| `device_id` | device identifier |
| `org_id` | organization identifier |
| `user_id` | user identifier |
| `actor_id` | audit actor id |
| `actor_type` | `user`, `admin`, `service`, `device` 等 |
| `outcome` | `success`, `failure`, `skipped` 等 |
| `error_category` | 穩定錯誤分類 |
| `method` | HTTP method |
| `path` | sanitised path，不能含 token query value |
| `status` | HTTP status code |
| `duration_ms` | request duration |

這些欄位名稱必須跨 repo 一致。不要在不同 service 使用 `req_id`、`requestId`、`devid`、`deviceID` 等別名。

## Level Rules

| Level | 使用時機 |
| --- | --- |
| `debug` | 短期診斷，預設 production 不開 |
| `info` | service lifecycle、重要狀態轉換、低量成功事件 |
| `warn` | 可恢復失敗、降級、retry、上游暫時失敗 |
| `error` | request/job 失敗、不可自動恢復、需要操作員注意 |

不要用 `error` 記錄預期中的 4xx customer input failure，除非它代表系統異常或安全事件。高流量成功事件不要逐筆 `info`，應改用 metrics。

## Error Fields

Go error 必須用 `zap.Error(err)`。如果需要分類，另外加 `error_category`。

```go
logger.Error(
	"database migration failed",
	zap.String("error_category", "database_migration_failed"),
	zap.Error(err),
)
```

不要把完整 error string 當 Loki label。不要把 raw SQL、token、private key path、authorization header 或 credential value 寫進 error message。

## HTTP Request Logs

HTTP request log 應至少包含：

- `method`
- `path`
- `status`
- `duration_ms`
- `remote_addr`
- `request_id` when available

`path` 必須先 redaction，不得保留 `token`、`access_token`、`refresh_token`、`api_key`、`password`、`client_secret` 等 query value。

HTTP middleware 不應讀 request body，也不應記錄 request/response body。需要診斷 payload 時，應新增明確 allowlist 欄位。

## Secret And PII Handling

禁止記錄：

- authorization header
- session cookie
- access token 或 refresh token
- private key、CSR private material、PKCS#11 PIN
- database DSN password
- SMTP password
- raw customer PII payload

如果欄位可能含 secret，必須 redaction 成固定字串，例如 `[REDACTED]`。不要 hash secret 後寫入 log，除非有明確安全設計；hash 後的 secret 仍可能成為可比對識別子。

## Loki Query Examples

查 staging API error：

```logql
{env="staging", service="video-cloud-api", level="error"}
```

查某個 runtime unit：

```logql
{env="staging", unit="video_cloud-api.service"}
```

先用 label 篩範圍，再用 JSON body 查 request：

```logql
{env="staging", service="video-cloud-api"} | json | request_id="req-123"
```

查可恢復上游失敗：

```logql
{env="staging", level="warn"} | json | msg="upstream request failed"
```

## Collector Requirements

collector 或 forwarder 必須保留 zap JSON body，並只把允許的低基數欄位升成 Loki labels。

systemd/journald path：

- `service`, `env`, `version`, `level` 優先從 zap JSON body 讀取
- `unit` 可從 zap JSON body 或 `_SYSTEMD_UNIT` 取得
- 若 zap JSON 缺少 `env` 或 `version`，collector 可用部署 default 補值

K8s path：

- `unit` 應對應固定 workload/container 名稱，不要用 pod name 的隨機 suffix 當 common label
- pod name、node name、namespace 可以保留在 metadata/body，除非另有基數評估
- collector 不得因 logger backend degraded 阻塞 application pod

## Review Checklist

新增或修改 service logger 時，PR 必須確認：

- root logger 使用 `rtk_cloud_logger` 或 repo-local wrapper，且輸出 JSON
- log line 包含 `service`, `env`, `version`
- runtime 可取得穩定 `unit`
- Loki labels 只使用 `env`, `service`, `unit`, `level`
- request、device、user、operation id 沒有被放進 label
- HTTP path 已 sanitise sensitive query values
- error 使用 `zap.Error(err)` 且必要時有 `error_category`
- internal package 接收 `*zap.Logger`，沒有自行建立 production root logger
- 沒有使用 `SugaredLogger`、string interpolation 或 `fmt.Printf` 作為 service log
