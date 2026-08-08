#!/usr/bin/env bash
set -euo pipefail

action="${1:-}"
workspace="${GITHUB_WORKSPACE:-$(git rev-parse --show-toplevel)}"
run_id="${RUNTIME_COVERAGE_RUN_ID:-}"
stack="${RUNTIME_COVERAGE_STACK:-}"
kubeconfig="${KUBECONFIG:-}"
output_root="${RUNTIME_COVERAGE_OUTPUT_ROOT:-$workspace/.artifacts/runtime-coverage/$run_id}"

usage() {
  echo "usage: runtime-coverage-k8s.sh snapshot|verify|endpoints|tunnel-start|tunnel-stop|prepare|collect|cleanup" >&2
}

staging_snapshot_path() {
  echo "$workspace/.artifacts/runtime-coverage/$run_id/staging-before.json"
}

read_staging_deployments() {
  local snapshot_tmp deployments_file pods_file
  snapshot_tmp="$(mktemp -d)"
  deployments_file="$snapshot_tmp/deployments.json"
  pods_file="$snapshot_tmp/pods.json"
  if ! kubectl --kubeconfig "$kubeconfig" get deployments -A \
    -l "rtk.realtek.com/stack=video-cloud-staging" -o json > "$deployments_file"; then
    rm -rf "$snapshot_tmp"
    return 1
  fi
  if ! kubectl --kubeconfig "$kubeconfig" get pods -A \
    -l "rtk.realtek.com/stack=video-cloud-staging" -o json > "$pods_file"; then
    rm -rf "$snapshot_tmp"
    return 1
  fi
  if jq -n --slurpfile deployments "$deployments_file" --slurpfile pods "$pods_file" '{
    deployments: [
      $deployments[0].items[] | {
        namespace: .metadata.namespace,
        name: .metadata.name,
        uid: .metadata.uid,
        images: ([.spec.template.spec.containers[].image] | sort)
      }
    ] | sort_by(.namespace, .name),
    image_digests: ([
      $pods[0].items[].status.containerStatuses[]?.imageID
      | select(length > 0)
    ] | unique | sort)
  }'; then
    rm -rf "$snapshot_tmp"
    return 0
  else
    status=$?
    rm -rf "$snapshot_tmp"
    return "$status"
  fi
}

snapshot() {
  local path
  path="$(staging_snapshot_path)"
  mkdir -p "$(dirname "$path")"
  read_staging_deployments |
    jq --arg run_id "$run_id" --arg generated_at "$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
      '. + {schema_version: 1, run_id: $run_id, generated_at: $generated_at}' > "$path"
  [[ "$(jq '.deployments | length' "$path")" -gt 0 ]] || {
    echo "shared staging snapshot contains no deployments" >&2
    exit 1
  }
}

