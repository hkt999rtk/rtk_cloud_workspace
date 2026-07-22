# AWS IoT Device Shadow API 差異分析

日期：2026-07-22

狀態：原始碼檢視與 API 對齊建議

## 結論摘要

RTK Device Shadow 的核心概念與 AWS 相近，已具備：

- unnamed shadow 與 named shadow
- `state.desired`、`state.reported` 與計算出的 `state.delta`
- partial merge
- 以 `null` 刪除欄位
- `version` 與 `clientToken`
- MQTT `accepted`、`rejected`、`delta`、`documents` 事件

但目前只能稱為 **AWS IoT Shadow-inspired**，不能稱為 AWS API
compatible。AWS Device SDK 無法在不修改的情況下直接連到 RTK。除了
HTTP path、MQTT topic prefix 與驗證規則不同之外，也有數項會造成客戶端
行為錯誤的語意差異。

最高風險問題如下：

1. Redis 的版本檢查與寫入不是 atomic operation；併發寫入可能同時成功、
   產生相同版本，並遺失其中一筆狀態。
2. `update/accepted`、`update/documents`、nested metadata 與 delete response
   的 JSON shape 不符合 AWS。
3. 未實作 AWS 的 64-byte `clientToken`、名稱格式、8 KiB state document、
   JSON nesting、陣列不可含 `null` 等限制。
4. RTK 在 provision／activation 時自動建立 unnamed shadow；AWS 則要等第一筆
   update 才建立 shadow。
5. RTK 永久保留 delete tombstone 的版本邊界；AWS 在刪除超過 48 小時後重新
   建立時，版本會重新開始。
6. RTK 的 app/device desired/reported 權限分工是產品擴充，不是 AWS Shadow
   API 的固有語意，而且目前 MQTT 共用 `update` topic 無法只靠 topic ACL
   驗證 payload section。

建議先修正 storage semantics 與 response document，再增加 AWS route/topic
alias。不要先只改 `$vc` 為 `$aws`，否則只是表面相容。

## 檢視範圍

RTK 原始碼與規格：

- [`repos/rtk_cloud_contracts_doc/DEVICE_SHADOW.md`](../repos/rtk_cloud_contracts_doc/DEVICE_SHADOW.md)
- [`repos/rtk_video_cloud/docs/device-shadow-spec.md`](../repos/rtk_video_cloud/docs/device-shadow-spec.md)
- [`repos/rtk_video_cloud/internal/deviceshadow/service.go`](../repos/rtk_video_cloud/internal/deviceshadow/service.go)
- [`repos/rtk_video_cloud/internal/httpapi/device_shadow.go`](../repos/rtk_video_cloud/internal/httpapi/device_shadow.go)
- [`repos/rtk_video_cloud/internal/mqtt/shadow.go`](../repos/rtk_video_cloud/internal/mqtt/shadow.go)
- [`repos/rtk_video_cloud/internal/rediscache/deviceshadow.go`](../repos/rtk_video_cloud/internal/rediscache/deviceshadow.go)

AWS 官方資料：

