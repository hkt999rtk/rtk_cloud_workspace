#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
WORKSPACE="${WORKSPACE:-$ROOT_DIR}"

die() { printf 'error: %s\n' "$*" >&2; exit 1; }
need() { command -v "$1" >/dev/null 2>&1 || die "$1 is required"; }

load_env_file() {
  local file="$1"
  if [[ -f "$file" ]]; then
    set -a
    # shellcheck disable=SC1090
    . "$file"
    set +a
  fi
}

load_env_file "$WORKSPACE/.secrets/shared/linode/env/ci-runners.env"
load_env_file "$WORKSPACE/.secrets/shared/github/env/runner-registration.env"

LINODE_API_BASE="${LINODE_API_BASE:-https://api.linode.com/v4}"
GITHUB_API_BASE="${GITHUB_API_BASE:-https://api.github.com}"
REGION="${CI_RUNNER_REGION:-us-sea}"
IMAGE="${CI_RUNNER_IMAGE:-linode/ubuntu24.04}"
PUBLIC_KEY_PATH="${CI_RUNNER_PUBLIC_KEY_PATH:-$HOME/.ssh/id_ed25519_rtkcloud.pub}"
SSH_KEY="${CI_RUNNER_SSH_KEY:-$HOME/.ssh/id_ed25519_rtkcloud}"
GITHUB_WORK_KEY="${CI_RUNNER_GITHUB_WORK_KEY_PATH:-$HOME/.ssh/id_ed25519_github_work}"
SSH_USER="${CI_RUNNER_SSH_USER:-root}"
ALLOWED_SSH_CIDRS="${CI_RUNNER_ALLOWED_SSH_CIDRS:-}"
STATE_DIR="${CI_RUNNER_STATE_DIR:-}"
STATE_DIR="${STATE_DIR:-$WORKSPACE/.secrets/shared/linode/state/ci-runners}"
if [[ "$STATE_DIR" == "$WORKSPACE/.secrets/"* && ! -d "$WORKSPACE/.secrets" ]]; then
  STATE_DIR="$WORKSPACE/.artifacts/linode-ci-runners/state"
fi

source "$ROOT_DIR/scripts/linode-ci-runners/runner-specs.sh"
load_runner_specs

need curl
need jq
need openssl
need ssh
need scp

[[ -n "${LINODE_TOKEN:-}" ]] || die "LINODE_TOKEN is required"
[[ -n "${GITHUB_TOKEN:-}" ]] || die "GITHUB_TOKEN is required"
[[ -n "$ALLOWED_SSH_CIDRS" ]] || die "CI_RUNNER_ALLOWED_SSH_CIDRS is required"
[[ -s "$PUBLIC_KEY_PATH" ]] || die "CI_RUNNER_PUBLIC_KEY_PATH not found: $PUBLIC_KEY_PATH"
[[ -s "$SSH_KEY" ]] || die "CI_RUNNER_SSH_KEY not found: $SSH_KEY"
[[ -s "$GITHUB_WORK_KEY" ]] || die "CI_RUNNER_GITHUB_WORK_KEY_PATH not found: $GITHUB_WORK_KEY"

linode_api() {
  local method="$1" path="$2" data="${3:-}"
  if [[ -n "$data" ]]; then
    curl -fsS -X "$method" "$LINODE_API_BASE$path" \
      -H "Authorization: Bearer $LINODE_TOKEN" \
      -H 'Content-Type: application/json' \
      --data-binary "$data"
  else
    curl -fsS -X "$method" "$LINODE_API_BASE$path" \
      -H "Authorization: Bearer $LINODE_TOKEN" \
      -H 'Content-Type: application/json'
  fi
}

github_api() {
  local method="$1" path="$2" data="${3:-}"
  if [[ -n "$data" ]]; then
    curl -fsS -X "$method" "$GITHUB_API_BASE$path" \
      -H "Authorization: Bearer $GITHUB_TOKEN" \
      -H 'Accept: application/vnd.github+json' \
      -H 'X-GitHub-Api-Version: 2022-11-28' \
      --data-binary "$data"
  else
    curl -fsS -X "$method" "$GITHUB_API_BASE$path" \
      -H "Authorization: Bearer $GITHUB_TOKEN" \
      -H 'Accept: application/vnd.github+json' \
      -H 'X-GitHub-Api-Version: 2022-11-28'
  fi
}

linode_by_label() {
  local label="$1"
  linode_api GET "/linode/instances?page_size=500" | jq -c --arg label "$label" '.data[] | select(.label == $label)' | head -n 1
}

