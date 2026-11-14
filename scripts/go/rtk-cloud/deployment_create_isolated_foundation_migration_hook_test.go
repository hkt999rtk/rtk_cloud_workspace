package main

import (
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
)

// The one owner-labelled network is created after the actual isolated GO. The
// migration listener executes only the four admitted real Jobs on that network.
func installIsolatedFreshFoundationMigration(t *testing.T, store secretStore, cfg deploymentConfig, plan environmentCreatePlan, controller *isolatedOpenBaoFirstController, kubeDir, kubectl, frozenKube string, env []string) ([]string, int, *isolatedMigrationController) {
	t.Helper()
	if os.Getenv("RTK_TEST_FOUNDATION_JOBS") != "1" {
		return env, 0, nil
	}
	if os.Getenv("RTK_TEST_FRESH_CREATE") != "1" || os.Getenv("RTK_TEST_OPENBAO_FIRST") != "1" || controller == nil || !isolatedScriptTempPath(store.Root) || !isolatedScriptTempPath(kubeDir) || !isolatedScriptTempPath(kubectl) || !isolatedScriptTempPath(frozenKube) || cfg.Environment != "dev" || plan.Environment != cfg.Environment || plan.Stack != cfg.Values["CLOUD_STACK_NAME"] || !plan.OpenBaoFirst {
		t.Fatal("same-parent isolated foundation migration fixture required")
	}
	network, owner, err := controller.ensureIsolatedFoundationNetwork(t.Context())
	if err != nil {
		t.Fatal("isolated shared foundation network unavailable", err)
	}
	docker, err := exec.LookPath("docker")
	if err != nil {
		t.Fatal("isolated Docker client unavailable")
	}
	selected := map[string]string{"pki-migrate": plan.Release.Images["LKE_VIDEO_CLOUD_IMAGE"].Reference, "pki-grants": plan.Release.Images["LKE_VIDEO_CLOUD_IMAGE"].Reference, "pki-create-database-logins": plan.Release.Images["LKE_POSTGRES_IMAGE"].Reference, "account-manager-migrate": plan.Release.Images["LKE_ACCOUNT_MANAGER_IMAGE"].Reference}
	t.Setenv("RTK_CLOUD_KUBECTL", kubectl)
	jobController, port := installIsolatedMigrationController(t, store, kubeDir, docker, network, owner, plan.Stack, selected)
	// Native backend passes its frozen selected identity to kubectl, not the raw
	// original YAML bytes. This is the same verified file as the earlier shim.
	t.Setenv("RTK_TEST_JOB_KUBECONFIG", frozenKube)
	updated := append([]string(nil), env...)
	count := 0
	for i, item := range updated {
		if strings.HasPrefix(item, "RTK_CLOUD_KUBECTL=") {
			updated[i] = "RTK_CLOUD_KUBECTL=" + os.Getenv("RTK_CLOUD_KUBECTL")
			count++
		}
		if strings.HasPrefix(item, "RTK_TEST_JOB_") {
			t.Fatal("ambiguous migration fixture environment")
		}
	}
	if count != 1 {
		t.Fatal("original isolated kubectl selection unavailable")
	}
	for _, key := range []string{"RTK_TEST_JOB_FALLBACK", "RTK_TEST_JOB_KUBECONFIG", "RTK_TEST_JOB_BRIDGE", "RTK_TEST_JOB_CAPABILITY", "RTK_TEST_JOB_PLATFORM_NAMESPACE"} {
		if os.Getenv(key) == "" {
			t.Fatal("migration bridge setting unavailable", key)
		}
		updated = append(updated, key+"="+os.Getenv(key))
	}
	if port <= 0 || port > 65535 {
		t.Fatal("isolated migration bridge port invalid: " + strconv.Itoa(port))
	}
	return updated, port, jobController
}
