# Scripts Directory

This directory contains workspace-level operational scripts for documentation checks, deployment-evidence collection, Linode staging provisioning and deployment, Brand Cloud creation, and GitHub Actions self-hosted runner management.

Unless stated otherwise, run commands from the workspace root.

## Environment, Architecture, and Adapters

`cloud_env/dev`, `cloud_env/staging`, and `cloud_env/prod` are environment instances. Shared Kubernetes intent lives in `cloud_deploy/architectures/kubernetes`; the LKE implementation lives in `cloud_deploy/adapters/lke`. An environment selects its architecture and adapter through `deployment.env`; `cloud_env/<env>/<provider>` is no longer used.

Canonical entry points:

```sh
scripts/check-deployment-credentials.sh --environment staging
go run ./scripts/go/rtk-cloud -- deployment plan --environment staging
go run ./scripts/go/rtk-cloud -- deployment provision --environment staging --confirm video-cloud-staging
go run ./scripts/go/rtk-cloud -- deployment acceptance --environment staging
```

Use the same rehearsal entry point for every environment:

```sh
scripts/deploy-environment.sh test \
  --environment dev \
  --confirm video-cloud-dev
```

Replace `dev` and the confirmation stack with `staging` or `prod`; do not copy or fork the script. `test` removes cloud and DNS resources owned by the stack after success or failure and retains sanitized evidence.

Non-secret generated state lives under Git-ignored `cloud_env/<environment>/runtime/`.
Kubeconfigs, service secrets, private keys and Device credentials live in the
environment [SecretStore](../docs/secret-store.md), not the workspace runtime.
LKE cluster, pool, and resource IDs are adapter-private runtime state. Shared
Kubernetes runtime and load tests read normalized runtime. If a matching cluster
exists but the environment lacks matching OpenBao/PostgreSQL operator state,
provisioning stops before mutation to prevent new secrets from being paired
with an old PVC.

## Core Backup and Restore

`go run ./scripts/go/rtk-cloud -- backup --help` and `restore --help` expose
environment-scoped maintenance backup, inspection, remote transfer, restore,
verification and explicit resume. See [Core Backup and Restore](../docs/backup-restore.md)
for the authoritative inventory, configuration/check requirements, safety
backup, separate escrow and production qualification limits. The v1 archive is
`scope=core`: Redis durable shadow data is included, cache is not; media/firmware
payloads and external audit recovery are separate.

`restore-staging-runtime.sh` is a narrow non-secret controller-state copy tool,
not a database/OpenBao/SecretStore restore command.

## Kubernetes Deployment Details

The LKE adapter owns Linode LKE cluster discovery, creation, and kubeconfig retrieval. Shared Kubernetes runtime owns RTK workloads, namespaces, secrets, deployments, services, ingress, network policy, rollout, and E2E orchestration. Deployment requires container images. The following may be set explicitly:

- `LKE_POSTGRES_IMAGE`
- `LKE_VIDEO_CLOUD_IMAGE`
- `LKE_ACCOUNT_MANAGER_IMAGE`
- `LKE_CLOUD_ADMIN_IMAGE`
- `LKE_FRONTEND_IMAGE`
- `LKE_CLOUD_LOGGER_IMAGE`

Each service repository publishes its release image to GHCR. The workspace resolves the pinned submodule commit, verifies that the corresponding image exists, and emits the `LKE_*_IMAGE` mapping used by deployment and E2E. Provider IDs `k8s`, `gke`, `aks`, and `eks` are reserved for generic Kubernetes, GCP, Azure, and AWS. Their current adapters fail before cloud API, DNS, or state mutation. `lke` is the only live-validated Kubernetes provider.

Private GHCR pulls use operator credentials from the environment SecretStore:

```env
GHCR_PULL_USERNAME=your GitHub username
GHCR_PULL_TOKEN=GitHub token with read:packages
GODADDY_KEY=GoDaddy API key
GODADDY_SECRET=GoDaddy API secret
LINODE_OBJ_ACCESS_KEY_ID=Object Storage access key
LINODE_OBJ_SECRET_ACCESS_KEY=Object Storage secret key
LINODE_OBJ_ENDPOINT=https://REGION.linodeobjects.com
LINODE_OBJ_BUCKET=artifact bucket name
```

Never put `GHCR_PULL_TOKEN` in tracked `env/stack.env`, Git, a PR, or logs. Run the read-only credential preflight before deployment:

```sh
scripts/check-deployment-credentials.sh --environment staging
```

By default, the command reads individual `0600` files only from `~/.config/rtk_cloud/<environment>/operator/env/`. Shared profiles, `--env-file`, and process-environment overrides are rejected; missing values fail closed. The environment-specific check covers Linode profile/LKE read access, pull access to five service GHCR repositories, GoDaddy domain read access, and, when clip direct upload is enabled, Object Storage inventory, limited-key scope, signed listing, and a write/read/delete canary. Failures return nonzero. `deployment provision`, `deployment test`, and legacy `staging-provision` run the same checks before writing runtime files, resolving images, or creating cloud resources. Secret values are never printed; a redacted receipt is stored in ignored runtime state.

If the only failure is HTTP 404 for the configured Object Storage bucket, explicitly create it and immediately repeat signed-read validation:

```sh
scripts/check-deployment-credentials.sh \
  --environment staging \
  --create-missing-object-storage-bucket
```

This flag is valid only for `credentials-check`. Normal checks and deployment never create a bucket automatically. Creation or validation failure returns nonzero and blocks deployment.

If an existing bucket returns HTTP 403, explicitly use `LINODE_TOKEN` to create a replacement limited key with `read_write` access to that bucket:

```sh
scripts/check-deployment-credentials.sh \
  --environment staging \
  --grant-object-storage-bucket-access
```

The script validates read/write/delete with the replacement key before atomically updating `LINODE_MEDIA_OBJ_ACCESS_KEY_ID` and `LINODE_MEDIA_OBJ_SECRET_ACCESS_KEY` as `0600` files. Failed validation leaves the profile unchanged. The old key is not revoked automatically because another bucket may still use it. Bucket creation and access grant are separate operations and cannot be combined in one invocation.

