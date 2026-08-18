# Build Staging From Scratch and Run the 1K Test

This runbook creates a new LKE staging environment from a fresh clone, proves
the service and billing paths, prepares 1,000 Home IoT identities, and runs the
canonical MQTT/Device Shadow 1K workflow.

Do not use this procedure to take control of an existing cluster. Restore that
cluster's complete ignored `cloud_env/staging/runtime/` instead; provisioning an
existing data plane from an empty runtime can pair new secrets with old storage.

## 1. Prepare the Controller

Required tools are Git, Go, Docker, `kubectl`, `helm`, `jq`, `curl`, OpenSSL,
SSH, and Ansible. Clone the workspace at its pinned commits:

```sh
git clone <workspace-repository>
cd rtk_cloud_workspace
git submodule update --init --recursive
```

Create the shared profile and environment media profile. Never commit either file:

```sh
mkdir -p ~/.config/rtk-cloud/environments
chmod 700 ~/.config/rtk-cloud ~/.config/rtk-cloud/environments
```

`~/.config/rtk-cloud/shared.env`:

```env
LINODE_TOKEN=<Linode API token>
GHCR_PULL_USERNAME=<GitHub username>
GHCR_PULL_TOKEN=<GitHub token with read:packages>
GODADDY_KEY=<GoDaddy API key>
GODADDY_SECRET=<GoDaddy API secret>
LINODE_ARTIFACT_OBJ_ACCESS_KEY_ID=<Seattle artifact key>
LINODE_ARTIFACT_OBJ_SECRET_ACCESS_KEY=<Seattle artifact secret>
```

`~/.config/rtk-cloud/environments/staging.env`:

```env
LINODE_MEDIA_OBJ_ACCESS_KEY_ID=<Singapore media key>
LINODE_MEDIA_OBJ_SECRET_ACCESS_KEY=<Singapore media secret>
```

```sh
chmod 600 ~/.config/rtk-cloud/shared.env ~/.config/rtk-cloud/environments/staging.env
```

Create the SSH key used by ephemeral load generators if it does not exist:

```sh
ssh-keygen -t ed25519 -f ~/.ssh/id_ed25519_rtkcloud
chmod 600 ~/.ssh/id_ed25519_rtkcloud
```

Confirm the Linode active-service limit with the account owner. Do not guess
it. Then create the ignored operator state:

```sh
mkdir -p cloud_env/staging/runtime/adapters/lke
cp cloud_env/staging/runtime/adapters/lke/account.env.example \
  cloud_env/staging/runtime/adapters/lke/account.env
chmod 600 cloud_env/staging/runtime/adapters/lke/account.env
```

Edit `account.env` if the confirmed limit is not the example value.

## 2. Plan and Provision

Run the read-only deployment preflight first:

```sh
go run ./scripts/go/rtk-cloud -- deployment preflight \
  --environment staging \
  --operation provision
```

Render and review the sanitized plan before creating paid resources:

```sh
go run ./scripts/go/rtk-cloud -- deployment plan --environment staging
```

Verify the stack name, region, resolved node-class topology, workload replicas,
projected active services, DNS names, and resolved image tags. The current plan,
not a copied node count, is authoritative. Then provision:

```sh
go run ./scripts/go/rtk-cloud -- deployment provision \
  --environment staging \
  --confirm video-cloud-staging

go run ./scripts/go/rtk-cloud -- deployment acceptance \
  --environment staging
```

The active runtime is `cloud_env/staging/runtime`. Keep its kubeconfig, service
credentials, certificates, test databases, and state out of Git.

## 3. Install EMQX Billing Rules

The MQTT billing connector, publish rule, and delivery rule are EMQX runtime
resources. Configure them after initial provisioning and after any broker
replacement that loses EMQX runtime state. The helper is idempotent.

The following creates a temporary EMQX dashboard user, obtains an API token,
installs the rules, and removes the user. It does not print the generated
password or ingest token:

```sh
set -euo pipefail
K=cloud_env/staging/runtime/state/kubeconfig.yaml
NS=video-cloud-staging-video-cloud
ADMIN=staging_billing_setup
PASS=$(openssl rand -hex 24)

cleanup() {
  KUBECONFIG="$K" kubectl exec -n "$NS" mqtt-0 -- \
    /opt/emqx/bin/emqx ctl admins del "$ADMIN" >/dev/null 2>&1 || true
  test -z "${PF_PID:-}" || kill "$PF_PID" >/dev/null 2>&1 || true
}
trap cleanup EXIT

KUBECONFIG="$K" kubectl exec -n "$NS" mqtt-0 -- \
  /opt/emqx/bin/emqx ctl admins del "$ADMIN" >/dev/null 2>&1 || true
KUBECONFIG="$K" kubectl exec -n "$NS" mqtt-0 -- \
  /opt/emqx/bin/emqx ctl admins add "$ADMIN" "$PASS" "billing setup"
KUBECONFIG="$K" kubectl port-forward -n "$NS" pod/mqtt-0 \
  18083:18083 >/tmp/rtk-emqx-billing-port-forward.log 2>&1 &
PF_PID=$!
until curl -fsS http://127.0.0.1:18083/api/v5/status >/dev/null; do sleep 1; done

TOKEN=$(curl -fsS -H 'content-type: application/json' \
  http://127.0.0.1:18083/api/v5/login \
  --data-binary "$(jq -nc --arg u "$ADMIN" --arg p "$PASS" \
    '{username:$u,password:$p}')" | jq -er '.token')
INGEST=$(KUBECONFIG="$K" kubectl get secret -n "$NS" \
  video-cloud-workers-runtime \
  -o jsonpath='{.data.VIDEO_CLOUD_MQTT_USAGE_INGEST_TOKEN}' | base64 -d)

EMQX_API_URL=http://127.0.0.1:18083/api/v5 \
EMQX_API_TOKEN="$TOKEN" \
VIDEO_CLOUD_MQTT_USAGE_INGEST_TOKEN="$INGEST" \
  ./scripts/configure-emqx-billing.sh

curl -fsS -H "Authorization: Bearer $TOKEN" \
  http://127.0.0.1:18083/api/v5/actions | \
  jq -r '(if type == "object" then .data else . end)[] |
    select(.name | startswith("billing_usage")) | [.name,.status] | @tsv'
```

Both `billing_usage_publish` and `billing_usage_delivery` must report
`connected`. Do not run billing acceptance until real MQTT
traffic produces logger and ledger evidence. See
[`mqtt-billing-runbook.md`](../repos/rtk_video_cloud/docs/mqtt-billing-runbook.md)
for the architecture and troubleshooting procedure.

## 4. Create and Validate 1K Test Data

Use a dedicated brand name to avoid colliding with older test identities. The
scenario description supplies 1,000 devices, 50 users at 20 devices per user,
and the exact eleven-type `home-diverse-v1` mix:

```sh
scripts/setup-staging-e2e-data.sh \
  --env-root cloud_env/staging/runtime \
  --brandname RTK1K \
  --description-file loadtests/home-100k/scenarios/mqtt-1k.description.env
```

Run smoke, runtime-log, and billing acceptance against that data:

```sh
CLOUD_STAGING_E2E_FACTORY_ENROLL_PORTS=18443,18444,18445,18446 \
go run ./scripts/go/rtk-cloud -- staging-acceptance \
  --env-root cloud_env/staging/runtime \
  --confirm video-cloud-staging \
  --brandname RTK1K \
  --user-count 50 \
  --device-count 1000 \
  --device-mix 'light=18,switch=7,smart_plug=12,air_conditioner=10,environment_sensor=12,security_sensor=10,smart_meter=8,camera_status=7,door_lock=4,appliance=7,gateway=5' \
  --device-prefix load1k-device \
  --steps mqtt,runtime-logs,billing-log,billing-db
```

Stop here if any acceptance step fails. Do not start paid load-generator VMs
until MQTT, all expected runtime logs, the billing logger, and `usage_facts`
have passed.

## 5. Plan and Run the 1K Workflow

Validate local inputs without creating a VM:

```sh
HOME100K_DESCRIPTION_FILE=loadtests/home-100k/scenarios/mqtt-1k.description.env \
HOME100K_BRANDNAME=RTK1K \
HOME100K_RUN_ID="preflight-mqtt1k-$(date -u +%Y%m%dT%H%M%SZ)" \
  ./loadtests/home-100k/scripts/home-100k.sh plan
```

The plan must show 1,000 devices, 50 users, the eleven-type mix, and one mixed
load-generator VM. Run the live lifecycle:

```sh
HOME100K_DESCRIPTION_FILE=loadtests/home-100k/scenarios/mqtt-1k.description.env \
HOME100K_BRANDNAME=RTK1K \
HOME100K_RUN_ID="mqtt1k-$(date -u +%Y%m%dT%H%M%SZ)" \
  ./loadtests/home-100k/scripts/home-100k.sh workflow-live
```

A successful report has `Status: COMPLETE`, `Result: SUCCESS`, 1,000 successful
device connections and subscriptions, 50 successful app command flows, exact
runtime-log stream correlation, complete server evidence, and no generator
saturation. The workflow shuts down its VM only after success. On failure it
preserves the VM for `workflow-resume-live`; explicitly run `shutdown-vms` when
investigation is complete.

## 6. Final Health Check

```sh
KUBECONFIG=cloud_env/staging/runtime/state/kubeconfig.yaml kubectl get nodes
KUBECONFIG=cloud_env/staging/runtime/state/kubeconfig.yaml \
  kubectl get pods -A --field-selector=status.phase!=Running,status.phase!=Succeeded
```

All nodes in the resolved deployment plan must be `Ready`, all
deployment/stateful workload replicas must be ready, and the second command
must return no resources. Preserve the ignored
runtime in an approved encrypted backup before another controller takes over.