verify_deployments() {
  local report="$output_root/deployment-anchors.json"
  local entries_file pods_file deployment_file source_commit expected_image expected_digest expected_container pod_selector
  local module namespace deployment image_env digest_env module_path error_json
  local status="PASS"
  local errors=()
  entries_file="$(mktemp)"
  pods_file="$(mktemp)"
  deployment_file="$(mktemp)"
  while IFS='|' read -r module namespace deployment image_env digest_env module_path; do
    [[ -n "$module" ]] || continue
    expected_image="${!image_env:-}"
    expected_digest="${!digest_env:-}"
    source_commit="$(git -C "$workspace/$module_path" rev-parse HEAD)"
    if [[ -z "$expected_image" ]]; then
      errors+=("$module expected image is missing")
    fi
    if [[ "$expected_digest" != sha256:* ]]; then
      errors+=("$module expected image digest is missing or invalid")
    fi
    if ! kubectl --kubeconfig "$kubeconfig" -n "$namespace" \
      rollout status "deployment/$deployment" --timeout=10m; then
      errors+=("$module deployment rollout is not ready")
    fi
    if ! kubectl --kubeconfig "$kubeconfig" -n "$namespace" \
      get "deployment/$deployment" -o json > "$deployment_file"; then
      errors+=("$module deployment manifest is unavailable")
      printf '{"items":[]}\n' > "$deployment_file"
    fi
    if ! jq -e --arg image "$expected_image" \
      'any(.spec.template.spec.containers[]?; .image == $image)' "$deployment_file" >/dev/null; then
      errors+=("$module deployment does not reference the expected coverage image")
    fi
    expected_container="$(
      jq -r --arg image "$expected_image" \
        '.spec.template.spec.containers[]? | select(.image == $image) | .name' \
        "$deployment_file" | head -n 1
    )"
    pod_selector="$(
      jq -r '
        .spec.selector.matchLabels
        | to_entries
        | map("\(.key)=\(.value)")
        | join(",")
      ' "$deployment_file"
    )"
    if ! kubectl --kubeconfig "$kubeconfig" -n "$namespace" \
      get pods -l "$pod_selector" -o json > "$pods_file"; then
      errors+=("$module deployment pods are unavailable")
      printf '{"items":[]}\n' > "$pods_file"
    fi
    if ! jq -e --arg container "$expected_container" --arg digest "$expected_digest" '
      [.items[].status.containerStatuses[]? | select(.name == $container)] as $statuses
      | ($statuses | length) > 0
        and all($statuses[]; ((.imageID // "") | contains($digest)))
    ' "$pods_file" >/dev/null; then
      errors+=("$module running pod image IDs do not match the expected digest")
    fi
    jq -n \
      --arg module "$module" \
      --arg namespace "$namespace" \
      --arg deployment "$deployment" \
      --arg image "$expected_image" \
      --arg digest "$expected_digest" \
      --arg container "$expected_container" \
      --arg source_path "$module_path" \
      --arg source_commit "$source_commit" \
      --slurpfile pods "$pods_file" \
      '{
        module: $module,
        namespace: $namespace,
        deployment: $deployment,
        image: $image,
        digest: $digest,
        source_path: $source_path,
        source_commit: $source_commit,
        pod_image_ids: ([
          $pods[0].items[].status.containerStatuses[]?
          | select(.name == $container)
          | .imageID
        ] | unique | sort)
      }' >> "$entries_file"
  done <<EOF
account-manager|$stack-account-manager|account-manager|LKE_ACCOUNT_MANAGER_IMAGE|LKE_ACCOUNT_MANAGER_IMAGE_DIGEST|repos/rtk_account_manager
cloud-admin-backend|$stack-admin|cloud-admin|LKE_CLOUD_ADMIN_IMAGE|LKE_CLOUD_ADMIN_IMAGE_DIGEST|repos/rtk_cloud_admin
cloud-frontend|$stack-frontend|frontend|LKE_FRONTEND_IMAGE|LKE_FRONTEND_IMAGE_DIGEST|repos/rtk_cloud_frontend
cloud-logger|$stack-logger|cloud-logger|LKE_CLOUD_LOGGER_IMAGE|LKE_CLOUD_LOGGER_IMAGE_DIGEST|repos/rtk_cloud_logger
video-cloud|$stack-video-cloud|video-cloud-api|LKE_VIDEO_CLOUD_IMAGE|LKE_VIDEO_CLOUD_IMAGE_DIGEST|repos/rtk_video_cloud
EOF
  if ((${#errors[@]})); then
    status="FAIL"
  fi
  mkdir -p "$(dirname "$report")"
  error_json="$(printf '%s\n' "${errors[@]:-}" | jq -Rsc 'split("\n") | map(select(length > 0))')"
  jq -n \
    --arg status "$status" \
    --arg run_id "$run_id" \
    --arg workspace_commit "$(git -C "$workspace" rev-parse HEAD)" \
    --arg generated_at "$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
    --argjson deployments "$(jq -s . "$entries_file")" \
    --argjson errors "$error_json" \
    '{
      schema_version: 1,
      status: $status,
      run_id: $run_id,
      workspace_commit: $workspace_commit,
      generated_at: $generated_at,
      deployments: $deployments,
      errors: $errors
    }' > "$report"
  rm -f "$entries_file" "$pods_file" "$deployment_file"
  if [[ "$status" != "PASS" ]]; then
    printf '%s\n' "${errors[@]}" >&2
  fi
  [[ "$status" == "PASS" ]]
}

feature_endpoint_host() {
  local namespace="$1"
  local service="$2"
  local host=""
  for _ in {1..120}; do
    host="$(
      kubectl --kubeconfig "$kubeconfig" -n "$namespace" get "service/$service" -o json |
        jq -r '.status.loadBalancer.ingress[0].ip // .status.loadBalancer.ingress[0].hostname // empty'
    )"
    if [[ -n "$host" ]]; then
      printf '%s\n' "$host"
      return 0
    fi
    sleep 5
  done
  echo "timed out waiting for endpoint $namespace/$service" >&2
  return 1
}

apply_feature_endpoint() {
  local namespace="$1"
  local service="$2"
  local app="$3"
  local port="$4"
  local target_port="$5"
  kubectl --kubeconfig "$kubeconfig" apply -f - <<EOF
apiVersion: v1
kind: Service
metadata:
  name: $service
  namespace: $namespace
  labels:
    app.kubernetes.io/name: $app
    app.kubernetes.io/component: runtime-coverage-public
    rtk-cloud-run-id: $run_id
    rtk.realtek.com/stack: $stack
spec:
  type: LoadBalancer
  selector:
    app.kubernetes.io/name: $app
  ports:
    - name: runtime-coverage
      port: $port
      targetPort: $target_port
EOF
}

allow_feature_endpoint_ingress() {
  local namespace="$1"
  local app="$2"
  local port="$3"
  kubectl --kubeconfig "$kubeconfig" apply -f - <<EOF
apiVersion: networking.k8s.io/v1
kind: NetworkPolicy
metadata:
  name: allow-runtime-coverage-public-$app
  namespace: $namespace
  labels:
    rtk-cloud-run-id: $run_id
    rtk.realtek.com/stack: $stack
spec:
  podSelector:
    matchLabels:
      app.kubernetes.io/name: $app
  policyTypes: [Ingress]
  ingress:
    - ports:
        - protocol: TCP
          port: $port
EOF
}

create_feature_endpoints() {
  local account_namespace="$stack-account-manager"
  local video_namespace="$stack-video-cloud"
  local ingress_namespace="$stack-ingress"
  local ingress_service="runtime-coverage-ingress"
  local account_domain="account.$stack.invalid"
  local video_domain="video.$stack.invalid"
  local device_domain="device.video.$stack.invalid"
  local ingress_host="127.0.0.1"
  local mqtt_host="127.0.0.1"
  local https_port="18443"
  local mqtt_port="18883"
  local endpoint_env report tls_tmp tls_crt tls_key ca_bundle
  local server_ca server_ca_key server_csr server_ext
  tls_tmp="$(mktemp -d)"
  tls_crt="$tls_tmp/tls.crt"
  tls_key="$tls_tmp/tls.key"
  server_ca="$RUNTIME_ENV_ROOT/state/secrets/runtime-coverage-server-ca.crt"
  server_ca_key="$tls_tmp/server-ca.key"
  server_csr="$tls_tmp/server.csr"
  server_ext="$tls_tmp/server.ext"
  ca_bundle="${RUNTIME_ENV_ROOT:?RUNTIME_ENV_ROOT is required}/state/secrets/device-client-ca-bundle.pem"
  [[ -s "$ca_bundle" ]] || {
    echo "run-scoped device client CA bundle is missing: $ca_bundle" >&2
    rm -rf "$tls_tmp"
    return 1
  }
  mkdir -p "$(dirname "$server_ca")"
  openssl req -x509 -nodes -newkey rsa:2048 -days 2 \
    -subj "/CN=RTK runtime coverage $run_id" \
    -addext "basicConstraints=critical,CA:TRUE" \
    -addext "keyUsage=critical,keyCertSign,cRLSign" \
    -keyout "$server_ca_key" -out "$server_ca" >/dev/null 2>&1
  chmod 0600 "$server_ca"
  openssl req -nodes -newkey rsa:2048 \
    -subj "/CN=$video_domain" \
    -keyout "$tls_key" -out "$server_csr" >/dev/null 2>&1
  cat > "$server_ext" <<EOF
basicConstraints=critical,CA:FALSE
keyUsage=critical,digitalSignature,keyEncipherment
extendedKeyUsage=serverAuth
subjectAltName=DNS:$video_domain,DNS:$device_domain,DNS:$account_domain
EOF
  openssl x509 -req -days 2 -sha256 \
    -in "$server_csr" -CA "$server_ca" -CAkey "$server_ca_key" -CAcreateserial \
    -extfile "$server_ext" -out "$tls_crt" >/dev/null 2>&1
  kubectl --kubeconfig "$kubeconfig" apply -f - <<EOF
apiVersion: v1
kind: Namespace
metadata:
  name: $ingress_namespace
  labels:
    app.kubernetes.io/part-of: rtk-cloud
    rtk.realtek.com/stack: $stack
---
apiVersion: v1
kind: Secret
metadata:
  name: runtime-coverage-ingress-tls
  namespace: $ingress_namespace
  labels:
    rtk-cloud-run-id: $run_id
    rtk.realtek.com/stack: $stack
type: kubernetes.io/tls
data:
  tls.crt: $(openssl base64 -A < "$tls_crt")
  tls.key: $(openssl base64 -A < "$tls_key")
---
apiVersion: v1
kind: Secret
metadata:
  name: runtime-coverage-device-client-ca
  namespace: $ingress_namespace
  labels:
    rtk-cloud-run-id: $run_id
    rtk.realtek.com/stack: $stack
type: Opaque
data:
  ca.crt: $(openssl base64 -A < "$ca_bundle")
---
apiVersion: v1
kind: ConfigMap
metadata:
  name: runtime-coverage-ingress
  namespace: $ingress_namespace
  labels:
    rtk-cloud-run-id: $run_id
    rtk.realtek.com/stack: $stack
data:
  nginx.conf: |
    user nginx;
    worker_processes auto;
    error_log /dev/stderr info;
    pid /run/nginx.pid;
    events {
      worker_connections 1024;
    }
    http {
      include /etc/nginx/mime.types;
      default_type application/octet-stream;
      access_log /dev/stdout combined;
      sendfile on;
      keepalive_timeout 65;
      map \$http_upgrade \$connection_upgrade {
        default upgrade;
        '' close;
      }
      server {
        listen 443 ssl;
        server_name $account_domain;
        ssl_certificate /etc/runtime-tls/tls.crt;
        ssl_certificate_key /etc/runtime-tls/tls.key;
        location / {
          proxy_set_header Host \$host;
          proxy_set_header X-Forwarded-Proto https;
          proxy_pass http://account-manager.$account_namespace.svc.cluster.local:80;
        }
      }
      server {
        listen 443 ssl;
        server_name $video_domain;
        ssl_certificate /etc/runtime-tls/tls.crt;
        ssl_certificate_key /etc/runtime-tls/tls.key;
        location / {
          proxy_http_version 1.1;
          proxy_set_header Host \$host;
          proxy_set_header X-Forwarded-Proto https;
          proxy_set_header Upgrade \$http_upgrade;
          proxy_set_header Connection \$connection_upgrade;
          proxy_read_timeout 75s;
          proxy_pass http://video-cloud-api.$video_namespace.svc.cluster.local:80;
        }
      }
      server {
        listen 443 ssl;
        server_name $device_domain;
        ssl_certificate /etc/runtime-tls/tls.crt;
        ssl_certificate_key /etc/runtime-tls/tls.key;
        ssl_client_certificate /etc/runtime-client-ca/ca.crt;
        ssl_verify_client on;
        ssl_verify_depth 2;
        location / {
          proxy_http_version 1.1;
          proxy_set_header Host \$host;
          proxy_set_header X-Forwarded-Proto https;
          proxy_set_header X-Client-Verify \$ssl_client_verify;
          proxy_set_header X-Client-S-DN \$ssl_client_s_dn_legacy;
          proxy_set_header Upgrade \$http_upgrade;
          proxy_set_header Connection \$connection_upgrade;
          proxy_read_timeout 75s;
          proxy_pass http://video-cloud-api.$video_namespace.svc.cluster.local:80;
        }
      }
    }
    stream {
      server {
        listen 8883;
        proxy_connect_timeout 10s;
        proxy_timeout 10m;
        proxy_pass mqtt.$video_namespace.svc.cluster.local:8883;
      }
    }
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: runtime-coverage-ingress
  namespace: $ingress_namespace
  labels:
    app.kubernetes.io/name: runtime-coverage-ingress
    rtk-cloud-run-id: $run_id
    rtk.realtek.com/stack: $stack
spec:
  replicas: 1
  selector:
    matchLabels:
      app.kubernetes.io/name: runtime-coverage-ingress
  template:
    metadata:
      labels:
        app.kubernetes.io/name: runtime-coverage-ingress
        rtk-cloud-run-id: $run_id
        rtk.realtek.com/stack: $stack
    spec:
      containers:
        - name: nginx
          image: nginx:1.29-alpine
          ports:
            - name: https
              containerPort: 443
            - name: mqtts
              containerPort: 8883
          readinessProbe:
            tcpSocket:
              port: https
          volumeMounts:
            - name: config
              mountPath: /etc/nginx/nginx.conf
              subPath: nginx.conf
            - name: tls
              mountPath: /etc/runtime-tls
              readOnly: true
            - name: client-ca
              mountPath: /etc/runtime-client-ca
              readOnly: true
      volumes:
        - name: config
          configMap:
            name: runtime-coverage-ingress
        - name: tls
          secret:
            secretName: runtime-coverage-ingress-tls
        - name: client-ca
          secret:
            secretName: runtime-coverage-device-client-ca
---
apiVersion: v1
kind: Service
metadata:
  name: $ingress_service
  namespace: $ingress_namespace
  labels:
    app.kubernetes.io/name: runtime-coverage-ingress
    app.kubernetes.io/component: runtime-coverage-public
    rtk-cloud-run-id: $run_id
    rtk.realtek.com/stack: $stack
spec:
  type: ClusterIP
  selector:
    app.kubernetes.io/name: runtime-coverage-ingress
  ports:
    - name: https
      port: 443
      targetPort: https
    - name: mqtts
      port: 8883
      targetPort: mqtts
EOF
  rm -rf "$tls_tmp"
  allow_feature_endpoint_ingress "$account_namespace" account-manager 8080
  allow_feature_endpoint_ingress "$video_namespace" video-cloud-api 8080
  allow_feature_endpoint_ingress "$video_namespace" mqtt 8883
  kubectl --kubeconfig "$kubeconfig" -n "$ingress_namespace" \
    rollout status deployment/runtime-coverage-ingress --timeout=5m
  endpoint_env="$output_root/feature-endpoints.env"
  report="$output_root/feature-endpoints.json"
  mkdir -p "$output_root"
  cat > "$endpoint_env" <<EOF
ACCOUNT_MANAGER_BASE_URL=https://$account_domain:$https_port
VIDEO_CLOUD_BASE_URL=https://$video_domain:$https_port
VIDEO_CLOUD_PUBLIC_BASE_URL=https://$video_domain:$https_port
VIDEO_CLOUD_TOKEN_BASE_URL=https://$device_domain:$https_port
VIDEO_CLOUD_MQTT_ADDR=$mqtt_host:$mqtt_port
HOME100K_ACCOUNT_MANAGER_BASE_URL=https://$account_domain:$https_port
HOME100K_VIDEO_CLOUD_PUBLIC_BASE_URL=https://$video_domain:$https_port
HOME100K_VIDEO_CLOUD_TOKEN_BASE_URL=https://$device_domain:$https_port
HOME100K_MQTT_ADDR=$mqtt_host:$mqtt_port
HOME100K_GENERATOR_HOSTS_OVERRIDE_IP=$ingress_host
HOME100K_CLOUD_LOGGER_ENDPOINT=http://127.0.0.1:18090
RUNTIME_COVERAGE_INGRESS_IP=$ingress_host
RUNTIME_COVERAGE_HOSTNAMES=$account_domain,$video_domain,$device_domain
RUNTIME_COVERAGE_SERVER_CA=$server_ca
VIDEO_CLOUD_LOAD_STORAGE_EXEC=kubernetes
VIDEO_CLOUD_LOAD_STORAGE_NAMESPACE=$video_namespace
VIDEO_CLOUD_LOAD_STORAGE_DEPLOYMENT=video-cloud-api
EOF
  jq -n \
    --arg run_id "$run_id" \
    --arg ingress_namespace "$ingress_namespace" \
    --arg ingress_service "$ingress_service" \
    --arg ingress_host "$ingress_host" \
    --arg account_domain "$account_domain" \
    --arg video_domain "$video_domain" \
    --arg device_domain "$device_domain" \
    --arg mqtt_host "$mqtt_host" \
    --argjson https_port "$https_port" \
    --argjson mqtt_port "$mqtt_port" \
    --arg generated_at "$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
    '{
      schema_version: 1,
      status: "PASS",
      run_id: $run_id,
      generated_at: $generated_at,
      dns_created: false,
      endpoints: [
        {
          kind: "https-ingress",
          namespace: $ingress_namespace,
          service: $ingress_service,
          host: $ingress_host,
          port: $https_port,
          virtual_hosts: [$account_domain, $video_domain, $device_domain],
          device_mtls: true
        },
        {kind: "mqtt-tunnel", namespace: $ingress_namespace, service: $ingress_service, host: $mqtt_host, port: $mqtt_port}
      ]
    }' > "$report"
}

tunnel_start() {
  local ingress_namespace="$stack-ingress"
  local tunnel_dir="$output_root/tunnels"
  local pid_file="$tunnel_dir/port-forward.pid"
  local logger_pid_file="$tunnel_dir/logger-port-forward.pid"
  local ingress_log_pid_file="$tunnel_dir/ingress-log.pid"
  local log_file="$tunnel_dir/port-forward.log"
  local logger_log_file="$tunnel_dir/logger-port-forward.log"
  local ingress_log_file="$tunnel_dir/ingress.log"
  local logger_namespace="$stack-logger"
  local logger_tunnel_enabled="${RUNTIME_COVERAGE_LOGGER_TUNNEL:-true}"
  local tunnel_pid logger_tunnel_pid
  mkdir -p "$tunnel_dir"
  tunnel_stop
  RUNNER_TRACKING_ID="" nohup kubectl --kubeconfig "$kubeconfig" -n "$ingress_namespace" \
    port-forward --address 127.0.0.1 service/runtime-coverage-ingress \
    18443:443 18883:8883 > "$log_file" 2>&1 &
  tunnel_pid=$!
  printf '%s\n' "$tunnel_pid" > "$pid_file"
  if [[ "$logger_tunnel_enabled" == "true" ]]; then
    RUNNER_TRACKING_ID="" nohup kubectl --kubeconfig "$kubeconfig" -n "$logger_namespace" \
      port-forward --address 127.0.0.1 service/cloud-logger \
      18090:80 > "$logger_log_file" 2>&1 &
    logger_tunnel_pid=$!
    printf '%s\n' "$logger_tunnel_pid" > "$logger_pid_file"
  fi
  RUNNER_TRACKING_ID="" nohup kubectl --kubeconfig "$kubeconfig" -n "$ingress_namespace" \
    logs --follow deployment/runtime-coverage-ingress > "$ingress_log_file" 2>&1 &
  printf '%s\n' "$!" > "$ingress_log_pid_file"
  for _ in {1..60}; do
    if kill -0 "$tunnel_pid" 2>/dev/null &&
      { [[ "$logger_tunnel_enabled" != "true" ]] || kill -0 "$logger_tunnel_pid" 2>/dev/null; } &&
      timeout 2 bash -c 'exec 3<>/dev/tcp/127.0.0.1/18443' &&
      timeout 2 bash -c 'exec 3<>/dev/tcp/127.0.0.1/18883' &&
      { [[ "$logger_tunnel_enabled" != "true" ]] || timeout 2 bash -c 'exec 3<>/dev/tcp/127.0.0.1/18090'; }; then
      return 0
    fi
    sleep 1
  done
  cat "$log_file" >&2 || true
  if [[ "$logger_tunnel_enabled" == "true" ]]; then
    cat "$logger_log_file" >&2 || true
  fi
  tunnel_stop
  echo "runtime coverage local tunnels did not become ready" >&2
  return 1
}

tunnel_stop() {
  local tunnel_dir="$output_root/tunnels"
  local pid_file="$tunnel_dir/port-forward.pid"
  local logger_pid_file="$tunnel_dir/logger-port-forward.pid"
  local ingress_log_pid_file="$tunnel_dir/ingress-log.pid"
  local tunnel_pid=""
  local logger_tunnel_pid=""
  mkdir -p "$tunnel_dir"
  if [[ -f "$pid_file" ]]; then
    tunnel_pid="$(tr -d '[:space:]' < "$pid_file")"
  fi
  if [[ "$tunnel_pid" =~ ^[1-9][0-9]*$ ]]; then
    kill "$tunnel_pid" 2>/dev/null || true
    wait "$tunnel_pid" 2>/dev/null || true
  fi
  if [[ -f "$logger_pid_file" ]]; then
    logger_tunnel_pid="$(tr -d '[:space:]' < "$logger_pid_file")"
  fi
  if [[ "$logger_tunnel_pid" =~ ^[1-9][0-9]*$ ]]; then
    kill "$logger_tunnel_pid" 2>/dev/null || true
    wait "$logger_tunnel_pid" 2>/dev/null || true
  fi
  if [[ -f "$ingress_log_pid_file" ]]; then
    ingress_log_pid="$(tr -d '[:space:]' < "$ingress_log_pid_file")"
    if [[ "$ingress_log_pid" =~ ^[1-9][0-9]*$ ]]; then
      kill "$ingress_log_pid" 2>/dev/null || true
      wait "$ingress_log_pid" 2>/dev/null || true
    fi
  fi
  : > "$pid_file"
  : > "$logger_pid_file"
  : > "$ingress_log_pid_file"
}

validate_scope() {
  [[ "$run_id" =~ ^[A-Za-z0-9][A-Za-z0-9_.-]{0,127}$ ]] || {
    echo "RUNTIME_COVERAGE_RUN_ID is missing or invalid" >&2
    exit 2
  }
  [[ "$stack" =~ ^coverage-[a-z0-9-]+$ ]] || {
    echo "RUNTIME_COVERAGE_STACK must start with coverage- and contain lowercase letters, digits, and hyphens" >&2
    exit 2
  }
  [[ "$stack" != *staging* && "$stack" != *production* && "$stack" != *prod* ]] || {
    echo "runtime coverage refuses staging/production stack names" >&2
    exit 2
  }
  [[ -n "$kubeconfig" && -f "$kubeconfig" ]] || {
    echo "KUBECONFIG must point to the run-scoped shared staging kubeconfig" >&2
    exit 2
  }
}

namespace_module() {
  case "$1" in
    "$stack-account-manager") echo "account-manager" ;;
    "$stack-admin") echo "cloud-admin-backend" ;;
    "$stack-frontend") echo "cloud-frontend" ;;
    "$stack-logger") echo "cloud-logger" ;;
    "$stack-video-cloud") echo "video-cloud" ;;
    *) return 1 ;;
  esac
}

