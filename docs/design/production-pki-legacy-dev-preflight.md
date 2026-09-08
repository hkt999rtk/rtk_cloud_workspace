# Legacy migration: dev source evidence

## Current authentication policy

Per the user's clarification, MFA is optional future functionality for human
user login only. It is not required now and is never a device authentication
requirement. The historical MFA prerequisites below describe the previous code,
not the current acceptance gate. `PKI_REQUIRE_USER_MFA` defaults to false;
authenticated users, correct roles and distinct approvals remain required.
The pending request for an MFA IdP/assurance class is withdrawn. Operator account
selection, RS256/mTLS setup and role provisioning remain separate work.

Observed 2026-09-08 on dev context `lke649805-ctx`, namespaces
`video-cloud-dev-video-cloud`, `video-cloud-dev-platform` and
`video-cloud-dev-secrets`. Staging was not accessed in this follow-up.

## Current source selection

The database contains 1,331 successful issuance records and 111 active
entitlements. For each entitlement, match device ID, certificate serial and
SHA-256 of the exact certificate PEM against the issuance journal. Exactly one
record matches for every active entitlement: **111 current source records**.
The entitlement hash is PEM-based; the issuance fingerprint is DER-based.

Of the remaining 1,220 issuance records, 1,100 map to active device IDs but do
not match their current entitlement credential, and 120 have no entitlement.
These remain residual history; exclusion from the current-source set is not
proof that an old credential is revoked or that any old connection has ended.
No entitlement or issuance record was changed.

## Public certificate and revocation evidence

The dev issuer uses `pki/device`. Public reads through verified OpenBao TLS
retrieved its intermediate, the root at `pki/root/cert/ca` and both full CRLs.
The Device CA endpoint supplied only the intermediate, requiring the separate
root retrieval. Certificate SHA-256 fingerprints:

- Root: `fccadf313ac5a0c638e42893ca9e6a34897dd8cf8fdbdfadf5409575426bb627`
- Device intermediate: `c1607759897eb6daf842c2f5db98ae0394d5792f090e76920a29f28422d955f9`

Both existing CRLs had valid signatures but expired on September 5. Refreshed
only `pki/root/crl/rotate` and `pki/device/crl/rotate` using existing dev authority.
Both returned success. No certificate revocation, CA-key rotation, initialization,
policy change, deployment or device replacement was performed.

| CRL | ThisUpdate UTC | NextUpdate UTC | Revoked entries | DER SHA-256 |
| --- | --- | --- | --- | --- |
| Root | 2026-09-08 04:12:38 | 2026-09-11 04:12:38 | 0 | `65372ff4496f180eed9532877bad4003efca438a8622369972ac63ed39151c0b` |
| Device | 2026-09-08 04:12:40 | 2026-09-11 04:12:40 | 0 | `868eea0bc65ed9aa2810b95a899a9e06244be749707ad6508d6d2c626c097dbf` |

At 2026-09-08 04:12:59 UTC, all 111 selected certificates passed OpenSSL full
chain verification with `-purpose sslclient -crl_check_all`, using the retrieved
root, intermediate and CRLs. Additional checks verified DER fingerprint, exact
device common name, clientAuth-only EKU, current validity and cloud/product
mapping. Root self-signature and intermediate signature were verified. This is
cryptographic source evidence, not governed import eligibility or device possession.

Public artifacts and source IDs are retained privately at
`/private/tmp/rtk-dev-legacy-evidence`, with restricted permissions; that temporary
directory is not durable escrow. No private key or token is included in this
committed report. Renew evidence before expiry and re-read mutable source state
before approval/import.

## Next critical path

### 2026-09-08 migration rehearsal against dev snapshots

Rechecked canonical dev context `lke649805-ctx`: OpenBao is Running/Ready and
the PKI controller remains absent. Took read-only custom-format PostgreSQL dumps
of `video_cloud` and `rtk_account_manager`, restored them into disposable local
PostgreSQL 16 databases, and ran only migration/grant commands against those
copies. No application server, device client or email worker used copied data.
Restricted local artifacts are in `/private/tmp/rtk-dev-pki-bootstrap-rehearsal`;
they contain sensitive database contents and are not committed or durable escrow.

- Video Cloud `0dca4d9`: `pkicontroller migrate-runtime` succeeded twice, then
  `grant-runtime-roles` succeeded. Twenty PKI tables were present. Content hashes
  over every original column proved all 4,131 existing rows in 43 tables unchanged.
  The three PKI groups have no login, superuser, role/database creation or RLS
  bypass privileges. Controller source reads succeeded in the privilege matrix;
  source deletion/schema creation, verifier issuer writes and issuer approval
  inserts were denied by that matrix. This is local privilege evidence, not a
  deployed workload-login check.
- The unmodified Account Manager migrator failed before PKI at
  `071_test_lab_sessions.sql`: dev recorded earlier Test Lab names 068/069/070.
  The deployed image already contains the renamed 071/072/073 files, whose SHA-256
  hashes match this checkout. No live history or table was modified to bypass it.
