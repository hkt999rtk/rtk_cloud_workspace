# Linode CI Runner Governance

Status: workspace source document for RTK Cloud self-hosted CI runner VMs.

This document defines the shared Linux X64 runner topology used by GitHub
Actions. The default topology concentrates CI capacity on one large host with a
fixed organization-wide runner-slot budget. CI runner hosts are not intended to
stay on permanently. The operator flow is:

1. Boot the shared Linode Linux CI runner VM before CI.
2. Wait for GitHub runners to become online.
3. Run or rerun the target CI jobs.
4. Archive GitHub Actions artifacts to Linode Object Storage.
5. Shut down the Linode CI runner VM.

The workflow is intentionally external to GitHub Actions. A self-hosted job
cannot boot its own runner VM because the runner must already be online before
GitHub can start the job.

## Goals

- Unblock repo CI jobs that require self-hosted runners.
- Consolidate Linux validation jobs onto one shared runner VM.
- Keep deployment, production, and environment-coupled workloads isolated from
  validation CI.
- Avoid storing deployment, production, or customer secrets on CI hosts.
- Store CI outputs durably in Linode Object Storage after runs complete.
- Keep runner VM cost bounded by shutting the shared VM down when CI is idle.

## Runner Topology

The Linux validation profile uses one large shared host. It runs a fixed number
of organization-scoped GitHub runner registrations; each registration is one
execution slot. All approved repositories share those slots through the same
`self-hosted`, `Linux`, and `X64` capability labels.

| Host label | Registration scope | Runner names | Runner labels | Capacity rule |
| --- | --- | --- | --- | --- |
| `rtk-shared-linux-ci` | organization `hkt999rtk` | `rtk-ci-linux-01` through the configured slot count | `self-hosted`, `Linux`, `X64` | Fixed from measured CPU, memory, Docker, and storage capacity; adding a repository never adds a slot. |

The shared host is for Linux validation only. Do not register CD/deploy labels
on it, including `account-manager-cd`, `video-cloud-cd`, or
`rtk_cloud_frontend`, `go`, `cd`, `website-test`. Keep those runners on
environment-specific deployment hosts.

`rtk_video_cloud` validation currently uses GitHub-hosted runners for its main
CI, release, deploy, and integration workflows. If Video Cloud Docker-heavy
integration is moved to Linode later, use a separate heavy runner profile rather
than adding it to `rtk-shared-linux-ci`.

## Cost And Lifecycle Policy

- Runner VMs may remain provisioned, but they should be powered off when no CI
  is running.
- Boot the shared runner before triggering or rerunning PR CI.
- Shut the shared runner down only after required artifacts have been copied to
  Linode Object Storage.
- Do not destroy runner VMs for normal idle periods; destroying them forces
  runner re-registration and loses host-level package/cache warmup.
- If a runner is suspected compromised, delete and rebuild the VM instead of
  preserving local state.


## Shared Linux Policy

The scripts accept only `CI_RUNNER_PROFILE=shared-linux` when a profile is set.
The default is the shared Linux profile.

The consolidation boundary is intentionally narrow:

- Validation CI for Account Manager, Cloud Admin, Cloud Frontend, Cloud Client
  Linux, and Cloud Logger can share one VM.
- CD/deploy labels remain outside this profile because they are coupled to
  staging hosts, sudo, service state, or website-test runtime state.
- macOS, iOS, Android, device-lab, and hardware validation are outside this
  Linux profile.
- Docker state and workspaces still need routine cleanup because multiple repos
  share the same VM.

## Security Boundary

- CI VMs must not store production deployment secrets.
- CI VMs must not store long-lived customer, device, or certificate-issuer key
  material.
- Runner registration tokens are short-lived and should be fetched by the
  operator script immediately before bootstrap.
- SSH ingress must be restricted to operator CIDRs.
- The runner process runs as an unprivileged `github-runner` user.
- Docker-enabled runners should be treated as privileged CI hosts; do not reuse
  them for production workloads.
- GitHub Actions workspaces are disposable. Runner bootstrap should configure
  cleanup or rely on repo workflows that clean `$GITHUB_WORKSPACE`.

## Local Secret Inputs

Provisioning reads secrets from operator-local environment variables or from the
ignored workspace secret tree defined by
[`deployment-secrets-governance.md`](deployment-secrets-governance.md).

Recommended local files:

```text
.secrets/shared/linode/env/ci-runners.env
.secrets/shared/github/env/runner-registration.env
.secrets/shared/ssh/private-keys/rtk-ci-runner
.secrets/shared/ssh/public-certs/rtk-ci-runner.pub
.secrets/shared/ssh/private-keys/github-work
```

