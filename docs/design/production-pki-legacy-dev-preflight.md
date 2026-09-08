# Legacy migration: dev source evidence

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