- Account Manager `63c928f` recognizes those exact historical filenames and the
  earlier 070/071/072 source lineage. Adoption pins current file digests, preserves
  original markers/timestamps and does not replay session revocations. Unknown
  filenames and changed migration contents fail instead of being inferred from
  an existing table or numeric prefix.
- With the fix, the copied Account Manager database reached migration 077.
  All 7,865 existing rows across 78 tables were preserved; only three system roles
  and seven canonical migration markers were added to the original tables.
  New bootstrap state was sealed with reason `existing_installation`; the PKI
  roles were assigned to nobody. A second run preserved the migrated contents.
  The full database race suite, migration regressions, vet and diff checks passed.

Live dev schema, credentials, images, roles and sessions are unchanged by this
rehearsal. Include the fixed migrator in the scoped rollout. Dedicated controller
database access, management TLS, OpenBao auth/policy, Account Manager RS256 setup,
the two real approvers and a device owner remain required. Stage is not accessed.

### Controller and authorization discovery

Local implementation `bc4b247`: four renderer tests and diff checks passed;
linux/amd64 image build and controller executable/usage smoke check passed.
Local image ID `sha256:1988dd988b3321d3f9d0e71e93ab20729982c82c198dda3bf4f1111edcd9582a`
has not been published or deployed. It is build evidence, not a registry digest
or live dev qualification.

The service renderer now has an explicit dev target: dev namespace, dev package
digest, dev OpenBao endpoint/auth role and existing `ghcr-pull` reference. The
controller-specific Secrets/ConfigMaps are absent in dev: migration/runtime DB
connections, controller TLS identity, Account Manager assertion public key and
provider transport CA. Their bootstrap must precede application of the rendered
workloads; generating manifests does not provision these dependencies.

Account Manager's Deployment has only its certificate-issuer client volume and
loads `account-manager-runtime`. A key-name-only inspection found HS256 access/
refresh secret settings and no signer selection, asymmetric key paths, PKI
controller connection or MFA assurance configuration. Current source defaults
`JWT_SIGNER_PROVIDER` to `hs256`; `SignPKIAssertion` requires RS256. The existing
integration previously required recent verified IdP MFA. Under the current policy,
MFA is optional; distinct PKI Administrator/Security Custodian approvals remain
required. Actual operator identities still need selection; the MFA IdP request is
withdrawn. No roles, MFA claims or approval assertions were fabricated.

Prepare controller bootstrap together with the Account Manager RS256/controller
transport configuration and compatibility checks for its token consumers. A
controller-only deployment cannot complete the governed canary. No dev rollout,
database migration, credential generation or Account Manager change has occurred
in this preparation step. Staging remains outside scope.

Read-only authorization follow-up found enabled Google OIDC and GitHub OAuth2
providers, two linked OIDC identities, and zero linked identity records containing
either `acr` or `auth_time`. These persisted records do not establish recent MFA;
the existence of Google login must not be treated as sufficient assurance. This
observation does not prove that every possible provider configuration lacks MFA.
An MFA assurance class is no longer a prerequisite. The actual two operators
and their required roles remain necessary for independent approval.

The live Account Manager database contains no `pki_admin`, `security_custodian`
or `pki_auditor` role rows, and no PKI/admin-recovery tables. Required PKI role and
sealed-bootstrap/recovery migrations therefore precede role assignment and canary
approval. No roles were assigned through direct SQL or bootstrap bypass.

Source review of Account Manager `internal/auth/auth.go` confirms strict signer
algorithm matching: after an RS256 switch, existing HS256 access and refresh
tokens are rejected. Plan fresh dev logins at the cutover; retaining HMAC secrets
does not provide dual-token acceptance. Do not switch signers as an unnoticed
controller-only change. Existing controller assertions and token consumers must
be tested together after the selected dev authorization configuration is ready.

Focused local Account Manager tests passed for configured verified MFA assurance,
refresh not minting MFA authority, HS256 denial for PKI assertions, RSA token-kind
validation and PEM signer loading/error cases. These are local implementation
checks, not proof of live IdP assurance or operator authentication.

1. Prepare the dev-only registry/controller rollout; public `pki_*` tables and
   the controller are absent. Preserve existing dev service configuration/data.
2. Establish the governed Device/Brand/Product target hierarchy with independent
   approvals and dev-only custody. Do not relabel dev as staging or reuse its keys.
3. Select a bounded canary with an available device owner and predecessor key;
   run actual governed inventory/import and consumer trust installation.
4. Measure replacement, acknowledgment, old-credential/session rejection and
   interrupted renewal before expansion and residual/trust-withdrawal acceptance.

All five live milestone checklist items remain open; source selection and
cryptographic validation have advanced item 1. Dev evidence does not establish
staging, physical hardware or custody/recovery qualification.