Optional Docker CLI verification:

```sh
printf '%s' "$GHCR_PULL_TOKEN" | \
  docker login ghcr.io \
    --username "$GHCR_PULL_USERNAME" \
    --password-stdin
```

Deployment loads values from the environment profile automatically; repeated `export` is unnecessary. See `docs/storage-credential-lifecycle.md`. GitHub Actions uses repository secrets `GHCR_PULL_USERNAME` and `GHCR_PULL_TOKEN`.

For Kubernetes runtime manifests, put non-secret YAML in `scripts/go/rtk-cloud/templates/k8s/*.yaml.tmpl` and render it with Go templates. Go helpers supply shared labels, selectors, namespace, and image-pull-secret metadata. Do not build Secret YAML with `fmt.Sprintf`; create typed Kubernetes objects, apply JSON with `kubectl apply -f -`, and keep raw tokens/passwords out of errors and test logs.

Add workloads to the provider-neutral workload registry. Do not maintain separate deployment, service, image-validation, rollout-target, or Prometheus-target lists. The registry is authoritative for image environment keys, namespace, port, metrics path, resource-override prefix, and rollout timeout. Future GKE/AKS/EKS support must reuse it.

`.github/workflows/lke-image-artifacts.yml` is the workspace LKE image-manifest workflow. PRs run secret-free tooling validation. `workflow_dispatch` checks out pinned submodules, resolves `sha-<12-character-commit>` GHCR tags, verifies manifests, and uploads `lke-image-manifest.json` and `lke-image-env.sh`. It needs `CI_RUNNER_GITHUB_WORK_KEY` to read private `git@github.com-work:` submodules.

Prometheus targets come from the Go deployer's workload registry, not handwritten `scrape_configs`. A metrics-enabled workload declares its service, namespace, port, and `/metrics/prometheus` path. `provision --deploy` renders `video-cloud-prometheus-config`. The first version uses a workspace-managed ConfigMap without Prometheus Operator, ServiceMonitor, or PodMonitor. LKE exposes Valkey metrics through `redis-exporter.<platform namespace>:9121/metrics`.

Grafana runs as a private `ClusterIP` service in the observability namespace and reads internal Prometheus. Provisioning creates no public hostname, ingress, or TLS SAN for it. Platform administrators view Grafana in a same-origin iframe under Cloud Admin Platform View; the Cloud Admin BFF protects `/api/admin/grafana/*` with a platform-admin session.

Use `--env-root PATH` for another non-sensitive runtime directory. `--secrets-root` has been removed; set `RTK_CLOUD_CONFIG_ROOT` or the secrets subcommand's `--config-root` for sensitive data.

Kubernetes staging acceptance entry points are `scripts/run-staging-e2e.sh` and `scripts/setup-staging-e2e-data.sh`. General environment operations use `rtk-cloud ... --environment <name>`. Retired VM shortcuts and service-owned VM runtime paths are historical only; see `docs/linode-staging-deployment-snapshot.md`.

See `docs/cloud-env-layout.md` for the directory layout.

### `go run ./scripts/go/rtk-cloud -- lke-resolve-images`

Resolve service images for pinned submodule commits without creating a cluster, applying Kubernetes resources, or building service images:

```sh
go run ./scripts/go/rtk-cloud -- lke-resolve-images \
  --env-root cloud_env/staging/runtime \
  --owner hkt999rtk \
  --out .artifacts/lke-images/lke-image-manifest.json
```

The manifest includes all `LKE_*_IMAGE` mappings above. On LKE, `scripts/run-staging-e2e.sh` resolves missing images automatically into `<env-root>/artifacts/lke-images/<timestamp>/` and updates the latest manifest. Manual `LKE_*_IMAGE` exports are overrides. Official GHCR `sha-*` overrides must exactly match the pinned commit and still pass existence checks. Custom development-registry overrides remain supported.

### `go run ./scripts/go/rtk-cloud -- lke-build-images`

Legacy helper retained only for building and pushing the PostgreSQL staging image. Service images are not built by the workspace. Normal LKE staging uses `lke-resolve-images` and images published by each service repository.

## Runtime Dependency Policy

Production operational scripts under `scripts/` must not depend on Python. Do not add `python`, `python3`, `pip`, `boto3`, `conda`, inline Python heredocs, or a Python/conda environment requirement.

Implement JSON/YAML, Object Storage, TLS, MQTT, and HTTP API logic in a Go module/helper and call it from a thin shell wrapper. POSIX shell, `awk`, `sed`, `jq`, and `openssl` are acceptable for simple text processing.

Production Linode Object Storage artifact upload, download, and verification must use a Go helper. Do not install or invoke `awscli`, `boto3`, `pip install awscli`, `aws s3 cp`, or inline Python SigV4 uploaders. Repository CI/release/deploy workflows use repository-local `cmd/linode-object-storage` or an equivalent Go tool.

## General Checks and Synchronization

### `go run ./scripts/go/rtk-cloud -- status-all`

Show Git status and the latest commit for the workspace and every submodule:

```sh
go run ./scripts/go/rtk-cloud -- status-all
```

Use it before and after branch switches, pulls, or submodule updates.

### `go run ./scripts/go/rtk-cloud -- sync-all`

Fetch workspace and submodule remotes, then initialize/update submodules to the commits pinned by the superproject. It does not change those pinned commits.

```sh
go run ./scripts/go/rtk-cloud -- sync-all
```

### `go run ./scripts/go/rtk-cloud -- test-matrix`

Run the fast workspace baseline: workspace/submodule status, diff checks, and Go-based workspace checks. It does not run every service or product E2E test.

```sh
(cd scripts/go && go run ./rtk-cloud -- test-matrix)
```

### `pre-pr`

Use the same changed-path selector as GitHub Actions to run affected workspace policy checks, Go/JavaScript coverage, and Cloud Admin desktop/mobile headless E2E. It never connects to or deploys shared staging. Integration tests requiring PostgreSQL, EMQX, or other CI service containers remain in the PR CI plan.

