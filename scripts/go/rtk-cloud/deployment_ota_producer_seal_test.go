package main

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

func TestOTAProducerCommandPlansAndChecksBeforeAnyJobMutation(t *testing.T) {
	workspace := writeDeploymentFixture(t, "staging", "lke")
	writeTestFile(t, filepath.Join(workspace, "cloud_env", "staging", "storage.env"), "RUNTIME_MEDIA_STORAGE_POLICY=colocated\nRUNTIME_MEDIA_STORAGE_BUCKET=selected-ota-media\nRUNTIME_MEDIA_STORAGE_PREFIX=ota-private\n")
	configRoot := t.TempDir()
	t.Setenv("RTK_CLOUD_CONFIG_ROOT", configRoot)
	store, err := newSecretStore(configRoot, "staging")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.ensureLayout(); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, filepath.Join(store.Root, "env", "stack.env"), "CLOUD_ENV_NAME=staging\nCLOUD_PROVIDER=lke\nCLOUD_STACK_NAME=video-cloud-staging\nCLOUD_DNS_ROOT_DOMAIN=example.test\nCLOUD_REGION=us-sea\n")
	image := "ghcr.io/example/video@sha256:" + strings.Repeat("a", 64)
	for key, value := range map[string]string{"LKE_VIDEO_CLOUD_IMAGE": image, "LKE_OTA_SERVICE_REGISTRATION_ENABLED": "true", "LKE_ACCOUNT_MANAGER_SERVICE_REGISTRATION_ENABLED": "true", "LKE_OTA_PRODUCER_SEAL_FIRST_MONTH": "2026-08"} {
		if err := store.write("operator/env/"+key, []byte(value), false); err != nil {
			t.Fatal(err)
		}
	}
	// A stale process override must not replace the selected environment pin.
	t.Setenv("LKE_VIDEO_CLOUD_IMAGE", "wrong.example/unrelated:mutable")
	previousCache, previousCanonical := lkeRuntimeSecretCache, activeCanonicalSecretStore
	lkeRuntimeSecretCache = map[string]string{"ota-producer-seal-token": strings.Repeat("p", 40)}
	activeCanonicalSecretStore = false
	t.Cleanup(func() { lkeRuntimeSecretCache = previousCache; activeCanonicalSecretStore = previousCanonical })
	credentials, applied := 0, []string{}
	failure := ""
	maintenance := false
	ops := otaProducerSealOps{
		credentials: func(environment string) (func(), error) {
			credentials++
			if environment != "staging" || failure == "credentials" {
				return nil, errors.New("credentials unavailable")
			}
			return func() {}, nil
		},
		identity: func(env map[string]string, allowPending bool) error {
			if allowPending != maintenance || lkeVideoCloudImage(env) != image || env["VIDEO_CLOUD_BLOB_BUCKET"] != "selected-ota-media" {
				t.Fatal("wrong owner/image/storage selected")
			}
			if failure == "identity" {
				return errors.New("identity unavailable")
			}
			return nil
		},
		ready: func(map[string]string) error {
			if failure == "ready" {
				return errors.New("OTA source unavailable")
			}
			return nil
		},
		apply: func(body string) error {
			if failure == "apply" {
				return errors.New("write failed")
			}
			applied = append(applied, body)
			return nil
		},
	}
	args := []string{"--workspace", workspace, "--environment", "staging", "--month", "2026-08"}
	if err := runDeploymentOTAProducerSealWithOps(args, ops); err != nil || credentials != 0 || len(applied) != 0 {
		t.Fatalf("plan had side effects: %v", err)
	}
	if err := runDeploymentOTAProducerSealWithOps(append(args, "--confirm", "video-cloud-dev"), ops); err == nil || credentials != 0 {
		t.Fatal("wrong environment confirmed")
	}
	for _, failed := range []string{"credentials", "identity", "ready", "apply"} {
		failure = failed
		if err := runDeploymentOTAProducerSealWithOps(append(args, "--confirm", "video-cloud-staging"), ops); err == nil || len(applied) != 0 {
			t.Fatalf("failure %s mutated Job: %v", failed, err)
		}
	}
	failure = ""
	if err := runDeploymentOTAProducerSealWithOps(append(args, "--confirm", "video-cloud-staging"), ops); err != nil || len(applied) != 6 {
		t.Fatalf("guarded close failed: %v writes=%d", err, len(applied))
	}
	if !strings.Contains(applied[5], "2026-08") || strings.Contains(applied[5], "--maintain-identity") {
		t.Fatal("wrong close Job")
	}
	applied = nil
	maintenance = true
	lkeRuntimeSecretCache = map[string]string{}
	maintainArgs := []string{"--workspace", workspace, "--environment", "staging", "--maintain-identity", "--confirm", "video-cloud-staging"}
	if err := runDeploymentOTAProducerSealWithOps(maintainArgs, ops); err != nil || len(applied) != 4 {
		t.Fatalf("identity maintenance depended on a billing token: %v", err)
	}
	if !strings.Contains(applied[3], "--maintain-identity") || strings.Contains(applied[3], "VIDEO_CLOUD_BILLING_USAGE_TOKEN") {
		t.Fatal("maintenance can close/bill a period")
	}
}
