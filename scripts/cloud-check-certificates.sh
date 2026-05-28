#!/usr/bin/env bash
if [ -z "${BASH_VERSION:-}" ]; then
	exec /usr/bin/env bash "$0" "$@"
fi
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
WORKSPACE="$(cd "$SCRIPT_DIR/.." && pwd)"
ENV_ROOT=""
DEPRECATED_ENV_ROOT=""
DNS_ROOT_DOMAIN="realtekconnect.com"
MIN_VALID_DAYS="${STAGING_CERT_CHECK_MIN_VALID_DAYS:-7}"
JSON_OUTPUT=0
SKIP_LIVE=0
SKIP_CACHE=0

die() {
	printf 'error: %s\n' "$*" >&2
	exit 1
}

usage() {
	cat <<'USAGE'
Usage:
  scripts/cloud-check-certificates.sh --env-root PATH [options]

Options:
  --workspace PATH        Default: script parent workspace.
  --env-root PATH         Required environment directory, for example cloud_env/staging.
  --secrets-root PATH     Deprecated alias for --env-root.
  --dns-root-domain NAME  Default: realtekconnect.com.
  --min-valid-days N      Required remaining validity. Default: 7.
  --skip-live             Do not connect to live HTTPS endpoints.
  --skip-cache            Do not check local cloud_env certificate cache.
  --json                  Print JSON result.
  -h, --help              Show this help.

Checks staging HTTPS certificates for:
  - video-cloud-staging.<root-domain>
  - certissuer.video-cloud-staging.<root-domain>
  - account-manager.video-cloud-staging.<root-domain>
  - admin.video-cloud-staging.<root-domain>

The script checks both local cloud_env certificate cache and live HTTPS
endpoints unless skipped. It exits nonzero when any checked certificate is
missing, expired, hostname-invalid, chain-invalid on live HTTPS, or below the
minimum remaining validity threshold.
USAGE
}

require_value() {
	local opt="$1"
	local value="${2:-}"
	[[ -n "$value" ]] || die "$opt requires a value"
}

while [[ $# -gt 0 ]]; do
	case "$1" in
	--workspace) require_value "$1" "${2:-}"; WORKSPACE="$2"; shift 2 ;;
	--env-root) require_value "$1" "${2:-}"; ENV_ROOT="$2"; shift 2 ;;
	--secrets-root) require_value "$1" "${2:-}"; DEPRECATED_ENV_ROOT="$2"; ENV_ROOT="$2"; shift 2 ;;
	--dns-root-domain) require_value "$1" "${2:-}"; DNS_ROOT_DOMAIN="$2"; shift 2 ;;
	--min-valid-days) require_value "$1" "${2:-}"; MIN_VALID_DAYS="$2"; shift 2 ;;
	--skip-live) SKIP_LIVE=1; shift ;;
	--skip-cache) SKIP_CACHE=1; shift ;;
	--json) JSON_OUTPUT=1; shift ;;
	-h|--help) usage; exit 0 ;;
	*) die "unknown argument: $1" ;;
	esac
done

[[ -n "$ENV_ROOT" ]] || die "--env-root is required; pass the environment directory explicitly, for example --env-root cloud_env/staging"
[[ "$MIN_VALID_DAYS" =~ ^[0-9]+$ ]] || die "--min-valid-days must be a non-negative integer"
[[ "$SKIP_LIVE" == "0" || "$SKIP_CACHE" == "0" ]] || die "at least one of live or cache checks must be enabled"

need_cmd() {
	command -v "$1" >/dev/null 2>&1 || die "$1 is required"
}

load_env_file_if_exists() {
	local path="$1"
	if [[ -f "$path" ]]; then
		set -a
		# shellcheck source=/dev/null
		. "$path"
		set +a
	fi
}

need_cmd python3
need_cmd jq

WORKSPACE="$(cd "$WORKSPACE" && pwd)"
source "$SCRIPT_DIR/lib/cloud-env.sh"
ENV_ROOT="$(cloud_env_init "$WORKSPACE" "$ENV_ROOT")"
DEPRECATED_ENV_ROOT="$ENV_ROOT"

