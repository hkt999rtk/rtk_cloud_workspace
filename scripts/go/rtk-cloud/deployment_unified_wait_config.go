package main

import (
	"strconv"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

const deploymentRolloutTimeoutKey = "DEPLOYMENT_ROLLOUT_TIMEOUT_SECONDS"

// The compiler requires maintained policy. Missing settings never select an
// implicit runtime deadline, and a resource wait cannot exceed the minimum PKI
// validity margin. The command's --timeout remains the overall execution budget.
func environmentRolloutTimeout(values map[string]string) (time.Duration, error) {
	raw := values[deploymentRolloutTimeoutKey]
	seconds, err := strconv.ParseInt(raw, 10, 64)
	window, windowErr := environmentPKIValidityWindow(values)
	if err != nil || raw != strconv.FormatInt(seconds, 10) || seconds <= 0 || seconds > 86400 || windowErr != nil || seconds > window {
		return 0, &deploymentplan.Blocker{ID: "deployment.configuration.rollout-timeout", Resource: deploymentRolloutTimeoutKey, Cause: "a positive rollout timeout up to 86400 seconds within the minimum PKI validity margin is required", Next: "maintain DEPLOYMENT_ROLLOUT_TIMEOUT_SECONDS and DEPLOYMENT_UPGRADE_WINDOW_SECONDS in the shared Kubernetes architecture, then repeat full preflight"}
	}
	return time.Duration(seconds) * time.Second, nil
}
