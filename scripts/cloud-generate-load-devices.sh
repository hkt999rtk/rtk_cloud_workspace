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
GENERATE_ONLY=0
CA_VALID_DAYS=365
DEVICE_VALID_DAYS=180
FACTORY_ENROLL_URL="${FACTORY_ENROLL_URL:-}"
FACTORY_ENROLL_AUTH_KEY="${FACTORY_ENROLL_AUTH_KEY:-}"
FACTORY_ID="${FACTORY_ENROLL_FACTORY_ID:-staging-loadtest}"
LINE_ID="${FACTORY_ENROLL_LINE_ID:-loadtest-line}"
STATION_ID="${FACTORY_ENROLL_STATION_ID:-loadtest-station}"
FIXTURE_ID="${FACTORY_ENROLL_FIXTURE_ID:-loadtest-fixture}"
OPERATOR_ID="${FACTORY_ENROLL_OPERATOR_ID:-loadtest-operator}"
BATCH_ID="${FACTORY_ENROLL_BATCH_ID:-}"
SERIAL_PREFIX="${FACTORY_ENROLL_SERIAL_PREFIX:-LOAD}"
RUN_ID="${FACTORY_ENROLL_RUN_ID:-}"
ENROLL_TIMEOUT="${FACTORY_ENROLL_TIMEOUT:-30}"
ENROLL_SUCCEEDED=0
ENROLL_FAILURES=0

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
  --factory-url URL      Override factory enrollment base URL. Default: env-root metadata.
  --factory-auth-key KEY Override factory enrollment HMAC key. Default: env-root service env.
  --factory-id ID        Factory id sent to enrollment. Default: staging-loadtest.
  --line-id ID           Line id sent to enrollment. Default: loadtest-line.
  --station-id ID        Station id sent to enrollment. Default: loadtest-station.
  --fixture-id ID        Fixture id sent to enrollment. Default: loadtest-fixture.
  --operator-id ID       Operator id sent to enrollment. Default: loadtest-operator.
  --batch-id ID          Batch id sent to enrollment. Default: generated run id.
  --serial-prefix PREFIX Serial number prefix. Default: LOAD.
  --run-id ID            Run id for request ids and serial numbers. Default: UTC timestamp.
  --enroll-timeout SEC   Curl timeout per device enrollment. Default: 30.
  --generate-only        Offline mode: generate locally signed simulation credentials only.
  --ca-valid-days N      Dev CA certificate validity. Default: 365.
  --device-valid-days N  Device certificate validity. Default: 180.
  --force                Remove existing output directory before generation.
  -h, --help             Show this help.

Supported device types match the currently implemented load-test simulator:
camera, light, air_conditioner, smart_meter.

By default this script generates a keypair/CSR for each simulated device and
then runs the real factory enrollment API. Use --generate-only only for offline
tests that need locally signed simulation credentials.
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
	--factory-url) require_value "$1" "${2:-}"; FACTORY_ENROLL_URL="$2"; shift 2 ;;
	--factory-auth-key) require_value "$1" "${2:-}"; FACTORY_ENROLL_AUTH_KEY="$2"; shift 2 ;;
	--factory-id) require_value "$1" "${2:-}"; FACTORY_ID="$2"; shift 2 ;;
	--line-id) require_value "$1" "${2:-}"; LINE_ID="$2"; shift 2 ;;
	--station-id) require_value "$1" "${2:-}"; STATION_ID="$2"; shift 2 ;;
	--fixture-id) require_value "$1" "${2:-}"; FIXTURE_ID="$2"; shift 2 ;;
	--operator-id) require_value "$1" "${2:-}"; OPERATOR_ID="$2"; shift 2 ;;
	--batch-id) require_value "$1" "${2:-}"; BATCH_ID="$2"; shift 2 ;;
	--serial-prefix) require_value "$1" "${2:-}"; SERIAL_PREFIX="$2"; shift 2 ;;
	--run-id) require_value "$1" "${2:-}"; RUN_ID="$2"; shift 2 ;;
	--enroll-timeout) require_value "$1" "${2:-}"; ENROLL_TIMEOUT="$2"; shift 2 ;;
	--generate-only) GENERATE_ONLY=1; shift ;;
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

