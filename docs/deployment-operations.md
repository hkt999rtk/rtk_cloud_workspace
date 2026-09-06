# RTK Cloud Deployment Operations Guide

Status: active

Owner: `rtk_cloud_workspace`

Last reviewed: 2026-09-07

Audience: internal deployment operators and new maintainers

This document is the deployment entry point for internal operators. It covers
creating a new environment, taking over an existing environment from another
controller, and accepting an existing deployment. Linode staging uses only
LKE/Kubernetes; the legacy VM runtime is not an active deployment path.

## Select the Operation First

| Scenario | Correct entry point | Modifies cloud resources? |
| --- | --- | --- |
| Check tracked configuration | `deployment preflight --operation plan` | No |
| Create a new environment | `deployment plan` -> `deployment provision` | Provision does |
| Upgrade persistent staging | Reviewed plan and CI image provenance -> `deployment upgrade` (or a scoped existing-workload rollout) | Updates selected resources; never implies reset |
| Check Console release features | `deployment console-check --environment NAME --cloud-id UUID --product-id UUID` | Creates private login sessions; otherwise GET/HEAD only |
| Take over an existing environment | Transfer matching non-secret controller state and SecretStore -> `deployment preflight --operation acceptance` | Preflight does not |
| Restore core data after deployment | [Matched backup/restore procedure](backup-restore.md) under a maintenance/write fence | Explicit restore replaces selected datasets after a safety backup |
| Accept an existing environment | `deployment acceptance` | Creates or updates test data; does not rebuild the deployment |
| One-time environment rehearsal | `deployment test` | Creates resources and removes the owned resources at the end |
| Remove an environment | `deployment remove` | Deletes resources owned by that stack |

Non-secret generated controller runtime is located at:

```text
cloud_env/<environment>/runtime
```

Neither `cloud_env/staging/lke` nor `cloud_env/staging/linode` is an active input.
Do not copy, rename, or symlink a legacy path into the runtime. For an existing
environment, transfer matching controller state and separately recover its
SecretStore. Core data restoration uses the explicit recovery procedure, not a
recursive controller-directory copy.

## Information Required Before Deployment

### Accounts and Permissions

- Git read access to the workspace and every private submodule.
- Linode account access and a `LINODE_TOKEN` allowed to create LKE, VMs, firewalls,
  VPCs, volumes, and Object Storage resources.
- The active-service limit confirmed by the Linode account owner. The Linode API
  cannot report this value; do not guess it.
- GHCR `read:packages` permission to pull the image for each pinned service commit.
- Record-mutation permission for the environment's selected DNS provider;
  staging defaults to GoDaddy.
- An SSH key pair for temporary load-generator VMs.

### Controller Tools

A fresh controller requires at least Git, Go, `kubectl`, Helm, `certbot`, `curl`,
`jq`, OpenSSL, and SSH. Load tests additionally require Ansible. Docker is needed
only for local image operations.

Initialize the complete checkout first:

```sh
git submodule update --init --recursive
```

### Environment SecretStore

All sensitive data is centralized under `~/.config/rtk_cloud/<environment>/`.
Each operator key is a separate mode `0600` file under `operator/env/`. Normal
deployment does not read process environment variables, shared profiles,
workspace runtime secrets, or legacy home env files.

The main current items for LKE + GoDaddy staging are:

```env
LINODE_TOKEN=<redacted>
GHCR_PULL_USERNAME=<github-user>
GHCR_PULL_TOKEN=<redacted>
GODADDY_KEY=<redacted>
GODADDY_SECRET=<redacted>
```

Manage them with `rtk-cloud secrets init|plan|migrate|verify|inventory`. Secrets
may exist only in the environment SecretStore, K8s runtime mirror, or GitHub
Actions Secrets. Never write them into tracked environment configuration, PRs,
issues, chat messages, or test reports.

### Tracked Environment and Ignored Runtime

| Location | Content | May be committed? |
| --- | --- | --- |
| `cloud_env/<env>/environment.env` | Stack, DNS root, logical location, public OAuth settings and explicit Test Lab intent | Yes; never client secrets |
| `cloud_env/<env>/deployment.env` | Architecture, deployment adapter, DNS adapter | Yes |
| `cloud_env/<env>/overrides/*.env` | Reviewed environment differences | Yes |
| `cloud_env/<env>/runtime/` | Kubeconfig, provider state, OpenBao, service secrets, test identities, artifacts | No |
| `runtime/adapters/lke/account.env` | Operator-confirmed active-service limit | No |

