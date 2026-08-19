# RTK Cloud Documentation Index

Workspace documents coordinate cross-repository deployment, testing,
architecture, governance, and evidence. Service implementation details belong
in the owning service repository; shared wire and payload contracts belong in
`repos/rtk_cloud_contracts_doc`.

## Start Here

| Goal | Document |
| --- | --- |
| Prepare, deploy, restore, accept, or remove an environment | [Deployment 操作指南](deployment-operations.zh-TW.md) |
| Prepare and run local, acceptance, qualification, or load tests | [測試操作指南](testing-operations.zh-TW.md) |
| Create tracked environment intent and overrides | [Environment README](../cloud_env/README.md) |
| Build a fresh staging environment and run canonical 1K validation | [Staging from scratch](staging-from-scratch.md) |

## Workspace Documents

| Document | Classification | Purpose |
| --- | --- | --- |
| [architecture.md](architecture.md) | Source | Cross-repo boundaries and source-of-truth model. |
| [account-manager-admin-boundary.md](account-manager-admin-boundary.md) | Source | Boundary between Account Manager as backend control plane and Admin as enterprise dashboard/BFF. |
| [documentation-governance.md](documentation-governance.md) | Source | Documentation ownership, status, and review rules. |
| [contracts-submodule-governance.md](contracts-submodule-governance.md) | Source | Contracts submodule path, URL, and commit alignment policy. |
| [artifact-release-governance.md](artifact-release-governance.md) | Source | Linode Object Storage artifact source-of-truth policy and adoption matrix. |
| [dependency-failure-policy.md](dependency-failure-policy.md) | Source | Cross-service dependency failure policy for startup-critical dependencies, request-scoped upstreams, durable async delivery, observability, and optional features. |
| [backend-release-readiness.md](backend-release-readiness.md) | Supporting note | Backend foundation closeout checklist, validation commands, report status, and remaining release-evidence items. |
| [deployment-secrets-governance.md](deployment-secrets-governance.md) | Source | Local deployment secret layout, environment/provider/service taxonomy, and handling rules. |
| [lke-migration-inventory.md](lke-migration-inventory.md) | Source | Documentation-first LKE migration inventory, current architecture review, target summary, and implementation gates. |
| [cost/README.md](cost/README.md) | Index | AWS cost estimation materials, including service mapping, sizing worksheet, pricing sources, and support-plan assumptions. |
| [persistence-cache-refactor-roadmap.md](persistence-cache-refactor-roadmap.md) | Source | Cross-repo persistence boundary audit and Redis/cache-readiness issue roadmap. |
| [postgres-capacity-expansion-runbook.md](postgres-capacity-expansion-runbook.md) | Source | PostgreSQL storage-pressure classification, LKE PVC expansion, Linode fallback, HA, cache/API boundaries, and validation evidence. |
| [realtek-connect-plus-gap-analysis.md](realtek-connect-plus-gap-analysis.md) | Discussion note | Evidence-backed gaps between Realtek Connect+ promotion content and current implementation. |
| [implementation-gap-backlog.md](implementation-gap-backlog.md) | Supporting note | Post-interface implementation and test gaps to open as owner-repo issues. |
| [core-platform-gap-roadmap.md](core-platform-gap-roadmap.md) | Supporting note | Core platform gap roadmap for readiness, private cloud, account lifecycle, fleet, telemetry, SDK/app, WebRTC, and smart-home ecosystem boundaries. |
| [business-model.md](business-model.md) | Source | Tier structure, evaluation limits, pricing model framing, SDK licensing posture, and website disclosure rules. |
| [payment-abstraction-rollout-plan.md](payment-abstraction-rollout-plan.md) | Source | Documentation-first ownership, provider-neutral balance/payment architecture, NewebPay prerequisites, implementation sequence, test evidence, and rollout gates. |
| [private-cloud-deployment.md](private-cloud-deployment.md) | Supporting note | Private-cloud deployment bill of materials, deployment order, profiles, operations runbook, support boundary, and follow-up routing. |
| [product-level-evidence.md](product-level-evidence.md) | Supporting note | Workspace evidence wrapper for private-cloud readiness sign-off and canonical report aggregation. |
| [linode-staging-deployment-snapshot.md](linode-staging-deployment-snapshot.md) | Supporting note | Current Linode staging deployment snapshot, live endpoints, PR state, and remaining production-readiness work. |
| [staging-from-scratch.md](staging-from-scratch.md) | Source | Fresh-clone LKE staging deployment, billing setup, acceptance, and canonical 1K MQTT/Device Shadow validation. |
| [linode-ci-runners.md](linode-ci-runners.md) | Source | Linode self-hosted CI runner VM topology, lifecycle, artifact archive, and shutdown policy. |
| [linode-100k-home-iot-shadow-loadtest.md](linode-100k-home-iot-shadow-loadtest.md) | Pointer | Moved pointer for the 100,000-device Home IoT Device Shadow load-test package under `loadtests/home-100k/`. |
| [status-reports/README.md](status-reports/README.md) | Source | Reusable weekly status report framework, material index, and builder workflow. |
| [webrtc-only-streaming-migration.md](webrtc-only-streaming-migration.md) | Supporting note | WebRTC-only video migration issue roadmap and validation checklist. |
| [video-cloud-load-test-roadmap.md](video-cloud-load-test-roadmap.md) | Supporting note | API-level video cloud E2E load test roadmap, issue owner matrix, and validation checklist. |
| [home-mqtt-loadtest-simulation.md](home-mqtt-loadtest-simulation.md) | Pointer | Moved pointer for the home MQTT simulation reference now centralized under `loadtests/home-100k/docs/`. |
| [cross-service-broker-packaging.md](cross-service-broker-packaging.md) | Supporting note | Retired cross-service broker packaging decision and future reintroduction bar. |
| [provisioning-issue-roadmap.md](provisioning-issue-roadmap.md) | Supporting note | Interface-first provisioning issue plan and repository ownership matrix. |
| [ota-issue-roadmap.md](ota-issue-roadmap.md) | Supporting note | Interface-first OTA campaign issue plan and repository ownership matrix. |
| [testing.md](testing.md) | Source | Cross-repository validation commands for pinned snapshots. |
| [adr/README.md](adr/README.md) | Index | Workspace architecture decision record location and format. |

