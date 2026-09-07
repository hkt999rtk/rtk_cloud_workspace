# Design document conflict audit and repair proposal

Status: supporting-note; documentation corrections implemented on 2026-09-07.

Owner: rtk_cloud_workspace.

Last reviewed: 2026-09-07.

All nine documented findings are corrected in this integration snapshot. The original
findings below preserve the pre-fix rationale; their line references refer to
the audited snapshot and may have moved in the corrected files. No service
runtime, database migration, deployment, credential, or payment behavior changed.

## Resolution record

| Finding | Applied correction |
| --- | --- |
| F1 | Global `app-user` examples aligned across contracts and Video Cloud; test-bundle targets now match existing `user` / `end_user` OpenAPI and Account Manager behavior. Issuer legacy capability and the broad application-default allowlist are explicitly distinguished from post-cutover policy. |
| F2 | SDK guidance uses authenticated factory enrollment and new `device` scope; deprecated renewal requires mTLS, and replacement renewal/helper coverage remains explicitly proposed. |
| F3 | Overview and authorization matrix route monetary ownership to Billing; identity/RBAC remain in Account Manager and charging gates remain separate. |
| F4 | Current API/outbox sequence is explicit; historical broker vocabulary and anchors are preserved in a non-normative section. |
| F5 | Runtime directory example excludes credential paths; environment-local SecretStore and current versus target OpenBao write authority are explicit. |
| F6 | One maintained cost mapping describes durable Redis and broker-free delivery; root mapping is a pointer. Dated prices and estimate quantities remain unchanged and are labeled as evidence. |
| F7 | SDK JWT claims use `iat` / `exp`; signed-token reissue, expiry recovery, and Account Manager's separate human refresh lifecycle are distinguished. |
| F8 | Shared clip wire details are stated in canonical media contracts; service design authority is limited to implementation internals. |
| F9 | Contracts README is an active index, LKE inventory is historical, and dependent references route current gates to active deployment/security/recovery guides. |

Runtime follow-up: the issuer application default still permits legacy tenant
subjects. The corrected issuer guide records the global-only post-cutover
allowlist and the separate compatibility/test review required before changing
that runtime default. Account Manager already uses global identities; this
pass does not claim to remove lower-level legacy issuer capability.

Target multi-cloud viewer scope, simulator-versus-backend OTA status, and
telemetry cleanup-on-ingest were also clarified without asserting a new rollout.
Known obsolete statements are checked by the existing `docs-check` command via
scoped consistency rules; historical documents are excluded from those rules.

Validation after corrections:

- `docs-check` and `contracts-check`: PASS.
- Focused design-consistency, contract-layout and SecretStore path/isolation tests: PASS.
- Existing Video Cloud app certificate issuance, missing-mTLS rejection and CSR mismatch tests: PASS.
- Newly added Markdown file links and whitespace checks across changed repositories: PASS.