load_factory_enroll_defaults() {
	local video_env configured_url configured_key
	video_env="$(cloud_env_video_env "$ENV_ROOT")"
	configured_url="$(cloud_env_file_var "$video_env" FACTORY_ENROLL_URL)"
	configured_key="$(cloud_env_file_var "$video_env" FACTORY_ENROLL_AUTH_KEY)"
	if [[ -z "$FACTORY_ENROLL_URL" && -n "$configured_url" ]]; then
		FACTORY_ENROLL_URL="$configured_url"
	fi
	if [[ -z "$FACTORY_ENROLL_URL" && -n "${VIDEO_CLOUD_DOMAIN:-}" ]]; then
		FACTORY_ENROLL_URL="https://$VIDEO_CLOUD_DOMAIN"
	fi
	if [[ -z "$FACTORY_ENROLL_AUTH_KEY" && -n "$configured_key" ]]; then
		FACTORY_ENROLL_AUTH_KEY="$configured_key"
	fi
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

type_service_options_json() {
	case "$1" in
	camera)
		printf '%s\n' '["mqtt","video_streaming","video_storage"]'
		;;
	light|air_conditioner|smart_meter)
		printf '%s\n' '["mqtt"]'
		;;
	esac
}

sign_factory_request() {
	local timestamp="$1"
	local request_id="$2"
	local body_path="$3"
	python3 - "$FACTORY_ENROLL_AUTH_KEY" POST /v1/factory/enroll "$timestamp" "$request_id" "$body_path" <<'PY'
import hashlib
import hmac
import pathlib
import sys

key, method, path, timestamp, request_id, body_path = sys.argv[1:]
body = pathlib.Path(body_path).read_bytes()
body_hash = hashlib.sha256(body).hexdigest()
canonical = "\n".join([method.upper().strip(), path.strip(), timestamp.strip(), request_id.strip(), body_hash])
print("v1=" + hmac.new(key.encode(), canonical.encode(), hashlib.sha256).hexdigest())
PY
}

write_enroll_request() {
	local out_path="$1"
	local request_id="$2"
	local device_id="$3"
	local type="$4"
	local model="$5"
	local display="$6"
	local capability="$7"
	local serial_number="$8"
	local service_options_json="$9"
	local capabilities_json="${10}"
	local csr_path="${11}"
	jq -n \
		--arg request_id "$request_id" \
		--arg device_id "$device_id" \
		--arg type "$type" \
		--arg model "$model" \
		--arg display_name "$display" \
		--arg capability "$capability" \
		--arg serial_number "$serial_number" \
		--arg factory_id "$FACTORY_ID" \
		--arg line_id "$LINE_ID" \
		--arg station_id "$STATION_ID" \
		--arg fixture_id "$FIXTURE_ID" \
		--arg operator_id "$OPERATOR_ID" \
		--arg batch_id "$BATCH_ID" \
		--arg run_id "$RUN_ID" \
		--rawfile csr_pem "$csr_path" \
		--argjson service_options "$service_options_json" \
		--argjson capabilities "$capabilities_json" \
		'{
			request_id: $request_id,
			devid: $device_id,
			csr_pem: $csr_pem,
			serial_number: $serial_number,
			factory_id: $factory_id,
			line_id: $line_id,
			station_id: $station_id,
			fixture_id: $fixture_id,
			operator_id: $operator_id,
			batch_id: $batch_id,
			service_options: $service_options,
			metadata: {
				source: "cloud-generate-load-devices",
				run_id: $run_id,
				device_type: $type,
				model: $model,
				display_name: $display_name,
				mqtt_capability: $capability,
				capabilities: $capabilities,
				service_options: $service_options
			}
		}' > "$out_path"
}

record_enroll_result() {
	local status="$1"
	local index="$2"
	local device_id="$3"
	local type="$4"
	local service_options_csv="$5"
	local http_status="$6"
	local request_id="$7"
	local serial_number="$8"
	local error="$9"
	jq -cn \
		--arg status "$status" \
		--argjson index "$index" \
		--arg device_id "$device_id" \
		--arg device_type "$type" \
		--arg service_options "$service_options_csv" \
		--arg http_status "$http_status" \
		--arg request_id "$request_id" \
		--arg serial_number "$serial_number" \
		--arg error "$error" \
		'{status: $status, index: $index, device_id: $device_id, device_type: $device_type, service_options: ($service_options | split(",")), http_status: $http_status, request_id: $request_id, serial_number: $serial_number, error: $error}' >> "$ENROLL_RESULTS_FILE"
}