The active runtime contract is `cloud_env/<environment>/runtime`. References to
`cloud_env/staging/lke` or `cloud_env/staging/linode` below are historical
evidence, not current operator instructions.

## Deployment

| Document | Purpose |
| --- | --- |
| [cloud-deployment-architecture.md](cloud-deployment-architecture.md) | Provider-neutral environment, architecture, adapter, and runtime model. |
| [cloud-env-layout.zh-TW.md](cloud-env-layout.zh-TW.md) | Tracked environment and ignored runtime directory layout. |
| [deployment-secrets-governance.md](deployment-secrets-governance.md) | Secret ownership, storage, injection, and redaction rules. |
| [dns-adapter-architecture.md](dns-adapter-architecture.md) | DNS adapter selection, credentials, state, and mutation boundary. |
| [lke-external-haproxy-edge.md](lke-external-haproxy-edge.md) | Current LKE external HAProxy edge contract. |
| [private-cloud-deployment.md](private-cloud-deployment.md) | Supporting private-cloud BOM and operational boundary. |
| [postgres-capacity-expansion-runbook.md](postgres-capacity-expansion-runbook.md) | PostgreSQL capacity and storage expansion procedure. |

## Testing

| Document | Purpose |
| --- | --- |
| [testing.md](testing.md) | Test governance, commands, coverage, artifacts, and evidence schemas. |
| [billing-staging-qualification.md](billing-staging-qualification.md) | Canonical agent/operator workflow for deployed Billing payment and Cloud Admin portal qualification. |
| [test-catalog.md](test-catalog.md) | Generated human-readable Test ID catalog. |
| [../e2e_test/README.md](../e2e_test/README.md) | Workspace E2E taxonomy and runner ownership. |
| [../loadtests/home-100k/README.md](../loadtests/home-100k/README.md) | Home MQTT/shadow and WebRTC/TURN load profiles. |
| [product-level-evidence.md](product-level-evidence.md) | Product readiness evidence aggregation and redaction. |