Commit local changes and update `origin/main` first. The command rejects a dirty worktree so the selector cannot miss uncommitted paths:

```sh
git fetch origin main
go run ./scripts/go/rtk-cloud -- pre-pr --base origin/main
go run ./scripts/go/rtk-cloud -- pre-pr --base origin/main --dry-run
```

UI selection installs Node/Playwright dependencies by default. Use `--install=false` when already installed or `--ui=false` for a faster coverage-only pass. PR CI remains the final Linux and service-container merge gate.

### `test-services`

Run local tests for services, SDKs, frontend, and repository tooling. Use `--repo NAME` to select one repository. Go tests set `GOWORK=off` automatically.

```sh
(cd scripts/go && go run ./rtk-cloud -- test-services)
(cd scripts/go && go run ./rtk-cloud -- test-services --repo rtk_cloud_admin)
```

### `test-e2e`

Run workspace-owned deterministic E2E, MQTT harness, and load-report tooling tests. `--scripts` also runs root staging-script contract tests. `no-deprecated-staging-wrappers.test.sh` is a repository-governance migration gate, not an E2E test.

```sh
(cd scripts/go && go run ./rtk-cloud -- test-e2e)
(cd scripts/go && go run ./rtk-cloud -- test-e2e --scripts)
```

### `test-ui`

Run Playwright with headless Chromium. The default starts the real Cloud Admin Go BFF and deterministic fixture upstream to test browser -> BFF -> backend behavior. Every case keeps a final viewport screenshot; failures also keep video, trace, error context, and retry artifacts. Outputs include `results.json`, JUnit, `evidence-manifest.json`, `test_report.md`, and an HTML report. Desktop `--full` merges unavailable, empty, stale, expired, partial-failure, and lifecycle phases so conditional cases do not remain only `SKIP`. Desktop and mobile are separate targets and artifacts.

```sh
(cd scripts/go && go run ./rtk-cloud -- test-ui)
(cd scripts/go && go run ./rtk-cloud -- test-ui --desktop)
(cd scripts/go && go run ./rtk-cloud -- test-ui --mobile)
(cd scripts/go && go run ./rtk-cloud -- test-ui --full)
(cd scripts/go && go run ./rtk-cloud -- test-ui --desktop --run-id local-review-001)
(cd scripts/go && go run ./rtk-cloud -- test-ui --install)
```

Read-only staging UI E2E requires `E2E_BASE_URL`, `E2E_PLATFORM_SESSION_ID`, `E2E_CUSTOMER_SESSION_ID`, and explicit `E2E_EVIDENCE_SAFE=1` confirmation that only dedicated test accounts/data can be captured:

```sh
(cd scripts/go && go run ./rtk-cloud -- test-ui --staging)
```

### `test-catalog`

`tests/catalog.yaml` is authoritative for Test ID, owner, purpose, method, source selector, target, environment, and evidence policy. `check` validates numbering, duplicates, sources, Playwright registration, and generated-Markdown drift. `render` updates `docs/test-catalog.md`. Published IDs cannot be renumbered or reused; mark removed IDs `retired`.

```sh
(cd scripts/go && go run ./rtk-cloud -- test-catalog check)
(cd scripts/go && go run ./rtk-cloud -- test-catalog render)
```

### `test-live`

Explicit staging/live E2E entry point. The default prints a plan without modifying the environment. Live execution requires `--run` and the correct `--confirm` value.

```sh
(cd scripts/go && go run ./rtk-cloud -- test-live --environment staging --plan)
(cd scripts/go && go run ./rtk-cloud -- test-live --environment staging --run --confirm video-cloud-staging)
```

### `test-multicloud`

Qualify the deployed staging multi-cloud lifecycle with a formally email-activated
global owner. Plan mode is read-only. Run mode uses the owner's cached global
credential to create and rename a second empty Brand Cloud, accepts and revokes an
emailed viewer invitation, verifies read-only enforcement, and soft-deletes the
empty cloud through public APIs. It does not modify cloud state through PostgreSQL.

```sh
(cd scripts/go && go run ./rtk-cloud -- test-multicloud --profile staging-live --run-id manual-multicloud-001)
(cd scripts/go && go run ./rtk-cloud -- test-multicloud \
  --profile staging-live \
  --workspace ../.. \
  --env-root ../../cloud_env/staging/runtime \
  --brandname RTK-LOAD-CANARY-manual-multicloud-001-B01 \
  --run-id manual-multicloud-001 \
  --run \
  --confirm video-cloud-staging-lke)
```

The manual GitHub Actions entry point is `Multi-cloud Staging Qualification`.
Its run mode first creates the owner through public signup and real email
activation, provisions one representative device and app certificate, then runs
the multi-cloud and MQTT checks. Uploaded evidence excludes SQLite credentials and
raw logs. The sharing evidence covers the cloud-membership slice only; Product
read and pending Product-invitation invalidation remain separate qualifications.

### `go run ./scripts/go/rtk-cloud -- docs-check`

Validate workspace documentation entry points, important runbooks, E2E directories, submodule documentation, and contracts-submodule alignment.

```sh
go run ./scripts/go/rtk-cloud -- docs-check
```

### `go run ./scripts/go/rtk-cloud -- secrets-check`

Verify that sensitive paths such as `.secrets` are Git-ignored and scan tracked workspace files for accidentally committed private keys, tokens, passwords, or DSNs.

```sh
go run ./scripts/go/rtk-cloud -- secrets-check
```

## Evidence and Reports

### `go run ./scripts/go/rtk-cloud -- collect-evidence`

Collect private-cloud/product-readiness evidence into `evidence/` or another directory. The command produces a manifest, service commit state, health checks, and report summaries with sensitive data redacted.

```sh
go run ./scripts/go/rtk-cloud -- collect-evidence

RTK_EVIDENCE_ENVIRONMENT=evaluation \
RTK_EVIDENCE_OUTPUT_DIR=./evidence \
RTK_EVIDENCE_RUN_SERVICE_COLLECTORS=0 \
RTK_EVIDENCE_TARBALL=1 \
go run ./scripts/go/rtk-cloud -- collect-evidence
```