AM_ENV="$(cloud_env_account_manager_env "$ENV_ROOT")"
ADMIN_ENV="$(cloud_env_admin_env "$ENV_ROOT")"
CERT_ROOT="$(cloud_env_certificates_dir "$ENV_ROOT")"
load_env_file_if_exists "$AM_ENV"
load_env_file_if_exists "$ADMIN_ENV"

VC_GATEWAY_DOMAIN="video-cloud-staging.$DNS_ROOT_DOMAIN"
VC_CERTISSUER_DOMAIN="certissuer.video-cloud-staging.$DNS_ROOT_DOMAIN"
AM_DOMAIN="${ACCOUNT_MANAGER_LINODE_DOMAIN:-account-manager.video-cloud-staging.$DNS_ROOT_DOMAIN}"
ADMIN_DOMAIN="${ADMIN_LINODE_DOMAIN:-admin.video-cloud-staging.$DNS_ROOT_DOMAIN}"

TMPDIR="$(mktemp -d /tmp/rtk-staging-cert-check.XXXXXX)"
trap 'rm -rf "$TMPDIR"' EXIT
TARGETS_TSV="$TMPDIR/targets.tsv"
RESULT_JSON="$TMPDIR/results.json"

: > "$TARGETS_TSV"
printf '%s\t%s\t%s\t%s\n' "video-cloud" "$VC_GATEWAY_DOMAIN" "$CERT_ROOT/$VC_GATEWAY_DOMAIN/fullchain.pem" "gateway" >> "$TARGETS_TSV"
printf '%s\t%s\t%s\t%s\n' "certissuer" "$VC_CERTISSUER_DOMAIN" "$CERT_ROOT/$VC_GATEWAY_DOMAIN/fullchain.pem" "video-cloud-cache" >> "$TARGETS_TSV"
printf '%s\t%s\t%s\t%s\n' "account-manager" "$AM_DOMAIN" "$CERT_ROOT/$AM_DOMAIN/fullchain.pem" "account-manager" >> "$TARGETS_TSV"
printf '%s\t%s\t%s\t%s\n' "cloud-admin" "$ADMIN_DOMAIN" "$CERT_ROOT/$ADMIN_DOMAIN/fullchain.pem" "cloud-admin" >> "$TARGETS_TSV"

set +e
python3 - "$TARGETS_TSV" "$RESULT_JSON" "$MIN_VALID_DAYS" "$SKIP_LIVE" "$SKIP_CACHE" <<'PY'
from __future__ import annotations

import datetime as dt
import json
import socket
import ssl
import sys
from pathlib import Path

targets_path = Path(sys.argv[1])
result_path = Path(sys.argv[2])
min_valid_days = int(sys.argv[3])
skip_live = sys.argv[4] == "1"
skip_cache = sys.argv[5] == "1"
min_valid_seconds = min_valid_days * 86400
now = dt.datetime.now(dt.timezone.utc)


def parse_asn1_time(value: str) -> dt.datetime:
    parsed = dt.datetime.strptime(value, "%b %d %H:%M:%S %Y %Z")
    return parsed.replace(tzinfo=dt.timezone.utc)


def subject_text(cert: dict) -> str:
    parts = []
    for rdn in cert.get("subject", ()):
        for key, value in rdn:
            if key == "commonName":
                parts.append(f"CN={value}")
    return ",".join(parts)


def issuer_text(cert: dict) -> str:
    parts = []
    for rdn in cert.get("issuer", ()):
        for key, value in rdn:
            if key in {"commonName", "organizationName"}:
                parts.append(f"{key}={value}")
    return ",".join(parts)


def dnsname_match(pattern: str, hostname: str) -> bool:
    pattern = pattern.rstrip(".").lower()
    hostname = hostname.rstrip(".").lower()
    if not pattern.startswith("*."):
        return pattern == hostname
    suffix = pattern[1:]
    if not hostname.endswith(suffix):
        return False
    left = hostname[: -len(suffix)]
    return bool(left) and "." not in left


def validate_hostname(cert: dict, hostname: str) -> None:
    names = [
        value
        for key, value in cert.get("subjectAltName", ())
        if key.lower() == "dns"
    ]
    if not names:
        names = [
            value
            for rdn in cert.get("subject", ())
            for key, value in rdn
            if key == "commonName"
        ]
    if not names:
        raise ValueError("certificate has no DNS subjectAltName or commonName")
    if not any(dnsname_match(name, hostname) for name in names):
        raise ValueError(f"{hostname!r} does not match {', '.join(names)!r}")


