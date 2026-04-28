# RTK Cloud Architecture Notes

This workspace tracks the integration boundary between the SDK client, video
cloud server, contracts repository, and account manager.

The workspace repository is an integration and governance layer. It does not
own service implementation details, service-local architecture, or generated
artifacts. Those stay in the owning service repositories.

## Source Of Truth

- Cross-repo wire and payload contracts: `repos/rtk_cloud_contracts_doc`
- Cross-repo integration snapshot: submodule commits pinned by this workspace
- SDK/runtime implementation: `repos/rtk_cloud_client`
- Video server implementation: `repos/rtk_video_cloud`
- Account and registry implementation: `repos/rtk_account_manager`

The contracts repository is the only normative source for shared API, payload,
streaming, device transport, provisioning, and cross-service channel behavior.
Service-local docs may summarize or reference those contracts, but they must
not become a second contract source of truth.

## Boundary Rules

- `rtk_cloud_client` implements client SDK APIs and runtime behavior.
- `rtk_video_cloud` owns the video-cloud HTTP/WebSocket/MQTT server behavior.
- `rtk_account_manager` owns account, organization, and registry-only device
  behavior.
- Cross-service provisioning and channel behavior should be tracked as
  integration work, not hidden inside the SDK client.
- Workspace docs may describe repository boundaries, integration snapshots,
  validation workflows, and documentation governance.
- Workspace docs should not duplicate service internals, package ownership, API
  payload definitions, or deployment-only runbooks owned by a service repo.

## Documentation Layers

| Layer | Owner | Examples |
| --- | --- | --- |
| Workspace | `rtk_cloud_workspace` | Cross-repo index, integration snapshot, documentation governance, validation entry points. |
| Contracts | `rtk_cloud_contracts_doc` | Normative HTTP, auth, streaming, transport, provisioning, and cross-service channel contracts. |
| Service | Owning service repo | Service architecture, module boundaries, deployment notes, operations notes, service-local testing. |
| Decision records | Workspace or owning service repo | Architecture decisions that explain why a boundary or approach was chosen. |

See [`documentation-governance.md`](documentation-governance.md) for ownership,
status, and review rules.