## Linode Cloud Environment Operations

### `go run ./scripts/go/rtk-cloud -- generate-load-devices`

Generate staging/load-test device identities through the production-like manufacturing flow. Each device gets a local private key and CSR; by default the real factory-enrollment API issues its client certificate and entitlement. This models a load test without real security chips while preserving the real enrollment flow.

Metadata records inventory `device_type` and ACL `service_options`; `device_type` is not an ACL source. Per-device `enroll start`, `enroll ok`, or `enroll failed` messages and `manifests/factory-enroll-results.jsonl` record results.

Default count and mix: 100 devices; `camera=40,light=25,air_conditioner=20,smart_meter=15`. Cameras receive `mqtt`, `video_streaming`, and `video_storage`; the other device types receive `mqtt`.

```sh
go run ./scripts/go/rtk-cloud -- generate-load-devices --env-root cloud_env/staging

go run ./scripts/go/rtk-cloud -- generate-load-devices \
  --env-root cloud_env/staging \
  --count 200 \
  --mix camera=80,light=50,air_conditioner=40,smart_meter=30

go run ./scripts/go/rtk-cloud -- generate-load-devices \
  --env-root cloud_env/staging \
  --out-dir cloud_env/staging/runtime/devices/manual \
  --force

go run ./scripts/go/rtk-cloud -- generate-load-devices \
  --env-root cloud_env/staging \
  --generate-only
```

Important options:

- `--count N`: device count; default `100`.
- `--mix SPEC`: device-type weights.
- `--prefix PREFIX`: device-ID prefix; default `load-device`.
- `--env-root PATH`: required environment directory.
- `--out-dir PATH`: default `cloud_env/staging/runtime/devices/test_device`.
- `--factory-url URL` / `FACTORY_ENROLL_URL`: factory-enrollment base URL override.
- `FACTORY_ENROLL_PRODUCTION_JWT`: short-lived token injected by the Account Manager production-run API; never write it to runtime, artifacts, or logs.
- `--factory-auth-key KEY` / `FACTORY_ENROLL_AUTH_KEY`: compatibility only when production JWT is unavailable.
- `--factory-id`, `--line-id`, `--station-id`, `--fixture-id`, `--operator-id`, `--batch-id`: manufacturing fields.
- `--generate-only`: sign with a local simulation CA without writing the cloud database.
- `--force`: rebuild an existing output directory.

Kubernetes runtime supplies factory-enrollment verifier configuration. E2E obtains `FACTORY_ENROLL_URL` through a service query/port-forward and asks Account Manager for a production JWT bound to Brand Cloud, device-item profile, quantity, and lifetime. The harness never reads the signing secret or signs tokens itself.

The active source of truth is `<env-root>/artifacts/test-data/<brand>-test-data.sqlite` (`0600`). `summary.json` is run evidence, not test-data authority. `loadtest.env` contains sourceable load-test parameters but no bearer token. Factory enrollment writes device keys, certificates, and bundles to the SQLite database. A `--generate-only` CA key remains in ignored runtime and must never be committed or used for production/customers.

Successful enrollment populates `video_cloud.factory_device_entitlements` and `video_cloud.cert_issue_requests`. `video_cloud.devices` usually appears only after activation/claim/runtime inventory and must not be used to assess factory-enrollment success. For the default 100-device mix, expect 100 entitlements, 100 successful certificate requests, active entitlement state, non-empty certificate fields, no missing sequence numbers, 40 camera ACLs, 25 light ACLs, 20 air-conditioner ACLs, and 15 smart-meter ACLs.

### `go run ./scripts/go/rtk-cloud -- unprovision-devices`

Read bindings from the SQLite test-data database and call the Account Manager user-facing unprovision API. This releases user/organization binding so a normal device can be resold or onboarded again. It does not SSH, expose raw Claim Tokens, revoke factory certificates, or modify the Video Cloud denylist.

```sh
go run ./scripts/go/rtk-cloud -- unprovision-devices \
  --env-root cloud_env/staging \
  --brandname RTK
```

`--bind-artifact FILE` is legacy compatibility; `--count N` limits devices; `--dry-run` lists accounts without login, API mutation, or artifact writes. Output includes a redacted stdout summary and `artifacts/device-unprovision/<brand>-device-unprovision-<timestamp>.json`, without passwords, tokens, or private material.

### `go run ./scripts/go/rtk-cloud -- migrate-env`

Retired VM-staging import command. Staging is Kubernetes-only and no longer imports state/environment files from retired submodule runtime directories. Use `sync-env` and Kubernetes service query/port-forward.

```sh
go run ./scripts/go/rtk-cloud -- migrate-env --env-root cloud_env/staging
# error: migrate-env is retired with the staging VM toolkit
```

`--env-root` remains required and `--force` remains syntax-compatible, but the command always reports retired.

### `go run ./scripts/go/rtk-cloud -- sync-env`

Generate Kubernetes stack/domain metadata from root metadata. `CLOUD_ENV_NAME` is the single stack slug/root; stack name, domains, Kubernetes namespace/labels, and service URLs derive from it. Root inputs are `CLOUD_ENV_NAME`, `CLOUD_PROVIDER`, `CLOUD_REGION`, and `CLOUD_DNS_ROOT_DOMAIN`.

```sh
go run ./scripts/go/rtk-cloud -- sync-env --env-root cloud_env/staging
go run ./scripts/go/rtk-cloud -- sync-env --env-root cloud_env/staging --check
```

`--check` does not edit files. LKE/Kubernetes does not require retired `topology/video-cloud-staging.yaml`. `CLOUD_PROVIDER=linode` exists only for retired VM metadata cleanup/check compatibility.

### `go run ./scripts/go/rtk-cloud -- provision-k8s`

Staging supports Kubernetes only. This command obtains kubeconfig, verifies namespaces, and waits for deployment/statefulset rollout. It does not create retired VM runtime or service-owned VM binaries. The only exception is the TURN data plane: the workspace manages one minimal standalone Linode coturn VM so UDP/TCP relay traffic remains outside Kubernetes Service/Ingress.

