# Database simplification implementation

Status: code and the workspace integration PR are merged. Dev schema maintenance and most service checks passed; full live acceptance is blocked by expired PKI CRLs. Staging and production have not been changed.

The isolated workspace was based on main commit `8d66b65c`, including the newer ER HTML document. The original tracked source checkout remains untouched. The user waived a rollback rehearsal; this does not waive schema, data or service verification.

## Scope delivered

- The ER generator handles effective drops and renames, excludes test reset helpers, rejects unsupported persistent DDL, and compares its output with independently initialized PostgreSQL and SQLite catalogs.
- Three duplicate indexes, seven low-use Admin/Video tables, three retired tenant identity tables and `acl_audit_events` are removed. General and ACL audit events share `audit_events` with `audit_domain`, preserving historical IDs, actors, payloads and query isolation.
- Product OTA columns are authoritative. Release, campaign and deployment writes use internal revisions, and deployment event deduplication/sequence changes are atomic. Handoff cancellation is visible to API and workers.
- Old model-keyed firmware routes, tables, workers and client methods are removed. Contracts, Go/JavaScript/Android/Swift/native SDKs, examples and Admin use Product OTA.
- Account Manager and Admin have offline schema maintenance commands; Video Cloud has an explicit cleanup command. Runtime startup rejects incompatible schemas instead of dropping data.
- Billing and PKI data models and keys are unchanged.

## Version set

