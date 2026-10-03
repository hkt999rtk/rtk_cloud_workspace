package main

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestDeploymentStorageRejectsMalformedConfiguredPrefixes(t *testing.T) {
	const runtime = "RUNTIME_MEDIA_STORAGE_POLICY=colocated\nRUNTIME_MEDIA_STORAGE_BUCKET=rtk-cloud-dev-runtime-us-sea\nRUNTIME_MEDIA_STORAGE_PREFIX=environments/video-cloud-dev\nRUNTIME_OTA_STORAGE_MODE=dedicated\nRUNTIME_OTA_STORAGE_POLICY=colocated\nRUNTIME_OTA_STORAGE_BUCKET=rtk-cloud-dev-ota-firmware-us-sea\nRUNTIME_OTA_STORAGE_PREFIX=environments/video-cloud-dev\n"
	const shared = "RELEASE_ARTIFACT_STORAGE_POLICY=shared-cross-region\nRELEASE_ARTIFACT_STORAGE_BUCKET=rtk-cloud-shared-artifacts-us-sea\nRELEASE_ARTIFACT_STORAGE_LOCATION=us-west\nRELEASE_ARTIFACT_STORAGE_REGION=us-sea\nRELEASE_ARTIFACT_STORAGE_PREFIX=releases\n"
	for _, key := range []string{"RUNTIME_MEDIA_STORAGE_PREFIX", "RUNTIME_OTA_STORAGE_PREFIX", "RELEASE_ARTIFACT_STORAGE_PREFIX"} {
		for _, prefix := range []string{"/", "/environments/dev", "environments/dev/", "environments//dev", "environments/../other", "environments/./dev", "bad name", "environments/Dev"} {
			t.Run(key+"/"+prefix, func(t *testing.T) {
				workspace := t.TempDir()
				root := filepath.Join(workspace, "cloud_env", "dev")
				runtimeText, sharedText := runtime, shared
				if strings.HasPrefix(key, "RELEASE") {
					sharedText = strings.Replace(sharedText, key+"=releases", key+"="+prefix, 1)
				} else {
					runtimeText = strings.Replace(runtimeText, key+"=environments/video-cloud-dev", key+"="+prefix, 1)
				}
				writeTestFile(t, filepath.Join(root, "storage.env"), runtimeText)
				writeTestFile(t, filepath.Join(workspace, "cloud_deploy", "storage", "release-artifacts.env"), sharedText)
				_, err := resolveDeploymentStoragePlan(workspace, root, map[string]string{"CLOUD_STACK_NAME": "video-cloud-dev", "DEPLOYMENT_LOCATION": "us-west"}, map[string]string{"LKE_REGION": "us-sea"})
				if err == nil || !strings.Contains(err.Error(), key) {
					t.Fatalf("expected %s validation error, got %v", key, err)
				}
			})
		}
	}
}