Required values:

| Variable | Purpose |
| --- | --- |
| `LINODE_TOKEN` | Linode API token with instance/firewall permissions. |
| `GITHUB_TOKEN` | GitHub token allowed to create organization self-hosted runner registration tokens. |
| `CI_RUNNER_ALLOWED_SSH_CIDRS` | Comma-separated operator CIDRs allowed to SSH to runner VMs. |
| `CI_RUNNER_PUBLIC_KEY_PATH` | SSH public key installed on the runner VMs. |
| `CI_RUNNER_SSH_KEY` | SSH private key used by the provision script after VM creation. |
| `CI_RUNNER_GITHUB_WORK_KEY_PATH` | GitHub SSH key used by CI runners to fetch private `git@github.com-work:` submodules. |
| `LINODE_OBJ_BUCKET` | Linode Object Storage bucket for archived CI artifacts. |
| `LINODE_OBJ_ENDPOINT` | Linode Object Storage S3-compatible endpoint. |

Optional values:

| Variable | Default |
| --- | --- |
| `CI_RUNNER_REGION` | `us-sea` |
| `CI_RUNNER_IMAGE` | `linode/ubuntu24.04` |
| `CI_RUNNER_STATE_DIR` | `.secrets/shared/linode/state/ci-runners` when `.secrets` exists, otherwise `.artifacts/linode-ci-runners/state` |
| `CI_RUNNER_VERSION` | latest GitHub Actions runner release discovered on the VM |
| `CI_RUNNER_GITHUB_WORK_KEY_PATH` | `$HOME/.ssh/id_ed25519_github_work` |

## First-Time Provisioning

Provision the host infrastructure once, or when rebuilding the shared runner:

```sh
export WORKSPACE=/path/to/rtk_cloud_workspace
set -a
. "$WORKSPACE/.secrets/shared/linode/env/ci-runners.env"
. "$WORKSPACE/.secrets/shared/github/env/runner-registration.env"
set +a

go run ./scripts/go/rtk-cloud -- ci-runners provision
```

The provisioning command creates the host and firewall. The operator bootstrap
on that host must then:

1. Wait for SSH readiness.
2. Fetch fresh organization runner registration tokens for the configured fixed slots.
3. Install Go, Node.js 22, Docker, Docker Compose, CMake, Ninja,
   Chrome when available, build tools, and the GitHub Actions runner.
4. Register the fixed organization-scoped runner slots with the common capability labels.
5. Write local ignored state containing Linode ids, public IPs, runner names,
   and slot mappings.

Runner registration tokens are never persisted. Rebuilding the host requires
fresh tokens and replacement of the old offline registrations.

## Current Workspace CI Boundary

Pull requests, pushes to `main`, and explicit workflow dispatches use the shared
Linux X64 capability pool. Linux jobs select
`[self-hosted, Linux, X64]`; workflows must not name an individual runner such
as `ci-0`. GitHub may assign any online runner that reports all three labels.
Changed-path selection still limits automatic pull-request work to affected
workspace, service, catalog, gitlink, and UI jobs. Unrelated coverage modules
and integration services are skipped.

The same capability policy applies to service-repository Linux CI. macOS, ARM,
hardware, deployment, and staging-mutating jobs retain their dedicated labels
and authorization boundaries. If fewer Linux X64 runners are online, jobs queue
until matching capacity is available; workflows must not work around an offline
runner by naming another machine.

The preferred topology is one large Linux X64 host with a deliberately bounded
set of organization-scoped runner slots shared with the approved repositories.
Each registration is one concurrent execution slot, so a large host may run
several registrations, but the slot count is derived from the host CPU, memory,
Docker, and storage budgets. Do not create one registration per repository:
that makes concurrency grow whenever another repository is added and bypasses
the host capacity budget. GitHub schedules registrations rather than physical
machines and does not infer that identical names, labels, or addresses share
resources.

The large host is a single failure and I/O domain. Its runner-slot services must
share an explicit systemd/cgroup budget, Docker data must live on monitored
storage, and the host thin-pool data and metadata percentages must be alerted
before either is exhausted. Adding a repository must not add another slot.

Workflow fan-out is also bounded. Pull-request updates cancel an older run of
the same workflow and PR, Go unit coverage runs at most two matrix entries at a
time, JavaScript coverage runs one entry at a time, and desktop/mobile browser
qualification runs serially. These limits reduce burst load; they do not replace
the fixed organization-wide slot budget.

The runner lifecycle remains external to GitHub Actions. Pull-request workflows
do not boot a runner VM or require a locally injected Linode token. Explicit
staging qualification keeps its separate authorization and secret boundary.

