# RTK Cloud 測試操作指南

Status: active

Owner: `rtk_cloud_workspace`

Last reviewed: 2026-08-11

Audience: internal test operators and new maintainers

本文件說明測試前要準備的環境、資料與證據。測試分成 local validation、deployed
acceptance、1K feature qualification 與 capacity/load test；不同層級不可互相取代。

## 選擇測試層級

| 目的 | 入口 | 主要前置條件 |
| --- | --- | --- |
| Workspace 快速基線 | `test-matrix` | 完整 recursive checkout |
| Service tests | `test-services` | 各 repo runtime/dependency |
| Deterministic E2E | `test-e2e` | 本機 fixture；不需要 shared staging |
| UI tests | `test-ui` | Chromium 與 local BFF/fixture |
| 已部署環境驗收 | `deployment acceptance` | matching runtime + kube access |
| Feature 1K qualification | `test-feature` | acceptance PASS + 專用 test identity |
| Capacity test | `home-100k.sh workflow-live` | 明確 target、容量計畫、足量 inventory 與 generator |

## 共通準備

1. 從 workspace root 執行，並初始化所有 submodule：

   ```sh
   git submodule update --init --recursive
   ```

2. 確認 workspace/submodule commit 與 dirty state：

   ```sh
   go run ./scripts/go/rtk-cloud -- status-all
   ```

3. Live test 使用 canonical runtime：

   ```text
   cloud_env/<environment>/runtime
   ```

4. Live test 前先完成 deployment acceptance preflight：

   ```sh
   go run ./scripts/go/rtk-cloud -- deployment preflight \
     --environment staging --operation acceptance
   ```

5. 使用專用 test user/device/brand，不得截取或匯出真實客戶資料。

## Local baseline、service、E2E 與 UI

快速基線：

```sh
(cd scripts/go && go run ./rtk-cloud -- test-matrix)
```

較完整的本機驗證：

```sh
(cd scripts/go && go run ./rtk-cloud -- test-services)
(cd scripts/go && go run ./rtk-cloud -- test-e2e)
(cd scripts/go && go run ./rtk-cloud -- test-e2e --scripts)
(cd scripts/go && go run ./rtk-cloud -- test-ui --desktop)
(cd scripts/go && go run ./rtk-cloud -- test-ui --mobile)
```

UI staging 模式只可使用 dedicated staging accounts，並設定 `E2E_EVIDENCE_SAFE=1`。完整
coverage、artifact layout 與 Test ID governance 見 [`testing.md`](testing.md)。

## 已部署 staging acceptance

Acceptance 使用 K8s service discovery/port-forward 取得 Account Manager、Video Cloud、factory
enroll 與 MQTT endpoints，不讀取退休的 VM state。

```sh
go run ./scripts/go/rtk-cloud -- deployment acceptance \
  --environment staging --confirm video-cloud-staging
```

Acceptance 必須證明 user/device setup、MQTT flow、runtime-log persistence 與所選 billing
checks。任一步失敗都應停止後續付費 load-generator 建立。

## 1K feature qualification

先只產生 plan：

```sh
go run ./scripts/go/rtk-cloud -- test-feature \
  --feature device-shadow \
  --profile qualification-1k \
  --environment staging \
  --run-id manual-shadow-001 \
  --plan
```

Live run 必須明確加入 `--run --confirm video-cloud-staging`。可選 feature 為
`device-shadow`、`video-webrtc`、`clip-storage`；各 feature 的 1K 定義不同，不能只看
device count 推論測試強度。Qualification 必須先通過 canary，才可建立 1K load。

## Capacity／load test 前置資料

10K、50K、100K 與 custom target 都是獨立 sizing exercise。每次執行前記錄：

- target connected devices/clients 與 success threshold；預設功能門檻通常為 99.5%。
- warm-up、steady、cool-down 時間。
- user/device inventory 數量、device mix、brand、certificate validity。
- generator VM 數量、每台 connection budget、CPU/memory/NIC、FD 與 ephemeral ports。
- K8s node class/count、EMQX workload shape/replicas、pod placement 與 service endpoints。
- HAProxy backend list、`maxconn`、file descriptors 與 memory。
- PostgreSQL CPU/memory/storage/connection limit，以及 Video Cloud API replicas/DB pool。
- 預期 bottleneck 與用來證實或否定它的 metric。

