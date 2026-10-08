package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
	"strings"
	"testing"
	"time"
)

func TestEarlyInventoryReportsChangedChecksWithoutQualifyingEnvironment(t *testing.T) {
	for _, environment := range []string{"dev", "staging", "prod"} {
		t.Run(environment, func(t *testing.T) {
			env, _, _ := completeEnvironmentRenderFixture(environment)
			live, _ := otaRetirementFixture(environment)
			var output bytes.Buffer
			err := runEnvironmentProgress(context.Background(), &output, "deployment.compile", func(ctx context.Context) error { return checkEnvironmentEarlyInventory(ctx, env, live) })
			var blocker *deploymentplan.Blocker
			if !errors.As(err, &blocker) || blocker.ID != "deployment.inventory.renderer" || !strings.HasSuffix(blocker.Resource, "/video-cloud-otaservice") {
				t.Fatal("disabled replacement did not fail early", err)
			}
			if !strings.Contains(output.String(), "[PRECHECK] deployment.early.ota-retirement PASS; full qualification pending") || strings.Contains(output.String(), "GO environment=") {
				t.Fatal("precheck was hidden or treated as qualification", output.String())
			}
			env["LKE_OTA_SERVICE_REGISTRATION_ENABLED"] = "true"
			t.Setenv("LKE_OTA_SERVICE_REGISTRATION_ENABLED", "true")
			if err := checkEnvironmentEarlyInventory(context.Background(), env, live); err != nil {
				t.Fatal(err)
			}
			deploymentplan.Map(live[0]["spec"])["replicas"] = float64(1)
			if err := checkEnvironmentEarlyInventory(context.Background(), env, live); !errors.As(err, &blocker) || blocker.ID != "deployment.ota.retirement" {
				t.Fatal("active registrar bypassed the real retirement check", err)
			}
		})
	}
}
func TestEarlyInventoryBlockerRunsBeforeAnyImageCommand(t *testing.T) {
	cfg, _, _, _ := maintainedDeploymentSnapshotFixture(t, "dev")
	cfg.Values["DEPLOYMENT_PROTECTION_PROFILE"] = "standard"
	directory := t.TempDir()
	ns := cfg.Values["CLOUD_STACK_NAME"] + "-video-cloud"
	workload := deploymentplan.Object{"apiVersion": "apps/v1", "kind": "Deployment", "metadata": map[string]any{"name": "video-cloud-otaservice", "namespace": ns, "uid": "existing-service"}}
	namespaces, _ := json.Marshal(map[string]any{"items": []any{map[string]any{"metadata": map[string]any{"name": ns}}}})
	inventory, _ := json.Marshal(map[string]any{"items": []any{workload}})
	writeTestFile(t, filepath.Join(directory, "namespaces.json"), string(namespaces))
	writeTestFile(t, filepath.Join(directory, "inventory.json"), string(inventory))
	writeTestFile(t, filepath.Join(directory, "kubectl"), `#!/bin/sh
case "$*" in
 *'get namespace '*) cat "$EARLY_TEST_ROOT/namespaces.json";;
 *'get deployment,'*) cat "$EARLY_TEST_ROOT/inventory.json";;
 *'get clusterrole,'*) printf '{"items":[]}';;
 *' get '*) exit 0;;
 *) exit 1;;
esac
`)
	if err := os.Chmod(filepath.Join(directory, "kubectl"), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", directory+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("EARLY_TEST_ROOT", directory)
	calls := 0
	run := func(context.Context, string, ...string) ([]byte, error) {
		calls++
		return nil, errors.New("image-command-should-not-run")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := compileEnvironmentDeploymentWithImages(ctx, cfg, "upgrade", environmentRelease{}, run)
	var blocker *deploymentplan.Blocker
	if !errors.As(err, &blocker) || blocker.ID != "deployment.inventory.renderer" || calls != 0 {
		t.Fatal("images ran before known renderer failure", calls, err)
	}
	// An early pass must continue into formal image verification, not return GO.
	cfg.AdapterValues["LKE_OTA_SERVICE_REGISTRATION_ENABLED"] = "true"
	compiled, err := compileEnvironmentDeploymentWithImages(ctx, cfg, "upgrade", environmentRelease{}, run)
	if compiled != nil || err == nil || calls == 0 {
		t.Fatal("early pass skipped formal images or produced a qualified plan", calls, err)
	}
}
