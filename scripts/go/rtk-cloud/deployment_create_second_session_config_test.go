package main

import (
	"path/filepath"
	"testing"
)

func TestResolveEnvironmentOptionalBootstrapTTL(t *testing.T) {
	workspace := writeDeploymentFixture(t, "dev", "lke")
	cfg, err := resolveDeploymentConfig(workspace, "dev", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := cfg.Values["PKI_BOOTSTRAP_TTL"]; ok {
		t.Fatal("an unspecified TTL was injected")
	}
	appendFile(t, filepath.Join(workspace, "cloud_env", "dev", "environment.env"), "PKI_BOOTSTRAP_TTL=4h\n")
	cfg, err = resolveDeploymentConfig(workspace, "dev", "")
	if err != nil || cfg.Values["PKI_BOOTSTRAP_TTL"] != "4h" {
		t.Fatalf("configured TTL was not preserved: %v", err)
	}
}
