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

### 2026-09-08 live dev database bootstrap

Published the local dev package from clean Video Cloud `0dca4d9`. The workspace
builder needed a two-line fix to include `/app/pkicontroller`; its generated
Dockerfile had omitted that binary despite the service Dockerfile including it.
Builder regression tests, vet, image architecture inspection and the controller
executable smoke check passed. No Git push, PR or remote CI run was used.

Verified registry reference and the actual image IDs used by both completed Jobs:
`ghcr.io/hkt999rtk/rtk_cloud_dev/video-cloud-api@sha256:be8d897147d3aa297a2f58a1c3f284d44c403d4ece7b1d60d099e94643f83d35`.
The build tag is `dev-pki-0dca4d9-20260908-1303`. Persisted the digest reference in
the dev operator file `PKI_CONTROLLER_IMAGE`; the separate overlay renderer read
that file to produce the executed manifests. Existing `LKE_VIDEO_CLOUD_IMAGE` and
application Deployment images were not changed. This image is dev-only.

Before schema mutation, captured a fresh read-only `video_cloud` dump and verified
its archive table of contents. It is 1,454,205 bytes, SHA-256
`82fd4cff778bc8e0047f312bb8dd7de72240adb1cbdb99ca5a38fce98fc4719c`, retained privately
as `/private/tmp/rtk-dev-pki-bootstrap-rehearsal/video_cloud-before-live-migrate.dump`.
The earlier restored-snapshot rehearsal covered the same migration/grant commands.
This phase changes database schema/permissions only; no provider configuration,
CA key, certificate, trust publication or governed operation was changed.

| Dev Job | UID | Result |
| --- | --- | --- |
| `pki-migrate` | `155cc400-18a9-42cf-8a1f-5ef1d4219928` | Completed, container exit 0 |
| `pki-grants` | `57fb0639-29b2-4058-abc9-6ddd783ee681` | Completed, container exit 0 |

The live database now has 20 `pki_*` tables and three unprivileged NOLOGIN groups.
Issuer, operation, approval, legacy-import and replacement counts are all zero;
schema creation is not a governed migration or device replacement.

Created `pki-migration-database` (resource version `760892`) from existing dev
PostgreSQL owner credentials for the two Jobs only. Created the dedicated login
`rtk_pki_controller_dev` using its previously prepared password, with inheritance
from only `rtk_pki_controller` and no elevated role flags or object ownership.
Created and verified `pki-controller-database` (resource version `761093`). The
runtime login connected over the actual dev PostgreSQL Service, read all 1,331
source rows and the empty issuer registry. Actual source/audit deletion and schema
creation probes failed with SQLSTATE 42501; an incorrect password was rejected.
No probe deleted rows or left objects behind.

Manifests, Job logs and `database-checkpoint.json` are retained under the dev
SecretStore's `pki/controller-bootstrap/rollout` directory. The controller remains
undeployed, and Account Manager still uses its existing login signer. Read-only
OpenBao inspection found AppRole/token authentication only; its existing service
account can perform Kubernetes TokenReview. Configure a dedicated Kubernetes auth
binding next, preserving legacy AppRole settings and issuer-scoped policy controls.
The coordinated Account Manager/controller rollout, real approvers, target hierarchy
and owned canary device remain pending. Staging was not accessed.

### 2026-09-08 prepared dev controller credentials and transport dependencies

The dev-only `rtk-cloud pki-dev-prepare --environment dev` command now creates
and validates persistent material in the canonical SecretStore at
`dev/pki/controller-bootstrap`. It generated the dedicated controller database
password, separate Account Manager access/refresh RSA pairs and independent
management server/client TLS identities. A repeat validated the same material.
No Device/Brand/Product CA or human assertion/approval was created. See the
[dev bootstrap runbook](production-pki-dev-bootstrap.md) for file custody,
scope, expiration and the remaining binding steps.

Created the previously absent objects only on dev context `lke649805-ctx` and
read them back to verify exact data against their selected sources:

| Namespace suffix | Object | Resource version at verification |
| --- | --- | --- |
| `-video-cloud` | Secret `pki-controller-tls` | `760635` |
| `-video-cloud` | ConfigMap `pki-account-manager-public-key` | `760638` |
| `-account-manager` | Secret `account-manager-pki-auth` | `760640` |
| `-video-cloud` | ConfigMap `pki-openbao-transport-ca` | `760724` |

OpenBao trust was copied only from the public `ca.crt` field of the existing dev
`openbao-tls` Secret. OpenSSL 3 verified the root self-signature, server chain,
serverAuth purpose and `openbao.video-cloud-dev-secrets.svc` hostname. The CA DER
SHA-256 is `44e0b04c974362d1eac26b4ee3334da9b216b38f7f29899a329c469b633ffe26`.
It is distinct from the new management trust. This is certificate evidence;
runtime controller-to-OpenBao connection and Kubernetes authentication are pending.

The Account Manager Deployment retained resource version `655962`, its existing
image and sole `account-manager-certissuer-client` secret mount, with one ready
replica. Prepared keys are not yet used for live login signing. No workload image,
database schema, role assignment, operator feature setting or existing key was
changed. Staging was not accessed.

Local tests cover real management mTLS admission and rejection of missing client
identity, staging hostname and server-only credentials used as clients; retries
preserve keys. Invalid environment, partial/mismatched/expired material, unsafe
permissions, shared JWT signers, symlinks and concurrent preparation locks fail.
Focused race tests (including CLI environment handling and SecretStore coverage),
vet and diff checks passed. These checks do not establish a running controller,
runtime database access, OpenBao authorization or device migration acceptance.

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

The service renderer has an explicit dev target: dev namespace, dev package
digest, dev OpenBao endpoint/auth role and existing `ghcr-pull` reference. Initially
all controller-specific Secrets/ConfigMaps were absent. The current checkpoint
above records the installed TLS/public-key/provider-CA objects. Migration/runtime
database connections and provider authorization remain to be provisioned before
application of the rendered workloads.

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
