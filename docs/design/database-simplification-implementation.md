# Database simplification implementation

Status: code complete across the five service/client repositories; the workspace integration PR and coordinated dev acceptance are still open. This page records observed results, including the current dev PKI blocker. Staging and production have not been changed.

The isolated workspace was based on main commit `8d66b65c`, including the newer ER HTML document. The original tracked source checkout remains untouched. The user waived a rollback rehearsal; this does not waive schema, data or service verification.

## Scope delivered

- The ER generator handles effective drops and renames, excludes test reset helpers, rejects unsupported persistent DDL, and compares its output with independently initialized PostgreSQL and SQLite catalogs.
- Three duplicate indexes, seven low-use Admin/Video tables, three retired tenant identity tables and `acl_audit_events` are removed. General and ACL audit events share `audit_events` with `audit_domain`, preserving historical IDs, actors, payloads and query isolation.
- Product OTA columns are authoritative. Release, campaign and deployment writes use internal revisions, and deployment event deduplication/sequence changes are atomic. Handoff cancellation is visible to API and workers.
- Old model-keyed firmware routes, tables, workers and client methods are removed. Contracts, Go/JavaScript/Android/Swift/native SDKs, examples and Admin use Product OTA.
- Account Manager and Admin have offline schema maintenance commands; Video Cloud has an explicit cleanup command. Runtime startup rejects incompatible schemas instead of dropping data.
- Billing and PKI data models and keys are unchanged.

## Version set

| Component | PR | Main commit or current follow-up |
|---|---|---|
| Contracts | [#168](https://github.com/hkt999rtk/rtk_cloud_contracts_doc/pull/168) | `7f55243f5fb195af2b7bc78d214ee8ed46915f62` |
| Account Manager | [#334](https://github.com/hkt999rtk/rtk_account_manager/pull/334), [#335](https://github.com/hkt999rtk/rtk_account_manager/pull/335) | `6a3fd7f0cf771321898bc6ee091f67e3f3cda4e5` |
| Video Cloud | [#674](https://github.com/hkt999rtk/rtk_video_cloud/pull/674) | `1a2ae267ff9bd1eb9146fd7f43653a9d7513b43d` |
| Cloud Admin | [#403](https://github.com/hkt999rtk/rtk_cloud_admin/pull/403) | `4d93e5ce5f0b05b9f106bcdeca7a41adcceb4fe2` |
| SDK | [#564](https://github.com/hkt999rtk/rtk_cloud_client/pull/564) | `3a5d6299c944c8f942d627183241a1f77679e138` |
| Workspace integration | [#484](https://github.com/hkt999rtk/rtk_cloud_workspace/pull/484) | CI rerun pending with exact Account Manager main revision |

## Schema and local verification

The predecessor fixtures and current catalogs are in `tests/fixtures/database-simplification/`. Independent initialization observed Account Manager 92→88 tables, Video Cloud/PKI 71→64, and Admin 16→12. These counts describe the catalogs; they are not acceptance targets or performance claims. Fresh versus upgraded catalog equality passed for all three services. The ER atlas includes 224 entities and 235 declared relationships.

The complete local pre-PR gate `db-simplification-final` passed against disposable PostgreSQL and MQTT, with Go and JavaScript coverage, Admin desktop/mobile/visual E2E, contracts, and SDK/native compilation. Tests cover populated audit reconciliation and ID conflicts, identity blockers, OTA CAS and duplicate/out-of-order events, handoff cancellation, schema retry/restart and deprecated route removal. The Account Manager follow-up passed local governed unit coverage and PostgreSQL coverage (80.99% overall; runtime database package 86.34%). Its offline maintenance package has a separate critical-package gate in the workspace policy.

## Dev cutover observations (2026-09-22)

Only the `video-cloud-dev` stack was mutated. Writers, workers and MQTT were frozen during offline maintenance, then restored. Canonical CI-built images were installed by digest for Account Manager, Video Cloud and Admin; Billing and EMQX images were unchanged. Dev database/PVC scope was checked before mutation; no cluster-wide or shared PVC deletion, certificate replacement, Root CA change or Billing schema change was performed. Existing cloud/device identities were retained.

Account Manager historical migrations advanced from 078 through 082 before simplification 083/084. The offline check/apply/verify completed; four retired identity/audit tables are absent. The 4,383 general and 3 ACL audit rows retained their pre-cutover content hashes exactly. Video Cloud maintenance completed with seven retired tables absent, three OTA revision columns present, 146 devices and 11 sessions retained. Billing still has 4 usage records and 7 cloud receipts. Admin's running SQLite database passed schema version 11 verification. The old `/create_firmware_campaign` and `/firmware_upgrade` routes return HTTP 404; Video Cloud health returns HTTP 200.

Of 24 deployments with desired replicas above zero across Account Manager, Admin, Video Cloud and Billing, 23 are ready. `video-cloud-api-pki` is restarting: controller CRL reads return HTTP 503 because the existing Device Root, Brand and Product CRLs expired on 2026-09-17/18. Root and Brand are offline authorities, so the controller cannot sign their replacements. Draft signing requests are prepared locally under `/tmp/rtk-db-dev-pki-crl-recovery/` (Root CRL 4, Brand CRL 11, preserving 7 Brand revocations); independent review, signing and import remain outstanding. Product CRL renewal and all consumer acknowledgments must follow the existing PKI procedure. Security checks were not disabled.

The live acceptance run reached platform-admin login and Brand Cloud setup, then app-certificate bootstrap repeatedly failed while the PKI dependency was unavailable. It was stopped before device/MQTT/lifecycle assertions. Local Product OTA and SDK suites passed; a full live Product OTA workflow has not been claimed. The replacement Account Manager image from #335 also remains to be installed after its merge so the dev image set matches the final source revision.

## Protected-environment blockers

A read-only staging inventory found Account Manager schema 082, no remaining tenant user/member/token rows or audit ID collisions, and no rows in Video Cloud's seven low-use tables. Four old firmware rows remain. They require trusted ownership/integrity/state mapping or explicit disposition before the destructive cleanup; the maintenance check intentionally blocks them. Staging was not migrated or deployed. Production inventory is unavailable because no production kubeconfig is present in this workspace; production was not touched.

## Remaining delivery steps

1. Pass workspace #484 CI with the exact Account Manager main revision and merge it.
2. Install that exact Account Manager image in dev and verify service restart and schema compatibility.
3. Complete the established offline PKI CRL ceremony for the dev Root and Brand, renew the Product CRL, install/acknowledge them, then rerun the full dev data, MQTT, billing, lifecycle and Product OTA acceptance matrix.
4. Record the final merged workspace commit, CI and dev acceptance results. Staging/prod promotion is a separate operation after their data blockers are resolved.
