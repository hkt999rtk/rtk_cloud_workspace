# Environment PKI operator authority: design and test plan

Status: accepted design and dev implementation (2026-09-29). Controller, proxy,
console and deployment checks are merged. The dev Service successor cutover and
DB/Loki evidence passed; staging and production require their own qualification.
Scope: environment CA and Service identity creation, update, rotation, revocation
and recovery. Billing price publication and Account Manager administrator-account
recovery are separate workflows and are not changed by this certificate plan.

Owner: rtk_cloud_workspace. Normative policy: [Platform PKI](../../repos/rtk_cloud_contracts_doc/platform_pki.md)
§7; deployment identity rules: [Deployment service identities](deployment-service-identities.md).

## Document inventory and disposition

| Document | Relevant authority wording | Disposition |
| --- | --- | --- |
| `repos/rtk_cloud_contracts_doc/platform_pki.md` | CA and deployment authority | Target policy and database/Loki evidence split updated. |
| `docs/design/deployment-service-identities.md` | SecretStore, initial and later Service identities | Target operator flow and migration gate updated. |
| `docs/product-services-dev-pki.md` and `docs/design/ota-pricing-activation-and-disclosure-plan.md` | OTA successor Service issuer | Dev operation activated; remaining Product identities, grants and billing gates distinguished. |
| `repos/rtk_video_cloud/docs/production-pki-controller.md` and `deploy/pki/README.md` | Controller roles, API and Service policy | Operator route and retained historical flow documented. |
| `repos/rtk_account_manager/docs/pki-controller-integration.md` and `private_cloud_deployment_runbook.md` | Role creation, bootstrap and proxy | Configured operator proxy check and recovery exception documented. |
| `repos/rtk_cloud_admin/README.md` | Legacy PKI import approval | Operator review and authorization flow documented. |
| Billing pricing/activation documents | Finance review and rate publication | Separate commercial decision; not changed by PKI authority policy. |

## Authority and evidence

One authenticated operator selected from the environment configuration may
request, inspect, authorize and execute that environment's PKI operation. The
same operator may perform all steps. `pki_admin` and `security_custodian` are
not required as separate people or countersignatures. The PKI WebUI and the
Platform Admin WebUI may remain separate surfaces. A browser role label by
itself is not proof of environment signing authority.

Authorization is bound to the exact environment, operator identity, immutable
issuer/parent IDs, allowed Service subjects and server DNS names, request
digest, signer reference and intended operation. The audit stores who acted,
when, which digest was reviewed, what the signer returned and whether registry,
provider and trust consumers agreed. It does not store private keys or secret
values. Replaying the same idempotency key with changed content fails.

The authorization record has two purposes with different failure behavior:

| Record | Contents and purpose | If unavailable |
| --- | --- | --- |
| `pki_operator_authorizations` (PKI PostgreSQL) | One append-only row per operation with operator ID, environment, exact request digest, signer reference and authorization times. The controller checks this row against the operation and issuer in the execution transaction. | A missing or mismatched row blocks signing, import and lifecycle execution. |
| `pki_audit` (PKI PostgreSQL) | Append-only operation state transitions committed with controller changes. | A failed audit write rolls back the state transition. |
| Structured controller event (central Loki pipeline) | Secret-free event with environment, operator ID, operation ID and request digest for operational search and correlation with controller state. Loki retention and indexing follow the logging policy. | Delayed or missing log delivery does not grant authority and does not undo a committed authorization. Reconcile it from the database record and controller state. |

Loki is a searchable operational log, not an immutable authorization ledger or
the controller's transaction boundary. The database keeps only the minimal
binding required to decide whether the action may execute; it does not replace
the wider operational log stream. Keep private keys, tokens and signer secrets
out of both records.

The dev controller now uses its configured operator and signer reference. Other
environments retain their own pre-cutover behavior until the same configuration,
migrations and evidence are completed there. This document does **not** authorize
direct database updates, fake approvals or self-granted workaround accounts.
Existing operation IDs and audit history remain intact during migration.

## Acceptance matrix

