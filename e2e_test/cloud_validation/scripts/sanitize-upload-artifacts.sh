#!/usr/bin/env bash
set -euo pipefail

root="${1:?artifact root is required}"
test -d "$root" || exit 0
failure_file="$root/redaction-failures.txt"
rm -f "$failure_file"
found=0
while IFS= read -r -d '' file; do
  if LC_ALL=C grep -Eiq -- '-----BEGIN (RSA |EC |OPENSSH )?PRIVATE KEY-----|Authorization:[[:space:]]*Bearer[[:space:]]+[^[:space:]]+|"(access_token|refresh_token|claim_token|password|private_key(_pem)?)"[[:space:]]*:[[:space:]]*"[^"[:space:]]+' "$file"; then
    printf '%s\n' "${file#$root/}" >> "$failure_file"
    rm -f -- "$file"
    found=1
  fi
done < <(find "$root" -type f \( -name '*.json' -o -name '*.xml' -o -name '*.md' -o -name '*.log' -o -name '*.txt' -o -name '*.crash' -o -name '*.ips' \) -print0)
if [[ "$found" == "1" ]]; then
  echo "secret-bearing artifacts were removed; see redaction-failures.txt" >&2
  exit 1
fi
