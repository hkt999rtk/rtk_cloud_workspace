# Fresh dev PKI rehearsal

## Scope

The user authorizes temporary accounts/devices and dev database resets when
useful. Existing dev issuance data is not a migration requirement. Staging is
untouched. MFA remains disabled and is optional future human-login functionality
only; devices never use it. Legacy fleet migration is deferred.

## Current live checkpoint: Product revocation and HTTP sessions

The disposable Product v2 issuer is now **revoked**. Root and Brand remain active;
the activation and enrollment records below are historical checkpoints. A new
Product issuer and fresh device are needed for the next MQTT acceptance run.

Governed operation `75ddce79-8f0b-41b5-a55b-cb043473ab43` used distinct simulated
PKI-admin and custodian approvals. Before execution, the device authenticated
with its current successor key and opened a verified-mTLS WebSocket (HTTP 101).
It remained connected through a full 12-second observation interval. Revocation
execution immediately changed registry authorization; the existing socket closed
8.003 seconds after the revoke request began. New token issuance returned 401
and certissuer renewal returned 403. These denials were observed before publishing
the new Brand CRL, so they establish online registry/session enforcement.

Finalization before CRL publication returned 409. The encrypted dev Brand key
then signed full CRL number 2 containing the revoked Product CA serial, reason 5
(cessation of operation). The API process fetched, installed, persisted and
acknowledged digest
`fac732d26b8373d950a284da07a72d07301d3ae9d1a447dec478e511bf76bb33`
at `2026-09-08T06:57:54.712328Z`. Only after this actual consumer acknowledgment
did governed finalization succeed. New device connections were then rejected at
the TLS handshake as well. No operator-generated acknowledgment or direct SQL
revocation write was used.

Full-chain CRL enforcement is enabled on the separate dev API with
`VIDEO_CLOUD_AUTH_CRL_MANIFEST=/run/pki-device/crls.json` and
`VIDEO_CLOUD_AUTH_PRODUCT_PKI_REQUIRE_CRLS=true`. The manifest pins the public
Root, Brand and Product authority records; their CRL caches live on the existing
PVC under `/run/pki-state`. Initial Root/Brand CRLs were signed by their encrypted
local simulation keys. The Product CRL was retrieved from OpenBao's public
`cert/crl` endpoint and was signed inside OpenBao; no Product key was exported.

The rollout exposed a bootstrap/runtime configuration conflict: leaving the
completed Root bundle acknowledgment enabled makes its CA-only chain enter the
client CRL verifier, which correctly requires a leaf/issuer chain. After verifying
all three issuers were active and their CRLs were acknowledged, the API's
`VIDEO_CLOUD_AUTH_ISSUER_TRUST_MANIFEST` setting was removed. The completed bundle
receipts remain in the registry; dynamic root-policy and mandatory CRL verification
remain enabled. Do not re-enable the historical all-issuer bootstrap manifest in
this steady-state configuration. Support for simultaneous Root bootstrap
acknowledgment and mandatory client CRLs has not been qualified.

A controlled API restart then replaced the pod with
`597e2d97-956d-4504-a6b1-2ecd9eb84fda`. Its image ID was unchanged, and the Root
policy plus all three persisted CRL files retained identical SHA-256 hashes.
The new Ready process still rejected the revoked device at TLS handshake.
`api-crl-restart-before.json` and `api-crl-restart-after.json` retain this check;
`product-v2-revocation-evidence.json` has SHA-256
`05d1f3314a1ce825f855c583605b8efd63d84d1255aa5ac20f084a14135669b3`.

Scoped deployment and ConfigMap manifests are persisted in the canonical dev
store. The revoked Product was removed from retained initial bundle references;
its pinned CRL authority and public revocation evidence remain available. The
API image stays at `548bde6883afbe0708bfe1095389b08288d86a0f6e3cf4d5bf2b012a7e133e16`.
The original API, factory enrollment, certissuer, MQTT and staging workloads were
not redeployed for this CRL/session test. No database reset was needed.

Evidence in the protected `pki/fresh-rehearsal` directory includes
`product-v2-revocation-evidence.json`, the requested/approved/pending/completed
operation snapshots, `brand-crl-2-imported.json` and the public ceremony manifests.
CRLs in this rehearsal expire on 2026-09-11; freshness remains enforced. This
checkpoint qualifies the recorded Product-CA revocation and HTTP/WebSocket case,
not individual Device-leaf revocation, MQTT, every provider failure, or production
custody/recovery.

## Next execution slice: MQTT transport and dev acceptance