Create active-service-limit state:

```sh
mkdir -p cloud_env/staging/runtime/adapters/lke
cp cloud_env/staging/runtime/adapters/lke/account.env.example \
  cloud_env/staging/runtime/adapters/lke/account.env
chmod 600 cloud_env/staging/runtime/adapters/lke/account.env
```

Replace the example value with the actual limit confirmed by the Linode account
owner.

## Read-Only Preflight

Preflight does not materialize runtime, modify tracked files, or call cloud
mutation APIs:

```sh
go run ./scripts/go/rtk-cloud -- deployment preflight \
  --environment staging \
  --operation plan

go run ./scripts/go/rtk-cloud -- deployment preflight \
  --environment staging \
  --operation provision
```

`plan` validates the tracked environment schema and basic tools. `provision`
additionally validates provider, DNS, GHCR, SSH key, active-service limit, and
existing-cluster safety state. Output shows only `PASS/WARN/FAIL`, never credential
values. Correct every `FAIL` before proceeding to provisioning.

## Create a New Environment

Use this path only after confirming that the target cluster/storage does not
exist. If a same-name cluster exists, stop and use the takeover path instead.

1. Create or review tracked configuration according to
   [`../cloud_env/README.md`](../cloud_env/README.md).
2. Run `preflight --operation provision`. `deployment provision` repeats this
   preflight automatically immediately before credential validation and mutation.
3. Generate a sanitized plan:

   ```sh
   go run ./scripts/go/rtk-cloud -- deployment plan --environment staging
   ```

4. Review the stack, provider region, resolved Product, node class, replicas,
   storage, DNS, image, and projected active services. Use only the topology
   values in this resolved plan.
5. Mutate only after explicit approval:

   ```sh
   go run ./scripts/go/rtk-cloud -- deployment provision \
     --environment staging \
     --confirm video-cloud-staging
   ```

6. Run acceptance after provisioning completes:

   ```sh
   go run ./scripts/go/rtk-cloud -- deployment preflight \
     --environment staging --operation acceptance
   go run ./scripts/go/rtk-cloud -- deployment acceptance \
     --environment staging --confirm video-cloud-staging
   ```

See [`staging-from-scratch.md`](staging-from-scratch.md) for the complete staging
+ billing + 1K MQTT/Device Shadow sequence.

## Upgrade Persistent Staging: Release Gates

An image rollout, a feature rollout and end-to-end data qualification are three
different results. Do not report a complete staging release from ready Pods,
`/health`, a login redirect, or an empty Cloud alone.

1. **Revision and rollback gate.** Record workspace and recursive service SHAs,
   old/new immutable image references and registry digests, the successful CI
   publication for each selected revision, migration version and rollback image.
   Include workers, ingesters and migration Jobs, not only public API Deployments.
   Main-push publication is supported by the current service workflows; tagging
   every repository is not a prerequisite. Verify each workflow's actual trigger;
   reuse successful publication and do not dispatch unrelated CI. Staging accepts
   CI-published images only; local images are for dev.
2. **Desired/runtime gate.** Reconcile tracked `environment.env`, canonical
   environment-local operator settings, restored runtime and effective workload
   configuration. Restored `stack.env` is old controller state, not the desired
   release. Save existing image overrides before `deployment plan` materializes
   runtime; re-resolve and verify intended images afterward. `provision --deploy`
   rejects declared Console settings that differ from its effective inputs before
   provider operations. It does not silently repair or overwrite them. Direct
   image-only Kubernetes updates do not execute this guard: compare configuration
   and required revision dependencies explicitly before using that narrower path.
3. **Configuration and credentials gate.** For selected AM/Admin consumers, the
   renderer binds the same environment-local `job-authorization-token` to both
   Secrets, using the actual `-account-manager` and `-admin` namespaces. Its
   checksum rolls both consumers when changed. For selected AM, enabled Google/
   GitHub require the matching client ID, client secret, state secret and exact
   selected Admin HTTPS callback before deployment. Do not fix a configuration
   failure by disabling login or borrowing another environment's secret. Test Lab
   is explicitly declared in dev/staging and requires tenant MQTT support; validate
   AM, Admin, VC, WebSocket endpoint and broker listener together when enabling it.
