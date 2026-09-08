# Production PKI domain and host inventory

## Current execution scope (2026-09-08)

Fresh dev Device PKI is complete. **Remaining trust consumers/live sessions** is
now active; see the [fixed work groups and management adoption design](production-pki-trust-consumers.md).
Staging and legacy migration remain deferred. The dated staging and milestone
counts below are historical, superseded by this section. Four broad milestones
remain. Controller management Service-client registry admission, role binding
and connection eviction are locally implemented in `9134882`; per-domain consumer
gates are implemented in `8baa0e3`. Serving-listener Service bundle receipts and
ready-CA bootstrap are implemented locally in `a857a1f`; managed callers and live
Service adoption remain open. Account Manager `41f1294` and Video Cloud `72a9ccf`
add locally tested Account Manager managed identity/egress through a private socket.
The active milestone is approximately 25%, with 1/6 work groups complete and 5 open.

## 2026-09-08 staging packaging checkpoint

Service commit: `08be3b6`. Validation passed: three renderer unit tests,
local linux/amd64 image build and controller executable/usage smoke check,
default-PKCS11 release bundle verification, workflow YAML parsing, shell syntax
and diff checks. No push, PR, remote CI or staging deployment occurred.

Local staging preparation now includes the controller in the shared image and
release bundle, with an offline renderer for separate migration, role grants and
controller deployment phases. Runtime credentials are separate from migration
credentials; deployment rendering requires a digest-pinned image and explicit
consumer IDs. See the [service rollout runbook](../../repos/rtk_video_cloud/docs/pki-staging-rollout.md).

Read-only inspection found OpenBao Pending with CSI volume mount failures; the
public root/CRL query could not start. No storage repair or live change occurred.
The [preflight](production-pki-legacy-staging-preflight.md) records prerequisites.
All five live legacy checklist items remain open; item 1 is partially progressed.
The five broader milestones remain unchanged, with legacy migration active.

Reviewed 2026-09-08 through Video Cloud `0387086`.

Active acceptance work is **milestone 1: legacy migration/device replacement**.
See the [fixed five-item rollout checklist](production-pki-legacy-rollout.md).
Local reporting preparation does not complete a live cohort acceptance item.

Read-only staging discovery is recorded in the
[2026-09-08 preflight](production-pki-legacy-staging-preflight.md): 226 successful
source issuances and 204 active candidate devices. The live PKI schema/controller
and complete legacy root/CRL evidence are prerequisites still missing; no candidate
is yet declared eligible or migrated. Item 1 has progressed, not completed.

Completed local MQTT host work is recorded in [Managed EMQX host identity](production-pki-emqx-host.md).
EMQX owns MQTT TLS termination; `pkibroker` is an outbound session-management
worker. The fixed four-item plan separates local supervisor implementation from
real broker, cluster and recovery qualification.
This inventory preserves the existing five acceptance milestones. It identifies
implementation work; it does not add a sixth milestone or certify production.
The authoritative trust boundaries remain Platform PKI contract sections 3–5.

## Current entry points

| Domain/use | Current source and behavior | Remaining implementation/acceptance |
| --- | --- | --- |
| Device client identity | Video Cloud `internal/pki` registry and Product claims; `internal/certissuer/product.go` and Product-mode bootstrap; SDK renewal/trust and owner-lifetime adapters recorded in the ledger. | Real legacy cohort replacement, application policy/owner adoption, physical firmware and supported-platform evidence. |
| App/user client identity | `internal/pki/app_issuance.go`, `app_verification.go`, `app_revocation.go`, `app_crl_worker.go`; API consumer and `internal/pkitrust/registry_app.go` compose broker/TURN acknowledgment after sweeps. Recovery includes `app_reconcile.go`, `recovery_app.go`, and `recovery_app_inventory.go`. | Dynamic App root-policy adoption, remaining application/SDK host wiring and real broker/relay eviction evidence. |
| Gateway/server issuance | `internal/certissuer/server_registry.go` and gateway handler/bootstrap select an independent registry domain. Exact approved DNS policy, durable claims, provider validation and CRL-aware replay apply. Authenticated renewal and both certificate-issuer and PKI-controller Service host key storage, scheduling, listener replacement and eviction are implemented. Empty issuance mode retains the legacy Device-backed signer. | Other server host adoption, dynamic root-policy migration, external recovery-history reconciliation and actual host cutover. CRL maintenance, public lineage recovery and restored-registry inventory are implemented locally. |
| Internal service client/server identity | The registry owns approved Service client identities, receipts, verification, reconciliation and revocation. Factory enrollment now owns durable key/CSR storage, issuance, scheduled renewal and its verified outbound connection lifecycle; certificate issuer listener admission, eviction and exact CRL consumption are integrated. Service CRL publication, restored receipt inventory and client-role-selected provider lineage checks are implemented locally. | Adoption by remaining Service hosts, dynamic root-policy/key renewal beyond the integrated host, and live eviction, matched restore and post-backup audit reconciliation evidence. |
| Dedicated MQTT server TLS | Independent `mqtt` issuer/receipt/CRL verification; API/log-ingester consumer admission and eviction. `0387086` adds protected EMQX host keys, durable renewal and native foreground supervision with stop-before-replacement and denial eviction. Disposable EMQX 5.9.0 MQTT sessions verified replacement/termination. | Dynamic root-policy refresh, real host/cluster rollout, client-authentication/ACL and durable-session recovery acceptance. Existing CRL maintenance and exact installed-digest consumer ACKs remain separate. Public-CA MQTT remains a distinct supported contract choice. |
| OpenBao transport TLS | Dedicated transport CA/files and TLS Raft artifacts; independent server issuance/CRLs/recovery; controller and certificate issuer support opt-in registry-backed provider HTTP with verified login/renewal, periodic connection eviction and optional exact installed-CRL ACKs. | Other provider-client adoption, root-policy/server-key renewal and real host rollout. Transport trust remains independent of Device/App/Service roots; seal/custody and HA qualification remain separate. |
| Public HTTPS | Contract requires publicly trusted CA/ACME. | Verify deployment/renewal acceptance separately; never route browser/public HTTPS issuance through private Device/App issuers. |