def cert_result(target: str, domain: str, source: str, cert: dict, detail: str = "") -> dict:
    not_after_raw = cert.get("notAfter", "")
    expires_at = parse_asn1_time(not_after_raw)
    seconds_left = int((expires_at - now).total_seconds())
    days_left = seconds_left // 86400
    status = "pass"
    failures = []
    try:
        validate_hostname(cert, domain)
    except Exception as exc:  # noqa: BLE001 - report exact ssl mismatch reason.
        failures.append(f"hostname: {exc}")
    if seconds_left < 0:
        failures.append("expired")
    elif seconds_left < min_valid_seconds:
        failures.append(f"expires within {min_valid_days} days")
    if failures:
        status = "fail"
        detail = "; ".join(failures)
    return {
        "target": target,
        "domain": domain,
        "source": source,
        "status": status,
        "days_left": days_left,
        "expires_at": expires_at.isoformat().replace("+00:00", "Z"),
        "subject": subject_text(cert),
        "issuer": issuer_text(cert),
        "detail": detail,
    }


def error_result(target: str, domain: str, source: str, detail: str) -> dict:
    return {
        "target": target,
        "domain": domain,
        "source": source,
        "status": "fail",
        "days_left": None,
        "expires_at": "",
        "subject": "",
        "issuer": "",
        "detail": detail,
    }


def check_cache(target: str, domain: str, cert_path: str) -> dict:
    path = Path(cert_path)
    if not path.exists() or path.stat().st_size == 0:
        return error_result(target, domain, "local-cache", f"missing {path}")
    try:
        cert = ssl._ssl._test_decode_cert(str(path))  # type: ignore[attr-defined]
        return cert_result(target, domain, "local-cache", cert)
    except Exception as exc:  # noqa: BLE001 - keep operator-facing reason.
        return error_result(target, domain, "local-cache", str(exc))


def check_live(target: str, domain: str) -> dict:
    try:
        context = ssl.create_default_context()
        context.check_hostname = True
        with socket.create_connection((domain, 443), timeout=10) as raw:
            with context.wrap_socket(raw, server_hostname=domain) as tls:
                cert = tls.getpeercert()
        return cert_result(target, domain, "live-443", cert)
    except Exception as exc:  # noqa: BLE001 - keep operator-facing reason.
        return error_result(target, domain, "live-443", str(exc))


results = []
for line in targets_path.read_text(encoding="utf-8").splitlines():
    target, domain, cache_path, _cache_label = line.split("\t")
    if not skip_cache:
        results.append(check_cache(target, domain, cache_path))
    if not skip_live:
        results.append(check_live(target, domain))

overall = "pass" if all(item["status"] == "pass" for item in results) else "fail"
payload = {
    "generated_at": now.isoformat().replace("+00:00", "Z"),
    "min_valid_days": min_valid_days,
    "status": overall,
    "results": results,
}
result_path.write_text(json.dumps(payload, indent=2) + "\n", encoding="utf-8")
sys.exit(0 if overall == "pass" else 3)
PY
status=$?
set -e

if [[ "$JSON_OUTPUT" == "1" ]]; then
	jq . "$RESULT_JSON"
else
	printf 'cloud_certificates status=%s min_valid_days=%s env_root=%s\n' \
		"$(jq -r '.status' "$RESULT_JSON")" "$MIN_VALID_DAYS" "$ENV_ROOT"
	printf '%-16s  %-55s  %-12s  %-6s  %-9s  %-20s  %-32s  %s\n' \
		'target' 'domain' 'source' 'status' 'days_left' 'expires_at' 'issuer' 'detail'
	jq -r '.results[] | [.target,.domain,.source,.status,(.days_left // "n/a"),.expires_at,.issuer,.detail] | @tsv' "$RESULT_JSON" |
		awk -F '\t' '{
			printf "%-16s  %-55s  %-12s  %-6s  %-9s  %-20s  %-32s  %s\n", $1, $2, $3, $4, $5, $6, substr($7, 1, 32), $8
		}'
fi

exit "$status"