validate_device_certificate() {
	local cert_path="$1"
	local chain_path="$2"
	local key_path="$3"
	local device_dir="$4"
	local cert_pub key_pub
	openssl x509 -in "$cert_path" -noout >>"$OPENSSL_LOG" 2>&1 || return 1
	openssl x509 -in "$chain_path" -noout >>"$OPENSSL_LOG" 2>&1 || return 1
	openssl x509 -in "$cert_path" -pubkey -noout > "$device_dir/device.cert.pub.pem" 2>>"$OPENSSL_LOG" || return 1
	openssl pkey -in "$key_path" -pubout > "$device_dir/device.key.pub.pem" 2>>"$OPENSSL_LOG" || return 1
	cert_pub="$(openssl pkey -pubin -in "$device_dir/device.cert.pub.pem" -outform DER 2>>"$OPENSSL_LOG" | openssl dgst -sha256 -r | awk '{print $1}')"
	key_pub="$(openssl pkey -pubin -in "$device_dir/device.key.pub.pem" -outform DER 2>>"$OPENSSL_LOG" | openssl dgst -sha256 -r | awk '{print $1}')"
	[[ -n "$cert_pub" && "$cert_pub" == "$key_pub" ]]
}

factory_enroll_device() {
	local index="$1"
	local type="$2"
	local device_id="$3"
	local model="$4"
	local display="$5"
	local capability="$6"
	local capabilities_json="$7"
	local service_options_json="$8"
	local device_dir="$9"
	local request_id serial_number service_options_csv timestamp signature status error_summary server_serial
	request_id="$RUN_ID-$device_id"
	serial_number="$(printf '%s-%s-%04d' "$SERIAL_PREFIX" "$RUN_ID" "$index")"
	service_options_csv="$(printf '%s' "$service_options_json" | jq -r 'join(",")')"

	log "enroll start: index=$(printf '%03d' "$index") device=$device_id type=$type service_options=$service_options_csv"
	write_enroll_request "$device_dir/factory-enroll-request.json" "$request_id" "$device_id" "$type" "$model" "$display" "$capability" "$serial_number" "$service_options_json" "$capabilities_json" "$device_dir/device.csr.pem"
	timestamp="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
	signature="$(sign_factory_request "$timestamp" "$request_id" "$device_dir/factory-enroll-request.json")"
	status="$(curl -sS -o "$device_dir/factory-enroll-response.json" -w '%{http_code}' \
		--max-time "$ENROLL_TIMEOUT" \
		-H 'Content-Type: application/json' \
		-H "X-Video-Cloud-Request-ID: $request_id" \
		-H "X-Video-Cloud-Timestamp: $timestamp" \
		-H "X-Video-Cloud-Signature: $signature" \
		--data-binary "@$device_dir/factory-enroll-request.json" \
		"$FACTORY_ENROLL_URL/v1/factory/enroll" || true)"

	case "$status" in
		2??) ;;
		*)
			error_summary="$(jq -r '.error // .message // .error_message // empty' "$device_dir/factory-enroll-response.json" 2>/dev/null || true)"
			[[ -n "$error_summary" ]] || error_summary="factory enrollment HTTP $status"
			log "enroll failed: index=$(printf '%03d' "$index") device=$device_id type=$type status=$status error=$error_summary"
			record_enroll_result failed "$index" "$device_id" "$type" "$service_options_csv" "$status" "$request_id" "$serial_number" "$error_summary"
			return 1
			;;
	esac

	if ! jq -er '.certificate_pem | select(length > 0)' "$device_dir/factory-enroll-response.json" > "$device_dir/device.cert.pem"; then
		error_summary="factory enrollment response missing certificate_pem"
		log "enroll failed: index=$(printf '%03d' "$index") device=$device_id type=$type status=$status error=$error_summary"
		record_enroll_result failed "$index" "$device_id" "$type" "$service_options_csv" "$status" "$request_id" "$serial_number" "$error_summary"
		return 1
	fi
	if ! jq -er '.certificate_chain_pem | select(length > 0)' "$device_dir/factory-enroll-response.json" > "$device_dir/device.chain.pem"; then
		error_summary="factory enrollment response missing certificate_chain_pem"
		log "enroll failed: index=$(printf '%03d' "$index") device=$device_id type=$type status=$status error=$error_summary"
		record_enroll_result failed "$index" "$device_id" "$type" "$service_options_csv" "$status" "$request_id" "$serial_number" "$error_summary"
		return 1
	fi
	if ! validate_device_certificate "$device_dir/device.cert.pem" "$device_dir/device.chain.pem" "$device_dir/device.key.pem" "$device_dir"; then
		error_summary="factory enrollment response certificate is invalid or does not match generated key"
		log "enroll failed: index=$(printf '%03d' "$index") device=$device_id type=$type status=$status error=$error_summary"
		record_enroll_result failed "$index" "$device_id" "$type" "$service_options_csv" "$status" "$request_id" "$serial_number" "$error_summary"
		return 1
	fi
	jq 'del(.certificate_pem, .certificate_chain_pem)' "$device_dir/factory-enroll-response.json" > "$device_dir/factory-enroll-response.redacted.json"
	rm -f "$device_dir/factory-enroll-response.json"
	server_serial="$(jq -r '.serial_number // empty' "$device_dir/factory-enroll-response.redacted.json")"
	[[ -n "$server_serial" ]] || server_serial="$serial_number"
	log "enroll ok: index=$(printf '%03d' "$index") device=$device_id type=$type status=$status serial=$server_serial"
	record_enroll_result ok "$index" "$device_id" "$type" "$service_options_csv" "$status" "$request_id" "$server_serial" ""
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
	local device_id model display capability capabilities_json service_options_json service_options_csv device_dir bundle_dir certificate_profile production warning
	device_id="$(printf '%s-%04d' "$PREFIX" "$index")"
	model="$(type_model "$type")"
	display="$(type_display_name "$type" "$ordinal")"
	capability="$(type_capability "$type")"
	capabilities_json="$(type_capabilities_json "$type")"
	service_options_json="$(type_service_options_json "$type")"
	service_options_csv="$(printf '%s' "$service_options_json" | jq -r 'join(",")')"
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

	if [[ "$GENERATE_ONLY" == "1" ]]; then
		log "generate-only: index=$(printf '%03d' "$index") device=$device_id type=$type service_options=$service_options_csv"
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
		cp "$OUT_DIR/ca/sim-device-ca.cert.pem" "$device_dir/device.chain.pem"
		certificate_profile="simulation-device-mtls-client"
		production=false
		warning="Simulation-only generated credential. Do not use as a production or customer device identity."
	else
		if ! factory_enroll_device "$index" "$type" "$device_id" "$model" "$display" "$capability" "$capabilities_json" "$service_options_json" "$device_dir"; then
			return 1
		fi
		certificate_profile="factory-enrolled-device-mtls-client"
		production=false
		warning="Factory-enrolled staging load-test credential. Keep private key material out of source control."
	fi

	cat "$device_dir/device.cert.pem" "$device_dir/device.key.pem" > "$bundle_dir/$device_id.pem"
	chmod 0600 "$bundle_dir/$device_id.pem"

	jq -n \
		--arg device_id "$device_id" \
		--arg type "$type" \
		--arg capability "$capability" \
		--arg model "$model" \
		--arg display_name "$display" \
		--arg cert "devices/$type/$device_id/device.cert.pem" \
		--arg chain "devices/$type/$device_id/device.chain.pem" \
		--arg key "devices/$type/$device_id/device.key.pem" \
		--arg csr "devices/$type/$device_id/device.csr.pem" \
		--arg bundle "bundles/$type/$device_id.pem" \
		--arg certificate_profile "$certificate_profile" \
		--argjson production "$production" \
		--arg warning "$warning" \
		--argjson capabilities "$capabilities_json" \
		--argjson service_options "$service_options_json" \
		'{
			device_id: $device_id,
			device_type: $type,
			mqtt_capability: $capability,
			service_options: $service_options,
			model: $model,
			display_name: $display_name,
			firmware_version: "0.0.0-loadtest",
			capabilities: $capabilities,
			certificate_profile: $certificate_profile,
			certificate_path: $cert,
			certificate_chain_path: $chain,
			key_path: $key,
			csr_path: $csr,
			bundle_path: $bundle,
			production: $production,
			warning: $warning
		}' > "$device_dir/metadata.json"

	printf '%s,%s,%s,%s,%s,%s,%s,%s\n' \
		"$device_id" "$type" "$capability" "$(printf '%s' "$service_options_json" | jq -r 'join(";")')" "$model" \
		"devices/$type/$device_id/device.cert.pem" \
		"devices/$type/$device_id/device.key.pem" \
		"bundles/$type/$device_id.pem" >> "$CSV_MANIFEST"
	printf '%s\n' "$device_id" >> "$DEVICE_IDS_FILE"
	printf '%s\n' "$device_dir/metadata.json" >> "$METADATA_FILES"
}