coverage_namespaces() {
  printf '%s\n' \
    "$stack-account-manager" \
    "$stack-admin" \
    "$stack-frontend" \
    "$stack-logger" \
    "$stack-video-cloud"
}

stack_namespaces() {
  printf '%s\n' \
    "$stack-platform" \
    "$stack-secrets" \
    "$stack-account-manager" \
    "$stack-admin" \
    "$stack-frontend" \
    "$stack-logger" \
    "$stack-video-cloud" \
    "$stack-observability" \
    "$stack-ingress"
}

prepare_deployment() {
  local namespace="$1"
  local deployment="$2"
  local claim="$3"
  local node_name="$4"
  local container patch
  kubectl --kubeconfig "$kubeconfig" -n "$namespace" scale "deployment/$deployment" --replicas=1
  container="$(kubectl --kubeconfig "$kubeconfig" -n "$namespace" get "deployment/$deployment" -o jsonpath='{.spec.template.spec.containers[0].name}')"
  patch="$(jq -cn \
    --arg container "$container" \
    --arg claim "$claim" \
    --arg run_id "$run_id" \
    --arg node_name "$node_name" \
    '{spec:{template:{spec:{
      nodeSelector:{"kubernetes.io/hostname":$node_name},
      securityContext:{fsGroup:10001,fsGroupChangePolicy:"OnRootMismatch"},
      volumes:[{name:"runtime-coverage",persistentVolumeClaim:{claimName:$claim}}],
      containers:[{
        name:$container,
        env:[
          {name:"GOCOVERDIR",value:"/coverage"},
          {name:"RUNTIME_COVERAGE_RUN_ID",value:$run_id}
        ],
        volumeMounts:[{name:"runtime-coverage",mountPath:"/coverage"}]
      }]
    }}}}')"
  kubectl --kubeconfig "$kubeconfig" -n "$namespace" patch "deployment/$deployment" --type strategic -p "$patch"
  kubectl --kubeconfig "$kubeconfig" -n "$namespace" rollout status "deployment/$deployment" --timeout=10m
  kubectl --kubeconfig "$kubeconfig" -n "$namespace" exec "deployment/$deployment" -c "$container" -- \
    sh -c 'test "${GOCOVERDIR:-}" = /coverage && test -w "$GOCOVERDIR"'
}