Read-only discovery confirms the existing dev `mqtt` StatefulSet runs EMQX 5.8.7.
The implemented session worker targets 5.9+; the separate API also needs an
internal callback transport. Its device listener enforces Device mTLS/CRLs and
cannot accept the broker's independent service identity.

1. Add an optional internal API HTTPS listener exposing only
   `POST /v1/internal/mqtt/authenticate`. Reuse the API server TLS identity, but
   require a separately configured broker client CA and exact broker client name.
   Keep the existing broker bearer-key check, token provenance, current registry
   checks, CRL policy, short leases and ACL calculation. The listener must never
   expose device token issuance, management APIs, metrics or other API routes.
   Starting or stopping either listener must stop its peer and close resources.
2. Package the existing `pkibroker` worker in the dev image and prepare a distinct
   broker callback client identity using dev transport provisioning. Create an
   isolated dev broker with a pinned compatible EMQX image, server TLS, disabled
   authentication/authorization caches, no permissive authenticator fallback,
   and NetworkPolicies for only the required connections. Existing dev `mqtt`
   and staging stay untouched. Broker management credentials belong only to its
   colocated session worker; localhost management HTTP remains inside that pod.
3. Provision a fresh Product issuer/device using the existing governed APIs and
   actual consumer acknowledgments. Connect over verified MQTT TLS using a
   certificate-bound token, verify publish/subscribe ACLs and the 60-second lease,
   then test replacement/revocation, old-token reconnect denial and removal of
   the exact affected live session while a valid successor remains connected.
4. Persist scoped manifests and exact image/evidence records. Report broker
   compatibility, cache reset, actual session actions and failures separately;
   neither Pod readiness nor a simulated broker response closes MQTT acceptance.

This slice tests the established fresh-dev scope. Independent hardware custody,
cluster availability, legacy fleet migration and staging remain later work. The
broker's own server certificate remains distinct from Device/Product authority.

## Earlier checkpoint: fresh hierarchy active

Initial trust installation used Video Cloud `ad08eef`, workspace `0e12b30`.
It deployed the separate dev API listener
`video-cloud-api-pki` with verified image and running image ID
`ghcr.io/hkt999rtk/rtk_cloud_dev/video-cloud-api@sha256:c79a651d2807db0bad26b5872784fc435f2833f9143ce042ade710b5f8d2fea7`.
The existing API Deployment and plaintext Service were preserved.

The API verified the reviewed Root bundle against its loaded TLS client pool
and acknowledged bundle
`b72ea85670db35f6a8edada250a1dc66fd4cf4581863d9e21be23133fc32e07b`
as consumer `video-cloud-api`. This acknowledgment came from the API process,
not the operator script. The requester then activated Root
`c92fdbb1-f87b-4cab-80a6-fa77c2dce1d8` through Account Manager. The Root is active.
The direct TLS listener rejected missing client identity, a management identity
used as a Device identity, and an incorrect server name. These are negative TLS
checks. A real Product device connection and renewal now pass; see the fresh
device checkpoint below.

Continuous root-policy synchronization is now enabled. Its state is stored on
PVC `video-cloud-api-pki-trust`, class `linode-block-storage-retain` (requested
5Gi, CSI provisioned 10Gi). The init container copies the authenticated initial
state only if no state exists, then the API fetches and installs the current
policy before listening. Policy acknowledgment digest:
`96381e5ae666aa055fa02db383fdadeb5b581a13e234f26406050da6c7b5b455`.

A controlled Recreate restart changed the API pod UID from
`33ef644d-965e-4a0b-8c7b-065aeb2bee50` to
`7763f25e-6316-4d2e-905e-46ce23c588ee`. The exact image ID and persisted state
SHA-256 `e849ecce2ef73602e133ec69ee3fc181bb4bd1af9c9d355bcb81475f64b152d1`
were unchanged; the replacement became Ready with current policy acknowledgment.
This verifies initial trust persistence/reload, not device renewal recovery.

Fresh Cloud `5382d0cf-0966-45e9-ad5e-9955f4f6360d` and Product
`773aa199-16e3-4d59-94c6-5cdb5801c02f` are active in Account Manager. They were
created through the authenticated administration API with the temporary requester
as designated owner. Brand issuer `b4da0b7b-42ff-4966-95cc-50eb77c706f0` is active after distinct
approvals, an encrypted local key ceremony, Root signing, import and actual API
bundle acknowledgment. Activation before that acknowledgment returned 409.
Cloud and Product creation alone does not generate private CA keys.

