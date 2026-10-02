#!/usr/bin/env bash
set -euo pipefail
workspace="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
monitor_bin_dir="${1:-$workspace/bin}"
mkdir -p "$monitor_bin_dir"
monitor_bin_dir="$(cd "$monitor_bin_dir" && pwd)"
cd "$workspace/scripts/go"
go build -trimpath -o "$monitor_bin_dir/cloud-monitor" ./cloud-monitor
go build -trimpath -o "$monitor_bin_dir/rtk-cloud" ./rtk-cloud
if [[ -d certificate-tools ]]; then
  go build -trimpath -o "$monitor_bin_dir/certificate-tools" ./certificate-tools
else
  printf 'Optional certificate-tools source unavailable; configure an external certificate_tool for complete discovery.\n' >&2
fi
printf 'Monitoring binaries: %s\n' "$monitor_bin_dir"
