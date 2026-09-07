# Production PKI domain and host inventory

Reviewed 2026-09-08 through Video Cloud `57c68f6`.
This inventory preserves the existing five acceptance milestones. It identifies
implementation work; it does not add a sixth milestone or certify production.
The authoritative trust boundaries remain Platform PKI contract sections 3–5.

## Current entry points

| Domain/use | Current source and behavior | Remaining implementation/acceptance |
| --- | --- | --- |
| Device client identity | Video Cloud `internal/pki` registry and Product claims; `internal/certissuer/product.go` and Product-mode bootstrap; SDK renewal/trust and owner-lifetime adapters recorded in the ledger. | Real legacy cohort replacement, application policy/owner adoption, physical firmware and supported-platform evidence. |
| App/user client identity | `internal/pki/app_issuance.go`, `app_verification.go`, `app_revocation.go`, `app_crl_worker.go`; API consumer and `internal/pkitrust/registry_app.go` compose broker/TURN acknowledgment after sweeps. Recovery includes `app_reconcile.go`, `recovery_app.go`, and `recovery_app_inventory.go`. | Dynamic App root-policy adoption, remaining application/SDK host wiring and real broker/relay eviction evidence. |
| Gateway/server issuance | `internal/certissuer/server_registry.go` and gateway handler/bootstrap select a configured independent registry domain via `CERT_ISSUER_SERVER_PKI_DOMAIN`. Exact approved DNS policy, durable claims, provider validation and CRL-aware replay apply. Empty mode retains the legacy Device-backed signer. | Remaining server host adoption, root/key renewal, external recovery-history reconciliation and actual host cutover. CRL maintenance, public lineage recovery and restored-registry inventory are implemented locally. |
| Internal service client/server identity | `internal/certissuer/material.go:LoadTLSConfig` and service bootstraps load provisioned transport certificates/roots; the generic registry admits Service roots/intermediates. | Service serverAuth issuance/receipts/revocation and a reusable HTTP connection owner exist locally. Service clientAuth profile/lifecycle and actual service-host wiring remain; loaded files alone do not prove registry-managed lifecycle. |
| Dedicated MQTT server TLS | Independent `mqtt` issuer/receipt/CRL verification; `a17c4ff` wires opt-in API subscriber/publisher and log-ingester TLS admission, scheduled sweeps and connection eviction. | Root-policy refresh, broker key renewal and actual host rollout. CRL maintenance is implemented in `7869d5e`, and exact installed-digest MQTT acknowledgments in `57c68f6`. Public-CA MQTT remains a distinct supported contract choice. |
| OpenBao transport TLS | Dedicated transport CA/files and TLS Raft artifacts; independent server issuance/CRLs/recovery; controller and certificate issuer support opt-in registry-backed provider HTTP with verified login/renewal and periodic connection eviction. | Other provider-client adoption, exact installed-CRL ACKs, root-policy/server-key renewal and real host rollout. Transport trust remains independent of Device/App/Service roots; seal/custody and HA qualification remain separate. |
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