Changes are delivered through service/contracts PRs and their dependent
workspace PR. The merged leaf changes are [contracts #155](https://github.com/hkt999rtk/rtk_cloud_contracts_doc/pull/155),
[Account Manager #325](https://github.com/hkt999rtk/rtk_account_manager/pull/325),
[Cloud Admin #388](https://github.com/hkt999rtk/rtk_cloud_admin/pull/388),
[SDK #560](https://github.com/hkt999rtk/rtk_cloud_client/pull/560), and
[Video Cloud #654](https://github.com/hkt999rtk/rtk_video_cloud/pull/654) plus
[review corrections #655](https://github.com/hkt999rtk/rtk_video_cloud/pull/655).
Review corrections encode clip authority in front matter, distinguish fleet
health routing from root telemetry cleanup, and document the SDK recovery
helper's unexercised signed-token reissue step. No deployment is included. Generated sequence-diagram
sources and binary exports were not edited; the corrected documents are not
inputs to that diagram PDF builder. The inventory retains pre-fix hashes and
adds post-fix hashes for changed audited files.

## Scope and evidence

The inventory covers 329 first-party Markdown files (87,396 lines): 73 workspace documents and 256 documents across all nine service/contracts repositories. Every inventoried file was scanned for cross-cutting identity, settlement, coordination, storage, credentials, authentication, and authority statements; candidate conflicts were then checked in context against narrower contracts, OpenAPI, ADRs, and selected implementation evidence. This is a cross-document consistency review, not a claim that every sentence or implementation is certified correct.

The [inventory](design-conflict-inventory-2026-09-07.json) records paths, line counts, content hashes, declared statuses, and topic matches. Mirrored contracts are counted once. Third-party sources, generated PDF exports, marketing translations, and operational test fixtures are outside the semantic review. Generated diagrams/PDFs should be refreshed after their source is corrected; binary parity was not checked.

Snapshot: workspace `39c016afa05e3b424209cbed831d865868831e69`; canonical contracts `6b29fa71e436cb75b5f4809cfd5d4d087151aecf`. This is the local pinned workspace, not a review of unmerged branches or live staging.

Authority follows [documentation governance](../documentation-governance.md): contracts own shared behavior, workspace policies own integration boundaries, and services own internal design. A newer timestamp alone does not override a normative contract. Target-state contracts must remain distinguishable from implemented and deployed behavior.

## Original findings and accepted corrections

### F1 — P1: New app certificates use two incompatible identity models

**Classification:** same fact, newer revision; internal inconsistency within the canonical contract repository.

**Evidence:**

- [repos/rtk_cloud_contracts_doc/auth.md:294](../../repos/rtk_cloud_contracts_doc/auth.md) requires `app-user:<user_id>` for the global human identity. [brand_cloud_admin.md:203](../../repos/rtk_cloud_contracts_doc/brand_cloud_admin.md) explicitly revokes legacy tenant certificates at cutover and permits only the global subject for new certificates.
- [repos/rtk_cloud_contracts_doc/api_usage.md:141](../../repos/rtk_cloud_contracts_doc/api_usage.md) and [provision.md:478](../../repos/rtk_cloud_contracts_doc/provision.md) still instruct Brand Cloud users to submit `app-brand-cloud-user:<brand_cloud_user_id>`.
- [repos/rtk_video_cloud/docs/auth.md:174](../../repos/rtk_video_cloud/docs/auth.md) and [cert-issuer-server-design.md:381](../../repos/rtk_video_cloud/docs/cert-issuer-server-design.md) repeat the two-subject issuance model; the latter also repeats it at line 482.

**Impact:** A client following the example can request a certificate incompatible with the unified identity cutover. Reviewers can also mistake tenant membership for a distinct certificate identity.

**Recommended source of truth:** canonical `auth.md` and `brand_cloud_admin.md` for post-cutover identity.

**Proposal:** Contracts owner updates API examples and provisioning text to the global subject. Video Cloud updates issuer validation guidance. Retain old subjects only in explicitly pre-cutover migration documentation, with rejection/revocation behavior stated. Do not rename historical audit identities or existing records as a documentation fix.

**Acceptance:** Fresh CSR examples use only the global identity. Contract/issuer tests distinguish a valid global CSR from retired tenant issuance. Separately reconcile the local/staging test-bundle vocabulary in [certificate_bundle.md:215](../../repos/rtk_cloud_contracts_doc/certificate_bundle.md) and [rtk_account_manager/docs/developer-pki-test-bundles.md:21](../../repos/rtk_account_manager/docs/developer-pki-test-bundles.md); that exception needs an explicit current identity mapping, not an inferred production exemption.

### F2 — P1: SDK PKI draft prescribes a different enrollment and renewal flow

**Classification:** same fact, newer revision; stale draft containing mandatory implementation instructions.

**Evidence:**

- [repos/rtk_cloud_client/docs/pki_device_auth.md:104](../../repos/rtk_cloud_client/docs/pki_device_auth.md) submits a CSR to `/api/device/provision_certificate`; canonical [openapi.yaml:164](../../repos/rtk_cloud_contracts_doc/openapi.yaml) defines `/v1/factory/enroll` with factory JWT/HMAC authentication. The proposed SDK enrollment path is absent from that OpenAPI.
- The SDK draft at line 226 says the SDK **must** request `camera`; canonical [auth.md:133](../../repos/rtk_cloud_contracts_doc/auth.md) prefers `device` for new integrations and retains `camera` for compatibility.
- The draft at line 254 describes renewal with a camera bearer token. Canonical [openapi.yaml:344](../../repos/rtk_cloud_contracts_doc/openapi.yaml) marks `/api/device/renew_certificate` deprecated and requires `mtlsDeviceAuth`.

**Impact:** Implementing the draft literally can select an uncontracted enrollment endpoint and the wrong renewal credential.

**Recommended source of truth:** canonical OpenAPI, `auth.md`, `provision.md`, and `platform_pki.md`; the SDK owns platform key handles and TLS integration only.

**Proposal:** SDK owner rewrites the draft into current enrollment/bootstrap behavior plus a separately labeled renewal proposal. Link to the authenticated factory bridge rather than suggesting that a bare CSR establishes manufacturing authority. Use `device` in new examples; label old camera helpers as compatibility. Describe deprecated renewal exactly as OpenAPI specifies, and leave any replacement renewal API pending a canonical contract decision.

**Acceptance:** Every executable PKI example maps to a declared route and security scheme. Unsupported or proposed platform/renewal features are explicitly marked. No new server endpoint is added merely to preserve an obsolete draft.

### F3 — P1: Billing is assigned to both Account Manager and RTK Billing

**Classification:** same fact, newer revision; internal inconsistency.

**Evidence:**

- [repos/rtk_cloud_contracts_doc/contract_overview.md:215](../../repos/rtk_cloud_contracts_doc/contract_overview.md) calls Account Manager the phase-one owner of commercial accounts, ledgers, payment methods, and top-up policy.
- [repos/rtk_cloud_contracts_doc/authorization.md:277](../../repos/rtk_cloud_contracts_doc/authorization.md) routes proposed commercial resources to Account Manager and says the permissions are unavailable until implementation.
- [repos/rtk_cloud_contracts_doc/billing_service.md:15](../../repos/rtk_cloud_contracts_doc/billing_service.md), [payments_and_balance.md:23](../../repos/rtk_cloud_contracts_doc/payments_and_balance.md), and workspace ADR [0001-commercial-settlement-ownership.md:28](../../docs/adr/0001-commercial-settlement-ownership.md) assign monetary runtime and persistence exclusively to `rtk_billing`.

**Impact:** Teams can choose the wrong upstream, service credential, database owner, or permission availability.

**Recommended source of truth:** `billing_service.md`, `payments_and_balance.md`, and accepted ADR 0001.

**Proposal:** Contracts owner corrects the overview and authorization table: Account Manager supplies global identity/membership/RBAC; Cloud Admin forwards trusted tenant context; Billing owns monetary resources. Link route and credential details to Billing's canonical boundary. Keep provider charging approval gates separate from whether Billing APIs are implemented.

**Acceptance:** No active document assigns monetary persistence to Account Manager. Billing authentication examples retain separate tenant/internal/debit credentials. Do not enable automatic charging as part of this correction.

### F4 — P1: Provisioning requires both a retired channel and direct APIs

**Classification:** internal inconsistency; historical architecture presented as current.

**Evidence:**

- [repos/rtk_cloud_contracts_doc/contract_overview.md:51](../../repos/rtk_cloud_contracts_doc/contract_overview.md) says provisioning uses the channel rather than direct server-to-server synchronization.
- [cross_service_channel.md:3](../../repos/rtk_cloud_contracts_doc/cross_service_channel.md) calls itself normative, but its service boundary at line 33 says the channel is historical and current coordination uses explicit APIs with database-backed outbox/retry.
- [provision.md:105](../../repos/rtk_cloud_contracts_doc/provision.md) specifies the outbox worker calling the authenticated Video lifecycle API and recording the result through Account Manager's inbox projection.
- Workspace [cross-service-broker-packaging.md:11](../../docs/cross-service-broker-packaging.md) removes NATS JetStream from the supported runtime. Both AWS mapping documents still call it the current default at line 65.

**Impact:** An integrator can add an unnecessary broker or wait for a coordinator that is not packaged, while missing the actual durable API delivery path.

**Recommended source of truth:** current provisioning flow and the explicit retirement decision.

**Proposal:** Split the channel document into current API/outbox delivery requirements and historical stream vocabulary. Correct the overview, dependent examples, and current cost/BOM statements. Preserve deliberately configured legacy adapters as explicitly scoped compatibility material; broker retirement does not mean every legacy adapter must be deleted.

**Acceptance:** One current sequence identifies producer transaction, authenticated receiver, idempotency, acknowledgement, retry, inbox projection, and dead-letter behavior. Current deployment requirements contain no NATS dependency. Existing provisioning retry/replay tests remain the behavioral evidence.

### F5 — P1: Secret storage layout contradicts current SecretStore policy

**Classification:** internal inconsistency; same fact, newer revision.

**Evidence:**

- [docs/secret-store.md:3](../../docs/secret-store.md) places local secrets in `~/.config/rtk_cloud/<environment>/`; lines 10–17 forbid shared fallback and reserve workspace runtime for non-secret state.
- [docs/cloud-env-layout.md:59](../../docs/cloud-env-layout.md) still shows `runtime/state/kubeconfig.yaml` and `runtime/secrets/` in the active runtime tree.
- [docs/storage-credential-lifecycle.md:14](../../docs/storage-credential-lifecycle.md) says credentials are environment-contained, but line 16 still describes a shared credential profile.
- [docs/deployment-secrets-governance.md:129](../../docs/deployment-secrets-governance.md) already puts kubeconfig under the external SecretStore's `kube/` directory.

**Impact:** Operators following the layout can place credential-bearing files in the wrong handoff/backup tree or recreate forbidden shared credential discovery.

**Recommended source of truth:** `secret-store.md` and the current local-root sections of deployment secrets governance.

**Proposal:** Workspace owner removes credential paths from the active runtime example and links to SecretStore's `operator/`, `runtime/`, `pki/`, and `kube/` paths. Rewrite “shared profile” as environment-local credentials for shared release infrastructure. Clearly separate current local bootstrap/recovery behavior from the OpenBao target state and identify which system is writable for each secret class.

**Acceptance:** Every active path example honors environment isolation; runtime evidence contains no credential material. Review examples against the existing SecretStore path tests. This audit did not inspect or relocate actual secrets.

### F6 — P1: Shadow storage is described as a disposable cache with PostgreSQL flush

**Classification:** same fact, newer revision; duplicated supporting documents lag the design.

**Evidence:**

- Both [docs/aws-service-mapping.md:61](../../docs/aws-service-mapping.md) and [docs/cost/aws-service-mapping.md:61](../../docs/cost/aws-service-mapping.md) include PostgreSQL shadow snapshots; line 64 describes a planned Redis cache with PostgreSQL flush.
- [repos/rtk_video_cloud/docs/device-shadow-hot-state.md:3](../../repos/rtk_video_cloud/docs/device-shadow-hot-state.md) makes Redis/Valkey the durable authority with no PostgreSQL hydration or flush worker.
- [repos/rtk_video_cloud/docs/device-shadow-spec.md:117](../../repos/rtk_video_cloud/docs/device-shadow-spec.md) repeats this requirement; [docs/backup-restore.md:46](../../docs/backup-restore.md) requires matched backup of durable documents/indexes/tombstones/outbox.

**Impact:** Deployment sizing and recovery plans derived from the mappings can omit durable Redis or assume PostgreSQL can reconstruct shadows.

**Recommended source of truth:** service Shadow durability design and workspace backup contract.

**Proposal:** Update storage roles, failure behavior, durability assumptions, and backup obligations in the cost mapping. Use `docs/cost/aws-service-mapping.md` as the maintained supporting note and reduce the root copy to a compatibility pointer after reconciling their differing content. Remove retired broker assumptions in the same pass. Do not recalculate prices without a separate dated pricing exercise.

**Acceptance:** The mapping says Redis is durable, no-TTL, fail-closed, with no PostgreSQL shadow fallback; it preserves the documented AOF every-second accepted loss window. Backup documentation remains consistent. Historical cost estimates remain dated evidence rather than silently rewritten history.

### F7 — P2: SDK token documentation mixes old claims and refresh terminology

**Classification:** internal inconsistency; naming mismatch with implementation consequences.

**Evidence:**

- [repos/rtk_cloud_client/docs/auth.md:130](../../repos/rtk_cloud_client/docs/auth.md) lists `issued_at` and `expires_at` as access-token claims. Canonical [auth.md:84](../../repos/rtk_cloud_contracts_doc/auth.md) uses JWT `iat`/`exp` and explicitly requires scheduling from the returned `exp`.
- SDK [pki_device_auth.md:185](../../repos/rtk_cloud_client/docs/pki_device_auth.md) describes rotating a refresh token, while canonical [auth.md:105](../../repos/rtk_cloud_contracts_doc/auth.md) and SDK [auth.md:66](../../repos/rtk_cloud_client/docs/auth.md) define reissuing a still-valid signed token through the legacy `refresh_token` request field.
- Video Cloud's [docs/auth.md:121](../../repos/rtk_video_cloud/docs/auth.md) says device mTLS issuance does not emit a separate refresh-token field.

**Impact:** Clients may look for nonexistent claim names, wait for a refresh credential not returned by device bootstrap, or assume expired tokens can be reissued.

**Proposal:** SDK owner uses `iat`/`exp`, describes signed-token reissue consistently, and distinguishes Video Cloud token handling from Account Manager's separate human refresh-token lifecycle. Expired device access tokens return to verified mTLS bootstrap.

**Acceptance:** Example parsing and renewal timing use actual JWT claims, including server TTL jitter. Existing expiry, subject-preservation, and mTLS recovery tests verify the examples' assumptions.

### F8 — P2: Clip design claims authority over shared wire contracts

**Classification:** internal inconsistency in source-of-truth governance.

**Evidence:**

- [docs/documentation-governance.md:24](../../docs/documentation-governance.md) and [docs/architecture.md:21](../../docs/architecture.md) make the contracts repository the sole normative shared-contract source.
- [repos/rtk_video_cloud/docs/clip-direct-upload-design.md:11](../../repos/rtk_video_cloud/docs/clip-direct-upload-design.md) calls itself the root of fact and says it wins when another document disagrees, including API descriptions and camera firmware.
- Canonical [snapshot_and_media.md:199](../../repos/rtk_cloud_contracts_doc/snapshot_and_media.md) delegates direct-upload behavior to that service-local design.

**Impact:** There is no unambiguous reviewer authority if the service design and shared OpenAPI disagree. This is an authority conflict; the reviewed direct-upload flows themselves currently agree.

**Proposal:** Keep internal uploader/verifier/storage design in Video Cloud. Move or explicitly restate externally observable routes, security, payloads, checksum requirements, states, and errors in canonical contracts. Narrow the service document's precedence to implementation internals and link back to the contract. Alternatively, any deliberate governance exception needs an explicit workspace decision; the recommendation is to retain the existing single-contract-source policy.

**Acceptance:** SDK/firmware teams can implement the wire protocol from contracts without a competing service-local override. OpenAPI and shared examples agree with the internal design.

### F9 — P2: Document status and release scope are not consistently declared

**Classification:** internal inconsistency in metadata; not proof of a runtime defect.

**Evidence:**

- Contracts [README.md:3](../../repos/rtk_cloud_contracts_doc/README.md) describes the whole repository as draft, while its source-of-truth section and many individual documents are normative/active.
- Workspace [docs/README.md:117](../../docs/README.md) places the LKE inventory under historical evidence, but [lke-migration-inventory.md:3](../../docs/lke-migration-inventory.md) calls itself current and line 11 calls itself the source-of-truth gate checklist.
- `cross_service_channel.md` mixes a normative status with an explicitly historical runtime, as detailed in F4.

**Proposal:** Use separate fields for document status and runtime evidence: for example, `Status: normative`, `Applies to: target release`, `Implementation: partial`, and a link to dated deployment evidence. Add explicit supersession links. Make the contracts README an active index with per-document status, and choose one consistent classification for the LKE inventory's current versus historical sections.

**Acceptance:** Index status agrees with the document. A planned contract is never automatically reported as deployed, and a historical plan does not impose an unexplained current release gate.

## Design transitions that need follow-up, not an automatic conflict verdict

- **Multi-cloud viewers:** [multicloud_ownership.md:73](../../repos/rtk_cloud_contracts_doc/multicloud_ownership.md) grants scoped read access without an extra Product assignment, while Admin [roles.md:184](../../repos/rtk_cloud_admin/docs/roles.md) describes assignment-based Product visibility. The multi-cloud document explicitly describes a target release. Add a target-release applicability note and role mapping; do not infer that today's runtime is wrong or grant all-Product visibility during documentation cleanup.
- **Product OTA:** [firmware_campaign.md:10](../../repos/rtk_cloud_contracts_doc/firmware_campaign.md) is proposed, while the OTA load-test design says its simulator is implemented. An implemented simulator does not establish backend rollout or production qualification. Link each status to the tested routes, pinned backend commit, and evidence rather than promoting the contract from a load-tool claim.
- **Telemetry retention:** Video Cloud's client guide promises 90 days; its schema inventory says there is no scheduled cleanup. Inspection of `internal/producttelemetry/telemetry.go:133` shows cleanup during ingestion. These can coexist. Clarify that trigger and whether a hard deletion deadline is required during idle periods; this is not presently counted as a contradictory retention value.
- **WebRTC scope:** Cloud Client's signaling-only ownership and Ameba WebRTC's media-engine ownership refer to different repositories. They are compatible. Likewise, exported local/staging test keys do not contradict the separate production non-exportable-key requirement.
- **Future deployments:** Reserved EKS/GKE adapters are not proof of a working deployment. Preserve fail-before-mutation statements and do not turn provider-neutral intent into an availability claim.

## Proposed implementation sequence

| Wave | Owner repositories | Concrete changes | Exit criteria |
| --- | --- | --- | --- |
| 1: Correct shared contracts | Contracts, then Account Manager/Video Cloud references | F1, F3, F4; resolve F8 authority wording | One identity model, monetary owner, provisioning sequence, and public-contract authority; matching OpenAPI and examples. |
| 2: Correct consumers and operations | Cloud Client, Video Cloud, workspace | F2, F5, F6, F7 | SDK routes/auth align; external SecretStore paths and durable Shadow storage are consistent; duplicate mapping becomes a pointer. |
| 3: Clarify applicability and prevent recurrence | Workspace and document owners | F9 and target-state follow-ups | Current/target/historical boundaries and evidence links are explicit; generated exports refreshed after source changes. |

Use documentation-only changes where accepted contracts already settle the answer. If implementation inspection reveals a behavioral mismatch, open a separately scoped owner-repository fix with contract-level acceptance tests. No database migration, credential rotation, broker deployment, or charging change is implied by this proposal.

For prevention, extend existing documentation checks with a small maintained set of scoped assertions: retired identity issuance, Account Manager monetary ownership, mandatory NATS in active runtime descriptions, PostgreSQL Shadow flush, and workspace credential paths. Exempt explicitly historical sections rather than banning these strings globally. Reuse existing OpenAPI/schema and conformance checks; do not build a second documentation database or duplicate contract definitions.

## Validation performed

- `go run ./scripts/go/rtk-cloud -- docs-check`: PASS.
- `go run ./scripts/go/rtk-cloud -- contracts-check`: PASS; all four consumer contract paths resolve to the same canonical checkout.
- Checked conflict evidence against local source lines and selected OpenAPI/security definitions.
- No runtime tests, live deployment inspection, upstream fetch, or remote issue/PR creation were performed. Passing structural checks does not establish semantic agreement.

The validation above records the original audit. See the resolution record at
the top for corrections and subsequent checks; runtime implementation remains unchanged.
