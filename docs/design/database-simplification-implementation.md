# Database simplification implementation

Status: database simplification code and workspace integration are merged. Dev schema maintenance passed, the dev Device PKI chain was rebuilt, and fresh P-256 factory enrollment and data setup pass. Full dev MQTT, Billing-event, lifecycle and live Product OTA acceptance remain blocked by a split legacy/PKI MQTT runtime and token-policy mismatch. Staging and production have not been changed.

The isolated workspace was based on main commit `8d66b65c`, including the newer ER HTML document. The original tracked source checkout remains untouched. The user waived a rollback rehearsal; this does not waive schema, data or service verification.

## Scope delivered

- The ER generator handles effective drops and renames, excludes test reset helpers, rejects unsupported persistent DDL, and compares its output with independently initialized PostgreSQL and SQLite catalogs.
- Three duplicate indexes, seven low-use Admin/Video tables, three retired tenant identity tables and `acl_audit_events` are removed. General and ACL audit events share `audit_events` with `audit_domain`, preserving historical IDs, actors, payloads and query isolation.
- Product OTA columns are authoritative. Release, campaign and deployment writes use internal revisions, and deployment event deduplication/sequence changes are atomic. Handoff cancellation is visible to API and workers.
- Old model-keyed firmware routes, tables, workers and client methods are removed. Contracts, Go/JavaScript/Android/Swift/native SDKs, examples and Admin use Product OTA.
- Account Manager and Admin have offline schema maintenance commands; Video Cloud has an explicit cleanup command. Runtime startup rejects incompatible schemas instead of dropping data.
- Billing and PKI data models are unchanged. During dev acceptance, the dev Device Root and its descendants were deliberately reissued after the old chain's CRLs expired; service, app, MQTT, OpenBao transport and Billing authorities were retained.

## Version set