## Next implementation sequence: independent server-domain issuance

Steps 1–2 are locally implemented in Video Cloud `893ff9a`: approved canonical
DNS policy, digest revalidation, independent private server-domain provisioning
and server-only OpenBao roles. Existing reservations without a policy remain
unsupported. The durable registry claim/validation portion of step 3 is implemented in
`f304ec1`; provider/HTTP composition and opt-in bootstrap (step 4) are implemented
in `6f5838e`. Server uncertain-outcome recovery is implemented in `a5ad490` plus
`72df2ef` (persisted timestamp correction). Server revocation publication and exact
CRL acknowledgment protocol are implemented in `742a768`; step 5 still requires
TLS transport/connection-owner wiring, consumer/host adoption and recovery verification.
`093bc79` adds read-only registry verification and a TLS handshake/resumption hook;
`243386e` adds tracked connection sweeping/eviction, including active TLS streams.
`a17c4ff` wires API/log-ingester MQTT transports, periodic sweeps and shutdown.
`7869d5e` adds scoped server CRL refresh/publication work and acknowledgment health.
`57c68f6` binds MQTT consumer acknowledgments to installed records and connection sweeps.
Service/OpenBao transport wiring, root/key lifecycle adoption and real rollout remain.

1. Bind an explicit server leaf policy to the approved immutable issuer operation.
   It must specify the intended private trust domain and exact permitted DNS names;
   CSR fields and mutable gateway display names cannot select the CA. Policy changes
   must participate in the request digest and independent approval checks. Do not
   give non-client domains the existing allow-any-name client role as a shortcut.
2. Extend provider provisioning and ACL rendering for that approved server policy.
   Generate intermediate keys in OpenBao, import only the signed public chain, and
   configure server-auth-only roles with exact name restrictions, certificate
   storage and no wildcard/IP/URI alternatives unless separately approved.
   Preserve Device/App profiles and keep public HTTPS outside private issuance.
3. Add registry-owned gateway issuance claims, pinned to the active issuer and
   approved policy. Enforce original CSR/request/caller binding, one signing owner,
   uncertain-outcome recovery and validated provider responses. Replay must recheck
   current issuer status and identity validity. A process-local lock is insufficient.
4. Wire the gateway handler/bootstrap to the independent registry path, preserving
   an explicitly identified legacy compatibility mode. In the new mode there must
   be no fallback to the Device/App signer or another domain's role. Update config,
   runtime grants, deployment examples and the existing gateway API contract.
5. Add domain-specific revocation, recovery and consuming-host adoption. Keep server
   TLS validation separate from client-auth policy, and account for active sessions
   and existing server certificates during rotation. Extend Service client identity
   under its own approved profile; MQTT and OpenBao transport remain independent.

Required local evidence includes approved-policy digest binding, wrong-domain and
unapproved-SAN rejection, one signing attempt under concurrency/failure, exact
provider certificate/CSR/chain checks, replay after revocation, restricted-role
SQL/ACL tests and real local OpenBao issuance. Tests must also prove Device/App
issuance does not change. Runtime host evidence must identify which server listener
and which clients adopted each root, certificate and revocation policy.

## Fixed completion boundaries

Existing backup tooling captures complete PostgreSQL/OpenBao state. App recovery
checks now provide concrete local consistency tests, but independently retained
post-backup security history and actual restored-key usability remain required.
SDK primitives must still be connected to real application owners and policy
updates. Local tests cannot establish staging fleet adoption, physical secure
storage/HSM behavior, custody approval or measured RPO/RTO.

The five remaining milestones are unchanged: legacy migration/device replacement;
trust consumers/live sessions; backup/recovery and SDK integration; provider/hardware
compatibility; staging/custody/recovery qualification. The next coding work above
belongs to trust consumers/live sessions and recovery integration; no new goal or
milestone is introduced.


### Private server recovery verification checkpoint (2026-09-08)

Implemented `recovery-check-server DOMAIN ISSUER_ID EXPECTED_ROOT_SHA256
[DNS_NAME LEAF_PEM]` for independent Service, MQTT and OpenBao TLS domains.
The check validates restored public issuer indexes/CSR/certificate lineage,
independent root pin and approved DNS-policy digest, and reads the provider's
fixed server-role selection and public key binding. Optional leaf verification
uses the same read-only repeatable-read snapshot for the original receipt,
identity/profile and signed CRLs. Provider access is GET-only and rejects other
domains. Writers must remain fenced across database/provider verification.

Local PostgreSQL tests cover all three domains, pin/provider/policy/index/domain
mismatches, revoked receipts/leaves, expired certificates and stale CRLs. HTTP
provider tests cover role selection, key binding, malformed/oversized responses,
redirects and mount isolation; CLI tests cover explicit domains and unsafe files.
This is public recovery evidence, not private-key usability, complete issuance
inventory, external-history reconciliation, or live RPO/RTO qualification.

Five acceptance milestones remain: legacy migration/device replacement; trust
consumers/live sessions; backup/recovery and SDK integration; provider/hardware
compatibility; staging/custody/recovery qualification. Remaining server work
includes complete recovery inventory, Service/OpenBao transport integration,
root-policy/key renewal adoption, and live rollout qualification. No push, PR,
remote CI, live deployment or custody operation. Goal remains active.

Service commit: `d8a0e50`. Full Go suite with PostgreSQL, focused PKI/OpenBao/
controller race tests, `go vet`, formatting and diff checks passed. An initially
malformed policy-tampering fixture was corrected to use a valid-format incorrect
digest before final validation and commit. Disposable database removed.


### Private server restored-registry inventory checkpoint (2026-09-08)

Implemented `recovery-inventory-server DOMAIN ISSUER_ID EXPECTED_ROOT_SHA256
CONSUMER_IDS_CSV`. Explicit Service/MQTT/OpenBao TLS scope, independent root pin,
approved DNS-policy digest, full public lineage, signed root/intermediate CRLs and
exact current consumer ACKs are checked in one read-only repeatable-read snapshot.
Every receipt attached to the issuer is scanned in 128-row keyset pages; scope
mismatches remain visible. Original request/CSR/DNS/issuance metadata, pending
claims, revocation records, historical publication and current CRL coverage are
checked. Unexpired unrevoked leaves also pass current server admission. Reports
contain public counts; no provider calls, signing, acknowledgments or repair occur.