write_manifests() {
	local type count profile iot_mix device_ids_csv enroll_mode
	mkdir -p "$OUT_DIR/manifests"
	CSV_MANIFEST="$OUT_DIR/manifests/devices.csv"
	DEVICE_IDS_FILE="$OUT_DIR/manifests/device_ids.txt"
	METADATA_FILES="$OUT_DIR/manifests/metadata_files.txt"
	ENROLL_RESULTS_FILE="$OUT_DIR/manifests/factory-enroll-results.jsonl"
	printf 'device_id,device_type,mqtt_capability,service_options,model,certificate_path,key_path,bundle_path\n' > "$CSV_MANIFEST"
	: > "$DEVICE_IDS_FILE"
	: > "$METADATA_FILES"
	: > "$ENROLL_RESULTS_FILE"

	local index=1
	for type in "${DEVICE_TYPES[@]}"; do
		count="$(get_allocated "$type")"
		[[ "$count" -gt 0 ]] || continue
		log "generating devices: type=$type count=$count"
		for ordinal in $(seq 1 "$count"); do
			if write_device_material "$index" "$type" "$ordinal"; then
				ENROLL_SUCCEEDED=$((ENROLL_SUCCEEDED + 1))
			else
				ENROLL_FAILURES=$((ENROLL_FAILURES + 1))
			fi
			index=$((index + 1))
		done
	done

	if [[ -s "$METADATA_FILES" ]]; then
		jq -s . $(cat "$METADATA_FILES") > "$OUT_DIR/manifests/devices.json"
	else
		printf '[]\n' > "$OUT_DIR/manifests/devices.json"
	fi

	device_ids_csv=""
	if [[ -s "$DEVICE_IDS_FILE" ]]; then
		device_ids_csv="$(paste -sd, "$DEVICE_IDS_FILE")"
	fi
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

	enroll_mode="factory_enroll"
	if [[ "$GENERATE_ONLY" == "1" ]]; then
		enroll_mode="generate_only"
	fi
	jq -n \
		--arg generated_at "$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
		--arg prefix "$PREFIX" \
		--arg mix "$MIX" \
		--arg out_dir "$OUT_DIR" \
		--arg enroll_mode "$enroll_mode" \
		--arg factory_url "$FACTORY_ENROLL_URL" \
		--arg loadtest_env "loadtest.env" \
		--arg device_ids "manifests/device_ids.txt" \
		--arg devices_csv "manifests/devices.csv" \
		--arg devices_json "manifests/devices.json" \
		--arg enroll_results "manifests/factory-enroll-results.jsonl" \
		--arg ca_cert "ca/sim-device-ca.cert.pem" \
		--argjson count "$COUNT" \
		--argjson enroll_succeeded "$ENROLL_SUCCEEDED" \
		--argjson enroll_failures "$ENROLL_FAILURES" \
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
			enrollment: {
				mode: $enroll_mode,
				factory_url: $factory_url,
				succeeded: $enroll_succeeded,
				failed: $enroll_failures,
				results: $enroll_results
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
	local mode_description credential_source
	if [[ "$GENERATE_ONLY" == "1" ]]; then
		mode_description="offline generate-only"
		credential_source="locally signed by the simulation CA"
	else
		mode_description="factory enrollment"
		credential_source="issued by $FACTORY_ENROLL_URL/v1/factory/enroll"
	fi
	cat > "$OUT_DIR/README.md" <<EOF_README
# Staging Load-Test Device Factory Output

This directory contains staging load-test device identities generated for
factory/provisioning flow rehearsal.

- Device count: $COUNT
- Requested mix: $MIX
- Mode: $mode_description
- Device key type: EC P-256
- Device certificate profile: clientAuth
- Credential source: $credential_source
- CA validity days: $CA_VALID_DAYS
- Device validity days: $DEVICE_VALID_DAYS

## Files

- \`ca/sim-device-ca.cert.pem\`: simulation CA certificate, present only with \`--generate-only\`.
- \`ca/sim-device-ca.key.pem\`: simulation CA private key, present only with \`--generate-only\`.
- \`devices/<type>/<device_id>/device.key.pem\`: per-device private key.
- \`devices/<type>/<device_id>/device.csr.pem\`: per-device CSR.
- \`devices/<type>/<device_id>/device.cert.pem\`: per-device client certificate.
- \`devices/<type>/<device_id>/device.chain.pem\`: issuing certificate chain.
- \`devices/<type>/<device_id>/factory-enroll-response.redacted.json\`: non-secret enrollment response details, present with factory enrollment mode.
- \`devices/<type>/<device_id>/metadata.json\`: per-device metadata.
- \`bundles/<type>/<device_id>.pem\`: certificate + private key bundle.
- \`manifests/devices.csv\`: compact inventory.
- \`manifests/devices.json\`: full inventory.
- \`manifests/device_ids.txt\`: one device id per line.
- \`manifests/factory-enroll-results.jsonl\`: per-device enrollment status.
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
require_positive_int "--enroll-timeout" "$ENROLL_TIMEOUT"
[[ "$PREFIX" =~ ^[A-Za-z0-9][A-Za-z0-9_.-]*$ ]] || die "--prefix contains unsupported characters"
[[ -n "$ENV_ROOT" ]] || die "--env-root is required; pass the environment directory explicitly, for example --env-root cloud_env/staging"
if [[ -z "$RUN_ID" ]]; then
	RUN_ID="$(date -u +%Y%m%dT%H%M%SZ)"
fi
if [[ -z "$BATCH_ID" ]]; then
	BATCH_ID="$RUN_ID"
fi

need_cmd openssl
need_cmd jq
need_cmd paste

WORKSPACE="$(cd "$WORKSPACE" && pwd)"
source "$SCRIPT_DIR/lib/cloud-env.sh"
ENV_ROOT="$(cloud_env_init "$WORKSPACE" "$ENV_ROOT")"
cloud_env_load_environment "$ENV_ROOT"
load_factory_enroll_defaults
if [[ "$GENERATE_ONLY" != "1" ]]; then
	[[ -n "$FACTORY_ENROLL_URL" ]] || die "factory enrollment URL missing; set FACTORY_ENROLL_URL in $(cloud_env_video_env "$ENV_ROOT") or pass --factory-url"
	[[ -n "$FACTORY_ENROLL_AUTH_KEY" ]] || die "factory enrollment auth key missing; set FACTORY_ENROLL_AUTH_KEY in $(cloud_env_video_env "$ENV_ROOT") or pass --factory-auth-key"
	need_cmd curl
	need_cmd python3
	FACTORY_ENROLL_URL="${FACTORY_ENROLL_URL%/}"
fi
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

if [[ "$GENERATE_ONLY" == "1" ]]; then
	log "start load-test device generation: count=$COUNT mix=$MIX mode=generate_only"
else
	log "start load-test device generation: count=$COUNT mix=$MIX mode=factory_enroll factory_url=$FACTORY_ENROLL_URL"
fi
log "workspace=$WORKSPACE"
log "output=$OUT_DIR"
log "run_id=$RUN_ID batch_id=$BATCH_ID"
parse_mix "$MIX"
allocate_counts
if [[ "$GENERATE_ONLY" == "1" ]]; then
	write_ca
fi
write_manifests
write_readme

if [[ "$ENROLL_FAILURES" -gt 0 ]]; then
	log "complete with failures: requested=$COUNT succeeded=$ENROLL_SUCCEEDED failed=$ENROLL_FAILURES results=$ENROLL_RESULTS_FILE"
	printf 'output=%s\n' "$OUT_DIR"
	printf 'summary=%s\n' "$OUT_DIR/summary.json"
	printf 'enroll_results=%s\n' "$ENROLL_RESULTS_FILE"
	printf 'openssl_log=%s\n' "$OPENSSL_LOG"
	exit 1
fi

log "complete: requested=$COUNT succeeded=$ENROLL_SUCCEEDED failed=$ENROLL_FAILURES"
printf 'output=%s\n' "$OUT_DIR"
printf 'summary=%s\n' "$OUT_DIR/summary.json"
printf 'enroll_results=%s\n' "$ENROLL_RESULTS_FILE"
printf 'loadtest_env=%s\n' "$OUT_DIR/loadtest.env"
printf 'openssl_log=%s\n' "$OPENSSL_LOG"
