#!/usr/bin/env bash
set -euo pipefail

env_file="${HOME100K_SECRET_ENV_FILE:-$HOME/.env}"
prefix="${HOME100K_CLEANUP_PREFIX:-home-100k}"
endpoint="${LINODE_API_ENDPOINT:-https://api.linode.com/v4}"
yes=0

usage() {
  cat <<EOF
usage: $(basename "$0") [--yes] [--prefix home-100k]

Deletes Linode VMs whose label starts with the prefix or whose tags include the
prefix. Dry-run is the default; pass --yes to delete.

Secrets:
  Reads only LINODE_TOKEN from HOME100K_SECRET_ENV_FILE, default ~/.env.
EOF
}

load_linode_token_from_env_file() {
  local line value
  if [[ -n "${LINODE_TOKEN:-}" || ! -f "$env_file" ]]; then
    return
  fi
  while IFS= read -r line; do
    case "$line" in
      LINODE_TOKEN=*|export\ LINODE_TOKEN=*)
        value="${line#export LINODE_TOKEN=}"
        value="${value#LINODE_TOKEN=}"
        value="${value%$'\r'}"
        value="${value%\"}"
        value="${value#\"}"
        value="${value%\'}"
        value="${value#\'}"
        export LINODE_TOKEN="$value"
        return
        ;;
    esac
  done < "$env_file"
}

while [[ "$#" -gt 0 ]]; do
  case "$1" in
    --yes)
      yes=1
      shift
      ;;
    --prefix)
      prefix="${2:?--prefix requires a value}"
      shift 2
      ;;
    -h|--help)
      usage
      exit 0
      ;;
    *)
      echo "unknown argument: $1" >&2
      usage >&2
      exit 2
      ;;
  esac
done

load_linode_token_from_env_file
if [[ -z "${LINODE_TOKEN:-}" ]]; then
  echo "LINODE_TOKEN is required in environment or $env_file" >&2
  exit 2
fi

tmpdir="$(mktemp -d)"
trap 'rm -rf "$tmpdir"' EXIT

page=1
matches_jsonl="$tmpdir/matches.jsonl"
: > "$matches_jsonl"

while true; do
  page_file="$tmpdir/page-${page}.json"
  curl -fsS \
    -H "Authorization: Bearer ${LINODE_TOKEN}" \
    -H "Accept: application/json" \
    "${endpoint%/}/linode/instances?page=${page}&page_size=100" \
    -o "$page_file"

  python3 - "$page_file" "$prefix" "$matches_jsonl" <<'PY'
import json
import sys

page_file, prefix, out_file = sys.argv[1], sys.argv[2], sys.argv[3]
with open(page_file) as f:
    data = json.load(f)
with open(out_file, "a") as out:
    for item in data.get("data", []):
        label = item.get("label", "")
        tags = item.get("tags") or []
        if label.startswith(prefix) or prefix in tags:
            out.write(json.dumps({
                "id": item.get("id"),
                "label": label,
                "region": item.get("region"),
                "status": item.get("status"),
                "ipv4": item.get("ipv4") or [],
                "tags": tags,
            }) + "\n")
PY
  pages="$(python3 -c 'import json,sys; print(json.load(open(sys.argv[1])).get("pages", 1))' "$page_file")"
  if [[ "$page" -ge "$pages" ]]; then
    break
  fi
  page=$((page + 1))
done

count="$(wc -l < "$matches_jsonl" | tr -d ' ')"
echo "matched ${count} Linode VM(s) for prefix/tag '${prefix}'"

if [[ "$count" == "0" ]]; then
  exit 0
fi

python3 - "$matches_jsonl" <<'PY'
import json
import sys

print("id\tlabel\tregion\tstatus\tipv4\ttags")
with open(sys.argv[1]) as f:
    for line in f:
        item = json.loads(line)
        print(f"{item['id']}\t{item['label']}\t{item['region']}\t{item['status']}\t{','.join(item['ipv4'])}\t{','.join(item['tags'])}")
PY

if [[ "$yes" != "1" ]]; then
  echo "dry-run only; pass --yes to delete these VMs"
  exit 0
fi

python3 - "$matches_jsonl" <<'PY' > "$tmpdir/ids.txt"
import json
import sys
with open(sys.argv[1]) as f:
    for line in f:
        item = json.loads(line)
        print(item["id"])
PY

while IFS= read -r id; do
  [[ -z "$id" ]] && continue
  echo "deleting Linode VM id=${id}"
  curl -fsS -X DELETE \
    -H "Authorization: Bearer ${LINODE_TOKEN}" \
    -H "Accept: application/json" \
    "${endpoint%/}/linode/instances/${id}" \
    >/dev/null
done < "$tmpdir/ids.txt"

echo "delete requests completed; re-run without --yes to verify count=0"