| Case | Test layer | Expected result |
| --- | --- | --- |
| Empty environment bootstrap | Disposable local provider and registry integration | The configured operator establishes the environment Root and initial Service intermediate and records their pins, exact policy and audit without a second human account. Runtime restart does not regenerate either CA. |
| First Platform Admin bootstrap | Account Manager deployment integration | The same environment operator supplies the one-time bootstrap identity and records its creation, first-login rotation and secret removal. A second administrator is optional for availability, not a required co-approver. |
| Successor Service intermediate | PKI controller PostgreSQL integration | The same configured operator requests, checks the digest, signs/imports and activates a successor with a reviewed additive subject/DNS policy; no `pki_admin` countersignature is required. Prior policy and trust remain available through the acknowledged overlap. |
| Root/Brand lifecycle | PKI controller and offline ceremony integration | The configured operator alone may execute a reviewed rotation, revocation or compromise response in its environment. Key possession, immutable lineage, signed CRLs, trust withdrawal and consumer acknowledgments remain enforced. |
| Governed legacy import | PKI controller PostgreSQL integration | The configured operator may authorize and execute the exact reviewed import manifest without a second human account; source certificates, CRLs, request IDs, time window and fingerprint scope remain validated. |
| Existing requested operation | Migration integration | A pre-migration `requested` operation retains its ID, creator, digest and history. The configured operator can explicitly continue it after revalidation. Migration does not silently mark it approved or execute it. |
| Exact-policy denial | Controller and offline signer tests | Changed subject/DNS list, parent, environment, CSR, certificate or request digest is denied before key use or provider mutation, even when the operator is authorized. |
| Operator and environment denial | Account Manager/controller assertion tests | Missing, expired, disabled or wrong-environment operator identity is denied. A Platform Admin login or copied PKI browser session without selected-environment authority cannot select another environment or arbitrary CA. |
| Provider ambiguity | Controller/provider integration | Unknown provision/import response remains reconcilable under the original operation and request digest; retries do not create another key, issuer or certificate. |
| Trust activation | Registry/consumer integration | Registry and OpenBao policy agree, signed current CRLs are installed, and every required consumer acknowledges the exact bundle before activation. Missing or stale acknowledgment blocks activation. |
| Stable deployment identity | Deployment-script local test | A second deployment reads the saved identity from the same environment SecretStore and makes zero signing calls. Missing initial identity is issued once and saved atomically; partial, mismatched or runtime-renewed state is not overwritten. |
| Public evidence and secrecy | Script/console test | Audit and operator output include operation ID, policy digest, public fingerprints, expiry and outcome; no private key, passphrase, bearer token or SecretStore value appears in logs or artifacts. |
| Cross-environment isolation | Deployment-script and controller test | A dev operator configuration cannot sign, import, activate or install a staging/prod identity, even when subject names match. |

Focused code-test locations include `repos/rtk_video_cloud/internal/pki/store_integration_test.go`,
`service_client_policy_test.go`, `root_reconcile_test.go`, controller HTTP tests,
and `scripts/go/rtk-cloud` deployment identity tests. Existing tests that expect
distinct `pki_admin`/`security_custodian` principals cover the retained
pre-cutover path. Operator-mode tests cover the new path; do not weaken digest,
scope, certificate, CRL or consumer checks merely to make same-operator
authorization pass.

## Dev qualification evidence (2026-09-29)

The preserved dev OTA Service operation
`b4d42f12-6f21-45c6-b8d2-3df4930a88bd` retained request digest
`d010a3b9a5193a0e001c6ed95b3f70a42e1d1b82c544fc639a50a6b1100edd99`.
The configured operator authorized it through Account Manager. PostgreSQL has
one `pki_operator_authorizations` row and the ordered `pki_audit` transitions
`operation_authorized`, `provisioning_started`, `csr_recorded`,
`certificate_imported` and `issuer_activated`. Loki ingested the secret-free
`pki_operator_authorized` controller event for the same operation and digest.

The first provision attempt entered `provisioning` before OpenBao rejected its
missing exact-mount ACL. The operator confirmed the mount and key inventory were
absent, installed only the reviewed issuer-specific controller/server/client ACLs,
created one internal OpenBao P-256 key at the original mount and reconciled the
original operation. The reconciled CSR had the same public key. The protected dev
Service Root signed that CSR after request, CSR and parent fingerprints matched.
The signed certificate fingerprint is
`7b1d6cf35e8c0477385bec3f61348b8ed4077d7da0e918ff9f2e28cdda0921f5`.
This recovery preserved the operation ID and audit history; it did not reset an
operation or create another issuer.

Both required Service trust consumers, `certissuer` and `pki-controller`, loaded
the additive immutable bundle manifest and acknowledged exact version
`5802c2123ea7c8b53b10875b0fcaec7f11686772114b5e863fc703d7bd6f3f0d`.
The successor `cf348f82-f4cc-434e-a59d-c37eee8222cf` is active; the predecessor
`c160c01f-a742-4c06-bcb7-d90be90e820b` is retiring with 12 existing Service
client issuances. The canonical dev SecretStore/live-cluster check passed after
allowing a newly active issuer to have zero leaves while still rejecting pending,
invalid, unpublished-revocation and missing-acknowledgment records for the
pinned issuer. A separate read-only retirement inventory of the predecessor's 12 issuances
passed on 2026-09-29 with zero pending, invalid, unpublished-revocation or
missing-acknowledgment records. Repeat it before removing predecessor trust.

## Remaining environment qualification

In dev, verify the seven intended workload identities, private registration
listener, six service registrations and Product grant before enabling OTA Product
writes or billing. Staging and production use their own environment configuration
and repeat the read-only preflight plus controlled operation; a dev receipt cannot
qualify another environment. Keep Product writes and billing gates off until that
environment's end-to-end evidence passes.
