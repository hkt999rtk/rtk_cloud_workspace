package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLKETargetedAccountManagerProductWritesSync(t *testing.T) {
	const key = "ACCOUNT_MANAGER_PLATFORM_SERVICE_PRODUCT_WRITES"
	t.Setenv(key, "")
	t.Setenv("LKE_ACCOUNT_MANAGER_IMAGE", "registry.example.test/account-manager:test")
	for _, tc := range []struct {
		name, before, desired, after, operation, encoded string
	}{
		{"existing false", `"ACCOUNT_MANAGER_PLATFORM_SERVICE_PRODUCT_WRITES":"ZmFsc2U=",`, "true", "dHJ1ZQ==", "replace", "dHJ1ZQ=="},
		{"missing defaults to false then enables", "", "true", "dHJ1ZQ==", "add", "dHJ1ZQ=="},
		{"missing defaults to false and stays disabled", "", "false", "ZmFsc2U=", "add", "ZmFsc2U="},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			statePath := filepath.Join(dir, "updated")
			patchPath := filepath.Join(dir, "patch.json")
			patchCountPath := filepath.Join(dir, "patch-count")
			kubectl := filepath.Join(dir, "kubectl")
			before := `{"metadata":{"uid":"secret-uid","resourceVersion":"41"},"data":{` + tc.before + `"KEEP":"cHJlc2VydmVk"}}`
			after := `{"metadata":{"uid":"secret-uid","resourceVersion":"42"},"data":{"ACCOUNT_MANAGER_PLATFORM_SERVICE_PRODUCT_WRITES":"` + tc.after + `","KEEP":"cHJlc2VydmVk"}}`
			script := fmt.Sprintf(`#!/usr/bin/env bash
set -euo pipefail
if [[ " $* " == *" get secret account-manager-runtime "* ]]; then
  if [[ -f %q ]]; then
    printf '%%s\n' %q
  else
    printf '%%s\n' %q
  fi
elif [[ " $* " == *" patch secret account-manager-runtime "* ]]; then
  cat > %q
  printf 'x\n' >> %q
  touch %q
else
  exit 1
fi
`, statePath, after, before, patchPath, patchCountPath, statePath)
			if err := os.WriteFile(kubectl, []byte(script), 0o700); err != nil {
				t.Fatal(err)
			}
			t.Setenv("RTK_CLOUD_KUBECTL", kubectl)
			t.Setenv("RTK_CLOUD_KUBECTL_RETRY_ATTEMPTS", "1")
			env := map[string]string{"CLOUD_STACK_NAME": "test-stack", key: tc.desired}
			if err := lkeSyncTargetedAccountManagerProductWrites(env); err != nil {
				t.Fatal(err)
			}
			patch, err := os.ReadFile(patchPath)
			if err != nil {
				t.Fatal(err)
			}
			var operations []struct {
				Op    string `json:"op"`
				Path  string `json:"path"`
				Value string `json:"value"`
			}
			if err := json.Unmarshal(patch, &operations); err != nil {
				t.Fatal(err)
			}
			if len(operations) != 3 ||
				operations[0].Op != "test" || operations[0].Path != "/metadata/uid" || operations[0].Value != "secret-uid" ||
				operations[1].Op != "test" || operations[1].Path != "/metadata/resourceVersion" || operations[1].Value != "41" ||
				operations[2].Op != tc.operation || operations[2].Path != "/data/"+key || operations[2].Value != tc.encoded {
				t.Fatalf("targeted patch must compare identity and version, then modify only the Product writes flag: %+v", operations)
			}
			if err := lkeSyncTargetedAccountManagerProductWrites(env); err != nil {
				t.Fatal(err)
			}
			patchCount, err := os.ReadFile(patchCountPath)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Count(string(patchCount), "x\n") != 1 {
				t.Fatalf("matching runtime flag should not be patched again: %q", patchCount)
			}
		})
	}
	env := map[string]string{"CLOUD_STACK_NAME": "test-stack", key: "true"}
	falseEnv := map[string]string{"CLOUD_STACK_NAME": "test-stack", key: "false"}
	if lkeAccountManagerOutboxWorkerManifest(falseEnv) == lkeAccountManagerOutboxWorkerManifest(env) {
		t.Fatal("Product writes flag change must update the outbox runtime checksum")
	}
}
