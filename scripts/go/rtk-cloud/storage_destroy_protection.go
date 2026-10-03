package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"rtk-cloud-workspace/scripts/go/internal/storagepolicy"
)

func validateDestroyRegisteredStorage(env map[string]string, bucket, region string) error {
	for _, scope := range []string{env["CLOUD_ENV_NAME"], "shared"} {
		for _, purpose := range []string{"artifacts", "backup", "pki-backup", "reports"} {
			name, err := storagepolicy.Bucket(scope, purpose, region)
			if err == nil && name == bucket {
				return fmt.Errorf("runtime bucket %s is registered %s storage and cannot be destroyed", bucket, purpose)
			}
		}
	}
	return nil
}

// Backup destinations are configured in JSON, not necessarily in stack.env.
// Inspect the selected environment's maintained configuration before permitting
// bucket deletion, including legacy destination names.
func validateDestroyBackupConfigurations(workspace, envRoot string, env map[string]string) error {
	root := filepath.Join(workspace, "cloud_env", env["CLOUD_ENV_NAME"])
	if filepath.Base(envRoot) == "runtime" {
		root = filepath.Dir(envRoot)
	}
	for _, name := range []string{"backup.json", "postgres-backup.json"} {
		body, err := os.ReadFile(filepath.Join(root, name))
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return fmt.Errorf("cannot inspect retained storage in %s", name)
		}
		type remote struct{ Bucket, Region string }
		var config struct {
			Environment, Stack string
			Remote             remote
			Worker             struct{ Remote remote }
		}
		if json.Unmarshal(body, &config) != nil || config.Environment != env["CLOUD_ENV_NAME"] || config.Stack != env["CLOUD_STACK_NAME"] {
			return fmt.Errorf("retained storage config %s is invalid or belongs to another environment", name)
		}
		destination := config.Remote
		if name == "postgres-backup.json" {
			destination = config.Worker.Remote
		}
		if destination.Bucket == "" || destination.Region == "" {
			return fmt.Errorf("retained storage config %s has no complete destination identity", name)
		}
		for _, prefix := range []string{"VIDEO_CLOUD_BLOB_", "VIDEO_CLOUD_OTA_BLOB_"} {
			// Backup region can be an S3 signing region rather than the provider
			// region. Never use that mismatch to permit deleting a retained name.
			if env[prefix+"BUCKET"] == destination.Bucket {
				return fmt.Errorf("runtime storage overlaps retained backup destination in %s; refusing bucket destruction", name)
			}
		}
	}
	return nil
}
