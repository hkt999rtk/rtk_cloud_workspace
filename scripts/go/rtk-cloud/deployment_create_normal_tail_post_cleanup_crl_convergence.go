package main

import (
	"context"
	"encoding/json"
	"fmt"
	"time"
)

// Native CRL consumers poll asynchronously. Wait for their real installation
// and registry ACKs before running final health, using fresh reads each time.
func verifyEnvironmentPostCleanupCRLConsumers(ctx context.Context, cfg deploymentConfig, store secretStore, backend environmentKubernetesBackend) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	return waitPostCleanupCRLConsumers(ctx, 10*time.Second, func(ctx context.Context) error {
		runtime := newFrozenDeploymentCheckRuntime(ctx, cfg.Environment, backend)
		namespace := cfg.Values["CLOUD_STACK_NAME"] + "-video-cloud"
		kubeconfig := store.KubeconfigPath()
		raw, err := secretCheckKubectl([]*deploymentCheckRuntime{runtime}, false, "--kubeconfig", kubeconfig, "-n", namespace, "get", "deployments", "-o", "json")
		if err != nil {
			return secretCheckFailure(err, "cannot observe corrected CRL consumers")
		}
		var deployments liveDeploymentList
		if json.Unmarshal(raw, &deployments) != nil {
			return postCleanupCorrectionBlock("corrected CRL inventory is invalid")
		}
		now := time.Now().UTC()
		for _, target := range []string{"video-cloud-api-pki", "mqtt-pki", "video-cloud-logingester"} {
			validUntil := now
			if target == "video-cloud-api-pki" {
				// The fixed Root worker refreshes at 24h remaining. Prove that
				// its refresh completed, not merely that the old CRL still lives.
				validUntil = now.Add(24 * time.Hour)
			}
			if err := verifyMountedPKICRLManifestsAt(kubeconfig, namespace, cfg.Environment, deployments, target, validUntil, runtime); err != nil {
				return err
			}
		}
		return nil
	})
}

func waitPostCleanupCRLConsumers(ctx context.Context, interval time.Duration, check func(context.Context) error) error {
	var last error
	for {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("corrected CRL consumers did not converge: %w (last observation: %v)", err, last)
		}
		if last = check(ctx); last == nil {
			return nil
		}
		timer := time.NewTimer(interval)
		select {
		case <-ctx.Done():
			timer.Stop()
		case <-timer.C:
		}
	}
}
