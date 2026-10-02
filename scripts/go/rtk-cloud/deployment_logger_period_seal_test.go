package main

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoggerPeriodSealCommandPlansWithoutCredentialsAndStartsGuardedJob(t *testing.T) {
	workspace := writeDeploymentFixture(t, "staging", "lke")
	writeTestFile(t, filepath.Join(workspace, "cloud_env", "staging", "storage.env"), "RUNTIME_MEDIA_STORAGE_POLICY=colocated\nRUNTIME_MEDIA_STORAGE_BUCKET=selected-seal-media\nRUNTIME_MEDIA_STORAGE_PREFIX=selected-seal-prefix\n")
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
	for key, value := range map[string]string{
		"LKE_VIDEO_CLOUD_IMAGE":                   "ghcr.io/example/video-cloud@sha256:" + strings.Repeat("a", 64),
		"LKE_LOGGER_SERVICE_REGISTRATION_ENABLED": "true", "LKE_LOGGER_BILLING_FACTS_ENABLED": "true",
		"LKE_LOGGER_PERIOD_SEALS_ENABLED": "true", "LKE_LOGGER_RETENTION_STORAGE_ENABLED": "true",
	} {
		if err := store.write("operator/env/"+key, []byte(value), false); err != nil {
			t.Fatal(err)
		}
		t.Setenv(key, "")
	}
	previousCache, previousCanonical := lkeRuntimeSecretCache, activeCanonicalSecretStore
	lkeRuntimeSecretCache = map[string]string{"logger-producer-seal-token": strings.Repeat("l", 40)}
	activeCanonicalSecretStore = false
	t.Cleanup(func() { lkeRuntimeSecretCache, activeCanonicalSecretStore = previousCache, previousCanonical })
	credentials, applied := 0, []string{}
	failed := ""
	ops := loggerPeriodSealOps{
		credentials: func(environment string) (func(), error) {
			credentials++
			if environment != "staging" || failed == "credentials" {
				return nil, errors.New("credential binding failed")
			}
			return func() {}, nil
		},
		readyLogger: func(env map[string]string) error {
			if env["VIDEO_CLOUD_BLOB_BUCKET"] != "selected-seal-media" || env["VIDEO_CLOUD_BLOB_REGION"] != "us-sea" || env["VIDEO_CLOUD_BLOB_PREFIX"] != "selected-seal-prefix" {
				t.Fatal("monthly close omitted the selected materialized storage projection")
			}
			if failed == "logger" {
				return errors.New("Logger unavailable")
			}
			return nil
		},
		readyRetention: func(map[string]string) error {
			if failed == "retention" {
				return errors.New("retention unavailable")
			}
			return nil
		},
		apply: func(body string) error {
			if failed == "apply" {
				return errors.New("Job write failed")
			}
			applied = append(applied, body)
			return nil
		},
	}
	args := []string{"--workspace", workspace, "--environment", "staging", "--month", "2026-08"}
	if err := runDeploymentLoggerPeriodSealWithOps(args, ops); err != nil || credentials != 0 || len(applied) != 0 {
		t.Fatalf("plan accessed credentials or created Job: %v", err)
	}
	if err := runDeploymentLoggerPeriodSealWithOps(append(args, "--confirm", "video-cloud-dev"), ops); err == nil || credentials != 0 {
		t.Fatal("cross-environment confirmation accepted")
	}
	for _, failure := range []string{"credentials", "logger", "retention", "apply"} {
		failed = failure
		if err := runDeploymentLoggerPeriodSealWithOps(append(args, "--confirm", "video-cloud-staging"), ops); err == nil || len(applied) != 0 {
			t.Fatalf("source-close failure accepted: %s, %v", failure, err)
		}
	}
	failed = ""
	if err := runDeploymentLoggerPeriodSealWithOps(append(args, "--confirm", "video-cloud-staging"), ops); err != nil || len(applied) != 2 {
		t.Fatalf("guarded Job start failed: %v, writes=%d", err, len(applied))
	}
	if !strings.Contains(applied[1], "/app/loggerperiodseal") || !strings.Contains(applied[1], "video-cloud-staging-video-cloud") {
		t.Fatal("wrong source-close runtime")
	}
	if err := runDeploymentLoggerPeriodSeal([]string{"--unknown-option"}); err == nil {
		t.Fatal("unknown operator argument accepted")
	}
}
