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

PayPal hosted top-ups remain disabled until the selected environment has
`operator/env/PAYPAL_ENABLED` set to `true` and the three mode `0600` files
`runtime/paypal-client-id`, `runtime/paypal-client-secret`, and
`runtime/paypal-webhook-id`. Set `operator/env/PAYPAL_ENVIRONMENT` to
`sandbox` for development; the deployment rejects production PayPal for a
`-dev` stack. The Billing runtime manifest reads these files from that
environment's SecretStore and derives fixed HTTPS return and cancel URLs from
the Billing domain, plus the Cloud Admin origin for the after-return URL.
Billing appends the owner-scoped Billing activity path after PayPal redirects back.
Before enabling checkout, verify that the PayPal webhook ID belongs to the
same environment, points to
`https://<billing-domain>/v1/payment-webhooks/paypal`, and subscribes to
`PAYMENT.CAPTURE.COMPLETED`. A targeted Billing rollout must update the
Billing server before its payment worker so the new migration is applied first.

### Deployment identity persistence

The [deployment service identity design](design/deployment-service-identities.md)
defines the environment-owned initial credentials, signer/bootstrap sequence and
update rules. Plaintext local SecretStore custody is an accepted operator policy.
A normal configuration or image update reuses the recorded identity. Only a truly
missing initial credential is eligible for enrollment; partial, expired or
mismatched material requires reconciliation/explicit renewal. Never replace a
Root or seed an already-managed runtime identity as a side effect of deployment.

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
existing-cluster safety state. It checks official operator `sha-` image pins
against the selected service commits before materializing runtime. When the
selected environment has a kubeconfig, `provision` also verifies Kubernetes
API readiness and rejects a `kubectl`
client more than one minor version from the API server. Select a supported
binary with `RTK_CLOUD_KUBECTL` if the shell's first `kubectl` is too old.
Output shows only `PASS/WARN/FAIL`, never credential values. Correct every
`FAIL` before proceeding to provisioning.

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
   The full LKE image resolver includes Billing in its required image set. It
   reads image pins from the selected environment's operator SecretStore before
   considering restored `stack.env` or an older image artifact. A process image
   override that conflicts with the operator pin, or an official `sha-` tag that
   does not match the selected service submodule commit, stops the upgrade.
   Digest-pinned images still require the separate canonical CI provenance and
   registry-pull checks; a digest alone does not identify its source revision.
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
   For Billing deployments, select an image containing `/rtk-billing-migrate` and set
   `LKE_BILLING_MIGRATION_JOB_ENABLED=true` in the selected environment's
   operator settings. The canonical SecretStore must contain the distinct
   mode-0600 `runtime/billing-db-runtime-password`. The dependency flow first
   creates `billing-migration-database` with the environment's PostgreSQL owner
   URL and password, and `billing-runtime` with a distinct
   `rtk_billing_runtime_<environment>` URL. The deployer explicitly removes
   any legacy `POSTGRES_PASSWORD` data key from `billing-runtime` and verifies
   its absence before starting the Jobs; omitting the key from a `stringData`
   manifest alone does not prove that an older Secret lost it.
   `billing-database-ensure` creates or updates the
   runtime role and grants DML on existing tables and sequences plus default
   privileges for future migration-owned objects; it does not give the runtime
   role schema ownership or CREATE. The one-shot Billing migration Job reads
   only `billing-migration-database`. Both Jobs must complete before updating
   Billing workloads. `BILLING_DB_MIGRATE_ON_STARTUP=false` prevents the API
   and payment simulator from attempting DDL. This separation is required for
   Billing deployment;
   an older image without the migration command must be updated before the
   isolated runtime role is used. Save the prior image/schema version and
   retain failed Job logs for review. After migration, verify the runtime role
   can perform required DML and cannot perform DDL, and that no API or worker
   Pod mounts the migration Secret. A targeted `--workloads billing` deploy
   reconciles only its selected placement and does not prune unrelated LKE
   node pools; node-pool retirement remains part of a reviewed full deploy.
   The OTA Platform period-seal schedule is separately gated by
   `LKE_OTA_PLATFORM_SEAL_SCHEDULE_ENABLED`, explicitly `false` in dev,
   staging and production. Before enabling it, provision a dedicated
   mode-0600 `runtime/ota-platform-seal-token` file in the selected
   environment's canonical SecretStore and verify that the token is distinct
   from Billing service/internal tokens. The opt-in gate fails if it is absent.
   Select a CI-published Account Manager image containing
   `/app/rtk-account-manager-ota-period-seal`, apply Billing migration 069,
   and update Billing and Account Manager runtime Secrets together. Verify the
   Account Manager Pod can reach the exact Billing period-seal endpoint over
   validated HTTPS. The opt-in CronJob submits the previous full UTC month on
   day 3 at 03:00 UTC; failed Jobs remain visible and whole-batch retries are
   idempotent. The Job has a 24-hour deadline so a larger Cloud inventory can
   finish; a missed schedule or failed Job requires an explicit month-specific
   retry and investigation. Check the first Job's exit, hashed Cloud references, Billing
   acknowledgment and source seal rows. The producer seal and alerting remain
   separate required gates before OTA pricing activation; a successful
   Platform Job alone does not qualify a billable month. Disabling the flag
   removes the CronJob but preserves its historical Jobs. The Billing endpoint
   token remains while those Jobs may run; revoke it only after the month is
   closed and no historical Job needs to submit.
   The independent OTA runtime has three separate LKE adapter flags:
   `LKE_OTA_SERVICE_REGISTRATION_ENABLED`, `LKE_OTA_SERVICE_EDGE_ENABLED`, and
   `LKE_OTA_CORE_CUTOVER_ENABLED`. The adapter defaults and dev, staging, and
   production overrides all set them to `false`. For an authorized rollout,
   first verify strict Product OTA entitlements, MQTT foundation registration,
   Account Manager service registration, private object storage, an HTTPS
   object endpoint, `VIDEO_CLOUD_OTA_DELIVERY_MODE=object_url`, required runtime
   Billing/Account Manager tokens, and the dedicated `service:ota` Platform
   identity. Keep the old `otaregistrar` disabled to avoid duplicate ownership.
   For the existing dev stack, use `deployment ota-service-rollout
   --environment dev` to review the narrow registration step. After setting
   `LKE_OTA_REGISTRAR_REGISTRATION_ENABLED=false` and
   `LKE_OTA_SERVICE_REGISTRATION_ENABLED=true` in the dev operator SecretStore,
   scale `video-cloud-otaregistrar` to zero and wait until it has no Pods.
   Pin `LKE_VIDEO_CLOUD_IMAGE` to the reviewed immutable digest, then run
   `deployment ota-service-rollout --environment dev --confirm video-cloud-dev`.
   The command combines the current reviewed dev deployment overrides with the
   environment-local SecretStore runtime; a managed worktree need not contain
   generated runtime files. In particular, the firmware manifest trust keys
   come from the reviewed environment configuration rather than an older
   SecretStore snapshot.
   Before a signed release, follow the Video Cloud
   [operator Manifest V1 guide](../repos/rtk_video_cloud/docs/ota-manifest-operator.md):
   generate the environment-specific private key in the local operator
   SecretStore, add only its public entry to the reviewed environment override,
   retain existing trusted keys, and verify the effective key ID on the OTA
   Service after rollout. The private key must never enter the deployment
   manifest, Kubernetes Secret, or Git. Until core cutover, a controlled
   acceptance run may send operator requests to the independent OTA Service
   through a temporary private port-forward with the existing OTA BFF token;
   that route does not replace the device mTLS edge.
   This initial registration applies only OTA registration policies, its
   private Service and its Deployment. It checks the old registrar is stopped
   and leaves the PKI-managed core API and log ingester untouched. Publish OTA
   manifest v2 with an authenticated service-mTLS request after the independent
   lease is ready; the rollout command does not change catalogue publication
   or Product grants.
   `ota-service-rollout` is the initial registration step and deliberately
   refuses to run after the device edge is enabled. For an additive public-key
   update on the already deployed dev OTA Service, first review
   `deployment ota-manifest-trust --environment dev`, then run it with
   `--confirm video-cloud-dev`. It requires the existing mTLS edge and ready
   service, rejects any removed or changed trusted key, verifies the pinned
   image, and patches only the OTA Service Deployment's manifest-trust value
   with a resource-version check. It waits for rollout and verifies the key
   value, endpoint and edge route afterward. It does not modify the core API;
   after core cutover, key rotation needs a coordinated procedure for both
   operator paths.
   Publication of a ready revision is allowed while OTA is `suspended`; it
   changes only the selected manifest version, not service activation or Product
   eligibility. If publication returns 409 despite a ready v2 lease and the
   expected v1 revision, update Account Manager to the suspended-publication
   fix. Do not temporarily activate OTA to bypass that check. For the
   publication, use only `ota-service-platform-identity` and verify its leaf
   subject is `service:ota`. Stage its client certificate, key and server CA
   in a temporary mode-0700 directory with mode-0600 files; forward the private
   Account Manager service-registration port 8443 to localhost and retain the
   exact `account-manager.<account-manager-namespace>.svc.cluster.local` TLS
   server name. Send `PATCH /v1/platform/services/ota/publication` with the
   observed `expected_version` and ready `manifest_version`. Read back the
   selected revision, `suspended` status and ready lease, then stop the
   forward and remove the temporary certificate files.
   For the existing dev stack, review `deployment ota-device-edge --environment
   dev` before changing the device route. After confirming that the independent
   OTA endpoint and v2 lease remain Ready, set
   `LKE_OTA_SERVICE_EDGE_ENABLED=true` in the dev operator SecretStore and run
   `deployment ota-device-edge --environment dev --confirm video-cloud-dev`.
   This narrow command requires strict Product entitlement checks and a stopped
   legacy registrar. It verifies that the existing device-host ingress still
   pins the expected client CA bundle, requires client mTLS, forwards the verified client
   certificate and retains its core API route. It adds only the OTA port 18084
   ingress policy, a bridge Service and the `/v1/device/ota/` Prefix route to
   that same ingress. Its JSON patch checks the observed ingress version and
   host, so a concurrent edit fails instead of replacing routes. Re-running it
   after success is safe. Verify an unauthenticated request is rejected at the
   edge, and a valid device certificate reaches OTA with the expected Product
   authorization decision before enabling core cutover. Keep the core cutover
   flag off until that check succeeds; then qualify task, download, outbox and
   Billing receipts. For rollback, restore core OTA handlers and verify their
   rollout before removing the OTA ingress path. Retain receipts and outbox
   evidence during either direction.

   After the controlled Product download and the three immediate source-to-Billing
   receipts pass in dev, use `deployment ota-core-cutover --environment dev`
   to review the exact core image and scope. Keep the existing
   `LKE_OTA_CORE_CUTOVER_ENABLED=false` until running
   `deployment ota-core-cutover --environment dev --confirm
   video-cloud-dev`. This narrow command requires the stopped old registrar,
   ready private OTA Service, active device mTLS route, strict Product gate and
   immutable operator-pinned Video Cloud image. It patches only the core API
   app image and the two OTA upstream settings with a resource-version guard,
   waits for rollout, checks live read-back, then persists the `true` flag in
   the selected dev operator SecretStore. If persistence fails after live
   cutover, retry the same guarded command; do not run a full deployment until
   the live and operator states agree. Verify public core health and
   an authorized Product operation through Cloud Admin afterward. Its
   `--read-only` mode checks the persisted live cutover without changes. This
   cutover does not publish OTA pricing or activate the suspended catalog.

   Product-issued device certificates chain through their Product issuer and
   Device intermediate to the environment's Device Root. The legacy device
   ingress bundle does not contain that Root and depth 2 rejects this three-hop
   chain even when the leaf is valid. For each environment with a pinned
   `PKI_DEVICE_ROOT_ID` and `PKI_DEVICE_ROOT_SHA256`, copy **only the public**
   active Root certificate from the PKI-published bundle to that environment's
   SecretStore `pki/devices/device-root.crt` after matching its subject and
   SHA-256 to the operator pin. The LKE deployment renderer then appends this
   Root to the existing client CA bundle and sets device ingress verification
   depth to 3; a missing or mismatched Root fails the deployment. It preserves
   legacy trust during transition. This file and the two pin values must be
   prepared in dev, staging and production when each environment activates
   Product Device PKI. Never copy a Root from a device-supplied chain as the
   authority for ingress trust.

   The already-running dev ingress can be updated without redeploying the
   stack. First run `deployment device-root-ingress-trust --environment dev
   --read-only` and record its current result. Review the plan without
   `--confirm`, then run with `--confirm video-cloud-dev`. The command accepts
   only the pinned Root, validates the existing CA bundle and mTLS ingress,
   applies resource-version guarded additive CA and depth patches, and verifies
   live read-back. Repeat `--read-only` and send a Product-issued device's
   complete certificate chain through the public mTLS host. A missing client
   certificate must still be denied. Preserve the pre-change CA bundle and
   ingress depth for rollback; remove the added trust only after new Product
   devices are no longer using that Root.

   OTA devices receive short-lived signed object GET URLs and download
   directly with Range support. The API never proxies firmware bytes. The
   producer-seal CronJob consumes the same delivery mode; a direct-object
   month requires no CDN review and rejects mixed CDN evidence.

   The future OTA CDN log collector is separately controlled by
   `LKE_OTA_CDN_COLLECTOR_ENABLED=false` in adapter defaults and every dev,
   staging, and production override. Before enabling it, configure the exact
   DataStream ID, OTA CDN host and URL path root, dedicated log bucket and log
   filename prefix, S3 region and HTTPS endpoint through the
   `VIDEO_CLOUD_OTA_CDN_*` adapter values. The prefix must exclude Akamai's
   connection-verification file. Provision a separate, read/list-only
   `ota-cdn-datastream-reader` Secret in the Video Cloud namespace with
   `AWS_ACCESS_KEY_ID` and `AWS_SECRET_ACCESS_KEY`; do not reuse firmware-origin
   write credentials. Apply the OTA billing schema including
   `ota_cdn_stream_objects` and `ota_cdn_edge_requests` before activation.
   The preflight verifies the configuration, DataStream reader credentials,
   and selected Video Cloud image. After the deployment-managed
   `video-cloud-runtime` Secret is applied, it verifies the PostgreSQL password
   before deploying the five-minute UTC CronJob. The job
   writes immutable source evidence into Video Cloud PostgreSQL; it creates
   no PVC and cannot approve a month. Verify one gzip delivery, parsed rows,
   an exact rerun, provider completeness proof and alerting before enabling
   producer sealing. Disabling the flag removes only future scheduling and
   preserves completed Jobs and source rows.

   The independent producer close schedule uses
   `LKE_OTA_PRODUCER_SEAL_SCHEDULE_ENABLED=false` in every environment until
   the selected delivery mode, source receipts, object inventory and Billing
   acknowledgement are qualified. Direct-object months do not need the CDN
   collector or edge review; future CDN months require both.
   To enable it, select Video Cloud, Account Manager and Billing in one reviewed
   rollout; confirm the Account Manager completed-month inventory API is deployed,
   the Video Cloud image contains `/app/otaseal`, and provision a separate
   environment-local `ota-producer-seal-token` of at least 32 characters. The
   deployer installs that token in a dedicated Video Cloud Secret and the Billing
   runtime. Full and targeted deployments validate the token before applying
   runtime dependencies; targeted activation refuses a partial selection.
   Billing's Pod template checksum includes the token so first activation and
   rotation restart its consumer. The deployer permits only `video-cloud-otaservice` and
   `ota-producer-period-seal` to reach Billing on port 8080. The CronJob starts
   at 04:00 UTC on days 3-7, each time replaying the previous UTC month. It
   enumerates every historical Brand Cloud through Account Manager, including
   disabled and zero-use Clouds; any missing delivery-mode evidence, source fact, object,
   inventory or Billing acknowledgement makes the batch fail. Inspect the Job,
   hashed Cloud failures, Billing's two seals and the oldest unclosed month;
   alert on failed or missing Jobs. For an older month, launch the packaged
   `/app/otaseal --all-brand-clouds --month YYYY-MM` with the same protected
   runtime inputs after the selected mode's review. Disabling the flag or rolling back the
   CronJob does not delete historical Jobs, source receipts or Billing seals.
   In staging, Product PKI Go/No-Go and the selected CI image provenance remain
   mandatory before any mutation.
   Before a protected Video Cloud PKI schema migration, run
   `scripts/check-deployment-credentials.sh --environment <environment> --read-only --require-pki-migration`;
   this checks the separate
   migration-owner Secret instead of accepting the controller runtime login.
   Before Product PKI lifecycle acceptance, add --require-product-pki to
   verify that the controller pins an active Device Root in its registry.
   Before the write fence, run the full credential check without --read-only
   and then deployment plan so the validated runtime-media storage receipt
   populates the blob endpoint used by the deployer.
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

   The wrapper uses the protected staging kubeconfig when no explicit
   kubeconfig is set. A missing kubeconfig still fails before creating test
   data; supply a valid selected-environment kubeconfig before retrying.

   This step intentionally creates/mutates test data and needs that scope of
   authorization. Do not reuse devices after lifecycle deactivation/unprovision.
   A device token 200 does not clear an app token 401. Record each unverified
   stage, lack of nonempty Billing/invoice fixtures and product-semantic defects
   as release blockers or explicitly accepted exceptions, not as PASS.

