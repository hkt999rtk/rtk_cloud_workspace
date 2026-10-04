package main

import (
	"strings"
	"testing"
)

func TestPRO2ExamplesPrefixPersistsInFrontendSettings(t *testing.T) {
	for _, k := range []string{"SDK_ARTIFACT_BUCKET", "SDK_ARTIFACT_ENDPOINT", "SDK_ARTIFACT_ACCESS_KEY_ID", "SDK_ARTIFACT_SECRET_ACCESS_KEY"} {
		t.Setenv(k, "test")
	}
	t.Setenv("PRO2_EXAMPLES_PREFIX", "pro2-examples/dev/")
	text, err := lkeFrontendSDKDownloadsSecretManifest(map[string]string{"CLOUD_STACK_NAME": "video-cloud-dev"})
	if err != nil || !strings.Contains(text, `PRO2_EXAMPLES_PREFIX: "pro2-examples/dev/"`) {
		t.Fatal("examples prefix not rendered", err)
	}
}

func TestPRO2ExamplesPrefixUsesDeploymentEnvironment(t *testing.T) {
	for _, key := range []string{"SDK_ARTIFACT_BUCKET", "SDK_ARTIFACT_ENDPOINT", "SDK_ARTIFACT_ACCESS_KEY_ID", "SDK_ARTIFACT_SECRET_ACCESS_KEY"} {
		t.Setenv(key, "test")
	}
	t.Setenv("PRO2_EXAMPLES_PREFIX", "")
	for _, environment := range []string{"dev", "staging", "prod", "qa-eu"} {
		t.Run(environment, func(t *testing.T) {
			for _, env := range []map[string]string{
				{"CLOUD_ENV_NAME": environment},
				{"CLOUD_STACK_NAME": "video-cloud-" + environment},
				{"CLOUD_ENV_NAME": environment, "CLOUD_STACK_NAME": "video-cloud-" + environment, "PRO2_EXAMPLES_PREFIX": "pro2-examples/" + environment + "/"},
			} {
				manifest, err := lkeFrontendSDKDownloadsSecretManifest(env)
				want := `PRO2_EXAMPLES_PREFIX: "pro2-examples/` + environment + `/"`
				if err != nil || !strings.Contains(manifest, want) {
					t.Fatalf("environment %v: expected %s, got error %v and manifest %s", env, want, err, manifest)
				}
			}
		})
	}
}

func TestPRO2ExamplesPrefixRejectsMixedScope(t *testing.T) {
	for _, test := range []struct {
		name     string
		env      map[string]string
		override string
	}{
		{name: "missing environment", env: map[string]string{}},
		{name: "unknown stack", env: map[string]string{"CLOUD_STACK_NAME": "sdk-test"}},
		{name: "invalid environment", env: map[string]string{"CLOUD_ENV_NAME": "../dev"}},
		{name: "invalid stack suffix", env: map[string]string{"CLOUD_STACK_NAME": "video-cloud-dev-"}},
		{name: "conflicting stack", env: map[string]string{"CLOUD_ENV_NAME": "dev", "CLOUD_STACK_NAME": "video-cloud-staging"}},
		{name: "legacy root", env: map[string]string{"CLOUD_ENV_NAME": "dev", "PRO2_EXAMPLES_PREFIX": "pro2-examples/"}},
		{name: "other environment", env: map[string]string{"CLOUD_ENV_NAME": "dev", "PRO2_EXAMPLES_PREFIX": "pro2-examples/staging/"}},
		{name: "operator override", env: map[string]string{"CLOUD_ENV_NAME": "dev", "PRO2_EXAMPLES_PREFIX": "pro2-examples/dev/"}, override: "pro2-examples/staging/"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv("PRO2_EXAMPLES_PREFIX", test.override)
			if prefix, err := lkeFrontendPRO2ExamplesPrefix(test.env); err == nil {
				t.Fatalf("expected invalid environment/prefix to fail, got %s", prefix)
			}
		})
	}
}