firewall_by_label() {
  local label="$1"
  linode_api GET "/networking/firewalls?page_size=500" | jq -c --arg label "$label" '.data[] | select(.label == $label)' | head -n 1
}

create_runner_vm() {
  local label="$1" type="$2"
  local existing ssh_key root_pass payload created
  existing="$(linode_by_label "$label" || true)"
  if [[ -n "$existing" ]]; then
    printf '%s\n' "$existing"
    return
  fi

  ssh_key="$(cat "$PUBLIC_KEY_PATH")"
  root_pass="$(openssl rand -base64 36)"
  payload="$(jq -cn \
    --arg label "$label" \
    --arg region "$REGION" \
    --arg type "$type" \
    --arg image "$IMAGE" \
    --arg root_pass "$root_pass" \
    --arg ssh_key "$ssh_key" \
    '{label:$label, region:$region, type:$type, image:$image, root_pass:$root_pass, authorized_keys:[$ssh_key], tags:["rtk-cloud-ci","github-runner"]}')"
  printf '[linode-ci] creating %s (%s)\n' "$label" "$type" >&2
  created="$(linode_api POST /linode/instances "$payload")"
  printf '%s\n' "$created"
}

ensure_firewall() {
  local label="$1"
  local linode_id="$2"
  local firewall_label="${label}-firewall"
  local existing firewall_id payload created
  existing="$(firewall_by_label "$firewall_label" || true)"
  if [[ -n "$existing" ]]; then
    firewall_id="$(jq -r '.id' <<<"$existing")"
  else
    payload="$(jq -cn --arg label "$firewall_label" --arg cidrs "$ALLOWED_SSH_CIDRS" '{
      label: $label,
      rules: {
        inbound_policy: "DROP",
        outbound_policy: "ACCEPT",
        inbound: [
          {label:"ssh", action:"ACCEPT", protocol:"TCP", ports:"22", addresses:{ipv4:($cidrs|split(","))}}
        ],
        outbound: []
      }
    }')"
    printf '[linode-ci] creating firewall %s\n' "$firewall_label" >&2
    created="$(linode_api POST /networking/firewalls "$payload")"
    firewall_id="$(jq -r '.id' <<<"$created")"
  fi

  if ! linode_api GET "/networking/firewalls/$firewall_id/devices" | jq -e --argjson id "$linode_id" '.data[] | select(.entity.id == $id)' >/dev/null; then
    linode_api POST "/networking/firewalls/$firewall_id/devices" "$(jq -cn --argjson id "$linode_id" '{id:$id,type:"linode"}')" >/dev/null
  fi
  printf '%s\n' "$firewall_id"
}

wait_for_ssh() {
  local host="$1"
  local label="$2"
  local attempt
  local ssh_opts=(-i "$SSH_KEY" -o BatchMode=yes -o StrictHostKeyChecking=accept-new -o UserKnownHostsFile=/dev/null -o ConnectTimeout=10)
  printf '[linode-ci] waiting for SSH: %s (%s)\n' "$label" "$host" >&2
  for attempt in $(seq 1 60); do
    if ssh "${ssh_opts[@]}" "$SSH_USER@$host" true >/dev/null 2>&1; then
      return 0
    fi
    sleep 10
  done
  die "SSH did not become ready for $label ($host)"
}

runner_registration_token() {
  local repo="$1"
  github_api POST "/repos/$repo/actions/runners/registration-token" | jq -r '.token'
}