Tests cover all three domains, 129-row pending inventories and 385-row mixed-domain
inventories with repeated caller/request keys; malformed context/DNS/digest/scope,
missing ACKs, missing/unpublished/mismatched revocations and revoked intermediates.
The restricted verifier SQL role runs the inventory and detects unresolved
revocation. This proves restored-registry consistency only: provider completeness,
post-backup external history, private-key usability and live recovery qualification
remain separate evidence requirements.

Five acceptance milestones remain: legacy migration/device replacement; trust
consumers/live sessions; backup/recovery and SDK integration; provider/hardware
compatibility; staging/custody/recovery qualification. Next server work is remaining
Service/OpenBao transport integration and root-policy/key renewal adoption, with
external-history reconciliation and live qualification still required. Local only;
no push, PR, remote CI, deployment or custody operation. Goal remains active.

Service commit: `340aed2`. Full Go suite with PostgreSQL, PKI/controller/Postgres
race tests, restricted SQL-role checks, vet, formatting and diff checks passed.
Fixture corrections used a valid-but-wrong server domain and observation times
after reconciliation; final checks passed before commit. Disposable PostgreSQL
fixture removed. No production acceptance gate is claimed closed.


### Controller OpenBao transport integration checkpoint (2026-09-08)

Added an origin-bound registry-backed HTTP/1.1 connection owner and wired it into
controller workload OpenBao construction before authentication. Opt-in requires
an independently pinned OpenBao TLS root, reviewed server DNS name and dedicated
CA file; optional sweep interval defaults to 10s (1s–1m). Startup validates registry
schema access. Normal TLS and registry receipt/DNS/CRL checks gate handshakes;
periodic sweeps evict active/idle connections on revocation or unavailable evidence.
Shutdown stops admission. Alternate origins, plaintext, redirects and environment
proxies cannot carry provider authentication through this configured transport.

Local TLS/PostgreSQL tests cover Service and OpenBao TLS domains, real client
Kubernetes login and definite-403 token renewal against a fixture provider,
verified HTTP keep-alive, wrong pins/origins and active response eviction after
revocation, database loss and cancellation. Controller partial-policy configuration
fails closed. Default configuration remains unchanged. No actual OpenBao host
rollout or production qualification is claimed.

Five acceptance milestones remain: legacy migration/device replacement; trust
consumers/live sessions; backup/recovery and SDK integration; provider/hardware
compatibility; staging/custody/recovery qualification. Certificate-issuer provider
clients and other Service hosts still require adoption. Exact installed-CRL ACKs,
root-policy and server key renewal, external recovery history and live qualification
remain. Recovery commands retain independent recovery trust. No push, PR, remote
CI, live deployment or custody operation. Goal remains active.

Service commit: `43eb2ee`. Full Go suite with PostgreSQL, PKI/controller/OpenBao
race tests, final focused authentication/eviction race checks, vet, formatting
and diff checks passed. Disposable database removed. Host inventory updated to
distinguish completed controller wiring from remaining client/renewal adoption.


### Certificate-issuer OpenBao transport adoption checkpoint (2026-09-08)

Certificate-issuer configuration now loads and validates the independent OpenBao
transport pin, DNS name and bounded sweep interval. Enabled mode creates one
application-owned verified HTTP transport before signer setup and passes it to
Product/App/server registry clients plus both legacy OpenBao signer adapters.
Partial policy fails before database setup. Startup does not auto-migrate in this
mode; shutdown and bootstrap failure close the transport before its registry DB.
Defaults preserve existing transport behavior; secret/environment preparation
retains independent bootstrap trust.

Tests cover environment loading and invalid pins/DNS/origins/intervals, application
bootstrap in legacy and combined registry modes, unmigrated-schema refusal without
schema mutation, cleanup ordering and post-shutdown denial. Legacy signer tests
prove custom transport rejection reaches both adapters. Existing provider TLS,
authentication, renewal and stream-eviction tests remain part of the full suite.
No deployed host or production qualification evidence is claimed.

Five acceptance milestones remain: legacy migration/device replacement; trust
consumers/live sessions; backup/recovery and SDK integration; provider/hardware
compatibility; staging/custody/recovery qualification. Remaining work includes
other Service clients, exact installed-CRL ACKs for HTTP consumers, root-policy/key
renewal, external recovery-history reconciliation and actual host qualification.
No push, PR, remote CI, live deployment or custody operation. Goal remains active.

Service commit: `58f8e47`. Full Go suite with PostgreSQL, config/certificate-issuer/
bootstrap/PKI race tests, vet, formatting and diff checks passed. Disposable database
removed. No production acceptance gate is claimed closed.


### HTTP consumer exact-CRL acknowledgment checkpoint (2026-09-08)

Controller and certificate-issuer provider transports now optionally load the
existing registry server CRL consumer using an explicit manifest and separate
management mTLS identity. Configuration rejects partial policy. Startup prepares,
sweeps and acknowledges before returning the provider transport; the management
endpoint must already be reachable independently of the starting controller.
Each timer cycle prepares installed CRLs, sweeps connections with exact digest
bounds, then acknowledges only the prepared evidence. Failed preparation still
sweeps; failed sweeps withhold ACKs. ACK failure clears consumer readiness and
immediately sweeps again so pooled HTTP connections cannot bypass that denial.
Consumer management resources close with the transport lifecycle.

HTTP tests use actual registry CRLs/ACK rows with a test adapter and assert stream
eviction before new-digest ACK, plus preparation/ACK-failure eviction and startup
rejection. Existing production consumer tests separately cover management mTLS,
persistence, rollback rejection and exact prepared-digest acknowledgment. This is
composed local coverage; no live HTTP fleet/latency qualification is claimed.

Five acceptance milestones remain: legacy migration/device replacement; trust
consumers/live sessions; backup/recovery and SDK integration; provider/hardware
compatibility; staging/custody/recovery qualification. Remaining work includes
other Service host adoption, root-policy/key renewal, external recovery history,
legacy cohorts, hardware/platform evidence and live qualification. No push, PR,
remote CI, live deployment or custody operation. Goal remains active.

