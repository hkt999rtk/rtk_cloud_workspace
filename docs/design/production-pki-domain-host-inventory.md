# Production PKI domain and host inventory

Reviewed 2026-09-08 through Video Cloud `6f5838e`.
This inventory preserves the existing five acceptance milestones. It identifies
implementation work; it does not add a sixth milestone or certify production.
The authoritative trust boundaries remain Platform PKI contract sections 3–5.

## Current entry points

| Domain/use | Current source and behavior | Remaining implementation/acceptance |
| --- | --- | --- |
| Device client identity | Video Cloud `internal/pki` registry and Product claims; `internal/certissuer/product.go` and Product-mode bootstrap; SDK renewal/trust and owner-lifetime adapters recorded in the ledger. | Real legacy cohort replacement, application policy/owner adoption, physical firmware and supported-platform evidence. |
| App/user client identity | `internal/pki/app_issuance.go`, `app_verification.go`, `app_revocation.go`, `app_crl_worker.go`; API consumer and `internal/pkitrust/registry_app.go` compose broker/TURN acknowledgment after sweeps. Recovery includes `app_reconcile.go`, `recovery_app.go`, and `recovery_app_inventory.go`. | Dynamic App root-policy adoption, remaining application/SDK host wiring and real broker/relay eviction evidence. |
| Gateway/server issuance | `internal/certissuer/server_registry.go` and gateway handler/bootstrap select a configured independent registry domain via `CERT_ISSUER_SERVER_PKI_DOMAIN`. Exact approved DNS policy, durable claims, provider validation and CRL-aware replay apply. Empty mode retains the legacy Device-backed signer. | Uncertain-outcome reconciliation, server revocation publication/consumer adoption and actual host cutover. |
| Internal service client/server identity | `internal/certissuer/material.go:LoadTLSConfig` and service bootstraps load provisioned transport certificates/roots; the generic registry admits Service roots/intermediates. | An approved Service leaf profile, online issuance/selection and registry-backed service identity lifetime/revocation. Existing loaded files do not prove registry-managed lifecycle. |
| Dedicated MQTT server TLS | Generic registry scope accepts `mqtt`; deployment TLS material can be provisioned separately. | If using the dedicated private-CA option, explicit server profile and consumer renewal/trust adoption. Public-CA MQTT remains a distinct supported contract choice. |
| OpenBao transport TLS | Dedicated transport CA/files and TLS Raft deployment artifacts exist. | Dedicated server-only issuance/renewal policy and host adoption; transport trust must remain independent of Device, App and Service roots. Seal/custody and real HA qualification are separate. |
| Public HTTPS | Contract requires publicly trusted CA/ACME. | Verify deployment/renewal acceptance separately; never route browser/public HTTPS issuance through private Device/App issuers. |

## Next implementation sequence: independent server-domain issuance

Steps 1–2 are locally implemented in Video Cloud `893ff9a`: approved canonical
DNS policy, digest revalidation, independent private server-domain provisioning
and server-only OpenBao roles. Existing reservations without a policy remain
unsupported. The durable registry claim/validation portion of step 3 is implemented in
`f304ec1`; provider/HTTP composition and opt-in bootstrap (step 4) are implemented
in `6f5838e`. Uncertain-outcome recovery and actual gateway host adoption remain.

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