## Troubleshooting And Handoff

| Document | Purpose |
| --- | --- |
| [staging-runtime-bootstrap.zh-TW.md](staging-runtime-bootstrap.zh-TW.md) | Restore or validate matching ignored runtime on another controller. |
| [billing-staging-qualification.md](billing-staging-qualification.md) | Dispatch, monitor, assess, and hand off the Billing staging qualification without exposing credentials. |
| [../loadtests/home-100k/docs/prepare-another-machine.md](../loadtests/home-100k/docs/prepare-another-machine.md) | Prepare another Home load-test controller or generator. |
| [dependency-failure-policy.md](dependency-failure-policy.md) | Cross-service startup/request/async dependency failure policy. |
| [backend-release-readiness.md](backend-release-readiness.md) | Backend validation status and release evidence checklist. |
| [linode-ci-runners.md](linode-ci-runners.md) | CI runner lifecycle; separate from staging application deployment. |

## Architecture And Governance

| Document | Purpose |
| --- | --- |
| [architecture.md](architecture.md) | Repository boundaries and source-of-truth model. |
| [documentation-governance.md](documentation-governance.md) | Ownership, classification, review, and drift prevention. |
| [contracts-submodule-governance.md](contracts-submodule-governance.md) | Canonical contracts submodule path, URL, and commit alignment. |
| [artifact-release-governance.md](artifact-release-governance.md) | Versioned release artifact policy and adoption matrix. |
| [account-manager-admin-boundary.md](account-manager-admin-boundary.md) | Account Manager and Cloud Admin responsibility boundary. |
| [service-logging-architecture.md](service-logging-architecture.md) | Cross-service logging topology and ownership. |
| [adr/README.md](adr/README.md) | Architecture decision records. |

## Historical Evidence

These files support audit and comparison only. They must not be used as active
deployment or test commands.

| Evidence | Classification |
| --- | --- |
| [linode-staging-deployment-snapshot.md](linode-staging-deployment-snapshot.md) | Retired VM staging snapshot. |
| [lke-capacity-sizing.md](lke-capacity-sizing.md) | Historical capacity experiments; embedded old paths identify original evidence locations. |
| [lke-migration-inventory.md](lke-migration-inventory.md) | Migration decision and implementation history. |
| [status-reports/README.md](status-reports/README.md) | Status-report materials and generated reporting workflow. |

Roadmaps, gap analyses, business material, and other supporting notes remain in
this directory and are searchable by filename. They are not operator entry
points unless linked from one of the active guides above.

## Repository Documentation

| Repository | Entry point |
| --- | --- |
| Contracts source of truth | [repos/rtk_cloud_contracts_doc](../repos/rtk_cloud_contracts_doc/README.md) |
| Client SDK | [repos/rtk_cloud_client/docs](../repos/rtk_cloud_client/docs/README.md) |
| Video Cloud | [repos/rtk_video_cloud/docs](../repos/rtk_video_cloud/docs/architecture.md) |
| Account Manager | [repos/rtk_account_manager/docs](../repos/rtk_account_manager/docs/SPEC.md) |
| Billing | [repos/rtk_billing](../repos/rtk_billing/README.md) |
| Frontend | [repos/rtk_cloud_frontend](../repos/rtk_cloud_frontend/README.md) |
| Cloud Admin | [repos/rtk_cloud_admin](../repos/rtk_cloud_admin/README.md) |
