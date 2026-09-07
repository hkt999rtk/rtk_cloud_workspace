# Production PKI implementation ledger

This change implements the Platform PKI contract in dependency order. This file
records actual delivery status; unchecked items are not production capabilities.

## Accepted decisions

- Full production lifecycle, including bootstrap governance and recovery.
- Offline Device/App/Service Root and Brand custody; online Product keys in OpenBao.
- Explicit CA provisioning, separate from business-record creation.
- OIDC MFA step-up and distinct governance/custodian approval identities.
- Preserve staging identities through a bounded 90-day migration window.
- Recovery targets: RPO <= 15 minutes; RTO <= 4 hours, demonstrated by drills.

## Delivery checklist (implementation, not production qualification)

- [x] Durable issuer registry, independent approvals, atomic activation and public chains.
- [x] CSR/certificate exchange and existing-key Product provisioning reconciliation.
- [x] Account Manager PKI authorization/API with request-bound RS256 assertions.
- [x] Sealed one-time bootstrap, including existing installations and concurrent starts.
- [ ] Administrative recovery and last-administrator protection.
- [x] OIDC assurance and recent-authentication enforcement; refresh grants no new MFA.
- [x] Cloud Admin lifecycle UI, same-account OIDC MFA callback and public artifact export.
- [x] Offline encrypted Root/Brand key and CA signing CLI.
- [x] Dynamic certissuer selection, signed factory context and exact reservation validation.
- [x] Runtime issuer-to-product binding and explicit staging legacy fingerprint checks.
- [ ] Legacy inventory/import automation and staged migration.
- [ ] Certificate replacement, revocation and versioned trust distribution.
- [x] OpenBao Kubernetes login and projected-token reauthentication.
- [x] Explicit runtime/PKI schema migration; Product mode workloads skip startup DDL.
- [ ] OpenBao Raft deployment, scoped workload policies and database grants.
- [ ] Backup/recovery tooling and SDK installation support.
- [x] Focused unit, PostgreSQL and race-detector tests for implemented controls.
- [ ] Live provider end-to-end and hardware compatibility validation.
- [ ] Live staging migration, custody ceremonies and recovery qualification.

## Completion boundaries

Production CA keys must never be generated on this development workstation.
Custodian approvals, offline key backups, IdP assurance configuration, and live
recovery evidence must be real operational inputs, not test fixtures or flags.
Production remains disabled until all required acceptance evidence is present.

## Current code and tests

Work is isolated at `/private/tmp/rtk-production-pki`; the original workspace's
unrelated edits are preserved. Relevant service branches are
`codex/production-pki-hierarchy`. Nothing has been pushed, merged or deployed.

- Video Cloud: `internal/pki`, `internal/pkicontrollerapp`, `cmd/pkicontroller`,
  and opt-in factory/certissuer/API integration. Operator configuration and
  remaining gates: `repos/rtk_video_cloud/docs/production-pki-controller.md`.
- Account Manager: PKI roles migration, exact-role lookup, OIDC MFA step-up,
  request-bound controller proxy. Configuration:
  `repos/rtk_account_manager/docs/pki-controller-integration.md`.
- Go client: strict certificate-bundle parser accepts additive issuer metadata.

Validated against an isolated PostgreSQL 16 container, not any shared environment:

- Video Cloud affected packages: pki, openbao, certissuer, certissuerapp,
  factoryenroll, factoryenrollapp, httpapi, apiapp, config, and controller build.
- Race-detector coverage: registry idempotency, independent approvals, rotation,
  reservation pinning, ancestor compromise and HTTP bypass rejection.
- Account Manager authentication/API regression suites and PKI-role integration.
- Go SDK certificate-bundle authentication package.

The full proposed plan is not complete. Immediate next delivery stages are
administrative recovery, real custody/escrow evidence, remaining
trust domains, replacement and CRL publication, least-privilege deployment,
backup/restore, migration tooling, and live qualification. Production OIDC MFA
assurance and real approver/custodian account identifiers have been requested;
they are operational inputs and must not be invented.

## Continuation delivery

- Bootstrap startup now creates and seals once; it ignores retired bootstrap
  secrets thereafter. Existing or disabled administrators do not reopen bootstrap.
- PKI roles bypass the user cache so assignment revocation is immediately visible.
- Offline `pkiceremony` uses encrypted PKCS#8 files and validates independent
  request/CSR/parent digests, hierarchy, keys, validity and output preservation.
- Cloud Admin `/platform/pki` includes the lifecycle workflow and same-session,
  same-provider, same-user OIDC callback. Full console/backend client suites pass.
- Issuer and operation search has environment-scoped keyset pagination.
- Local tests also cover encrypted Root/Brand ceremonies, bootstrap concurrency,
  immutable sealing, console CSRF/state/subject rejection, and search isolation.

No production keys, live migration, deployment, push or merge has occurred.
The UI has backend and syntax coverage; live-browser and real-IdP qualification
remain outstanding. Full production scope remains unfinished as listed above.

Local checkpoint commits (not pushed): Video Cloud `345a541`, Account Manager
`ac3131b`, Cloud Admin `c0a3a8a`, and Go client `762137f`. The workspace checkpoint
pins these four implementations and leaves unrelated repository pointer updates
unstaged. Production remains disabled; this checkpoint does not complete the
full implementation plan.
