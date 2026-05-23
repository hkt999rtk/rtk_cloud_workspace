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

V1 defaults to three dedicated Linode VMs:

| VM label | GitHub repo | Runner labels | Recommended type | Reason |
| --- | --- | --- | --- | --- |
| `rtk-ci-account-manager` | `hkt999rtk/rtk_account_manager` | `self-hosted`, `Linux`, `X64`, `account-manager-ci` | `g6-standard-2` | Account Manager CI uses Go tests and PostgreSQL service containers. |
| `rtk-ci-cloud-admin` | `hkt999rtk/rtk_cloud_admin` | `self-hosted`, `Linux`, `X64`, `rtk-cloud-admin-ci` | `g6-standard-2` | Admin CI uses Go, Node, Docker build, and container smoke. |
| `rtk-ci-video-cloud` | `hkt999rtk/rtk_video_cloud` | `self-hosted`, `Linux`, `X64`, `video-cloud-ci` | `g6-standard-4` | Video Cloud CI is the heaviest suite and should not block platform repos. |

Do not run these three labels on one shared host in v1. `rtk_video_cloud` CI can
consume enough CPU, disk IO, Docker state, and cache space to delay or pollute
other repositories. Separate VMs also make runner failures easier to diagnose.

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


## Shared Host Profiles

The default profile is `dedicated`, which creates one VM per repo runner. Shared
hosts are supported for cost control, but each repository still needs its own
GitHub runner registration. A single repo-scoped runner cannot serve multiple
repositories. When profiles share a VM, the bootstrap installs multiple runner
instances under separate directories such as `/opt/actions-runner/<runner-name>`
and registers each instance to its owning repo.

| Profile | Hosts | Runner instances | Recommended use |
| --- | --- | --- | --- |
| `dedicated` | 3 | 3 | Default. Best isolation and easiest debugging. |
| `shared-platform` | 2 | 3 | Account Manager and Admin share `rtk-ci-platform`; Video Cloud remains dedicated. |
| `shared-all` | 1 | 3 | Lowest VM count, highest contention; use only for low-frequency CI or emergency cost saving. |

Set the profile before provisioning, powering, or waiting for runners:

```sh
export CI_RUNNER_PROFILE=dedicated          # default
# export CI_RUNNER_PROFILE=shared-platform  # two VMs
# export CI_RUNNER_PROFILE=shared-all       # one VM, three runner services
```

If a shared profile is used, do not allow concurrent heavy jobs unless the VM
type is sized for it. `shared-all` should use at least `g6-standard-8` because
Video Cloud, Docker builds, and service-container tests may overlap.

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

Boot runners:

```sh
scripts/linode-ci-runners/power-ci-runners.sh start
scripts/linode-ci-runners/wait-runners-online.sh
```

Trigger or rerun the target PR CI. For already queued PR jobs, runners should
pick them up once online. Otherwise rerun failed/queued checks from GitHub or
with `gh run rerun <run-id> --repo <owner/repo>`.

Archive artifacts after each run completes:

```sh
scripts/linode-ci-runners/archive-ci-artifacts.sh \
  --repo hkt999rtk/rtk_account_manager \
  --run-id <run-id>

scripts/linode-ci-runners/archive-ci-artifacts.sh \
  --repo hkt999rtk/rtk_cloud_admin \
  --run-id <run-id>

scripts/linode-ci-runners/archive-ci-artifacts.sh \
  --repo hkt999rtk/rtk_video_cloud \
  --run-id <run-id>
```

The archive script downloads GitHub Actions artifacts and run metadata, then
uploads them to Linode Object Storage under:

```text
ci-runs/<owner>_<repo>/<run-id>/
```

The script uses `aws s3 sync` when AWS CLI is installed. If AWS CLI is not
available, it falls back to Python `boto3` against the same Linode
S3-compatible endpoint.

Only after artifact upload succeeds, shut down runners:

```sh
scripts/linode-ci-runners/power-ci-runners.sh stop
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
