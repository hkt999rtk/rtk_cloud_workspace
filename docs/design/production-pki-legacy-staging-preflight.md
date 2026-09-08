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

### Follow-up: provider availability and local rollout preparation

A subsequent 2026-09-08 read-only attempt to forward the OpenBao Service for
public root/CRL retrieval failed because `openbao-0` was Pending. Pod events
reported CSI device-path mount failures for `pvc-0848cb41ff1545f1` and
`pvc-d5992ca40171483a` on `lke646126-951189-9w6nc`. No public root or CRL was
retrieved. Existing storage must be recovered and qualified before provider
operations; no restart, PVC change, initialization or repair was performed.

The service now provides controller image/release packaging and an offline
three-phase [rollout renderer/runbook](../../repos/rtk_video_cloud/docs/pki-staging-rollout.md).
It prepares reviewable manifests only. No schema migration, role grant,
controller deployment, consumer cutover or CI publication has occurred.

### Execution prerequisites

### 2026-09-08 storage diagnosis and proposed recovery

Read-only follow-up confirmed `openbao-0` remains ContainerCreating on
`lke646126-951189-9w6nc` (Linode instance `104080805`). Both PVCs remain Bound,
with Retain reclaim and StatefulSet retention policies:

| Claim | Linode volume | Kubernetes PV |
| --- | --- | --- |
| data-openbao-0 | 17676701 | pvc-0848cb41ff1545f1 |
| audit-openbao-0 | 17641459 | pvc-d5992ca40171483a |

The Linode read API reports both volumes active, in `sg-sin-2`, attached to
`104080805`; Kubernetes VolumeAttachments also report attached to that node.
Read-only inspection through its existing CSI node container found only `sda`
in `/proc/partitions` and only the QEMU system disk in `/dev/disk/by-id`.
Neither expected volume device exists. CSI logs repeatedly fail NodeStageVolume
for these exact handles. This identifies a control-plane/guest device discrepancy,
not proof that data was lost or that it is intact.

Proposed next live action, not executed: temporarily exclude this node from the
OpenBao StatefulSet's scheduling through required node affinity, preserving its
existing pod anti-affinity, then recreate only the unstarted OpenBao pod. Retain
the exact PVCs, single replica, image `quay.io/openbao/openbao:2.5.4`, Secrets and
configuration. Kubernetes/CSI must perform ordinary detach/reattach; do not force
detach, delete VolumeAttachments, format disks, recreate PVCs, initialize OpenBao,
or change the storage backend. Other nodes in the same region are Ready, but
actual placement/capacity and attachment behavior must be verified during repair.

Before mutation capture exact StatefulSet resourceVersion/template and Pod UID;
use preconditions to reject concurrent changes. Confirm no existing OpenBao
process is running and no other pod is using these claims. Stop on multi-attach,
missing volume, mount/filesystem failure or unexpected initialization state;
escalate the retained volume IDs and device evidence to the provider rather than
resetting storage. Preserve the old template for rollback; restoring it must not
force a second running writer or bypass CSI ownership.

After mounting, verify existing initialized/sealed state before any further
operation. Unseal/custody actions remain separate. Retrieve only public legacy
Root/issuer chains/CRLs once available, then resume cohort eligibility analysis.
No live repair, restart, provider mutation or support message was performed.

Local follow-up `5787230` aligns the controller pod with the existing OpenBao HA
network-policy selector. Three renderer tests and a direct comparison with the
actual HA values passed: the controller matches the allowed pod selector while
migration/grant Jobs do not. The existing workload namespace must also carry
`rtk.cloud/pki-client-access=enabled`; this was documented, not applied or assumed
present. This closes a manifest integration gap without changing live acceptance.

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
