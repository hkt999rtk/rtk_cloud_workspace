package main

import (
	"bytes"
	"context"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestDeploymentRuntimeProgressIdentifiesQueriesWithoutCommandPayloads(t *testing.T) {
	for _, test := range []struct {
		name, action, resource string
		sql                    bool
		args                   []string
	}{
		{"secret-inventory", "inventory", "Secret//", false, []string{"--kubeconfig", "/private-kubeconfig", "get", "secrets", "--all-namespaces", "-o", "json"}},
		{"all-namespace-inventory", "inventory", "Pod//", false, []string{"-n", "selected", "get", "pods", "-A", "-o", "json"}},
		{"explicit-namespace-inventory", "inventory", "Pod/selected/", false, []string{"-n", "selected", "get", "pods", "--all-namespaces=false", "-o", "json"}},
		{"namespaced-inventory", "inventory", "Pod/selected/", false, []string{"-n", "selected", "get", "pods", "-l", "private-selector=value", "-o", "json"}},
		{"qualified-inventory", "inventory", "Lease/selected/", false, []string{"get", "leases.coordination.k8s.io", "--namespace=selected", "-o", "json"}},
		{"cluster-inventory", "inventory", "Node//", false, []string{"-n", "selected", "get", "nodes", "-o", "json"}},
		{"unknown-inventory", "inventory", "", false, []string{"get", "private-kind", "-o", "json"}},
		{"invalid-inventory-namespace", "inventory", "", false, []string{"-n", "private-invalid\ncredential", "get", "secrets", "-o", "json"}},
		{"secret", "read", "Secret/selected/runtime", false, []string{"--kubeconfig", "/private-kubeconfig", "-n", "selected", "get", "secret", "runtime", "-o", "json"}},
		{"pod", "read", "Pod/selected/app-0", false, []string{"get", "pod/app-0", "--namespace=selected", "-o", "json"}},
		{"namespace", "read", "Namespace//selected", false, []string{"get", "namespace", "selected", "-o", "json"}},
		{"sql", "sql", "Pod/selected/postgresql-0", true, []string{"-n", "selected", "exec", "postgresql-0", "--", "env", "PGOPTIONS=private-option", "psql", "-c", "private-sql-command"}},
		{"sql-deployment", "sql", "", true, []string{"-n", "selected", "exec", "deployment/postgresql", "--", "psql", "-c", "private-sql-command"}},
		{"pki-exec", "pki", "Pod/selected/issuer-0", false, []string{"-n", "selected", "exec", "pod/issuer-0", "--", "private-exec-command", "private-token"}},
		{"permissions", "permissions-get", "", false, []string{"auth", "can-i", "get", "private-resource"}},
		{"named-permissions", "permissions-delete", "Lease/selected/execution", false, []string{"auth", "can-i", "delete", "leases.coordination.k8s.io/execution", "--namespace=selected"}},
		{"collection-permissions", "permissions-list", "Deployment/selected/", false, []string{"-n", "selected", "auth", "can-i", "list", "deployments.apps"}},
		{"cluster-permissions", "permissions-get", "Node//original", false, []string{"-n", "selected", "auth", "can-i", "get", "nodes/original"}},
		{"unknown-permission-verb", "permissions", "ConfigMap/selected/settings", false, []string{"-n", "selected", "auth", "can-i", "private-verb", "configmaps/settings"}},
		{"permission-list-format", "permissions", "", false, []string{"auth", "can-i", "--list"}},
		{"invalid-permission-name", "permissions-get", "", false, []string{"-n", "selected", "auth", "can-i", "get", "secrets/private-invalid\ncredential"}},
		{"raw-url", "read", "", false, []string{"get", "--raw", "https://private-url?private-token"}},
		{"invalid-name", "read", "", false, []string{"-n", "selected", "get", "secret", "private-invalid\ncredential"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			action, resource := deploymentRuntimeProgressTarget(test.args, test.sql)
			if action != test.action || resource != test.resource || strings.Contains(action+resource, "private-") {
				t.Fatal("query progress fabricated a target or exposed command payloads", action, resource)
			}
		})
	}
}

func TestDeploymentRuntimeProgressDoesNotCreateOperationsForCacheHits(t *testing.T) {
	calls := deploymentRuntimeTestKubectl(t, `printf '%s' '{"private-body":"private-runtime-output"}'
`)
	var output bytes.Buffer
	err := runEnvironmentProgressInterval(context.Background(), &output, "deployment.pki", time.Millisecond, func(ctx context.Context) error {
		runtime := newDeploymentCheckRuntime(ctx, "staging")
		var joined sync.WaitGroup
		for range environmentDeploymentCheckConcurrency {
			joined.Add(1)
			go func() {
				defer joined.Done()
				body, err := runtime.kubectl(false, "--kubeconfig", "/private-config", "-n", "selected", "get", "configmap", "trust", "-o", "json")
				if err != nil || !bytes.Contains(body, []byte("private-runtime-output")) {
					t.Error("progress changed the cached private read outcome", err)
				}
			}()
		}
		joined.Wait()
		progress := ctx.Value(environmentProgressKey{}).(*environmentProgress)
		progress.mu.Lock()
		active, settled := len(progress.operations), progress.settled
		progress.mu.Unlock()
		if active != 0 || settled != 1 {
			t.Fatal("cached callers fabricated API activity or left a finished query active", active, settled)
		}
		return nil
	})
	if err != nil || deploymentRuntimeCallCount(t, calls) != 1 || strings.Contains(output.String(), "private-") {
		t.Fatal("progress created extra API work or exposed private read inputs", err, output.String())
	}
}

func TestDeploymentRuntimeInventoryProgressShowsCollectionScope(t *testing.T) {
	calls := deploymentRuntimeTestKubectl(t, `/bin/sleep 0.05
printf '%s' '{"items":[],"private-body":"private-inventory-output"}'
`)
	var output bytes.Buffer
	err := runEnvironmentProgressInterval(context.Background(), &output, "deployment.pki", time.Millisecond, func(ctx context.Context) error {
		runtime := newDeploymentCheckRuntime(ctx, "staging")
		for _, args := range [][]string{
			{"--kubeconfig", "/private-config", "-n", "selected-platform", "get", "pods", "-l", "private-selector=value", "-o", "json"},
			{"--kubeconfig", "/private-config", "--namespace=selected-api", "get", "leases", "-o", "json"},
		} {
			if _, err := runtime.kubectl(false, args...); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil || deploymentRuntimeCallCount(t, calls) != 2 {
		t.Fatal("inventory reporting changed actual reads", err)
	}
	for _, scope := range []string{"Pod/selected-platform/*", "Lease/selected-api/*"} {
		if !strings.Contains(output.String(), "Inspect resource inventory "+scope) {
			t.Fatal("inventory progress hid its kind or namespace", scope, output.String())
		}
	}
	if strings.Contains(output.String(), "private-") {
		t.Fatal("inventory progress exposed command arguments or response bodies", output.String())
	}
}