prepare_namespace() {
  local namespace="$1"
  local module claim node_name deployment
  module="$(namespace_module "$namespace")"
  claim="${module}-runtime-coverage"
  node_name="$(
    kubectl --kubeconfig "$kubeconfig" -n "$namespace" get pods \
      -l "rtk.realtek.com/stack=$stack" -o json |
      jq -er '[
        .items[]
        | select(.status.phase == "Running")
        | .spec.nodeName
        | select(length > 0)
      ] | group_by(.) | max_by(length)[0]'
  )"
  kubectl --kubeconfig "$kubeconfig" -n "$namespace" apply -f - <<EOF
apiVersion: v1
kind: PersistentVolumeClaim
metadata:
  name: $claim
  labels:
    rtk-cloud-run-id: $run_id
    rtk-cloud-purpose: runtime-coverage
spec:
  accessModes: [ReadWriteOnce]
  resources:
    requests:
      storage: 1Gi
EOF
  while IFS= read -r deployment; do
    [[ -n "$deployment" ]] || continue
    prepare_deployment "$namespace" "$deployment" "$claim" "$node_name"
  done < <(kubectl --kubeconfig "$kubeconfig" -n "$namespace" get deployment \
    -l "rtk.realtek.com/stack=$stack" -o jsonpath='{range .items[*]}{.metadata.name}{"\n"}{end}')
}