LKE installs metrics-server `v0.8.1` by default (`LKE_METRICS_SERVER_VERSION`) to provide `metrics.k8s.io` for `kubectl top` and HPA resource metrics. It does not replace Prometheus. `provision --deploy` also deploys private Prometheus, Grafana, Valkey, Redis exporter, and Loki.

Key settings include:

- `LKE_REDIS_IMAGE` (default `valkey/valkey:8-alpine`) and `LKE_REDIS_EXPORTER_IMAGE` (default `oliver006/redis_exporter:v1.74.0`).
- Valkey CPU/memory request and limit overrides; defaults `100m`, `128Mi`, `512Mi`.
- Redis-exporter overrides; defaults `50m`, `64Mi`, `256Mi`.
- `LKE_GRAFANA_IMAGE` (default `grafana/grafana:13.0.2`), `LKE_GRAFANA_ADMIN_PASSWORD`, `LKE_GRAFANA_PERSISTENCE`, and `LKE_GRAFANA_STORAGE` (default `5Gi`).
- `CLOUD_ADMIN_GRAFANA_BASE_URL` for the cluster-internal Grafana URL.
- `LKE_LOKI_IMAGE` (default `grafana/loki:3.5.1`) and `RTK_CLOUD_LOGGER_LOKI_URL`. Cloud Logger always uses Loki; no in-process fallback exists.

Account Manager uses platform Valkey as a read-through user cache while PostgreSQL remains authoritative. Cache misses or Redis failures read PostgreSQL and refill when available. The cache has no TTL; after direct DB repair, run `/app/rtk-account-manager-user-cache rebuild` for platform users or remove relevant brand/end-user keys.

The first Grafana dashboard covers target health, per-Brand-Cloud MQTT publish/delivery rates, device state, API rate/5xx/latency, logger queue/drop/write failures, async backlog/dead letters, TURN nodes, blob capacity, and clip totals. `mqtt_brand_*` counters are dashboard-only; PostgreSQL `mqtt_usage_windows` is billing authority.

```sh
go run ./scripts/go/rtk-cloud -- provision-k8s \
  --env-root cloud_env/staging \
  --confirm video-cloud-staging

go run ./scripts/go/rtk-cloud -- deployment provision \
  --environment staging \
  --confirm video-cloud-staging
```

Common options: `--workspace PATH`, required `--env-root PATH`, `--confirm STACK` matching `CLOUD_STACK_NAME`, and `--timeout DURATION` (default five minutes; also `CLOUD_STAGING_E2E_K8S_ROLLOUT_TIMEOUT`).

### `go run ./scripts/go/rtk-cloud -- provision --dns`

The LKE public edge is an external HAProxy VM, not a NodeBalancer. DNS provisioning:

1. Installs/updates `ingress-nginx` in `<stack>-ingress`.
2. Uses `NodePort` for ingress-nginx, with HTTPS default `30443`.
3. Uses shared certbot DNS-01 plus the selected GoDaddy/Route53 adapter to issue a staging multi-SAN certificate into `video-cloud-staging-public-tls`.
4. Creates ExternalName bridge services from ingress to internal cross-namespace `ClusterIP` services.
5. Creates HTTPS routes for Video Cloud, device, certissuer, turnregistry, Account Manager, Admin, and Frontend hosts.
6. Installs/updates host HAProxy in TCP mode with round-robin forwarding for public 443 and 8883.
7. Points DNS A records at the HAProxy VM, including turnregistry.
8. Installs/updates one host coturn VM plus `video-cloud-turnregistrar.service`.
9. Points `turn.<VIDEO_CLOUD_DOMAIN>` (or `LKE_COTURN_DOMAIN`) at coturn.
10. Exposes MQTTS through NodePort `31883` to a three-pod EMQX StatefulSet with stable pod DNS and required anti-affinity.
11. Applies default-deny ingress NetworkPolicy and required allow rules.

Required inputs: DNS credentials in the environment operator store, `CLOUD_DNS_ROOT_DOMAIN`, `certbot`, `helm`, `kubectl`, `LKE_PUBLIC_EDGE_MODE=external-haproxy`, HAProxy VM inputs, and coturn VM inputs. `LKE_EDGE_HAPROXY_MAXCONN` defaults to `400000`; 100K MQTTS through TCP proxy approaches 200K HAProxy-side sockets, so preserve headroom for token and signaling traffic. Coturn defaults: name `turn01`, label `<stack>-turn01`, type `g6-nanode-1`, domain `turn.<VIDEO_CLOUD_DOMAIN>`, relay range `49152`–`49200`, and count `1`.

```sh
kubectl -n video-cloud-staging-ingress get svc ingress-nginx-controller
kubectl -n video-cloud-staging-video-cloud get svc mqtt-public
kubectl get ingress -A
dig +short A video-cloud-staging.realtekconnect.com @ns23.domaincontrol.com
dig +short A turnregistry.video-cloud-staging.realtekconnect.com @ns23.domaincontrol.com
nc -vz video-cloud-staging.realtekconnect.com 443
nc -vz video-cloud-staging.realtekconnect.com 8883
curl -fsS https://video-cloud-staging.realtekconnect.com/healthz
curl -fsS https://account-manager.video-cloud-staging.realtekconnect.com/v1/health
curl -fsS https://admin.video-cloud-staging.realtekconnect.com/healthz
```

HAProxy is a host package/systemd L4 passthrough. TLS, mTLS, SNI, and HTTP routing remain in ingress-nginx, EMQX, and service pods. See `docs/lke-external-haproxy-edge.md`. PostgreSQL, OpenBao, Prometheus, and Grafana remain private. Operator Grafana debugging:

```sh
kubectl -n video-cloud-staging-observability port-forward svc/video-cloud-grafana 3000:3000
curl -fsS http://127.0.0.1:3000/api/health
```

Kubeconfig lookup order: `CLOUD_STAGING_K8S_KUBECONFIG`, `KUBECONFIG`, LKE download using `LINODE_TOKEN` plus cluster ID, then cluster label (default `<CLOUD_STACK_NAME>-lke`).

### `go run ./scripts/go/rtk-cloud -- remove-k8s`