Service commit: `fc93e2f`. Full Go suite with PostgreSQL, PKI/consumer/config/host
race tests, final focused startup/order checks, vet, formatting and diff checks
passed. Disposable PostgreSQL fixture removed. No production acceptance gate closed.


### API Account Manager Service transport checkpoint (2026-09-08)

Added opt-in Service-domain trust to API app-token authorization requests to
Account Manager. Explicit root pin/DNS/CA configuration is validated before runtime
setup; enabled mode requires the API registry and disables schema auto-initialization.
The API owns the shared verified HTTP connection lifecycle and closes it before
the database. The authorizer receives that client, preserves its configured timeout
and bearer-token contract, and propagates transport denial without fallback.
Optional exact-CRL management settings reuse installed-digest preparation/sweep/ACK.
Domain selection remains fixed to `service`, independent of peer input.

Local tests cover config/env mapping, early partial-policy rejection, unmigrated
registry refusal without mutation, rejection of a CA-file-trusted but unregistered
server before HTTP, authorization transport denial, and timeout preservation.
Registered-Service TLS/eviction/ACK behavior is covered by the shared transport
suite. These are composed local checks, not live Account Manager deployment proof.

Five acceptance milestones remain: legacy migration/device replacement; trust
consumers/live sessions; backup/recovery and SDK integration; provider/hardware
compatibility; staging/custody/recovery qualification. Cross-service workers and
other Service hosts still require adoption; Service clientAuth lifecycle, root/key
renewal, external recovery history, legacy cohorts and live/hardware qualification
remain. No push, PR, remote CI, live deployment or custody operation. Goal active.

Service commit: `d612c1c`. Full Go suite with PostgreSQL, config/consumer/HTTP/API/
PKI race tests, vet, formatting and diff checks passed. Disposable database removed.
No production acceptance gate is claimed closed.


### Factory certificate-issuer Service transport checkpoint (2026-09-08)

An authoritative workspace search found no production constructor calls for the
three cross-service workers; each already accepts an HTTP client. Work moved to
the active factory-enrollment certificate-issuer client instead of inventing a
worker executable. Opt-in factory Service trust now defers issuer construction
until application-owned registry setup. Existing client certificate/key material
is attached to the verified Service transport; a dedicated Service CA/pin controls
server admission independently of legacy CA settings. Request timeout and refusal
of issuance/cancellation redirects remain. Enabled mode rejects proxies, externally
supplied issuer clients and missing registry/environment, and does not auto-migrate.
Shutdown/bootstrap failure/listener failure close the owned transport and registry.

Local tests cover mTLS through injected HTTP transport, timeout/redirect behavior,
environment decoding, unsafe bootstrap rejection, schema non-mutation, transport
ownership and listener-failure cleanup. Bootstrap tests use parseable identity
fixtures without asserting peer admission; registered-Service admission and exact
CRL behavior have shared local transport coverage. No live deployment is claimed.

Five acceptance milestones remain: legacy migration/device replacement; trust
consumers/live sessions; backup/recovery and SDK integration; provider/hardware
compatibility; staging/custody/recovery qualification. Next active host gap is
factory Account Manager admission/recovery transport; Service clientAuth lifecycle,
other hosts, root/key renewal, external recovery history and live/hardware evidence
remain. No push, PR, remote CI, deployment or custody action. Goal remains active.

Service commit: `184f422`. Full Go suite with PostgreSQL, factory/client/bootstrap/
consumer/PKI race tests, vet, formatting and diff checks passed. Disposable database
removed. No production acceptance gate is claimed closed.

### Factory Account Manager Service transport checkpoint (2026-09-08)

Factory enrollment now loads optional Account Manager Service trust under
`FACTORY_ENROLL_ACCOUNT_MANAGER_` and injects the application-owned verified
HTTP client into its existing admission adapter. Reservation, lookup, cancellation
and result publication, including recovery coordination, share this transport.
Dedicated bearer credentials, redirect refusal and uncertain-outcome semantics
are preserved. Partial trust configuration, missing registry/environment/token
and unmigrated PKI schemas fail startup. Either factory Service trust mode disables
automatic schema creation. Owned connections close before the registry database.
Optional exact CRL consumers use the existing prepare/sweep/ACK lifecycle.

Service commit: `ea00d9e`. Full Go suite, targeted factory/PKI/consumer race suite,
vet, formatting and diff checks passed. The disposable PostgreSQL fixture was
removed. Bootstrap tests cover both issuer and admission ownership and schema
non-mutation. Admission requests to an unregistered TLS peer remain unavailable
for all four operations and never become non-issuance evidence. Positive registry
TLS admission, eviction and exact CRL acknowledgement rely on shared transport
coverage; this checkpoint does not claim live Account Manager qualification.

Five acceptance milestones remain: legacy migration/device replacement; trust
consumers/live sessions; backup/recovery and SDK integration; provider/hardware
compatibility; staging/custody/recovery qualification. Factory admission transport
is no longer an implementation gap. Service clientAuth issuance/verification and
lifecycle, remaining host adoption, root/key renewal, external recovery history,
and real hardware/staging/custody evidence remain. Next implementation focus is
the Service clientAuth lifecycle, grounded in the original domain design.
No push, PR, remote CI, deployment or custody action. Goal remains active.

### Approved Service client identity policy checkpoint (2026-09-08)

Service intermediate requests now persist an exact `service_client_ids` policy
bound to independent approval alongside optional server DNS policy. Canonical
`service:<name>` identities are sorted/unique; other domains and issuer kinds
reject the field. Empty existing policy grants no new rights. Policy tampering,
removal and request reuse are rejected. Unsupported provider adapters cannot
consume provisioning claims or import provider material for the new profile.

A separate OpenBao `service-client` role uses P-256, digitalSignature and
clientAuth only, a 90-day maximum TTL, exact common names, no alternative SANs
and stored certificates. Its constrained signer ACL is emitted separately from
server signing on dual-profile Service issuers. Controller/recovery permissions
remain separate from signing. Local provider evidence checks certificate profile,
unapproved names/extra SAN rejection and reciprocal server/client signer denial.
This implements policy governance and provider provisioning, not completed
Service client leaf lifecycle or production host qualification.

