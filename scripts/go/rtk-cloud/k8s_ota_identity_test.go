package main

import (
	"strings"
	"testing"
)

func TestLKEProductWriteGateIsPersistedInAccountManagerRuntime(t *testing.T) {
	env := map[string]string{"CLOUD_STACK_NAME": "video-cloud-dev", "CLOUD_ENV_NAME": "dev"}
	if !strings.Contains(lkeAccountManagerSecretManifest(env), `ACCOUNT_MANAGER_PLATFORM_SERVICE_PRODUCT_WRITES: "false"`) {
		t.Fatal("Product writes must default off in the rendered runtime Secret")
	}
	env["ACCOUNT_MANAGER_PLATFORM_SERVICE_PRODUCT_WRITES"] = "true"
	if !strings.Contains(lkeAccountManagerSecretManifest(env), `ACCOUNT_MANAGER_PLATFORM_SERVICE_PRODUCT_WRITES: "true"`) {
		t.Fatal("rendered runtime Secret ignored the Product write gate")
	}
}
