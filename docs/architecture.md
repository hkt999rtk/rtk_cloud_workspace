# RTK Cloud Architecture Notes

This workspace tracks the integration boundary between the SDK client, video
cloud server, contracts repository, account manager, user-facing frontend, and
admin dashboard.

The workspace repository is an integration and governance layer. It does not
own service implementation details, service-local architecture, or generated
artifacts. Those stay in the owning service repositories.

## Source Of Truth

- Cross-repo wire and payload contracts: `repos/rtk_cloud_contracts_doc`
- Cross-repo integration snapshot: submodule commits pinned by this workspace
- SDK/runtime implementation: `repos/rtk_cloud_client`
- Video server implementation: `repos/rtk_video_cloud`
- Account and registry implementation: `repos/rtk_account_manager`
- User-facing Realtek Cloud introduction website: `repos/rtk_cloud_frontend`
- Admin dashboard implementation: `repos/rtk_cloud_admin`

The contracts repository is the only normative source for shared API, payload,
streaming, device transport, provisioning, and cross-service channel behavior.
Service-local docs may summarize or reference those contracts, but they must
not become a second contract source of truth.

## Boundary Rules

- `rtk_cloud_client` implements client SDK APIs and runtime behavior.
- `rtk_video_cloud` owns the video-cloud HTTP/WebSocket/MQTT server behavior.
- `rtk_account_manager` owns account, organization, and registry-only device
  behavior. It is a backend identity, tenant, authorization, entitlement,
  registry, and provisioning control-plane service; it should not own product
  Web UI.
- `rtk_cloud_frontend` owns the public website content and lead/contact
  experience for users learning about Realtek Cloud.
- `rtk_cloud_admin` owns the B2B admin dashboard/BFF for fleet, provisioning,
  lifecycle, health, and audit operations; it does not replace account manager
  or video cloud as system-of-record services. It may proxy or aggregate
  account/video APIs, but it must not become the source of truth for customer,
  organization, device, quota, or provisioning state.
- Device/app provisioning should be able to complete without
  `rtk_cloud_admin`; the canonical path is device/app/SDK to
  `rtk_account_manager`, then cross-service activation through
  `rtk_video_cloud`.
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

See [`account-manager-admin-boundary.md`](account-manager-admin-boundary.md) for
the detailed `rtk_account_manager` versus `rtk_cloud_admin` server boundary.
