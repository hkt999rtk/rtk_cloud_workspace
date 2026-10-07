# RTK Cloud Deployment Operations Guide

Status: active

Owner: `rtk_cloud_workspace`

Last reviewed: 2026-10-07

Audience: internal deployment operators and new maintainers

Current implementation delivery is the two-script existing-environment update
workflow in [the fixed scope](deployment-readiness-standard.md#fixed-script-delivery-scope--2026-10-07):
preflight, deployment of that same checked target, and required rollout health
verification. Actual environment checks and deployment are run by the operator.
Fresh bootstrap and Production custody/backup/restore activation work are separate
from this delivery. The incomplete prototype entries described below are not yet
a completed two-script handoff.

The deployment wrapper builds and runs the native CLI so exit codes 0/1/2 are
preserved, including when called outside the workspace. Its temporary directory
contains compiler output only and is removed on exit; environment credentials,
reports and operational data use the canonical environment store. After exact
target-image/owner rollout verification, deployment reuses the maintained
read-only health checker for runtime Secrets, PKI, workloads, configured provider
reads and required public CertIssuer mTLS. A failed or incomplete required check
leaves deployment incomplete; Ready Pods alone do not establish completion.

This document is the deployment entry point for internal operators. It covers
creating a new environment, taking over an existing environment from another
controller, and accepting an existing deployment. Linode staging uses only
LKE/Kubernetes; the legacy VM runtime is not an active deployment path.

Complete-environment GO/NO-GO is defined by the accepted
[Unified Deployment Readiness Standard](deployment-readiness-standard.md).
The public plan/preflight/upgrade entries select the shared v2 engine and report
`scope=existing-environment-update` for routine updates. All six required update
gates must pass; Production activation and restore qualification are separate
operations. Fresh bootstrap remains outside the current delivery. Legacy schema-1 preparation
and complete provision entries are retired. The narrower image-only and current
health checks do not replace complete qualification. No production activation
qualification has been completed by these documentation/code changes.

## Select the Operation First

| Scenario | Correct entry point | Modifies cloud resources? |
| --- | --- | --- |
| Check tracked configuration | `deployment plan --operation upgrade --release RELEASE.json` | No |
| Check an existing-workload image upgrade | `scripts/check-deployment-preflight.sh --environment NAME --operation image-upgrade --manifest PLAN.json --image GHCR_DIGEST` | No cloud writes; validates exact candidate, CI, schema, startup and retained identity/configuration |
| Check whether the desired deployment can proceed | `scripts/check-deployment-preflight.sh --environment NAME` | No; shared target plan and every required evidence gate |
| Check the health of an existing deployment | `scripts/check-deployment-health.sh --environment NAME` | No; read-only live environment checks |
| Create a new environment | `deployment plan --operation create` -> complete preflight -> `deployment create --plan-sha256 SHA256` | Provision does |
| Upgrade persistent staging | Reviewed plan and CI image provenance -> `deployment upgrade` (or a scoped existing-workload rollout) | Updates selected resources; never implies reset |
| Check Console release features | `deployment console-check --environment NAME --cloud-id UUID --product-id UUID --test-account-id UUID` | Uses an existing Test Lab account; creates private login sessions, otherwise GET/HEAD only |
| Take over an existing environment | Transfer matching controller state and SecretStore -> complete preflight and current health check | Preflight does not |
| Prepare or migrate Object Storage | [Storage policy](object-storage-policy.md) and [storage operations](storage-credential-lifecycle.md) | Only explicitly selected bootstrap/migration/cutover steps |
| Restore core data after deployment | [Matched backup/restore procedure](backup-restore.md) under a maintenance/write fence | Explicit restore replaces selected datasets after a safety backup |
| Reconcile an interrupted deployment | `deployment reconcile-execution --environment NAME --plan-sha256 ORIGINAL_SHA --confirm video-cloud-NAME` on the original controller | Releases only verified original execution fences; archives receipts without resource rollback. Partial writes or uncertain acceptance require separately qualified recovery. Repeat complete preflight afterward. |
| Import original storage completion evidence | `deployment storage-import-state --environment NAME --purpose media\|ota --source-runtime ORIGINAL_RUNTIME --confirm STACK` | Writes validated original evidence to the canonical environment directory; never changes cloud resources or generates a new completion time. See [storage evidence migration](storage-credential-lifecycle.md#environment-owned-receipts-and-proof-location). |
| Schedule online PostgreSQL backups | [Daily PostgreSQL procedure](postgres-backup-restore.md) and isolated restore qualification | Creates dedicated backup resources; production scheduling requires measured evidence |
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

### Tracked Environment and Ignored Runtime

| Location | Content | May be committed? |
| --- | --- | --- |
| `cloud_env/<env>/environment.env` | Stack, DNS root, logical location, public OAuth settings and explicit Test Lab intent | Yes; never client secrets |
| `cloud_env/<env>/deployment.env` | Architecture, deployment adapter, DNS adapter | Yes |
| `cloud_env/<env>/overrides/*.env` | Reviewed environment differences | Yes |
| `cloud_env/<env>/runtime/` | Deterministic secret-free resolved configuration only | No |
| `~/.config/rtk_cloud/<env>/deployment/adapters/lke/account.env` | Operator-confirmed active-service limit | No |
| `~/.config/rtk_cloud/<env>/deployment/adapters/lke/state.env` | Environment/stack-bound original cluster and node-pool IDs | No |

Maintain the confirmed quota in the selected private environment store. Use
`0700` directories and a `0600` `account.env`; the only allowed assignment is
`LKE_ACTIVE_SERVICE_LIMIT`. Existing legacy provider state is migrated explicitly:

```sh
./scripts/deploy-environment.sh provider-import-state \
  --environment staging \
  --source-runtime /absolute/original/cloud_env/staging/runtime \
  --confirm video-cloud-staging
```

Use the actual limit confirmed by the Linode account owner. Use the literal `unlimited` only when the account owner confirms there
is no active-service cap. The live inventory and projected resource count are
still queried and reported.

## Read-Only Preflight

Complete preflight derives the entire target in memory from maintained settings,
managed lifecycle bindings and an immutable formal release. It does not require
or discover a private workload-spec plan. The default selected release is
`~/.config/rtk_cloud/<environment>/deployment/release.json`; `--release` selects
another explicit manifest. `deployment plan` prints a sanitized plan, not GO.

```sh
go run ./scripts/go/rtk-cloud -- deployment plan \
  --environment staging --operation upgrade --release /reviewed/release.json
scripts/check-deployment-preflight.sh \
  --environment staging --release /reviewed/release.json \
  --report /private/controller/staging-preflight.json
```

Review the reported plan SHA-256. Every required gate must PASS; FAIL, ERROR,
BLOCKED, SKIPPED or missing evidence means NO-GO. `--fast` cannot authorize a
complete update. The report includes stable gate IDs, cause, repair, impact and
recheck instructions, without desired Secret values or private baseline specs.
English output uses exit codes 0 (selected operation passed), 1 (NO-GO/check
failure), 2 (invalid invocation/configuration).

## Existing Pod image upgrade: GO / NO-GO

For an update of existing Pods with current features and configuration retained,
use the existing checker with `--operation image-upgrade`. This scope is distinct
from default full-deployment rendering, feature activation and data acceptance.
The default legacy renderer's managed-PKI refusal does not qualify or prohibit
this separately checked image-only path.

GO requires all selected immutable images to pass authenticated full amd64 pulls,
match the selected source checkouts, and have successful exact-source main CI
and formal `Publish LKE image` jobs. Supply every selected RTK container/sidecar
image and every existing consumer of each selected package (APIs, workers and
auxiliary executables), with complete candidate Deployment JSON using repeatable `--image` and
`--manifest` flags (a Kubernetes List is supported). Each candidate must have an
existing configuration/rollback owner, and its nonimage spec must match the live
spec. Changes to identity, mounts, environment, PVCs, ports or feature flags
require their separately reviewed configuration/migration operation. Retained
ServiceAccount-token projections are checked for UID/fsGroup file read access;
projected Secret/configuration sources still require separate effective-access
qualification.

The check additionally requires applied candidate AM/Billing migration versions,
all current environment Deployments/StatefulSets/DaemonSets and their application
Pods to be Ready at their current generation (OnDelete StatefulSets require
Ready owned Pods at the declared container/init images rather than an automatic
rollout revision), and the required public CertIssuer
route plus actual client mTLS and anonymous TLS denial. Pending/failed CI,
unapplied migrations, incomplete inventories or failed startup/PKI checks mean
NO-GO. `--fast` cannot qualify this operation. Post-deploy with the same operation
also requires every selected Pod-template image to equal the candidate.

For a selected payment simulator, the effective configuration must disable
startup migration. The checker executes its **published artifact** in an isolated
local Docker fixture with schema CREATE denied, then requires its health endpoint
to return HTTP 200 from inside the isolated network; no host port is exposed. The fixture uses no environment data or credentials, is private to the
invocation and is removed after success or failure. Cleanup failure prevents GO.
These image-upgrade checks read cloud state; Docker pulls and disposable local
fixtures change local Docker state. Docker and GitHub CLI authentication are
required. Schema reads use the existing LKE PostgreSQL operator access. Current
live PKI checks cover installed PKI schema/identity prerequisites; this operation
does not replace a separately required PKI schema migration qualification.

```sh
# Use the selected release checkout and reviewed environment-private plan.
# Repeat both flags for all affected workloads and package digests.
scripts/check-deployment-preflight.sh --environment staging \
  --operation image-upgrade --checks ghcr,mounts \
  --manifest "$HOME/.config/rtk_cloud/staging/deployment-readiness-candidates/current/plan.json" \
  --image "$RELEASE_IMAGE" --report /tmp/staging-image-upgrade.json
# After the authorized rollout, require the same exact candidate images:
scripts/check-deployment-health.sh --environment staging \
  --operation image-upgrade --checks ghcr,mounts \
  --manifest "$HOME/.config/rtk_cloud/staging/deployment-readiness-candidates/current/plan.json" \
  --image "$RELEASE_IMAGE" --report /tmp/staging-image-upgrade-health.json
```

Keep plans containing runtime configuration private (0700 directories/0600 files)
in the selected environment's SecretStore. Read each result's scope and selected
candidate set. GO applies to updating those existing Pod images with retained
settings. Disabled feature activation and full business/data qualification remain
separate results; they do not become image-upgrade blockers simply because their
features have not been enabled. A prerequisite pass cannot predict every future
application defect; recheck exact images and required service behavior after rollout.

## Deployment Phase Checks

Choose the check for the deployment phase. Each shell entry prints its purpose
and when to use it before building or contacting external systems:

```sh
# Before a whole-environment create/upgrade: desired inputs and safe migration prerequisites.
scripts/check-deployment-preflight.sh --environment staging --fast \
  --report /tmp/staging-pre-deploy-check.json
# After deployment: current Kubernetes mirrors, live PKI and deployed routing.
scripts/check-deployment-health.sh --environment staging --fast \
  --report /tmp/staging-post-deploy-check.json
# Direct Go entry points:
go run ./scripts/go/rtk-cloud -- deployment check --phase pre-deploy --environment staging --fast
go run ./scripts/go/rtk-cloud -- deployment check --phase post-deploy --environment staging --fast
# Scoped pull evidence; this is not complete release GO:
go run ./scripts/go/rtk-cloud -- deployment credentials-check --environment staging \
  --read-only --checks ghcr --image "$RELEASE_IMAGE"
```

The shell builds the checker once and preserves its 0/1/2 exit status.
Both named scripts are read-only. Complete preflight uses the same compiler and
resource bodies as create/upgrade; no schema-1 file selects a different renderer.
A production protection profile adds HA, custody/escrow and recovery/lifecycle
qualification to the same required-gate engine. Missing implementation or evidence
blocks every environment consistently. `--fast` reports required gates SKIPPED.

Use `deployment credentials-check` for a scoped provider/image/TLS check; full
preflight does not accept `--checks`, ad hoc `--image` or `--manifest` inputs in
place of the selected release. Use `--operation image-upgrade` only for the
separately described narrower existing-consumer update.
Post-deploy health still checks the existing runtime. Its PASS is health evidence,
not complete release/plan acceptance or permission to initialize identities.
The legacy credentials script defaults to post-deploy provider canaries; use the
named health entry for routine read-only inspection.

| Mode | Provider writes and receipts | Explicit `--image` |
| --- | --- | --- |
| Legacy/direct post-deploy default | Selected DNS/storage canaries and validation receipts remain enabled | Full controller-host `linux/amd64` pull |
| Named preflight/health wrappers, or `--read-only` | No canaries or new receipts | Full pull remains enabled |
| `--fast` | Implies read-only | Manifest/config and platform metadata; full pull reported as skipped |

Without `--image`, GHCR checks repository access. Metadata access, a controller
pull that may reuse cached layers, and a cluster rollout provide different
evidence. A controller pull does not prove cold-node downloads or matching live
`imagePullSecret` credentials. Supply each affected immutable image explicitly.

`--timeout` defaults to **2m for fast mode and 10m otherwise**, measured after
CLI/configuration validation and any shell build. The checker limits independent
read probes to four and full pulls to two; provider writes remain sequential.
Individual HTTP, Kubernetes, SQL and pull operations have shorter deadlines.
Temporary read failures retry once within the remaining budget. On cancellation,
created canaries receive up to 30s of additional cleanup time, with cleanup
failure reported separately. See the [checker reference](../scripts/README.md#existing-environment-deployment-checks)
for exact limits and retry conditions.
`--report PATH` writes sanitized JSON with mode 0600 to an existing parent
directory. Schema version 1 records `phase`, `read_only`, scope, coverage and per-check status/code,
resource, required flag, duration, attempts, dependencies, message, next action
and evidence time/level. Final statuses are `PASS`, `FAIL`, `ERROR`, `BLOCKED`,
and `SKIPPED`; required incomplete checks prevent success.

The shell and compiled checker return **0** for successful required selected
checks, **1** for check/report failure and **2** for invalid inputs/configuration.
Use the shell for exact exit codes; `go run` may return 1 for a program that exits
2. A PASS covers the selected checks; complete release approval still requires
the [release gates below](#upgrade-persistent-staging-release-gates).
See [checker options and PKI gates](../scripts/README.md#existing-environment-deployment-checks)
for TLS/mount qualification, optional PKI checks and explicit credential repairs.

## Retired schema-1 managed workload upgrade

`deployment prepare-upgrade` and private `deployment/managed-upgrade.json`
workload-spec selection are retired at the public complete deployment entry.
Their presence or absence cannot change the v2 renderer, qualification or plan.
Preserve any original private file for reviewed migration evidence in the same
environment's private archive; never commit it. Necessary differences belong in
maintained configuration or typed lifecycle/ownership bindings, not copied live
workload specs. Use the explicit migration utility to create a review candidate:

```sh
go run ./scripts/go/rtk-cloud -- deployment adopt-managed-state \
  --environment staging --release /reviewed/release.json
```

This read-only cluster inventory writes a new 0600
`~/.config/rtk_cloud/<environment>/deployment/identity-bindings.candidate.json`
inside 0700 directories; it never overwrites active bindings or updates workloads.
It records exact owner UIDs and typed mount/credential/identity references, without
copying commands, application flags, probes, replicas or live desired specs.
Review against maintained configuration before selecting the active
`identity-bindings.json`. Review admission bindings and all consumers separately;
a candidate is not a complete bootstrap bundle or a GO receipt.

When a matching private schema-1 plan exists, the utility preserves its original
bytes at `deployment/archive/managed-upgrade-<sha256>.json`, idempotently and
without exposing contents. The original remains available until migration is
verified. Necessary configuration differences must move into maintained overrides;
do not import its workload specs. Foreign/deleting owners, unknown image roles,
inline private identity settings or an unverified file-storage OpenBao deployment
block adoption. Matched Raft storage/seal migration remains a separate required
operation; importing references never proves issuer lineage.

Controller consumer, authentication and worker policies belong in maintained
architecture/environment overrides. Adoption excludes them even when an old Pod
used a Secret reference. Set exact unprefixed receipt IDs in
`PKI_REQUIRED_CONSUMERS_DEVICE`, `PKI_REQUIRED_CONSUMERS_APP`,
`PKI_REQUIRED_CONSUMERS_SERVICE`, `PKI_REQUIRED_CONSUMERS_MQTT` and
`PKI_REQUIRED_CONSUMERS_OPENBAO_TLS`. Optional
`PKI_REQUIRED_BUNDLE_CONSUMERS_<DOMAIN>` settings select separate bundle consumers;
an absent setting uses that domain's native consumer list. Empty, duplicated or
`service:`-prefixed IDs block planning. The retired global
`PKI_REQUIRED_CONSUMERS` cannot supply these domain policies.

Full managed deployment requires `PKI_APP_CRL_WORKER_ENABLED=true` and
`PKI_SERVER_CRL_DOMAIN=service,mqtt,openbao_tls` (order is immaterial).
Maintain `PKI_REQUIRE_USER_MFA=true|false` explicitly; production requires `true`.
The renderer explicitly clears absent legacy global/bundle settings so `envFrom`
imports cannot reactivate them. These settings do not establish live consumer
acknowledgments or production qualification. A missing consumer integration needs
its actual lifecycle implementation and qualification, not a placeholder name.
The standard profile explicitly preserves the existing non-MFA controller policy;
production selects MFA in its protection overrides. The 2026-10-06 staging
configuration migration preserves the original private plan's Device, Service,
MQTT and MQTT-bundle receipt IDs only. Its absent App/OpenBao transport policies
remain blockers until their actual consumers and bindings are qualified. These
dated settings are migration inputs, not live acknowledgment evidence.

Execution requires the exact plan SHA-256 from complete preflight:

```sh
go run ./scripts/go/rtk-cloud -- deployment upgrade \
  --environment staging --release /reviewed/release.json \
  --confirm video-cloud-staging --plan-sha256 REVIEWED_SHA256
```

The executor requalifies and fences inputs, owner UIDs, retained identity/data,
resource versions and completed-stage dependencies before advancing. The exact
server-dry-run body is the write body. Private rollback and partial execution
journals are retained under the same environment SecretStore. No automatic
rollback overwrites concurrent changes or restores schema/PKI state blindly.
Complete post-deploy acceptance is required before recording completion.
Current missing capability adapters keep this entry NO-GO; no upgrade has been
performed by this implementation work.

## CertIssuer Route Convergence

CertIssuer uses one resolved TLS policy for its internal HTTPS port, required
server DNS names, public Ingress and the ingress-nginx controller's passthrough
setting. General HTTP ingress still terminates TLS. The public CertIssuer path
preserves the original client TLS handshake and targets `certissuer:9443` in
the Video Cloud namespace through `certissuer-public-mtls`; it does not use the
HTTP ExternalName bridge or attach the edge Web PKI Secret.

The read-only pre-deploy assessment allows a terminating legacy route when the
deployer can safely migrate it. Automatic migration recognizes only the prior
`video-cloud-staging-certissuer` or shared `video-cloud-staging-https` objects
in the selected ingress namespace, with matching stack/provider/RTK ownership,
nginx class and the exact public host, root path and historical CertIssuer
backend. A shared Ingress keeps its unrelated rules and TLS hosts. Foreign or
unrecognized routes, extra CertIssuer paths, changed ownership and unfinished
prior migration records require review; do not relabel a resource to bypass
these checks.

Before switching an existing route, the deployment validates the concrete
Service, available CertIssuer workload and installed serving identity against
the resolved public hostname. A managed identity must already have approved
DNS policy and an installed identity; a static certificate must already cover
the public host and be within its validity interval. A new environment can be
planned before these objects exist, but routing mutation waits for installed
serving material. Route convergence does not silently create a successor
issuer, change stored CA material, or reissue an existing serving certificate.
Complete a necessary identity-policy or certificate change through the normal
approved PKI lifecycle first.

A missing local kubeconfig is not evidence that the environment is new. The
pre-deploy check requires provider discovery to confirm that the selected
cluster is absent before taking the initial-deployment path without Kubernetes
access. Discovery uses the deployment's actual explicitly selected or persisted
cluster ID and reads that ID freshly; an unavailable, missing or mismatched ID
does not become an absent-cluster result or fall back to a same-label cluster.
An explicitly selected label must agree with the selected ID's live label; the
default stack-derived label does not override ID selection. With no selected
ID, discovery searches the resolved label across the complete
provider inventory; incomplete, invalid or changing pagination blocks an
absence claim. Otherwise restore the matching environment selection and
kubeconfig and inspect the existing cluster. Initial planning remains read-only
and validates any cached CertIssuer TLS state without creating identity files
or contacting Kubernetes.

For a full static deployment, the desired state is the persisted material in
`~/.config/rtk_cloud/<environment>/pki/certissuer/`: `server.crt`, `server.key`,
`service-ca.crt`, `client.crt`, `client.key`, `factory.crt` and `factory.key`.
Before installing it, the deployer checks completeness, configured algorithm,
key/certificate pairs, TLS purposes, validity, CA signatures and all resolved
public/internal server SANs. The stored CA and server leaf must also exactly
match the selected live public certificates. Missing, invalid or stale state
blocks full reconciliation; restore matching protected controller state or use
the approved identity lifecycle. Do not delete existing material to regenerate
a CA, change the algorithm to force replacement, or disable health checks to
make an unrelated controller's state pass.

A compatible full static deployment can repair an owned Service's mutable old
port/type and the static listener's old settings. A headless Service's immutable
ClusterIP topology still blocks this path. Its pre-deploy migration planner
therefore qualifies the unchanged persisted identity and exact current
Service/Deployment ownership before deferring these mutable topology checks
until the full renderer installs the desired configuration. This exception
does not permit unknown owners, replacement identity or managed PKI adoption.
If the Deployment is absent, initial bootstrap requires the Service, any
selected-stack/public-host CertIssuer route and `certissuer-runtime` Secret to
be absent as well. The existing-cluster Secret check reads only its metadata
UID. A surviving Secret requires restoring the existing identity owner source,
even when no local TLS state remains; it cannot authorize a newly minted CA.
DNS-only and targeted rollouts use the strict installed-topology planner because
they do not reconcile these static dependencies. Every route switch and post-deploy check
still requires the desired Service/listener and converged serving workload;
pre-deploy permission to repair old topology is not a live-health PASS.

Deployment-owned public routing runs the migration during both full public
HTTPS setup and deploy-only Video Cloud routing updates. It rereads the
inventory before mutation, uses current UID/resource-version preconditions for
replacement/deletion, installs the canonical passthrough route and verifies
that no conflicting route remains. Repeating an already converged deployment
does not recreate or delete Ingress objects.

Each actual change writes a mode-0600 restore journal under
`~/.config/rtk_cloud/<environment>/deployment/journals/certissuer-ingress/`
with real mode-0700 parent directories. Both planning and execution resolve the
same explicit environment; checkout paths and artifact-directory overrides do
not select the journal location. Definite failures roll
back only objects that still match the migration's own recorded result. An
uncertain mutation outcome or concurrent drift stops automatic rollback and
blocks retry until the restore record and current API objects have been
reviewed. A legacy restore journal in a checkout or former artifact directory
blocks retry until explicitly migrated. Preserve its exact bytes and filename;
import it into the selected canonical directory without replacing an existing
record, verify the copy, and archive the original under the same environment's
private `migration-backup/certissuer-ingress/` area. Conflicting copies require
review.
Do not rewrite an unfinished status to bypass the fence. These are recovery
records, not GO or deployment-completion receipts.
Keep the journal; do not delete it to force a retry or disable the
admission webhook to permit duplicate host/path ownership.

This convergence validates routing and installed identity prerequisites. Run
the post-deploy health check and required authenticated public/internal endpoint
acceptance probes afterward. It does not qualify a whole-Deployment replacement
of a separately managed PKI installation; follow the preservation boundary in
the [Dev managed PKI runbook](product-services-dev-pki.md#public-certissuer-configuration-and-rollout-boundary).
Full legacy reconciliation checks this boundary before resource mutations and
fails with the supported targeted or route-only alternatives instead of
overwriting managed identity and discovering the mismatch after deployment.

## Create a New Environment

Review maintained configuration, the formal release, storage/capacity and managed
bootstrap lineage. Use the shared create operation:

```sh
go run ./scripts/go/rtk-cloud -- deployment plan \
  --environment staging --operation create --release /reviewed/release.json
scripts/check-deployment-preflight.sh \
  --environment staging --operation create --release /reviewed/release.json
```

Fresh creation requires staged infrastructure qualification, then Kubernetes
admission after the API exists, then managed bootstrap/workload/routing and full
acceptance. The complete provider/bootstrap stage adapters are not yet available;
this remains a BLOCKED / NO-GO operation. Do not use retired full provision as a
fallback. The intended execution entry requires stack confirmation and the newly
reviewed create-plan SHA-256, and cannot enable normal production services until
production qualification passes.

See [`staging-from-scratch.md`](staging-from-scratch.md) for the older test/bootstrap
history, not an alternate complete create path during this replacement.

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
   release. `deployment plan` reads immutable release intent and does not materialize
   runtime or reuse old generated image overrides. `provision --deploy`
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
   Before Product PKI lifecycle acceptance, add --require-product-pki to
   verify that the controller pins an active Device Root in its registry.
   Before the write fence, run the full credential check without --fast or --read-only
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

   This step intentionally creates/mutates test data and needs that scope of
   authorization. Do not reuse devices after lifecycle deactivation/unprovision.
   A device token 200 does not clear an app token 401. Record each unverified
   stage, lack of nonempty Billing/invoice fixtures and product-semantic defects
   as release blockers or explicitly accepted exceptions, not as PASS.

The legacy reset/provision path in `run-staging-e2e.sh` is retired and refuses
execution before resource mutation. Use the common reviewed create/upgrade plan
for complete environment work; acceptance runs keep reset and provisioning
disabled. Authorization does not reactivate the retired renderer.

### Registered-service listener (opt-in, not deployed)

For the existing dev managed-PKI stack, complete the separate
[Product service PKI prerequisite](product-services-dev-pki.md) before enabling
this listener or any of the six Product registrars. It preserves the live CRL
consumers and uses a new bootstrap session rather than the retired state PVC.

`LKE_ACCOUNT_MANAGER_SERVICE_REGISTRATION_ENABLED` defaults to `false`. When
reviewed and enabled, the LKE renderer adds a private `account-manager`
Service port `8443`, the matching Account Manager mTLS listener settings, and
an ingress NetworkPolicy limited to the MQTT foundation, Shadow worker,
WebRTC service, and video-storage service Pod identities in the Video Cloud
namespace. It does not add a public ingress route or enable Product writes.
The NetworkPolicy targets the Pod's named registrar listener (`service-reg`),
which fits Kubernetes' 15-character port-name limit. Its destination port may differ
from the Service port: an existing `8443 -> service-reg:9444` mapping must allow
Pod port 9444. Preserve that mapping during routing-only reconciliation.

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

A Git clone includes configuration, not deployment state. Recover the matching
environment store under `~/.config/rtk_cloud/<env>/`, preserving original identity,
keys, provider bindings and completion proofs, with `0700` directories and `0600`
files. Do not transfer operational state into `cloud_env`, copy only kubeconfig,
or reprovision from empty controller state. This handoff does not restore cloud
databases or OpenBao.

`restore-staging-runtime.sh` is retired and returns an English migration error
without reading/copying source state. Explicitly import original storage receipts
with `deployment storage-import-state`, and original LKE provider state with
`deployment provider-import-state`. The provider operation compares the original
selected Kubernetes API/CA with the native cluster identity, verifies recorded
node-pool IDs, archives source bytes in private `migration-backup/provider-state/`,
and exclusively publishes the environment/stack-bound IDs and original quota.
It preserves Kubernetes credentials and never creates completion evidence.
Regenerate configuration after import and retire old operational files only
after the maintained-code cutover. Other legacy-state migration must also finish
before full qualification. The normalized provider
metadata producer and acceptance reader both use
`~/.config/rtk_cloud/<env>/deployment/provider-preflight.env`. It contains region
and quota metadata only and cannot substitute for any readiness gate.

Then run the shared exact-release preflight and post-deploy acceptance checks.
Missing state or unfinished migration/capability is NO-GO. The older
[`staging-runtime-bootstrap.md`](staging-runtime-bootstrap.md) steps are historical
context and must not be used as a fallback.

## Core Data Backup and Restore After Deployment

For the platform PostgreSQL 16 instance, use
[Daily PostgreSQL Backup and Restore](postgres-backup-restore.md) to plan/configure
the native daily full backup and rehearse an isolated restore. Configure with the
schedule suspended, qualify source impact and recovery, then explicitly enable
the reviewed daily schedule with `--enable-schedule --qualification FILE`.
The default time is 03:00 Asia/Shanghai. Its database-only archive and isolated drill do
not perform production application cutover. Dated staging qualification remains
pending until evidence is recorded in that procedure.

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

## Freeze the latest formal release

Use `deployment freeze-release --environment <environment> --provider-release
<reviewed-provider-manifest> --output <new-release-manifest>` to fix the refreshed
workspace main and every service gitlink before retrieving exact-source CI
publication artifacts. The reviewed provider manifest follows
`rtk-cloud.release/v1` and supplies explicit upstream digests, including PostgreSQL,
ingress and OpenBao. Its RTK application entries are ignored and replaced by the
frozen source's formal CI digests. The backup runner must have the same PostgreSQL
base digest. Missing API, MQTT PKI or backup publication blocks selection.

The command writes a new 0600 file, never overwrites the selected release, and
prints `FROZEN`, which is publication selection evidence rather than deployment
GO. Review the output and pass it as `--release` to full preflight. A dirty or
out-of-date checkout, pending/failed latest exact-source run, wrong rerun artifact,
missing consumer digest or mismatched backup base requires repair and a new freeze.
No environment resources are installed by release freezing.

The new manifest destination must be an absolute JSON filename under
`~/.config/rtk_cloud/<environment>/deployment/releases/` with real 0700 parent
directories. The writer creates missing private directories and writes a new
0600 file; existing selections are never overwritten. External/worktree and
other-environment output paths are rejected before querying publication. Review
the resulting candidate and pass that same canonical path to `--release`.
## Environment-owned storage evidence

Storage receipts and migration proofs live under `~/.config/rtk_cloud/<environment>/deployment/storage/` (0700 directories, 0600 files); completed private journals remain in `migration-backup/`. Preflight and deployment validate the same state. Preflight never performs cutover or creates completion evidence. Workspace runtime files are explicit legacy import sources only. See [storage evidence location and import](storage-credential-lifecycle.md#environment-owned-receipts-and-proof-location).