prepare() {
  while IFS= read -r namespace; do
    kubectl --kubeconfig "$kubeconfig" get namespace "$namespace" >/dev/null
    prepare_namespace "$namespace"
  done < <(coverage_namespaces)
  prepare_turn_registrar
}

prepare_turn_registrar() {
  local namespace="$stack-video-cloud"
  local shared_turn_host="${RUNTIME_COVERAGE_SHARED_TURN_HOST:-}"
  local video_image
  [[ -n "$shared_turn_host" ]] || {
    echo "RUNTIME_COVERAGE_SHARED_TURN_HOST is required" >&2
    exit 1
  }
  video_image="$(
    kubectl --kubeconfig "$kubeconfig" -n "$namespace" \
      get deployment video-cloud-api -o jsonpath='{.spec.template.spec.containers[0].image}'
  )"
  [[ -n "$video_image" ]] || {
    echo "video-cloud coverage image is unavailable" >&2
    exit 1
  }
  kubectl --kubeconfig "$kubeconfig" -n "$namespace" apply -f - <<EOF
apiVersion: apps/v1
kind: Deployment
metadata:
  name: runtime-coverage-turnregistrar
  labels:
    app.kubernetes.io/name: runtime-coverage-turnregistrar
    rtk.realtek.com/stack: $stack
    rtk-cloud-run-id: $run_id
    rtk-cloud-purpose: runtime-coverage