Product v1 issuer `2a3f529b-a663-418b-bc62-da4092fe6cb1`, operation
`b09930b0-a32f-4151-83e5-b74cda10d352`, received distinct approvals but provisioning
failed before key generation. OpenBao rejected its nested mount with HTTP 400:
`path is already in use at pki/device/`. Provider inventory confirms the target
mount is absent. Existing legacy mounts remain unchanged. Its provisioning record
is retained as evidence; do not retry key generation or rewrite that reference.

The corrected design reserves new issuers at
`pki-issuers/<domain>/<issuer_id>/v<version>`. Existing governed references retain
their exact old layout. Video Cloud `1067380` is now deployed to the controller and separate API with
verified running image digest
`sha256:548bde6883afbe0708bfe1095389b08288d86a0f6e3cf4d5bf2b012a7e133e16`.
The unused v1 policy was removed and the controller role now has only the exact
v2 policy. This is a fresh dev provisioning repair, not migration work.

Product v2 issuer `14865afa-c346-4976-b9cb-915d7b21a958` is active. OpenBao generated
one internal P-256 key at
`pki-issuers/device/14865afa-c346-4976-b9cb-915d7b21a958/v2`, returning only the CSR.
The encrypted Brand key signed it in the local ceremony simulation. Import and
role configuration passed. Activation before API acknowledgment returned 409;
the API then verified the exact Product bundle against its actual TLS trust,
acknowledged it using its management identity, and governed activation passed.

Provider metadata exposes no private key. Effective controller-token capabilities
deny device signing, key reads, exported generation and legacy device signing.
This establishes the internal-generation path and runtime permission separation;
it does not qualify hardware custody or independent disaster recovery.

Local verification: complete affected pki, pkitrust, apiapp and postgres suites
passed against disposable PostgreSQL 16; OpenBao/PKI race tests and vet passed.
New/old governed layouts, exact policies, domain separation and malformed/broad
mount rejection are covered. The disposable test database was removed. Dev data
was retained; no reset or staging mutation was needed.

## Earlier bootstrap verification on 2026-09-08

- Dev context `lke649805-ctx`; namespaces `video-cloud-dev-video-cloud` and
  `video-cloud-dev-account-manager`.
- Controller image and running image ID:
  `ghcr.io/hkt999rtk/rtk_cloud_dev/video-cloud-api@sha256:be8d897147d3aa297a2f58a1c3f284d44c403d4ece7b1d60d099e94643f83d35`
  (service source `0dca4d9`). Deployment is Ready.
- Account Manager image:
  `ghcr.io/hkt999rtk/rtk_cloud_dev/account-manager@sha256:4ee5be3c53a666c8fb9df4149000cb6ab8c6311d568b51cfdcf606339a943288`
  (clean service source `63c928f`). Registry digest verified; schema Job
  `account-manager-pki-migrate` completed and the Deployment is Ready.
- Account Manager uses the separate access/refresh RSA keys and management mTLS
  identity prepared earlier. Existing HS256 access/refresh tokens require fresh
  login. Existing workers were not upgraded. No database reset was needed.
- Controller mTLS accepts Account Manager and a separate `video-cloud-api`
  management identity. Missing identity, wrong server name and a server-only
  client identity are rejected. The API identity cannot invoke human endpoints;
  Account Manager without its request-bound human assertion is rejected.
- Live Account Manager-to-controller testing exposed missing ingress under the
  existing default-deny policy. Video Cloud `14ff366` adds the scoped policy to
  the renderer. Dev policy is installed; five renderer tests and the authenticated
  request passed. No staging policy was changed.
- Three ordinary verified fixture users were seeded in the dev database without
  PKI roles or email delivery. All password logins ran through Account Manager,
  with RS256 tokens and no MFA claim. Each was denied PKI access before its role
  assignment. The existing authenticated dev administrator assigned requester
  and approver `pki_admin` and a distinct `security_custodian` through the ACL API.
  These are simulated test actors, not independent human custody evidence.
- Device Root operation `f08aac85-a944-4e7f-a52a-49676ddc78a3`, issuer
  `c92fdbb1-f87b-4cab-80a6-fa77c2dce1d8`, reached `ready` after distinct approvals,
  CSR registration and certificate import. Self-approval and a PKI administrator
  claiming the custodian role returned 403. Activation without the runtime trust
  acknowledgment returned 409.
- The existing ceremony CLI generated an encrypted P-256 Root key locally and
  produced the public CSR/certificate. Only public artifacts went to the
  controller. This is a dev simulation on the workstation, not a physically
  offline ceremony or qualified escrow.

## Remaining acceptance work, in order

1. **Done:** API initial trust installation, actual bundle acknowledgment, governed
   Root activation, continuous policy synchronization and persisted-state restart.