bootstrap_runner() {
  local host="$1"
  local runner_name="$2"
  local repo="$3"
  local custom_label="$4"
  local token="$5"
  local ssh_opts=(-i "$SSH_KEY" -o BatchMode=yes -o StrictHostKeyChecking=accept-new -o UserKnownHostsFile=/dev/null)
  printf '[linode-ci] bootstrapping runner %s for %s\n' "$runner_name" "$repo" >&2
  scp "${ssh_opts[@]}" "$GITHUB_WORK_KEY" "$SSH_USER@$host:/tmp/github-work-key" >/dev/null
  ssh "${ssh_opts[@]}" "$SSH_USER@$host" bash -s -- "$runner_name" "$repo" "$custom_label" "$token" "${CI_RUNNER_VERSION:-}" <<'REMOTE'
set -euo pipefail
runner_name="$1"
repo="$2"
custom_label="$3"
token="$4"
runner_version="${5:-}"
runner_user="github-runner"
runner_root="/opt/actions-runner/${runner_name}"

export DEBIAN_FRONTEND=noninteractive
apt-get update -y
apt-get install -y ca-certificates curl jq tar gzip git build-essential pkg-config unzip zip make docker.io nodejs npm golang-go
systemctl enable --now docker
if ! id "$runner_user" >/dev/null 2>&1; then
  useradd -m -s /bin/bash "$runner_user"
fi
usermod -aG docker "$runner_user"
install -d -m 0750 -o "$runner_user" -g "$runner_user" /etc/github-runner
install -m 0600 -o "$runner_user" -g "$runner_user" /tmp/github-work-key /etc/github-runner/github-work
rm -f /tmp/github-work-key
install -d -m 0755 /etc/ssh/ssh_config.d
cat >/etc/ssh/ssh_config.d/github-work.conf <<EOF
Host github.com-work
  HostName github.com
  User git
  IdentityFile /etc/github-runner/github-work
  IdentitiesOnly yes
  StrictHostKeyChecking accept-new
EOF
mkdir -p "$runner_root"
chown "$runner_user:$runner_user" "$runner_root"
if [[ -z "$runner_version" ]]; then
  runner_version="$(curl -fsS https://api.github.com/repos/actions/runner/releases/latest | jq -r '.tag_name | sub("^v"; "")')"
fi
arch="x64"
archive="actions-runner-linux-${arch}-${runner_version}.tar.gz"
url="https://github.com/actions/runner/releases/download/v${runner_version}/${archive}"
if [[ ! -x "$runner_root/config.sh" ]]; then
  tmp="$(mktemp -d)"
  curl -fsSL "$url" -o "$tmp/$archive"
  tar -xzf "$tmp/$archive" -C "$runner_root"
  rm -rf "$tmp"
  chown -R "$runner_user:$runner_user" "$runner_root"
fi
if [[ -f "$runner_root/.runner" ]]; then
  systemctl stop "actions.runner.hkt999rtk-${repo##*/}.${runner_name}.service" >/dev/null 2>&1 || true
  sudo -u "$runner_user" bash -lc "cd '$runner_root' && ./config.sh remove --unattended --token '$token'" >/dev/null 2>&1 || true
fi
repo_url="https://github.com/${repo}"
sudo -u "$runner_user" bash -lc "cd '$runner_root' && ./config.sh --unattended --replace --url '$repo_url' --token '$token' --name '$runner_name' --labels '$custom_label' --work _work"
(cd "$runner_root" && ./svc.sh install "$runner_user")
(cd "$runner_root" && ./svc.sh start)
REMOTE
}

mkdir -p "$STATE_DIR"
chmod 0700 "$STATE_DIR" 2>/dev/null || true

for spec in "${RUNNER_SPECS[@]}"; do
  IFS='|' read -r host_label runner_name repo type custom_label <<<"$spec"
  vm="$(create_runner_vm "$host_label" "$type")"
  linode_id="$(jq -r '.id // .[0].id' <<<"$vm")"
  public_ipv4="$(jq -r '(.ipv4[0] // .[0].ipv4[0])' <<<"$vm")"
  [[ -n "$linode_id" && "$linode_id" != null ]] || die "failed to read Linode id for $host_label"
  [[ -n "$public_ipv4" && "$public_ipv4" != null ]] || die "failed to read public IPv4 for $host_label"
  firewall_id="$(ensure_firewall "$host_label" "$linode_id")"
  wait_for_ssh "$public_ipv4" "$host_label"
  token="$(runner_registration_token "$repo")"
  [[ -n "$token" && "$token" != null ]] || die "failed to get registration token for $repo"
  bootstrap_runner "$public_ipv4" "$runner_name" "$repo" "$custom_label" "$token"
  state_file="$STATE_DIR/$runner_name.json"
  jq -n \
    --arg host_label "$host_label" \
    --arg runner_name "$runner_name" \
    --arg repo "$repo" \
    --arg type "$type" \
    --arg custom_label "$custom_label" \
    --argjson linode_id "$linode_id" \
    --arg public_ipv4 "$public_ipv4" \
    --arg firewall_id "$firewall_id" \
    --arg updated_at "$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
    '{host_label:$host_label, runner_name:$runner_name, repo:$repo, type:$type, labels:["self-hosted","Linux","X64",$custom_label], linode_id:$linode_id, public_ipv4:$public_ipv4, firewall_id:$firewall_id, updated_at:$updated_at}' \
    > "$state_file"
  chmod 0600 "$state_file"
done

printf '[linode-ci] provisioned runner VMs. State dir: %s\n' "$STATE_DIR"