| Component | PR | Main commit |
|---|---|---|
| Contracts | [#168](https://github.com/hkt999rtk/rtk_cloud_contracts_doc/pull/168) | `7f55243f5fb195af2b7bc78d214ee8ed46915f62` |
| Account Manager | [#334](https://github.com/hkt999rtk/rtk_account_manager/pull/334), [#335](https://github.com/hkt999rtk/rtk_account_manager/pull/335) | `6a3fd7f0cf771321898bc6ee091f67e3f3cda4e5` |
| Video Cloud | [#674](https://github.com/hkt999rtk/rtk_video_cloud/pull/674), [#675](https://github.com/hkt999rtk/rtk_video_cloud/pull/675), [#676](https://github.com/hkt999rtk/rtk_video_cloud/pull/676), [#677](https://github.com/hkt999rtk/rtk_video_cloud/pull/677) | `08c904c1bb4237f3e3049742b5dd067728616d1e` |
| Cloud Admin | [#403](https://github.com/hkt999rtk/rtk_cloud_admin/pull/403) | `4d93e5ce5f0b05b9f106bcdeca7a41adcceb4fe2` |
| SDK | [#564](https://github.com/hkt999rtk/rtk_cloud_client/pull/564) | `3a5d6299c944c8f942d627183241a1f77679e138` |
| Workspace integration | [#484](https://github.com/hkt999rtk/rtk_cloud_workspace/pull/484) | `eecbb5a3a0acc8c84d33771bee8ae2a4d1f07d80` |

## Schema and local verification

The predecessor fixtures and current catalogs are in `tests/fixtures/database-simplification/`. Independent initialization observed Account Manager 92→88 tables, Video Cloud/PKI 71→64, and Admin 16→12. These counts describe the catalogs; they are not acceptance targets or performance claims. Fresh versus upgraded catalog equality passed for all three services. The ER atlas includes 224 entities and 235 declared relationships.

The complete local pre-PR gate `db-simplification-final` passed against disposable PostgreSQL and MQTT, with Go and JavaScript coverage, Admin desktop/mobile/visual E2E, contracts, and SDK/native compilation. Tests cover populated audit reconciliation and ID conflicts, identity blockers, OTA CAS and duplicate/out-of-order events, handoff cancellation, schema retry/restart and deprecated route removal. The Account Manager follow-up passed local governed unit coverage and PostgreSQL coverage (80.99% overall; runtime database package 86.34%). Its offline maintenance package has a separate critical-package gate in the workspace policy.

Workspace [#484](https://github.com/hkt999rtk/rtk_cloud_workspace/pull/484) merged only after all required and applicable checks passed. The final [coverage run](https://github.com/hkt999rtk/rtk_cloud_workspace/actions/runs/35732660373) passed Account Manager and Video Cloud PostgreSQL coverage, MQTT, governed Go/JavaScript coverage, catalog/policy checks and aggregate redaction. [Desktop/mobile E2E](https://github.com/hkt999rtk/rtk_cloud_workspace/actions/runs/35732660533), [OpenAPI](https://github.com/hkt999rtk/rtk_cloud_workspace/actions/runs/35732660360) and [submodule pointer](https://github.com/hkt999rtk/rtk_cloud_workspace/actions/runs/35732660344) checks passed. Billing's separate PostgreSQL job was outside the affected-job selector; cross-service Billing behavior was exercised in the local gate.

## Dev cutover observations (2026-09-22 to 2026-09-23)

Only the `video-cloud-dev` stack was mutated. Writers, workers and MQTT were frozen during offline maintenance, then restored. Canonical CI-built images were installed by digest for Account Manager, Video Cloud and Admin; Billing and EMQX images were unchanged. Dev database/PVC scope was checked before mutation; no cluster-wide or shared PVC deletion, certificate replacement, Root CA change or Billing schema change was performed. Existing cloud/device identities were retained.

The dev image manifest points to the merged workspace commit above and the exact Account Manager `sha256:b1ed32d5b70b1d787a6b3c04690ef85b692d25249729deef01d394300889b9fb`, Video Cloud `sha256:3ac8206bdc30e99d8d49d424b752f2e9aeb2f9aaae5b350fe91defa452d3b980` and Admin `sha256:705bbdbb20e26e923e8a3612e2ef30bbf732c319e66d701176d88a71d9c8df10` images.

Account Manager historical migrations advanced from 078 through 082 before simplification 083/084. The offline check/apply/verify completed; four retired identity/audit tables are absent. The 4,383 general and 3 ACL audit rows retained their pre-cutover content hashes exactly. Video Cloud maintenance completed with seven retired tables absent, three OTA revision columns present, 146 devices and 11 sessions retained. Billing still has 4 usage records and 7 cloud receipts. Admin's running SQLite database passed schema version 11 verification. The old `/create_firmware_campaign` and `/firmware_upgrade` routes return HTTP 404; Video Cloud health returns HTTP 200.

The old dev Device Root `c92fdbb1-f87b-4cab-80a6-fa77c2dce1d8` was reset during a fenced maintenance window. Eleven descendant issuers and 44 device bindings were marked revoked, and the old Root remains in the cumulative distrust policy. The replacement OpenBao-backed Root `ad5f7da3-97d7-4655-86e4-4365f3256d67` has certificate SHA-256 `1faac429c8b91ed85b120a6918ee957db4e91bf960f60ef32c73f4e59a28cf9c`. Its activation followed genuine broker and API consumer acknowledgments. `video-cloud-api-pki`, `mqtt-pki`, controller, certissuer, factoryenroll, Account Manager and Video Cloud API are ready. Root/bootstrap jobs and public evidence are held under `/tmp/rtk-dev-device-pki-rebuild-20260922/`; no private Device Root key was exported.

Account Manager's 28 automatic Cloud/Product CA jobs initially failed because the Video Cloud management proxy omitted `POST /v1/pki/automatic` from its exact route allowlist. The route fix and regression test passed the local pre-PR gate and Video Cloud CI in [#675](https://github.com/hkt999rtk/rtk_video_cloud/pull/675), merged as `f40d9f72548516e5c68d553c8977d170c65e5573`. Its published image `sha256:9c89d33947811c7c0bd1a20e8c173f9f49eaaaa26ee552cc9d20e1788aca6eca` runs in the dev Account Manager management sidecar. After a fenced, read-only-checked requeue, all 28 original jobs reached ready. Four additional Product jobs created by live acceptance also reached ready. The controller has one active new Root, seven active Brand issuers and 25 active Product issuers.

The earlier live acceptance run stopped at App certificate bootstrap while the old Device CRLs were expired. After the new Device chain was activated, App certificate bootstrap passed. The acceptance tooling now waits for Product CA creation and controller activation before requesting factory device enrollment; focused tests cover pending, active, failed, denied and bounded retry cases. Video Cloud #677 corrected Product issuer selection to parse a device CSR instead of applying CA-only constraints. The dev device profile is now explicitly P-256, matching the current OpenBao signing role. Fresh one-device and two-device factory enrollment, binding, provisioning and Product-access validation pass. An Ed25519 attempt reached issuer selection but remained unresolved during signing; it is not an accepted dev algorithm.

The live MQTT run found and corrected a stale broker-auth key in the dev Video Cloud Secret, after which App and device authentication and device-to-App telemetry passed on the legacy `mqtt` service. The shadow command did not complete because Video Cloud consumes shadow requests from the separate `mqtt-pki` service. Directly testing `mqtt-pki` then showed its MQTT App-PKI policy requires certificate-bound App tokens, while the public token path does not issue those claims. A temporary policy change also caused device-token issuance to fail and was removed. Billing log/ledger checks cannot receive a new usage event until this MQTT path is unified; preserved Billing rows remain intact. Two fresh devices satisfy the lifecycle fixture, but transport readiness fails at device-token issuance. A full live Product OTA workflow therefore remains pending. Local Product OTA, concurrency and SDK suites passed. The final Account Manager image from #335 (`sha256:b1ed32d5b70b1d787a6b3c04690ef85b692d25249729deef01d394300889b9fb`) remains installed; its API and four workers are ready, and schema verification passes.

## Protected-environment blockers

A read-only staging inventory found Account Manager schema 082, no remaining tenant user/member/token rows or audit ID collisions, and no rows in Video Cloud's seven low-use tables. Four old firmware rows remain. They require trusted ownership/integrity/state mapping or explicit disposition before the destructive cleanup; the maintenance check intentionally blocks them. Staging was not migrated or deployed. Production inventory is unavailable because no production kubeconfig is present in this workspace; production was not touched.

## Remaining delivery steps

1. Converge the dev public MQTT endpoint, Video Cloud shadow consumer and MQTT App-PKI token policy on one broker path. Then rerun MQTT, Billing-event, lifecycle and live Product OTA acceptance and record the evidence.
2. Decide whether Ed25519 device CSRs will be supported by the OpenBao signing role. Until then, keep dev device enrollment explicitly P-256 and reject unsupported algorithms before reservation.
3. Resolve the four staging legacy-firmware rows through a reviewed data disposition before a protected-environment upgrade. Inspect production through its own authorized environment before planning production promotion.
