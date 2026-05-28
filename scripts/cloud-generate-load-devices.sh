#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
WORKSPACE="$(cd "$SCRIPT_DIR/.." && pwd)"
START_EPOCH="$(date +%s)"
ENV_ROOT=""
COUNT=100
MIX="camera=40,light=25,air_conditioner=20,smart_meter=15"
PREFIX="load-device"
OUT_DIR=""
FORCE=0
CA_VALID_DAYS=365
DEVICE_VALID_DAYS=180

DEVICE_TYPES=(camera light air_conditioner smart_meter)
WEIGHT_camera=0
WEIGHT_light=0
WEIGHT_air_conditioner=0
WEIGHT_smart_meter=0
ALLOC_camera=0
ALLOC_light=0
ALLOC_air_conditioner=0
ALLOC_smart_meter=0
REM_camera=0
REM_light=0
REM_air_conditioner=0
REM_smart_meter=0

die() {
	printf 'error: %s\n' "$*" >&2
	exit 1
}

log() {
	local now elapsed
	now="$(date +%H:%M:%S)"
	elapsed=$(($(date +%s) - START_EPOCH))
	printf '[cloud-load-devices %s +%03ds] %s\n' "$now" "$elapsed" "$*" >&2
}

usage() {
	cat <<'USAGE'
Usage:
  scripts/cloud-generate-load-devices.sh [options]

Options:
  --count N              Number of devices to generate. Default: 100.
  --mix SPEC             Device type weights. Default: camera=40,light=25,air_conditioner=20,smart_meter=15.
  --prefix PREFIX        Device id prefix. Default: load-device.
  --out-dir PATH         Output directory. Default: <env-root>/devices/test_device.
  --workspace PATH       Default: script parent workspace.
  --env-root PATH        Required environment directory, for example cloud_env/staging.
  --ca-valid-days N      Dev CA certificate validity. Default: 365.
  --device-valid-days N  Device certificate validity. Default: 180.
  --force                Remove existing output directory before generation.
  -h, --help             Show this help.

Supported device types match the currently implemented load-test simulator:
camera, light, air_conditioner, smart_meter.

The generated keys and certificates are simulation-only production-flow
materials. They are written under cloud_env by default and must not be committed.
USAGE
}

require_value() {
	local opt="$1"
	local value="${2:-}"
	[[ -n "$value" ]] || die "$opt requires a value"
}

while [[ $# -gt 0 ]]; do
	case "$1" in
	--count) require_value "$1" "${2:-}"; COUNT="$2"; shift 2 ;;
	--mix) require_value "$1" "${2:-}"; MIX="$2"; shift 2 ;;
	--prefix) require_value "$1" "${2:-}"; PREFIX="$2"; shift 2 ;;
	--out-dir) require_value "$1" "${2:-}"; OUT_DIR="$2"; shift 2 ;;
	--workspace) require_value "$1" "${2:-}"; WORKSPACE="$2"; shift 2 ;;
	--env-root) require_value "$1" "${2:-}"; ENV_ROOT="$2"; shift 2 ;;
	--ca-valid-days) require_value "$1" "${2:-}"; CA_VALID_DAYS="$2"; shift 2 ;;
	--device-valid-days) require_value "$1" "${2:-}"; DEVICE_VALID_DAYS="$2"; shift 2 ;;
	--force) FORCE=1; shift ;;
	-h|--help) usage; exit 0 ;;
	*) die "unknown argument: $1" ;;
	esac
done

need_cmd() {
	command -v "$1" >/dev/null 2>&1 || die "$1 is required"
}

is_supported_type() {
	local candidate="$1"
	local type
	for type in "${DEVICE_TYPES[@]}"; do
		[[ "$candidate" == "$type" ]] && return 0
	done
	return 1
}

require_positive_int() {
	local name="$1"
	local value="$2"
	[[ "$value" =~ ^[0-9]+$ && "$value" -gt 0 ]] || die "$name must be a positive integer"
}

shell_quote() {
	printf "'%s'" "$(printf '%s' "$1" | sed "s/'/'\\\\''/g")"
}

type_model() {
	case "$1" in
	camera) printf 'RTC-CAM-PRO2-SIM' ;;
	light) printf 'RTC-LIGHT-SIM' ;;
	air_conditioner) printf 'RTC-AC-SIM' ;;
	smart_meter) printf 'RTC-METER-SIM' ;;
	esac
}

