package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestProvisionSummaryUsesPrivateEnvironmentLocationAcrossCheckouts(t *testing.T) {
	for _, environment := range []string{"dev", "staging", "prod"} {
		t.Run(environment, func(t *testing.T) {
			store := makeIsolatedTestSecretStore(t, environment)
			values := map[string]string{"CLOUD_ENV_NAME": environment, "CLOUD_STACK_NAME": "video-cloud-" + environment, "CLOUD_REGION": "us-sea", "VIDEO_CLOUD_DOMAIN": "video-cloud-" + environment + ".example.test"}
			for i := 0; i < 2; i++ {
				checkout := t.TempDir()
				arbitrary := filepath.Join(t.TempDir(), "must-not-be-used")
				paths := provisionPaths{EnvRoot: checkout, ArtifactsDir: arbitrary}
				writeTestFile(t, filepath.Join(checkout, "env", "stack.env"), "CLOUD_ENV_NAME="+environment+"\n")
				before := readTestFile(t, filepath.Join(checkout, "env", "stack.env"))
				dir, err := writeLKEProvisionArtifacts(paths, values)
				if err != nil || !strings.HasPrefix(dir, filepath.Join(store.Root, "deployment", "artifacts", "lke-provision")+string(filepath.Separator)) {
					t.Fatal("summary placement depends on checkout or override", dir, err)
				}
				file := filepath.Join(dir, "summary.json")
				var summary map[string]any
				if err := json.Unmarshal([]byte(readTestFile(t, file)), &summary); err != nil || summary["stack"] != values["CLOUD_STACK_NAME"] || summary["provider"] != "lke" {
					t.Fatal("summary lost the selected environment", err)
				}
				if info, err := os.Lstat(file); err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 {
					t.Fatal("summary is not a private regular file", err)
				}
				for current := dir; current != filepath.Dir(store.ConfigRoot); current = filepath.Dir(current) {
					if info, err := os.Lstat(current); err != nil || !info.IsDir() || info.Mode().Perm() != 0700 {
						t.Fatal("summary directory is not private", current, err)
					}
				}
				for _, forbidden := range []string{arbitrary, filepath.Join(checkout, "artifacts")} {
					if _, err := os.Lstat(forbidden); !os.IsNotExist(err) {
						t.Fatal("summary writer created checkout or arbitrary output", forbidden)
					}
				}
				if readTestFile(t, filepath.Join(checkout, "env", "stack.env")) != before {
					t.Fatal("operational summary modified configuration")
				}
			}
		})
	}
}

func TestProvisionSummaryRejectsMissingEnvironmentAndUnsafePrivateParents(t *testing.T) {
	store := makeIsolatedTestSecretStore(t, "dev")
	paths := provisionPaths{EnvRoot: t.TempDir()}
	if _, err := writeLKEProvisionArtifacts(paths, map[string]string{}); err == nil {
		t.Fatal("summary writer implicitly selected staging")
	}
	if err := os.Symlink(t.TempDir(), filepath.Join(store.Root, "deployment")); err != nil {
		t.Fatal(err)
	}
	if _, err := writeLKEProvisionArtifacts(paths, map[string]string{"CLOUD_ENV_NAME": "dev", "CLOUD_STACK_NAME": "video-cloud-dev"}); err == nil {
		t.Fatal("summary writer followed a private-store symlink")
	}
}
