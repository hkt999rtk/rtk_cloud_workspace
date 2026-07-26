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
| `provisioning/bulk_bind_validation/` | Bulk bind/provision artifact validation for 100-device staging onboarding. | Implemented. |
| `../scripts/staging_email_signup_e2e.py` | Deployed staging customer signup, HTTPS Send Mail delivery, local IMAP receipt, and browser activation. | Implemented; explicit operator opt-in only. |
| `admin_bff/` | Admin dashboard live BFF E2E entry points and ownership notes. | Indexed; runner still lives in `rtk_cloud_admin` until migrated. |
| `cloud_validation/` | Android/iOS sample app SDK validation against a deployed Cloud plus a real-certificate virtual device. | Implemented with built-in staging fixture/evidence/cleanup providers, Cloud WebSocket command trigger, and deploy/nightly/release profiles. |
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

## Test IDs and Reports

All case-level E2E scenarios are registered in the workspace
`tests/catalog.yaml`; repositories must not create a competing ID scheme. A
published ID is immutable. Retired cases keep their ID with `status: retired`.
SDK cloud-validation manifests retain the legacy `id` as `scenario_id` and add
the catalog `test_id`; JSON, JUnit, and Markdown reports expose both.

Each final report records the Test ID, execution time and duration, purpose,
method, result, and explicit `PASS`/`FAIL` assessment. Run-scoped fixtures and
evidence must be attributable to the same run ID and preserve workspace,
submodule, SDK, or server revisions as applicable.

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

## Cloud Send Mail + local IMAP signup activation

`E2E-CA-SIGNUP-EMAIL-001` validates the deployed staging flow: Account Manager
queues an email verification message, the cloud email worker sends it from
`no-reply@realtekconnect.com`, and a browser completes the customer activation.
It is intentionally not a CI/nightly test because the operator-held Send Mail
Bearer token and IMAP credentials must remain outside tracked content.

The receiver mailbox must support plus-addressing. Each run uses
`imap-test01+<run-id>@realtekconnect.com`, polls the `imap-test01` inbox in
read-only mode, and keeps the created E2E account/organization for audit.

```sh
RUN_LIVE_EMAIL_E2E=1 \
  python3 scripts/staging_email_signup_e2e.py \
  --confirm video-cloud-staging
```

The command passes `SENDMAIL_HTTP_BEARER_TOKEN` only to the scoped LKE
Account Manager rollout and maps IMAP credentials from `~/.env` only to the
local browser/helper process. It writes redacted evidence to
`.artifacts/e2e_test/email-signup/<run-id>/`. Use `--skip-deploy` only after
the same Send Mail configuration has already rolled out successfully.