spec:
  replicas: 1
  selector:
    matchLabels:
      app.kubernetes.io/name: runtime-coverage-turnregistrar
      rtk-cloud-run-id: $run_id
  template:
    metadata:
      labels:
        app.kubernetes.io/name: runtime-coverage-turnregistrar
        rtk.realtek.com/stack: $stack
        rtk-cloud-run-id: $run_id
        rtk-cloud-purpose: runtime-coverage
    spec:
      securityContext:
        runAsNonRoot: true
        runAsUser: 10001
      containers:
        - name: turnregistrar
          image: $video_image
          imagePullPolicy: IfNotPresent
          command: ["/app/turnregistrar"]
          env:
            - name: VIDEO_CLOUD_TURN_REGISTRY_ADDR
              value: "http://video-cloud-turnregistry.$namespace.svc.cluster.local:18190"
            - name: VIDEO_CLOUD_TURN_REGISTRY_NODE_AUTH_KEY
              valueFrom:
                secretKeyRef:
                  name: video-cloud-workers-runtime
                  key: VIDEO_CLOUD_TURN_REGISTRY_NODE_AUTH_KEY
            - name: VIDEO_CLOUD_TURN_NODE_ID
              value: "runtime-coverage-$run_id"
            - name: VIDEO_CLOUD_TURN_NODE_REGION
              value: "${CLOUD_REGION:-us-sea}"
            - name: VIDEO_CLOUD_TURN_NODE_ZONE
              value: "${CLOUD_REGION:-us-sea}"
            - name: VIDEO_CLOUD_TURN_NODE_PUBLIC_HOST
              value: "$shared_turn_host"
            - name: VIDEO_CLOUD_TURN_NODE_UDP_PORT
              value: "3478"
            - name: VIDEO_CLOUD_TURN_NODE_TCP_PORT
              value: "3478"
            - name: VIDEO_CLOUD_TURN_NODE_TLS_PORT
              value: "0"
            - name: VIDEO_CLOUD_TURN_NODE_MAX_SESSIONS
              value: "6000"
            - name: VIDEO_CLOUD_TURN_NODE_WEIGHT
              value: "100"
            - name: VIDEO_CLOUD_TURN_NODE_SECRET_VERSION
              value: "1"
            - name: VIDEO_CLOUD_TURN_NODE_HEARTBEAT_INTERVAL
              value: "10s"
---
apiVersion: networking.k8s.io/v1
kind: NetworkPolicy
metadata:
  name: allow-runtime-coverage-turnregistrar
  labels:
    rtk.realtek.com/stack: $stack
    rtk-cloud-run-id: $run_id
    rtk-cloud-purpose: runtime-coverage
spec:
  podSelector:
    matchLabels:
      app.kubernetes.io/name: video-cloud-turnregistry
  policyTypes:
    - Ingress
  ingress:
    - from:
        - podSelector:
            matchLabels:
              app.kubernetes.io/name: runtime-coverage-turnregistrar
              rtk-cloud-run-id: $run_id
      ports:
        - protocol: TCP
          port: 18190
EOF
  kubectl --kubeconfig "$kubeconfig" -n "$namespace" \
    rollout status deployment/runtime-coverage-turnregistrar --timeout=5m
  for attempt in {1..30}; do
    if kubectl --kubeconfig "$kubeconfig" -n "$namespace" \
      logs deployment/runtime-coverage-turnregistrar --tail=100 |
      grep -q 'turn registry register succeeded'; then
      return 0
    fi
    sleep 2
  done
  kubectl --kubeconfig "$kubeconfig" -n "$namespace" \
    logs deployment/runtime-coverage-turnregistrar --tail=200 >&2 || true
  echo "run-scoped TURN registrar did not register successfully" >&2
  exit 1
}