Five acceptance milestones remain: legacy migration/device replacement; trust
consumers/live sessions; backup/recovery and SDK integration; provider/hardware
compatibility; staging/custody/recovery qualification. Next is durable Service
client issuance with strict CSR/provider validation and current receipt-based
verification, followed by revocation/recovery, renewal and listener adoption.
The original goal remains active. No push, PR, remote CI, deployment or real
custody action is authorized or performed by this checkpoint.

Service commit: `51709bb`. Full Go suite with local PostgreSQL/OpenBao,
PKI/provider/controller race tests and vet passed. After tightening the client
role maximum TTL to the design's 90-day target, affected policy/provider tests
were rerun under race detection and passed. Disposable fixtures were removed.
No production acceptance gate is claimed closed.

### Durable Service client issuance and verification checkpoint (2026-09-08)

Implemented independent `pki_service_client_issuances` receipts with a single
signing owner, immutable environment/caller/request/CSR/context binding, approved
exact Service identity policy and a 1–90 day requested lifetime. Pending or
uncertain outcomes cannot obtain a second signing claim. Completion/replay checks
current issuer lineage, original CSR/key/profile and fresh signed root/intermediate
CRLs. Both client-only and dual-profile Service issuers are supported; Device,
App and server receipt paths remain separate. Stored PostgreSQL issuance precision
is returned on completion/replay. Pending and unexpired client leaves, even locally
revoked leaves, now conservatively block Service issuer retirement.

Read-only repeatable-read verification requires an original successful unrevoked
receipt, recomputed request digest, approved Service policy, exact stored lineage,
independently supplied root pin, expected service identity and fresh signed CRLs.
The installed-CRL variant additionally requires both exact current issuer digests.
The verifier alone does not prove possession; callers must require authenticated
TLS. Schema migration and database-role refresh are explicit prerequisites. The
actual restricted issuer/controller/verifier roles preserve receipt/token/mutation
boundaries in local tests.

Local coverage includes concurrency, unknown outcomes, invalid CSR/leaf profiles,
wrong identity/environment/root, unrecorded certificates, policy/context tampering,
CRL advance/expiry/revocation, retirement accounting and real OpenBao sign/complete/
verify/replay. OpenBao non-CA leaves omit optional Basic Constraints; validation
accepts that standard encoding while rejecting CA=true, CA signing usage, non-client
EKUs and every SAN extension. A root-disable fixture was corrected to update both
indexed status and the canonical issuer document, as lifecycle writes do.

Five acceptance milestones remain: legacy migration/device replacement; trust
consumers/live sessions; backup/recovery and SDK integration; provider/hardware
compatibility; staging/custody/recovery qualification. Next Service client work is
authenticated issuance integration, operational uncertain-outcome reconciliation
and revocation, TLS listener/consumer adoption and renewal/recovery qualification.
No push, PR, remote CI, deployment or real custody action. Goal remains active.

Service commit: `992e15d`. Full Go suite with PostgreSQL/OpenBao, targeted
PKI/PostgreSQL/controller race suite, vet, formatting and diff checks passed.
Disposable database/provider fixtures were removed. No production acceptance
gate is claimed closed.

### Service client reconciliation and revocation checkpoint (2026-09-08)

Added issuer-scoped controller operations for Service client reconciliation,
revocation, provider publication and finalization. They use the existing Account
Manager mTLS/request-bound assertion boundary and require fresh MFA `pki_admin`.
Recovery takes only caller/request/optional serial, revalidates durable request
context and recovers a unique provider certificate without re-signing. Missing
serial recovery uses bounded complete inventory; writers must be fenced. Owner
completion and recovery share the same immutable completion transaction.

Revocation commits receipt denial and a durable Service client revocation row
atomically. Provider failure cannot restore access. Publication is scoped to the
Service issuer mount and imports only a signed CRL covering the recorded serial;
fresh covering evidence is reused. Finalization requires all configured consumers
on the exact current digest and rechecks policy/CRL changes. No acknowledgment is
manufactured by recovery or publication. Unexpired leaves still conservatively
block issuer retirement until consumer/host acceptance exists.

Tests cover request assertion binding, authorization, concurrent/uncertain recovery,
missing serials and original-context tampering, publication failures, wrong domains,
current-digest/consumer expansion gates, restricted SQL roles and local OpenBao
lost-serial recovery plus revoke/publish/finalize. Real-provider fixture comparison
allows equivalent surrounding PEM whitespace; acknowledgment time is sampled after
provider publication, so a newly issued CRL is not tested against an older clock.
Local fixture acknowledgments are not live listener-eviction evidence.

Five acceptance milestones remain: legacy migration/device replacement; trust
consumers/live sessions; backup/recovery and SDK integration; provider/hardware
compatibility; staging/custody/recovery qualification. Remaining Service client work
includes authenticated issuance integration, periodic CRL publication/consumer work,
TLS listener adoption, renewal and restored-inventory qualification. Explicit schema
migration and refreshed role grants are required for the new revocation table.
No push, PR, remote CI, deployment or custody action. Goal remains active.

The expanded real-provider flow exposed a shared current-CRL query bug: selecting
`number::text` and ordering by its unqualified output name used lexical ordering,
so CRL 9 could outrank CRL 10. Current selection now orders the numeric table column
explicitly. A regression imports 9, 10, 99 and 100 and checks current selection and
rejection of stale-digest acknowledgments. This fixes CRL selection across domains;
the earlier timestamp-fixture adjustment alone did not resolve the failure.

Service commit: `9897894`. The full Go suite with PostgreSQL/OpenBao, the targeted
PKI/PostgreSQL/controller race suite, vet, formatting and diff checks passed.
Disposable PostgreSQL and OpenBao fixtures were removed. No production acceptance
gate is claimed closed.

### Authenticated Service client issuance checkpoint (2026-09-08)

