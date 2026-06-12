#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
STG_SH="${RTK_CLOUD_STG_SH:-$ROOT/stg.sh}"
STACK_FILE="$ROOT/cloud_env/staging/linode/env/stack.env"
PROVIDER="${CLOUD_PROVIDER:-}"
if [[ -z "$PROVIDER" && -f "$STACK_FILE" ]]; then
	PROVIDER="$(awk -F= '$1 == "CLOUD_PROVIDER" {print $2; exit}' "$STACK_FILE")"
fi
PROVIDER="${PROVIDER:-linode}"
if [[ "$PROVIDER" != "linode" ]]; then
	printf 'error: unsupported CLOUD_PROVIDER=%s; staging E2E currently supports only linode\n' "$PROVIDER" >&2
	exit 2
fi
STACK_NAME="${RTK_CLOUD_STAGING_STACK_NAME:-}"
if [[ -z "$STACK_NAME" && -f "$STACK_FILE" ]]; then
	STACK_NAME="$(awk -F= '$1 == "CLOUD_STACK_NAME" {print $2; exit}' "$STACK_FILE")"
fi
STACK_NAME="${STACK_NAME:-video-cloud-staging}"
BRANDNAME="RTK"
USER_COUNT="10"
DEVICE_COUNT="100"
CONFIRM=""
PLAN=0
OUT_DIR=""

usage() {
	cat <<'USAGE'
Usage:
  scripts/run-staging-e2e.sh --confirm <stack-name> [args]
  scripts/run-staging-e2e.sh --plan [args]

Runs the full staging E2E flow through ./stg.sh e2e.
Current supported provider: linode.

Flow:
  1. remove staging provider resources
  2. provision/deploy staging with current CI artifacts
  3. run scripts/setup-staging-e2e-data.sh for brand/users/devices/bind
  4. run live MQTT E2E
  5. write final installation report with segment durations
  6. print final redacted report paths

Options:
  --confirm <stack-name>         Required for destructive run mode.
  --plan                         Print the underlying E2E plan only.
  --out-dir PATH                  Override report output directory.
  -h, --help                      Show this help.

Artifact selection:
  By default the underlying staging deploy logic selects the latest readable CI
  artifacts. Set VIDEO_RELEASE, ACCOUNT_RELEASE, or ADMIN_RELEASE to override.
USAGE
}

write_install_report() {
	local summary_file="$1"
	local e2e_report_file="$2"
	local report_dir="$3"
	local install_report_file="$report_dir/INSTALL_REPORT.md"

	if [[ -z "$summary_file" || ! -f "$summary_file" ]]; then
		return 0
	fi
	if ! command -v jq >/dev/null 2>&1; then
		printf 'warning: jq not found; installation report was not generated\n' >&2
		return 0
	fi

	mkdir -p "$report_dir"

	local overall generated_at env_root stack brandname total_seconds
	overall="$(jq -r '.overall // "unknown"' "$summary_file")"
	generated_at="$(jq -r '.generated_at // empty' "$summary_file")"
	env_root="$(jq -r '.env_root // empty' "$summary_file")"
	stack="$(jq -r '.stack // empty' "$summary_file")"
	brandname="$(jq -r '.brandname // empty' "$summary_file")"
	total_seconds="$(jq -r '[.steps[]?.duration_seconds // 0] | add // 0' "$summary_file")"

	if [[ -z "$e2e_report_file" ]]; then
		e2e_report_file="$(jq -r '.artifacts.report_file // empty' "$summary_file")"
	fi

	{
		printf '# Staging Installation Report\n\n'
		printf '%s\n' "- Overall: $overall"
		printf '%s\n' "- Provider: $PROVIDER"
		if [[ -n "$stack" ]]; then
			printf '%s\n' "- Stack: $stack"
		fi
		if [[ -n "$brandname" ]]; then
			printf '%s\n' "- Brand: $brandname"
		fi
		if [[ -n "$generated_at" ]]; then
			printf '%s\n' "- Generated: $generated_at"
		fi
		if [[ -n "$env_root" ]]; then
			printf '%s\n' "- Env root: \`$env_root\`"
		fi
		printf '%s\n\n' "- Total duration seconds: $total_seconds"

		printf '## Segment Durations\n\n'
		printf '| Segment | Status | Duration seconds | Log |\n'
		printf '| --- | --- | ---: | --- |\n'
		while IFS=$'\t' read -r name status duration log_file; do
			printf '| %s | %s | %s |' "$name" "$status" "$duration"
			if [[ -n "$log_file" ]]; then
				printf ' `%s`' "$log_file"
			fi
			printf ' |\n'
		done < <(jq -r '.steps[]? | [(.name // ""), (.status // ""), ((.duration_seconds // 0) | tostring), (.log_file // "")] | @tsv' "$summary_file")

		printf '\n## Artifacts\n\n'
		printf '%s\n' "- Summary: \`$summary_file\`"
		if [[ -n "$e2e_report_file" ]]; then
			printf '%s\n' "- E2E report: \`$e2e_report_file\`"
		fi
		printf '%s\n' "- Logs: \`$report_dir/logs\`"
		printf '%s\n' "- Bind validation: \`$report_dir/bind-validation\`"
		printf '%s\n' "- MQTT report: \`$report_dir/home-mqtt/TEST_REPORT.md\`"
	} >"$install_report_file"

	printf '%s\n' "$install_report_file"
}

