#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

active_paths=(
	"$ROOT/scripts/run-staging-e2e.sh" \
	"$ROOT/scripts/reset-staging-k8s.sh" \
	"$ROOT/scripts/provision-staging.sh" \
	"$ROOT/scripts/run-staging-acceptance.sh" \
	"$ROOT/README.md" \
	"$ROOT/scripts/README.zh-TW.md" \
	"$ROOT/docs/cloud-env-layout.zh-TW.md" \
	"$ROOT/docs/private-cloud-deployment.md" \
	"$ROOT/docs/testing.md" \
	"$ROOT/scripts/go/rtk-cloud/main.go" \
	"$ROOT/scripts/go/rtk-cloud/provision.go" \
	"$ROOT/scripts/go/rtk-cloud/native_commands.go" \
	"$ROOT/scripts/go/rtk-cloud/k8s_runtime.go" \
	"$ROOT/scripts/go/rtk-cloud/lke.go" \
	"$ROOT/scripts/go/rtk-cloud/k8s_edge_haproxy.go" \
	"$ROOT/scripts/go/rtk-cloud/k8s_coturn_vm.go" \
	"$ROOT/scripts/go/rtk-cloud/provision_provider.go" \
	"$ROOT/scripts/go/rtk-cloud/provider_linode_vm.go"
)

for path in "${active_paths[@]}"; do
	if rg -n -- '--target vm|CLOUD_STAGING_E2E_TARGET|remove-all-vm|remove_vm|provision_all|update-ssh-whitelist|linode_deploy|deploy/linode|provision-public-vm|deploy-public-vm|provision-admin-vm|deploy-admin|linode-deploy deploy|deploy-staging\.sh --local-build' "$path" >/tmp/staging-k8s-static.out; then
		cat /tmp/staging-k8s-static.out >&2
		echo "staging runtime docs/scripts must stay K8s-only except explicit public edge and TURN data-plane VM helpers" >&2
		exit 1
	fi
done

if rg -n -- 'LKE_|EKS_|GKE_|GODADDY_|ROUTE53_|AWS_ACCESS_|lke\.linode\.com/pool-id' "$ROOT/cloud_deploy/architectures" >/tmp/staging-k8s-static.out; then
	cat /tmp/staging-k8s-static.out >&2
	echo "provider-neutral architecture must not contain adapter keys or provider labels" >&2
	exit 1
fi

if rg -n --glob '!**/runtime/**' -- 'GODADDY_(KEY|SECRET)|AWS_(ACCESS_KEY_ID|SECRET_ACCESS_KEY|SESSION_TOKEN)|ROUTE53_HOSTED_ZONE_ID' \
	"$ROOT/cloud_env/dev" "$ROOT/cloud_env/staging" "$ROOT/cloud_env/prod" >/tmp/staging-k8s-static.out; then
	cat /tmp/staging-k8s-static.out >&2
	echo "tracked environments must not contain DNS credentials or provider zone IDs" >&2
	exit 1
fi

if rg -n --glob '!**/runtime/**' -- '\b(MQTT_REPLICAS|VIDEO_CLOUD_API_REPLICAS)\b' \
	"$ROOT/cloud_deploy" "$ROOT/cloud_env/dev" "$ROOT/cloud_env/staging" "$ROOT/cloud_env/prod" \
	>/tmp/staging-k8s-static.out; then
	cat /tmp/staging-k8s-static.out >&2
	echo "tracked architecture must use minimum and generated effective replica keys" >&2
	exit 1
fi

if rg -n -- 'LKE_|EKS_|GKE_|LINODE_' "$ROOT/scripts/go/rtk-cloud/deployment_capacity.go" >/tmp/staging-k8s-static.out; then
	cat /tmp/staging-k8s-static.out >&2
	echo "shared capacity planner must remain provider-neutral" >&2
	exit 1
fi

if rg -n -i -- 'godaddy|route53|api\.godaddy|GODADDY_|hosted.zone' \
	"$ROOT/scripts/go/rtk-cloud/lke.go" \
	"$ROOT/scripts/go/rtk-cloud/k8s_coturn_vm.go" \
	"$ROOT/scripts/go/rtk-cloud/k8s_shared_helpers.go" \
	"$ROOT/scripts/go/rtk-cloud/k8s_runtime.go" >/tmp/staging-k8s-static.out; then
	cat /tmp/staging-k8s-static.out >&2
	echo "deployment adapter and shared Kubernetes runtime must not contain DNS vendor implementation" >&2
	exit 1
fi