type_display_name() {
	local type="$1"
	local ordinal="$2"
	case "$type" in
	camera) printf 'PRO2 Camera Simulator %03d' "$ordinal" ;;
	light) printf 'Light Simulator %03d' "$ordinal" ;;
	air_conditioner) printf 'Air Conditioner Simulator %03d' "$ordinal" ;;
	smart_meter) printf 'Smart Meter Simulator %03d' "$ordinal" ;;
	esac
}

type_capability() {
	case "$1" in
	camera) printf 'camera' ;;
	light) printf 'light' ;;
	air_conditioner) printf 'air_conditioner' ;;
	smart_meter) printf 'smart_meter' ;;
	esac
}

type_capabilities_json() {
	case "$1" in
	camera)
		printf '%s\n' '["camera_event","status_report","snapshot","websocket_owner","webrtc","recording_clip","mqtt_legacy_snapshot"]'
		;;
	light)
		printf '%s\n' '["mqtt","power","brightness","color_temperature","state_report","command_result"]'
		;;
	air_conditioner)
		printf '%s\n' '["mqtt","power","target_temperature","mode","fan","state_report","command_result"]'
		;;
	smart_meter)
		printf '%s\n' '["mqtt","status_report","telemetry_report","power_watts","energy_kwh","voltage","current"]'
		;;
	esac
}

set_weight() {
	case "$1" in
	camera) WEIGHT_camera="$2" ;;
	light) WEIGHT_light="$2" ;;
	air_conditioner) WEIGHT_air_conditioner="$2" ;;
	smart_meter) WEIGHT_smart_meter="$2" ;;
	esac
}

get_weight() {
	case "$1" in
	camera) printf '%s' "$WEIGHT_camera" ;;
	light) printf '%s' "$WEIGHT_light" ;;
	air_conditioner) printf '%s' "$WEIGHT_air_conditioner" ;;
	smart_meter) printf '%s' "$WEIGHT_smart_meter" ;;
	esac
}

set_allocated() {
	case "$1" in
	camera) ALLOC_camera="$2" ;;
	light) ALLOC_light="$2" ;;
	air_conditioner) ALLOC_air_conditioner="$2" ;;
	smart_meter) ALLOC_smart_meter="$2" ;;
	esac
}

get_allocated() {
	case "$1" in
	camera) printf '%s' "$ALLOC_camera" ;;
	light) printf '%s' "$ALLOC_light" ;;
	air_conditioner) printf '%s' "$ALLOC_air_conditioner" ;;
	smart_meter) printf '%s' "$ALLOC_smart_meter" ;;
	esac
}

set_remainder() {
	case "$1" in
	camera) REM_camera="$2" ;;
	light) REM_light="$2" ;;
	air_conditioner) REM_air_conditioner="$2" ;;
	smart_meter) REM_smart_meter="$2" ;;
	esac
}

get_remainder() {
	case "$1" in
	camera) printf '%s' "$REM_camera" ;;
	light) printf '%s' "$REM_light" ;;
	air_conditioner) printf '%s' "$REM_air_conditioner" ;;
	smart_meter) printf '%s' "$REM_smart_meter" ;;
	esac
}