The certificate issuer now exposes `POST /v1/certificates/service-client/issue`
behind a dedicated `service-provisioner` mTLS policy. Forwarded certificate
headers are rejected. Requests bind caller, exact approved `service:<name>`
subject, CSR DER, TTL and metadata to the durable registry claim. CSRs with a
different subject, SAN, or requested extension fail before provider access.
OpenBao signing always uses the pinned Service issuer and fixed `service-client`
role; exact replay returns the recorded leaf without another provider call.

`CERT_ISSUER_SERVICE_CLIENT_PKI_ENABLED` is opt-in, requires the explicit schema
and direct mTLS, and disables automatic schema mutation. The endpoint applies its
own 90-day ceiling while allowing the shared issuer TTL policy to remain suitable
for longer-lived Device/App flows. The default TTL is capped at 90 days when the
shared default is higher.

Five acceptance milestones remain: legacy migration/device replacement; trust
consumers/live sessions; backup/recovery and SDK integration; provider/hardware
compatibility; staging/custody/recovery qualification. Remaining Service client
work is listener/consumer adoption, renewal ownership, periodic CRL operation and
restored-inventory qualification. No push, PR, remote CI, deployment or custody
action. Goal remains active.

Service commit: `73fdaa2`. Full Go tests, targeted race tests, vet, formatting and
diff checks passed. Local tests cover direct-mTLS enforcement, caller restriction,
fixed provider policy, exact replay, changed-context conflict and CSR extension
rejection. No production acceptance gate is claimed closed.

### Registry-checked Service client renewal checkpoint (2026-09-08)

`POST /v1/certificates/service-client/renew` now accepts an existing three-level
Service client chain only after the registry revalidates its successful unrevoked
receipt, exact approved identity, independent Service root pin and fresh signed
root/intermediate CRLs. The successor CSR is fixed to the authenticated
`service:<name>` identity and passes through the same 90-day profile, pinned
OpenBao role and durable claim/replay rules as initial issuance.

The independent root fingerprint is optional configuration for initial issuance;
without it, self-renewal fails unavailable. This supports a provisioner for first
enrollment while keeping routine renewal bound to the workload's current private
key. Existing shared Device/App TTL settings remain compatible.

Five acceptance milestones remain: legacy migration/device replacement; trust
consumers/live sessions; backup/recovery and SDK integration; provider/hardware
compatibility; staging/custody/recovery qualification. Next Service work is the
host-owned private-key/credential store, automatic renewal scheduling and actual
listener/client adoption. No push, PR, remote CI, deployment or custody action.
Goal remains active.

Service commit: `791a323`. Full Go tests, targeted race tests, vet, formatting and
diff checks passed. Tests cover successful self-renewal, denial after registry
rejection and the independent root configuration boundary. No production
acceptance gate is claimed closed.

### Host-owned Service credential store checkpoint (2026-09-08)

Video Cloud `835d50d` adds `internal/serviceidentity.Store`. It generates a P-256
private key on the workload host and persists it only in an atomic `0600` state
file under a private directory. Before issuance, the store durably records the
request ID, private key and exact CSR while retaining the active credential.
Restart reuses the same unresolved request; another request cannot replace it.

Installation verifies a three-certificate clientAuth chain, exact Service subject,
pending public key, validity and certificate lineage before atomically promoting
it. A failed or mismatched renewal leaves the prior credential loadable. Successful
replacement is synchronized through the containing directory. The PKI registry
continues to store public certificates and receipts only, never this private key.

Five acceptance milestones remain: legacy migration/device replacement; trust
consumers/live sessions; backup/recovery and SDK integration; provider/hardware
compatibility; staging/custody/recovery qualification. Next is wiring this store to
an issuance/renewal client and a real Service transport owner. Hardware-backed key
adapters and live rollout remain separate. No push, PR, remote CI, deployment or
custody action. Goal remains active.

Full Go tests, the Service identity race test, vet, formatting and diff checks
passed. Restart replay, permissions, key mismatch and failed-renewal rollback are
covered. No production acceptance gate is claimed closed.
# Service credential trust correction (2026-09-08)

Service commit `bdd1070` corrects the host store introduced in `835d50d`.
The previous installer verified against the response's own root and reload checked
only subject/expiry. Opening now requires an independently configured root SHA-256
pin. Installation and reload both enforce the pin, ordered chain signatures,
P-256, exact CN-only clientAuth profile, validity, issuer margin and 90-day ceiling.
Invalid installation retains the current credential and pending request.

Full Go tests, targeted race tests and vet passed. Regression coverage rejects
an untrusted response root, changed reload pin, expired leaf and incomplete saved
chain. This is a prerequisite correction; host network orchestration, scheduling,
runtime receipt/CRL checks and listener adoption remain unfinished.

Five acceptance milestones remain: legacy migration/device replacement; trust
consumers/live sessions; backup/recovery and SDK integration; provider/hardware
compatibility; staging/custody/recovery qualification. No live qualification gate
is closed. Local commits only.

### Service client integration milestone completed locally (2026-09-08)

Video Cloud commit `0481e7a` completes all **5/5 work groups** in the current
factory-to-certificate-issuer Service client integration milestone:

1. **Issuance/renewal orchestration:** the host store drives authenticated initial
   issuance and same-identity renewal. Durable request/CSR state survives lost
   responses; replay does not create another signature.
2. **Scheduling/restart ownership:** one manager holds the host state lease,
   renews at two-thirds of actual validity and retries every minute. Initial
   startup failure preserves the request for supervised restart; uncertain
   provider claims require the existing reconciliation operation.
3. **Runtime integration:** factory enrollment owns `service:factory-enroll`,
   dynamically presents its installed credential through the verified Service
   HTTP owner, and closes previous connections on replacement. The certificate
   issuer owns Service listener admission and active/hijacked socket eviction.
4. **Registry/CRL lifecycle:** current receipt/profile/root/CRL checks apply during
   admission, requests and periodic sweeps. Durable installed CRLs bind exact
   acknowledgments; failed sweeps do not acknowledge. The Service controller
   worker now handles client-only and combined server/client issuer revocations.