An Object Storage artifact is not evidence that `rtk-shared-linux-ci` was
running. Check the artifact manifest fields such as `workflow`, `run_id`,
`run_attempt`, runner/job logs, and `source_commit` to identify the workflow that
built and uploaded it.

There is currently no active workspace GitHub Actions workflow that automatically
orchestrates the shared Linode CI runner lifecycle. Use the manual runner session
commands below when a job explicitly needs the `rtk-shared-linux-ci` VM.

Required environment variables for local manual sessions, or equivalent GitHub
Actions secrets if a runner lifecycle workflow is reintroduced:

| Secret | Purpose |
| --- | --- |
| `RTK_CI_GITHUB_TOKEN` | PAT or GitHub token with permission to rerun/watch runs and download artifacts in the target private repos. |
| `LINODE_TOKEN` | Linode API token with instance boot/shutdown permissions. |
| `LINODE_OBJ_ACCESS_KEY_ID` | Linode Object Storage S3-compatible access key. |
| `LINODE_OBJ_SECRET_ACCESS_KEY` | Linode Object Storage S3-compatible secret key. |
| `LINODE_OBJ_BUCKET` | Linode Object Storage bucket for archived CI artifacts. |
| `LINODE_OBJ_ENDPOINT` | Linode Object Storage S3-compatible endpoint. |

Manual runner session flow:

1. Collect one or more target GitHub Actions run ids:
   - `--account-run-id` for `hkt999rtk/rtk_account_manager`.
   - `--admin-run-id` for `hkt999rtk/rtk_cloud_admin`.
   - `--frontend-run-id` for `hkt999rtk/rtk_cloud_frontend`.
   - `--client-run-id` for `hkt999rtk/rtk_cloud_client`.
   - `--logger-run-id` for `hkt999rtk/rtk_cloud_logger`.
2. Keep `--rerun true` when re-running failed or queued PR checks.
3. Keep `--shutdown-policy always` for normal use.

`rtk-cloud ci-runners run-session` runs this sequence:

```text
go run ./scripts/go/rtk-cloud -- ci-runners power start
go run ./scripts/go/rtk-cloud -- ci-runners wait-online
gh run rerun/watch for each provided run id
go run ./scripts/go/rtk-cloud -- ci-runners archive-artifacts for each run id
go run ./scripts/go/rtk-cloud -- ci-runners power stop
```

The archive script downloads GitHub Actions artifacts and run metadata, then
uploads them to Linode Object Storage under:

```text
ci-runs/<owner>_<repo>/<run-id>/
```

`rtk-cloud ci-runners archive-artifacts` uploads through the Go Object Storage
client against the Linode S3-compatible endpoint.

### Operator Fallback

If GitHub Actions itself is unavailable, an operator can run the same lifecycle
locally with equivalent environment variables loaded:

```sh
go run ./scripts/go/rtk-cloud -- ci-runners run-session \
  --account-run-id <run-id> \
  --admin-run-id <run-id> \
  --frontend-run-id <run-id> \
  --client-run-id <run-id> \
  --logger-run-id <run-id> \
  --rerun true \
  --shutdown-policy always
```

## Verification

Check Linode VM power state:

```sh
go run ./scripts/go/rtk-cloud -- ci-runners power status
```

Check registered runner status:

```sh
go run ./scripts/go/rtk-cloud -- ci-runners list
```

Expected online runners before CI starts:

| Repo | Expected online runner |
| --- | --- |
| `rtk_account_manager` | `rtk-ci-account-manager` with `account-manager-ci` |
| `rtk_cloud_admin` | `rtk-ci-cloud-admin` with `rtk-cloud-admin-ci` |
| `rtk_cloud_frontend` | `rtk-ci-cloud-frontend` with `rtk_cloud_frontend`, `go` |
| `rtk_cloud_client` | `rtk-ci-cloud-client-linux` with `client-sdk-ci` |
| `rtk_cloud_logger` | `rtk-ci-cloud-logger` with `rtk-cloud-logger-ci` |

After runners are online, queued jobs should move from `queued` to
`in_progress` and show the runner name.

## Operations

- Upgrade packages with normal Ubuntu security updates.
- Rotate GitHub runner registration by removing and re-registering the runner.
- Rebuild the VM instead of trying to preserve local CI state after suspicious
  activity.
- If Docker disk usage grows, prune Docker state only on the affected runner VM.
- Keep VM labels stable so GitHub runner names and Linode hostnames remain easy
  to correlate.

## Deprovisioning

Before deleting a VM, remove the runner from the GitHub repository settings or
through the GitHub API, then delete the Linode VM and firewall. Preserve the
ignored local state file for audit until replacement runners are verified.