Low-level compatibility helper. Prefer `scripts/reset-staging-k8s.sh` / `rtk-cloud staging-reset-k8s`. Removal requires `CLOUD_STAGING_E2E_K8S_DESTRUCTIVE_RESET=1` and `--yes`. It removes runtime resources while retaining namespaces and PVC/PV/provider storage; `--purge-storage` deletes PVCs before namespaces.

```sh
go run ./scripts/go/rtk-cloud -- remove-k8s --env-root cloud_env/staging --yes
```

### `scripts/destroy-linode-staging-resources.sh`

Dangerous teardown helper. It defaults to dry-run and lists matching LKE clusters, instances, firewalls, VPCs, and Object Storage buckets. Deletion requires `--yes` and the exact confirmation string.

```sh
scripts/destroy-linode-staging-resources.sh --env-root cloud_env/staging/runtime
scripts/destroy-linode-staging-resources.sh --env-root cloud_env/staging/runtime --yes --confirm-text "destroy video-cloud-staging"
```

Object Storage buckets are skipped unless `--include-object-storage` is supplied, and Linode refuses non-empty buckets. Retained unattached `pvc-*` volumes are also skipped. Delete them only by copying exact IDs from dry-run and providing both `--include-orphan-volumes` and `--orphan-volume-ids`; never delete by name pattern alone.

### Staging Kubernetes Lifecycle Phases

The staging lifecycle has three independently runnable phases. Shell files are POSIX wrappers; Go commands contain the logic.

```sh
scripts/reset-staging-k8s.sh --plan
scripts/reset-staging-k8s.sh --confirm video-cloud-staging
scripts/reset-staging-k8s.sh --confirm video-cloud-staging --purge-storage

scripts/provision-staging.sh --plan
scripts/provision-staging.sh --confirm video-cloud-staging

scripts/run-staging-acceptance.sh --plan
scripts/run-staging-acceptance.sh --confirm video-cloud-staging
```

Reset is destructive and requires confirmation. It preserves storage unless `--purge-storage` is explicit. Provision resolves images, applies manifests/DNS/artifacts, and waits for rollout. Acceptance never resets or deploys; it validates the deployed stack.

`run-staging-e2e` supports `--steps`: `reset`, `provision`, `data`, `mqtt`, `runtime-logs`, `billing-log`, and `billing-db`; `billing` expands to both billing steps. Billing verification reuses the load-test Brand Cloud/device credentials.

PostgreSQL PVC expansion is not a normal rollout. `LKE_POSTGRES_STORAGE` affects new claims only. Follow [`docs/postgres-capacity-expansion-runbook.md`](../docs/postgres-capacity-expansion-runbook.md) for existing PVC expansion, CSI prerequisites, filesystem resize, backup, fallback, and evidence. `emptydir` is only for explicit ephemeral validation.

### `go run ./scripts/go/rtk-cloud -- staging-e2e-test`

Kubernetes staging E2E compatibility orchestrator. It joins reset, rollout readiness, service query/port-forward, E2E data setup, Home MQTT simulation, and persisted runtime-log verification, producing sanitized `summary.json` and `test_report.md`. Data creation lives in `scripts/setup-staging-e2e-data.sh` / `staging-e2e-data-setup`.

The operator entry point is `scripts/run-staging-e2e.sh`. Account Manager verification and password-reset mail use only the Realtek Connect Send Mail HTTP API. Before a full reset, provide these values in the operator process environment; never write the secret to Git, PRs, or logs:

```sh
export AUTH_TOKEN_BASE_URL=https://admin.video-cloud-staging.realtekconnect.com
export SENDMAIL_HTTP_BASE_URL=https://sm.realtekconnect.com
export SENDMAIL_HTTP_TIMEOUT=15s
export SENDMAIL_HTTP_BEARER_TOKEN='<load from operator secret store>'

scripts/run-staging-e2e.sh --plan
scripts/run-staging-e2e.sh --confirm video-cloud-staging
```

Missing/invalid Send Mail settings fail before workload deletion. A targeted `account-manager-email-deploy` repair updates only Account Manager runtime secret, API, migration job, and email worker. Live validation must verify external delivery; API 202 alone is insufficient.

Capacity planning uses `cloud_env/staging/runtime/env/stack.env`. It computes required MQTT replicas from target connections, usable node capacity after system reserve, and required nodes from CPU, memory, and MQTT floors. The 1K profile pins 1,000 target connections, 20,000 connections per MQTT pod, two MQTT replicas, and two nodes. Review 100K changes with plan/preflight.

The observed 100K MQTT/shadow + WebRTC baseline recommends a dedicated `g6-standard-8` PostgreSQL node, `LKE_POSTGRES_REQUEST_CPU=4`, `LKE_POSTGRES_REQUEST_MEMORY=4Gi`, and `LKE_POSTGRES_LIMIT_MEMORY=8Gi` because `g6-standard-4` reached about 102% CPU p95/max and token bootstrap timed out. A constrained `g6-standard-6` rerun is acceptable only when the report records the account-type limitation. `video-100k-turn-v1` allows one bounded device-token retry; remaining timeouts belong to the API/token-bootstrap/PostgreSQL path, not TURN.

Record capacity experiments with `scripts/run-lke-capacity-experiment.sh`, including MQTT, Cloud Logger, concurrency, applied stack configuration, reports, and `capacity-run-summary.json`. Do not tune only the live cluster. The 100K WebRTC baseline uses at least a 16Gi Cloud Logger limit after an 8Gi OOMKilled observation. A skipped runtime correlation is not a complete E2E trace. Large shards need timeout grace for disconnect, cleanup, and `results.json` writes.

The 100K 7/7 distribution in `loadtests/home-100k/scenarios/brand-plan-100k.json` contains 10 Brand Clouds, 5,000 member users, 10 developer owners, and 100,000 devices. Developer users are setup/validation actors only. Loader VM count is calculated as `ceil(devices / HOME100K_LOAD_GENERATOR_DEVICES_PER_VM)`.

