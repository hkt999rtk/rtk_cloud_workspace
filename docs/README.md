# RTK Cloud Documentation Index

This directory contains workspace-level documentation for cross-repository
coordination. Service implementation details belong in the owning service
repositories. Shared wire and payload contracts belong in
`repos/rtk_cloud_contracts_doc`.

## Workspace Documents

| Document | Classification | Purpose |
| --- | --- | --- |
| [architecture.md](architecture.md) | Source | Cross-repo boundaries and source-of-truth model. |
| [account-manager-admin-boundary.md](account-manager-admin-boundary.md) | Source | Boundary between Account Manager as backend control plane and Admin as enterprise dashboard/BFF. |
| [documentation-governance.md](documentation-governance.md) | Source | Documentation ownership, status, and review rules. |
| [contracts-submodule-governance.md](contracts-submodule-governance.md) | Source | Contracts submodule path, URL, and commit alignment policy. |
| [artifact-release-governance.md](artifact-release-governance.md) | Source | Linode Object Storage artifact source-of-truth policy and adoption matrix. |
| [backend-release-readiness.md](backend-release-readiness.md) | Supporting note | Backend foundation closeout checklist, validation commands, report status, and remaining release-evidence items. |
| [deployment-secrets-governance.md](deployment-secrets-governance.md) | Source | Local deployment secret layout, environment/provider/service taxonomy, and handling rules. |
| [persistence-cache-refactor-roadmap.md](persistence-cache-refactor-roadmap.md) | Source | Cross-repo persistence boundary audit and Redis/cache-readiness issue roadmap. |
| [realtek-connect-plus-gap-analysis.md](realtek-connect-plus-gap-analysis.md) | Discussion note | Evidence-backed gaps between Realtek Connect+ promotion content and current implementation. |
| [implementation-gap-backlog.md](implementation-gap-backlog.md) | Supporting note | Post-interface implementation and test gaps to open as owner-repo issues. |
| [core-platform-gap-roadmap.md](core-platform-gap-roadmap.md) | Supporting note | Core platform gap roadmap for readiness, private cloud, account lifecycle, fleet, telemetry, SDK/app, WebRTC, and smart-home ecosystem boundaries. |
| [business-model.md](business-model.md) | Source | Tier structure, evaluation limits, pricing model framing, SDK licensing posture, and website disclosure rules. |
| [private-cloud-deployment.md](private-cloud-deployment.md) | Supporting note | Private-cloud deployment bill of materials, deployment order, profiles, operations runbook, support boundary, and follow-up routing. |
| [product-level-evidence.md](product-level-evidence.md) | Supporting note | Workspace evidence wrapper for private-cloud readiness sign-off and canonical report aggregation. |
| [linode-staging-deployment-snapshot.md](linode-staging-deployment-snapshot.md) | Supporting note | Current Linode staging deployment snapshot, live endpoints, PR state, and remaining production-readiness work. |
| [linode-ci-runners.md](linode-ci-runners.md) | Source | Linode self-hosted CI runner VM topology, lifecycle, artifact archive, and shutdown policy. |
| [status-reports/README.md](status-reports/README.md) | Source | Reusable weekly status report framework, material index, and builder workflow. |
| [webrtc-only-streaming-migration.md](webrtc-only-streaming-migration.md) | Supporting note | WebRTC-only video migration issue roadmap and validation checklist. |
| [video-cloud-load-test-roadmap.md](video-cloud-load-test-roadmap.md) | Supporting note | API-level video cloud E2E load test roadmap, issue owner matrix, and validation checklist. |
| [home-mqtt-loadtest-simulation.md](home-mqtt-loadtest-simulation.md) | Supporting note | Env-root driven home daily-use MQTT load simulation plan and developer issue breakdown. |
| [cross-service-broker-packaging.md](cross-service-broker-packaging.md) | Supporting note | Retired cross-service broker packaging decision and future reintroduction bar. |
| [provisioning-issue-roadmap.md](provisioning-issue-roadmap.md) | Supporting note | Interface-first provisioning issue plan and repository ownership matrix. |
| [ota-issue-roadmap.md](ota-issue-roadmap.md) | Supporting note | Interface-first OTA campaign issue plan and repository ownership matrix. |
| [testing.md](testing.md) | Source | Cross-repository validation commands for pinned snapshots. |
| [adr/README.md](adr/README.md) | Index | Workspace architecture decision record location and format. |

## Workspace E2E Entry Points

| Entry point | Classification | Purpose |
| --- | --- | --- |
| [`../e2e_test/README.md`](../e2e_test/README.md) | Index | Workspace-owned cross-repo E2E test taxonomy, ownership rules, and artifact layout. |
| [`../e2e_test/factory_enroll/README.md`](../e2e_test/factory_enroll/README.md) | Source | Factory enrollment E2E runner and certificate issuance evidence path. |
| [`../e2e_test/video_cloud/load/`](../e2e_test/video_cloud/load/) | Source | API-level video cloud load and WebRTC setup runner. |
| [`../e2e_test/provisioning/account_video_smoke/README.md`](../e2e_test/provisioning/account_video_smoke/README.md) | Planned source | Account Manager + Video Cloud provisioning smoke test boundary and fixture inputs. |
| [`../e2e_test/admin_bff/README.md`](../e2e_test/admin_bff/README.md) | Index | Admin BFF live E2E migration and current service-owned runner locations. |
| [`../e2e_test/fixtures/README.md`](../e2e_test/fixtures/README.md) | Source | Local-only E2E fixture layout for test users and device certsets. |

## Repository Entry Points

| Repository | Classification | Entry point |
| --- | --- | --- |
| `repos/rtk_cloud_contracts_doc` | Contract source of truth | [`README.md`](../repos/rtk_cloud_contracts_doc/README.md) |
| `repos/rtk_cloud_client` | Service-owned docs | [`docs/README.md`](../repos/rtk_cloud_client/docs/README.md) |
| `repos/rtk_video_cloud` | Service-owned docs | [`docs/architecture.md`](../repos/rtk_video_cloud/docs/architecture.md) |
| `repos/rtk_account_manager` | Service-owned docs | [`docs/SPEC.md`](../repos/rtk_account_manager/docs/SPEC.md) |
| `repos/rtk_cloud_frontend` | User-facing website docs | [`README.md`](../repos/rtk_cloud_frontend/README.md) |
| `repos/rtk_cloud_admin` | Admin dashboard docs | [`README.md`](../repos/rtk_cloud_admin/README.md) |

## Reference-Only Documents

Some service repositories contain mirrored or reference-only copies of upstream
contracts for local implementation context. Treat these as supporting notes
unless the document explicitly states that it is the source of truth. For shared
wire, payload, and integration behavior, start from
`repos/rtk_cloud_contracts_doc`.
