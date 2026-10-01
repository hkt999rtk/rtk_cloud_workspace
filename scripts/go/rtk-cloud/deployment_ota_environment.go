package main

import "errors"

// Narrow OTA updates are also steps in the reviewed persistent staging release.
// Protected-environment qualification and CI image provenance remain release
// gates; each command additionally checks its selected stack and live inputs.
func requireTargetedOTAEnvironment(cfg deploymentConfig) error {
	if cfg.Adapter != "lke" || (cfg.Environment != "dev" && cfg.Environment != "staging") {
		return errors.New("targeted OTA update requires an existing dev or reviewed staging LKE stack")
	}
	return nil
}
