# OpenBao Workload Identity Implementation Plan

> **For agentic workers:** Use superpowers:executing-plans in this session;
> a single fresh whole-branch review follows complete implementation.

**Goal:** Build new dev then staging generations with OpenBao workload identity custody.
**Architecture:** KV v2 durable state, native synchronous store adapters, private
Pod projections, Kubernetes auth/Lease, existing PKI registry and deployment executor.
**Tech Stack:** Go, OpenBao KV v2, Kubernetes, PostgreSQL, existing shell wrappers.
**Spec:** ../specs/2026-11-08-openbao-workload-identity-design.md

## Global constraints

Preserve old local keys/journals in generation archives. User explicitly authorized
removal of dev and staging Linode runtime resources and fresh creation; new
generations establish independent identities. Protect prod, CI and shared artifacts.
Do not create identities in preflight. Environment settings select all policy.
Local operation data: ~/.config/rtk_cloud/<environment>/, 0700/0600.
Independent OpenBao cold startup and original offline Root custody remain.
No live migration before isolated complete lifecycle acceptance.

## Review focus

Lost response: reconcile exact committed state, not duplicate issuance.
Old writer: deny stale owner after lease transfer, including resumed process.
CRL cache loss: preserve anti-rollback high-water across Pod reconstruction.
Rollback after renewal: newest state must reach original PVC before reuse.
OpenBao restore: reconcile KV versions with original registry and revocations.

## Tasks

- [x] 1. Update canonical Platform PKI contract and workspace custody/architecture
  sources; label accepted target separately from observed deployed state.
- [ ] 2. Add versioned KV record read/write primitives to native OpenBao client.
  Tests: create-only CAS, stale version, missing/deleted data, forbidden response,
  lost write response and reconciliation, path traversal, bounded body, no secrets
  in errors. Preserve exact native state bytes; no generic filesystem mirroring.
- [ ] 3. Add remote native identity backend and scoped runtime configuration.
  Test file/remote parity, unchanged original keys, pending recovery, renewed
  state commit before exposure, initial creation vs existing record absence.
  Implement owner fencing and token lifecycle using existing Kubernetes/OpenBao
  primitives. Test lease loss/takeover, concurrent update and denied foreign key.
- [ ] 4. Adapt all native client/server owners and trust/CRL stores, excluding
  openbaohost startup. Preserve cache anti-rollback and cold-start ordering.
  Test real native API/Controller/CertIssuer/worker/management profiles.
- [ ] 5. Project environment-selected backend through shared create/upgrade/
  recovery/preflight renderers. No migration CLI or copying old workload keys:
  operator superseded in-place migration with fresh dev and staging generations.
  Create-only CAS initializes scoped records only in qualified new bootstrap.
- [ ] 6. Run isolated real OpenBao/Postgres/Kubernetes full lifecycle and failure
  matrix. Controlled clock for expiration; no real TTL waits. Validate cold start,
  pending issuance/renewal recovery, owner isolation/fencing, outage and restore.
  Run focused pre-PR then equivalent full required CI and one final review;
  publish/merge leaf contracts and native changes before workspace gitlinks.
- [ ] 7. Use formal removal planner to inventory exact dev Linode resources,
  preserve local generation evidence, remove through formal script and verify
  provider absence. Never match prod/CI/shared resources by loose name.
  Full create preflight GO, formal new dev deployment and health acceptance.
  Repeat staging removal/create only after new dev acceptance. Fresh create
  must not allocate the former per-workload PKI PVCs.

## Ledger

2026-11-08: Approved implementation, original-key migration and per-service
restart; dev before staging. Primary checkout dirty: use clean existing worktree.
Moved ignored old test-run artifacts to GoBuild with compatibility symlink;
no environment operational records moved. Documentation target updated first.

2026-11-08 steering: user explicitly replaces migration with destructive fresh
creation for BOTH dev and staging. Do not develop the migration/rollback/retire
CLI from the former scope. Keep backend compatibility for other environments;
retain old local material segregated and never use it to initialize fresh PKI.
KV record RED observed (undefined methods); full openbao package -race GREEN
3.453s after implementation. Broader lifecycle acceptance remains pending.
