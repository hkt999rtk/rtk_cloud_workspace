# Billing Staging Qualification Runbook

Status: active

Owner: `rtk_cloud_workspace`

Last reviewed: 2026-10-02 (OTA/Logger closeout boundary addendum)

Audience: internal test operators, maintainers, and delegated agents

This is the canonical operator runbook for the deployed Billing payment and
Cloud Admin portal qualification. Delegated agents should use the GitHub
Actions workflow on the latest `main`; local live commands are reserved for an
operator who already owns a matching staging runtime.

## Safety Boundary

The canonical entry point is `.github/workflows/billing-staging-qualification.yml`
in `hkt999rtk/rtk_cloud_workspace`. It resolves the official CI-published
images pinned by workspace `main`, verifies that every image exists remotely,
and performs one coordinated full-stack deployment before running dedicated
tests against the LKE `video-cloud-staging` stack. The full deployment is
required because ownership handoff enables Account Manager, Billing, Factory,
Video Control Plane and MQTT usage participants as one runtime boundary.
It also deploys the Billing settlement collector and waits for its rollout; the
collector alone may request the Video Cloud usage checkpoint through the
dedicated NetworkPolicy and shared settlement credential.
The run-scoped SecretStore drops legacy service-image pins from its copied
operator bundle so the remotely verified CI image manifest remains the only
deployment image source for the coordinated stack.
The run-scoped runtime is seeded with the tracked, non-secret staging
`environment.env`, so Account Manager email delivery and other service
settings use the canonical staging topology instead of an empty CI directory.
The workflow projects only the allowlisted Account Manager email settings into
the generated `stack.env`; the bearer credential remains in the job-only
SecretStore bundle.
It also applies the tracked architecture overrides, including capacity and
certificate algorithm policy, to the same run-scoped stack.
It also reuses the current deployed Video Cloud's non-secret blob endpoint and
region while requiring its bucket and prefix to match tracked `storage.env`.
This preserves the existing media location without copying object-store
credentials out of the job-only SecretStore.

Do not rotate shared PKI, reconcile DNS, delete the LKE cluster or node pools,
delete CI runners or artifact storage, use a legacy VM deployment path, cancel
an in-progress `staging-mutating-tests` run, or reuse an unrelated customer
identity. The qualification may mutate only its stable bootstrap Brand Cloud,
the audited staging-only member used to obtain a global account session, and
the fresh run-scoped Brand Cloud and payment state created by that member.

The stable bootstrap cloud exists only to provision or rotate the verified
qualification member through the audited staging-only admin endpoint. The
qualification never calls public registration and never bypasses public email
activation to create an owner. After global `/v1/auth/login`, the verified
member creates a fresh Brand Cloud through
`POST /v1/developer/brand-clouds`; that canonical operation makes the member
the cloud's sole owner. All Billing mutations and evidence for the run use that
fresh cloud, not the bootstrap cloud. Tenant Billing requests carry the exact
`owner_user_id` and `ownership_version` returned by that create response. The
runner waits only for the asynchronous Billing ownership/account projection;
invalid or stale ownership evidence fails immediately instead of being retried.
The Account Manager API and Billing receiver use a dedicated
`billing-cloud-creation` SecretStore credential for that projection. It must be
present on both services and must not reuse the service, internal, debit, or
ownership-handoff credential.

## Required Access And Repository Configuration

The delegated agent needs:

- read access to the repository and GitHub Actions plus permission to dispatch
  workflows;
- explicit authorization to mutate the shared staging environment;
- an authenticated `gh` CLI session for `hkt999rtk/rtk_cloud_workspace`.

The repository `staging` environment must already provide these values. The
agent verifies only that the workflow is configured and must never request,
read, print, copy, or store their values:

| Kind | Name | Purpose |
| --- | --- | --- |
| Secret | `LINODE_TOKEN` | Obtain the existing LKE kubeconfig without logging credentials. |
| Secret | `CI_RUNNER_GITHUB_WORK_KEY` | Initialize the pinned private submodules. |
| Secret | `RTK_CLOUD_SECRET_BUNDLE` | Materialize the existing staging SecretStore without printing values. |
| Secret | `RTK_CLOUD_MQTT_USAGE_SETTLEMENT_TOKEN` | Transitional isolated value for the new `mqtt-usage-settlement` catalog entry; must match the value incorporated into the next opaque bundle rotation. |
| Variable | `BILLING_STAGING_OTHER_ORG_ID` | Prove the Cloud Admin view cannot cross tenant boundaries. |
| Variable | `BILLING_STAGING_QUALIFICATION_ENABLED` | Set to `true` only when the scheduled live qualification is enabled. Manual dispatch does not require it. |

The workflow reads the existing `ghcr-pull` identity from the staging Account
Manager namespace after obtaining the kubeconfig, masks both fields, and uses
it only to verify and pull the official images. It does not require or create a
duplicate repository-level package credential.

Before dispatching, verify access and inspect recent runs:

```sh
BILLING_REPO=hkt999rtk/rtk_cloud_workspace
gh auth status
gh workflow view billing-staging-qualification.yml --repo "$BILLING_REPO"
gh run list --repo "$BILLING_REPO" \
  --workflow billing-staging-qualification.yml --limit 10
```

Do not proceed if another staging-mutating workflow is running or if the user
has not authorized a live staging mutation. Do not cancel another run to make
this one start sooner; the workflow concurrency group queues safely.

## 1. Run And Verify The Plan

Plan is the required first step and does not deploy workloads or execute live
payment mutations:

```sh
BILLING_REPO=hkt999rtk/rtk_cloud_workspace
gh workflow run billing-staging-qualification.yml \
  --repo "$BILLING_REPO" --ref main -f mode=plan

BILLING_PLAN_RUN_ID="$(gh run list --repo "$BILLING_REPO" \
  --workflow billing-staging-qualification.yml --branch main \
  --event workflow_dispatch --limit 1 --json databaseId --jq '.[0].databaseId')"
gh run watch "$BILLING_PLAN_RUN_ID" --repo "$BILLING_REPO" --exit-status
```

The plan passes only when the workflow validates the test catalog and prints
the `staging-live` sequence for the dedicated organization. Stop and report the
plan run URL if it fails; do not dispatch `mode=run`.

## 2. Run The Live Qualification

After the plan passes, dispatch the live run with the exact stack confirmation:

```sh
BILLING_REPO=hkt999rtk/rtk_cloud_workspace
gh workflow run billing-staging-qualification.yml \
  --repo "$BILLING_REPO" --ref main \
  -f mode=run -f confirm=video-cloud-staging-lke

BILLING_RUN_ID="$(gh run list --repo "$BILLING_REPO" \
  --workflow billing-staging-qualification.yml --branch main \
  --event workflow_dispatch --limit 1 --json databaseId --jq '.[0].databaseId')"
gh run watch "$BILLING_RUN_ID" --repo "$BILLING_REPO" --exit-status
gh run view "$BILLING_RUN_ID" --repo "$BILLING_REPO" \
  --json url,headSha,status,conclusion
```

For the first retained Cloud Logger billing inbox only, add
`-f initialize_billing_inbox=true`. This is an explicit one-time cutover flag;
normal and scheduled runs leave it false so missing retained storage fails
closed instead of silently creating a new financial stream.

The underlying runner also requires the fixed safety confirmation
`rtk-payment-simulator-qualification`. The workflow supplies it; an agent must
not substitute a different confirmation. This string authorizes only the
stable qualification bootstrap identity plus the fresh run-scoped Brand Cloud
created by the workflow; it is not the display name of a reusable Billing test
cloud.

## PASS Gate

A green deployment or a completed job is not sufficient. All of the following
must pass in the same run:

| Test ID | Required evidence |
| --- | --- |
| `LIVE-STG-SIMULATOR-001` | Hosted setup works over public TLS at desktop and mobile sizes, activates a synthetic method, and revokes it during cleanup. |
| `LIVE-STG-AUTOTOPUP-001` | One idempotently replayed debit threshold crossing produces exactly one automatic charge and credit. |
| `LIVE-STG-MANUAL-TOPUP-001` | A separate manual TWD 300 top-up reconciles exactly once. |
| `LIVE-STG-BILLING-DOCUMENT-001` | Qualification usage closes an immutable invoice and records PDF evidence. |
| `UI-CA-BILLING-STG-001` | Real staging overview remains tenant scoped and provider safe on desktop and mobile. |
| `UI-CA-BILLING-STG-002` | Real invoice detail serves the immutable PDF on desktop and mobile. |
| `UI-CA-BILLING-STG-003` | Billing activity and profile remain customer safe on desktop and mobile. |

The `Revoke ephemeral Cloud Admin customer session` step, payment policy/method
cleanup, evidence redaction, and `Upload sanitized qualification evidence` step
must also succeed. Any missing Test ID, missing screenshot/PDF, credential-like
artifact, failed cleanup, or missing artifact is a failed qualification.

### OTA and Logger closeout boundary (2026-10-02)