parse_mix() {
	local raw="$1"
	local item type weight
	[[ -n "$raw" ]] || die "--mix must not be empty"
	for item in ${raw//,/ }; do
		[[ -n "$item" ]] || continue
		[[ "$item" == *=* ]] || die "invalid --mix item: $item"
		type="${item%%=*}"
		weight="${item#*=}"
		is_supported_type "$type" || die "unsupported device type in --mix: $type"
		[[ "$weight" =~ ^[0-9]+$ ]] || die "invalid weight for $type: $weight"
		set_weight "$type" "$weight"
	done
	local total=0
	for type in "${DEVICE_TYPES[@]}"; do
		total=$((total + $(get_weight "$type")))
	done
	[[ "$total" -gt 0 ]] || die "--mix must include at least one positive weight"
	MIX_TOTAL="$total"
}

allocate_counts() {
	local type weight base remainder allocated_total leftover selected selected_remainder
	allocated_total=0
	for type in "${DEVICE_TYPES[@]}"; do
		weight="$(get_weight "$type")"
		base=$((COUNT * weight / MIX_TOTAL))
		remainder=$((COUNT * weight % MIX_TOTAL))
		set_allocated "$type" "$base"
		set_remainder "$type" "$remainder"
		allocated_total=$((allocated_total + base))
	done
	leftover=$((COUNT - allocated_total))
	while [[ "$leftover" -gt 0 ]]; do
		selected=""
		selected_remainder=-1
		for type in "${DEVICE_TYPES[@]}"; do
			weight="$(get_weight "$type")"
			[[ "$weight" -gt 0 ]] || continue
			remainder="$(get_remainder "$type")"
			if [[ "$remainder" -gt "$selected_remainder" ]]; then
				selected="$type"
				selected_remainder="$remainder"
			fi
		done
		[[ -n "$selected" ]] || die "could not allocate device count from --mix"
		set_allocated "$selected" "$(($(get_allocated "$selected") + 1))"
		set_remainder "$selected" -1
		leftover=$((leftover - 1))
	done
}

write_ca() {
	log "generating simulation device CA"
	mkdir -p "$OUT_DIR/ca"
	openssl ecparam -name prime256v1 -genkey -noout -out "$OUT_DIR/ca/sim-device-ca.key.pem" >>"$OPENSSL_LOG" 2>&1
	chmod 0600 "$OUT_DIR/ca/sim-device-ca.key.pem"
	cat > "$OUT_DIR/ca/ca.conf" <<'EOF_CA'
[ req ]
prompt = no
distinguished_name = dn
x509_extensions = v3_ca

[ dn ]
C = TW
O = Realtek Connect Plus Simulation
OU = Load Test Device Factory
CN = Realtek Connect Plus Simulation Device CA

[ v3_ca ]
basicConstraints = critical, CA:TRUE, pathlen:0
keyUsage = critical, keyCertSign, cRLSign
subjectKeyIdentifier = hash
authorityKeyIdentifier = keyid:always,issuer
EOF_CA
	openssl req -x509 -new -sha256 \
		-key "$OUT_DIR/ca/sim-device-ca.key.pem" \
		-days "$CA_VALID_DAYS" \
		-out "$OUT_DIR/ca/sim-device-ca.cert.pem" \
		-config "$OUT_DIR/ca/ca.conf" >>"$OPENSSL_LOG" 2>&1
}

write_device_material() {
	local index="$1"
	local type="$2"
	local ordinal="$3"
	local device_id model display capability capabilities_json device_dir bundle_dir
	device_id="$(printf '%s-%04d' "$PREFIX" "$index")"
	model="$(type_model "$type")"
	display="$(type_display_name "$type" "$ordinal")"
	capability="$(type_capability "$type")"
	capabilities_json="$(type_capabilities_json "$type")"
	device_dir="$OUT_DIR/devices/$type/$device_id"
	bundle_dir="$OUT_DIR/bundles/$type"
	mkdir -p "$device_dir" "$bundle_dir"

	openssl ecparam -name prime256v1 -genkey -noout -out "$device_dir/device.key.pem" >>"$OPENSSL_LOG" 2>&1
	chmod 0600 "$device_dir/device.key.pem"

	cat > "$device_dir/csr.conf" <<EOF_CSR
[ req ]
prompt = no
distinguished_name = dn
req_extensions = req_ext

[ dn ]
C = TW
O = Realtek Connect Plus Simulation
OU = $type
CN = $device_id

[ req_ext ]
subjectAltName = @alt_names

[ alt_names ]
DNS.1 = $device_id.simulated.realtek-connect.local
URI.1 = urn:realtek-connect:simulated-device:$device_id
EOF_CSR
	openssl req -new -sha256 \
		-key "$device_dir/device.key.pem" \
		-out "$device_dir/device.csr.pem" \
		-config "$device_dir/csr.conf" >>"$OPENSSL_LOG" 2>&1

	cat > "$device_dir/cert-ext.conf" <<EOF_EXT
basicConstraints = CA:FALSE
keyUsage = critical, digitalSignature
extendedKeyUsage = clientAuth
subjectAltName = DNS:$device_id.simulated.realtek-connect.local,URI:urn:realtek-connect:simulated-device:$device_id
EOF_EXT
	openssl x509 -req -sha256 \
		-in "$device_dir/device.csr.pem" \
		-CA "$OUT_DIR/ca/sim-device-ca.cert.pem" \
		-CAkey "$OUT_DIR/ca/sim-device-ca.key.pem" \
		-CAserial "$OUT_DIR/ca/sim-device-ca.srl" \
		-CAcreateserial \
		-days "$DEVICE_VALID_DAYS" \
		-out "$device_dir/device.cert.pem" \
		-extfile "$device_dir/cert-ext.conf" >>"$OPENSSL_LOG" 2>&1

	cat "$device_dir/device.cert.pem" "$device_dir/device.key.pem" > "$bundle_dir/$device_id.pem"
	chmod 0600 "$bundle_dir/$device_id.pem"

	jq -n \
		--arg device_id "$device_id" \
		--arg type "$type" \
		--arg capability "$capability" \
		--arg model "$model" \
		--arg display_name "$display" \
		--arg cert "devices/$type/$device_id/device.cert.pem" \
		--arg key "devices/$type/$device_id/device.key.pem" \
		--arg csr "devices/$type/$device_id/device.csr.pem" \
		--arg bundle "bundles/$type/$device_id.pem" \
		--argjson capabilities "$capabilities_json" \
		'{
			device_id: $device_id,
			device_type: $type,
			mqtt_capability: $capability,
			model: $model,
			display_name: $display_name,
			firmware_version: "0.0.0-loadtest",
			capabilities: $capabilities,
			certificate_profile: "simulation-device-mtls-client",
			certificate_path: $cert,
			key_path: $key,
			csr_path: $csr,
			bundle_path: $bundle,
			production: false,
			warning: "Simulation-only generated credential. Do not use as a production or customer device identity."
		}' > "$device_dir/metadata.json"

	printf '%s,%s,%s,%s,%s,%s,%s\n' \
		"$device_id" "$type" "$capability" "$model" \
		"devices/$type/$device_id/device.cert.pem" \
		"devices/$type/$device_id/device.key.pem" \
		"bundles/$type/$device_id.pem" >> "$CSV_MANIFEST"
	printf '%s\n' "$device_id" >> "$DEVICE_IDS_FILE"
	printf '%s\n' "$device_dir/metadata.json" >> "$METADATA_FILES"
}

