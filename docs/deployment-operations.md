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
| Check Console release features | `deployment console-check --environment NAME --cloud-id UUID --product-id UUID --test-account-id UUID` | Uses an existing Test Lab account; creates private login sessions, otherwise GET/HEAD only |
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
   Before a protected Video Cloud PKI schema migration, run
   `scripts/check-deployment-credentials.sh --environment <environment> --read-only --require-pki-migration`;
   this checks the separate
   migration-owner Secret instead of accepting the controller runtime login.
5. **Console gate.** Run the maintained check below and inspect its JSON report.
   Any `FAIL` or `SKIP` produces a nonzero exit. Use an explicitly selected
   qualification Cloud owned by the selected environment's bootstrap admin,
   its Product, and an existing Test Lab account; the checker creates none of them. It reads `runtime/platform-admin`
   and optional `operator/env/ACCOUNT_MANAGER_BOOTSTRAP_PLATFORM_ADMIN_EMAIL`
   from that environment's SecretStore, holds cookies in memory, refuses redirects
   and does not print API bodies or secrets.

   ```sh
   go run ./scripts/go/rtk-cloud -- deployment console-check \
     --environment staging --cloud-id <qualification-cloud-uuid> \
     --product-id <qualification-product-uuid> \
     --test-account-id <existing-test-lab-account-uuid>
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

### Registered-service listener (opt-in, not deployed)

`LKE_ACCOUNT_MANAGER_SERVICE_REGISTRATION_ENABLED` defaults to `false`. When
reviewed and enabled, the LKE renderer adds a private `account-manager`
Service port `8443`, the matching Account Manager mTLS listener settings, and
an ingress NetworkPolicy limited to the MQTT foundation, Shadow worker,
WebRTC service, and video-storage service Pod identities in the Video Cloud
namespace. It does not add a public ingress route or enable Product writes.

Before enabling the flag, provision the environment-local Kubernetes Secret
`account-manager-service-registration-tls` in the Account Manager namespace
with `tls.crt` (server certificate and chain), `tls.key`, `client-ca.crt`, and
the current issuer-signed `client.crl`. The rendered Pod mounts it read-only
at mode `0440` with `fsGroup: 10001`; the workload deployment step must reject
missing or invalid material before applying selected workloads. For each
MQTT or optional-service identity, qualify its client key/certificate pair, exact
approved service CN, client-auth purpose, validity, trust chain against this
listener's client CA, and issuer-signed current CRL (including revocation).
Also verify that the identity Secret's server CA validates the listener's
server certificate for its private DNS name. This cross-Secret verification
is a local pre-deploy gate, not proof of the Platform issuer's live registry
receipt or workload approval. The renderer does **not** issue or rotate the
certificates. Qualify the server DNS name
`account-manager.<stack>-account-manager.svc.cluster.local`, client-CA chain,
server key match, CRL issuer/currentness, file access, and every affected
rendered workload with the protected-environment credential/TLS preflight.
The registration URL for clients is the private Service origin on port 8443.
Rotate the mounted CRL atomically; the listener rereads it on every request.
Rotating the server certificate/key also requires an Account Manager rollout,
because the listener loads that pair at startup. Do not enable plugin Pods or
the Product-write gate until registration, denial, lease expiry, and CRL
rotation pass with the actual issuer and workloads.

The Video Cloud LKE image build paths include the independent
`mqttfoundation`, `shadowworker`, `webrtcservice`, and `videostorage` binaries.
`LKE_MQTT_FOUNDATION_REGISTRATION_ENABLED` is a separate default-off deploy
flag for one MQTT foundation registrar. It requires the Account Manager listener
flag and an existing `mqtt-foundation-platform-identity` Secret in the Video
Cloud namespace with `client.crt`, `client.key`, and `server-ca.crt`. The
renderer checks the certificate/key pair, trust and revocation against the
Account Manager listener Secret before the workload step mutates Kubernetes. A
targeted Video Cloud rollout also checks that the existing Account Manager
Service exposes its private `8443` port. The registrar runs under a fixed
`mqtt-foundation-0` instance ID with a `Recreate` strategy, so the Platform
workload approval must bind that exact instance to its platform-issued client
certificate subject, issuer fingerprint, service ID `mqtt`, and option `mqtt`.
Do not reuse this identity for another replica. Restart the registrar after
client certificate/key rotation so its mTLS client loads the new identity, and
verify the replacement approval before draining the old one. The flag controls
creation and updates, not retirement of an already-deployed registrar; drain
or suspend it explicitly before removing its workload. No optional plugin Pods
or gateway routes are deployed by this flag. It does not enable Product writes.

`LKE_SHADOW_WORKER_REGISTRATION_ENABLED` is a separate default-off Shadow
worker rollout flag. It requires the MQTT foundation flag and a distinct
`shadow-worker-platform-identity` Secret with the same three client identity
keys. Its one `shadow-worker-0` instance must be approved for service `shadow`
and option `iot_shadow`, with `mqtt` as the declared dependency. The renderer
adds private EMQX ingress for that Pod and uses the existing Video Cloud
namespace access to PostgreSQL and Redis. Enabling the flag also makes the
core API stop consuming Shadow MQTT requests before the worker rollout is
waited on. Plan for a temporary Shadow-request outage during this cutover;
verify the old API replicas have completed rollout, the worker is registered
and ready, and one request produces one response before enabling new Product
grants. The core API still serves HTTP Shadow routes, so this is not the final
route-ownership split. As with MQTT, turning the deploy flag off does not
retire an already-running worker. The workload step rejects a core rollback
while the worker Deployment still exists; drain and remove that Deployment
before returning MQTT Shadow subscriptions to the API.

Shadow HTTP handoff is a second default-off step. The registered worker now
serves the two Shadow route families on its private port `18081`; it uses the
same token/SigV4 secret as core and refuses HTTP requests when its Platform
lease or dependencies are unavailable. After the worker is independently
registered and verified, set `LKE_SHADOW_HTTP_CORE_CUTOVER_ENABLED=true` for a
separate core rollout. The renderer first requires its private Service and a
ready EndpointSlice, opens core-to-worker ingress, and sets core's fixed
private upstream. Existing public and device-mTLS ingress stays on core.
Verify bearer and SigV4 authorization (including the original signed Host),
adjacent non-Shadow routes, option grants, and upstream-outage `503` before
release. Rollback resets the HTTP flag and waits for core handlers to return
before removing the worker. The first Shadow registration leaves `iot_shadow`
visible but suspended. Keep it unselectable during the private worker/cutover
window; after the live HTTP and MQTT routes are both verified, a platform
administrator may activate service `shadow`. Registration and heartbeats do
not activate it. Check any older already-active row before enabling Product
writes; the new default does not rewrite existing catalog state.

`LKE_WEBRTC_SERVICE_REGISTRATION_ENABLED` is a separate default-off WebRTC
process rollout flag. It requires the MQTT foundation flag and its own
`webrtc-service-platform-identity` Secret in the Video Cloud namespace with
`client.crt`, `client.key`, and `server-ca.crt`. Account Manager must approve
the exact service `webrtc`, instance `webrtc-service-0`, certificate identity,
option `video_streaming`, and dependency `mqtt` before startup. The Pod has a
private ClusterIP Service on port `18082`; readiness requires its Platform
lease, PostgreSQL, and Redis signaling store, and its process rejects plugin
requests without the lease. The renderer gives this Pod the same TURN registry
access as the core API. This flag does **not** publish WebRTC ingress paths or
remove the core WebRTC handlers, so it can be verified before route cutover.
Use the existing CI-built Video Cloud image, rotate the Secret only with a
matching approval and Pod restart, and do not reuse the fixed identity for a
second replica. Turning the flag off does not delete an existing Pod. External
route and core-handler cutover remain separate, coordinated release steps.

The WebRTC HTTP handoff uses two additional default-off flags. After the
independent Pod is registered and ready, set
`LKE_WEBRTC_SERVICE_EDGE_ENABLED=true` and apply public HTTPS. The renderer
checks the private `video-cloud-webrtcservice` ClusterIP Service and a ready
port-`18082` EndpointSlice before adding exact WebRTC paths on both the public
Video Cloud host and the device-mTLS host. It applies backend NetworkPolicy
before ingress. Test authenticated APP offer/ICE/answer/close flows, device
answer through the mTLS host, unrelated core routes, and the unready/expired
registration response before proceeding. This flag alone leaves core handlers
available for rollback.

The first WebRTC registration leaves `video_streaming` suspended. Only after
the live public and device-mTLS ingress rules have been observed
pointing all four exact paths at the WebRTC bridge Service should
`LKE_WEBRTC_CORE_CUTOVER_ENABLED=true` be used for a separate Video Cloud
workload rollout. The deployment step rejects core cutover if the edge flag,
registered workload flag, ready EndpointSlice, or observed live routes are
missing. A combined first `--deploy --dns` activation cannot bypass this
ordering because workloads deploy before ingress. Roll back by setting the
core cutover flag to `false` and waiting for its rollout first; then set the
edge flag to `false` and apply public HTTPS. Activate service `webrtc` through
the platform-admin status operation only after both route ownership and
authenticated calls are verified; otherwise keep it suspended. The edge step
refuses to remove WebRTC routes while the live core Deployment still has handlers disabled or
has not completed restoration. These are local safety checks, not a staging
`GO` verdict or an end-to-end proof of authenticated media behavior.

`LKE_VIDEO_STORAGE_SERVICE_REGISTRATION_ENABLED` is a separate default-off,
private video-storage Pod rollout. It requires the MQTT foundation flag,
direct S3 clip upload configuration and credentials, the existing clip crypto
key in `video-cloud-runtime`, and its own
`video-storage-service-platform-identity` Secret (`client.crt`, `client.key`,
`server-ca.crt`). Account Manager must approve service `video-storage`,
instance `video-storage-service-0`, option `video_storage`, dependency `mqtt`,
and the exact certificate identity. The Pod serves a private ClusterIP Service
on port `18083`; `/readyz` depends on its Platform lease, PostgreSQL, and S3.
It does not start MQTT or WebRTC signaling. The renderer checks the identity
Secret before mutating selected workloads, but does not issue/renew that
certificate or publish public storage routes. Core continues to own all clip,
download, upload, and playback paths by default.

Storage HTTP handoff uses the separate default-off
`LKE_VIDEO_STORAGE_CORE_CUTOVER_ENABLED=true` switch. Deploy and observe the
registered storage Pod first, then perform a separate core rollout. The
renderer checks that its port-`18083` private Service has a ready EndpointSlice
before any selected-workload mutation, opens a NetworkPolicy only from core to
that Pod, and sets core's fixed private upstream URL. The existing public and
device-mTLS ingress routes stay on core; its gateway forwards only legacy
storage URLs and the recognized `/v1/devices/{id}/…` media segment shapes.
The first storage registration leaves `video_storage` visible but suspended.
Keep it suspended while this private Pod is registered but core has not cut
over; readiness alone must not expose a new Product choice. A platform
administrator may activate service `video-storage` only after verifying the
cutover and authorization on both hosts. Existing active rows need an explicit
inventory/suspension before Product writes; first-registration defaults do
not alter them.
Never route the broad `/v1/devices/` prefix to the Pod. On storage outage the
gateway returns `503`, with no local fallback. Verify both hosts, Product grant
enforcement, device mTLS, rejection of forged client-certificate headers on
the public host, non-media pass-through, and outage/recovery before
release. Rollback requires restoring core S3/clip-key dependencies and the
cutover flag to `false`, then waiting for the core rollout; do not remove the
private Pod while core still forwards to it. Authenticated media E2E and
approved Platform identity are still required before enabling storage Product
options for a release.

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
- Existing active services above the recorded limit: refresh the account value when
  a current confirmation is available. The live capacity guard may continue only
  when its reconciliation proves `additional_required=0`; any operation that adds
  an active service remains blocked.
- Deployment succeeds but tests fail: run acceptance first according to
  [`testing-operations.md`](testing-operations.md), then identify data, MQTT, API,
  database, or generator bottlenecks.