The seven Test IDs above qualify the existing payment and Cloud Admin Billing
workflow. They do not certify a new OTA/Logger rate card, Logger source
completeness, OTA direct-object delivery, or an OTA/Logger invoice. The
separate frozen staging closeout has applied Billing migrations 072 and 073,
retained Loki data on one 100 GiB PVC, preserved the original service options
of 32 Products during grant backfill, and completed the scoped Logger runtime
cutover. Its 11-row price comparison is a **technical review**, not a
publication or an effective customer price. The existing active five-row
card and four issued invoices remain the financial baseline. The newer Cloud
Admin image still needs publication and customer-view acceptance. The
tracked Factory public-entry flag has been set. The scoped Factory deployment
repair merged in PR #637: when public entry is selected, the normal Video
Cloud path requires Factory at the selected image. PR #638 allows only trailing
CR/LF differences in the canonical-to-live issuer PEM comparison; all runtime
values remain exact. Both live Factory Secret UIDs and their full raw data,
plus the local source files, must remain unchanged through rollout. This
merged safeguard passed the normal scoped staging rollout: 42 live Secret
snapshots, 83 resource snapshots, 84 operator entries and 62 local files were
preserved. Factory is on the selected Video Cloud image and its public
TLS/auth gate passed. The original production run and enrollment returned
HTTP 201/200, and one legacy certificate was issued. The separate strict
Product issuer check failed because `issuer_id`/Product fields were absent;
Product claim bindings remain zero. The deployed Factory/certissuer use the
[documented staging compatibility signer](../repos/rtk_cloud_contracts_doc/platform_pki.md#16-current-implementation-gap-and-migration),
so do not treat public transport readiness as formal Product CA issuer
qualification. The same legacy Device/key/CSR/certificate was then used in
one normal Account Manager provisioning operation; it succeeded without a new
scope, certificate, production run or enrollment. Twelve read-only checks and
a normal GET confirmed the same operation, its immutable succeeded outbox and
Video Cloud applied `device_logging`, `mqtt` and `ota` grant with seven-day
retention. The public Account Manager Device exposes
`applied_grant_revision`, while its filtered `metadata` omits bare grant
fields. An initial evidence helper expected those absent fields; local-only
config/artifact finalization exited successfully at
`2026-10-02T03:06:06Z` using the authoritative outbox and Video Cloud grant
read-back, without reposting. Keep this compatibility result separate from
formal Product signer qualification and OTA/Logger billing acceptance.

For Logger, an initial private helper stopped before MQTT connect because it
missed the renderer-derived public endpoint. A separate Python strict TLS
check rejected the legacy MQTT CA for missing AKI, while normal Go TLS
verification of the canonical CA and hostname passed; neither preliminary
attempt sent a log or demonstrated a deployed TLS failure. The corrected
unpublished-only continuation reused the same provisioned fixture, reached
Ready and received one QoS 1 PUBACK. Read-back at
`2026-10-02T03:32:42Z` verified one accepted 528-byte receipt, zero pending
receipts, Product sequence one, one matching Loki entry and the applied
grant's revision/digest/seven-day UTC expiry. The partial October source
outbox and Billing fact counts remained zero; no closed-month fact
acknowledgment or seal passed. This is accepted-source evidence only.

For OTA, the first controlled fixture's Admin preparation passed but its
device check returned HTTP 409 before any artifact token, event or download.
It was a legacy `ota/` release created through Admin `/v1/ota` while core
cutover was false; the independent billable API requires `ota-billable-v1`.
The scoped diagnosis found no OTA deployment, grant, task, download, object,
upload, source fact or Billing fact. The original campaign was canceled with
the normal API (HTTP 200) before cutover and is not adopted or repriced.
Normal core-cutover apply then passed, both selected Video Cloud API Pods
became Ready and the pinned independent-service read-back passed with the
canonical flag true. The original Product grant, Device and certificate were
retained. A new billable release was created through the cut-over core, then
its signed object PUT returned HTTP 204 and finalization returned HTTP 200.
The `artifact_write` source ACK and Billing fact each reached one. Publication
returned HTTP 409 because the original legacy release was still Published
while the new one was Ready, with neither release assigned to a device. Video
Cloud PR #747 contains the narrow legacy Published-to-Revoked repair and
is merged at `a9201728`; workspace PR #641 merged the scoped active OTA image
update at `2fde40e0`. The normal core-first and independent OTA rollout
completed on 2026-10-02 with matching selected images and preserved runtime
Secrets, grant, Loki claim and Logger source. All three affected Pods used
canonical digest
`sha256:562c62c4f800f5ad5998c3cfa0963f0e965db9d9fe7a21b6b5745c38be4133e1`.
They were Ready with zero restarts. One OTA startup readiness Warning occurred before
Ready and did not repeat during the 30-second observation; the public health
check passed using system TLS verification. The initial release's signed
manifest had expired during CI and cannot be replaced through normal Finalize.
A single normal revoke changed only the pre-cutover legacy release from
Published revision 3 to Revoked revision 4; the expired first billable release
stayed Ready revision 2, with its first acknowledged write and tracked object
preserved. The older legacy provider object remains physically present but
has zero billable receipt, task, download or fact.

A separate `1.0.3` release on the **same** Product, Device, certificate and
grant completed the normal 23-step OTA flow once. Its new billable object
produced a second `artifact_write`; assignment produced one `device_task`,
and the first verified 1 MiB download produced one
`successful_download_gib` fact at quantity 976563/scale 9. Direct object
Range/full reads returned HTTP 206/200 with the expected digest; no physical
installation was attempted. The 2026-10-02 10:20 UTC read-only aggregate saw
all four immediate source facts acknowledged with pending zero and four
Billing facts carrying the original Product grant. A later per-usage-ID
read-back matched the source and Billing IDs, hashes, quantity, unit, UTC
window and grant time, then an exact one-time replay of the original
sequence-3 downloaded body returned HTTP 200 with all seven selected source
and Billing rowsets unchanged. There are two *billable-ledger tracked*
objects (first billable release plus new release), while the older legacy
provider object is separately unbilled.

| Immediate OTA metric | Matched source / Billing facts | Recorded quantity |
| --- | ---: | --- |
| `artifact_write` | 2 / 2 | Two writes to separate retained billable releases |
| `device_task` | 1 / 1 | One device task |
| `successful_download_gib` | 1 / 1 | 976563 at scale 9 GiB, from one verified 1 MiB download |

All four facts are acknowledged, with zero pending. This does not include a
closed-month `artifact_storage_gib_month` fact or prove monthly storage billing.

The first strict replay after-check compared PostgreSQL captured times to a
different operator host clock and therefore retained a failed report despite
unchanged rows. Offline reconciliation of its original immutable packets
verified the causal order using the operator's own timestamps and file
creation sequence, without another POST. A no-client-certificate device
check returned Ingress HTTP 400 with verified server TLS and no source or
Billing change. Its original helper expected a visible `Server` header, but
the actual Ingress hides that header while its exact body says a certificate
is required and identifies nginx; the offline reconciliation preserved that
failed helper report and qualified the HTTP 400 outcome without another
request. These are scoped functional and nonbilling checks, not a formal
OTA price publication, full-month source seal, invoice or Product CA signer
qualification. The activation order remains independent service, strict
Product, authenticated edge and source schema, then core cutover, **before**
creating a paid Admin release.

The cutover core returns `503 OTA_DIRECT_ROUTE_REQUIRED` for device identity
`check`, deployment `events` and `artifact-token`; use the independent OTA
edge with direct mTLS for those APIs. Signed
`PUT /v1/device/ota/internal/upload/<release-id>` is the sole core device-path
forwarding exception: OTA validates the release-scoped expiring token and
current Product grant before reserving upload. It neither authenticates a
device nor forwards artifact GET/download. After Product grant and core
cutover read-back, create the billable release through core-forwarded
operator `POST /v1/ota/.../releases`, then use its signed PUT URL.

PR #639 retains the selected non-secret capacity profile across normal
deployment reloads and refuses a missing or stale canonical staging profile
before provider operations. A fresh zero-addition provider plan passed; the
normal rollout then restored all seven previously lost workload budgets. The
Factory read-back matched its new declared `100m` CPU and `128Mi` memory
request with a `128Mi` memory limit. The post-rollout Product PKI prerequisite,
Loki retention/mount and Logger source checks, plus issuer mTLS, passed. These
results do not certify OTA/Logger billing acceptance.

Logger source coverage began at the actual
`2026-10-01T20:26:25.633349Z` migration clock. The source-schema maintenance
record contains an initial GO-assertion exception and a subsequent read-only
recovery verification; it must not be reported as a fully passed initial
writer-pause gate. October cannot be certified as a full source month.
November is the first possible complete month if coverage remains continuous
and all Product/owner gates pass. Its Logger seal cannot complete before
`2026-12-02 00:00 UTC`; OTA's 48-hour grace makes
`2026-12-03 00:00 UTC` the earliest possible combined November close. A
missing seal means incomplete evidence, not zero usage. Do not manufacture
an October seal or mark OTA/Logger charge acceptance PASS from this payment
qualification run. At `2026-10-02T02:43:44Z`, the normal
`deployment logger-period-seal --month 2026-10` invocation, without
`--confirm`, refused the open UTC month with `month must be YYYY-MM and closed
for at least 24 hours in UTC` before configuration, provider, Job or database
writes. It proves the live time guard only, not source completeness or a real
month seal. An initial denied-before Logger helper stopped on a variable-name
regex before sending an event. The corrected normal probe then obtained an
mTLS request token and received exact HTTP 403 for one rejected Logger POST.
Its verified before/after read-back at `2026-10-02T03:17:29Z` found zero
scoped event, receipt, source outbox row and Billing fact, plus zero entries in
the bounded Loki query; source identity was unchanged. This qualifies only
this rejected request's nonbilling path, not a complete-month source seal. The
current
[Logger seal guard](../repos/rtk_billing/internal/billingstore/logger_period_seals.go)
checks full-month source coverage and reconciled facts; the
[invoice close guard](../repos/rtk_billing/internal/billingstore/invoices.go)
holds an incomplete month. Record the separately reviewed financial approval,
published future version, source/seal high-water, accepted/rejected fact
reconciliation and first actual invoice as distinct follow-on evidence.

## Evidence And Handoff Record

The workflow uploads
`billing-staging-qualification-<github-run-id>-<run-attempt>` for 90 days. The
run-scoped files include:

```text
.artifacts/test-runs/billing-staging-<github-run-id>-<run-attempt>/
  payments/staging-live/test_report.md
  payments/staging-live/qualification-context.json
  payments/staging-live/evidence/
  cloud-admin-billing/
```

Download the sanitized artifact when a local review is required:

```sh
BILLING_REPO=hkt999rtk/rtk_cloud_workspace
gh run download "$BILLING_RUN_ID" --repo "$BILLING_REPO" \
  --pattern "billing-staging-qualification-${BILLING_RUN_ID}-*" \
  --dir ".artifacts/review/billing-staging-${BILLING_RUN_ID}"
```

The final handoff record must contain the immutable run URL, `main` commit SHA,
conclusion, all seven Test ID results, cleanup/session-revocation status,
artifact name, and the first failing step when applicable. Never paste raw
workflow logs or artifact contents containing suspected credentials into chat.

## Failure Triage

| Failing step | Check first | Required action |
| --- | --- | --- |
| Initialize private sources | Repository key configuration and pinned submodule reachability. | Report the step and run URL; do not replace pinned commits. |
| Resolve official pinned images | Canonical release workflow result, GHCR read permission and exact `sha-<commit>` tag. | Repair or wait for the normal service release workflow; never build, push or retag a staging image here. |
| Acquire staging kubeconfig | `LINODE_TOKEN`, cluster label, and Linode API status. | Do not print the response or create a replacement cluster. |
| Deploy coordinated stack | Preflight output, rollout events, Account Manager handoff and cloud-deletion workers, Billing and Admin readiness. | Do not rotate PKI, reconcile DNS, or narrow the full-stack deployment while ownership handoff is enabled. |
| Billing staging qualification | The first failed live Test ID, run-scoped Brand Cloud state, cloud-creation outbox/worker, Billing worker, and ledger correlation. | A projection timeout requires restoring the paired cloud-creation transport; do not merely lengthen the timeout. Confirm payment cleanup ran before considering a rerun. |
| Cloud Admin Billing smoke | Ephemeral session creation, organization/invoice context, desktop/mobile screenshots, and public endpoint health. | Confirm the logout step ran; never preserve or reuse the session. |
| Session revoke or evidence upload | Logout response, payment cleanup report, redaction result, and artifact presence. | Treat the run as failed and escalate cleanup status before any rerun. |

Do not retry blindly. A rerun is allowed only after recording the cause and
confirming that the prior run revoked its ephemeral session and left no active
payment policy or method in the dedicated organization. Never manually clean up
by deleting shared namespaces, storage, clusters, DNS, CI runners, or unrelated
test identities.

## Agent Handoff Prompt

```text
In hkt999rtk/rtk_cloud_workspace, use the latest main and the canonical
billing-staging-qualification.yml GitHub Actions workflow. Verify gh access and
recent runs, then dispatch mode=plan and wait for it with --exit-status. Only if
the plan passes and no conflicting staging mutation is active, dispatch
mode=run with confirm=video-cloud-staging-lke and monitor it to completion.

Do not run local staging mutations, rotate shared PKI, reconcile DNS, delete
LKE/CI/artifact resources, cancel another staging-mutating-tests run, reveal
secrets, or substitute another organization for
rtk-payment-simulator-qualification.

PASS requires LIVE-STG-SIMULATOR-001, LIVE-STG-AUTOTOPUP-001,
LIVE-STG-MANUAL-TOPUP-001, LIVE-STG-BILLING-DOCUMENT-001,
UI-CA-BILLING-STG-001, UI-CA-BILLING-STG-002, and
UI-CA-BILLING-STG-003, plus payment cleanup, customer-session revocation,
redaction, and sanitized artifact upload. Return the run URL, main SHA, seven
case results, cleanup status, and artifact name. On failure, do not blindly
retry; report the first failed step, safe error summary, evidence availability,
and whether cleanup/session revocation completed.
```