write_manifests() {
	local type count profile iot_mix device_ids_csv
	mkdir -p "$OUT_DIR/manifests"
	CSV_MANIFEST="$OUT_DIR/manifests/devices.csv"
	DEVICE_IDS_FILE="$OUT_DIR/manifests/device_ids.txt"
	METADATA_FILES="$OUT_DIR/manifests/metadata_files.txt"
	printf 'device_id,device_type,mqtt_capability,model,certificate_path,key_path,bundle_path\n' > "$CSV_MANIFEST"
	: > "$DEVICE_IDS_FILE"
	: > "$METADATA_FILES"

	local index=1
	for type in "${DEVICE_TYPES[@]}"; do
		count="$(get_allocated "$type")"
		[[ "$count" -gt 0 ]] || continue
		log "generating devices: type=$type count=$count"
		for ordinal in $(seq 1 "$count"); do
			write_device_material "$index" "$type" "$ordinal"
			index=$((index + 1))
		done
	done

	jq -s . $(cat "$METADATA_FILES") > "$OUT_DIR/manifests/devices.json"

	device_ids_csv="$(paste -sd, "$DEVICE_IDS_FILE")"
	profile="mixed"
	if [[ "$ALLOC_camera" -eq "$COUNT" ]]; then
		profile="camera"
	elif [[ "$ALLOC_camera" -eq 0 ]]; then
		profile="iot"
	fi
	iot_mix="light=$ALLOC_light,air_conditioner=$ALLOC_air_conditioner,smart_meter=$ALLOC_smart_meter"

	{
		printf '# Source this file before e2e_test/video_cloud/load/scripts/run_video_loadtest.sh.\n'
		printf '# It contains no bearer tokens; provide VIDEO_CLOUD_LOAD_*_TOKEN separately.\n'
		printf 'export VIDEO_CLOUD_LOAD_DEVICE_PREFIX=%s\n' "$(shell_quote "$PREFIX")"
		printf 'export VIDEO_CLOUD_LOAD_VIRTUAL_DEVICES=%s\n' "$COUNT"
		printf 'export VIDEO_CLOUD_LOAD_DEVICE_IDS=%s\n' "$(shell_quote "$device_ids_csv")"
		printf 'export VIDEO_CLOUD_LOAD_MQTT_DEVICE_PROFILE=%s\n' "$(shell_quote "$profile")"
		printf 'export VIDEO_CLOUD_LOAD_MQTT_IOT_MIX=%s\n' "$(shell_quote "$iot_mix")"
		printf 'export VIDEO_CLOUD_LOAD_DEVICE_MANIFEST=%s\n' "$(shell_quote "$OUT_DIR/manifests/devices.json")"
		printf 'export VIDEO_CLOUD_LOAD_DEVICE_CERT_ROOT=%s\n' "$(shell_quote "$OUT_DIR")"
	} > "$OUT_DIR/loadtest.env"

	jq -n \
		--arg generated_at "$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
		--arg prefix "$PREFIX" \
		--arg mix "$MIX" \
		--arg out_dir "$OUT_DIR" \
		--arg loadtest_env "loadtest.env" \
		--arg device_ids "manifests/device_ids.txt" \
		--arg devices_csv "manifests/devices.csv" \
		--arg devices_json "manifests/devices.json" \
		--arg ca_cert "ca/sim-device-ca.cert.pem" \
		--argjson count "$COUNT" \
		--argjson camera "$ALLOC_camera" \
		--argjson light "$ALLOC_light" \
		--argjson air_conditioner "$ALLOC_air_conditioner" \
		--argjson smart_meter "$ALLOC_smart_meter" \
		'{
			generated_at: $generated_at,
			count: $count,
			prefix: $prefix,
			requested_mix: $mix,
			allocated: {
				camera: $camera,
				light: $light,
				air_conditioner: $air_conditioner,
				smart_meter: $smart_meter
			},
			paths: {
				output_dir: $out_dir,
				loadtest_env: $loadtest_env,
				device_ids: $device_ids,
				devices_csv: $devices_csv,
				devices_json: $devices_json,
				ca_cert: $ca_cert
			}
		}' > "$OUT_DIR/summary.json"
}

