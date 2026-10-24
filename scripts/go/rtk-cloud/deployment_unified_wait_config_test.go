package main

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func TestSharedRolloutTimeoutAppliesToAllEnvironmentsWithoutOverrides(t *testing.T) {
	workspace, err := workspaceRoot()
	if err != nil {
		t.Fatal(err)
	}
	shared, err := readStrictEnv(filepath.Join(workspace, "cloud_deploy", "architectures", "kubernetes", "architecture.env"))
	if err != nil || shared[deploymentRolloutTimeoutKey] != "300" || shared["DEPLOYMENT_UPGRADE_WINDOW_SECONDS"] != "3600" {
		t.Fatal("shared per-resource timeout changed the complete update window", err)
	}
	for _, environment := range []string{"dev", "staging", "prod"} {
		t.Run(environment, func(t *testing.T) {
			overrides, err := readOptionalStrictEnv(filepath.Join(workspace, "cloud_env", environment, "overrides", "architecture.env"))
			if err != nil || overrides[deploymentRolloutTimeoutKey] != "" {
				t.Fatal("rollout timeout was copied into an environment-specific override", err)
			}
			cfg, err := resolveDeploymentConfig(workspace, environment, "")
			if err != nil {
				t.Fatal(err)
			}
			timeout, err := environmentRolloutTimeout(cfg.Values)
			if err != nil || timeout != 300*time.Second {
				t.Fatal("environment did not inherit the maintained shared timeout", err)
			}
		})
	}
}

func TestEnvironmentRolloutTimeoutRejectsMissingOrUnboundedPolicy(t *testing.T) {
	for _, raw := range []string{"", "0", "-1", "0300", "300s", "1.5", " 300", "3601", "86401", "9223372036854775808", "private-unapproved-value"} {
		t.Run(raw, func(t *testing.T) {
			values := map[string]string{deploymentRolloutTimeoutKey: raw, "DEPLOYMENT_UPGRADE_WINDOW_SECONDS": "3600"}
			timeout, err := environmentRolloutTimeout(values)
			var failure *deploymentplan.Blocker
			if timeout != 0 || !errors.As(err, &failure) || failure.ID != "deployment.configuration.rollout-timeout" || failure.Resource != deploymentRolloutTimeoutKey || failure.Next == "" || strings.Contains(err.Error(), "private-unapproved-value") {
				t.Fatal("invalid rollout policy inherited a deadline or exposed its input", err)
			}
		})
	}
	for _, window := range []string{"", "0", "invalid"} {
		if _, err := environmentRolloutTimeout(map[string]string{deploymentRolloutTimeoutKey: "300", "DEPLOYMENT_UPGRADE_WINDOW_SECONDS": window}); err == nil {
			t.Fatal("rollout policy lost its enclosing complete update window")
		}
	}
	for _, seconds := range []string{"1", "300", "86400"} {
		if timeout, err := environmentRolloutTimeout(map[string]string{deploymentRolloutTimeoutKey: seconds, "DEPLOYMENT_UPGRADE_WINDOW_SECONDS": "86400"}); err != nil || timeout <= 0 {
			t.Fatal("valid bounded rollout policy was rejected", seconds, err)
		}
	}
}

func TestCompiledDeploymentDoesNotFallbackWhenRolloutPolicyIsMissing(t *testing.T) {
	for _, environment := range []string{"dev", "staging", "prod"} {
		t.Run(environment, func(t *testing.T) {
			profile := "standard"
			if environment == "prod" {
				profile = "production"
			}
			cfg := deploymentConfig{Environment: environment, Adapter: "lke", Values: map[string]string{"DEPLOYMENT_PROTECTION_PROFILE": profile, "DEPLOYMENT_PREFLIGHT_VALIDITY_SECONDS": "3600", "DEPLOYMENT_UPGRADE_WINDOW_SECONDS": "3600"}}
			compiled, err := compileEnvironmentDeploymentWithImages(context.Background(), cfg, "upgrade", environmentRelease{}, nil)
			var failure *deploymentplan.Blocker
			if compiled != nil || !errors.As(err, &failure) || failure.ID != "deployment.configuration.rollout-timeout" {
				t.Fatal("compiler reached credentials or selected an implicit rollout timeout", err)
			}
		})
	}
}
