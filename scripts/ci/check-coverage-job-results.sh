#!/usr/bin/env bash
set -euo pipefail

# Artifact availability cannot replace a selected integration job succeeding.
# Run this before aggregate downloads or submodule initialization.
jq -e '
  . as $needs
  | if .changes.result != "success" then
      error("coverage selector did not succeed")
    else . end
  | .changes.outputs.run_coverage as $coverage
  | if ($coverage != "true" and $coverage != "false") then
      error("missing or invalid run_coverage selector")
    else . end
  | [
      ["policy", "policy"],
      ["run_unit", "unit"],
      ["run_node", "node"],
      ["account_manager_postgres", "account-manager-postgres"],
      ["billing_postgres", "billing-postgres"],
      ["video_cloud_postgres_emqx", "video-cloud-postgres-emqx"]
    ]
  | all(.[];
      .[0] as $flag | .[1] as $job
      | $needs.changes.outputs[$flag] as $selected
      | $needs[$job].result as $result
      | if ($selected != "true" and $selected != "false") then
          error("missing or invalid coverage selector: " + $flag)
        elif (["success", "failure", "cancelled", "skipped"] | index($result)) == null then
          error("missing or invalid coverage job result: " + $job)
        elif $selected == "true" and $result != "success" then
          error("selected coverage job did not succeed: " + $job + " (" + $result + ")")
        else true end)
' <<< "${NEEDS_JSON:?NEEDS_JSON is required}" >/dev/null