Default `run-staging-e2e.sh --confirm` resets runtime and rebuilds SQLite test data. Reuse requires explicit `--skip-remove` or `--resume`. Live staging acceptance defaults to 10 users and 100 devices with the standard mix.

```sh
go run ./scripts/go/rtk-cloud -- staging-e2e-test --env-root cloud_env/staging --plan

go run ./scripts/go/rtk-cloud -- staging-e2e-test \
  --env-root cloud_env/staging \
  --run \
  --confirm video-cloud-stg-0529 \
  --brandname RTK \
  --user-count 10 \
  --device-count 100 \
  --device-mix camera=40,light=25,air_conditioner=20,smart_meter=15
```

Outputs include `summary.json`, `test_report.md`, and per-step `logs/*.log`. Summary/report are scanned for common secrets; raw logs are operator artifacts and are not automatically sanitized or committable.

Production-like app actors generate an app key and `app-user:<user_id>` CSR, obtain an app certificate, pin its identity, and exchange it through mTLS for a subject-bound app token. Device actors similarly exchange device certificates for device tokens. MQTT PASS requires separate actor connections and publish/subscribe/receive evidence. Self-publish/self-receive or local artifacts alone do not pass. Telemetry requires the app observer to receive the matching `message_id`; command flow requires the device to receive matching desired state and the app to receive the reported-state acknowledgment.

`mqtt-trace-report` creates a sanitized `E2E_TRACE_CHAIN_REPORT.md`. `mqtt-test --trace-detail` accepts `summary`, `full`, or `none`; traces omit payload bodies, `clientToken`, and credential material.

### `scripts/setup-staging-e2e-data.sh`

Create/update E2E test data without removing provider resources, provisioning servers, or running MQTT. It supports single-brand arguments and multi-brand `--brand-plan` input.

```sh
scripts/setup-staging-e2e-data.sh \
  --brandname RTK \
  --user-count 10 \
  --device-count 100 \
  --out-dir cloud_env/staging/runtime/artifacts/staging-e2e/manual-data-setup

scripts/setup-staging-e2e-data.sh \
  --env-root cloud_env/staging/runtime \
  --brand-plan loadtests/home-100k/scenarios/brand-plan-100k.json \
  --plan
```

Important options: `--plan`, `--workspace`, `--env-root`, `--brandname`, `--brand-plan`, `--user-count`, `--device-count`, `--device-mix`, `--device-prefix`, and `--out-dir`.

Before enrollment, setup creates/reuses a run-scoped device-item profile, creates a production run, and obtains a short-lived JWT. A fixture-provider JWT may be passed through without creating a second run. The JWT exists only in the generator subprocess. Failure stops before enrollment/binding and never falls back to legacy HMAC. `summary.json` records each stage, exit code, duration, and log path. Only Kubernetes providers are supported.

### Home Load Test

The canonical Home IoT Device Shadow scenarios, operator guide, report schema/template, Ansible, scripts, and legacy MQTT reference live under `loadtests/home-100k/` and `loadtests/home-100k/docs/`. Do not add detailed Home load-test instructions here.

`video-relay-test` runs staging WebRTC RTP relay smoke using a camera with `video_streaming`. PASS requires device owner online, viewer session creation, SDP/ICE exchange, ICE connected/completed, twenty seconds of looped 1080p H.264 RTP with SPS/PPS/IDR/non-IDR evidence, and authorized close. It is not the legacy raw RTP relay test. Results are written under `<env-root>/artifacts/video-relay-test/<timestamp>/` with credential material redacted.

### Kubernetes Runtime Configuration and Log Level

Staging is Kubernetes-only. Manage service log levels through Kubernetes manifests, secrets, or configuration, then verify rollout with `provision-k8s`.

Cloud Admin `/admin` uses Account Manager platform-admin login, not legacy Cloud Admin bootstrap credentials. Automation uses `cloud_env/staging/runtime/services/account-manager/account-manager-platform-admin.env`. `ACCOUNT_MANAGER_BOOTSTRAP_PLATFORM_ADMIN_EMAIL` may override the default `platform-admin@<stack-name>.local`; the password comes from `LKE_PLATFORM_ADMIN` or generated runtime secret state. Never put it in tracked files or documentation. See `docs/account-manager-admin-boundary.md#staging-login-credential-boundary`.

### `go run ./scripts/go/rtk-cloud -- check-certificates`

Validate staging public HTTPS certificates, hostname matching, chain validity, expiration, and minimum remaining lifetime against live endpoints and the local certificate cache.

```sh
go run ./scripts/go/rtk-cloud -- check-certificates --env-root cloud_env/staging
go run ./scripts/go/rtk-cloud -- check-certificates --env-root cloud_env/staging --skip-live
go run ./scripts/go/rtk-cloud -- check-certificates --env-root cloud_env/staging --json
```

Targets: Video Cloud, certissuer, Account Manager, and Admin staging hosts. Options include required `--env-root`, `--dns-root-domain` (default `realtekconnect.com`), `--min-valid-days` (default `7`), `--skip-live`, `--skip-cache`, and `--json`. Any missing, expired, mismatched, untrusted, or short-lived certificate returns `status=fail` and nonzero.

### `go run ./scripts/go/rtk-cloud -- create-brandname-cloud`

Create a Brand Cloud through Account Manager staging. The command ensures platform-admin bootstrap is available, calls the API, uses a PostgreSQL fallback upsert only for a known server error, and verifies the result through the API.

```sh
go run ./scripts/go/rtk-cloud -- create-brandname-cloud --env-root cloud_env/staging --brandname RTK
```

Options: `--workspace`, required `--env-root`, and `--skip-bootstrap`. Progress goes to stderr; final JSON goes to stdout.

### `go run ./scripts/go/rtk-cloud -- list-brandname-clouds`

Read Brand Clouds through the Account Manager admin API without mutation.

```sh
go run ./scripts/go/rtk-cloud -- list-brandname-clouds --env-root cloud_env/staging
go run ./scripts/go/rtk-cloud -- list-brandname-clouds --env-root cloud_env/staging --brandname RTK
go run ./scripts/go/rtk-cloud -- list-brandname-clouds --env-root cloud_env/staging --json
```

