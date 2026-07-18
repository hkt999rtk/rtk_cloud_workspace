#!/usr/bin/env bash
set -euo pipefail

out_file="${CLOUD_VALIDATION_OUT_DIR:?CLOUD_VALIDATION_OUT_DIR is required}/cloud-evidence.json"
if [[ -n "${CLOUD_VALIDATION_EVIDENCE_PROVIDER_COMMAND:-}" ]]; then
  /bin/bash -lc "$CLOUD_VALIDATION_EVIDENCE_PROVIDER_COMMAND"
else
  "$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)/providers/collect-staging-evidence.sh"
fi
if [[ ! -s "$out_file" ]]; then
  echo "Cloud evidence provider did not create $out_file" >&2
  exit 1
fi
jq -e --arg run_id "$CLOUD_VALIDATION_RUN_ID" --arg platform "$CLOUD_VALIDATION_PLATFORM" '
  .schema_version == 1 and .run_id == $run_id and .platform == $platform and
  (.events | type == "array") and
  all(.events[]; (.scenario_id | type == "string" and length > 0) and
                 (.correlation_id | type == "string" and length > 0) and
                 (.type | type == "string" and length > 0) and
                 (.observed_at | type == "string" and length > 0))
' "$out_file" >/dev/null
