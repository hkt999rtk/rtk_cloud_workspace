# Deployment Secrets Governance

Status: active workspace source document for deployment secret handling.

Owner: rtk_cloud_workspace.

Last reviewed: 2026-10-12.

This document defines how RTK Cloud deployment secrets, keys, certificates,
runtime environment files, bootstrap tokens, and operator state are organized.
It is a governance document only; it must not contain secret values.

## Goals

- Keep deployment secrets out of git, PR descriptions, issue bodies, logs, and
  readiness reports.
- Separate staging and production secret material.
- Keep the current Linode deployment shape while preserving future AWS/GCP
  migration paths.
- Give deployment scripts a stable way to locate secrets without hard-coding
  service-specific local paths.
- Standardize staging and production on OpenBao as the runtime secret source of
  truth, while retaining local files only for bootstrap, local development, and
  rollback.
- Standardize production-like LKE deployments on OpenBao or an approved
  customer secret manager, with Kubernetes Secrets limited to synchronized
  runtime material and local files retained only for bootstrap, local
  development, and rollback.

## Canonical Local Secret Root

### Configuration versus deployment-generated data

This is a required workspace boundary, independent of whether data is secret.
`cloud_env/` contains configuration only: maintained environment intent,
overrides and deterministic secret-free resolved configuration. Operational
deployment outputs are never configuration merely because they are redacted.

All deployment-generated local data lives beneath
`~/.config/rtk_cloud/<environment>/`. This includes tokens, credentials, private
keys, certificates and chains, kubeconfig, allocated cloud IDs/provider state,
receipts/proofs, journals, execution locks, deployment reports, rollback/restore
material and temporary deployment files. Use `0700` directories and `0600`
files. Code must use the selected environment's shared path resolver; it must
not invent another workspace, worktree, external-disk or OS-specific location.

