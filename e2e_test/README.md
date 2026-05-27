# Workspace E2E Tests

This directory is the workspace-owned entry point for cross-repository and
cross-cloud end-to-end tests. Service repositories may keep service-local unit,
integration, and component tests, but product-level E2E runners, operator
scripts, and E2E fixture documentation should be indexed here.

## Directory Layout

| Directory | Scope | Status |
| --- | --- | --- |
| `factory_enroll/` | Factory enrollment bridge to cert issuer and device certificate issuance. | Implemented. |
| `video_cloud/load/` | API-level video cloud load and WebRTC setup runner. | Implemented. |
| `provisioning/account_video_smoke/` | Account Manager + Video Cloud provisioning smoke using test users and factory certsets. | Implemented; live staging prerequisites may report `BLOCKED`. |
| `admin_bff/` | Admin dashboard live BFF E2E entry points and ownership notes. | Indexed; runner still lives in `rtk_cloud_admin` until migrated. |
| `fixtures/` | Fixture layout and local secret/artifact conventions for E2E runs. | Documentation only; secrets stay untracked. |

## Artifact Layout

Generated E2E artifacts belong under:

```text
.artifacts/e2e_test/<suite>/<run_id>/
```

Shared E2E fixtures that are generated locally but not committed belong under:

```text
.artifacts/e2e_test/fixtures/<fixture_type>/<run_id>/
```

Do not commit private keys, passwords, bearer tokens, raw service responses that
contain secrets, or generated certificates unless a repository explicitly marks a
sample fixture as safe and non-secret.

## Ownership Rules

- Workspace owns cross-repo orchestration, E2E runners, fixture indexing, and
  product-level E2E reports.
- Service repos own service-local tests, APIs, mocks, and prerequisites.
- Contracts docs define cross-repo interface semantics, not E2E implementation.
- Service-owned live scripts should either move here when they become
  cross-repo product E2E, or be indexed here with a migration note.

## Current Local Fixtures

Known local fixtures may exist at:

```text
.artifacts/e2e_test/fixtures/account_manager_test_users/20260519T014833Z/
../rtk_video_cloud/keys/factory-linode-certset-20260514T225802Z/
```

The first stores Account Manager test users. The second stores operator-held
factory-enrolled device keys and certificates. Both are local-only and must stay
outside tracked git content.