write_readme() {
	cat > "$OUT_DIR/README.md" <<EOF_README
# Staging Load-Test Device Factory Output

This directory contains simulation-only device identities generated for staging
load tests and factory/provisioning flow rehearsal.

- Device count: $COUNT
- Requested mix: $MIX
- Device key type: EC P-256
- Device certificate profile: clientAuth
- CA validity days: $CA_VALID_DAYS
- Device validity days: $DEVICE_VALID_DAYS

## Files

- \`ca/sim-device-ca.cert.pem\`: simulation device CA certificate.
- \`ca/sim-device-ca.key.pem\`: simulation device CA private key.
- \`devices/<type>/<device_id>/device.key.pem\`: per-device private key.
- \`devices/<type>/<device_id>/device.csr.pem\`: per-device CSR.
- \`devices/<type>/<device_id>/device.cert.pem\`: per-device client certificate.
- \`devices/<type>/<device_id>/metadata.json\`: per-device metadata.
- \`bundles/<type>/<device_id>.pem\`: certificate + private key bundle.
- \`manifests/devices.csv\`: compact inventory.
- \`manifests/devices.json\`: full inventory.
- \`manifests/device_ids.txt\`: one device id per line.
- \`loadtest.env\`: environment variables for the current load-test runner.

## Usage

\`\`\`sh
set -a
. "$OUT_DIR/loadtest.env"
set +a

# Then provide API URL and bearer tokens separately before running:
# e2e_test/video_cloud/load/scripts/run_video_loadtest.sh
\`\`\`

These credentials are not production credentials. Do not register them against
production or customer environments.
EOF_README
}

require_positive_int "--count" "$COUNT"
require_positive_int "--ca-valid-days" "$CA_VALID_DAYS"
require_positive_int "--device-valid-days" "$DEVICE_VALID_DAYS"
[[ "$PREFIX" =~ ^[A-Za-z0-9][A-Za-z0-9_.-]*$ ]] || die "--prefix contains unsupported characters"
[[ -n "$ENV_ROOT" ]] || die "--env-root is required; pass the environment directory explicitly, for example --env-root cloud_env/staging"

need_cmd openssl
need_cmd jq
need_cmd paste

WORKSPACE="$(cd "$WORKSPACE" && pwd)"
source "$SCRIPT_DIR/lib/cloud-env.sh"
ENV_ROOT="$(cloud_env_init "$WORKSPACE" "$ENV_ROOT")"
if [[ -z "$OUT_DIR" ]]; then
	OUT_DIR="$(cloud_env_test_devices_dir "$ENV_ROOT")"
fi
if [[ -e "$OUT_DIR" ]]; then
	[[ "$FORCE" == "1" ]] || die "$OUT_DIR already exists; use --force to replace it"
	rm -rf "$OUT_DIR"
fi
mkdir -p "$OUT_DIR"
OUT_DIR="$(cd "$OUT_DIR" && pwd)"
OPENSSL_LOG="$OUT_DIR/openssl.log"
: > "$OPENSSL_LOG"

log "start load-test device generation: count=$COUNT mix=$MIX"
log "workspace=$WORKSPACE"
log "output=$OUT_DIR"
parse_mix "$MIX"
allocate_counts
write_ca
write_manifests
write_readme

log "complete: generated $COUNT simulation devices"
printf 'output=%s\n' "$OUT_DIR"
printf 'summary=%s\n' "$OUT_DIR/summary.json"
printf 'loadtest_env=%s\n' "$OUT_DIR/loadtest.env"
printf 'openssl_log=%s\n' "$OPENSSL_LOG"