| Local material | Environment-relative location |
| --- | --- |
| Operator credentials | `operator/env/` |
| Explicit new-generation bootstrap operator inputs | `operator/env/ACCOUNT_MANAGER_BOOTSTRAP_PLATFORM_ADMIN_EMAIL`, `operator/env/ACCOUNT_MANAGER_BOOTSTRAP_PLATFORM_ADMIN_PASSWORD` and `operator/env/PKI_OPERATOR_SIGNER_REF` (operator preparation supplies initial account/signing inputs as 0600 files; create preflight validates the frozen inputs, admitted native bootstrap consumes them; no automatic historical credential fallback, account creation or successful-ceremony claim during preparation) |
| Initial selected deployment SSH identity | `operator/ssh/id_ed25519` and `operator/ssh/id_ed25519.pub` (operator explicitly supplies the new pair or invokes `secrets init-deployment-ssh --environment NAME --confirm video-cloud-NAME`; create qualification verifies exact matching ownership and plan public-key digest, native edge/TURN management consumes the private key; preflight never generates/replaces it, both files 0600) |
| Fresh-create qualification | `deployment/create/preflight/<plan-sha256>/qualification-*/artifact.json` and `deployment/create/preflight/active.json` (create preflight produces immutable maintained plan, native gate hashes and complete exact formal CI manifests/proofs with expiry; execution consumes the attempt also selected by shared `deployment/preflight/active.json`; no future UID, private key or fabricated receipt) |
| Fresh-create native allocation/stage journal | `deployment/create/executions/<plan-sha256>/execution-*/journal.json` (admitted exclusive executor writes request intent before mutation and original native receipts afterward; recovery/removal consume, uncertain outcome retains the fence and blocks repeated allocation) |
| Provider-only dev create recovery qualification | `deployment/create/recovery/<review-sha256>/qualifications/<UTC-time>.json` and `qualification.json` (read-only recovery qualification preserves each immutable six-gate proof before replacing the current admission artifact; original owner/journal/kubeconfig digests, exact formal release, updated frozen inputs and one-hour expiry; explicit recovery execution consumes current artifact; renewal requires all live gates again, expired original GO is provenance only, 0700/0600) |
| Explicit new-generation storage keys | `deployment/create/recovery-storage-keys/<original-plan-sha256>/<media|ota>/{original/<credential-name>,create-intent.json,native-key-response.json}` and sibling `verified-promotion-intent.json`, `completed.json` (explicitly confirmed provider-only dev recovery preparation preserves original credential bytes, writes exact one-bucket read_write request intent and privately saves the one-time secret response before validation; real private ACL/policy and canary checks precede guarded promotion to `operator/env/`; retry consumes preserved responses and rejects uncertain issuance or unrelated current values; original provider keys are retained, no secret values in completion reports, 0700/0600) |
| Original owner snapshot during provider-only recovery | `deployment/create/executions/<original-plan-sha256>/execution-*/owner-recovery-handoff/{owner.json,running.lock}` (qualified stopped-executor recovery preserves exact original bytes before atomically changing the active owner while keeping its original OS lock held; diagnosis consumes this inactive snapshot, never as evidence of released ownership or completed deployment, 0700/0600) |
| Recovered create journal | `deployment/create/executions/<review-sha256>/execution-*/journal.json` (recovery produces original journal reference/digest, retained authentic completed provider receipts and original host-material plan reference, then records new bootstrap intents/results; execution, recovery/removal and diagnosis consume; failed original journal remains unchanged, 0700/0600) |
| Storage grant diagnosis and temporary probe credentials | `deployment/repairs/storage-key-grant-<UTC-time>-<unique>/` (authorized repair records exact one-bucket read-only key request before issuance, privately persists the native response and key ID, and records sanitized genuine ACL/policy/list readback and exact-ID revocation outcome; diagnostic cleanup consumes its own key ID only, original credentials remain unchanged; 0700/0600, no credential values in reports) |
| Admitted fresh-create child plans | `deployment/rollback/<child-plan-sha256>/plan.json` and `journal.json` (ordinary Kubernetes executor retains exact private resource bodies and original owner/acceptance evidence for guarded recovery; TLS Secret bodies may contain same-owner exported serving keys, so these are private 0600 deployment data in 0700 directories, never public reports or automatic replacement identity inputs; parent stage intent contains only plan/resource hashes and IDs) |
| Fresh-create temporary transport and public ceremony evidence | `deployment/create/material/<plan-sha256>/<role>-transport/` and `deployment/create/<plan-sha256>/bootstrap/` (executor produces bounded temporary transport for original foundation jobs and native public ceremony/session/binding observations; original stage execution/recovery consume; temporary material contains private keys and remains 0600, public evidence does not contain native owner serving keys) |
| Fresh-create durable ingress admission transport | `deployment/create/material/<plan-sha256>/ingress-admission/identity.json` (only the admitted, fenced parent generates the original P-256 server key/leaf and dedicated public CA once under its frozen admission profile; guarded Secret publication consumes it; the native ingress controller consumes the immutable original Secret and the API server consumes the actual webhook CA; native readback records original parent-stage UIDs and key/certificate/CA/material hashes in the parent journal; 0700 directories/0600 file, no preflight generation, missing recorded material cannot regenerate identity) |
| Fresh-create active original issuer lineage | `pki/<domain>/issuer.json` (native ceremony executor publishes the verified active root/intermediate metadata after authenticated consumer receipts; ordinary PKI/removal tooling consume the original public lineage, never an invented activation receipt) |
| Fresh-create explicit internal App routing allocation | `pki/app/routing.json` (admitted fresh executor writes schema `rtk-cloud.native-app-routing/v1` only after the actual App Service, Deployment and immutable `<stack>-video-cloud/pki-create-app-routing` ConfigMap are allocated; contains their original UIDs, selected environment/stack, explicit `internal-service` mode and exact parent plan digest; canonical observation consumes the private 0600 record with the live immutable declaration and owners, rejecting missing/replaced evidence rather than inventing a public host or regenerating ownership) |
| Runtime secret mirrors | `runtime/` |
| Independent VideoStorage credential mirror | Existing `operator/env/LINODE_MEDIA_OBJ_ACCESS_KEY_ID` and `operator/env/LINODE_MEDIA_OBJ_SECRET_ACCESS_KEY` are frozen inputs to the common runtime dependency producer; it publishes only `AWS_ACCESS_KEY_ID` and `AWS_SECRET_ACCESS_KEY` in Kubernetes Secret `<stack>-video-cloud/video-storage-object-storage`, consumed only by the independent VideoStorage container. The original shared `video-cloud-runtime` Secret, all its fields and UID remain under their existing lifecycle. Preflight generates no keys; existing mirror UID/key drift and existing consumer reference changes block ordinary deployment. |
| Per-relay Coturn CLI password | `deployment/turn/<relay-name>/cli-password` (Coturn VM deployment installer produces/consumes; new allocation generates once, retries reuse, upgrades retain verified original runtime password; never generated by preflight or generic catalog repair) |
| Kubernetes identity | `kube/` |
| PKI identities/certificates | `pki/` |
| OpenBao controller material | `openbao/` |
| Segregated previous deployment generation | `deployment/archives/<generation>/` (explicit new-generation preparation preserves original OpenBao/PKI/runtime/inventory/bindings bytes and produces a manifest of original paths and content hashes; explicit validated restore consumes it, ordinary preflight/bootstrap never search it as a fallback; 0700 directories/0600 files, no secret values in reports, no provider-removal or successful-deployment claim) |
| Plans, generated preflight observations and execution evidence | `deployment/` |
| Active execution owner and local process lock | `deployment/.execution-active/owner.json` and `running.lock` (executor produces; preflight inspects occupancy and original-controller reconciliation consumes owner/process evidence; successful Lease cleanup archives this directory on success, failure, cancellation or timeout, while failed cleanup retains it) |
| Released execution owner/process evidence | `deployment/rollback/<plan-sha256>/execution-owner-*/owner/` (executor archives each attempt's original active directory to a unique private directory only after verified Lease release while holding its process lock; retained for outcome inspection) |
| Original execution plan and resource/acceptance journal | `deployment/rollback/<plan-sha256>/plan.json` and `journal.json` (executor produces and retains after success/failure; original-owner reconciliation consumes; never overwritten or used for automatic continuation) |
| Accepted all-`retain` plan revalidation receipts | `deployment/recovery/revalidated-<unique>/verification.json` (executor consumes the original accepted plan/journal and current qualified proof, then produces plan/original-journal digests, exact Lease owner, resource count, UTC verification time and accepted revalidation outcome after immediate checks, fences and acceptance; operators consume the receipt for verification/recovery inspection; cleanup remains separately required and archives the owner under the original rollback plan, no resource writes or original plan/journal overwrite, 0700 directories and 0600 files) |
| Scoped partial-deployment recovery evidence | `deployment/recovery/<attempt>/` (operator recovery produces current resource/PVC-owner snapshots, reviewed guarded patches, dry-run/write results, original Lease owners and sanitized outcomes; reconciliation consumes them independently of the retained original execution journal; 0700 directories and 0600 files, no credential values in reports) |
| Generated preflight owner/identity observations and qualification | `deployment/identity-bindings.json` (preflight writes NO-GO before checking; complete qualification writes GO with plan/bindings hashes; deployment verifies the matching record and fresh live observations; issuer runtime fields contain only original Secret names, with container paths derived from recorded read-only mount/item mappings; ordinary existing-worker observations may include the validated original MQTT worker UID and exact self/enrollment placement affinity, consumed by the shared renderer without copying identity bytes; ordinary cleanup observations select the current Controller-mounted immutable record under its verified original namespace/API CA lineage) |
| Immutable preflight qualification artifacts | `deployment/preflight/<plan-sha256>/qualification-<unique>/{proof.json,plan.json,release.json,bindings.json}` (complete preflight produces exact private resource bodies, frozen release/owner observations and `rtk-cloud.preflight-proof/v1` with invocation generation, checker/policy, input/cluster/Kubernetes identity, gate native-response/CI provenance hashes, exact formal manifests, qualification/expiry and earlier PKI timestamps; deployment consumes the exact active attempt and operators inspect retained evidence; raw probe diagnostics/payloads are not observation content, no hand-authored GO, 0700 directories and 0600 files) |
| Active preflight authorization pointer | `deployment/preflight/active.json` (`rtk-cloud.preflight-active/v1`; preflight invalidates to NO-GO with its generation before new checks and publishes GO with exact attempt/proof hash only after immutable files and matching bindings succeed; deployment verifies expiry at load and execution admission, then fences the selected generation/pointer/hash throughout the admitted attempt without an age-based execution cap; no search for older attempts or binding-only GO fallback; cancellation/expiry/commit failure revokes only that producer's generation, 0700 parent directories and 0600 file) |
| Preflight generation publication lock | `deployment/preflight/.publication.lock` (preflight invalidation, initial/compiled NO-GO observations and final bindings-GO/active-GO writers consume a short nonblocking OS lock and compare the active generation before writes; stale producers cannot overwrite newer authorization/invalidation, and canceled artifacts remain inactive history; 0700 parent directories and 0600 lock file) |
| Historical adoption candidates | `deployment/identity-bindings.candidate.json` (retired input; never automatically promoted or consumed) |
| Account Manager bootstrap retirement guard | `deployment/account-bootstrap-retired.json` |
| Storage preparation/completion receipts and migration proofs | `deployment/storage/` |
| Runtime dependency access correction | Original execution `normal-tail-stages/runtime-dependency-access.json`, parent journal entry and standard `deployment/rollback/<child-plan>/` retain the finite secret-free NetworkPolicy intents and genuine owners. The same-parent executor produces these private operational records; recovery, phase fences and final-target compilation consume them. No credential, identity or separate admission is created. Directories 0700, files 0600. |
| Interrupted workload forward correction | Original execution `normal-tail-workload-forward.json` and the ordinary forward child records preserve and bind the original target, failed journal and exact corrected private workload target. The normal executor produces the forward child journal and composite logical-workload receipt; ownership, volume, recovery and final-verification readers authenticate both histories. Original plans/journals, keys, Secrets and PVC identities remain unchanged. Directories 0700, files 0600. |
| Approved remaining-duration caller selection | `deployment/repairs/<repair>/caller-window.json` is private operational data, exclusively written by the approved launcher and read on every retry. It binds the proposal, original admission, selected start and fixed total/deadline; it is explicitly not a program admission. Preserve incomplete or expired records for diagnosis rather than selecting a new clock. Directories0700, files0600. |
| Product session expiry recovery | `deployment/create/<parent>/bootstrap/product-identities/<scope>/service/session-renewal/{source,observed,completed,cleanup-source,cleanup-completed}.json` binds the expired original session, original key/CSR hashes and same permanent product owners to a genuinely produced distinct temporary session. Producer, effective-session resolver, cleanup and final-verification readers verify the exact source, accepted children, genuine native tombstone and the exact original Secret/ConfigMap UID-and-resource-version deletion intents and observed-absence receipts. A missing object without an earlier matching pending intent is not cleanup evidence. On the original PVC, renewal uses a distinct public-certificate path; finalization preserves the old public certificate before native cleanup removes its exact temporary key/CSR. Original private keys/CSR remain on their existing PVC until genuine finalization; permanent identity bytes and earlier session records are preserved. Directories0700, files0600. |
| Product session workload successor | Original execution `normal-tail-product-session-forward.json` and ordinary child journals bind the unchanged started zero-write forward to an exact ACK-session/cleanup correction. Shared ownership/allocation/recovery and independent final readers require genuine accepted successor evidence and explicit failed-source supersession; no historical child is relabeled accepted. Directories0700, files0600. |
| Rejected unstarted workload input | Original execution `rejected/<raw-sha256>.json` preserves the exact private derived descriptor before its source-derived correction. The fenced correction producer writes it; recovery and audit readers verify it. A parent intent, child input/rollback directory or matching lease prevents this correction. Original native plans/journals, admission and deadline remain unchanged. Directories 0700, files 0600. |
| Product steady cleanup and preparation retirement | Original execution `normal-tail-stages/product-identities-<scope12>-{steady,producer-retire}.json` for continuation, or `stage-inputs/fresh-product-{steady,producer-retire}.json` for fresh create; existing stage executors produce immutable inputs and ordinary `deployment/rollback/<child-plan>/` journals. Recovery and final-target consumers authenticate original workload/preparation UIDs and actual deletion outcomes. Private identity PVCs and permanent Secrets remain; no replacement identity, receipt, authority or clock is generated. All directories 0700 and files 0600. |
| OTA allocated-bucket rendering observation | `deployment/storage/ota-render-observation.json` (formal compiler produces public selected-environment/provider/bucket/region/endpoint/type metadata plus maintained prefix, stable SHA-256 and observation time after a genuine read-only existing-bucket GET; private snapshot and OTA-only renderer consume validated metadata/hash, with observation time excluded from the plan digest; 0700 directories/0600 file, no keys, canary result or activation receipt) |
| VideoStorage original-media rendering observation | `deployment/storage/media-render-observation.json` (formal compiler produces public selected-environment/provider/bucket/region/endpoint/type metadata plus maintained prefix, stable SHA-256 and observation time after a genuine read-only existing-bucket GET; private input snapshot/VideoStorage renderer consume validated metadata/hash and execution qualification requires a matching fresh GET; 0700 directories/0600 file, timestamp excluded from plan digest, no credential, object-write result or lifecycle receipt) |
| Provider operational state | `deployment/adapters/<adapter>/` (LKE uses `state.env` and `account.env`) |
| DNS ownership, ACME challenges, zone evidence and synchronization metadata | `deployment/dns/<adapter>/` (`ownership.json`, `challenges.json`, `state.json`, `dns.env`; DNS synchronization and challenge handlers produce, shared deployment/removal consumes; removal retains original ownership for retry and validates current values before declaring removal) |
| Reviewed current-state DNS recovery receipt | `deployment/dns/<adapter>/reconciliation.json` (`dns-reconcile-state --confirm STACK --review-sha256 SHA256` publishes exact reviewed Linode numeric-ID/role/address and managed A-record mappings plus hashes; ownership's `reconciliation_sha256` binds the receipt; removal validates receipt/ownership/current mappings and retains both for retries after DNS/provider absence; original evidence remains unchanged, no historical ownership or ACME cleanup proof is fabricated, 0700 parents/0600 files) |
| Original DNS migration sources | `migration-backup/dns-state/<source-sha256>/` |
| Original provider migration sources | `migration-backup/provider-state/<source-sha256>/` |
| Controlled teardown state/credential archive | `migration-backup/destroy-lke-<operation-id>/` (Linode teardown archives the original `deployment/adapters/lke/state.env` and `kube/kubeconfig.yaml` when present; operators consume for partial-removal diagnosis and explicit recovery; the archive is not proof of final provider absence or volume ownership) |
| Removal PV ownership evidence | `deployment/remove-volume-ownership.json` (schema 1: unified removal records selected environment, stack, canonical kubeconfig SHA-256, original LKE cluster ID when available and volume IDs captured from PVs before workload deletion; schema 2: explicit `reconcile-removal-volumes --confirm STACK --review-sha256 SHA256` publishes `method=linode-event-recovery`, `scope=qualified-original-PVC-inventory`, reviewed payload hash, original source hashes/preflight generation, declared cluster/Kubernetes bindings, native event IDs and exact qualified PVC/volume mappings after read-only validation; removal/retries revalidate scope, source/generation/current identity and consume exact IDs for detached-volume cleanup and verification; retain after partial failure, 0700 parents/0600 file, no guessed IDs, cross-generation merges or overwrite of a different original record) |
| Post-cleanup runtime correction | `deployment/create/executions/<parent>/execution-*/normal-tail-post-cleanup-correction-{source,prepared,completed}.json`, associated `normal-tail-stages/post-cleanup-runtime-correction-{prepare,apply,retire}.json` and ordinary `deployment/rollback/<child-plan>/` (the admitted same-program producer saves source/owner bindings and actual preparation/completion proofs; recovery, final target and independent readers consume them; original CA, keys, PVCs, profiles and historical receipts are retained, 0700/0600) |
| Tested post-cleanup executable promotion | `deployment/repairs/global-flow-<date>/postcleanup-qualified-<source>/` and immutable continuation/delegated-window records beside it (operator preparation retains source preimages, exact candidate/checker, complete recovery-test and independent replay evidence plus failed actual reports; the continuation launcher consumes the selected qualified manifest. Promotion selects the checker last and creates no native completion or program admission.) |
| Normalized provider region/quota metadata (not completion evidence) | `deployment/provider-preflight.env` |
| Temporary native Kubernetes / Docker image-pull / Buildx client identity and state | `deployment/tmp/` (operation-owned directories removed on success/failure; release qualification and rollout image pulls consume only selected `operator/env/GHCR_PULL_USERNAME` and `operator/env/GHCR_PULL_TOKEN`, produce private `image-pull-*/config.json` without ambient credential helpers, and clean up the process group and private directory on completion/cancellation) |
| Deployment reports | `deployment/reports/` (deployment/check producers and operator diagnosis consumers; optional `reconcile-removal-volumes --report` writes sanitized review JSON here with source hashes, declared binding/scope and exact reviewed mappings; optional `dns-reconcile-state --report` writes sanitized current-state DNS/provider mappings and review digest here; reports are not ownership publication or deployment authorization, 0700 directories and 0600 files) |
| Sanitized deployment progress and timing logs | `deployment/logs/<action>-<UTC-time>-<unique-id>.log` (unified preflight/deployment CLI and shared progress/acceptance reporters produce; operators/diagnosis consume; one new 0600 file per attempt in 0700 directories; no raw subprocess output, command arguments, resource bodies or credentials) |
| Deployment gate/probe measurements | `deployment/logs/metrics-<UTC-time>-<unique-id>.log` (unified CLI and initialized-create recovery produce diagnostic JSON Lines; operators/performance analysis consume start/end times, elapsed milliseconds, instrumented query-attempt counts and Kubernetes read-cache/immutable CI content hits; 0700 directories/0600 files; no cache keys/values, request bodies, command arguments, tokens or raw errors; separate from qualification digests) |
| Managed public registration CRL profile | `operator/env/ACCOUNT_MANAGER_SERVICE_REGISTRATION_CRL_REFRESH_PROFILE` (`managed-public-pem-v1`) and `operator/env/PKI_MANAGEMENT_IMAGE` (qualified immutable dev image) select the optional one-owner profile; separate `operator/env/PKI_CONTROLLER_IMAGE` retains the qualified dev Controller-only image role. The existing `pkimanagement` producer writes verified public Root/retiring/active CRLs atomically to `/run/account-pki/private/service-registration.crl.pem` on the shared `pki-socket` `emptyDir`; Account Manager consumes this fixed public file, UID/GID 10001 and mode 0600. The initializer projects only original listener Secret `client.crl` as a startup compatibility seed, never private keys or a freshness fallback. Ordinary scoped `deployment/rollback/<planSHA>/{plan,journal}.json` and a follow-up run record linking the new FULL-health report preserve update/retry proof. No CA/private identity creation, app identity-PVC mount or new historical selection format; canonical local evidence uses 0700/0600. |
| Registration listener CRL and original consumer-policy repair evidence | `deployment/repairs/service-registration-crl-<YYYYMMDD>[-<unique-id>]/` (reviewed operator repair produces original/published public CRLs, sanitized reviewed/outcome/lease records, its guarded helper, and a private original Controller Deployment snapshot when policy repair is included; operators consume these for diagnosis/recovery, Account Manager consumes the original Secret's updated `client.crl`, and the original Controller consumes the reviewed policy) |
| Dev original transport Root CRL recovery evidence | `deployment/repairs/root-crl-<YYYYMMDD>[-<unique-id>]/` (reviewed operator renewal records public original issuers/CRLs, complete requests and signed CRLs, native dry-run/import outcomes, helper and execution-lease records; diagnosis/recovery consumes the archive, while the native dev-only importer publishes the exact signed CRL to the registry for original consumers; existing encrypted Root keys and passphrases stay at their original custody paths) |
| Core API original MQTT management identity restoration | `deployment/repairs/core-mqtt-management-<unique-id>/` (reviewed operator repair produces original Deployment/PVC snapshots, a read-only native inspection manifest and public result, reviewed environment-field patch, guarded helper and ownership/cleanup/outcome records; operators consume recovery evidence and the original API consumes its restored managed identity bindings and derived trust/renewal configuration; private keys remain on the original Service identity PVC) |
| CertIssuer route migration restore journal | `deployment/journals/certissuer-ingress/certissuer-<operation-id>.json` |
| LKE provision operational summary | `deployment/artifacts/lke-provision/<operation-timestamp>/summary.json` |
| Temporary deployment material | `deployment/tmp/` |
| Zammad native renderer copies/client state | `deployment/tmp/zammad-*` (common compiler produces/consumes frozen chart and values copies; removed on success/failure, 0700 directories and 0600 files) |
| Protected original migration/rollback snapshots | `migration-backup/` |

Removal copies the validated canonical kubeconfig into an operation-owned private
directory under `deployment/tmp/` for PV capture and Kubernetes purge. The
temporary identity uses 0700 directories and 0600 files and is removed on exit;
it is not another permanent kubeconfig location. Original DNS ownership and
outstanding ACME challenge evidence stay in `deployment/dns/<adapter>/`. Removal
blocks when neither original ownership nor a validated explicitly reviewed DNS
reconciliation is available, or challenges remain, and never fabricates an empty
ownership or cleanup receipt.

DNS current-state reconciliation uses the selected canonical environment store.
Default review performs only authenticated provider/DNS reads; an optional
absolute report path must remain beneath its `deployment/reports/` directory.
Exact stack confirmation and the approved review digest authorize publication
of declared current-state evidence, not fabricated deployment history. Publish
preserves original legacy/canonical evidence and changes no DNS or credentials.
Ordinary removal consumes the validated digest-bound ownership/receipt and
rechecks exact current mappings before deletion. Keep the receipt after partial
or complete DNS cleanup so retries can recognize absent reviewed targets.

Removal-volume recovery reads the explicitly selected original immutable
`deployment/preflight/<plan-sha256>/qualification-<unique>/` artifacts and
`migration-backup/destroy-lke-<operation-id>/` kubeconfig and retained provider
evidence in the same environment store. These sources remain unchanged. Default
review performs no publication or provider deletion. Confirmed publication
creates only the declared schema-2 ownership record with provenance; ordinary
removal consumes it for the original qualified PVC inventory and preserves
unmatched volumes. A historical qualification is lineage evidence for this
recovery, not fresh deployment authorization.
If original archived provider state is missing, `cluster_binding` declares
`explicit-reviewed-node-set`; exact stack confirmation and the reviewed payload
digest record the operator's review of the explicit original cluster/node set.
This does not create a provider-state file or claim that the missing original
capture existed.

Preflight admission validity comes from maintained
`DEPLOYMENT_PREFLIGHT_VALIDITY_SECONDS`. Deployment verifies it when loading
the private proof and again before execution starts. An admitted attempt may
outlive this validity under its existing `--timeout` deadline, while active
pointer/generation/hash and current input/source/owner fences remain required.
Every new attempt must obtain fresh start admission; no consumer extends or
rewrites the immutable proof's timestamps. The separate
`DEPLOYMENT_UPGRADE_WINDOW_SECONDS` is a minimum PKI margin: live checks must
cover the later of that fresh margin or the execution deadline. Ownership
cleanup retains its independent bounded deadline.

Every producer and consumer must agree on one documented location. An explicit
legacy import validates the environment and original proof before migration;
normal operation has no worktree/environment fallback. A missing file in an
existing deployment generation is a repair/qualification blocker, not authority
to generate replacement keys, reinitialize data or create a successful receipt.
Preflight only validates.

### Deployment generation identity boundary

The formal `--operation create` preflight detects an original checkpoint and reads the selected
`deployment/.execution-active/owner.json`, its bound original journal, the SHA-bound
original email-repair proof and the existing recovery engine's live evidence.
Proof discovery consumes the completed Account bootstrap receipt's
`email_key_repair_proof_sha256` and reads only private
`deployment/repairs/account-email-format-*/verified.json` producers in the same
canonical store. It requires a unique matching digest, environment, parent and
completed schema; no other generation, archive or worktree is a fallback.
Its current report producer writes
`deployment/reports/create-recovery-preflight.json` (or the explicitly selected
canonical report path), invalidating previous current GO before the attempt.
After all checks, that report contains the original engine's complete private
qualification artifact for operator review; it is not a new identity or owner
record. Unknown outcomes remain fenced. Original owner, journal, tokens, keys,
encrypted custody, certificates and native PVC ownership are retained. Actual
execution consumes the separately stored version-bound qualification below;
the report adapter never overwrites it with a public summary.

Initialized OpenBao audit checkpoint recovery produces
`deployment/create/openbao-recovery/<review-sha256>/{qualification.json,qualifications/<UTC-time>.json,source-owner.json,source-journal.json}`.
Its 15-minute six-gate proof binds original initialization response/key hashes,
accepted foundation and native Pod/StatefulSet owners, actual redacted audit
readback, unchanged transport/JWT/runtime values and exact formal release.
Explicit continuation consumes this proof, appends actual recovery evidence to
the original generation journal and never reinitializes or regenerates keys.
Guarded declarative audit repairs record original ConfigMap bytes, exact UID/RV
and content-fenced patches, projected-file verification and SIGHUP/native-readback
outcomes under `deployment/repairs/openbao-audit-config-<UTC-time>/`; failed API
attempts remain at `deployment/repairs/openbao-audit-<UTC-time>/` and cannot imply
audit completion. Canonical private modes remain 0700 directories/0600 files.

The `--after-database-roles` checkpoint uses the same private recovery directory
and producer/consumer lifecycle. Original login passwords are produced only by
initial bootstrap at `deployment/create/material/<parent-sha256>/database/
{controller,issuer,verifier}.password`. Recovery reads and hashes these exact
files, checks them against the original credential Secret and authenticates the
existing native logins. Its separately named grant-repair Job and original-password
runtime DSN Secrets use ordinary private `deployment/rollback/<child-sha256>/`
plans/journals; genuine completion is recorded in the original parent execution.
Missing password material blocks recovery rather than authorizing regeneration.
The Service trust renderer also consumes the original verifier password to bind
`pki-verifier-database` in the Account Manager namespace. It first verifies the
original Video Cloud namespace Secret against that same native login and DSN;
the Account Manager PKI consumer receives the existing credential through its
own namespace Secret. The qualified partial trust recovery produces its exact
`service-root-trust-plan.json` in the reviewed recovery directory and ordinary
private child plans/journals under `deployment/rollback/<child-sha256>/`.
Execution consumes these bindings without replacing the password, database login,
Root custody or existing PVC. Original failed child evidence remains immutable.
Service session ordering recovery reads the original
`deployment/create/<parent-sha256>/bootstrap/service/session-intent.json`, completed
active issuer/CRL documents and private failed/accepted child evidence. It saves
`service-session-plan.json` beneath its reviewed recovery directory. Only a
terminal Job with no actual Pod execution and no registered session qualifies
the forward executor. That executor consumes the original session ID and uses a
new, explicitly retained custody PVC; original failure evidence is preserved.
Expired unused bootstrap recovery produces its new intent and observed native
session/cleanup bindings under
`deployment/create/<parent-sha256>/bootstrap/service/session-renewal/<review-sha256>/`.
Its exact native renewal Job is saved as `service-session-plan.json` in the reviewed
recovery directory. The native producer reuses `bootstrap.key` and `bootstrap.csr`
on the original retained `pki-service-bootstrap-state` PVC and writes only a new
certificate under `renew-<failed-child-sha256-prefix>/bootstrap.crt`. The original
custody reader consumes that certificate; verification checks unchanged key/CSR
hashes before publishing new immutable namespace credential copies. The new
session's actual UIDs and cleanup binding govern its consumers and finalization.
After genuine native finalization, its recorded custody preparation preserves
the old public certificate at `expired-<original-session-id>/bootstrap.crt` on
the same retained PVC and links the renewed certificate into the canonical
native cleanup slot. The native cleanup command consumes that exact finalized
certificate and original key/CSR, writes its tombstone and unlinks the ephemeral
credentials. Original expired namespace credential copies are saved under
`session-renewal/<review-sha256>/expired-credential-copies/<namespace>.json`
in the same private generation record before exact UID-conditional retirement.
These `0600` archival records are recovery evidence and are never runtime inputs.
Original session documents, expired certificate, credential copies, failed Job,
Root custody and OpenBao initialization remain unchanged.
The authorized formal CI selection producer writes
`deployment/repairs/service-bootstrap/selection.json` and
`deployment/repairs/service-bootstrap/<source-revision>/image-manifest.json`
with 0700 directories and 0600 files. Original-generation application trust
preflight consumes these exact bytes, parent binding, source revision, successful
main Release run and attempt; execution consumes only the qualified immutable
image for native grant repair and same-key renewal. These records contain no
private key or token and never replace original custody or generation identity.
Identity-lock recovery qualification produces
`deployment/create/openbao-recovery/<review-sha256>/service-managed-core-plan.json`
with the same 0700/0600 permissions. Review/admission and the original-generation
executor consume its bound existing owners and current session; only the rollout
strategy and exclusive owner-mode annotation change. Keys stay on the original PVC and successful bootstrap stages
remain intact.
MQTT trust-order qualification writes
`deployment/create/openbao-recovery/<review-sha256>/mqtt-root-trust-plan.json`
with 0700/0600 permissions. Review/admission and forward execution consume the
original bodies, owner UID and original encrypted MQTT Root custody/operation;
configuration is installed before Pods without replacing keys or replaying
completed Service/application enrollment.
`deployment/create/openbao-recovery/<review-sha256>/used-bootstrap-renewal-plan.json`
is the private 0600 admission plan for a genuinely expired, previously used seed.
Its executor reuses the retained key/CSR and original application `current` state;
renewed session records and archived exact-UID expired copies remain under the
same generation's `bootstrap/service/session-renewal/<review-sha256>/` directory.
The `--after-database-secrets` profile consumes these same original files and
the failed child journal only when all three actual Secret writes completed.
Its independently qualified all-retain stage changes no Secret values; its real
verification receipt completes the pending parent stage and preserves the failed
child journal unchanged.
The managed transport partial-rollout profile produces its five-resource
`openbao-root-trust-plan.json` in that same private recovery directory. Review
and exact-checker admission consume the original source prefix, completed seed
and enrollment records, exact failed child and current original owners. MQTT
runtime credentials appear only in the private Secret/candidate; its public
ConfigMap contains no credential or server private key. The original failed
`deployment/rollback/<child-sha256>/` plan/journal remains immutable evidence,
while execution produces a separate accepted forward plan/journal in the same
canonical rollback hierarchy. Original session credentials, client identities
and enrolled server keys are reused, never regenerated or copied into reports.
An interrupted admitted forward consumes the same private candidate path and
prior immutable GO/source/admission/failed-child records. Its additional owners
are the original Controller and Service trust ConfigMap; a public CRL manifest
addition carries no key or credential. Native host CRL caches use private
directories on each original identity/state PVC through the explicit
`/var/lib/pki-service` alias, with 0700 directories and 0600 files. Their producer
is the native Service CRL consumer; host startup and genuine installation ACK
consume the signed Root/online intermediate state. Neither cache preparation nor
policy correction may replace an identity file or manufacture an ACK.
The written-OpenBao profile uses that same canonical private candidate and
retains every failed predecessor. It consumes the original accepted registry
Secret UID/type/body without copying its value, plus original initialization,
root-token/share and ordinal PVC proofs. The parent journal's
`openbao-managed-recovery-verified-<review-sha256>` entry is produced only by
actual restored native status/audit/policy/role verification. Qualification
records retained prerequisite hashes and expressly pending native readback;
it cannot fabricate an audit observation or an installation ACK.
The accepted-host audit repair consumes the genuinely accepted child and
native original-share unseal receipt. Qualification's private retained-process
hashes authenticate only the exact provider executable/UID/start time and
historical PID marker; the latter never becomes signal authority. The private
parent entry `openbao-managed-audit-reload-<review-sha256>` records the original
ConfigMap/Pod/StatefulSet IDs, projected public HCL digest and actual redacted
audit readback after native reload. No root token is placed in arguments or
reports; native authentication receives the original token on standard input.
The original audit PVC, retained client/server states and seed remain unchanged.
The post-managed API export qualifier consumes those completed original receipts
and accepted Job/reader children, recording only current audit and owner hashes
in `deployment/create/openbao-recovery/<review-sha256>/` and the canonical parent
journal. It does not replay signals or enrollment, copy key files, or create a
replacement seed. Native export remains in memory until its own namespace TLS
Secret is accepted through the guarded deployment plan.
Formal OpenBao ACK image selection and its untouched CI manifest use the same
selected-environment store at `deployment/repairs/openbao-host-ack/selection.json`
and `deployment/repairs/openbao-host-ack/<source>/image-manifest.json` (0700
directories/0600 files). The artifact acquisition producer pins archive/manifest
bytes; the typed loader checks original parent, sole native role and the same
actual successful formal main CI attempt. This package evidence contains no
runtime credential and is never a rollout or native ACK receipt by itself.
The final-core restoration producer writes
`deployment/create/openbao-recovery/<review>/managed-complete-issuer-forward-plan.json`
using 0700 directories and 0600 files. Its source loader consumes only the original
coupled admission and captured source, accepted ACK/application/routing journals,
original private owner and the exact failed core plan/journal. The typed restoration
observer checks genuine expired bootstrap copies against their retained key/CSR,
leaf fingerprint and Secret UIDs. Their presence provides observation authority
only; an eight-ACK restoration GO cannot mint a credential, perform old-copy cleanup,
finalize the session or release the parent owner. The source/candidate and actual
forward receipt remain in the selected canonical environment store.
The interrupted CertIssuer provider-trust repair uses
`deployment/repairs/certissuer-provider-prepare/selection.json` and
`deployment/repairs/certissuer-provider-prepare/<source>/image-manifest.json` for
the actual formal publication producer and exact-parent qualification consumer.
Its separately material-bound descriptor is
`deployment/create/openbao-recovery/<review>/certissuer-provider-prepare-image.json`.
Only after genuine native Core acceptance and exact failed-alias reconciliation
does the completion producer write
`deployment/create/certissuer-provider-prepare/<review>/final-journal.json` and
`completed.json`, followed by the parent pointer
`deployment/create/material/<parent>/certissuer-provider-prepare.json`.
Release, inventory and health consumers require that authenticated chain and
preserve the sole CertIssuer image override. The final journal includes private
owner identity; all these records remain in the selected environment store,
with 0700 directories and 0600 files. No runtime credential or CA identity is
replaced, and image publication alone cannot count as deployment acceptance.
The admission-only Account Manager profile saves its newly qualified exact
two-resource candidate as `account-admission-plan.json` in that recovery directory.
Rendering consumes the original selected runtime snapshot; no credential is
generated. Execution records actual scoped creation and parent completion only
after native readback, while retaining the rejected original intent.

Initial email-format repair produces immutable original runtime/Secret snapshots,
UID/resource-version guarded requests and actual readback at
`deployment/repairs/account-email-format-<UTC-time>/`. Its `verified.json` binds
the original parent/child, original unused seed, deterministic encoding repair
and exact retained owners. Initialized-create recovery consumes and independently
checks that proof against the original foundation and current canonical runtime.
No valid key or existing outbox ciphertext may be changed through this profile.

Initial temporary transport renewal produces new authority signing keys,
certificates and original serving-key copies under
`deployment/create/transport-renewal/<review-sha256>/{management,openbao}-transport/`.
Original resource bodies use that directory's `original/`; actual completion uses
`completed.json` and the matching one-time pointer
`deployment/create/material/<parent-sha256>/transport-renewal.json`. The qualified,
fenced executor produces these paths only before any native issuer exists;
subsequent foundation recovery independently verifies original material hashes,
unchanged leaf keys/public identities, new signatures and exact original resource
UIDs before consuming the new CA paths. Original material remains immutable.
The separate all-CA-ACTIVE same-key temporary TLS repair uses prepared candidate
certificates at `deployment/repairs/active-temporary-tls-same-key-<UTC>/`, and
qualifier/executor evidence at `deployment/create/transport-renewal/<review-sha256>/`.
This includes `qualification.json`, `source-journal.json`, original owner
snapshots, guarded per-resource intents/readback, unchanged serving-key material
in `<role>-transport/identities.json`, renewed `ca.crt`, and `completed.json`.
Authority-key references remain at the original V1 `ca.key` paths; no new CA key
is generated or copied. The original V1 pointer and materials remain immutable.
The executor publishes only completed proof to
`deployment/create/material/<parent-sha256>/active-transport-renewal.json`;
normal foundation recovery authenticates that proof and its same-key/owner
bindings before selecting the new public certificate paths. Controller files
remain 0700/0600, and Secret-bearing snapshots/plans are never source-controlled.
The repair does not replace managed issuers, initialization, custody or workloads'
persistent identities. Native temporary TLS reload and provider-consumer trust
reload must be verified independently of on-disk certificate validity.

The existing Service Root CRL repair records prior native publication,
revocation inventory, exact reviewed request, original public authority, signed
successor and native import/state readback at
`deployment/repairs/root-crl-<UTC>-<native-sha12>/`. Encrypted Root key custody and
its separate passphrase stay at their original declared paths. The qualifier
produces immutable `qualification.json` and `source-journal.json` under
`deployment/create/root-crl-recovery/<review-sha256>/`; the executor publishes only
verified completion at
`deployment/create/material/<parent-sha256>/service-root-crl-renewal.json`.
Original initial CRL material, issuer publications and receipts remain immutable.
Recovery and subsequent managed renderers use the proof's signed current CRL
file and original Secret owner binding. Original native Controller/CertIssuer
state/PVC/Pod identities are checked before applying public CRL state, with a
recorded local and Kubernetes execution fence. An interrupted original repair
whose native publication and four original disk states already match can use
separately qualified observation-only completion. Its producer writes immutable
`qualification.json`, `source-journal.json` and `completed.json` beneath
`deployment/create/root-crl-recovery/<original-review>/installed-resume/<resume-review>/`,
plus the exact `installed-resume/completed.json` pointer. The installed executor
and original CRL proof reader authenticate those files, the original partial
journal, current checker and exact native state; they never search for another
candidate or rewrite the original admission. No key/CRL generation, state replay
or runtime ACK occurs. All these local files/directories are 0600/0700; native
workload stores follow their own declared owner contract.

The new generation's sole authenticated Platform Admin binding is produced once
by admitted bootstrap at `deployment/create/material/<parent-sha256>/operator-binding.json`;
listeners and subsequent authenticated bootstrap consume that bound native UUID.
No prior generation user, JWT, OpenBao or managed PKI identity is overwritten.

Fresh worker naming produces the native instance/class/ordinal mapping at
`deployment/adapters/lke/worker-labels.json` before display-label mutation. The
fenced create executor consumes that mapping for exact instance naming and records
per-instance intents/readback in its original parent journal. Existing mappings
require explicit recovery rather than renumbering or replay.

Authorized LKE display-label repairs produce original metadata, exact rename
intents, provider responses and readback at
`deployment/repairs/lke-naming-<UTC-time>/`. The repair consumer verifies the
selected cluster/pool/instance IDs and original Node UID, and changes only names.
Allocated IDs, disks, addresses, kubeconfig and OpenBao/PKI material remain intact.
The maintained naming policy is [environment and infrastructure naming](environment-dns-naming.md#lke-cluster-pools-and-worker-display-names).

Initial foundation recovery produces
`deployment/create/foundation-recovery/<review-sha256>/qualification.json`,
immutable `qualifications/<UTC-time>.json`, and original byte-preserving
`source-owner.json`/`source-journal.json` snapshots before execution. Explicit
continuation consumes the one-hour six-gate proof and original transport/JWT
files, appends an actual recovery admission to the original parent journal,
and records an independent all-retain verification stage under the original
generation. Only its genuine verification completes the pending foundation;
the failed child rollback journal remains untouched. Namespace, provider, PVC/PV
and signer identities stay fixed. All files use 0600 under canonical 0700 directories.

Explicit new-generation bucket-key preparation also produces
`deployment/create/recovery-storage-keys/<original-plan-sha256>/retention-readback.json`
when the operator verifies native old-key retention and promoted new values.
It contains only native key IDs, original-file hashes and verification time;
diagnosis consumes it without reading credential values. The completed-canary
recovery profile preserves the stopped recovery journal and its ancestor digests,
then produces another actual six-gate qualification and a separately admitted
journal in the existing canonical recovery/execution paths. No canary receipt
may be fabricated or overwritten; original verified DELETE/404 evidence and
current exact-object absence must both be available. All these files are 0600
under 0700 directories.

An explicitly selected brand-new deployment generation may establish new
OpenBao keys and Service PKI identities through its qualified bootstrap
lifecycle. Previous dev keys may remain preserved without requiring a new dev
deployment to reuse them. Continuation, upgrade, retry and restore of an existing
generation retain its original identity; absence of files alone never grants
new-generation authorization.

Before new active state is initialized, qualify native target absence and idle
execution/recovery, then preserve and segregate the previous generation's
material under `deployment/archives/<generation>/` in the same canonical
environment store. Record original relative paths and content hashes without
secret values; retain all original bytes with 0700 directories and 0600 files.
Only explicit validated restore may consume archived identities. Ordinary
preflight and bootstrap use the selected active paths without archive fallback.
The archive does not prove provider removal, issuer activation or deployment
success. Preflight generates no OpenBao keys, CA or Service identities; only the
separately admitted deployment executor establishes the new lineage. Follow the
[new deployment identity lifecycle](deployment-operations.md#new-deployment-identity-lifecycle).

### Existing-generation qualification boundaries

The read-only `deployment.execution.surge-capacity` check obtains fresh native
Node and all-namespace Pod snapshots for changed singleton RollingUpdate
Deployments requiring overlap. Assigned nonterminal requests and status allocations
remain reserved even when a Pod is non-Ready or terminating. The check creates no
Pod, infrastructure allocation, request change or credential/identity material;
it requires no application readiness, CRL write or ACK. Recreate and zero-surge
updates skip it. Its sanitized result uses the existing private deployment
reports/progress logs; it introduces no operational-data path or allocation
receipt and does not prove eventual scheduling or startup.

`PKI_TURN_CLI_PASSWORD_FILE` is the pkiturn container's mounted file reference
(currently `/run/pkiturn-cli/password`), not a controller-side secret input.
Preflight checks existing reference/mount metadata without opening that path on
the controller or requiring a future deployment-generated local password. The
source `pkiturn-cli` Kubernetes Secret and its private runtime copy remain
distinct from the local per-relay deployment/recovery credential. Existing
upgrades preserve them; a new consumer must receive the same relay credential
through managed initialization. Missing existing credentials or mismatched
copies cannot be repaired by generating a different password. See the
[TURN lifecycle and producer/consumer boundary](cloud-deployment-architecture.md#turn-cli-credential-lifecycle).

The optional `secret_servers` identity records contain public pins, policy and
mounted paths, never PEM keys or a desired workload spec. Their bootstrap/adoption
population remains integration work; preflight must reject an active server
without independently recorded policy. It may not derive the trusted Root from
that server's Secret. Kubernetes owns the mounted synchronized Secret files;
read-only native probes retain key bytes in memory and return public digests.
Controller validation also binds the exact original Secret bytes in memory;
neither observer exports keys into arguments, reports or temporary files.

Synthetic tests may use isolated fixture roots containing no real environment
material. CI deployment controllers use their own canonical environment store;
they must not choose arbitrary temporary roots for actual deployment secrets.
Online OpenBao/Kubernetes storage and independently held encrypted escrow retain
their documented runtime/custody authority; this section governs local controller
files and does not export online CA keys.

Legacy deployment writers that still use workspace runtime are migration work
and do not conform to this boundary. Storage evidence placement is implemented
by this change. Edge/TURN deployment artifacts and their relay reader now use
private `deployment/artifacts/edge-haproxy/` and `deployment/artifacts/coturn-vm/`
with no checkout fallback or duplicate TURN summary.
LKE provision summaries also use private `deployment/artifacts/lke-provision/`;
the legacy artifact-directory option cannot select another summary destination.
The selected environment must be explicit and match maintained configuration.
These summaries describe an operation and are not completion receipts or GO
evidence. Remaining service,
temporary and report writers are part of the
unified deployment replacement and must be corrected before claiming completion.

The canonical local secret root is defined by [SecretStore](secret-store.md):

```text
~/.config/rtk_cloud/<environment>/
```

Environment names such as `staging` and `prod` select independent stores; there
is no implicit alias, shared fallback or staging-to-production reuse.
`RTK_CLOUD_CONFIG_ROOT` may select a different parent directory. The repository
`cloud_env/<environment>/runtime/` may contain secret-free resolved configuration
only; dynamic operational state belongs in the canonical environment store.

Authoritative storage receipts and migration proofs live in
`~/.config/rtk_cloud/<environment>/deployment/storage/` (`0700`/`0600`). They
survive checkout changes; private cutover journals remain in `migration-backup/`.
Workspace runtime copies are explicit legacy import sources only. See
[storage evidence location and migration](storage-credential-lifecycle.md#environment-owned-receipts-and-proof-location).

The OTA-specific compiler projects maintained storage intent for
`video-cloud-otaservice` and derives its endpoint/type from current read-only
provider metadata for the exact existing firmware bucket. Original credential
Secret references and UID/key bindings are retained and checked without copying
keys or requiring OTA readiness. Missing local lifecycle evidence is not authority
to create a bucket/key, invent an allocation/activation receipt or borrow historical
workload endpoint values. No manual derived endpoint setting is added.
The common environment path resolver and protected writer store the public
observation at `deployment/storage/ota-render-observation.json`, with 0700
directories and a 0600 bounded regular file. Its selected-target metadata and
stable hash feed the private input snapshot/OTA renderer; a fresh unchanged
provider observation preserves the file and its timestamp does not change the
plan digest. It cannot substitute for required lifecycle or storage-validation
receipts.
For an existing OTA update without a related owner schema migration, the original
UID-fenced PostgreSQL database supplies read-only startup table/column evidence.
No DDL, migration receipt or OTA initialization is produced. Related owner
migrations need their own order/evidence boundary qualification; their future
objects are not treated as pre-existing runtime evidence.

OTA's public immutable registration descriptor is produced by formal CI from the
exact published image's pure SDK description entry and carried in existing
`image-manifest.json` evidence as `service_registration_manifests.ota`. Release
selection and the plan hash bind its provenance; preflight consumes it with a
bounded read-only query against the original UID-fenced Account Manager database
using frozen identity. It checks immutable content and original workload/instance
approval without registry writes, identity changes or OTA readiness. Actual
registration leases, heartbeats and Ready evidence belong to post-deploy acceptance.
No standalone operational-data path or manual manifest/version setting is added;
private plan/report/log handling remains at the existing canonical environment
paths with 0700 directories and 0600 files.

The former `.secrets/<environment>/<provider>/<service>/` layout is
reference-only migration input, not the current deployment or recovery source.
Only `secrets migrate` may read legacy secret paths; normal deployment and
backup commands do not fall back to them.

## Current Ownership and OpenBao Target

| Material | Current writable authority | Consumer/recovery copy |
| --- | --- | --- |
| Provider/operator credentials and kubeconfig | Environment-local SecretStore | Job-local CI material or operator access only. |
| Catalogued runtime values injected by deployment | Environment-local SecretStore through the current CLI | Kubernetes Secrets are verified synchronized copies. |
| Issuer keys, PKI state, policy and revocation in OpenBao | OpenBao for deployments using that issuer | Matched offline backup and independent seal escrow; local PKI files do not replace issuer state. |
| Runtime KV after a reviewed secret-manager cutover | OpenBao or approved customer manager | One-way synchronized workload material; record recovery/rotation authority in the cutover manifest. |

Do not enable independent writes to both local runtime files and OpenBao KV for
the same secret. The target below does not claim that every runtime value has
already migrated. Retain the current SecretStore path until its selected
injection and recovery procedure is explicitly implemented and qualified.

## OpenBao Target Responsibilities

OpenBao is the target secret manager for staging and production. The local
environment-specific SecretStore remains the operator bootstrap and recovery
interface, but long-lived service-generated runtime secrets should be
stored in OpenBao once the selected K8s secret-injection path is approved.

V1 OpenBao responsibilities:

- `kv-v2` stores runtime secrets such as DSNs, JWT/token keys, MQTT
  credentials, TURN shared secrets, object-storage credentials, factory
  enrollment HMAC keys, and service bootstrap pointers.
- `pki` signs device/factory, gateway/server, and app/user CSRs. Existing
  service-side CSR validation, request idempotency, and audit tables remain in
  `rtk_video_cloud`.
- OpenBao audit logging records secret reads and PKI signing operations. RTK
  services still record domain audit events such as certificate issuance
  request id, CSR hash, subject, serial, entitlement evidence, and outcome.

V1 OpenBao mounts:

```text
secret/rtk-cloud/<environment>/<service>/...
pki/device
pki/app
pki/gateway
```

The `secret/` paths are examples. Deployments may choose a different mount, but
the service-level manifest must record the actual mount and path prefix.

Recommended `kv-v2` layout:

```text
secret/rtk-cloud/staging/video-cloud/api
secret/rtk-cloud/staging/video-cloud/certissuer
secret/rtk-cloud/staging/video-cloud/factoryenroll
secret/rtk-cloud/staging/video-cloud/mqtt
secret/rtk-cloud/staging/video-cloud/turn
secret/rtk-cloud/staging/account-manager/api
secret/rtk-cloud/staging/shared/dns
secret/rtk-cloud/staging/shared/object-storage
```

OpenBao policies must be least-privilege:

- `video-cloud-certissuer` may sign only approved PKI roles and read only the
  runtime values needed by `cmd/certissuer`.
- The LKE Account Manager client identity is `service:account-manager`. During
  the managed-PKI transition, certissuer also accepts the legacy
  `account-manager` common name when it is signed by the same deployment-owned
  certissuer service CA. The deployment preflight must verify the live mTLS
  authorization path with `scripts/check-certissuer-app-mtls.sh` before running
  staging or production acceptance.
- `video-cloud-env-renderer` may read only the KV paths needed to render the
  target workload runtime environment. Legacy VM/systemd env rendering remains
  reference-only.
- `factoryenroll` may not sign certificates directly in OpenBao; it continues
  to call `cmd/certissuer` over the existing issuer boundary.
- Operators may write or rotate secrets through explicit administrative policy;
  service roles must not have write access to production runtime secrets.

For legacy Linode/systemd deployments, service authentication to OpenBao used
AppRole:

```text
/etc/video_cloud/openbao/role_id
/etc/video_cloud/openbao/secret_id
```

Both files must be root-owned, mode `0600`, and excluded from readiness
reports when inspecting legacy hosts. Current K8s runtime should use Kubernetes
auth or an approved External Secrets-style injection path instead of AppRole.

Legacy VM transition only: services consume env files. A deployment
render step reads OpenBao KV entries and writes root-owned files under
`/run/video_cloud/*.env` or another tmpfs runtime directory before systemd
starts the service. This preserves the current process config boundary while
moving secret ownership to OpenBao.

## Local SecretStore Layout

```text
~/.config/rtk_cloud/<environment>/
  operator/
  runtime/
  pki/
  openbao/
  kube/
  test/
```

| Directory | Purpose |
| --- | --- |
| `operator/` | Environment-bound provider/operator credentials; independent recovery access. |
| `runtime/` | Long-lived runtime secret catalog; explicitly selected encrypted backup material. |
| `pki/` | Deployment PKI and certificate/key material, not a replacement for OpenBao issuer state. |
| `openbao/` | TLS material and separately controlled seal/bootstrap access; never archive the whole directory indiscriminately. |
| `kube/` | Environment kubeconfig; recreate for a replacement cluster. |
| `test/` | Test-only Device/user credentials and databases; separate controller handoff. |

Directories are `0700`, files `0600`. Short-lived access/admin tokens are not
persisted. Use `secrets inventory` for non-secret metadata rather than creating
a second service-directory catalog.

## LKE Secret Management Target

LKE deployments must not make Git, Helm values, Kustomize overlays, Docker
images, or CI logs the source of truth for runtime secrets. The target secret
boundary is:

- OpenBao or an approved customer secret manager stores long-lived runtime
  secrets, PKI state and policy; audit is emitted to an independently retained
  sink and must not be rewound with core storage.
- Workloads authenticate to OpenBao through Kubernetes auth or another reviewed
  workload identity method. AppRole remains a legacy VM bridge unless a specific
  transition runbook approves it.
- Kubernetes Secrets may hold short-lived synchronized material or bootstrap
  references needed by Pods, but those values must be generated or injected at
  deploy time and never committed.
- External Secrets-style sync, CSI secret injection, or init-container rendering
  require reviewed workload identity, rotation, rollback, and recovery
  qualification under this policy and
  [deployment-operations.md](deployment-operations.md).
- OpenBao root tokens, unseal keys, recovery keys, HSM PINs, production signing
  keys, and raw private key PEM values must never be committed, embedded in
  images, placed in public documentation, or stored in readiness artifacts.

The current LKE adapter uses standalone OpenBao file storage. The
[core backup/restore procedure](backup-restore.md) handles it with an offline
PVC archive matched to PostgreSQL. Production still requires approved HA/seal
design, independent recovery-key escrow, audit retention, auth rebinding and a
successful full recovery rehearsal; local backup script tests do not close
those gates.

## Legacy Manifest Example (Reference Only)

The older service-level manifest example below remains migration/reference
material. Current deployments use the SecretStore catalog and `secrets
inventory`; current core archives use the versioned manifest documented in
[backup-restore.md](backup-restore.md). All public inventories follow the same
no-secret-values rule.

Each `manifest.json` records metadata only. It must not contain raw secret
values, private key PEM blocks, bearer tokens, JWTs, passwords, DSNs with
passwords, or full environment file contents.

Required manifest fields:

- `environment`: one of `local`, `staging`, `production`, or `shared`
- `provider`: provider name, for example `linode`, `aws`, `gcp`, `github`, or `dns`
- `service`: service or shared category name
- `owner`: team or operator responsible for the directory
- `items`: inventory entries for secret-bearing files or directories

Each item should include:

- `id`: stable identifier
- `type`: `env_file`, `certificate`, `private_key`, `token`, `state`, or `pointer`
- `relative_path`: path relative to the service secret directory
- `deployed_to`: destination path or consumer, if applicable
- `contains`: human-readable categories, not values
- `rotation_required`: boolean
- `last_rotated`: ISO date or `unknown`

See `docs/examples/secrets-manifest.example.json`.

## Deployment Script Interface

Current workspace commands select the environment explicitly:

```sh
go run ./scripts/go/rtk-cloud -- secrets verify --environment staging
go run ./scripts/go/rtk-cloud -- secrets inventory --environment staging
```

`DEPLOY_SECRETS_DIR` and AppRole-based VM rendering are legacy service
interfaces, not workspace SecretStore fallbacks. A reviewed legacy bridge may
use the following service-specific OpenBao settings; Kubernetes workload auth
must follow the selected LKE injection contract:

```sh
OPENBAO_ADDR=https://openbao.internal:8200
OPENBAO_CACERT=/etc/video_cloud/openbao/ca.pem
OPENBAO_AUTH_METHOD=approle
OPENBAO_ROLE_ID_FILE=/etc/video_cloud/openbao/role_id
OPENBAO_SECRET_ID_FILE=/etc/video_cloud/openbao/secret_id
OPENBAO_KV_MOUNT=secret
OPENBAO_KV_PREFIX=rtk-cloud/staging/video-cloud
```

The renderer output is runtime state, not source material. Do not commit it and
do not archive it in readiness artifacts.
For LKE, `DEPLOY_SECRETS_DIR` is not the runtime secret source of truth. It may
hold operator-local bootstrap files, non-secret manifests, public certificates,
and rollback references while OpenBao or the approved secret manager owns live
runtime values.

## Artifact Boundary

`.artifacts/` remains test output only. It may contain temporary E2E run outputs,
redacted reports, generated fixtures, or local debugging material. It is not a
long-term deployment secret store.

Long-lived deploy secrets belong in the environment SecretStore and/or their
owning cloud secret manager. Reused E2E credentials belong under that
environment's `test/` tree. Encrypted core backups use a dedicated private
directory/bucket outside release artifacts; unseal shares and backup decryption
identities are escrowed independently. See [backup-restore.md](backup-restore.md).

Scheduled PostgreSQL physical backups use dedicated backup-writer and read-only
restore Object Storage credentials in the selected environment's `operator/env/`
entries. See [SecretStore](secret-store.md#backup-and-recovery-boundary) for their
exact names and [the PostgreSQL procedure](postgres-backup-restore.md) for the
replication-password mount and independent age-identity escrow. Do not fall back
to media/release credentials or place a decryption identity in the CronJob.

## Security Rules

- Do not commit `.secrets/`, `.secrets.backup/`, private keys, raw env files,
  bearer tokens, JWTs, or password-bearing DSNs.
- Do not paste secret values into PRs, issues, docs, terminal summaries, or test
  reports.
- Use file mode `0600` for private keys, raw env files, and token files.
- Production manifests must not reference staging domains, staging token files,
  staging certsets, or test-only device material.
- Any secret that appears in tracked files, PR bodies, issue bodies, shared logs,
  or generated reports should be treated as compromised and rotated.
- Do not log OpenBao tokens, AppRole `secret_id` values, rendered env files, or
  OpenBao response bodies that contain secret data.
- Treat OpenBao availability as a deployment prerequisite. If OpenBao is
  unreachable during startup, services must fail closed unless an operator has
  explicitly selected the rollback env-file path.
- New secret-injection designs require explicit workload identity, rotation,
  rollback and recovery qualification under this policy and
  [deployment-operations.md](deployment-operations.md). The historical LKE
  migration checklist does not block already implemented tooling.

## Current Deployment and Recovery Order

1. Update documentation first: governance, service config maps, certissuer
   design, OpenBao bootstrap, rollout, rollback, and acceptance criteria.
2. Initialize/verify the environment SecretStore. Use the explicit migration
   command for legacy inputs; do not create new `.secrets/` deployment trees.
3. Stand up OpenBao with TLS, audit logging, `kv-v2`, and PKI mounts for
   device/factory, app/user, and gateway/server certificates.
4. For a reviewed runtime-KV cutover, migrate only the selected service runtime
   values to OpenBao and record its write/sync authority. Provider/operator,
   kubeconfig and test credentials stay in their environment-local SecretStore
   paths. Until cutover, deployment continues to use the local runtime catalog.
5. Keep runtime injection and local recovery material aligned with the
   SecretStore catalog; preserve reviewed legacy bridges only where necessary.
6. Switch `cmd/certissuer` staging config to the OpenBao PKI signer provider.
7. Qualify workload identity, secret injection, rotation, rollback, and recovery
   under this policy and [deployment-operations.md](deployment-operations.md)
   before approving production manifests, Helm values, or deployment pipelines.
8. Run staging validation, including `scripts/run-staging-e2e.sh`.
9. Rehearse matched core backup/restore with separately retrieved escrow before
   production approval. Restore original PKI, then reconcile external
   revocation, billing and notification side effects before resuming traffic.
10. Rotate any values previously exposed in tracked files or logs.

The OpenBao migration is not accepted until the staging end-to-end command
passes:

```sh
scripts/run-staging-e2e.sh
```

### Derived issuer runtime references

For the main CertIssuer and FactoryEnroll, the generated binding's
`issuer_runtime_secret` field records only the name of one original referenced
Secret selected by its role keys. Its UID is retained and fenced by the plan;
Secret payloads are not copied into this field. The shared renderer derives
CertIssuer's CA container path from the recorded Secret item and read-only mount,
and builds database settings with a `POSTGRES_PASSWORD` Secret reference before
the DSN that expands it. A container path is not a new local certificate-storage
location. Local observations still use the canonical private environment store,
with `0700` directories and `0600` files. Missing identity is not permission to
regenerate it or search another environment. See
[issuer startup configuration](cloud-deployment-architecture.md#issuer-startup-configuration).

### LogIngester MQTT lifecycle references

The existing LogIngester's managed MQTT identity references are observed under
`VIDEO_CLOUD_LOG_INGESTER_MQTT_IDENTITY_` and retained in the canonical deployment
binding. The shared renderer derives only transport endpoints and mounted public
CA/CRL paths; the original Service identity state, independent Root pins and
MQTT PVC remain lifecycle-owned. Qualification verifies complete candidate
settings, original public CA/CRL ConfigMap keys and ownership, the original
writable PVC mapping and original state paths. The LogIngester
runtime environment derives from the selected maintained environment; its namespace
and both original UID-bound manifests must match that environment and the native
MQTT/Service domains, active/retiring issuer lifecycle, pinned signing chain and
unique original state mappings. This checks public inputs only; it reads no runtime
cache, demands no ACK and creates or rebinds no authority/identity material.
The common managed LogIngester profile uses one replica with `Recreate` on its
original `mqtt-pki-state` PVC,
terminating the old Pod before starting the replacement to protect the singleton
identity/cache writer without extra surge Pod capacity. Candidate qualification
checks this strategy, replica count and original mapping; it does not depend on
future replacement Pod readiness or alter the original identity/state contents.
Service CRL snapshots retain
`/var/lib/mqtt-pki/identity/service-crls/*.json`; MQTT CRL snapshots retain
`/var/lib/mqtt-pki/crls/*.json`. Preflight does not require runtime readiness,
directory inspection Pods, actual CRL writes or ACKs. Actual CRL persistence,
synchronization and ACKs belong to post-deploy acceptance. That read-only check
reads both original public CRL cache snapshots from the updated LogIngester
workload, verifies the pinned issuer, signed content and metadata against the
latest registry number/digest, and requires each exact native consumer's ACK.
The native consumer can reload and verify an existing same-digest cache without
rewriting it; acceptance creates no new CRL/key or write canary. The renderer
never creates another identity or stores keys in configuration.

### VideoStorage lifecycle references

The enabled independent VideoStorage candidate retains its original credential
Secret references, registration identity/file mappings, clip key, media bucket
and prefix, and durable state. Candidate qualification checks the role's complete
direct-upload/S3/PostgreSQL/registration inputs without changing Core's separately
maintained direct-upload flag or copying/regenerating credential material.
The compiler records only public existing-allocation metadata, maintained prefix,
stable hash and observation time in canonical
`deployment/storage/media-render-observation.json`, using common protected path
helpers and 0700 directories/0600 files. The private snapshot and independent
renderer consume it; qualification compares a fresh provider GET to the original
record and candidate. For an existing upgrade, missing/changed allocation is a
blocker, not authority to create a bucket or fabricate validation/cutover evidence.
The separate create preparation/credential flow retains its authority; this role
guard checks candidate settings/allocation without requiring future resource UIDs.
Unchanged metadata
retains the record and timestamp; only the stable projection affects the plan hash.
No VideoStorage readiness, registration lease or business write is required in
preflight. Actual registration/Ready and clip-upload/business-write evidence
belong to post-deploy acceptance.

### Device API callback references

The Device API's private MQTT callback uses its existing public broker CA
ConfigMap and mounted server TLS Secret. The common renderer derives the CA
container path from the original `mqtt-callback` mount and item, and retains the
broker auth key as a required Secret reference. Qualification reads only the
original CA owner and public certificate key. The derived Device API reader
profile uses explicit non-root UID/GID 10001 with supplemental group 0 for the
original root:root TLS Secret projection's 0440 group-read permission. Candidate
qualification checks the retained read-only mount, item paths, effective file
modes and process UID/groups, including container overrides, without a running
API/broker or inspection Pod. No `fsGroup` is added, PVC metadata is unchanged,
and no Secret, identity or path is copied or regenerated. Actual TLS startup and
listener operation belong to post-deploy acceptance. This profile adds no manual
path, UID or group setting. These observed references use the same canonical
`deployment/identity-bindings.json` below.

### App API lifecycle references

Preflight observes the existing App API's TLS Secret, App trust ConfigMap,
management Secret, trust-state PVC, mounts and original owned public route.
They are saved only as generated reference/ownership evidence in the selected
`~/.config/rtk_cloud/<environment>/deployment/identity-bindings.json` (0700
parent directories, 0600 file), using the common environment path resolver.
Deployment consumes the qualified observations and fences their live owners;
neither preflight nor an ordinary upgrade regenerates their payloads or resets
initialized root-policy state. Desired role booleans remain secret-free shared
configuration in `architecture.env`, not observed bindings.

### Core API CRL persistence directory permission repair

The shared renderer produces the release-pinned `core-identity-permissions`
initializer; the Core API consumes its original Service identity PVC root for
atomic CRL snapshot persistence. The state filename and mount derive from the
original lifecycle binding, while UID 10001 comes from the formal API image
contract. The initializer requires the existing identity, rejects links and
verifies unchanged state bytes. It changes only the original PVC root directory
owner/mode to 10001:10001/0700; identity/key/CRL file contents and metadata remain
untouched. Preflight validates the exact candidate recipe and CRL parent coverage
without requiring the API to be running. No operator path or UID setting is added.
Inspection manifests, directory metadata and deployment outcomes use the selected
canonical environment's `deployment/repairs/` or `deployment/recovery/` store,
with 0700 directories and 0600 files; helpers never export identity contents.

### Controller identity directory permission repair

The common Controller initializer is permission-only: its producer is the shared
renderer and pinned Alpine container; its consumer is the existing PKI Controller
identity store. Both original identity state files and the original PVC are
required. Only the PVC's existing `identity/` subtree receives UID/GID 10001,
0700 directories and 0600 files; contents, keys, Root pins, issuance records and
other PVC subdirectories are preserved. Links and missing state are blockers.
Preflight reads filesystem metadata only and never runs this initializer.
Metadata read transport failures retain a safe Kubernetes error code and require
API access/connectivity diagnosis; a failed read is not authority to change
directory ownership or mode. Actual metadata mismatches remain separate permission
blockers. Safe API GETs use maintained defaults of 30 seconds per request and
45 seconds per process, at most three attempts and a 200-millisecond context-aware
delay for classified timeout/rate-limit/unavailable failures only. Authentication,
forbidden, not-found, malformed-response and UID-drift outcomes stay strict;
exhausted transient failures are not reused as cached evidence. Container `stat`,
SQL and mutations are not replayed. The concurrency limit of twelve and complete
qualification deadline remain unchanged; no operator setting or identity change
is introduced.
Scoped repair proposals, original resource snapshots and outcome evidence belong
to the selected canonical environment's `deployment/recovery/`, using 0700
directories and 0600 files; they are separate from full rollout receipts.

The original-owner installation-lock continuation produces
`deployment/create/openbao-recovery/<review-sha256>/current-owner-quiesce-plan.json`
and `current-installation-plan.json` in the same private directory. Recovery
qualification writes these exact resource plans; the qualified executor verifies
and consumes them to pause the two recorded consumers and run only unfinished
native attestations. The plans, original source journal, admission and resulting
rollback journals are canonical selected-generation operational evidence, with
0700 directories and 0600 files. They do not belong in `cloud_env/` or a worktree.

### Original-generation OpenBao installed-ACK role proof

The qualified create-recovery producer stores its exact selected role descriptor
and candidate at `deployment/create/openbao-recovery/<review>/openbao-host-ack-source.json`
and `openbao-host-ack-plan.json`, alongside its immutable qualification and captured
source owner/journal. After accepted original-owner execution and nine native ACKs,
the completion producer writes immutable
`deployment/create/openbao-host-ack/<review>/completed.json` first, then
`deployment/create/material/<parent>/openbao-host-ack.json`. All paths resolve under
`~/.config/rtk_cloud/<environment>/`, with0700 directories/0600 files.
The bootstrap identity observer, final create target compiler and independent health
inventory consumer authenticate these proofs, original journal membership and formal
publication before overlaying only `LKE_OPENBAO_PKI_IMAGE`. The original parent
release stays immutable. After execution-fence retirement, consumers authenticate
the exact archived owner at its original execution directory. Missing/malformed
completion material never establishes a replacement identity or an image fallback.

### Original dev post-Core trust recovery material

Root CRL recovery producers and consumers use only the selected canonical
environment store. Signed requests/candidates/dry-run evidence live under
`deployment/repairs/{mqtt-root-crl,openbao-tls-root-crl}/<source-selector>/`;
source/qualification/final-journal/completion mirrors live under
`deployment/create/{mqtt-root-crl-recovery,openbao-tls-root-crl-recovery}/<review>/`.
The late-session source reader consumes the corresponding two
`deployment/create/material/<parent>/*-root-crl-renewal.json` pointers and the
accepted Core proof. Original CA ciphertext, passphrases, certificate/CSR,
OpenBao initialization and identities are retained unchanged.
For a paired expired-prior repair, the MQTT qualification producer reads the
original completed initial Root CRL stage files and records exact source/body,
custody, native inventory and consumer proof in its existing immutable plan.
The MQTT executor revalidates that proof before native import. It does not add
paired evidence to signing custody or change the stable signed-candidate path;
all original private material stays at its canonical location. Genuine advancing
ACK timestamps are checked monotonically, never fabricated.
Native Kubernetes consumer cache directories may retain the filesystem setgid
bit imposed by their maintained fsGroup: mode 0700 or 2700, with original
runAsUser/runAsGroup/fsGroup 10001 and matching directory owner/group. This
permits no group or other access. Cache files remain regular, non-symlink 0600
files owned by the original runtime user. The recovery reader validates these
native attributes and never clears setgid by changing the online store. Local
environment-store directories still require exactly 0700 and files 0600.

For an already imported MQTT #2 with both genuine native consumer stages
completed, the completion-only producer and reader use
`deployment/create/mqtt-root-crl-recovery/<original-review>/completion-only/<fresh-review>/`
for fresh qualification, actual captured journal, scoped lease and completion
proof. Scoped lease attempts use
`execution-leases/<lease-digest>/lease.json` beneath that sidecar; downstream
completion readers authenticate its referenced `completed.json`. They retain
the original signed candidate and child receipts unchanged.
Only the existing original top receipt/completion and its final mirror/pointer
may be written after fresh verification. No native sign/import/cache write or
new identity is authorized. This sidecar uses the same selected environment
helpers and 0700 directories/0600 files; no checkout or alternate store fallback
is permitted.

Late bootstrap artifacts belong to
`deployment/create/openbao-recovery/<review>/` and
`deployment/create/<parent>/bootstrap/service/session-renewal/<review>/`.
The pre-admission reader consumes the actual qualification and its existing
descriptor/preview files from that exact review directory. It authenticates
their hashes and current original owner/journal before hydration; this adds no
admission sidecar or alternate store. Current native rechecks and unchanged GO
expiry still govern the first parent admission and native write.
Only a qualified original-key native seed creates a distinct temporary session
credential; its actual three Secret UIDs, cleanup authority, original owner
forward and exclusive Existing-only Log helper determine completion. Native
finalization/tombstone and UID-bound retirement govern credential destruction.
All producers and readers require 0700 directories/0600 files and never fall
back to another environment or checkout.

A separately qualified terminal-seed trust-forward stores its fresh descriptor,
source snapshots, corrected Job preview and genuine accepted-forward proof
under `deployment/create/openbao-recovery/<fresh-review>/` as
`managed-late-seed-trust-forward-source.json` and
`managed-late-seed-trust-forward-plan.json`, alongside the existing qualification
and source-owner/source-journal producers. Its consumers are
the forward qualifier/executor, exact original Late runtime-progress reader,
effective-seed native observer and durable completion reader. It preserves the
original review's immutable seed plan, descriptor and failed child journal.
Only authentic accepted corrected-child evidence may resolve the original
pending stage; no historical failed receipt becomes fabricated acceptance.
No alternate environment store or checkout fallback is allowed.

Private operator diagnosis for this stopped seed uses
`deployment/repairs/managed-late-seed-failure-<original-review>/original-job-container.log`
`original-job.json`, `original-pod.json` and
`native-no-mint-sql-observed.json`, with a sanitized summary at
`deployment/reports/dev-late-seed-failure-<original-review>.json`.
Operator diagnosis/handover are the consumers; these supporting observations
are never execution authority and a new qualifier reobserves native outcomes.
All files remain 0600 in 0700 selected-environment directories.

The App-specific Root CRL preparer writes the immutable candidate, reviewed
request, public issuer document and signed CRL under
`deployment/repairs/app-root-crl/<source-custody-selector>/` as `candidate.json`,
`request.json`, `issuer.json` and `signed/revocations.pem`. The selector binds
source/owner, original Root, live prior/revocations and original custody hashes;
qualification and the exact importer consume these files. Existing candidates
are reused byte-for-byte, and missing signed output grants no replay.
The qualifier writes source/owner snapshots, plan and six-gate qualification under
`deployment/create/app-root-crl-recovery/<review>/`. The existing original
`pki/custody/roots/<AppRootID>/ca-key.encrypted.pem` and
`pki/custody/app-root-v1-key-passphrase` are read-only inputs to offline signing;
there is no identity regeneration or replacement custody. The normal bound
human management API importer and original API App-PKI/MQTT-PKI observers consume
the candidate. Completion writes
`deployment/create/material/<parent>/app-root-crl-renewal.json`; typed post-mint
source/progress/durable readers authenticate the exact four-stage parent tail,
recorded lease and completion pointer. All local outputs use the selected
canonical store, 0700 directories and 0600 files. Original Root #1 outputs remain
immutable. Missing, pending or unknown completion grants no fallback or new epoch.

After a proved native App import, the App completion-only qualifier exclusively
produces qualification, source-owner, source-journal and completed records under
`deployment/create/app-root-crl-recovery/<original-review>/completion-only/<fresh-review>/`.
The completion executor and typed completion reader consume these canonical
0700/0600 records, which bind the actual stopped373/374 source, same original
App import/candidate, real cache/ACK evidence and existing recorded lock owner.
The executor completes only the original remaining read proofs and mirrors; it
cannot sign/import again, acquire a second lock or establish an installation
clock. Exact owned-lock release follows genuine completion. Original artifacts
and import receipts remain immutable; unknown native outcomes have no fallback.

The scoped post-mint qualifier produces
`deployment/create/openbao-recovery/<fresh-review>/managed-late-post-mint-source.json`
and `managed-late-post-mint-owners-plan.json`, plus qualification and original
owner/journal snapshots. It restores the genuine selected session and cleanup
binding from accepted native/credential children. Remaining-owner execution,
bounded progress and durable completion consume the same source and candidate
pins. Original artifacts remain immutable.

First actual installation admission exclusively produces
`deployment/create/<parent>/bootstrap/service/managed-late-installation-clock.json`
under the selected environment store. It binds parent, review, source/owner,
checker, descriptor/qualification hashes, SID/fingerprint, actual admission epoch,
original caller deadline and genuine leaf/session expiry. Deadline is the minimum
of epoch plus 90 minutes, caller bound and actual authority expiry minus five
minutes; creation and finalizer entry require 15 minutes. The completed admission
receipt pins its raw SHA. Typed progress and durable completion consume it without
rewriting the epoch. Any admission or clock prevents another clock; missing or
orphan state blocks as an unknown outcome. Files remain 0600 in 0700 directories.
No alternate store, signing, identity regeneration or private material publication
is authorized. Retired temporary transport certificates are consumed only as
historical authenticated completed content in this scope; fresh current managed
TLS/session proof remains mandatory.

Expired-unused late renewal produces
`deployment/create/openbao-recovery/<fresh-review>/managed-late-expired-unused-source.json`
and `managed-late-expired-unused-seed-plan.json`, plus the effective late source,
owner/custody/Log plans, qualification and original owner/journal snapshots.
The qualified executor and typed current/durable readers consume those exact
records. They retain original descriptors and key/CSR bytes, bind the genuine
expired unused temporary certificate/session and permit a distinct temporary
certificate path and SID only after fresh full qualification. They cannot
replace an existing installation clock or regenerate CA/permanent identities.
All paths resolve under the selected canonical environment store through common
helpers, with 0700 directories and 0600 files; no alternate environment or
worktree fallback is permitted.

Online Device Root CRL recovery produces its qualified source and standalone
journal under `deployment/create/device-root-crl-renewal/<review>/`, and the
completion pointer `deployment/create/material/<parent>/device-root-crl-renewal.json`.
The repair executor consumes the exact original mount/key metadata, original
canonical human binding, revocation snapshot and consumer owners. The journal
records rotation intent, same-key signed successor, normal human import,
consumer proofs and its scoped lease. Unknown native outcomes require readback;
they never authorize another key, identity or fabricated completion. Later
qualification consumes the pointer and fresh native evidence. No private Device
signing key is exported or written locally. All producers use selected-environment
path helpers with 0700 directories and 0600 files, leaving original parent
journal and protected identity files intact.

Completion-only Device repair authority is produced below the original review at
`deployment/create/device-root-crl-renewal/<original-review>/completion-only/<fresh-review>/`.
Its `qualification.json` and `source-repair-journal.json` bind the current checker,
original artifact, stopped rotation intent/deadline and exact signed provider
readback. The executor and completion readers consume this proof through the
original repair journal's completion-review link. No producer rewrites original
qualification/source files, repeats rotation, exports a key or resets the
original execution deadline. These files use the same canonical environment
helpers and private permissions as the standalone repair.

Late historical-source reuse creates no disk cache or new authority artifact.
Its private in-memory snapshot binds canonical raw hashes, private modes and
producer-directory membership, with bounded file/byte counts. Reuse and GO/write
boundaries revalidate those inputs; native observations remain uncached. A save
or local source-snapshot/native write invalidates the shared snapshot. Recovery
admission checks the current manifest and held original owner/journal before
invalidating, writes its private source-owner/source-journal snapshots, and
rechecks ownership before creating an empty scope. The final authentication
reconstructs historical source in that scope; existing review-directory inputs
remain in the manifest. A later phase reconstructs its own authenticated scope. Returned objects are decoded separately, so a caller's
mutation cannot alter retained authority. Only a newly selected, unreferenced
`deployment/create/openbao-recovery/<review>/` output directory may be registered
as this command's producer; existing historical input directories cannot be hidden.


Admitted-only375 qualification writes its additional source-binding sidecar at
`~/.config/rtk_cloud/<environment>/deployment/create/openbao-recovery/<fresh-review>/managed-late-expired-unused-admitted-only.json`
through the common environment helpers (0700 directories, 0600 files). The
qualifier produces it; the qualified reader and immediate execution boundary
consume it together with fresh qualification bytes/current checker. It pins the
actual375 journal/owner, original admission/artifact/source, descriptor, same
prospective SID and original key/CSR/seed plan. Original artifacts, source
snapshots and admission receipt remain immutable. It grants no mint authority
without fresh six-gate GO and current no-mint/old-policy-role/certificate-absence
proof. No begun native outcome or installation clock is permitted in this scope.
The new command has one fixed native50 phase; no durable installation clock is
created or replaced.

Same-session remaining388 qualification produces
`~/.config/rtk_cloud/<environment>/deployment/create/openbao-recovery/<fresh-review>/managed-late-same-session-remaining.json`.
Its consumers are the qualified remaining reader and immediate execution
admission. It binds actual388 source, original owner/fence and producer review,
accepted owners/Log-stop child hashes, genuine seed/credential observation,
selected SID/fingerprint, original key/CSR, signed NotBefore and actual expiry.
Original descriptors/plans/source snapshots remain immutable.

Execution produces, through common environment helpers with O_EXCL and 0700/0600,
`~/.config/rtk_cloud/<environment>/deployment/create/<parent>/bootstrap/service/managed-late-remaining-authority.json`.
The remaining budget, native progress and finalization consumers verify its
same-byte canonical content and exact historical fresh-six-GO/checker binding.
It establishes a distinct remaining-only policy anchored to signed NotBefore,
with fixed min(NotBefore+120min, real expiry−5min, original caller deadline);
it does not reconstruct the old nondurable native50 clock. Retries reuse it
unchanged. Invalid/orphaned/ambiguous authority blocks; admission cannot replace
missing prior authority or reset its epoch. No second SID, key, CSR, signature,
policy/role, credential publication or accepted owner replay is permitted.

At exact completed389, the fresh remaining sidecar additionally binds actual389
as `current_source_sha256` and the freshly read accepted terminal helper as
`helper_observation_sha256`. The qualified reader and execution consume these
independent material pins with genuine nine-subject native installation proof.
The original388 descriptor digest, authority file, raw-qualified artifact,
authority review and deadline remain unchanged. Missing original authority is
blocking; this path cannot generate a replacement or recreate the accepted
helper. Final durable ACK evidence retains the original authority review/hash,
while accepted child journals and the immutable terminal observation prove the
helper retirement and original Log restoration.

A distinct installed-nine cleanup-only producer creates
`~/.config/rtk_cloud/<environment>/deployment/create/<parent>/bootstrap/service/managed-late-installed-cleanup-authority.json`
through the same private O_EXCL rules. Its consumers are cleanup admission,
fixed-budget/native progress checks and durable completion. Fresh full six-gate
qualification binds actual389/current owner, exact raw artifact/current checker,
genuine nine ACKs and the same terminal helper; original helper execution and
acceptance must be inside the original authority's admitted window. The receipt
pins that original authority/raw GO, immutable388 descriptor and original
SID/fingerprint/key/CSR alongside current389/helper evidence.

The new cleanup phase starts once at its actual admission, with fixed
min(admission+30min, original caller deadline, genuine expiry−5min), retaining
caller90, entry/finalizer15, finalizer cap15 and native life>20. Original
Remaining120 and its historical deadline are neither rewritten nor extended.
Only helper retirement, same original Log restoration, genuine proof,
same-session finalization and exact copy cleanup are authorized; no mint,
authorization, credential or accepted owner replay is allowed. Known completed
cleanup prefixes reuse the same marker; missing/changed/ambiguous state blocks
rather than creating another clock. Final proof must link both authorities and
the actual389/lease prefix, installation time and immutable helper observation.

Exact completed392 may produce a separate finalization-only receipt at
`~/.config/rtk_cloud/<environment>/deployment/create/<parent>/bootstrap/service/managed-late-finalization-authority.json`.
The full-six-GO/current-checker/raw-artifact producer and fixed-budget/native
progress/durable-completion consumers bind the actual392 owner/lease prefix,
original e2 identity and Log proof, and both historical authority/raw-GO hashes.
Genuine Log restoration and nine-ACK proof must have completed inside the prior
installed-cleanup authority. Prior receipts, files and deadlines remain unchanged.
The new private O_EXCL receipt starts one native50 phase at actual admission,
capped by its first caller90 deadline and genuine expiry−5min, with entry15,
finalizer15 and native life>20 unchanged. Existing or ambiguous state cannot
establish another epoch.

Only proof/CRL ACK checks, finalization of that same native session, tombstone
and exact credential-copy cleanup are authorized before the original full tail.
No seed, authorization, credentials, owners, helper or Log operation replays.
Durable completion must retain all three receipt/raw-artifact links and the
exact392/lease/Log-proof binding; missing markers or unknown/pending outcomes
block. Legacy embedded SQL session JSON is accepted only when its exact compact
or single PostgreSQL lexical form matches every immutable recorded raw hash;
new producers compact before hashing, without rewriting old evidence.
The remaining source also restores the exact original e0/e1 stale-copy observations
and historical five-ACK cleanup proof. Fresh native reobservation binds that proof
and the complete stale-copy set in the remaining descriptor/materials. Final
retirement rereads its raw-pinned remaining authority and original observations;
missing or changed evidence remains blocking. The target gate uses the existing
authenticated SID-only owner adapter for the accepted original OpenBao StatefulSet,
then retains strict body and readiness checks.

Historical terminal PVC references are observation evidence only; they do not
authorize deletion, ownership adoption or an exemption for active holders.

The remaining policy uses a fixed 120-minute signed-leaf anchor, selected before
its first admission; an admitted receipt cannot be extended. The caller's original
90-minute deadline and genuine expiry reserve remain additional clamps. Only two
read-only preparation boundaries avoid duplicate complete native inventories.
They retain held owner/fence, exact authority, fixed budget and pure full runtime
source/progress validation. Auth, seed budget, accepted-owner readiness and every
actual helper/retire/restore mutation still require their original fresh native
checks. No mutable observation is cached or reused as mutation evidence.

Admitted historical reuse is memory only. Its manifest captures actual canonical
current inputs while its pure source remains authenticated original374. Only
complete literal progress and fresh owner/fence checks promote a provisional
historical374/370 scope to the exact current journal. Saves and local seed/Log
artifact writes invalidate it; later boundaries reconstruct authority. Mutable
progress, child outcomes and native observations never become cached evidence.

Read-only OCI image diagnostics use private temporary
`deployment/tmp/runtime-image-probe-*` directories beneath the selected canonical
environment store. The diagnostic copies only that environment's GHCR pull
credentials into a 0600 Docker configuration, uses the installed Buildx plugin,
prints only status/digest metadata, and removes the directory on completion.
Raw credentials, manifests and Docker authentication data are not report output.

Terminal finalizer completion uses the same canonical environment root. Its producer writes `~/.config/rtk_cloud/<environment>/deployment/create/<parent>/bootstrap/service/managed-late-terminal-finalizer-authority.json` exclusively once after fresh full six-gate qualification of exact396. The private receipt pins raw qualification bytes, current checker, actual396 source/owner, original pending intent/native partial state and the three unchanged historical authorities. The separate cleanup deadline is fixed at first admission to30 minutes, capped by the first terminal caller's90-minute deadline and real leaf/session expiry minus5 minutes. It stores the old finalization overall cutoff as an unchanged historical link; retries cannot reset either recorded clock. The completed native adapter writes sibling `managed-late-terminal-finalizer-completed.json`, binding actual finalizer receipt/completion and original pending intent. Runtime and durable readers require these exact canonical files; missing, orphan, changed, public or symlink state blocks. All directories are0700 and files0600. The original393 mirror, temporary certs/keys and historical qualification bytes are never rewritten; native cleanup follows the existing verified tombstone contract. The admission producer first writes and syncs the byte-identical private sibling `managed-late-terminal-finalizer-admission.json` with O_EXCL, then writes and syncs the authority. Admission, runtime and durable readers require both canonical files to be present, private and byte-identical. An orphan, unreadable file or mismatch blocks without searching for a fallback or starting another clock; original bytes are retained for explicit reconciliation.


### Post-native cleanup authority paths

The exact400 post-native continuation uses only `~/.config/rtk_cloud/<environment>/deployment/create/<parent>/bootstrap/service/managed-late-post-native-cleanup-admission.json` and sibling `managed-late-post-native-cleanup-authority.json`. Admission is their sole producer after fresh full six-gate qualification, exact original source/tail authentication, and real native finalization/tombstone plus bootstrap-file/current-copy absence. It writes the witness first and authority second with O_EXCL, file/directory sync,0600 files and0700 directories. Source, admission, runtime and durable consumers require byte-identical regular private files at these paths; missing partial pairs, symlinks, public permissions or changed bytes fail closed without fallback or another clock.

Schema `rtk-cloud.create-managed-late-post-native-cleanup/v1` records a once-only30-minute local cleanup window,15-minute reserve at the first admission only, and the minimum of the current caller, unchanged first terminal caller deadline and genuine leaf/session expiry minus5 minutes. It preserves every earlier authority and deadline. Retrying the same admitted pair requires positive remaining time and a caller covering its unchanged deadline, with fresh full six-gate GO and the current checker; it cannot rewrite the pair or establish another reserve or clock. The supported writes are the remaining original historical credential retirements and the original pending finalizer receipt; native finalization, key/CSR access, custody cleanup and completed retirements are not replayed. Sibling `managed-late-terminal-finalizer-completed.json` records explicit post-native review/authority links and separates actual native completion within the old window from local completion within this new window. Diagnostic reports are observations and never substitute for these authorities or fresh native evidence.


Post-native target verification requires the exact session ACL policy to be absent through a successful authenticated bounded policy listing (or a verified native404); a denied or failed request is not absence. It retains the original shared Kubernetes-role body and receipt, recorded Controller/signer, and current OpenBao owner, process, init and audit checks. The native cleanup contract deletes the session policy; it does not delete the shared role.

### Residual-copy cleanup operator approval and grant data

After genuine native finalization, residual-copy recovery requires explicit
human approval of a concrete proposal SHA; a proposal, old timeout, missing state
or operator-file discovery is not execution authority. The approved scope is
the exact24 historical Secret IDs/UIDs, completion of original396, and the
unchanged FULL deployment/acceptance tail. New60-minute cleanup and90-minute
first-caller bounds are independent of, and never rewrite, historical authority
files/clocks. Finite credentials actually used retain a5-minute reserve.
A revoked/destroyed bootstrap leaf remains historical evidence; its
finalization must predate its original expiry. It is not renewed or used for
current authentication. A currently authenticated signed Kubernetes token
without expiry is represented explicitly, without inventing a NotAfter value.

Under the selected canonical
`deployment/create/<parent>/bootstrap/service/` directory:

| Private path | Producer | Consumer |
| --- | --- | --- |
| `managed-late-residual-copy-cleanup-proposal-60m.json` | Operator's finite proposal, containing original public object/UID/proof references | Explicit approval and residual source qualification; not authority |
| `managed-late-residual-copy-cleanup-operator-approval.json` | Operator records the actual human decision and proposal SHA | Residual approval/descriptor/authority loader; not GO/admission |
| `managed-late-residual-copy-cleanup-source-journal.json`, `...-source-owner.json` | Operator's exclusive exact-byte capture of approved source400 and owner | Raw/semantic ancestry and child-stage ownership validation; never substitutes for current fencing |
| `managed-late-residual-copy-cleanup-failed-run.json` | Operator's exclusive byte-for-byte capture of the actual failed result | Durable hash-bound failure validation, independent of later CLI-result overwrite |
| `managed-late-residual-copy-cleanup-grant-admission.json`, `...-grant-authority.json` | Freshly qualified admission, witness first, O_EXCL/file+directory sync | Private identical-pair runtime/durable verification and fixed60/90 clocks |
| `managed-late-terminal-finalizer-completed.json` | Actual original396 completion | Existing reader additionally checks residual review/authority and actual24 retirement receipts within the new grant window |
| `managed-late-residual-copy-cleanup-remaining-proposal-60m.json` | Operator prepares remaining original UIDs after authenticating completed receipts | Human review of a separate finite continuation; not approval, GO, grant or a new clock |
| `managed-late-residual-copy-cleanup-remaining-source-journal.json`, `...-remaining-failed-run.json` | Operator preserves exact actual partial-progress and failure bytes with exclusive creation | Remaining-only proposal review and subsequent explicitly approved qualification; no historical authority overwrite |
| `managed-late-residual-copy-cleanup-remaining-operator-approval.json` | Operator records the actual explicit human remaining21 approval | Remaining proposal/source403 descriptor reader; permission metadata, not GO or an execution grant |
| `managed-late-residual-copy-cleanup-remaining-grant-admission.json`, `...-remaining-grant-authority.json` | Fresh remaining-scope qualification/admission, witness first, exclusive and synced | Runtime/durable identical-pair readers preserve one new60/new first-caller90 bound and all prior grants |

The remaining phase requires its own explicit proposal approval and fresh full
six-gate current-checker GO before admission. Its source403 preserves the three
genuine retirements under the original residual60-minute window. Only the
remaining21 original UIDs and local original396 completion use the distinct new
window; the original396 intent/start and native terminal-window fact remain
unchanged. Existing terminal completion data links both grants and all24 real
receipts with their respective windows. New first admission reserves15 minutes,
finite current credentials retain5 minutes, and retries never extend clocks.

Completed428 normal-tail resumption uses a private in-memory qualification frame
and the existing canonical qualification/child-plan stores; it introduces no
new grant or export-artifact path. Its consumers bind the actual held journal,
durable terminal proof, original owner and lease ancestry, exact accepted later
children, current native evidence and the existing overall deadline. The
original live Account Manager `pkimanagement` container produces a read-only
`export-server` response from its original state PVC. The consumer verifies
ownership, approved image/mounts, registry/root/name and the original completed
certificate hash. Private PEM/key remain in process memory until the existing
private canonical child-plan and intended TLS Secret publication. Logs and
receipts retain public hashes and ownership references only. No replacement
helper, identity, completed receipt or separate private export file is allowed.

The operator-produced human timing view
`deployment/reports/dev-after-go-step-times.json` also remains under this
canonical environment root with0700 directories and0600 files. It derives only
public identity references, actual timestamps/durations and evidence hashes
from metrics and receipts, contains no credential values and grants no authority.

Temporary operator probe sources, Go overlay maps, checker-build metadata, CLI result snapshots, read-only process samples and
the closed-step timing runner for this dev continuation reside at
`deployment/repairs/dev-continuation-20261013/` beneath the same selected store.
The operator is their producer and consumer; they carry no deployment authority.
The optional public-only result of the actual read-only account export check is
`deployment/reports/dev-completed-tail-read-only-export-validation-<UTC>.json`,
produced by the operator and consumed by human timing/source review. It contains
status, certificate hash, unchanged-owner result and elapsed time, never the
private native response. All these diagnostic paths use0700 directories and
0600 files; synthetic test fixtures continue to use isolated roots.

Remaining-normal-tail artifacts use the same canonical environment store:

| Relative path | Producer | Consumer / meaning |
| --- | --- | --- |
| `deployment/repairs/managed-late-remaining-normal-tail-proposal-90m.json` | Operator prepares exact433 scope/evidence references | Explicit human review and proposal loader; no authority or clock |
| `deployment/repairs/managed-late-remaining-normal-tail-source-journal.json` | Operator exclusively preserves actual stopped433 bytes | Raw/semantic provenance loader; current fencing remains independent |
| `deployment/repairs/managed-late-remaining-normal-tail-operator-approval.json` | Operator records actual new human decision for that proposal SHA | Strict approval loader; not GO or an execution grant |
| `deployment/reports/dev-normal-tail-qualification-entry-diagnostic-<UTC>.json` | Operator records the actual early CLI error stage and closed timing-runner record when the CLI did not update its structured result | Human diagnosis only; identifies the unchanged historical result explicitly, preserves original source hash and reports no grant; never a replacement result, receipt or qualification |
| `deployment/reports/dev-ingress-dependency-order-failure-<UTC>.json` | Operator exclusively preserves actual failed CLI result | Proposal/source loader and timing review; not authority |
| `deployment/create/openbao-recovery/<review>/managed-late-remaining-normal-tail.json` | Fresh qualification emits bounded descriptor/material pins, including original raw kubeconfig and normalized selected-context credential hashes separately | Current-checker qualified/runtime readers verify both representations and current mode/expiry evidence; no duplicate ingress key export |
| `deployment/create/<parent>/bootstrap/bindings.json` | Completed bootstrap publishes validated public identity/owner references; normal-tail retry reuses identical private bytes read-only | Final target compilation compares independently observed bindings; a new write invalidates the prior source before exclusive publication and subsequent readers strongly reauthenticate current434 |
| `deployment/create/<parent>/bootstrap/service/managed-late-normal-tail-admission.json`, `managed-late-normal-tail-authority.json` | Qualified admission writes/syncs witness first and identical primary exclusively | Runtime/durable readers bind one fixed new first-caller90 deadline |
| `deployment/create/openbao-recovery/<fresh-review>/normal-tail-redis-durable.json` | Read-only current qualification emits separate Redis source/claim/helper intent | Fresh admission/runtime compare exact intent and original18 pins; no snapshot or identity creation |
| `deployment/create/<parent>/bootstrap/redis-durable/source.json` | Fenced migration exclusively preserves qualified descriptor, real434 journal and qualification hashes through a typed private4MiB writer; generic public metadata remains256KiB | Existing bounded4MiB strict migration source, allocation and reconciliation readers; unchanged JSON/source digests and original normal authority |
| `deployment/create/<parent>/bootstrap/redis-durable/source.rdb` | Fenced execution exports the saved exact original writer and exclusively archives private bytes | SHA-verified copy/recovery readers; never printed, regenerated from missing state or stored in configuration |
| `deployment/create/<parent>/bootstrap/redis-durable/volume.json` | Migration records the accepted new claim and actual Bound PV/CSI identity | Narrow newallocation proof and original18-preserving ledger readers |
| `deployment/create/<parent>/bootstrap/redis-durable/completed.json` | Migration publishes only after helper retirement and verified same-UID cutover | Typed completion/reconciliation and final ownership readers; no authority for replay |
| `deployment/create/<parent>/bootstrap/redis-durable/bindings.json` | Completed migration independently reobserves and publishes public identity/owner bindings | Final target compiler; original bootstrap bindings remain exact-byte historical evidence |
| `deployment/rollback/<helper-plan>/acceptance-recovery/attempt-*/original.json`, `completed.json` | Acceptance-only continuation exclusively preserves the genuine failed child journal, actual revalidation lease and successful acceptance outcome | Exact pre-pause recovery and operator reconciliation; preserves the original plan/resource write owners, and grants no resource recreation or new timeout |

The exact pre-pause437 recovery reader additionally consumes the existing
`source.json`, `volume.json` and original helper execution journal. It binds
actual437 qualification to the saved434 source and accepted claim, and may
complete only the original helper's failed acceptance after verifying its
genuine write receipt and recorded Pod UID. It does not republish these private
source/allocation files or replace an existing timeout pair. Missing artifacts,
in-flight writes or snapshot/cutover evidence cannot establish this profile.

The optional anchored total180 timeout supplement has distinct canonical paths:

| Relative path | Producer | Consumer / meaning |
| --- | --- | --- |
| `deployment/repairs/managed-late-normal-tail-timeout-supplement-proposal-total180m.json` | Operator prepares exact434 scope and original fixed-clock references | Human review and strict proposal loader; execution_authority:false |
| `deployment/repairs/managed-late-normal-tail-timeout-supplement-source-journal.json` | Operator exclusively preserves actual stopped434 bytes | Source/prefix and qualification readers |
| `deployment/repairs/managed-late-normal-tail-timeout-supplement-source-owner.json` | Operator exclusively preserves actual original owner bytes | Same-owner/source validation; never a replacement execution fence |
| `deployment/repairs/managed-late-normal-tail-timeout-supplement-operator-approval.json` | Operator records only the actual new human decision for that proposal SHA | Strict approval loader; not GO or a grant |
| `deployment/create/openbao-recovery/<fresh-review>/managed-late-normal-tail-timeout-supplement.json` | Fresh full qualification emits supplemental intent and credential pins | Current-checker admission and durable readers |
| `deployment/create/<parent>/bootstrap/service/managed-late-normal-tail-timeout-supplement-admission.json`, `managed-late-normal-tail-timeout-supplement-authority.json` | Fenced fresh-GO admission writes/syncs distinct witness then identical primary exclusively | Fixed anchored180 runtime/retry and historical window readers |

All use0700 directories and0600 files in the same selected environment store.
The original normal90 authority bytes and first-start clock remain unchanged.
Supplemental admission records time of authorization only. Its authority hash
also appears in the Redis saved-source and actual completion records; its
intent hash belongs to the qualified Redis descriptor. Neither missing state
nor an old human decision permits creating a supplemental grant.

An additional anchored total360 timeout extension uses distinct paths:

| Relative path | Producer | Consumer / meaning |
| --- | --- | --- |
| `deployment/repairs/managed-late-normal-tail-timeout-extension-proposal-total360m.json` | Operator prepares exact434 scope, original clock and admitted180 predecessor pins | New human review and strict extension loader; no execution authority |
| `deployment/repairs/managed-late-normal-tail-timeout-extension-source-journal.json` | Operator exclusively preserves actual stopped434 bytes | Exact source/qualification provenance |
| `deployment/repairs/managed-late-normal-tail-timeout-extension-source-owner.json` | Operator exclusively preserves original owner bytes | Same-parent/source checks; never a replacement fence |
| `deployment/repairs/managed-late-normal-tail-timeout-extension-operator-approval.json` | Operator records only the actual new decision for the360 proposal SHA | Strict loader; old180 approval does not qualify |
| `deployment/create/openbao-recovery/<fresh-review>/managed-late-normal-tail-timeout-extension.json` | Current full qualification emits extension intent and current credential pins | Current-checker admission and durable runtime verification |
| `deployment/create/<parent>/bootstrap/service/managed-late-normal-tail-timeout-extension-admission.json`, `managed-late-normal-tail-timeout-extension-authority.json` | Held execution publishes and syncs a distinct witness then identical primary exclusively after fresh six-GO | Fixed original-start+360 maximum and predecessor-bound retry/receipt readers |

These files use the same selected0700/0600 store. The original90 and admitted180
pairs remain exact-byte evidence; extension admission neither rewrites them nor
creates another start time. Active Redis intent and genuine source/completion
authority hashes bind the actually admitted extension. Source and owner must
remain authenticated, with caller/current credential clipping and15-minute
reserves unchanged. An absent, partial or different pair refuses.

Redis target runtime data remains on its separately proven claim: a private
retained copy at `/data/rtk-migration/source.rdb`, helper log/PID files under the
same private directory, and validated durable AOF at `/data/appendonlydir/`.
The isolated helper produces these; the destination Redis and reconciliation
readers consume them. Shell producers use umask077, and the archive directory
uses0700. Native runtime paths do not replace the independent canonical local
archive. A failed pre-switch source resumption makes target data stale; an
uncertain switch preserves evidence and refuses automatic emptyDir rollback.

This scope retains old authorities, original396/397 cleanup proof, accepted429
registration, accepted430–431 custody retirement and accepted433 ingress Secret.
It reuses private ingress material at
`deployment/create/material/<parent>/ingress-admission/identity.json`, with exact
hash, Secret UID/body and original pending432 intent/start. No old human approval
is repurposed; no old clock, key, identity or completed receipt is rewritten.
These paths use0700 directories/0600 files and introduce no `cloud_env/`,
worktree or cross-environment fallback. Final acceptance/release, independent
FULL post-health and original19/18 preservation remain mandatory.

All directories are0700 and files0600 beneath
`~/.config/rtk_cloud/<environment>/`. Approval/snapshots are operational
metadata and may not be placed in `cloud_env/`, a checkout, a worktree or an
external volume. Existing failed/superseded proposals and all prior authorities
remain intact. No fallback searches, receipt fabrication, PKI identity creation,
native finalizer replay or private bootstrap material regeneration is allowed.

## Staged create operational records

The remaining-program artifacts, frozen stage inputs, product native receipts
and fresh-product session reference use only the canonical environment store.
Their exact producers, consumers and paths are maintained in the
[staged create path table](cloud-deployment-architecture.md#staged-create-program-and-recovery-boundary).
Full target definitions can contain Secret bodies; every artifact remains private
(0700 directories, 0600 files), including qualification copies. No operational
record belongs in `cloud_env/`, a source checkout or a worktree.

Qualification does not generate identity or output receipts. Execution records
actual native issuer/session/certificate and resource ownership. A finalized
session's tombstone, deleted bootstrap credential and accepted steady child are
verified together; a JSON file alone cannot establish installation or cleanup.
Original Root custody, identities, credentials, PVCs, prior receipts and expired
execution approvals remain unchanged. Tests use isolated synthetic stores and
never write the real selected environment store.

Public HTTPS issuance uses the same canonical-store rule. The private producer
plan may contain an observed existing certificate/key pair; its qualification
artifact remains 0600. New ACME account/leaf keys, CSR, actual order response,
certificate material and native publication receipt use the public HTTPS paths
in the staged create path table. Retries retain the original account key, leaf key,
CSR and every order. Only a fresh authenticated terminal INVALID result permits
an append-only numbered attempt under that same producer. Its intent precedes
the request, its actual response follows, and the final receipt binds the winning
order and full attempt chain. The per-order propagation record also remains in
that canonical directory. Missing recorded identity, partial material or an
unknown order response is reconciled from original evidence; it does not
authorize replacement issuance.
DNS credentials and enabled SDK download credentials are read from the selected
operator store. SDK mirrors use exact canonical data and original recorded UID,
and absent selected SDK settings clear ambient shell values during rendering.


The operator's `deployment/repairs/global-flow-<date>/` build references,
remaining-execution description and final read-only helper, together with
`deployment/reports/dev-global-flow-*`, per-invocation `global-flow-<UTC>/` logs
and `deployment/reports/dev-flow-reorganization-<date>.md`, follow the exact
producer/consumer entries in the staged create path table. Latest-run timing
views contain stage names, real timestamps, durations, status and public evidence
references only. They select one uniquely matched metrics run and cannot issue
GO, reset admission deadlines or substitute for native receipts. These diagnostics
also use 0700 directories and 0600 files under the selected canonical store.
Exact qualified future-source integration uses `deployment/repairs/<repair>/future-program-qualified<file-count>-<source-prefix>/` for the previous source/checker archive, qualification logs, candidate source, selected actual reports, `future-build.json` and `integration-result.json`. The operator produces these only from the qualified package and completed deployment plus new FULL health evidence; subsequent source/build review consumes them. Use 0700 directories and 0600 files, and 0700 for the separately retained executable. The future-build record explicitly says `QUALIFIED_NOT_DEPLOYED`; it does not replace the deployed checker selector or original deployment reports. A partial integration records the files actually applied and requires inspection before continuing.

Independent final-reader results use the same private run directory as `final-proof-*.json`. The operator records actual read-only closure, identity, native volume and health results with explicit source report paths; these files are diagnostics, not execution authority or replacement receipts.

Per-run `step-timings.json` is derived from that invocation’s metrics, and an
optional bounded `history-read-sample*.txt` records local execution stacks for
operator diagnosis. Both stay in `deployment/reports/global-flow-<UTC>/`; only
redacted timing and relevant function names may be included in shared summaries.
Neither artifact is an admission, receipt or health result.

An explicit remaining-program total-window extension writes only the private
`deployment/create/executions/<parent>/execution-*/normal-tail-program-window.json`
supplement listed in the staged create path table, followed by contiguous
`normal-tail-program-window-0002.json` and subsequent files only for explicitly
larger totals. Each record binds the original Program, owner, qualification,
admission and anchor; later records also bind their predecessor's raw-file hash.
Runtime, history and final-verification consumers authenticate the whole chain
and use its latest fixed deadline. All previous bytes and the original start time
remain unchanged. Supplements use exclusive creation and sync, 0700 directories
and 0600 files under the selected environment store. Missing or changed evidence
cannot be replaced by a new retry clock.

Product session transport correction stores its immutable source at
`deployment/create/<parent>/bootstrap/product-identities/<scope>/service/session-forward.json`
under the same canonical environment store (0700 directories, 0600 file).
The producer records the original failed child, native owners, session intent,
current public CA trust and hashes of the existing key/CSR. The semantic recovery
reader and final allocation verifier consume those bindings and the genuine
accepted correction child. Private key/CSR contents stay on their original PVC;
the temporary read-only custody process has no credential or registry access.
It is retired before the next Job mounts the RWO claim. A read-only initialization
check verifies saved file hashes and private modes before native execution;
missing original files cannot fall through to bootstrap key generation. Existing
files are not subject to recursive fsGroup permission changes. The projected
service-account token is independently readable by the selected native UID on
its read-only mount; its audience, expiry and access are not widened to other
workloads. In-memory operator refresh uses the existing canonical credentials
and retains the exact operator identity, without persisting or logging bearer
tokens or replaying an uncertain request.
Failed Job and child journals remain historical failures. Missing original
custody cannot authorize identity regeneration, and the correction does not
replace or restart the admitted program's clock.

Same-intent product session retries record only public native proof and hashes at
`deployment/create/<parent>/bootstrap/product-identities/<scope>/service/session-forward-attempt-<previous-plan-sha256>.json`.
The producer appends each record after a definite pre-mint failure. Recovery and
final allocation verification consume the linked failed plans and accepted
winner; native failed journals remain unchanged. These files use the same private
canonical environment directories and never contain the original private key or CSR.


### Captured Billing runtime correction inputs

The original execution's
`normal-tail-runtime-configuration-source.json` is produced by
`deployment capture-runtime-configuration-source` before selected operator
payment settings change. It contains the full original private configuration,
release and deployment snapshot, including secrets, and binds the exact original
Program, owner, stopped plan and journal. The finite correction producer and
independent completion reader consume it; never print its contents or store it
under `cloud_env/`. Capture makes no native request and cannot grant a new
execution window.

`normal-tail-workload-forward-0002.json` records only the selected three provider
flags and three PayPal credential inputs together with the exact derived target.
The ordinary `normal-tail-stages/workload-forward-0002.json` input and
`deployment/rollback/<child-plan>/` journal record real execution. All these
files remain under the selected environment's canonical deployment store with
`0700` directories and `0600` files. Original snapshots, identities, plans,
journals and allocation evidence remain immutable. Runtime target, recovery,
lease, storage, final compilation and completion readers authenticate the same
explicit predecessor chain. External PayPal credentials must be supplied by the
operator; missing credentials never authorize generating them or disabling the
provider silently.

Operator repair reports, selected-input decisions, tested checker references and
continuation helpers stay in `deployment/repairs/global-flow-<date>/`. The repair
producer records `billing-runtime-repair-validation.prepared.json` before the
payment choice, then the genuine `billing-payment-choice.json` and executable
`billing-runtime-repair-validation.json` only once the selected inputs are
known. The continuation launcher consumes their hashes and the existing fixed
window; a prepared report cannot start deployment. Guarded helper replacement
preserves its original bytes under `retained-helpers/dev-final-readonly-<sha>.py`
for historical verification. These directories and files use `0700` and `0600`.

A separately approved remaining120 continuation uses the distinct
`dev-continuation-remaining120m-proposal.json`, genuine
`dev-continuation-remaining120m-operator-approval.json` and
`caller-window-remaining120.json` in that same canonical repair directory.
The paired remaining120 launcher and full-health wrapper consume one fixed
selection; the selector publishes it only at first actual approved invocation.
Earlier proposals, decisions, caller selections and native program windows stay
unchanged. These private records use the same `0700`/`0600` permissions.


### Versioned payment-key projection and repeated correction records

`runtime/payment-reference-encryption` remains the canonical authority. Newly
generated material uses 32 random bytes encoded with standard Base64. A valid
existing native key is preserved literally. The finite
`billing-payment-reference-sha256-v2` compatibility profile projects only the
recognized historical 24-byte URL-safe token shape through the original
SHA-256/token-to-Base64 recipe. It changes the Kubernetes mirror and consumer
checksums without replacing canonical material. The original raw projection is
retained for historical descriptor validation. Renderer, dependency preflight,
mirror health and missing-binding synchronization consume the same maintained
projection. Unknown formats cannot be repaired by generating replacements.

For later corrections, the existing producer writes
`normal-tail-runtime-configuration-source-<NNNN>.json` and
`normal-tail-workload-forward-<NNNN>.json` in the original execution directory.
Sources contain private effective snapshots and immutable predecessor hashes;
they must never appear in logs or `cloud_env/`. The ordinary stage-input file
and `deployment/rollback/<plan>/` hold genuine child execution. Recovery,
allocation, leases, final compilation and the independent completion reader
consume the full linked history. All directories remain `0700` and files `0600`.

`deployment/repairs/global-flow-<date>/time-extension-delegated-authorization.json`
records an explicit operator delegation; it does not itself change a deadline.
A necessary extension is documented as a delegated decision, retaining previous
approvals and windows. Tested repair manifests and `continue-dev-tested-repair.py`
bind the current checker, validation evidence, original deployment and an
existing fixed window. Deployment retries and independent FULL health share
that window. The launcher cannot create a replacement clock when its selected
record is missing. Sanitized startup-format, test-discovery and preservation
reports are produced and consumed in this same canonical repair directory.

Native Kubernetes API reads and writes produce an ephemeral frozen-identity copy
at `deployment/tmp/kubernetes-api-<numeric>/kubeconfig.yaml` in the selected
canonical environment store. The command consumes it and removes its own private
copy on exit; directories are `0700` and the file is `0600`. Concurrent whole-store
validation may tolerate ENOENT only for a previously enumerated, nonsymlink
native temporary directory or its exact regular kubeconfig copy. The environment,
`deployment/tmp` parent, all surviving permissions/symlinks, other entries and
errors, and every required canonical secret remain subject to the existing
checks. This does not authorize replacement of a missing canonical kubeconfig,
identity, deployment receipt or journal.

The `core-identity-*` repair artifacts under the selected environment's canonical `deployment/repairs/global-flow-20261019/` retain metadata and hash proofs for the bounded SGID directory repair. Producers and consumers are described in the deployment architecture. Use private `0700` directories and `0600` files; native identity contents are never copied into the diagnostic log. Only exact original file hashes and filesystem metadata are observed. The repair does not change file bytes, key ownership, identity or CA material, and its temporary Kubernetes Pods are removed after their outcome is preserved.


The product-registration CRL repair stores public CRLs and their original-issuer provenance in `deployment/create/executions/<parent>/<execution>/normal-tail-account-registration-crls.json`, with the ordinary preparation and rollback records described in the deployment architecture. Runtime0004 source and descriptor consume the accepted proof and dependency owners. The second temporary renewal stores `source.json`, `observed.json`, `completed.json`, `cleanup-source.json` and `cleanup-completed.json` under `deployment/create/<parent>/bootstrap/product-identities/<scope>/service/session-renewal-0002/`; it links and preserves the original `session-renewal/` records. All paths resolve through the selected environment store, with directories `0700` and files `0600`. Private key/CSR bytes remain on the original custody PVC until genuine finalization; local proof files contain hashes and ordinary scoped-plan material under the existing secret-store contract. Public diagnostic CRLs and redacted validation evidence use `deployment/repairs/global-flow-20261019/account-registration-current-crls/` and `account-registration-native-validation/`. No deployment-generated material belongs in `cloud_env/`.


New-program product registration additionally uses `deployment/create/<parent>/bootstrap/product-identities/<scope>/service/account-registration-crls.json`. Its producer runs after the genuine intermediate activation; its consumers are the ordinary successor-CRL preparation, semantic reader and installed-session restoration. It stores only public CRLs and exact issuer, owner, scope, Secret UID and capture-time provenance. Apply the same `0700` directory and `0600` file requirements. The current program scope is unchanged by the reader's in-memory proof binding; historical programs without the frozen Account Manager Secret do not gain a new implicit requirement.

### Optional directory recipe selection in renewal source records

`deployment/create/<parent>/bootstrap/product-identities/<scope>/service/session-renewal/source.json` may carry the non-secret `CertificateDirectoryProfile` selected when a new first-renewal source is qualified. The producer is the same-key first-renewal qualification; consumers are its source validation and Job recipe construction. The only new value is `private-directory-sgid-v1`; an omitted historical value retains the exact old recipe. The record stays in the selected environment store, in `0700` directories with a `0600` file, and remains immutable after publication. This profile changes only accepted directory metadata and does not authorize key/CSR replacement, private-byte rewriting, or identity regeneration. Second-generation source records do not carry this first-generation selector.


The scoped OpenBao convergence producer stores redacted validation, intent,
execution and preservation artifacts at
`deployment/repairs/global-flow-<date>/openbao-pod-convergence-<timestamp>/`.
It consumes the selected environment's original `openbao/init.json`,
`openbao/unseal-key` and `openbao/root-token` through the existing unseal lifecycle;
it never copies their values into logs or generates replacements. The ordinary
execution journal records genuine replacement-Pod unseal completion. The
continuation manifest and final preservation verifier consume the repair records.

Same-Root public CRL maintenance artifacts use
`deployment/repairs/global-flow-<date>/service-root-crl-maintenance-<timestamp>-<source>/`.
The original offline signer consumes existing encrypted custody; the maintenance
wrapper stores public request/signature/manifest, custody hashes, genuine import
receipt, ACK verification and outcome. Historical authority and CRL files remain
unchanged. Static-bundle completion consumes the actual original closure report
and preserves the existing Secret's private material. Both repair directories
use `0700` and files use `0600`, including public diagnostic artifacts. No
operational data is written under `cloud_env/`.


Historical CRL validation never edits the recorded `CapturedAt`, `QualifiedAt`,
signed PEM, native receipt or original plan to make it current. Existing private
provenance files remain immutable, even after their signed CRL expires. New
current publication has its own genuine receipt and consumer observations.
Qualification artifacts for the reader correction, exact source preimages,
selected checker, completed isolated export and independent verification are
produced under `deployment/repairs/global-flow-<date>/historical-crl-validity-qualified/`;
the common continuation launcher consumes their exact hashes. Preparation alone
is not successful qualification or native deployment completion. Directories
remain `0700`, files `0600`, and synthetic test credentials remain in isolated
fixture stores.