5. **Integration/recovery checks:** real PostgreSQL and mTLS exercise lost-response
   restart, successor installation, active-stream eviction, new-handshake denial,
   exact CRL acknowledgment and denial of a restored revoked host backup. A
   read-only paginated Service client recovery inventory checks all receipts,
   pending claims, lineage, publication and current consumer evidence.

The host private key stays in its private persistent directory; the registry
contains public receipts. Configuration examples and runbook document the
dedicated initial provisioner, per-instance storage, route identity policy,
explicit schema/grants, CRL manifests and restore command. An unfinished TLS
handshake cannot block listener sweep/shutdown.

Validation passed: full Go suite with disposable PostgreSQL; affected runtime and
PKI race tests; local OpenBao 2.5.5 provisioning/signing/reconciliation/revocation
integration; vet, formatting and diff checks. The new end-to-end host test uses a
fixture OpenBao HTTP signing endpoint; the separate real-provider test validates
OpenBao behavior. Host-backup restore and registry inventory checks do not
substitute for matched production database/provider PITR or post-backup audit
reconciliation.

**Current integration milestone: 0 work groups unfinished.** Five broader
acceptance milestones remain: (1) legacy migration/device replacement,
(2) trust consumers/live sessions, (3) backup/recovery and SDK integration,
(4) provider/hardware compatibility, (5) staging/custody/recovery qualification.
This closes the concrete local factory-to-issuer slice; other service-host
adoption and live/hardware/recovery acceptance are not claimed complete.
No push, PR, remote CI, deployment or custody operation.

### Service client provider recovery checkpoint (2026-09-08)

Video Cloud `4cac76a` adds
`pkicontroller recovery-check-service-client ISSUER_ID EXPECTED_ROOT_SHA256 [SERVICE_SUBJECT LEAF_PEM]`.
With writers fenced, it checks approved restored Service lineage against an
independent root pin, then reads the separate OpenBao `service-client` role's
selected issuer and public key binding. It cannot substitute the server role.
An optional exact Service leaf must also pass its successful unrevoked receipt,
profile, policy and current signed root/intermediate CRLs. Invalid local evidence
fails before provider metadata reads. The registry transaction is read-only.

Affected-package tests with PostgreSQL, targeted race tests, vet and diff checks
passed. A disposable OpenBao 2.5.5 integration exercised the existing recovery-only
ACL and verified that signing, revocation and key generation are denied. Its
offline Service root fixture now supplies the serial metadata required by the
strict recovery validator.

This closes a provider-lineage verification item within backup/recovery work,
not an additional completed acceptance milestone. Issuer-only success proves
lineage matching, not CRL freshness. Neither this check nor registry inventory
proves signing-key usability, full provider inventory/policy equivalence or
post-backup security history. Other Service host adoption and live matched
restore/audit reconciliation remain.

Five broader acceptance milestones remain: legacy migration/device replacement;
trust consumers/live sessions; backup/recovery and SDK integration;
provider/hardware compatibility; staging/custody/recovery qualification.
Local commits only; no push, PR, remote CI, deployment or custody action.

### Authenticated private-server renewal API milestone (2026-09-08)

Video Cloud `aac4a46` completes the fixed **3/3** local API checklist:
(1) authenticate current-certificate ownership and current registry/CRL admission;
(2) issue an exact-CN/DNS replacement through durable claims and replay;
(3) add regression coverage, configuration and recovery guidance.

`POST /v1/certificates/gateway/renew` requires an authorized direct-mTLS
management caller plus a versioned request proof signed by the current P-256
server key. Server certificates stay serverAuth-only. The proof binds environment,
domain, independent root pin, caller, predecessor, CSR, request ID and explicit TTL.
Both predecessor verification and claimed replacement lineage enforce the pin.
The separate initial route also enforces the pin when configured. There is no
legacy fallback from renewal.

Tests with disposable PostgreSQL and a fixture OpenBao signing endpoint cover
successor key replacement, identical replay with a fresh randomized signature,
changed request conflict, uncertain provider outcomes without re-signing, invalid
caller/proof/domain/root/names, stale CRLs, expiry and revoked-predecessor replay.
Affected-package tests, targeted race tests, vet and diff checks passed. This
checkpoint did not run a real OpenBao instance or deploy a host.

**Current API milestone: 0/3 items unfinished.** The next server-host implementation
gap remains protected key/CSR state, durable scheduling and listener replacement;
the API alone does not implement those. Five broader acceptance milestones remain:
legacy migration/device replacement; trust consumers/live sessions;
backup/recovery and SDK integration; provider/hardware compatibility;
staging/custody/recovery qualification. No push, PR, remote CI or custody action.

### Managed certificate-issuer server-host milestone (2026-09-08)

Video Cloud `bbbd902` completes the fixed **4/4** local host checklist:
(1) protected server key/CSR storage; (2) durable renewal scheduling/restart retries;
(3) actual certificate-issuer listener replacement and connection eviction;
(4) integration/restore tests, configuration example and runbook.

The opt-in Service server host imports an already registered three-certificate
chain and matching key once, after current receipt/CRL verification. A private
0700 directory, atomic 0600 state and manager lease protect host-owned state.
Replacement keys/CSRs survive lost responses and restart. Renewal uses a separate
authorized management mTLS identity plus current server-key proof; server leaves
remain serverAuth-only. The worker checks on startup and every minute and renews
at two-thirds of validity. The listener dynamically selects the installed leaf
and checks current registry/CRL admission at handshake, requests and timed sweeps;
installation and trust denial evict active connections.

Full `GOWORK=off go test ./...` passed with disposable PostgreSQL. Race tests for
serviceidentity, config, certissuerapp and pkitrust, `go vet ./...`, gofmt and diff
checks passed. The PostgreSQL/mTLS integration proves pending-key replay without
a second signature after a lost response, successor certificate selection,
old-stream eviction, revocation eviction and rejection of restored predecessor
state. Provider signing used an OpenBao HTTP fixture, not a real provider run.

**Current host milestone: 0/4 work groups unfinished.** This closes certificate
issuer host adoption; it does not close other Service/MQTT/OpenBao server hosts,
dynamic root-policy migration, management credential lifecycle, matched provider/
database restore or post-backup audit reconciliation. Local ownership checks do
not themselves install or acknowledge CRLs.

