package main

import (
	"errors"
	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
	"strconv"
	"time"
)

func validityWindowBlocker() error {
	return &deploymentplan.Blocker{ID: "deployment.pki.validity-window", Cause: "an explicit positive bounded minimum PKI validity margin is required", Next: "maintain DEPLOYMENT_UPGRADE_WINDOW_SECONDS in the shared architecture or environment architecture override, then repeat complete preflight; execution PKI checks must also cover the command deadline"}
}

func environmentPKIValidityWindow(values map[string]string) (int64, error) {
	raw := values["DEPLOYMENT_UPGRADE_WINDOW_SECONDS"]
	window, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || raw != strconv.FormatInt(window, 10) || window <= 0 || window > int64((1<<63-1)/time.Second) {
		return 0, validityWindowBlocker()
	}
	return window, nil
}

// Preserve only explicitly sanitized checker diagnostics. Unknown tool/provider
// errors still use a fixed public message; shared health and preflight agree.
func environmentRuntimePKIQualificationError(err error) error {
	const next = "run check-deployment-health.sh for the selected environment; repair its original certificate, trust or runtime configuration through the managed lifecycle, then repeat preflight"
	var summary *deploymentValidationError
	var runtime *deploymentRuntimeError
	if errors.As(err, &summary) || errors.As(err, &runtime) {
		return &deploymentplan.Blocker{ID: "deployment.pki", Cause: err.Error(), Next: next}
	}
	return environmentCompilationError(err, "deployment.pki", "retained runtime PKI is invalid or will not remain valid through the checked rollout window", next)
}
