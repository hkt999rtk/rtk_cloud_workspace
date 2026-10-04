package main

import (
	"fmt"

	"rtk-cloud-workspace/scripts/go/internal/storagepolicy"
)

func validateStorageBucketCreation(environment, purpose string, target deploymentStorageTarget) error {
	scope := environment
	if purpose == "artifacts" {
		scope = "shared"
	}
	if err := storagepolicy.Validate(target.Bucket, scope, purpose, target.Region); err != nil {
		return fmt.Errorf("legacy naming exceptions only permit existing buckets; cannot create destination: %w", err)
	}
	return nil
}