while [[ $# -gt 0 ]]; do
	case "$1" in
		--confirm)
			CONFIRM="${2:-}"
			if [[ -z "$CONFIRM" ]]; then
				printf 'error: --confirm requires a value\n' >&2
				exit 2
			fi
			shift 2
			;;
		--plan)
			PLAN=1
			shift
			;;
		--out-dir)
			OUT_DIR="${2:-}"
			if [[ -z "$OUT_DIR" ]]; then
				printf 'error: --out-dir requires a value\n' >&2
				exit 2
			fi
			shift 2
			;;
		-h|--help)
			usage
			exit 0
			;;
		*)
			printf 'error: unknown argument: %s\n' "$1" >&2
			usage >&2
			exit 2
			;;
	esac
done

if [[ "$PLAN" -eq 1 ]]; then
	exec "$STG_SH" e2e --plan --brandname "$BRANDNAME" --user-count "$USER_COUNT" --device-count "$DEVICE_COUNT"
fi

if [[ "$CONFIRM" != "$STACK_NAME" ]]; then
	if [[ -z "$CONFIRM" ]]; then
		printf 'error: --confirm %s is required before deleting and redeploying staging\n' "$STACK_NAME" >&2
	else
		printf 'error: --confirm must be %s, got %s\n' "$STACK_NAME" "$CONFIRM" >&2
	fi
	exit 2
fi

run_args=(
	e2e
	--run
	--confirm "$STACK_NAME"
	--brandname "$BRANDNAME"
	--user-count "$USER_COUNT"
	--device-count "$DEVICE_COUNT"
)
if [[ -n "$OUT_DIR" ]]; then
	run_args+=(--out-dir "$OUT_DIR")
fi

output="$("$STG_SH" "${run_args[@]}")"
printf '%s\n' "$output"

summary_file=""
report_file=""
if command -v jq >/dev/null 2>&1; then
	summary_file="$(printf '%s\n' "$output" | jq -r 'select(type == "object") | .summary_file // empty' 2>/dev/null | tail -n 1 || true)"
	report_file="$(printf '%s\n' "$output" | jq -r 'select(type == "object") | .report_file // empty' 2>/dev/null | tail -n 1 || true)"
fi

if [[ -n "$summary_file" || -n "$report_file" ]]; then
	report_dir=""
	if [[ -n "$OUT_DIR" ]]; then
		report_dir="$OUT_DIR"
	elif [[ -n "$report_file" ]]; then
		report_dir="$(dirname "$report_file")"
	elif [[ -n "$summary_file" ]]; then
		report_dir="$(dirname "$summary_file")"
	fi
	install_report_file=""
	if [[ -n "$report_dir" ]]; then
		install_report_file="$(write_install_report "$summary_file" "$report_file" "$report_dir")"
	fi
	printf '\nFinal report paths:\n'
	if [[ -n "$summary_file" ]]; then
		printf 'summary_file=%s\n' "$summary_file"
	fi
	if [[ -n "$report_file" ]]; then
		printf 'report_file=%s\n' "$report_file"
	fi
	if [[ -n "$install_report_file" ]]; then
		printf 'install_report_file=%s\n' "$install_report_file"
	fi
	if [[ -n "$report_dir" ]]; then
		printf 'logs_dir=%s\n' "$report_dir/logs"
		printf 'bind_validation_dir=%s\n' "$report_dir/bind-validation"
		printf 'mqtt_report_file=%s\n' "$report_dir/home-mqtt/TEST_REPORT.md"
	fi
fi