Five broader acceptance milestones remain: (1) legacy migration/device replacement,
(2) trust consumers/live sessions, (3) backup/recovery and SDK integration,
(4) provider/hardware compatibility, (5) staging/custody/recovery qualification.
Local commits only; no push, PR, remote CI, deployment or custody operation.



### PKI controller managed server-host milestone (2026-09-08)

Video Cloud `6d76748` completes the fixed **3/3** local adoption checklist:
(1) share the managed Service server host owner; (2) integrate controller policy,
listener and lifecycle; (3) validate compatibility/recovery behavior and document
deployment configuration.

Both certificate issuer and PKI controller now use `internal/pkitrust.ServerHost`.
The controller's isolated `PKI_HOST_` configuration selects protected per-replica
state, independent host root/name policy, separate renewal management identity and
verified remote issuer transport. Initial registered chain/key files seed state
once. Startup/shutdown own scheduling, lease and transport cleanup; the listener
uses current credentials and evicts connections on replacement or trust denial.
Static TLS remains the default; incomplete host policy fails closed. The existing
controller production-qualification gate and account-manager authorization remain.

Full Go tests passed with disposable PostgreSQL; affected-package race tests,
`go vet ./...`, formatting and diff checks passed. The shared loader regression
adds mTLS listener admission, exclusive ownership, worker shutdown, restart without
seed files and revoked-state startup denial. Existing tests retain lost-response
renewal replay and active-stream eviction coverage. Controller adapter tests verify
static mTLS compatibility and reject partial configuration. Signing uses an OpenBao
HTTP fixture; no actual controller deployment or real-provider run is claimed.

**Current controller-host milestone: 0/3 items unfinished.** Other Service hosts,
MQTT/OpenBao server-key lifecycle adoption, dynamic root migration, management
credential renewal and matched recovery-history reconciliation remain. Optional
remote CRL consumption needs an independently reachable management endpoint at
startup; local host admission does not emit CRL acknowledgments.

Five broader acceptance milestones remain: (1) legacy migration/device replacement,
(2) trust consumers/live sessions, (3) backup/recovery and SDK integration,
(4) provider/hardware compatibility, (5) staging/custody/recovery qualification.
Local commits only; no push, PR, remote CI, deployment or custody operation.

### Managed EMQX host implementation (2026-09-08)

Documentation was clarified first in workspace `95bbc11`. Video Cloud
`0387086` completes the fixed **4/4** plan in
[Managed EMQX host identity](production-pki-emqx-host.md):
responsibility clarification; MQTT host identity/renewal;
local EMQX installation and process ownership; tests/deployment/runbook.

`emqxpkihost` supervises one native EMQX foreground node under a dedicated
systemd control group. The MQTT leaf private key stays in protected local state
and runtime files; the renewal origin uses independent Service trust and a
separate management credential. Durable pending renewal survives restart/lost
responses. The process owner stops before replacement, rechecks registry evidence,
and denies serving on revocation, expiry or unavailable trust. Polling is every
five seconds with five-second verification timeouts; process shutdown escalates
from TERM to KILL. This replaces the full node and causes a reconnect outage.
`pkibroker` remains an outbound session worker with no TLS listener.

Full Go tests passed with disposable PostgreSQL. Affected-package race checks,
vet, formatting and diff checks passed. Release verification passed with the
normal PKCS#11-enabled build (an earlier build without PKCS#11 was correctly
rejected by the release checker). The MQTT-domain integration uses distinct MQTT
and Service roots, lost-response replay, restart without seed files and denial of
restored revoked state. Its signing endpoint is an OpenBao HTTP fixture.

A disposable real `emqx/emqx:5.9.0` test passed for replacement TLS certificates
and established MQTT 3.1.1 session termination after replacement and simulated
trust denial. The image digest is
`sha256:c897388a3c628b684c064459a14c71b259317b044be02a586eaaab14916d755c`.
Client authentication was disabled only in that isolated lifecycle fixture.
Actual operator authentication/ACL policy, durable-session recovery, cluster
rollout, physical custody and matched restore/security-history reconciliation
remain acceptance work. No deployment or zero-downtime guarantee is claimed;
local supervision does not emit CRL installation acknowledgments.

**Current EMQX milestone: 0/4 items unfinished.** Five broader acceptance
milestones remain: (1) legacy migration/device replacement, (2) trust consumers/
live sessions, (3) backup/recovery and SDK integration, (4) provider/hardware
compatibility, (5) staging/custody/recovery qualification.
Local commits only; no push, PR, remote CI, deployment or custody operation.

### Milestone 1 local cohort reporting preparation (2026-09-08)

Video Cloud `e6c17a4` adds `pkicontroller legacy-progress OPERATION_ID`.
It reports the fixed cohort from a completed governed staging import using a
read-only repeatable-read snapshot. The report validates operation/manifest scope
and digest, tracks pending signing, awaiting acknowledgment and acknowledgment,
retains missing/inconsistent entries, and reports current legacy registry
acceptance independently from replacement status. Expiry/revocation is not
counted as successful replacement. This is not global population inventory,
current successor health, installed CRL evidence or live-session proof.

The complete pki/pkicontrollerapp suites passed with disposable PostgreSQL.
Legacy migration/replacement race tests, final progress race tests, vet, gofmt
and diff checks passed. The new integration exercises approved import through
actual replacement claim/completion/acknowledgment, residual accounting,
expiry, revocation, changed/missing bindings and audit-write absence.

The active milestone is now legacy migration/device replacement, with the fixed
[five-item acceptance checklist](production-pki-legacy-rollout.md). Local reporting
preparation is complete. **All five live checklist items remain open:** cohort
inventory; approved canary import/trust deployment; measured replacement;
expanded rollout/residual accounting; legacy-trust withdrawal. The target staging
environment and cohort have been requested; no live inventory, approvals, device
replacement or consumer trust change was performed.

Five broader acceptance milestones remain: legacy migration/device replacement
(active); trust consumers/live sessions; backup/recovery and SDK integration;
provider/hardware compatibility; staging/custody/recovery qualification.
Local commits only; no push, PR, remote CI, deployment or custody operation.
