# Linode CI Runner Governance

Status: workspace source document for RTK Cloud self-hosted CI runner VMs.

This document defines the Linode CI runner topology used when GitHub Actions
jobs require repo-specific self-hosted labels. CI runner VMs are not intended to
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

The Linux validation profile uses one shared Linode VM with multiple
repo-scoped GitHub runner registrations. This keeps existing repository
permissions and labels intact while reducing the number of powered Linode
instances.

| VM label | GitHub repo | Runner name | Runner labels | Recommended type | Reason |
| --- | --- | --- | --- | --- | --- |
| `rtk-shared-linux-ci` | `hkt999rtk/rtk_account_manager` | `rtk-ci-account-manager` | `self-hosted`, `Linux`, `X64`, `account-manager-ci` | `g6-standard-4` | Account Manager CI uses Go tests and PostgreSQL service containers. |
| `rtk-shared-linux-ci` | `hkt999rtk/rtk_cloud_admin` | `rtk-ci-cloud-admin` | `self-hosted`, `Linux`, `X64`, `rtk-cloud-admin-ci` | `g6-standard-4` | Admin CI uses Go and Node 22/npm frontend validation. |
| `rtk-shared-linux-ci` | `hkt999rtk/rtk_cloud_frontend` | `rtk-ci-cloud-frontend` | `self-hosted`, `Linux`, `X64`, `rtk_cloud_frontend`, `go` | `g6-standard-4` | Frontend CI is Go-based and can use Chrome/Chromium for visual smoke. |
| `rtk-shared-linux-ci` | `hkt999rtk/rtk_cloud_client` | `rtk-ci-cloud-client-linux` | `self-hosted`, `Linux`, `X64`, `client-sdk-ci` | `g6-standard-4` | Client Linux CI needs Python, CMake/Ninja/CTest, and Node/npm. |
| `rtk-shared-linux-ci` | `hkt999rtk/rtk_cloud_logger` | `rtk-ci-cloud-logger` | `self-hosted`, `Linux`, `X64`, `rtk-cloud-logger-ci` | `g6-standard-4` | Logger CI is a lightweight Go package validation. |

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
| `GITHUB_TOKEN` | GitHub token allowed to create repo self-hosted runner registration tokens. |
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

Run this once, or when rebuilding a runner VM:

```sh
export WORKSPACE=/path/to/rtk_cloud_workspace
set -a
. "$WORKSPACE/.secrets/shared/linode/env/ci-runners.env"
. "$WORKSPACE/.secrets/shared/github/env/runner-registration.env"
set +a

scripts/linode-ci-runners/provision-ci-runners.sh
```

The script:

1. Creates the missing shared Linode VM and firewall.
2. Waits for SSH readiness.
3. Fetches a fresh GitHub runner registration token per repository.
4. Installs Go, Node.js 22, Docker, Docker Compose, Python, CMake, Ninja,
   Chrome when available, build tools, and the GitHub Actions runner.
5. Registers repo-scoped runners on the shared VM with the required labels.
6. Writes local ignored state containing Linode ids, public IPs, runner names,
   and repo mappings.

The script is idempotent for already existing VM labels. If a label already
exists, it reuses the VM and re-runs bootstrap.

## Normal CI Run Procedure

The normal path is the GitHub-hosted workspace workflow
`.github/workflows/linode-ci-orchestrator.yml`. It solves the self-hosted runner
bootstrapping problem by running orchestration on `ubuntu-latest`, not on the
Linode runner that is still powered off.

Required GitHub Actions secrets in `rtk_cloud_workspace`:

| Secret | Purpose |
| --- | --- |
| `RTK_CI_GITHUB_TOKEN` | PAT or GitHub token with permission to rerun/watch runs and download artifacts in the target private repos. |
| `LINODE_TOKEN` | Linode API token with instance boot/shutdown permissions. |
| `LINODE_OBJ_ACCESS_KEY_ID` | Linode Object Storage S3-compatible access key. |
| `LINODE_OBJ_SECRET_ACCESS_KEY` | Linode Object Storage S3-compatible secret key. |
| `LINODE_OBJ_BUCKET` | Linode Object Storage bucket for archived CI artifacts. |
| `LINODE_OBJ_ENDPOINT` | Linode Object Storage S3-compatible endpoint. |

Pull request validation:

1. PRs that change `.github/workflows/linode-ci-orchestrator.yml`,
   `scripts/linode-ci-runners/**`, or this document trigger the orchestrator.
2. The PR run uses smoke-only mode:
   - boot the shared Linode Linux CI VM.
   - wait until each GitHub runner is online.
   - shut the shared CI VM down.
3. Smoke-only mode does not rerun service CI and does not archive artifacts,
   because no target service run id exists during workspace PR validation.
4. The PR run requires only `RTK_CI_GITHUB_TOKEN` and `LINODE_TOKEN`.

Manual orchestrator flow:

1. Open the workspace `Linode CI Orchestrator` workflow.
2. Enter one or more target GitHub Actions run ids:
   - `account_run_id` for `hkt999rtk/rtk_account_manager`.
   - `admin_run_id` for `hkt999rtk/rtk_cloud_admin`.
   - `frontend_run_id` for `hkt999rtk/rtk_cloud_frontend`.
   - `client_run_id` for `hkt999rtk/rtk_cloud_client`.
   - `logger_run_id` for `hkt999rtk/rtk_cloud_logger`.
3. Keep `rerun=true` when re-running failed or queued PR checks.
4. Keep `shutdown_policy=always` for normal use.

The orchestrator runs this sequence:

```text
power-ci-runners.sh start
wait-runners-online.sh
gh run rerun/watch for each provided run id
archive-ci-artifacts.sh for each run id
power-ci-runners.sh stop
```

The archive script downloads GitHub Actions artifacts and run metadata, then
uploads them to Linode Object Storage under:

```text
ci-runs/<owner>_<repo>/<run-id>/
```

The script uses `aws s3 sync` when AWS CLI is installed. If AWS CLI is not
available, it falls back to Python `boto3` against the same Linode
S3-compatible endpoint.

### Operator Fallback

If GitHub Actions itself is unavailable, an operator can run the same lifecycle
locally with equivalent environment variables loaded:

```sh
scripts/linode-ci-runners/run-ci-session.sh \
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
scripts/linode-ci-runners/power-ci-runners.sh status
```

Check registered runner status:

```sh
scripts/linode-ci-runners/list-ci-runners.sh
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