Options: `--workspace`, required `--env-root`, `--brandname`, `--limit` (default `200`), and `--json`. Use JSON to inspect complete Brand Cloud metadata.

### `go run ./scripts/go/rtk-cloud -- create-users`

Create enabled users under an existing Brand Cloud through the Account Manager platform-admin API, without signup/email verification or direct PostgreSQL access.

```sh
go run ./scripts/go/rtk-cloud -- create-users --env-root cloud_env/staging --brandname RTK --count 10
```

Options: `--workspace`, required `--env-root`, `--brandname`, `--count` (default `10`), `--role` (`owner`, `admin`, or `member`), `--rotate-password`, and `--dry-run`. Existing users fail unless password rotation is explicit.

stdout contains summary JSON without secrets. Passwords and app bootstrap material are stored in the `0600` SQLite test-data database. The command performs a production-like first app login: generate an app key/CSR when required, obtain a certissuer-signed app certificate, and store certificate metadata. If an existing valid certificate has no matching local private key, the command stops rather than creating unusable mTLS data.

### `go run ./scripts/go/rtk-cloud -- bind-devices`

Bind factory-enrolled devices to Brand Cloud users through the Account Manager API and start account-side provisioning. Possession proof uses one-time Claim Tokens. The platform admin creates Claim Tokens with category and canonical `service_options`; assigned members resolve and claim them, then call the provisioning API.

```sh
go run ./scripts/go/rtk-cloud -- create-users --env-root cloud_env/staging --brandname RTK --count 10
go run ./scripts/go/rtk-cloud -- generate-load-devices --env-root cloud_env/staging --count 100
go run ./scripts/go/rtk-cloud -- bind-devices --env-root cloud_env/staging --brandname RTK
go run ./scripts/go/rtk-cloud -- validate-device-bind --env-root cloud_env/staging --brandname RTK
```

Assignment preserves device order and rotates users within device-type segments. Reruns fail on already-claimed/bound devices; the operator must use new fixtures or deliberately clean state. Summary JSON never includes credentials. Bindings and provisioning state live in the `0600` SQLite database. Raw Claim Tokens, passwords, and bearer tokens exist only in temporary process files and are removed at exit.

Options: required `--env-root`, `--brandname`, legacy `--users-file` and `--devices-dir`, `--count`, `--dry-run`, and `--skip-bootstrap`.

### `go run ./scripts/go/rtk-cloud -- validate-device-bind`

Validate SQLite binding/provisioning state for the 100-device onboarding smoke profile. It checks account-device IDs, provisioning-operation IDs, and ACL `service_options`; it does not require live video.

```sh
go run ./scripts/go/rtk-cloud -- validate-device-bind \
  --env-root cloud_env/staging \
  --brandname RTK \
  --expected-count 100 \
  --expected-devices-per-user 10
```

Outputs: stdout summary JSON, `bulk-bind-validation-results.json`, and `bulk-bind-validation-report.md` under `.artifacts/e2e_test/provisioning/bulk_bind_validation/<timestamp>/`, with redacted identifiers only.

## Linode CI Runner Management

Runner commands manage repository-scoped GitHub Actions self-hosted runners on a shared Linode VM.

### `rtk-cloud ci-runners` Runner Specs

Shared configuration defines the Linux VM, runner names, target repositories, Linode type, and labels. Other runner commands consume it.

### `go run ./scripts/go/rtk-cloud -- ci-runners provision`

Create the shared VM and firewall, then register multiple repository-scoped runners.

```sh
go run ./scripts/go/rtk-cloud -- ci-runners provision
```

Configuration normally comes from `.secrets/shared/linode/env/ci-runners.env` and `.secrets/shared/github/env/runner-registration.env`, including `LINODE_TOKEN`, `GITHUB_TOKEN`, allowed SSH CIDRs, and SSH-key paths.

### `go run ./scripts/go/rtk-cloud -- ci-runners power`

```sh
go run ./scripts/go/rtk-cloud -- ci-runners power status
go run ./scripts/go/rtk-cloud -- ci-runners power start
go run ./scripts/go/rtk-cloud -- ci-runners power stop
```

### `go run ./scripts/go/rtk-cloud -- ci-runners wait-online`

Wait for runners to become online. `CI_RUNNER_ONLINE_TIMEOUT_SECONDS` defaults to 900 and `CI_RUNNER_ONLINE_POLL_SECONDS` to 15.

```sh
go run ./scripts/go/rtk-cloud -- ci-runners wait-online
```

### `go run ./scripts/go/rtk-cloud -- ci-runners list`

List runner online/busy state and labels for Account Manager, Cloud Admin, Frontend, Client, and Logger repositories. Requires authenticated `gh`.

```sh
go run ./scripts/go/rtk-cloud -- ci-runners list
```

### `go run ./scripts/go/rtk-cloud -- ci-runners run-session`

Start the VM, wait for runners, optionally rerun selected workflows, watch completion, archive artifacts to Linode Object Storage, and shut down according to policy.

```sh
go run ./scripts/go/rtk-cloud -- ci-runners run-session \
  --account-run-id RUN_ID \
  --admin-run-id RUN_ID \
  --frontend-run-id RUN_ID \
  --client-run-id RUN_ID \
  --logger-run-id RUN_ID
```

Options: `--rerun true|false` (default true), `--shutdown-policy always|on-success|never` (default always), and `--smoke-only true`.

### `go run ./scripts/go/rtk-cloud -- ci-runners archive-artifacts`

Download artifacts from a GitHub Actions run and upload them to Linode Object Storage.

```sh
go run ./scripts/go/rtk-cloud -- ci-runners archive-artifacts \
  --repo hkt999rtk/rtk_video_cloud \
  --run-id RUN_ID
```

Use `--prefix PREFIX` to choose an Object Storage prefix. Requires `gh`, `go`, `LINODE_OBJ_BUCKET`, `LINODE_OBJ_ENDPOINT`, `LINODE_OBJ_ACCESS_KEY_ID`, and `LINODE_OBJ_SECRET_ACCESS_KEY`.
