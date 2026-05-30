#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
WORKSPACE="$(cd "$SCRIPT_DIR/.." && pwd)"
BIND_ARTIFACT=""
OUT_DIR=""
EXPECTED_COUNT=100
EXPECTED_DEVICES_PER_USER=10

usage() {
	cat <<'USAGE'
Usage:
  scripts/cloud-validate-device-bind.sh --bind-artifact FILE [options]

Options:
  --bind-artifact FILE             Required artifact from cloud-bind-devices.sh.
  --out-dir DIR                    Report directory. Default: .artifacts/e2e_test/provisioning/bulk_bind_validation/<timestamp>.
  --expected-count N               Expected device assignments. Default: 100.
  --expected-devices-per-user N    Expected device count per user. Default: 10.
  -h, --help                       Show this help.

Validates the redacted bulk bind/provision artifact and writes JSON plus
Markdown reports. stdout contains only a summary JSON.
USAGE
}

die() {
	printf 'error: %s\n' "$*" >&2
	exit 2
}

while [[ $# -gt 0 ]]; do
	case "$1" in
	--bind-artifact) BIND_ARTIFACT="$2"; shift 2 ;;
	--out-dir) OUT_DIR="$2"; shift 2 ;;
	--expected-count) EXPECTED_COUNT="$2"; shift 2 ;;
	--expected-devices-per-user) EXPECTED_DEVICES_PER_USER="$2"; shift 2 ;;
	-h|--help) usage; exit 0 ;;
	*) die "unknown argument: $1" ;;
	esac
done

[[ -n "$BIND_ARTIFACT" ]] || die "--bind-artifact is required"
[[ -f "$BIND_ARTIFACT" ]] || die "bind artifact not found: $BIND_ARTIFACT"
[[ "$EXPECTED_COUNT" =~ ^[0-9]+$ && "$EXPECTED_COUNT" -gt 0 ]] || die "--expected-count must be a positive integer"
[[ "$EXPECTED_DEVICES_PER_USER" =~ ^[0-9]+$ && "$EXPECTED_DEVICES_PER_USER" -gt 0 ]] || die "--expected-devices-per-user must be a positive integer"

args=(
	--bind-artifact "$BIND_ARTIFACT"
	--expected-count "$EXPECTED_COUNT"
	--expected-devices-per-user "$EXPECTED_DEVICES_PER_USER"
)
if [[ -n "$OUT_DIR" ]]; then
	args+=(--out-dir "$OUT_DIR")
fi

(
	cd "$WORKSPACE/e2e_test"
	tmpbin="$(mktemp /tmp/rtk-bulk-bind-validate.XXXXXX)"
	trap 'rm -f "$tmpbin"' EXIT
	go build -o "$tmpbin" ./provisioning/bulk_bind_validation/cmd/rtk-bulk-bind-validate
	"$tmpbin" "${args[@]}"
)
