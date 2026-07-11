# Architecture defaults 與 deployment adapters

本目錄保存所有 environment 共用的 deployment contract，不保存任何 environment identity、runtime state 或 secret。

- `architectures/kubernetes/`：workloads、capacity、logical node classes、每類 node 的最低 vCPU/memory、placement、edge 與 TURN intent。
- `adapters/lke/`：logical location 到 LKE region 的 mapping、Linode instance catalog 與 provider implementation defaults；不保存 account quota。
- `adapters/eks/`、`adapters/gke/`：adapter contract；目前不支援 mutation。
- `dns_adapters/godaddy/`、`dns_adapters/route53/`：可獨立於 deployment adapter 選擇的 DNS provider defaults 與 schema。

新增 environment 時不要複製或修改這些 defaults；請依 [`cloud_env/README.md`](../cloud_env/README.md) 建立 environment，並只在該 environment 的 `overrides/` 寫差異。

只有當所有 environment 的共用架構或 provider mapping 都要改變時，才修改本目錄。Architecture 不得出現 provider-specific key；adapter 不得覆寫 architecture key。Persistent storage capacity 是 workload/storage intent，不是 node SKU sizing。

Shared capacity planner 先依 workload registry 計算 effective Pod replicas，再依每個 logical node class 的 planning shape、system reserve、aggregate requests 與 spread floor 計算 effective node count。Adapter 只能選擇符合 planning shape 的 provider SKU，不得重新計算 replicas 或 node count。

DNS adapter 與 deployment adapter 正交。Shared DNS orchestration 只處理 hostname、record intent、convergence 與 ACME DNS-01 lifecycle；GoDaddy/Route53 credentials、zone discovery、API mutation 與 provider IDs 只能存在 DNS adapter 或 ignored runtime。完整 contract 見 [`docs/dns-adapter-architecture.md`](../docs/dns-adapter-architecture.md)。