collect_claim() {
  local namespace="$1"
  local module="$2"
  local claim="$3"
  local collector="coverage-collector-${claim:0:35}"
  local destination="$output_root/$module/$namespace/$claim"
  kubectl --kubeconfig "$kubeconfig" -n "$namespace" delete pod "$collector" --ignore-not-found=true --wait=true
  kubectl --kubeconfig "$kubeconfig" -n "$namespace" run "$collector" \
    --image=alpine:3.22 --restart=Never --overrides="$(
      printf '{"spec":{"containers":[{"name":"collector","image":"alpine:3.22","command":["sh","-c","sleep 1800"],"volumeMounts":[{"name":"coverage","mountPath":"/coverage"}]}],"volumes":[{"name":"coverage","persistentVolumeClaim":{"claimName":"%s"}}]}}' "$claim"
    )"
  kubectl --kubeconfig "$kubeconfig" -n "$namespace" wait --for=condition=Ready "pod/$collector" --timeout=3m
  mkdir -p "$destination"
  kubectl --kubeconfig "$kubeconfig" -n "$namespace" cp "$collector:/coverage/." "$destination"
  kubectl --kubeconfig "$kubeconfig" -n "$namespace" delete pod "$collector" --wait=true
}

write_anchor() {
  local module="$1"
  local module_path="$2"
  local destination="$output_root/$module/coverage-runtime.json"
  local workspace_commit module_commit generated_at
  workspace_commit="$(git -C "$workspace" rev-parse HEAD)"
  module_commit="$(git -C "$workspace/$module_path" rev-parse HEAD)"
  generated_at="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
  mkdir -p "$(dirname "$destination")"
  jq -n \
    --arg run_id "$run_id" \
    --arg module "$module" \
    --arg workspace_commit "$workspace_commit" \
    --arg module_commit "$module_commit" \
    --arg generated_at "$generated_at" \
    '{schema_version:1,run_id:$run_id,module:$module,workspace_commit:$workspace_commit,module_commit:$module_commit,generated_at:$generated_at}' \
    > "$destination"
}

collect() {
  while IFS= read -r namespace; do
    module="$(namespace_module "$namespace")"
    kubectl --kubeconfig "$kubeconfig" -n "$namespace" scale deployment --all --replicas=0
    for attempt in {1..150}; do
      pod_count="$(kubectl --kubeconfig "$kubeconfig" -n "$namespace" get pod \
        -l "rtk.realtek.com/stack=$stack" -o json |
        jq '[.items[] | select(any(.metadata.ownerReferences[]?; .kind == "ReplicaSet"))] | length')"
      [[ "$pod_count" == "0" ]] && break
      sleep 2
    done
    [[ "$pod_count" == "0" ]] || {
      echo "$namespace still has running pods after graceful scale-down" >&2
      exit 1
    }
    while IFS= read -r claim; do
      [[ -n "$claim" ]] || continue
      collect_claim "$namespace" "$module" "$claim"
    done < <(kubectl --kubeconfig "$kubeconfig" -n "$namespace" get pvc \
      -l "rtk-cloud-run-id=$run_id,rtk-cloud-purpose=runtime-coverage" \
      -o jsonpath='{range .items[*]}{.metadata.name}{"\n"}{end}')
  done < <(coverage_namespaces)

  write_anchor account-manager repos/rtk_account_manager
  write_anchor cloud-admin-backend repos/rtk_cloud_admin
  write_anchor cloud-frontend repos/rtk_cloud_frontend
  write_anchor cloud-logger repos/rtk_cloud_logger
  write_anchor video-cloud repos/rtk_video_cloud

  for module in account-manager cloud-admin-backend cloud-frontend cloud-logger video-cloud; do
    find "$output_root/$module" -type f -name 'covmeta.*' -print -quit | grep -q . || {
      echo "$module runtime coverage has no covmeta files" >&2
      exit 1
    }
    find "$output_root/$module" -type f -name 'covcounters.*' -print -quit | grep -q . || {
      echo "$module runtime coverage has no covcounters files" >&2
      exit 1
    }
  done
}

