# Legacy migration: staging preflight, 2026-09-08

Observed at 2026-09-08 03:11 UTC using the canonical staging kubeconfig.
Target: context `lke646126-ctx`, stack `video-cloud-staging`.
Queries used explicit read-only PostgreSQL transactions with 10-second statement
timeouts. Kubernetes discovery and the exact public `device-ca.crt` field were
read; private keys and secrets were neither printed nor persisted.

## Live observations

| Check | Result |
| --- | --- |
| Successful issuance rows | 226 |
| Distinct device IDs / fingerprints | 226 / 226 |
| Active entitlements | 204 |
| Successful issuance rows mapped to active entitlements | 204 |
| Active entitlements missing cloud/product mapping | 0 / 0 |
| Missing leaf / stored chain / fingerprint | 0 / 0 / 0 |
| Parsed leaves/chains, matching DER fingerprint and device CN | 226 each |
| ClientAuth-only leaf EKU | 226 |
| Time-valid leaf and stored chain | 226 |
| Stored chain length | 2 certificates for each row |
| Stored chains ending in a self-issued certificate | 0 |
| PKI registry tables in Video Cloud database | No `pki_*` tables |
| PKI controller workload | Not found in pod discovery |
| OpenBao | `openbao-0` exists in staging-secrets namespace |

All three inspected deployments (certissuer, factoryenroll, video-cloud-api) use
`ghcr.io/hkt999rtk/rtk_video_cloud/video-cloud-api:sha-8fad83a3c71f`.
The certissuer has legacy OpenBao mount/role settings; inspected workloads have
no explicit Product PKI enablement settings and no envFrom sources. Image
publication provenance was not assessed by this read-only inventory.

Stored-chain terminal certificate fingerprints:
- `f68b10bcd25d009a5cd11ae5fca9c4366054629b064425d244bc77baf810ae1e`: 204 rows.
- `813b407fa8903963d7abdcc791e29397fc9dffe5061c95e2b5bf0d372e0c6c08`: 22 rows.

The configured `/etc/video-cloud/certissuer/device-ca.crt` contains one CA
certificate matching the 204-row fingerprint above. It is not self-issued.
It cannot be substituted for the self-signed legacy Root required by the governed
inventory/import validator.

## Meaning and limits

The 204 active-entitlement records are the candidate population for further
review, not an approved or eligible cohort. The other 22 issuance records lack an
active matching entitlement and must remain in residual/source accounting.
No device identifiers or certificate bodies are included in this committed report.

Parsing, fingerprint, CN, EKU and time checks do not establish certificate
signature/path validity, CRL freshness or import eligibility. Full legacy root
and issuer-chain/CRL artifacts have not been identified. No target Device/Brand/
Product hierarchy exists in this live registry because the registry tables are
absent. No governed import or replacement operation can be executed yet.

## Ordered prerequisites before canary execution

1. Prepare and publish the reviewed PKI-capable service revision through canonical
   CI; retain existing images/settings as rollback evidence. Do not deploy local
   images to staging. Deployments remain Kubernetes-only.
2. Review explicit PKI schema and database-role migration, controller/service
   configuration and immutable image references. Apply only in the subsequently
   authorized staging rollout; this assessment performed no schema changes.
3. Establish the target staging Device/Brand/Product hierarchy through its real
   governed process, with actual custodian identities and signing custody.
4. Obtain the actual public legacy Root, full applicable issuer chains and fresh
   signed CRLs; run governed inventory to validate each candidate and resolve the
   22 residual records.
5. Select a bounded canary cohort from verified eligible records, retain its
   source IDs/fingerprints in controlled operational evidence, then obtain the
   required independent approvals and exercise replacement/acknowledgment.

The fixed milestone-1 checklist remains open. Environment discovery and source
assessment have progressed item 1; cohort eligibility and selection are incomplete.
No live import, issuance, revocation, device replacement, trust change, deployment,
PR, push or CI run occurred.