if rg -n --glob '!**/*_test.go' --glob '!test_*.go' -- 'repos/rtk_video_cloud/tools/godaddy-dns|cmd/godaddy-dns' \
	"$ROOT/scripts/go/rtk-cloud" >/tmp/staging-k8s-static.out; then
	cat /tmp/staging-k8s-static.out >&2
	echo "active DNS implementation must not invoke the service submodule GoDaddy tool" >&2
	exit 1
fi

for environment in dev staging prod; do
	grep -Eq '^DNS_ADAPTER=(godaddy|route53)$' "$ROOT/cloud_env/$environment/deployment.env"
done

if rg -n -- 'CAPACITY_TARGET_CONNECTIONS|CAPACITY_ACTIVE_DEVICES|lkeCapacityWorkloads|ceilDiv' \
	"$ROOT/scripts/go/rtk-cloud/lke_capacity.go" >/tmp/staging-k8s-static.out; then
	cat /tmp/staging-k8s-static.out >&2
	echo "LKE capacity code must consume effective values instead of recalculating generic capacity" >&2
	exit 1
fi

if rg -n --glob '!**/runtime/**' -- 'LKE_REGION|LKE_(GENERAL|BROKER|DATABASE)_NODE_TYPE|LINODE_ACTIVE_SERVICE_LIMIT' \
	"$ROOT/cloud_env/dev" "$ROOT/cloud_env/staging" "$ROOT/cloud_env/prod" \
	>/tmp/staging-k8s-static.out; then
	cat /tmp/staging-k8s-static.out >&2
	echo "tracked environments must describe provider-neutral location and node sizing" >&2
	exit 1
fi

if rg -n -- 'HOME100K_LINODE_ACTIVE_SERVICE_LIMIT|LINODE_ACTIVE_SERVICE_LIMIT' \
	"$ROOT/loadtests/home-100k" >/tmp/staging-k8s-static.out; then
	cat /tmp/staging-k8s-static.out >&2
	echo "load test must consume normalized provider preflight state" >&2
	exit 1
fi

if rg -n -- '^HOME100K_REGION=' "$ROOT/loadtests/home-100k/scenarios" >/tmp/staging-k8s-static.out; then
	cat /tmp/staging-k8s-static.out >&2
	echo "load-test scenarios must resolve provider region from the selected environment" >&2
	exit 1
fi

if rg -n --glob '!**/*_test.go' -- 'LKE_|lke\.linode\.com/pool-id' \
	"$ROOT/loadtests/home-100k/scripts/home-100k.sh" \
	"$ROOT/loadtests/home-100k/internal/home100k" >/tmp/staging-k8s-static.out; then
	cat /tmp/staging-k8s-static.out >&2
	echo "load-test runtime must consume normalized environment state, not LKE internals" >&2
	exit 1
fi

if git -C "$ROOT" check-ignore -q cloud_env/qa/environment.env; then
	echo "arbitrary environment config must be trackable" >&2
	exit 1
fi
if ! git -C "$ROOT" check-ignore -q cloud_env/qa/runtime/secrets/token.env; then
	echo "arbitrary environment runtime and secrets must stay ignored" >&2
	exit 1
fi

if rg -n -- 'ACCOUNT_MANAGER_LINODE_|ADMIN_LINODE_|CLOUD_LOGGER_LINODE_|provision-public-vm|deploy-public-vm|provision-admin-vm|deploy-admin|linode-deploy deploy|deploy-staging\.sh --local-build' \
	"$ROOT/scripts/go/rtk-cloud/internal/envroot" \
	"$ROOT/scripts/go/rtk-cloud/logs_check.go" \
	"$ROOT/scripts/go/rtk-cloud/main.go" \
	"$ROOT/scripts/go/cloud-mqtt-test/main.go" >/tmp/staging-k8s-static.out; then
	cat /tmp/staging-k8s-static.out >&2
	echo "workspace staging schema/runtime code must not reference retired VM metadata/toolkits" >&2
	exit 1
fi

skill="/Users/kevinhuang/.codex/skills/linode-video-cloud-deploy/SKILL.md"
if [[ -f "$skill" ]]; then
	if rg -n -- 'edge/api/infra/mqtt/coturn|Docker Compose EMQX|VM deploy|VM provisioning|--target vm' "$skill" >/tmp/staging-k8s-skill.out; then
		cat /tmp/staging-k8s-skill.out >&2
		echo "linode-video-cloud-deploy skill must describe K8s staging, not VM topology" >&2
		exit 1
	fi
	grep -F 'K8s-only' "$skill" >/dev/null
	grep -F 'scripts/run-staging-e2e.sh --confirm video-cloud-staging' "$skill" >/dev/null
	grep -F 'submodule VM toolkits are retired' "$skill" >/dev/null
fi
