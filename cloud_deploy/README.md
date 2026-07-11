# Architecture defaults 與 deployment adapters

本目錄保存所有 environment 共用的 deployment contract，不保存任何 environment identity、runtime state 或 secret。

- `architectures/kubernetes/`：workloads、capacity、logical node classes、placement、edge 與 TURN intent。
- `adapters/lke/`：LKE region、instance type、quota 與 provider mapping defaults。
- `adapters/eks/`、`adapters/gke/`：adapter contract；目前不支援 mutation。

新增 environment 時不要複製或修改這些 defaults；請依 [`cloud_env/README.md`](../cloud_env/README.md) 建立 environment，並只在該 environment 的 `overrides/` 寫差異。

只有當所有 environment 的共用架構或 provider mapping 都要改變時，才修改本目錄。Architecture 不得出現 provider-specific key；adapter 不得覆寫 architecture key。
