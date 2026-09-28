package main

import (
	"strings"
	"testing"
)

func TestDeploymentPKIStoragePlanUsesTrackedEnvironmentPlans(t *testing.T) {
	root, err := workspaceRoot()
	if err != nil {
		t.Fatal(err)
	}
	if err := runDeploymentWithOperations([]string{"pki-storage-plan", "--workspace", root, "--environment", "dev"}, deploymentOperations{}); err != nil {
		t.Fatalf("active dev plan: %v", err)
	}
	if err := runDeploymentPKIStoragePlan([]string{"--workspace", root, "--environment", "staging", "--render"}); err != nil {
		t.Fatalf("review-only staging PVC render: %v", err)
	}
}

func TestDeploymentPKIStoragePlanRejectsInvalidReviewModes(t *testing.T) {
	root, err := workspaceRoot()
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, want string
		args       []string
	}{
		{"missing environment", "--environment is required", nil},
		{"extra argument", "positional arguments are not accepted", []string{"--environment", "dev", "extra"}},
		{"unknown flag", "flag provided but not defined", []string{"--unknown"}},
		{"conflicting render and live", "PKI storage plan:", []string{"--workspace", root, "--environment", "dev", "--render", "--live"}},
		{"cleanup outside dev", "PKI storage plan:", []string{"--workspace", root, "--environment", "prod", "--cleanup-audit"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := runDeploymentPKIStoragePlan(tc.args); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v; want %q", err, tc.want)
			}
		})
	}
}
