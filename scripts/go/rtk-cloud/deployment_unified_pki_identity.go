package main

import (
	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
	"strconv"
	"time"
)

func validityWindowBlocker() error {
	return &deploymentplan.Blocker{ID: "deployment.pki.validity-window", Cause: "an explicit positive bounded upgrade validity window is required", Next: "maintain DEPLOYMENT_UPGRADE_WINDOW_SECONDS in the shared architecture or environment architecture override; select a window that covers the complete update and acceptance, then repeat complete preflight"}
}

func environmentPKIValidityWindow(values map[string]string) (int64, error) {
	raw := values["DEPLOYMENT_UPGRADE_WINDOW_SECONDS"]
	window, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || raw != strconv.FormatInt(window, 10) || window <= 0 || window > int64((1<<63-1)/time.Second) {
		return 0, validityWindowBlocker()
	}
	return window, nil
}
