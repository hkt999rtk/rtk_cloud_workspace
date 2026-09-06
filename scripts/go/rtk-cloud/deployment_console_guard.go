package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// A restored runtime is evidence of the old deployment, not desired configuration.
// Fail before provider operations; never silently overwrite operator settings.
func validateConsoleRuntimeConfig(runtimeRoot string, actual map[string]string, opts provisionOptions) error {
	path := filepath.Join(filepath.Dir(runtimeRoot), "environment.env")
	desired, err := readStrictEnv(path)
	if os.IsNotExist(err) {
		// Custom/legacy env roots have no declarative environment contract.
		return nil
	}
	if err != nil {
		return fmt.Errorf("read desired Console configuration: %w", err)
	}
	return compareConsoleRuntimeConfig(desired, actual, opts)
}

func compareConsoleRuntimeConfig(desired, actual map[string]string, opts provisionOptions) error {
	keys := []string{}
	if lkeWorkloadSelected(actual, opts, "account-manager") {
		keys = append(keys, "AUTH_TOKEN_BASE_URL", "SOCIAL_LOGIN_CALLBACK_URL", "GOOGLE_LOGIN_ENABLED", "GOOGLE_OAUTH_CLIENT_ID", "GITHUB_LOGIN_ENABLED", "GITHUB_OAUTH_CLIENT_ID", "CHIPSET_PROVIDER_ALLOWED_HOSTS")
	}
	if lkeWorkloadSelected(actual, opts, "account-manager") || lkeWorkloadSelected(actual, opts, "cloud-admin") || lkeWorkloadSelected(actual, opts, "video-cloud") {
		keys = append(keys, "TEST_LAB_ENABLED")
	}
	var changed []string
	for _, key := range keys {
		if want, declared := desired[key]; declared && strings.TrimSpace(want) != strings.TrimSpace(lkeEnvValue(actual, key)) {
			changed = append(changed, key)
		}
	}
	if len(changed) > 0 {
		sort.Strings(changed)
		return fmt.Errorf("stale or conflicting Console runtime settings: %s; reconcile environment.env and selected-environment overrides, then regenerate runtime with deployment plan before deploying (values suppressed)", strings.Join(changed, ", "))
	}
	return nil
}

func validateLKEConsoleDeployInputs(env map[string]string, opts provisionOptions, secret func(string) string) error {
	am := lkeWorkloadSelected(env, opts, "account-manager")
	admin := lkeWorkloadSelected(env, opts, "cloud-admin")
	if (am || admin) && strings.TrimSpace(secret("job-authorization-token")) == "" {
		return fmt.Errorf("selected Console workload requires environment-local runtime/job-authorization-token; repair deployment-managed bootstrap/binding, do not borrow credentials")
	}
	if am {
		enabled := false
		for _, provider := range []string{"GOOGLE", "GITHUB"} {
			if !strings.EqualFold(lkeEnvValue(env, provider+"_LOGIN_ENABLED"), "true") {
				continue
			}
			enabled = true
			if strings.TrimSpace(lkeEnvValue(env, provider+"_OAUTH_CLIENT_ID")) == "" || strings.TrimSpace(secret(strings.ToLower(provider)+"-oauth-client-secret")) == "" {
				return fmt.Errorf("%s login is enabled but its environment-local client ID or client secret is missing (values suppressed)", provider)
			}
		}
		if enabled {
			if len(strings.TrimSpace(secret("social-oauth-state-secret"))) < 32 {
				return fmt.Errorf("enabled social login requires environment-local social-oauth-state-secret of at least 32 characters")
			}
			domain := strings.TrimSpace(env["CLOUD_ADMIN_DOMAIN"])
			if domain == "" || lkeEnvValue(env, "SOCIAL_LOGIN_CALLBACK_URL") != "https://"+domain+"/api/auth/social/callback" {
				return fmt.Errorf("SOCIAL_LOGIN_CALLBACK_URL must match the selected Cloud Admin HTTPS origin and /api/auth/social/callback")
			}
		}
	}
	if (am || admin || lkeWorkloadSelected(env, opts, "video-cloud")) && strings.EqualFold(lkeEnvValue(env, "TEST_LAB_ENABLED"), "true") && !lkeTestLabEnabled(env) {
		return fmt.Errorf("requested Test Lab cannot be rendered: require a dev/staging stack and MQTT tenant namespace support")
	}
	return nil
}
