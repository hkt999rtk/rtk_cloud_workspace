package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPublicMigrationCommandsRejectInvalidScopeBeforeStatePublication(t *testing.T) {
	for _, environment := range []string{"dev", "staging", "prod"} {
		t.Run(environment, func(t *testing.T) {
			store := makeIsolatedTestSecretStore(t, environment)
			workspace := writeDeploymentFixture(t, environment, "lke")
			stack := "video-cloud-" + environment
			for _, entry := range []struct {
				name string
				run  func([]string) error
				args []string
				want error
			}{
				{"dns-help", runDeploymentDNSImport, []string{"--help"}, nil},
				{"dns-unknown", runDeploymentDNSImport, []string{"--unknown-option"}, exitCode(2)},
				{"dns-relative-source", runDeploymentDNSImport, []string{"--environment", environment, "--source-runtime", "relative"}, exitCode(2)},
				{"dns-foreign-confirm", runDeploymentDNSImport, []string{"--workspace", workspace, "--environment", environment, "--source-runtime", t.TempDir(), "--confirm", "foreign"}, exitCode(2)},
				{"dns-trailing-argument", runDeploymentDNSImport, []string{"--workspace", workspace, "--environment", environment, "--source-runtime", t.TempDir(), "--confirm", stack, "unexpected"}, exitCode(2)},
				{"reconcile-help", runEnvironmentExecutionReconcile, []string{"--help"}, nil},
				{"reconcile-unknown", runEnvironmentExecutionReconcile, []string{"--unknown-option"}, exitCode(2)},
				{"reconcile-invalid-sha", runEnvironmentExecutionReconcile, []string{"--workspace", workspace, "--environment", environment, "--plan-sha256", "latest"}, exitCode(2)},
				{"reconcile-zero-timeout", runEnvironmentExecutionReconcile, []string{"--workspace", workspace, "--environment", environment, "--plan-sha256", strings.Repeat("a", 64), "--timeout", "0s"}, exitCode(2)},
				{"reconcile-foreign-confirm", runEnvironmentExecutionReconcile, []string{"--workspace", workspace, "--environment", environment, "--plan-sha256", strings.Repeat("a", 64), "--confirm", "foreign"}, exitCode(2)},
				{"reconcile-missing-identity", runEnvironmentExecutionReconcile, []string{"--workspace", workspace, "--environment", environment, "--plan-sha256", strings.Repeat("a", 64), "--confirm", stack}, exitCode(1)},
			} {
				t.Run(entry.name, func(t *testing.T) {
					if err := entry.run(entry.args); err != entry.want {
						t.Fatal("public command returned an incorrect exit code", err)
					}
				})
			}
			if _, err := os.Lstat(filepath.Join(store.Root, "deployment")); !os.IsNotExist(err) {
				t.Fatal("invalid public command published state or execution ownership")
			}
		})
	}
}
