# RTK Cloud Documentation Index

This directory contains workspace-level documentation for cross-repository
coordination. Service implementation details belong in the owning service
repositories. Shared wire and payload contracts belong in
`repos/rtk_cloud_contracts_doc`.

## Workspace Documents

| Document | Classification | Purpose |
| --- | --- | --- |
| [architecture.md](architecture.md) | Source | Cross-repo boundaries and source-of-truth model. |
| [documentation-governance.md](documentation-governance.md) | Source | Documentation ownership, status, and review rules. |
| [testing.md](testing.md) | Source | Cross-repository validation commands for pinned snapshots. |
| [adr/README.md](adr/README.md) | Index | Workspace architecture decision record location and format. |

## Repository Entry Points

| Repository | Classification | Entry point |
| --- | --- | --- |
| `repos/rtk_cloud_contracts_doc` | Contract source of truth | [`README.md`](../repos/rtk_cloud_contracts_doc/README.md) |
| `repos/rtk_cloud_client` | Service-owned docs | [`docs/README.md`](../repos/rtk_cloud_client/docs/README.md) |
| `repos/rtk_video_cloud` | Service-owned docs | [`docs/architecture.md`](../repos/rtk_video_cloud/docs/architecture.md) |
| `repos/rtk_account_manager` | Service-owned docs | [`docs/SPEC.md`](../repos/rtk_account_manager/docs/SPEC.md) |

## Reference-Only Documents

Some service repositories contain mirrored or reference-only copies of upstream
contracts for local implementation context. Treat these as supporting notes
unless the document explicitly states that it is the source of truth. For shared
wire, payload, and integration behavior, start from
`repos/rtk_cloud_contracts_doc`.
