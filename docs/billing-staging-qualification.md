# Billing Staging Qualification Runbook

Status: active

Owner: `rtk_cloud_workspace`

Last reviewed: 2026-09-01

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
fresh cloud, not the bootstrap cloud.

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
| Deploy coordinated stack | Preflight output, rollout events, Account Manager handoff worker, Billing and Admin readiness. | Do not rotate PKI, reconcile DNS, or narrow the full-stack deployment while ownership handoff is enabled. |
| Billing staging qualification | The first failed live Test ID, run-scoped Brand Cloud state, Billing worker, and ledger correlation. | Confirm payment cleanup ran before considering a rerun. |
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