- [Device Shadow service documents](https://docs.aws.amazon.com/iot/latest/developerguide/device-shadow-document.html)
- [Device Shadow MQTT topics](https://docs.aws.amazon.com/iot/latest/developerguide/device-shadow-mqtt.html)
- [Device Shadow REST API](https://docs.aws.amazon.com/iot/latest/developerguide/device-shadow-rest-api.html)
- [Interacting with shadows](https://docs.aws.amazon.com/iot/latest/developerguide/device-shadow-data-flow.html)
- [Device Shadow error messages](https://docs.aws.amazon.com/iot/latest/developerguide/device-shadow-error-messages.html)
- [AWS IoT Core Device Shadow quotas](https://docs.aws.amazon.com/general/latest/gr/iot-core.html#iot-protocol-limits)
- [ListNamedShadowsForThing](https://docs.aws.amazon.com/iot/latest/apireference/API_iotdata_ListNamedShadowsForThing.html)

本報告是原始碼層級比較，未宣稱已用 RTK staging 與真實 AWS account 執行
black-box differential test。

## 相容性總表

| 項目 | RTK 與 AWS 的關係 | 影響 |
| --- | --- | --- |
| desired/reported 模型 | 大致一致 | 低 |
| partial merge 與 `null` 刪除 | 大致一致 | 低 |
| 一般 JSON 的 delta 計算 | 一致 | 低 |
| version 設計目的 | 一致 | 低 |
| 併發下的 version 實作 | 不一致 | 嚴重 |
| MQTT action suffix | 一致 | 低 |
| MQTT topic prefix | `$vc/devices` 與 `$aws/things` 不同 | 高 |
| HTTP data-plane path | 不同 | 高 |
| response JSON shape | 部分不同 | 高 |
| nested metadata | 不同 | 高 |
| delete/recreate lifecycle | 不同 | 高 |
| validation 與 quota | 差異很大 | 高 |
| named-shadow list | 不同且功能不足 | 中 |
| authentication/authorization | RTK 產品自訂 | 中 |
| lifecycle bootstrap | RTK 擴充 | 中 |

## 詳細語意差異

### 1. 併發更新無法保證 AWS 的 version 語意

AWS 對每一筆成功 update 增加 version；若 request 指定的 version 與最新
version 不符，回傳 409。這個流程必須在 storage boundary 內序列化或使用
compare-and-swap。

RTK 現在的流程是：

```text
Redis GET
process 內檢查 request version
process 內 merge 並 version++
Redis SET
```

如果兩個 handler 同時讀到 version 10，兩者都可能接受 request version 10，
都回傳 version 11，最後一個 `SET` 覆蓋前一個。即使 request 不帶 version，
兩個 partial patch 也可能互相覆蓋。

這會使客戶端看到：

- 兩筆都顯示成功，但其中一筆狀態消失
- 重複的 version
- `documents` event 與實際 committed transition 不一致

**建議：**將 read、version check、merge、version increment、tombstone、document
write 與 named index 維護放進同一個 Redis Lua/function atomic operation，並由
該 operation 回傳 committed previous/current document。

### 2. HTTP path 與 MQTT topic 不相容

AWS HTTP：

```text
GET    /things/{thingName}/shadow?name={shadowName}
POST   /things/{thingName}/shadow?name={shadowName}
DELETE /things/{thingName}/shadow?name={shadowName}
GET    /api/things/shadow/ListNamedShadowsForThing/{thingName}?pageSize=...&nextToken=...
```

RTK HTTP：

```text
GET    /api/devices/{devid}/shadow?name={shadowName}
POST   /api/devices/{devid}/shadow?name={shadowName}
DELETE /api/devices/{devid}/shadow?name={shadowName}
GET    /api/devices/{devid}/shadows
```

AWS MQTT prefix 是 `$aws/things/{thingName}/shadow`；RTK 是
`$vc/devices/{devid}/shadow`。後面的 get/update/delete 與 response suffix
則大致相同。

**建議：**增加 AWS-compatible alias，內部仍映射到同一個 `devid` 與同一份
shadow state。既有 `$vc` 與 `/api/devices` 可在 migration 期間保留。

### 3. `update/accepted` response 不同

AWS 的 `update/accepted` 與 UpdateThingShadow HTTP response 只回傳此次 request
中被接受的 desired/reported patch、相對應 metadata、timestamp、version 與
可選的 client token。

RTK 每次都回傳完整 `desired`、`reported`、`delta`，也固定輸出空 object，並
加入 AWS 沒有的 `updated_at`。

**建議：**update accepted 必須由 normalized request patch 產生；完整 document
只用於 GET。AWS mode 應省略不存在的 section，`updated_at` 則只留在 RTK
extension mode。

### 4. `update/documents` shape 不同

AWS 在 `previous` 與 `current` 內放 `state`、`metadata`、`version`，最外層才放
`timestamp` 與可選的 `clientToken`。

RTK 重複使用完整 document serializer，因此 `previous`／`current` 內還會出現
`delta`、`timestamp`、`clientToken`、`updated_at`，最外層又多一個 AWS 規格
沒有的 `version`。

**建議：**為 documents event 建立獨立 serializer，不要重用 GET serializer。

### 5. nested metadata shape 不同

AWS metadata 會鏡像 state 的 property tree，timestamp 放在 attribute leaf。
RTK 則輸出：

```json
{
  "lights": {
    "timestamp": 1778397600,
    "children": {
      "color": {"timestamp": 1778397600}
    }
  }
}
```

`children` wrapper 與 object parent timestamp 都是 RTK 自訂格式，AWS client
不會預期這個結構。

**建議：**public metadata serializer 必須直接鏡像 state tree，並增加 nested
object 與 nested delta golden tests。

### 6. 空 section 的表示方式不同

AWS 可省略不存在的 `desired` 或 `reported`；section 設為 `null` 後會從 document
移除。RTK 內部轉成空 object，response 永遠包含：

```json
"state": {"desired": {}, "reported": {}, "delta": {}}
```

**建議：**AWS mode 的 serializer 應省略 absent/empty section。

### 7. delete 與 recreate 不同

AWS HTTP DELETE 沒有 body；MQTT delete payload 內容會被忽略。刪除後 48 小時內
重建會延續 version，超過 48 小時後重新開始。

RTK 的 HTTP 與 MQTT delete body 接受 `version` 和 `clientToken`，可因 stale
version 拒絕 delete，並永久保留 tombstone version。delete response 也使用一般
完整 document serializer。

**建議：**將 public shadow delete 與 lifecycle/audit tombstone 分離。AWS mode
的 HTTP DELETE 不收 body，MQTT delete 忽略 body。若要求完全相容，public
version 應實作 48 小時 reset；audit 可另外永久保存。

### 8. shadow 建立時機不同

AWS 建立 Thing 不會自動建立 shadow。第一次 GET 會是 not found；第一次 update
才建立 shadow。

RTK provision／activation 會自動建立 unnamed shadow，並寫入 lifecycle-derived
reported fields，因此裝置尚未做 shadow update 就已經有 shadow 與非零 version。

**建議：**

- Exact AWS mode：不要自動建立 public shadow。
- RTK extension mode：保留 bootstrap，但清楚標示為非 AWS 行為。

### 9. validation 與 quota 不同

AWS 的重要限制包括：

- `clientToken` 最多 64 bytes
- thing name 為 1–128 characters，格式 `[a-zA-Z0-9:_-]+`
- shadow name 為 1–64 characters，格式 `[$a-zA-Z0-9:_-]+`
- desired/reported state document 最多 8 KiB，metadata 不列入
- JSON nesting 有上限
- array 不可包含 `null`
- 僅支援 UTF-8
- 具有 400、401/403、404、409、413、415、429、500、503 等錯誤語意

目前 RTK 沒有完整實作這些限制；Go JSON decoder 也會接受含 `null` 的 array。

AWS quota 頁目前寫八層，但 error guide 仍可看到較舊的六層說法。建議以 quota
值為實作基準，release 前再對真實 AWS 執行邊界測試。

**建議：**建立 HTTP/MQTT 共用 validator，依 UTF-8 byte 計算，驗證 update 後的
完整 stored state，而不只是 request patch。

### 10. named-shadow list 不同

AWS `ListNamedShadowsForThing` 支援 `pageSize` 1–100、`nextToken`，response 包含
`results`、可選 `nextToken` 與 epoch `timestamp`。只存在 unnamed shadow 或 Thing
不存在時，官方文件定義為空 list。

RTK 一次回傳全部排序名稱，只有 `results`，沒有 pagination 與 timestamp；Thing
不存在時會先回 404。

**建議：**實作 AWS route、opaque stable pagination、timestamp、參數驗證與
missing-thing 行為。

### 11. authentication 與 section authorization 不同

AWS HTTP data plane 使用 SigV4/IAM 或 mTLS certificate，並依 Thing resource 的
Get/Update/Delete/List action 授權。app 寫 desired、device 寫 reported 是典型使用
方式，不是 AWS API 強制的 payload-section 權限。

RTK HTTP 使用 bearer scope，禁止 device/camera token 寫 desired。這可以保留為
產品安全政策，但不是 AWS compatibility 的一部分。

MQTT 方面，desired 與 reported 共用同一個 update topic。普通 topic ACL 只能決定
能否 publish 該 topic，無法判斷 payload 是 desired 或 reported。因此目前不能只靠
ACL 保證「device 不可寫 desired」。

**建議：**若 MQTT 必須強制 section-level permission，應把 authenticated publisher
claims 傳給 handler，或使用 broker payload-aware authorization hook。

### 12. HTTP commit 後 MQTT 發送失敗會產生不明確結果

RTK HTTP desired update 先 commit shadow，再 publish MQTT delta。如果 publish
失敗，HTTP 回 503，但 state 與 version 其實已改變；client retry 會再增加一次
version。

**建議：**shadow mutation 與 outbox/event record 應 atomic commit。commit 成功就
回傳 accepted，MQTT event 由具 retry 與 version ordering 的背景流程送出，不要把
post-commit delivery failure 回報成 mutation failure。

## 已對齊、應保留的行為

- update 可建立不存在的 shadow
- update 只 merge request 指定的 object field
- property 設為 `null` 會刪除
- `desired: null`／`reported: null` 會清除 section
- array 採完整 replacement
- delta 包含 desired-only 與 desired-different 欄位，不含 reported-only 欄位
- nested delta 保留完整 path
- request 不帶 version 時略過 optimistic version check
- reported 與 desired 相同時清除相對應 delta
- named 與 unnamed shadow 狀態彼此獨立
- MQTT action 與 response suffix 與 AWS 相同
- MQTT rejection 具有 code、message、timestamp 與可選 client token

## 建議執行順序

### P0：先修正語意正確性

1. 實作 Redis atomic mutation/CAS。
2. 實作 atomic outbox 或等價的可靠有序 notification。
3. 加入 versioned 與 unversioned concurrent writer 測試。

### P1：對齊 AWS document

1. 分開實作 GET accepted、UPDATE accepted、DELETE accepted、documents、delta、
   list 與 error serializer。
2. 修正 nested metadata 與 empty section omission。
3. 實作共用 AWS validator 與精確 error mapping。
4. 明確決定 delete/recreate 與 lifecycle bootstrap 的 compatibility mode。

### P2：提供 AWS wire compatibility

1. 增加 `$aws/things/{thingName}/shadow...` alias。
2. 增加 `/things/{thingName}/shadow` 與 `ListNamedShadowsForThing` alias。
3. 實作 list pagination。
4. 定義 `thingName` 到 RTK `devid` 的唯一映射。

### P3：SDK 驗證

1. 決定目標是 payload/topic compatibility，還是包含 SigV4 與 mTLS 的完整相容。
2. 以 AWS SDK 與 embedded Device SDK 對 RTK 執行 conformance test。
3. 使用相同 black-box fixtures 同時測 AWS IoT Core 與 RTK，比較 normalized result。

## 最小 conformance test matrix

1. shadow 建立前 GET。
2. 第一筆 desired update 與 reported update。
3. partial nested merge。
4. leaf delete、section delete 與 empty document。
5. nested delta 與 delta clear。
6. whole-array replacement 與 array containing `null` rejection。
7. matching version、stale version、omitted version。
8. 兩個 writer 同時使用相同 version，只能一個成功。
9. 兩個 unversioned partial patch 都必須保留，且 version 不可重複。
10. UPDATE accepted 與 GET accepted shape。
11. `documents` 與 `delta` event exact shape。
12. delete response、立即重建、超過 compatibility window 後重建。
13. named/unnamed isolation、list pagination、missing thing。
14. 64-byte 與 65-byte client token。
15. 合法與非法 thing/shadow name。
16. 最大 state size、nesting、invalid UTF-8、unsupported media type。
17. HTTP commit 時 MQTT 暫時無法送出。
18. app/device authorization；此項與 AWS payload semantics 分開測試。

## 最終建議

目前對外應描述為「AWS IoT Shadow-inspired」，不應標示為 AWS-compatible。
最短且安全的對齊路徑為：

1. 先修正 atomic mutation 與 notification durability。
2. 對齊 response document 與 validation。
3. 再增加 AWS HTTP route 與 MQTT topic alias。
4. RTK lifecycle 與 authorization 僅作為明確標示的 extension。
5. 最後用同一套 black-box suite 對 AWS 與 RTK 做 differential verification。