2. **Done for this dev simulation:** Brand and Product issuer provisioning,
   separate key custody, exact OpenBao policies, actual API bundle acknowledgment
   and governed activation. Hardware/offline-custody qualification stays deferred.
3. **Done for the recorded case:** fresh device-key enrollment, direct-mTLS
   authentication, new-key renewal and interruption before acknowledgment across
   certissuer/API restarts. Exact replay and old-key cutoff are verified below.
   This does not qualify every possible provider failure or lost signing outcome.
4. **Partially done:** Product CA revocation, direct-mTLS denial and existing
   WebSocket termination passed. Next add the compatible MQTT broker and its
   consumers to the required trust installation set, using a fresh Product/device.
5. Retain pass/fail results and a reproducible setup for the full dev lifecycle.

Initial HTTP acceptance currently requires consumer `video-cloud-api`. MQTT is
not included in that phase and is not yet qualified. Do not count missing
acknowledgments as user-input blockers or send fabricated acknowledgments.

The resolved bootstrap issue was: the API's automatic root-policy synchronizer
expects an already active/retiring Root, while the first Root needs actual
installation acknowledgment before activation. The new initial
installation path now resolves that cycle and automatic synchronization is running.
Activation checks remain enforced; disk-only installation is never a runtime ACK.

The initial installation path uses an explicit reviewed manifest of issuer IDs
and exact trust-bundle versions. API startup re-reads those issuers from its
registry, checks environment/domain/status/version and verifies their full chains
against its actual loaded TLS client trust pool before sending acknowledgments
with its own management identity. It never adds trust from the manifest. Any
validation or delivery failure fails startup; a restart safely repeats the exact
acknowledgment. After initial Root activation, configure the existing dynamic
root-policy synchronizer for continuing trust removal. This initial bootstrap
does not itself claim continuing revocation or MQTT acceptance.

## Retained configuration and artifacts

The canonical dev SecretStore contains:

- `pki/controller-bootstrap/rollout/pki-controller.json`: controller overlay,
  including scoped NetworkPolicy; `controller-auth-probe.json`: public TLS results.
- `pki/controller-bootstrap/rollout/account-manager-pki-migrate.json` and
  `account-manager-pki.json`: exact scoped schema/runtime manifests.
  `account-manager-before.json` retains the prior Deployment for review/rollback.
- `pki/controller-bootstrap/rollout/video-cloud-api-pki-deployment.json`, the
  matching Service/ConfigMap/PVC manifests and initial-state manifest retain the
  scoped API overlay. `PKI_API_IMAGE` pins its verified dev image;
  `PKI_CONSUMER_PODS=video-cloud-api-pki` maps its pod name to the existing
  `video-cloud-api` management identity. The restart-check annotation is transient
  operational evidence and does not change the persisted workload configuration.
- `pki/fresh-rehearsal/brand-active.json`, `product-v2-active.json`,
  `product-v2-custody-probe.json`, `product-v2-key-inventory.json`, and
  `product-v2-policies.json` retain hierarchy and provider evidence. The failed
  first Product attempt remains in `product-v2-prior-attempt.json` and its original
  operation/approval files. No provider key existed for that attempt.
- `pki/fresh-rehearsal/device-root-active.json`, `cloud.json`, `product.json`,
  `api-restart.json` and `api-tls-negative-probe.json` retain this checkpoint.
- `operator/env/PKI_CONTROLLER_IMAGE`, `PKI_REQUIRED_CONSUMERS`,
  `PKI_ACCOUNT_MANAGER_IMAGE` and the selected JWT/PKI file settings record the
  overlay configuration. These separate PKI overlays are not automatically
  applied by the default full-platform provisioner. Apply the retained overlay
  deliberately with current resource-version checks; do not assume a full
  provision preserves it or point all workers at the new Account Manager image.
- `pki/fresh-rehearsal/`: protected fixture account credentials, public operation
  and issuer snapshots, and encrypted local ceremony outputs.
- `pki/rehearsal-passphrases/`: protected dev simulation passphrase files. They are
  on the same workstation and do not constitute independent custody.

Credential files are not tracked or included in reports. Root/Brand keys never
belong in service database rows, container images or Kubernetes workloads.
No git push, PR, CI run or staging mutation was performed.

## Factory issuance and device renewal deployment policy

Reuse the existing independent certissuer service TLS identity and factory client
identity. Upgrade only dev certissuer/factory enrollment, set their environment to
`dev`, and enable Product PKI together. Use a projected Kubernetes token bound to
the certissuer service account, with only the active Product mount's signer policy.
Keep existing dev database credentials for this feature rehearsal; database role
qualification remains separate work. Do not migrate legacy enrollment records.