cleanup() {
  local report_root="$workspace/.artifacts/test-runs/$run_id/coverage"
  local report="$report_root/cleanup-report.json"
  local before after residual_namespaces residual_pvcs residual_pods residual_services
  local retained_targets residual_retained_pvs residual_cloud_volumes deleted_cloud_volumes
  local deleted_cloud_volumes_file
  local cleanup_errors=()
  local status="PASS"
  mkdir -p "$report_root"
  deleted_cloud_volumes_file="$report_root/deleted-cloud-volumes.jsonl"
  : > "$deleted_cloud_volumes_file"
  set +e
  tunnel_stop
  while IFS= read -r namespace; do
    kubectl --kubeconfig "$kubeconfig" delete namespace "$namespace" --ignore-not-found=true --wait=true
    if [[ $? -ne 0 ]]; then
      cleanup_errors+=("failed to delete namespace $namespace")
    fi
  done < <(stack_namespaces)

  residual_namespaces="$(
    while IFS= read -r namespace; do
      kubectl --kubeconfig "$kubeconfig" get namespace "$namespace" -o name 2>/dev/null
    done < <(stack_namespaces)
  )"
  residual_pvcs="$(kubectl --kubeconfig "$kubeconfig" get pvc -A -l "rtk-cloud-run-id=$run_id" -o name 2>/dev/null)"
  residual_pods="$(kubectl --kubeconfig "$kubeconfig" get pod -A -l "rtk-cloud-run-id=$run_id" -o name 2>/dev/null)"
  residual_services="$(kubectl --kubeconfig "$kubeconfig" get service -A -l "rtk-cloud-run-id=$run_id" -o name 2>/dev/null)"
  [[ -z "$residual_namespaces" ]] || cleanup_errors+=("coverage namespaces remain")
  [[ -z "$residual_pvcs" ]] || cleanup_errors+=("coverage PVCs remain")
  [[ -z "$residual_pods" ]] || cleanup_errors+=("coverage or generator pods remain")
  [[ -z "$residual_services" ]] || cleanup_errors+=("coverage LoadBalancer services remain")

  retained_targets="$(
    kubectl --kubeconfig "$kubeconfig" get pv -o json 2>/dev/null |
      jq -r --arg prefix "$stack-" '
        .items[]
        | select((.spec.claimRef.namespace // "") | startswith($prefix))
        | select(.spec.persistentVolumeReclaimPolicy == "Retain")
        | [
            .metadata.name,
            (.spec.csi.volumeHandle // ""),
            (.spec.claimRef.namespace // ""),
            (.spec.claimRef.name // "")
          ]
        | @tsv
      '
  )"
  while IFS=$'\t' read -r pv_name volume_handle claim_namespace claim_name; do
    [[ -n "$pv_name" ]] || continue
    volume_id="${volume_handle%%-*}"
    if [[ ! "$pv_name" =~ ^pvc-[a-f0-9]+$ ]] ||
      [[ ! "$volume_id" =~ ^[0-9]+$ ]] ||
      [[ "$claim_namespace" != "$stack-"* ]]; then
      cleanup_errors+=("refused unsafe retained-volume target $pv_name/$claim_namespace/$volume_handle")
      continue
    fi
    if [[ -z "${LINODE_TOKEN:-}" ]]; then
      cleanup_errors+=("LINODE_TOKEN is required to delete retained coverage volume $volume_id")
      continue
    fi
    http_status="$(
      curl -sS -o /dev/null -w '%{http_code}' -X DELETE \
        -H "Authorization: Bearer $LINODE_TOKEN" \
        "https://api.linode.com/v4/volumes/$volume_id"
    )"
    if [[ "$http_status" != "200" && "$http_status" != "404" ]]; then
      cleanup_errors+=("failed to delete retained coverage volume $volume_id: HTTP $http_status")
      continue
    fi
    if ! kubectl --kubeconfig "$kubeconfig" delete pv "$pv_name" --ignore-not-found=true --wait=true; then
      cleanup_errors+=("failed to delete retained coverage PV $pv_name")
      continue
    fi
    jq -cn \
      --arg volume_id "$volume_id" \
      --arg pv_name "$pv_name" \
      --arg claim_namespace "$claim_namespace" \
      --arg claim_name "$claim_name" \
      '{volume_id:$volume_id,pv_name:$pv_name,claim_namespace:$claim_namespace,claim_name:$claim_name}' \
      >> "$deleted_cloud_volumes_file"
  done <<< "$retained_targets"

  residual_retained_pvs="$(
    kubectl --kubeconfig "$kubeconfig" get pv -o json 2>/dev/null |
      jq -r --arg prefix "$stack-" '
        .items[]
        | select((.spec.claimRef.namespace // "") | startswith($prefix))
        | .metadata.name
      '
  )"
  residual_cloud_volumes=""
  while IFS= read -r volume_id; do
    [[ -n "$volume_id" ]] || continue
    http_status="$(
      curl -sS -o /dev/null -w '%{http_code}' \
        -H "Authorization: Bearer $LINODE_TOKEN" \
        "https://api.linode.com/v4/volumes/$volume_id"
    )"
    if [[ "$http_status" != "404" ]]; then
      residual_cloud_volumes+="${residual_cloud_volumes:+$'\n'}$volume_id"
    fi
  done < <(jq -r '.volume_id' "$deleted_cloud_volumes_file")
  [[ -z "$residual_retained_pvs" ]] || cleanup_errors+=("retained coverage PVs remain")
  [[ -z "$residual_cloud_volumes" ]] || cleanup_errors+=("retained Linode coverage volumes remain")
  deleted_cloud_volumes="$(jq -s '.' "$deleted_cloud_volumes_file")"

  before="$(staging_snapshot_path)"
  after="$workspace/.artifacts/runtime-coverage/$run_id/staging-after.json"
  mkdir -p "$(dirname "$after")"
  if ! read_staging_deployments > "$after"; then
    cleanup_errors+=("failed to read staging deployment state after cleanup")
  elif [[ ! -s "$before" ]]; then
    cleanup_errors+=("staging deployment snapshot from before the run is missing")
  elif ! diff -u <(jq -S '{deployments, image_digests}' "$before") <(jq -S '{deployments, image_digests}' "$after") >/dev/null; then
    cleanup_errors+=("staging deployment UID or image changed during runtime coverage")
  fi

  if ((${#cleanup_errors[@]})); then
    status="FAIL"
  fi
  error_json="$(printf '%s\n' "${cleanup_errors[@]:-}" | jq -Rsc 'split("\n") | map(select(length > 0))')"
  jq -n \
    --arg status "$status" \
    --arg run_id "$run_id" \
    --arg generated_at "$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
    --arg residual_namespaces "$residual_namespaces" \
    --arg residual_pvcs "$residual_pvcs" \
    --arg residual_pods "$residual_pods" \
    --arg residual_services "$residual_services" \
    --arg residual_retained_pvs "$residual_retained_pvs" \
    --arg residual_cloud_volumes "$residual_cloud_volumes" \
    --argjson deleted_cloud_volumes "$deleted_cloud_volumes" \
    --argjson errors "$error_json" \
    '{
      schema_version: 1,
      status: $status,
      run_id: $run_id,
      generated_at: $generated_at,
      staging_workloads_unchanged: ($status == "PASS"),
      residual_namespaces: ($residual_namespaces | split("\n") | map(select(length > 0))),
      residual_pvcs: ($residual_pvcs | split("\n") | map(select(length > 0))),
      residual_pods: ($residual_pods | split("\n") | map(select(length > 0))),
      residual_services: ($residual_services | split("\n") | map(select(length > 0))),
      deleted_cloud_volumes: $deleted_cloud_volumes,
      residual_retained_pvs: ($residual_retained_pvs | split("\n") | map(select(length > 0))),
      residual_cloud_volumes: ($residual_cloud_volumes | split("\n") | map(select(length > 0))),
      errors: $errors
    }' > "$report"
  set -e
  [[ "$status" == "PASS" ]]
}

validate_scope
case "$action" in
  snapshot) snapshot ;;
  verify) verify_deployments ;;
  endpoints) create_feature_endpoints ;;
  tunnel-start) tunnel_start ;;
  tunnel-stop) tunnel_stop ;;
  prepare) prepare ;;
  collect) collect ;;
  cleanup) cleanup ;;
  *) usage; exit 2 ;;
esac
