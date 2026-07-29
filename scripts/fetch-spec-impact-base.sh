#!/usr/bin/env bash
set -euo pipefail

if [ "$#" -ne 1 ] || [ -z "$1" ]; then
  echo "usage: $0 <workspace-base-commit>" >&2
  exit 2
fi

script_dir="$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)"
workspace="$(CDPATH= cd -- "$script_dir/.." && pwd)"
base_ref="$1"

git -C "$workspace" cat-file -e "${base_ref}^{commit}"

while read -r mode object_type object path; do
  if [ "$mode" != "160000" ] || [ "$object_type" != "commit" ]; then
    continue
  fi
  repository="$workspace/$path"
  if [ ! -d "$repository" ]; then
    echo "base submodule checkout is missing: $path" >&2
    exit 1
  fi
  if ! git -C "$repository" cat-file -e "${object}^{commit}" 2>/dev/null; then
    git -C "$repository" fetch --no-tags --depth=1 origin "$object"
  fi
  git -C "$repository" cat-file -e "${object}^{commit}"
done < <(git -C "$workspace" ls-tree -r "$base_ref")