4. **In-place rollout gate.** Use `deployment upgrade` for an authorized platform
   upgrade; use a scoped rollout for a service-only request. Preserve PVCs, issuer
   state and existing data. Run required additive migrations with the intended CI
   image and valid runtime configuration before dependent applications; retain
   failed Job evidence. An image-only update does not run migrations. The current
   targeted dependency helper does not provide a general AM-only migration gate:
   explicitly run/verify the migration or use the reviewed full-upgrade path when
   the selected revision requires one. Do not assume a successful API rollout
   updated every auxiliary workload. This guardrail change does not rewrite that
   broader migration/worker orchestration.
5. **Console gate.** Run the maintained check below and inspect its JSON report.
   Any `FAIL` or `SKIP` produces a nonzero exit. Use an explicitly selected
   qualification Cloud owned by the selected environment's bootstrap admin and
   its Product; the checker creates neither. It reads `runtime/platform-admin`
   and optional `operator/env/ACCOUNT_MANAGER_BOOTSTRAP_PLATFORM_ADMIN_EMAIL`
   from that environment's SecretStore, holds cookies in memory, refuses redirects
   and does not print API bodies or secrets.

   ```sh
   go run ./scripts/go/rtk-cloud -- deployment console-check \
     --environment staging --cloud-id <qualification-cloud-uuid> \
     --product-id <qualification-product-uuid>
   ```

   The checker verifies configured social providers are visible, private admin
   and developer login, nonempty SDK releases, a published first-party AmebaPRO2
   provider snapshot matching the SHA-256 of the current same-origin manifest,
   Board/video metadata and local model availability, Billing read endpoints and
   consistent Cloud ownership, and the enabled Test Lab read route. It does not
   contact arbitrary third-party manifest URLs, refresh providers, start sessions,
   bind devices or make payments. Billing reads passing do **not** mean nonempty
   metering or invoice generation passed. Model availability does **not** prove
   WebGL rendering or YouTube playback; check those in the browser too.

   If the first-party snapshot is stale, review the manifest change and refresh
   the **existing, exact published first-party provider** through the authorized
   platform management flow. Record provider ID, old/new SHA and refresh result,
   then rerun the check. Do not create duplicate providers, rewrite third-party
   snapshots or treat an image rebuild as a catalog refresh.

6. **Data gate.** Separately qualify app and device mTLS, then MQTT -> persisted
   logs -> Billing facts with fresh run-scoped data. Use the existing acceptance
   entry point, which sets both skip-reset and skip-provision internally:

   ```sh
   scripts/run-staging-acceptance.sh --plan
   CLOUD_STAGING_E2E_VIDEO_CLOUD_TOKEN_BASE_URL_OVERRIDE="https://device.video-cloud-staging.realtekconnect.com" \
     scripts/run-staging-acceptance.sh --confirm video-cloud-staging \
       --no-resume --device-prefix <unique-prefix>
   ```

   This step intentionally creates/mutates test data and needs that scope of
   authorization. Do not reuse devices after lifecycle deactivation/unprovision.
   A device token 200 does not clear an app token 401. Record each unverified
   stage, lack of nonempty Billing/invoice fixtures and product-semantic defects
   as release blockers or explicitly accepted exceptions, not as PASS.

The default `run-staging-e2e.sh --confirm ...` includes reset/provision. It is **not**
the default persistent-staging update command. Use it only for an explicitly
authorized destructive rehearsal after reviewing its plan. Neither a skill nor a
test script grants permission to reset an existing environment.

### 2026-09-07 Findings and Remaining Work

- Staging ran the selected CI-built service revisions; missing tags were not the
  cause. Published chipset snapshots remained older than the new Admin assets.
- Google enable/client ID existed in tracked staging configuration; the staging
  SecretStore also contained a nonempty Google client secret. The effective AM
  configuration was incomplete. Earlier "create another Google credential"
  advice was incorrect: reconcile bindings first, validate the existing client
  and its registered callback, and rotate only if independently required.