不要只修改 `HOME100K_DEVICES` 就把 10K-sized environment 當成 50K/100K capacity
evidence。

## Load plan、preflight 與執行

先選擇 scenario description，並確認準備好的 inventory 足以涵蓋 target：

```sh
HOME100K_ENV_ROOT="$PWD/cloud_env/staging/runtime" \
HOME100K_DESCRIPTION_FILE=loadtests/home-100k/scenarios/mqtt-1k.description.env \
HOME100K_BRANDNAME=RTK1K \
HOME100K_RUN_ID="preflight-mqtt1k-$(date -u +%Y%m%dT%H%M%SZ)" \
  ./loadtests/home-100k/scripts/home-100k.sh plan
```

Fixture/certificate preflight：

```sh
HOME100K_ENV_ROOT="$PWD/cloud_env/staging/runtime" \
HOME100K_DESCRIPTION_FILE=loadtests/home-100k/scenarios/mqtt-1k.description.env \
HOME100K_BRANDNAME=RTK1K \
  ./loadtests/home-100k/scripts/home-100k.sh preflight
```

兩者都通過後才執行：

```sh
HOME100K_ENV_ROOT="$PWD/cloud_env/staging/runtime" \
HOME100K_DESCRIPTION_FILE=loadtests/home-100k/scenarios/mqtt-1k.description.env \
HOME100K_BRANDNAME=RTK1K \
HOME100K_RUN_ID="mqtt1k-$(date -u +%Y%m%dT%H%M%SZ)" \
  ./loadtests/home-100k/scripts/home-100k.sh workflow-live
```

50K、100K 或其他 target 應使用相符 description/capacity plan；完整 profile 與參數見
[`../loadtests/home-100k/README.md`](../loadtests/home-100k/README.md)。

## 執行中監控

至少同步監控：

- K8s node/pod/container CPU、memory、restart 與 unschedulable workload。
- EMQX cluster membership、listener current connections、shutdown/socket/congestion counters。
- HAProxy frontend/backend sessions、backend health、FD、RSS 與 connection errors。
- PostgreSQL connections、CPU、memory、IO、transaction/lock pressure。
- Video Cloud API token bootstrap latency、DB pool、API-to-MQTT connection health。
- Generator process limits、CPU、memory、network 與 per-shard completion。

先確認 routing correctness 與 generator headroom，再決定是否擴增 server capacity。

## Report、resume 與 cleanup

最終 report 位於：

```text
loadtests/home-100k/reports/<run-id>/TEST_REPORT.md
```

`Status: COMPLETE` 只表示流程完成；只有 `Result: SUCCESS` 且滿足本次 target gate 才能
宣稱成功。`FAIL`、`INCOMPLETE` 與 `BLOCKED` 都是 non-passing outcome。

若 VM 已建立但 workflow 中斷，使用同一個 run ID：

```sh
HOME100K_RUN_ID=<run-id> \
  ./loadtests/home-100k/scripts/home-100k.sh workflow-resume-live
```

調查完成後只清理該 run 建立的 load-generator VM：

```sh
HOME100K_RUN_ID=<run-id> \
  ./loadtests/home-100k/scripts/home-100k.sh list-vms
HOME100K_RUN_ID=<run-id> \
  ./loadtests/home-100k/scripts/home-100k.sh destroy-vms --live --confirm-live
```

不要刪除 LKE cluster、CI runner、edge/TURN VM、release bucket 或其他 run 的 generator。

## 常見停止條件

- Runtime 不完整或 identity 不符：停止並還原 matching runtime。
- Inventory 少於 target 或 device mix 不符：重新準備資料，禁止啟動 generator。
- MQTT route/backend 與 ready pod placement 不符：先修 routing。
- Generator FD/CPU/network 飽和：先擴充 generator，不能把結果歸因於 server。
- `/request_token` 失敗：先查 API/PostgreSQL，不要只增加 EMQX pod。
- Device connect 成功但 shadow delta/ACK 失敗：檢查 API-to-MQTT、workers、database 與
  persisted runtime logs。