Certissuer startup validation must accept the Kubernetes authentication already
supported by its provider adapter and require the role/projected-token path.
For device renewal, load Device Root trust separately from service client trust.
Only the two device renewal routes may admit identities under the Device Root;
all other routes must retain service-chain verification. A matching factory or
service common name on a Device certificate cannot grant service authority.
The renewal handlers still require current registry binding, a new CSR key,
issuer lifecycle checks and successor-key acknowledgment.

## 2026-09-08 first fresh device and renewal

Factory enrollment now runs Product PKI in `dev` using Video Cloud `1067380`,
image digest `548bde6883afbe0708bfe1095389b08288d86a0f6e3cf4d5bf2b012a7e133e16`.
The previous deployment's environment was incorrectly `staging`; it is corrected.
Certissuer uses its existing independent service TLS identity and a projected
Kubernetes token for SA `certissuer-pki`, role `certissuer-pki-dev`. Its sole
policy signs through the active Product v2 mount; no controller rights or
legacy/App signer permissions were added. Existing dev database credentials
were preserved. Runtime database role qualification remains pending.

Actual startup exposed an AppRole-only configuration validator even though the
provider adapter supports Kubernetes auth. `86c0417` fixes that validation and
adds separate `CERT_ISSUER_DEVICE_CLIENT_CA` trust for renewal. Real TLS tests
cover a Device certificate with the same CN as a service caller: service routes
reject it. Product renewal still requires current registry binding. Configuration
and certissuer application race tests and vet passed.

Device `pki-dev-d9c7d8aeada0411bb5676100b6662ca6` generated its P-256 key locally.
An authenticated administrator created production run
`e507f37b-39dd-4380-b416-73d0bcbc9b0b` with quantity 1. Actual factory enrollment
reserved `6e6c55c9-f33f-4c40-a167-5ee784d67388`, signed through Product v2, completed
Account Manager coordination, and projected entitlement/device state. The returned
certificate matches the device key and issuer. Replay returned the same certificate
and reservation; the database reports quantity 1 of 1 and exactly one reservation.

Direct API mTLS authentication passed. The device generated a different successor
key and renewed through certissuer. The successor authenticates; predecessor
acknowledgment is denied, successor acknowledgment succeeds and is idempotent,
and the predecessor can no longer authenticate or renew after acknowledgment.
A Device certificate is also denied on the factory service route.

The first renewal replay exposed only an `overlap_until` precision difference
(first response nanoseconds versus persisted PostgreSQL microseconds). The
certificate was identical. `74906d7` returns the stored timestamp; database-backed
replacement race tests cover nanosecond input. Certissuer now runs `74906d7` with verified running digest
`sha256:7c678e7f6d56c10c4346b0dc5ded2cfaf6ee85ec397b688853bdb7f28b679292`.
The second renewal returned an exactly identical response on replay, including
the deadline. With acknowledgment pending, both certissuer and the API were
restarted using resource-version-checked Deployment changes. Replacement pods
had new UIDs, retained the expected image IDs, became Ready, and reproduced the
same renewal response from persisted state. Both keys authenticated during the
overlap. Predecessor acknowledgment was denied; successor acknowledgment was
idempotent and immediately denied the predecessor while preserving the successor.

The database contains exactly three certificate bindings and two acknowledged
replacement records for this device. Artifacts `renew2-issued.json`,
`renew2-restart-before.json` and `renew2-restart-evidence.json` retain the precise
case. This verifies a restart before installation acknowledgment, not every
possible unknown provider-signing outcome. No database reset was used.

The probe uses Go's verified TLS client, matching the application stack, with
explicit server CA and hostname validation. The macOS system Python TLS runtime
cannot decode the existing service CA algorithm; Python 3.14's strict X.509
default additionally rejects its missing authority-key identifier. No TLS
verification bypass was used. Service-certificate compatibility with strict
OpenSSL clients remains a later compatibility item, not a Device CA key change.

Protected artifacts are in `pki/fresh-rehearsal/device-1/`: enrollment request and
result, production token, device/successor keys, authentication responses and
renewal evidence. The current device key/chain are `successor2-key.pem` and
`successor2-chain.pem`; predecessor files are retained for denial tests. Only public IDs/results are recorded here. The retained scoped
rollouts are `certissuer-product-pki.json`, `factoryenroll-product-pki.json`,
`certissuer-device-renewal-roots.json` and the certissuer ServiceAccount manifest.
`PKI_CERTISSUER_IMAGE` and `PKI_FACTORYENROLL_IMAGE` accompany these explicit
overlays; the full-platform provisioner does not automatically consume them.