- Secret rendering omitted job authorization, and its catalog used the wrong
  Admin namespace. Reapplying a generated Secret could remove the manual repair.
  Renderer, namespace, checksum and repeated-render tests now cover this defect.
- Test Lab UI availability did not imply its live backend gate was enabled.
  The explicit environment declaration and Console probe make this visible.
- Fleet's fabricated firmware/signal values, health inconsistency and Analytics
  empty-data semantics are product defects, not release packaging defects. Track
  and fix them in Cloud Admin/contracts tests; rebuilding the same code cannot
  correct them. This change does not claim to repair these product behaviors.
- AM-only targeted migration/auxiliary version orchestration, automated browser
  acceptance, and automatic integration of `console-check` into aggregate E2E
  remain follow-up work. For now Console checking is a required separate operator
  gate. The new checker has no automatic refresh or repair mode.

## Take Over an Existing Environment

A Git clone does not include runtime or secrets. To take over the same cluster,
transfer the allowlisted non-secret controller state from
`cloud_env/<env>/runtime/` and separately recover the matching environment
SecretStore under `~/.config/rtk_cloud/<env>/`. Do not copy only kubeconfig or
reprovision from empty controller state. This handoff does not restore cloud
databases or OpenBao.

```sh
scripts/restore-staging-runtime.sh \
  --source-runtime /secure/path/cloud_env/staging/runtime \
  --target-runtime "$PWD/cloud_env/staging/runtime"

scripts/restore-staging-runtime.sh \
  --check-only \
  --target-runtime "$PWD/cloud_env/staging/runtime"
```

Then run acceptance preflight. It checks runtime identity, provider metadata,
kubeconfig, OpenBao/PostgreSQL state, and Kubernetes API access. Any missing state
means the handoff is incomplete. See
[`staging-runtime-bootstrap.md`](staging-runtime-bootstrap.md) for detailed
transfer and permission requirements.

## Core Data Backup and Restore After Deployment

Use [Core Backup and Restore](backup-restore.md) for the authoritative matched
data procedure and `rtk-cloud backup` / `rtk-cloud restore` commands. V1 uses a
manual maintenance window, not zero downtime, and covers staging, prod and
other explicitly configured environments. Redis cache is excluded, but durable
shadow/index/outbox state is included.

For disaster recovery, deploy matching infrastructure/releases under external
traffic and dispatch isolation, then explicitly apply the core backup. The
restore command always saves a target safety backup before overwriting data,
keeps maintenance active, and requires verification plus reconciliation before
resume. Ordinary provisioning is not a restore-target mode and must not be used
to regenerate lost PKI as a substitute for restoring the original issuer state.
Media/firmware payloads and independent audit/escrow are separate dependencies.

## Rehearsal, Removal, and Evidence

`deployment test` always runs `provision -> acceptance -> cleanup` and may be used
only for an ephemeral stack that did not previously exist:

```sh
go run ./scripts/go/rtk-cloud -- deployment preflight \
  --environment dev --operation ephemeral-test
scripts/deploy-environment.sh test \
  --environment dev --confirm video-cloud-dev
```

Removing an existing environment is destructive:

```sh
go run ./scripts/go/rtk-cloud -- deployment remove \
  --environment dev --confirm video-cloud-dev
```

Before running it, back up runtime/evidence that must be retained and confirm the
stack identity. Never target staging, CI runners, release buckets, or unrelated
resources as rehearsal cleanup.

A successful handoff retains at least the resolved deployment plan, exact
workspace/submodule commits, acceptance report, Kubernetes rollout/health results,
and every skipped/blocked check. Reports may contain only sanitized evidence.

## Failure Triage

- Missing `LINODE_TOKEN`/DNS/GHCR: add the operator-local credential; never write
  it to tracked environment configuration.
- Existing cluster missing OpenBao/PostgreSQL state: stop provisioning and restore
  matching runtime.
- Unusable kubeconfig: verify runtime provenance and permissions; do not create a
  new credential arbitrarily for old storage.
- Image-resolution failure: confirm that the GHCR image for the pinned service
  commit is published and the token has `read:packages`.
- Unknown active-service limit: ask the Linode account owner; never guess a default.
- Deployment succeeds but tests fail: run acceptance first according to
  [`testing-operations.md`](testing-operations.md), then identify data, MQTT, API,
  database, or generator bottlenecks.
