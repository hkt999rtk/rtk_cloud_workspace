# cloud_env 目錄配置

`cloud_env/` 是 workspace 的本機 cloud environment 目錄，整個目錄都被 git ignore。它保存 live deployment 需要的 topology、runtime env、state、keys/certificates、device fixtures、artifacts 與 migration backup。

預設 staging environment directory 是 `cloud_env/staging`。目前 Linode provider 的實際資料在：

```text
cloud_env/staging/linode/
  env/
  topology/
  services/
  keys/
  certificates/
  devices/
  state/
  artifacts/
  backups/
```

## 目錄用途

- `env/`：跨服務 operator credential，例如 `operator.env` 內的 Linode、GoDaddy、Object Storage credential。
- `topology/`：環境 topology manifest，例如 `video-cloud-staging.yaml`。
- `services/`：各服務 runtime/deploy env，例如 `video-cloud/`、`account-manager/`、`cloud-admin/`。
- `keys/`：staging/production-like CA、client certificate、factory enrollment certificate，以及服務部署會用到的 private key。
- `certificates/`：公開 HTTPS certificate cache。成功取得 Let's Encrypt certificate 後，staging deploy 會把 `fullchain.pem`、`privkey.pem`、可用時的 `cert.pem`/`chain.pem` 存到 `certificates/<fqdn>/`。下次 provision/deploy 時，如果 certificate 在安全期限內尚未過期，script 會先上傳 cached certificate 到 VM，建立 certbot lineage 與 renewal config，啟用 `certbot.timer`，避免重複向 Let's Encrypt 申請並保留 host 端自動 renew。
- `devices/`：load test 或 factory rehearsal 的模擬 device credentials，例如 `test_device/`。
- `state/`：Linode VM、firewall、VPC 等已建立資源的 state file。
- `artifacts/`：provision、deploy readiness、e2e、runtime health 等輸出。
- `backups/`：migration 或 reset 前留下的 local backup。

## 操作原則

- staging scripts 需要明確指定 `--env-root PATH`，避免操作到錯誤環境。
- 可傳 `cloud_env/staging` 作為 environment directory；script 會自動解析到 `cloud_env/staging/linode`。
- `--secrets-root PATH` 只保留為舊參數 alias，新的文件與操作都使用 `--env-root`。
- HTTPS certificate cache 是 environment-local secret material，包含 private key；只放在 `cloud_env/`，不要複製到 repo 或 artifact。
- 不要 commit `cloud_env/` 裡的任何檔案。
