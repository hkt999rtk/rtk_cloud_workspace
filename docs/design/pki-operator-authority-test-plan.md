# Environment PKI operator authority: design and test plan

Status: accepted target (2026-09-29); controller, proxy, console and deployment
verification changes are implemented in this worktree. Live cutover and evidence
remain pending.
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
| `docs/product-services-dev-pki.md` and `docs/design/ota-pricing-activation-and-disclosure-plan.md` | OTA successor Service issuer | Pending dev operation preserved; target and live blocker distinguished. |
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

The append-only `pki_operator_authorizations` row is the minimal, transactionally
checked authorization evidence. `pki_audit` keeps controller state transitions;
the controller emits a secret-free structured event for Loki search. A missing
or delayed Loki event does not authorize a database operation. A missing database
authorization always blocks execution.

The deployed controller retains independent-role approvals until each
environment is cut over with matching operator and signer configuration. This
document does **not** authorize direct database updates, fake approvals,
self-granted workaround accounts or activation of a pending operation. Existing
operation IDs and audit history remain intact during migration.

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

## Live qualification after implementation

In dev, first reconcile the existing pending OTA Service intermediate operation
against its persisted digest and the current environment Root; then execute it
with the configured operator identity. Verify the seven intended workload
identities, service registration and Product grant before enabling OTA. Staging
and production use their own environment configuration and repeat the same
read-only preflight plus controlled operation; a dev receipt cannot qualify
another environment. Keep Product writes and billing gates off until that
environment's end-to-end evidence passes.