| Component | PR | Main commit |
|---|---|---|
| Contracts | [#168](https://github.com/hkt999rtk/rtk_cloud_contracts_doc/pull/168) | `7f55243f5fb195af2b7bc78d214ee8ed46915f62` |
| Account Manager | [#334](https://github.com/hkt999rtk/rtk_account_manager/pull/334), [#335](https://github.com/hkt999rtk/rtk_account_manager/pull/335) | `6a3fd7f0cf771321898bc6ee091f67e3f3cda4e5` |
| Video Cloud | [#674](https://github.com/hkt999rtk/rtk_video_cloud/pull/674) | `1a2ae267ff9bd1eb9146fd7f43653a9d7513b43d` |
| Cloud Admin | [#403](https://github.com/hkt999rtk/rtk_cloud_admin/pull/403) | `4d93e5ce5f0b05b9f106bcdeca7a41adcceb4fe2` |
| SDK | [#564](https://github.com/hkt999rtk/rtk_cloud_client/pull/564) | `3a5d6299c944c8f942d627183241a1f77679e138` |
| Workspace integration | [#484](https://github.com/hkt999rtk/rtk_cloud_workspace/pull/484) | `eecbb5a3a0acc8c84d33771bee8ae2a4d1f07d80` |

## Schema and local verification

The predecessor fixtures and current catalogs are in `tests/fixtures/database-simplification/`. Independent initialization observed Account Manager 92→88 tables, Video Cloud/PKI 71→64, and Admin 16→12. These counts describe the catalogs; they are not acceptance targets or performance claims. Fresh versus upgraded catalog equality passed for all three services. The ER atlas includes 224 entities and 235 declared relationships.

The complete local pre-PR gate `db-simplification-final` passed against disposable PostgreSQL and MQTT, with Go and JavaScript coverage, Admin desktop/mobile/visual E2E, contracts, and SDK/native compilation. Tests cover populated audit reconciliation and ID conflicts, identity blockers, OTA CAS and duplicate/out-of-order events, handoff cancellation, schema retry/restart and deprecated route removal. The Account Manager follow-up passed local governed unit coverage and PostgreSQL coverage (80.99% overall; runtime database package 86.34%). Its offline maintenance package has a separate critical-package gate in the workspace policy.

Workspace [#484](https://github.com/hkt999rtk/rtk_cloud_workspace/pull/484) merged only after all required and applicable checks passed. The final [coverage run](https://github.com/hkt999rtk/rtk_cloud_workspace/actions/runs/35732660373) passed Account Manager and Video Cloud PostgreSQL coverage, MQTT, governed Go/JavaScript coverage, catalog/policy checks and aggregate redaction. [Desktop/mobile E2E](https://github.com/hkt999rtk/rtk_cloud_workspace/actions/runs/35732660533), [OpenAPI](https://github.com/hkt999rtk/rtk_cloud_workspace/actions/runs/35732660360) and [submodule pointer](https://github.com/hkt999rtk/rtk_cloud_workspace/actions/runs/35732660344) checks passed. Billing's separate PostgreSQL job was outside the affected-job selector; cross-service Billing behavior was exercised in the local gate.

## Dev cutover observations (2026-09-22)

Only the `video-cloud-dev` stack was mutated. Writers, workers and MQTT were frozen during offline maintenance, then restored. Canonical CI-built images were installed by digest for Account Manager, Video Cloud and Admin; Billing and EMQX images were unchanged. Dev database/PVC scope was checked before mutation; no cluster-wide or shared PVC deletion, certificate replacement, Root CA change or Billing schema change was performed. Existing cloud/device identities were retained.

The dev image manifest points to the merged workspace commit above and the exact Account Manager `sha256:b1ed32d5b70b1d787a6b3c04690ef85b692d25249729deef01d394300889b9fb`, Video Cloud `sha256:3ac8206bdc30e99d8d49d424b752f2e9aeb2f9aaae5b350fe91defa452d3b980` and Admin `sha256:705bbdbb20e26e923e8a3612e2ef30bbf732c319e66d701176d88a71d9c8df10` images.

Account Manager historical migrations advanced from 078 through 082 before simplification 083/084. The offline check/apply/verify completed; four retired identity/audit tables are absent. The 4,383 general and 3 ACL audit rows retained their pre-cutover content hashes exactly. Video Cloud maintenance completed with seven retired tables absent, three OTA revision columns present, 146 devices and 11 sessions retained. Billing still has 4 usage records and 7 cloud receipts. Admin's running SQLite database passed schema version 11 verification. The old `/create_firmware_campaign` and `/firmware_upgrade` routes return HTTP 404; Video Cloud health returns HTTP 200.

Of 24 deployments with desired replicas above zero across Account Manager, Admin, Video Cloud and Billing, 23 are ready. `video-cloud-api-pki` is restarting: controller CRL reads return HTTP 503 because the existing Device Root, Brand and Product CRLs expired on 2026-09-17/18. Root and Brand are offline authorities, so the controller cannot sign their replacements. Draft signing requests are prepared locally under `/tmp/rtk-db-dev-pki-crl-recovery/` (Root CRL 4, Brand CRL 11, preserving 7 Brand revocations); independent review, signing and import remain outstanding. Product CRL renewal and all consumer acknowledgments must follow the existing PKI procedure. Security checks were not disabled.

The live acceptance run reached platform-admin login and Brand Cloud setup, then app-certificate bootstrap repeatedly failed while the PKI dependency was unavailable. It was stopped before device/MQTT/lifecycle assertions. Local Product OTA and SDK suites passed; a full live Product OTA workflow has not been claimed. The final Account Manager image from #335 (`sha256:b1ed32d5b70b1d787a6b3c04690ef85b692d25249729deef01d394300889b9fb`) was installed by digest after its merge; its API and four workers are ready without restarts, and schema verification passes.

## Protected-environment blockers

A read-only staging inventory found Account Manager schema 082, no remaining tenant user/member/token rows or audit ID collisions, and no rows in Video Cloud's seven low-use tables. Four old firmware rows remain. They require trusted ownership/integrity/state mapping or explicit disposition before the destructive cleanup; the maintenance check intentionally blocks them. Staging was not migrated or deployed. Production inventory is unavailable because no production kubeconfig is present in this workspace; production was not touched.

## Remaining delivery steps

1. Complete the established offline PKI CRL ceremony for the dev Root and Brand, renew the Product CRL, install/acknowledge them, then rerun the full dev data, MQTT, billing, lifecycle and Product OTA acceptance matrix. Record the resulting dev pass/fail evidence.
2. Resolve the four staging legacy-firmware rows through a reviewed data disposition before a protected-environment upgrade. Inspect production through its own authorized environment before planning production promotion.
