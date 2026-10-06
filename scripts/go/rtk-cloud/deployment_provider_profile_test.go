package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestProviderProfileUsesSamePrivatePathAcrossCheckoutsAndEnvironments(t *testing.T) {
	for _, environment := range []string{"dev", "staging", "prod"} {
		t.Run(environment, func(t *testing.T) {
			store := makeIsolatedTestSecretStore(t, environment)
			workspace := writeDeploymentFixture(t, environment, "lke")
			cfg, err := resolveDeploymentConfig(workspace, environment, "")
			if err != nil {
				t.Fatal(err)
			}
			legacy := filepath.Join(cfg.RuntimeRoot, "state", "provider-preflight.env")
			writeTestFile(t, legacy, "original-legacy-metadata\n")
			if err := materializeDeploymentRuntime(cfg); err != nil {
				t.Fatal(err)
			}
			path, err := deploymentProviderProfilePath(environment)
			if err != nil || path != filepath.Join(store.Root, "deployment", "provider-preflight.env") {
				t.Fatal("provider producer escaped selected private store", path, err)
			}
			if readTestFile(t, legacy) != "original-legacy-metadata\n" {
				t.Fatal("configuration materializer overwrote legacy operational state")
			}
			for _, directory := range []string{store.Root, filepath.Dir(path)} {
				info, err := os.Lstat(directory)
				if err != nil || info.Mode().Perm() != 0700 {
					t.Fatal("provider metadata directory is not private", err)
				}
			}
			info, err := os.Lstat(path)
			if err != nil || info.Mode().Perm() != 0600 || !strings.Contains(readTestFile(t, path), "PROVIDER_REGION=us-sea") {
				t.Fatal("normalized provider metadata is missing or public", err)
			}
			// Every other acceptance prerequisite is missing; inspect only the
			// profile row. Changing checkout must not change its authority.
			cfg.RuntimeRoot = t.TempDir()
			var out bytes.Buffer
			reporter := &deploymentPreflightReporter{out: &out}
			validateAcceptanceRuntime(cfg, reporter)
			if !strings.Contains(out.String(), "SecretStore/deployment/provider-preflight.env is available") {
				t.Fatal("acceptance did not use producer's canonical path", out.String())
			}
			if err := os.Remove(path); err != nil {
				t.Fatal(err)
			}
			cfg.RuntimeRoot = filepath.Dir(filepath.Dir(legacy))
			out.Reset()
			reporter = &deploymentPreflightReporter{out: &out}
			validateAcceptanceRuntime(cfg, reporter)
			if strings.Contains(out.String(), "provider-preflight.env is available") {
				t.Fatal("missing canonical metadata fell back to legacy checkout state")
			}
		})
	}
}

func TestProviderProfileRefusesSymlinksInsecurePathsAndNonMetadata(t *testing.T) {
	for _, mode := range []string{"public-directory", "public-file", "symlink-directory", "symlink-file", "token", "newline", "invalid-quota"} {
		t.Run(mode, func(t *testing.T) {
			store := makeIsolatedTestSecretStore(t, "dev")
			values := map[string]string{"PROVIDER_REGION": "us-sea"}
			if err := writeDeploymentProviderProfile("dev", values); err != nil {
				t.Fatal(err)
			}
			path, _ := deploymentProviderProfilePath("dev")
			switch mode {
			case "public-directory":
				os.Chmod(filepath.Dir(path), 0755)
			case "public-file":
				os.Chmod(path, 0644)
			case "symlink-directory":
				os.Remove(path)
				os.Remove(filepath.Dir(path))
				os.Symlink(t.TempDir(), filepath.Dir(path))
			case "symlink-file":
				os.Remove(path)
				os.Symlink(filepath.Join(t.TempDir(), "untouched"), path)
			case "token":
				values["LINODE_TOKEN"] = "must-not-be-published"
			case "newline":
				values["PROVIDER_REGION"] = "us-sea\nGO=true"
			case "invalid-quota":
				values["PROVIDER_ACTIVE_SERVICE_LIMIT"] = "0"
			}
			if err := writeDeploymentProviderProfile("dev", values); err == nil || strings.Contains(err.Error(), "must-not-be-published") {
				t.Fatal("unsafe private profile accepted or value leaked", err)
			}
			if _, err := os.Stat(filepath.Join(store.Root, "state", "provider-preflight.env")); !os.IsNotExist(err) {
				t.Fatal("writer invented another provider metadata location")
			}
		})
	}
}