The default `run-staging-e2e.sh --confirm ...` includes reset/provision. It is **not**
the default persistent-staging update command. Use it only for an explicitly
authorized destructive rehearsal after reviewing its plan. Neither a skill nor a
test script grants permission to reset an existing environment.

### Staging deployment check lessons (2026-10-01)

The selected frozen release's full `deployment upgrade` stopped at the
`certissuer-runtime/client-ca.crt` identity guard. The live Secret contained
the original transport CA plus the active Service Root, while the local
`pki/certissuer/service-ca.crt` and baseline renderer held only the original
CA. The guard correctly refused to remove the active trust extension, but it
ran after base resources had been applied. The release was completed with
scoped workloads and resource-version-guarded, image-only updates to the
remaining Video Cloud Deployments; no PKI Secret or trust bundle was replaced.
For a full upgrade, run the read-only continuity check **before mutation**:

```sh
scripts/check-deployment-credentials.sh --environment staging --read-only \
  --require-deployment-identity
```

The deployment engine also runs that check before node-pool and base-resource
steps whenever an existing environment kubeconfig is present. A legitimate
live CA-bundle addition requires a reviewed managed renderer/patch path; copying
an older local CA over the live bundle is not reconciliation.

The first staging acceptance attempt reached test-data creation but failed with
`pki_not_ready`. Root pin, registry, workload readiness and service mTLS were
healthy; the retained RTK test Cloud's automatic CA job had failed before the
new Device Root existed. Its Product jobs remained pending because no active
Cloud issuer could parent them. This failure was invisible to the former Root
only check. For acceptance against a retained Cloud, include its exact UUID:

```sh
scripts/check-deployment-credentials.sh --environment staging --read-only \
  --require-product-pki --product-pki-cloud-id "$ACCEPTANCE_CLOUD_ID"
```

The extra read-only query requires that Cloud and all its active Products to
have ready CA issuers. The documented [Device PKI requeue procedure](automatic-device-pki-rollout.md)
was applied only after reviewing one failed Cloud and eleven failed active
Product jobs, all without existing issuers; it preserved business IDs and
recorded an audit event. The requeued twelve jobs and the newly created test
Product subsequently reached `ready`. A future acceptance run must use a new
device prefix. Intermittent LKE `exec` proxy timeouts were retried as transport
errors and were not treated as PKI or database evidence.

The next acceptance run used the former default of 64 concurrent factory
enrollments. All 100 attempts hit the client's 30-second timeout, while the
issuer journal showed only partial progress. A ten-device run at concurrency
two completed setup in 24 seconds. The `environment-acceptance` defaults are
now four users, two device enrollments and two binds at a time; explicit flags
or environment settings still permit a separate load exercise. The small run
then reached MQTT Shadow: `accepted` arrived, but the device did not receive
`delta`. The Redis document had a nonempty delta and the correct Cloud ID;
the outbox and dead-letter queues were empty. The cause of that delivery gap
is still under investigation. Do not record the full acceptance as passed on
the strength of readiness or an empty outbox alone.
An MQTT-only retry received `delta` but timed out awaiting the reported-state
`documents` event. The broker showed the test clients subscribed to the
expected physical `_bc/<cloud-id>/...` topics, and the final Redis document
contained the reported state with delta cleared. Thus the outstanding gate is
intermittent broker delivery or probe observation, not CA issuance or missing
Shadow state. Video Cloud [PR #739](https://github.com/hkt999rtk/rtk_video_cloud/pull/739)
corrected a definite routing flaw: tenant-scoped responses for one Device had
been assigned to different publisher shards by their full topic names. The
merged fix keeps them on one shard, but staging MQTT acceptance must verify
delivery after its image is deployed. Billing log and database checks passed
independently; their step-only report does not supersede the failed full
acceptance.

### Registered-service listener (opt-in)

Staging checkpoint (2026-10-01): the environment-local Product Service
bootstrap session was sealed after six successful, unrevoked issuance receipts
were matched to the installed Secrets, Ready workloads, and active Platform
leases. Its expired signing window was not extended. The temporary certissuer
bootstrap settings, local bootstrap key and certificate, one-shot Jobs, and
session PVC were removed; the PV disappeared and Linode returned 404 for its
volume. The three retained PKI consumer PVCs remain Bound. The canonical
read-only credential check with Product PKI and migration requirements and the
Account Manager-to-CertIssuer mTLS probe passed. Staging enables strict OTA
Product entitlement checks and private OTA service registration in `object_url`
mode. Device edge routing, core OTA cutover, Logger Billing facts, Product
writes, and customer pricing remain separate acceptance gates.

A targeted Video Cloud rollout must apply the Logger registration settings and
its dedicated identity mount, not merely restart the previous Pod. Registered
Logger keeps its existing MQTT subscription and needs group access to the
mode-`0440` certificate mount (`fsGroup: 10001`). Before replacing the
baseline Deployment, the renderer refuses any existing managed PKI identity
owner so that another controller's state is preserved.

The 2026-10-01 staging rollout of the frozen Video Cloud image exposed two
Logger startup defects and a plan visibility gap. The scoped `video-cloud`
selector also applies Fleet
Valkey, Prometheus, MQTT, and every auxiliary worker; the old plan showed only
the image group. The plan now lists those rollout targets before mutation.
Log Ingester has one fixed Platform instance and one persistent MQTT client ID,
so its Deployment must use `Recreate`. A rolling update starts two Pods with
the same identities and can leave both unready. The deployment preflight
checks the generated Logger and MQTT usage singleton strategies before
updating cluster resources. Run the read-only
`scripts/check-deployment-credentials.sh --environment staging --read-only --require-video-cloud-ready`
check against the existing controllers before a routine scoped update; it
fails on a missing or unready Fleet Valkey, Prometheus, API, MQTT, Logger, or
MQTT usage controller. For a recovery rollout, record the failed check and
repair the unhealthy workload before declaring success. The lease readiness
probe must depend on the
database and Logger backend, then the HTTP readiness probe also waits for the
MQTT subscription and active lease. Requiring MQTT connectivity to acquire
the initial lease deadlocks when a persistent session replays logs: an
unregistered handler closes the connection to avoid acknowledging QoS 1 data.
Keep the unacknowledged messages in the broker until registration succeeds.
Video Cloud [PR #740](https://github.com/hkt999rtk/rtk_video_cloud/pull/740)
fixes that lease startup order on the frozen staging branch.
The next staged image acquired its lease, then repeatedly replayed a retained
test-device log with a permanent Product entitlement denial. The old handler
closed the MQTT connection for every error, so one denied QoS 1 event kept the
whole subscriber unready. [PR #741](https://github.com/hkt999rtk/rtk_video_cloud/pull/741)
records a redacted denial and acknowledges that permanently unauthorized
event without accepting or billing it; database and backend failures still
trigger redelivery. A successful image rollout and full acceptance are still
required to confirm this recovery in staging.
The first scoped rollout returned success while several auxiliary workers
still ran the previous image: the plan listed them, but the scoped deploy
path only reapplied the Logger. A scoped Video Cloud deploy now reapplies and
waits for every auxiliary worker, including the MQTT usage checkpoint owner.
After rollout, use `--require-video-cloud-image` on the credential checker.
It reads the selected environment's protected `LKE_VIDEO_CLOUD_IMAGE` pin,
requires the API and seven auxiliary deployments, and checks all present
Video Cloud service deployments against that pin. The separately managed
`video-cloud-api-pki` deployment is outside this image group. Image drift or
a missing required worker is a failed rollout even when the API is Ready.

The first fresh acceptance then stopped at Shadow `documents` although the
reported state reached version 2 with an empty delta and the Redis outbox
drained. The MQTT test client had carried its handshake deadline across the
whole multi-step probe. It now clears that deadline after connection and
starts a new read deadline for each expected publish. The staging acceptance
wrapper also selects the protected staging kubeconfig when none is explicitly
set, so an operator need not rediscover that prerequisite during the run.
The next acceptance reached MQTT and Shadow but found no persisted runtime
logs. Logger was Ready and had consumed all six messages; its audit warnings
identified `factory entitlement service denied`. The newly created camera
Product allowed MQTT and video but omitted the billable `device_logging`
service. Staging camera fixtures now explicitly select `device_logging` with
seven-day retention. Do not weaken Logger's Product entitlement check or
count denied messages as usage; verify the fixture's service grant before
interpreting missing log evidence as a transport failure.
This fixture cannot yet be created in staging: on 2026-10-01 its Account
Manager Product write gate was off, Logger catalog entry was suspended, and
the read-only service-grant report found 32 Products awaiting backfill with
zero existing grant revisions. Creating the logging Product returned HTTP 400
with the legacy three-option limit. Runtime-log and Billing acceptance remain
NO-GO until the documented Product service grant backfill, active Logger
publication, and Product write cutover complete. Rejected MQTT logs leave no
accepted Logger receipt or billable usage; they generate only a redacted
operational warning and broker acknowledgment.
Run `scripts/check-deployment-credentials.sh --environment staging --read-only
--require-billable-logging-ready` before creating billable-log acceptance
fixtures. On this staging snapshot it fails immediately on the disabled
Product write gate. Once that gate is enabled, the same check also rejects
missing immutable grants or a non-active Logger catalog entry. It does not
change any Product option or grant.

If a rollout is stuck, stop further updates, inspect both old and new Pod
readiness plus broker client state, repair the dependency, and rerun the scoped
deployment and full acceptance. A partially completed `provision --deploy`
is not a successful release.

For a routine scoped update, use this read-only sequence before the mutating
command:

```sh
go run ./scripts/go/rtk-cloud -- provision --env-root cloud_env/staging/runtime \
  --preflight --plan --workloads video-cloud
scripts/check-deployment-credentials.sh --environment staging --read-only \
  --require-video-cloud-ready
```

After the rollout, verify readiness and exact pinned images:

```sh
scripts/check-deployment-credentials.sh --environment staging --read-only \
  --require-video-cloud-image
```

If the readiness check fails, treat the existing deployment as a recovery
case. Capture the failed controller and broker state before changing it; the
normal pre-update PASS criterion cannot be claimed retroactively.

For the existing dev managed-PKI stack, complete the separate
[Product service PKI prerequisite](product-services-dev-pki.md) before enabling
this listener or any of the six Product registrars. It preserves the live CRL
consumers and uses a new bootstrap session rather than the retired state PVC.

`LKE_ACCOUNT_MANAGER_SERVICE_REGISTRATION_ENABLED` defaults to `false`. When
reviewed and enabled, the LKE renderer adds a private `account-manager`
Service port `8443` mapped to the Account Manager app's Pod port `9444`,
the matching mTLS listener settings, and an ingress NetworkPolicy on Pod port
`9444` limited to the six registered MQTT, Shadow, WebRTC, video-storage,
Logger, and OTA Pod identities in the Video Cloud namespace. The existing
`pkimanagement` sidecar already binds Pod port `8443`; do not assign that port
to the app listener. This does not add a public ingress route or enable Product
writes.

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
For initial issuance, set
`CERT_ISSUER_SERVICE_CLIENT_BOOTSTRAP_SERVER_DNS_NAME` to that exact name only
during the operator's active deployment session. Certissuer requires the
bootstrap caller's direct mTLS identity and leaf fingerprint, `purpose=server`,
one matching DNS SAN and the durable session record. Remove the setting after
sealing; keep
the ordinary gateway caller regex unchanged.
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

### Inspect deployment and current certificates

Use the Go `deployment certificate-check --environment <name>` operation and
[certificate operations runbook](certificate-operations.md). It inspects expected
sources without changing credentials. Shared inventory applies to dev, staging
and prod; persist managed-topology overrides in the selected environment's
`certificate-check.json`. Scheduling is a separate operator decision.
