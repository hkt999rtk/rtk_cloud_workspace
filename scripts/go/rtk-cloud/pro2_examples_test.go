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
