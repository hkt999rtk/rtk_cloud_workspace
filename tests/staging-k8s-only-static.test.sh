#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

for path in "$ROOT/scripts/run-staging-e2e.sh" "$ROOT/bin/stg.sh" "$ROOT/README.md" "$ROOT/scripts/README.zh-TW.md"; do
	if rg -n -- '--target vm|CLOUD_STAGING_E2E_TARGET|remove-all-vm|remove_vm|provision_all|update-ssh-whitelist' "$path" >/tmp/staging-k8s-static.out; then
		cat /tmp/staging-k8s-static.out >&2
		echo "staging runtime docs/scripts must stay K8s-only" >&2
		exit 1
	fi
done

if rg -n -- 'ACCOUNT_MANAGER_LINODE_|ADMIN_LINODE_|CLOUD_LOGGER_LINODE_|provision-public-vm|deploy-public-vm|provision-admin-vm|deploy-admin|linode-deploy deploy|deploy-staging\.sh --local-build' \
	"$ROOT/scripts/go/rtk-cloud/internal/envroot" \
	"$ROOT/scripts/go/rtk-cloud/logs_check.go" \
	"$ROOT/scripts/go/rtk-cloud/main.go" \
	"$ROOT/scripts/go/cloud-mqtt-test/main.go" >/tmp/staging-k8s-static.out; then
	cat /tmp/staging-k8s-static.out >&2
	echo "workspace staging schema/runtime code must not reference retired VM metadata/toolkits" >&2
	exit 1
fi

for repo in "$ROOT/repos/rtk_video_cloud" "$ROOT/repos/rtk_account_manager" "$ROOT/repos/rtk_cloud_admin"; do
	if [[ -d "$repo" ]] && rg -n \
		-g '!**/docs/TEST_REPORT.md' \
		-g '!**/docs/linode-staging-k8s.md' \
		-- 'linode_deploy|deploy/linode|provision-public-vm|deploy-public-vm|provision-admin-vm|deploy-admin|linode-deploy deploy|deploy-staging\.sh --local-build|ACCOUNT_MANAGER_LINODE_|ADMIN_LINODE_|CLOUD_LOGGER_LINODE_|edge/api/infra/mqtt/coturn' \
		"$repo" >/tmp/staging-k8s-submodules.out; then
		cat /tmp/staging-k8s-submodules.out >&2
		echo "submodule staging VM toolkits must stay retired" >&2
		exit 1
	fi
done

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
