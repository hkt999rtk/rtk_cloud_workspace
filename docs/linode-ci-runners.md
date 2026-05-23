# Linode CI Runner Governance

Status: workspace source document for RTK Cloud self-hosted CI runner VMs.

This document defines the Linode CI runner topology used when GitHub Actions
jobs require repo-specific self-hosted labels. CI runner VMs are not intended to
stay on permanently. The operator flow is:

1. Boot the Linode CI runner VMs before CI.
2. Wait for GitHub runners to become online.
3. Run or rerun the target CI jobs.
4. Archive GitHub Actions artifacts to Linode Object Storage.
5. Shut down the Linode CI runner VMs.

The workflow is intentionally external to GitHub Actions. A self-hosted job
cannot boot its own runner VM because the runner must already be online before
GitHub can start the job.

## Goals

- Unblock repo CI jobs that require self-hosted runners.
- Keep heavy and privileged workloads isolated by repository.
- Avoid storing deployment, production, or customer secrets on CI hosts.
- Store CI outputs durably in Linode Object Storage after runs complete.
- Keep runner VM cost bounded by shutting VMs down when CI is idle.

## Runner Topology

V1 uses exactly three dedicated Linode VMs:

| VM label | GitHub repo | Runner labels | Recommended type | Reason |
| --- | --- | --- | --- | --- |
| `rtk-ci-account-manager` | `hkt999rtk/rtk_account_manager` | `self-hosted`, `Linux`, `X64`, `account-manager-ci` | `g6-standard-2` | Account Manager CI uses Go tests and PostgreSQL service containers. |
| `rtk-ci-cloud-admin` | `hkt999rtk/rtk_cloud_admin` | `self-hosted`, `Linux`, `X64`, `rtk-cloud-admin-ci` | `g6-standard-2` | Admin CI uses Go, Node, Docker build, and container smoke. |
| `rtk-ci-video-cloud` | `hkt999rtk/rtk_video_cloud` | `self-hosted`, `Linux`, `X64`, `video-cloud-ci` | `g6-standard-4` | Video Cloud CI is the heaviest suite and should not block platform repos. |

Do not run these three labels on one shared host. `rtk_video_cloud` CI can
consume enough CPU, disk IO, Docker state, and cache space to delay or pollute
other repositories. Separate VMs also make runner failures easier to diagnose.
Each VM is stopped independently after its repo CI has completed and its
artifacts have been archived.

## Cost And Lifecycle Policy

- Runner VMs may remain provisioned, but they should be powered off when no CI
  is running.
- Boot runners before triggering or rerunning PR CI.
- Shut runners down only after required artifacts have been copied to Linode
  Object Storage.
- Do not destroy runner VMs for normal idle periods; destroying them forces
  runner re-registration and loses host-level package/cache warmup.
- If a runner is suspected compromised, delete and rebuild the VM instead of
  preserving local state.


## Dedicated-Only Policy

Shared runner hosts are not supported for normal RTK Cloud CI. The scripts keep
`CI_RUNNER_PROFILE=dedicated` as a tolerated no-op for backwards-compatible
operator shells, but any non-dedicated profile is rejected.

This is intentionally stricter than a cost-optimized shared-host model:

- Lifecycle is deterministic: one repo CI maps to one VM.
- Shutdown is safe after that repo's CI artifacts are archived.
- Docker state, caches, service containers, and temporary secrets do not cross
  repo boundaries.
- Runner debugging is direct because the VM label, runner name, and repo owner
  are the same unit of operation.

If cost pressure requires fewer VMs later, that must be designed as a new
workspace document and script change with explicit busy-runner checks. Do not
reintroduce shared hosts by only changing local environment variables.

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

1. Creates missing Linode VMs and per-VM firewalls.
2. Waits for SSH readiness.
3. Fetches a fresh GitHub runner registration token per repository.
4. Installs Go, Node.js, Docker, build tools, and the GitHub Actions runner.
5. Registers each VM with the required repo-specific label.
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

Manual orchestrator flow:

1. Open the workspace `Linode CI Orchestrator` workflow.
2. Enter one or more target GitHub Actions run ids:
   - `account_run_id` for `hkt999rtk/rtk_account_manager`.
   - `admin_run_id` for `hkt999rtk/rtk_cloud_admin`.
   - `video_run_id` for `hkt999rtk/rtk_video_cloud`.
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
  --video-run-id <run-id> \
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
| `rtk_video_cloud` | `rtk-ci-video-cloud` with `video-cloud-ci` |

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
