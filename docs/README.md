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
| [test-catalog.md](test-catalog.md) | Generated human-readable Test ID catalog. |
| [../e2e_test/README.md](../e2e_test/README.md) | Workspace E2E taxonomy and runner ownership. |
| [../loadtests/home-100k/README.md](../loadtests/home-100k/README.md) | Home MQTT/shadow and WebRTC/TURN load profiles. |
| [product-level-evidence.md](product-level-evidence.md) | Product readiness evidence aggregation and redaction. |

## Troubleshooting And Handoff

| Document | Purpose |
| --- | --- |
| [staging-runtime-bootstrap.zh-TW.md](staging-runtime-bootstrap.zh-TW.md) | Restore or validate matching ignored runtime on another controller. |
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
| Frontend | [repos/rtk_cloud_frontend](../repos/rtk_cloud_frontend/README.md) |
| Cloud Admin | [repos/rtk_cloud_admin](../repos/rtk_cloud_admin/README.md) |
