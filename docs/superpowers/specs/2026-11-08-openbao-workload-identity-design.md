# OpenBao cloud workload identity custody

Status: accepted target; implementation and environment migration pending.
Approval: operator selected preservation of original keys/certificates and
per-service restart, dev before staging, on 2026-11-08.

## Decision

Use existing OpenBao KV v2 as durable storage for cloud workload identity
state and accepted trust/CRL state. Use private temporary projections in Pods.
Keep CA signing in existing PKI engines, registry/admission in PostgreSQL,
original offline Root custody and independent OpenBao cold-start storage.
Do not upload device/user client private keys or rotate identities for migration.

## Alternatives

Retaining leaf PVCs and deleting only caches saves too few volumes. Reissuing
per-Pod identities changes lifecycle and is not the selected compatibility
model. The selected KV custody preserves original complete state and allows
bounded per-service migration without dual writers or a new storage service.

## Interface and persistence

Environment intent selects PKI_STATE_BACKEND=pvc|openbao; OpenBao additionally
requires PKI_STATE_KV_MOUNT. Keep existing deployments on their selected PVC
backend until an explicit migration promotion. Derived record names include
original environment, create generation, workload and identity purpose; no Pod
UID in the durable identity key. Separate Kubernetes auth role/SA per owner,
projected short-lived audience-bound JWT and exact-record ACLs. No root token
or private key in environment settings, logs or public reports.

Native stores load/validate whole records, including pending key/CSR/request ID,
and commit changes using KV v2 CAS. Existing serviceidentity APIs remain usable
with file backend. Remote records include schema, owner/trust binding and exact
native payload. A CAS conflict is explicit; never retry a stale overwrite.
Reconcile uncertain writes by reading the expected operation/payload before
continuing. Preserve same request after uncertain issuance. Enforce single
writer using Kubernetes Lease plus CAS fencing. Lease loss prevents identity
updates and active ownership; reconnect cannot resume as stale owner.

Keep original application file paths as private temporary projections where
consumers require files. Commit authoritative updates before projection/use.
Do not implement init-only downloads or periodic blind file copies. Existing
loaded valid credentials may survive a brief outage within trust/expiry limits;
cold start and pending changes wait/fail explicitly, never regenerate.

Public Root pins remain independent environment/trust inputs. Cache latest
verified CRLs and high-water versions durably; temporary cache reset does not
reset anti-rollback. Controller startup must not depend on its own HTTP API.

## Fresh dev and staging generations (supersedes migration)

Operator explicitly authorized removal of all dev Linode runtime resources and
fresh dev creation, then separately confirmed staging also rebuilds from scratch.
Do not implement old-volume key migration. Preserve/separate original local
material and historical evidence, but initialize independent new PKI identities
through qualified new-generation bootstrap. Existing prod, CI runners and
shared release artifacts are outside cleanup. Formal removal must bind exact
owned cluster/node/VM/LB/volume/bucket/DNS IDs and verify absence; incomplete
provider responses reconcile through retained original evidence. Keep old
generations until new code/lifecycle tests are ready, then dev removal/create/
health first, staging second. Original environment settings remain authoritative;
explicitly select openbao backend for new generations as this approved intent.

## Acceptance

Isolated real OpenBao + PostgreSQL + Kubernetes lifecycle tests, interrupted
creation/renewal, CAS conflict/lost response, cross-owner denial, cold restart,
OpenBao outage, CRL rollback/expiry using controlled clock, and matched-state backup/restore with
newer committed renewal must pass before any live migration. Then dev full create preflight/deployment/health, followed by staging.
Report old/new resource IDs, original archived generation evidence, and actual
PVC counts; never claim prior cloud data survived authorized destruction.
