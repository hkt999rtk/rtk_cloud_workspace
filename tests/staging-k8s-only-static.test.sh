#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

active_paths=(
	"$ROOT/scripts/run-staging-e2e.sh" \
	"$ROOT/scripts/reset-staging-k8s.sh" \
	"$ROOT/scripts/provision-staging.sh" \
	"$ROOT/scripts/run-staging-acceptance.sh" \
	"$ROOT/bin/stg.sh" \
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

if rg -n -- 'LKE_|EKS_|GKE_|lke\.linode\.com/pool-id' "$ROOT/cloud_deploy/architectures" >/tmp/staging-k8s-static.out; then
	cat /tmp/staging-k8s-static.out >&2
	echo "provider-neutral architecture must not contain adapter keys or provider labels" >&2
	exit 1
fi

if rg -n --glob '!**/*_test.go' -- 'LKE_|lke\.linode\.com/pool-id' \
	"$ROOT/loadtests/home-100k/scripts/home-100k.sh" \
	"$ROOT/loadtests/home-100k/internal/home100k" >/tmp/staging-k8s-static.out; then
	cat /tmp/staging-k8s-static.out >&2
	echo "load-test runtime must consume normalized environment state, not LKE internals" >&2
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
