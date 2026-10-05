package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func deploymentRuntimeTestKubectl(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	calls := filepath.Join(dir, "calls")
	path := filepath.Join(dir, "kubectl")
	if err := os.WriteFile(path, []byte("#!/bin/sh\nprintf 'call\\n' >> \"$RTK_CHECK_CALLS\"\n"+body), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("RTK_CLOUD_KUBECTL", path)
	t.Setenv("RTK_CHECK_CALLS", calls)
	return calls
}

func deploymentRuntimeCallCount(t *testing.T, path string) int {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return strings.Count(string(raw), "call\n")
}

func deploymentRuntimeCode(t *testing.T, err error, want string) {
	t.Helper()
	var classified interface{ CheckCode() string }
	if !errors.As(err, &classified) || classified.CheckCode() != want {
		t.Fatalf("error = %v, want code %s", err, want)
	}
}

func TestDeploymentCheckRuntimeSharesSelectedSecretInventory(t *testing.T) {
	store := makeIsolatedTestSecretStore(t, "staging")
	if err := store.write("kube/kubeconfig.yaml", []byte("fixture"), true); err != nil {
		t.Fatal(err)
	}
	secrets := map[string]map[string]string{}
	for _, entry := range rtkSecretCatalog() {
		if err := store.write(filepath.Join("runtime", entry.ID), []byte("canonical"), true); err != nil {
			t.Fatal(err)
		}
		for _, binding := range entry.K8SBinding {
			label := "video-cloud-staging" + binding.NamespaceSuffix + "/" + binding.Secret
			if secrets[label] == nil {
				secrets[label] = map[string]string{}
			}
			secrets[label][binding.Key] = base64.StdEncoding.EncodeToString([]byte("canonical"))
		}
	}
	secrets["video-cloud-staging-account-manager/account-manager-runtime"]["ACCOUNT_MANAGER_ENV"] = base64.StdEncoding.EncodeToString([]byte("staging"))
	secrets["video-cloud-production-video-cloud/unrelated-private-key"] = map[string]string{"tls.crt": "unrelated-sensitive-marker", "tls.key": "invalid"}
	items := []any{}
	for label, data := range secrets {
		parts := strings.SplitN(label, "/", 2)
		items = append(items, map[string]any{"metadata": map[string]string{"namespace": parts[0], "name": parts[1]}, "data": data})
	}
	raw, err := json.Marshal(map[string]any{"items": items})
	if err != nil {
		t.Fatal(err)
	}
	fixture := filepath.Join(t.TempDir(), "inventory.json")
	if err := os.WriteFile(fixture, raw, 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("RTK_CHECK_INVENTORY", fixture)
	calls := deploymentRuntimeTestKubectl(t, `case "$*" in
  *'get secrets --all-namespaces -o json'*) cat "$RTK_CHECK_INVENTORY" ;;
  *'get deployments -o json'*) printf '%s' '{"items":[{"metadata":{"name":"video-cloud-api"}},{"metadata":{"name":"account-manager"}}]}' ;;
  *) exit 99 ;;
esac
`)
	store.checkRuntime = newDeploymentCheckRuntime(context.Background(), "staging")
	if err := verifySecretStoreK8SBindings(store); err != nil {
		t.Fatal(err)
	}
	if err := verifySecretStoreK8SRuntime(store, time.Now()); err != nil {
		t.Fatal(err)
	}
	if got := deploymentRuntimeCallCount(t, calls); got != 3 {
		t.Fatalf("calls = %d, want inventory plus two Deployment lists", got)
	}
	for _, entry := range store.checkRuntime.cache {
		if strings.Contains(string(entry.output), "unrelated-sensitive-marker") || strings.Contains(string(entry.output), "production-video-cloud") {
			t.Fatal("runtime retained a Secret from another stack")
		}
	}
	if err := verifySecretStoreK8SRuntime(store, time.Now()); err != nil {
		t.Fatal(err)
	}
	if got := deploymentRuntimeCallCount(t, calls); got != 3 {
		t.Fatalf("repeated same-run checks used %d calls", got)
	}
}

func TestDeploymentCheckRuntimeCachesReadsAndSingleDeployments(t *testing.T) {
	calls := deploymentRuntimeTestKubectl(t, `case "$*" in
  *'get deployments -o json'*) printf '%s' '{"items":[{"metadata":{"name":"pki-controller"}}]}' ;;
  *) printf '%s' '{"data":{},"items":[]}' ;;
esac
`)
	runtime := newDeploymentCheckRuntime(context.Background(), "staging")
	for _, args := range [][]string{
		{"--kubeconfig", "fixture", "-n", "selected", "get", "deployment", "pki-controller", "-o", "json"},
		{"--kubeconfig", "fixture", "-n", "selected", "get", "deployments", "-o", "json"},
		{"--kubeconfig", "fixture", "-n", "selected", "get", "configmap", "trust", "-o", "json"},
		{"--kubeconfig", "fixture", "-n", "selected", "get", "configmap", "trust", "-o", "json"},
		{"--kubeconfig", "fixture", "-n", "selected", "get", "pods", "-l", "app.kubernetes.io/name=postgresql", "-o", "json"},
		{"--kubeconfig", "fixture", "-n", "selected", "get", "pods", "-l", "app.kubernetes.io/name=postgresql", "-o", "json"},
	} {
		if _, err := runtime.kubectl(false, args...); err != nil {
			t.Fatal(err)
		}
	}
	if got := deploymentRuntimeCallCount(t, calls); got != 3 {
		t.Fatalf("calls = %d, want three distinct inventories", got)
	}
	if _, err := runtime.kubectl(false, "--kubeconfig", "other-context", "-n", "selected", "get", "deployments", "-o", "json"); err != nil {
		t.Fatal(err)
	}
	if got := deploymentRuntimeCallCount(t, calls); got != 4 {
		t.Fatal("cache reused a different kubeconfig")
	}
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := runtime.kubectl(false, "-n", "another", "get", "configmap", "trust", "-o", "json"); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if got := deploymentRuntimeCallCount(t, calls); got != 5 {
		t.Fatalf("concurrent reads were not deduplicated: %d calls", got)
	}
}

func TestDeploymentCheckRuntimeClassifiesAndBoundsRetries(t *testing.T) {
	for _, tc := range []struct {
		name, diagnostic, code string
		calls                  int
	}{
		{"forbidden", "Error from server (Forbidden): password=private-marker", "KUBE_FORBIDDEN", 1},
		{"unauthorized", "Unauthorized token=private-marker", "KUBE_UNAUTHORIZED", 1},
		{"missing", "Error from server (NotFound): private-marker", "KUBE_NOT_FOUND", 1},
		{"rate limit", "TooManyRequests private-marker", "KUBE_RATE_LIMITED", 2},
		{"unavailable", "Service Unavailable private-marker", "KUBE_UNAVAILABLE", 2},
		{"timeout", "TLS handshake timeout private-marker", "KUBE_TIMEOUT", 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := deploymentRuntimeTestKubectl(t, "printf '%s' \"$RTK_CHECK_DIAGNOSTIC\" >&2\nexit 1\n")
			t.Setenv("RTK_CHECK_DIAGNOSTIC", tc.diagnostic)
			runtime := newDeploymentCheckRuntime(context.Background(), "staging")
			runtime.retryDelay = time.Millisecond
			for range 2 {
				_, err := runtime.kubectl(false, "get", "configmap", "trust", "-o", "json")
				deploymentRuntimeCode(t, err, tc.code)
				if strings.Contains(err.Error(), "private-marker") {
					t.Fatal("error exposed tool diagnostics")
				}
			}
			if got := deploymentRuntimeCallCount(t, calls); got != tc.calls {
				t.Fatalf("calls = %d, want %d", got, tc.calls)
			}
		})
	}
}

func TestDeploymentCheckRuntimeFailureDoesNotRepeatInventoryForEachBinding(t *testing.T) {
	store := makeIsolatedTestSecretStore(t, "staging")
	if err := store.write("kube/kubeconfig.yaml", []byte("fixture"), true); err != nil {
		t.Fatal(err)
	}
	calls := deploymentRuntimeTestKubectl(t, "printf 'Forbidden secret-value-marker' >&2\nexit 1\n")
	store.checkRuntime = newDeploymentCheckRuntime(context.Background(), "staging")
	for _, err := range []error{verifySecretStoreK8SBindings(store), verifySecretStoreK8SRuntime(store, time.Now())} {
		deploymentRuntimeCode(t, err, "KUBE_FORBIDDEN")
		if strings.Contains(err.Error(), "secret-value-marker") {
			t.Fatal("error leaked diagnostic")
		}
	}
	if got := deploymentRuntimeCallCount(t, calls); got != 1 {
		t.Fatalf("inventory failure caused %d calls", got)
	}
}

func TestDeploymentCheckRuntimeSQLTimeoutAndNoExecRetry(t *testing.T) {
	argsPath := filepath.Join(t.TempDir(), "args")
	t.Setenv("RTK_CHECK_ARGS", argsPath)
	calls := deploymentRuntimeTestKubectl(t, `printf '%s\n' "$@" > "$RTK_CHECK_ARGS"
printf 'ERROR: canceling statement due to statement timeout; private-query-marker' >&2
exit 1
`)
	runtime := newDeploymentCheckRuntime(context.Background(), "staging")
	_, err := runtime.kubectl(true, "-n", "selected", "exec", "postgresql-0", "--", "psql", "-U", "postgres", "-At", "-c", "SELECT 1")
	deploymentRuntimeCode(t, err, "SQL_TIMEOUT")
	if strings.Contains(err.Error(), "private-query-marker") {
		t.Fatal("SQL error leaked query diagnostics")
	}
	if got := deploymentRuntimeCallCount(t, calls); got != 1 {
		t.Fatalf("SQL exec retried %d times", got)
	}
	raw, err := os.ReadFile(argsPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "--request-timeout=10s\n") || !strings.Contains(string(raw), "--\nenv\nPGOPTIONS=-c statement_timeout=10000\npsql\n") {
		t.Fatalf("SQL timeout controls missing: %s", raw)
	}
}

func TestDeploymentCheckRuntimeCancellationStopsDescendants(t *testing.T) {
	for _, cancelRun := range []bool{false, true} {
		name := "process deadline"
		if cancelRun {
			name = "caller cancellation"
		}
		t.Run(name, func(t *testing.T) {
			marker := filepath.Join(t.TempDir(), "orphan-marker")
			t.Setenv("RTK_CHECK_ORPHAN", marker)
			ready := filepath.Join(t.TempDir(), "child-ready")
			t.Setenv("RTK_CHECK_READY", ready)
			calls := deploymentRuntimeTestKubectl(t, "(printf ready > \"$RTK_CHECK_READY\"; sleep 2; printf orphan > \"$RTK_CHECK_ORPHAN\") &\nwait\n")
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			runtime := newDeploymentCheckRuntime(ctx, "staging")
			runtime.processTimeout = 500 * time.Millisecond
			runtime.retryDelay = time.Millisecond
			if cancelRun {
				runtime.processTimeout = 5 * time.Second
				go func() {
					until := time.Now().Add(3 * time.Second)
					for time.Now().Before(until) {
						if _, err := os.Stat(ready); err == nil {
							cancel()
							return
						}
						time.Sleep(10 * time.Millisecond)
					}
					cancel()
				}()
			}
			started := time.Now()
			_, err := runtime.kubectl(false, "get", "configmap", "trust", "-o", "json")
			want, attempts := "KUBE_TIMEOUT", 2
			if cancelRun {
				want, attempts = "CHECK_CANCELLED", 1
			}
			deploymentRuntimeCode(t, err, want)
			if time.Since(started) > 4*time.Second {
				t.Fatal("cancelled process held the checker open")
			}
			// A deadline may expire before a retried child reaches its first
			// instruction under load. Exact retry counts are covered separately;
			// this fixture verifies the bound and descendant termination.
			if got := deploymentRuntimeCallCount(t, calls); got < 1 || got > attempts || (cancelRun && got != 1) {
				t.Fatalf("calls = %d, want between 1 and %d", got, attempts)
			}
			time.Sleep(2100 * time.Millisecond)
			if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("a child process survived command cancellation")
			}
		})
	}
}

func TestDeploymentCheckRuntimeRejectsMalformedSecretInventory(t *testing.T) {
	for _, tc := range []struct {
		name, body, code string
	}{
		{"null", `null`, "KUBE_INVALID_RESPONSE"},
		{"missing items", `{}`, "KUBE_INVALID_RESPONSE"},
		{"null items", `{"items":null}`, "KUBE_INVALID_RESPONSE"},
		{"object items", `{"items":{}}`, "KUBE_INVALID_RESPONSE"},
		{"null item", `{"items":[null]}`, "KUBE_INVALID_RESPONSE"},
		{"unnamed item", `{"items":[{"metadata":{"namespace":"video-cloud-staging-platform"}}]}`, "KUBE_INVALID_RESPONSE"},
		{"missing namespace", `{"items":[{"metadata":{"name":"example"}}]}`, "KUBE_INVALID_RESPONSE"},
		{"empty", `{"items":[]}`, "KUBE_INVENTORY_EMPTY"},
		{"other environment only", `{"items":[{"metadata":{"name":"example","namespace":"video-cloud-prod-platform"},"data":{"token":"private-marker"}}]}`, "KUBE_INVENTORY_EMPTY"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := deploymentRuntimeTestKubectl(t, `printf '%s' "$RTK_CHECK_INVENTORY_BODY"`)
			t.Setenv("RTK_CHECK_INVENTORY_BODY", tc.body)
			runtime := newDeploymentCheckRuntime(context.Background(), "staging")
			for range 2 {
				_, err := runtime.secretInventory("fixture")
				deploymentRuntimeCode(t, err, tc.code)
				if strings.Contains(err.Error(), "private-marker") {
					t.Fatal("inventory error leaked a Secret")
				}
			}
			if got := deploymentRuntimeCallCount(t, calls); got != 1 {
				t.Fatalf("invalid inventory was fetched %d times", got)
			}
		})
	}
}

func TestDeploymentCheckRuntimeRejectsMalformedDeploymentInventory(t *testing.T) {
	for _, body := range []string{`null`, `{}`, `{"items":null}`, `{"items":{}}`, `{"items":[null]}`, `{"items":[{}]}`} {
		t.Run(body, func(t *testing.T) {
			calls := deploymentRuntimeTestKubectl(t, `printf '%s' "$RTK_CHECK_INVENTORY_BODY"`)
			t.Setenv("RTK_CHECK_INVENTORY_BODY", body)
			runtime := newDeploymentCheckRuntime(context.Background(), "staging")
			for _, tail := range [][]string{
				{"deployments", "-o", "json"},
				{"deployment", "account-manager-handoff-worker", "--ignore-not-found=true", "-o", "name"},
			} {
				args := append([]string{"--kubeconfig", "fixture", "-n", "video-cloud-staging-account-manager", "get"}, tail...)
				_, err := runtime.kubectl(false, args...)
				deploymentRuntimeCode(t, err, "KUBE_INVALID_RESPONSE")
			}
			if got := deploymentRuntimeCallCount(t, calls); got != 1 {
				t.Fatalf("invalid Deployment inventory was fetched %d times", got)
			}
		})
	}
}

func TestDeploymentCheckRuntimeEmptyDeploymentsPreserveOptionalNamedLookup(t *testing.T) {
	for _, namespace := range []string{"video-cloud-staging-video-cloud", "video-cloud-staging-account-manager"} {
		t.Run(namespace, func(t *testing.T) {
			calls := deploymentRuntimeTestKubectl(t, `printf '%s' '{"items":[]}'`)
			runtime := newDeploymentCheckRuntime(context.Background(), "staging")
			args := []string{"--kubeconfig", "fixture", "-n", namespace, "get"}
			// The optional lookup must remain valid both before and after a required
			// list fails, even though both operations reuse the same snapshot.
			for range 2 {
				output, err := runtime.kubectl(false, append(args, "deployment", "account-manager-handoff-worker", "--ignore-not-found=true", "-o", "name")...)
				if err != nil || len(output) != 0 {
					t.Fatalf("optional missing Deployment: output=%q err=%v", output, err)
				}
				_, err = runtime.kubectl(false, append(args, "deployments", "-o", "json")...)
				deploymentRuntimeCode(t, err, "KUBE_INVENTORY_EMPTY")
				_, err = runtime.kubectl(false, append(args, "deployment", "pki-controller", "-o", "json")...)
				deploymentRuntimeCode(t, err, "KUBE_NOT_FOUND")
			}
			if got := deploymentRuntimeCallCount(t, calls); got != 1 {
				t.Fatalf("empty Deployment inventory was fetched %d times", got)
			}
		})
	}
}

func TestDeploymentCheckRuntimeRejectsUnobservedRequiredWorkloads(t *testing.T) {
	for _, namespace := range []string{"video-cloud-staging-video-cloud", "video-cloud-staging-account-manager"} {
		t.Run(namespace, func(t *testing.T) {
			store := makeIsolatedTestSecretStore(t, "staging")
			if err := store.write("kube/kubeconfig.yaml", []byte("fixture"), true); err != nil {
				t.Fatal(err)
			}
			t.Setenv("RTK_CHECK_EMPTY_NAMESPACE", namespace)
			deploymentRuntimeTestKubectl(t, `case "$*" in
  *'get secrets --all-namespaces -o json'*) printf '%s' '{"items":[{"metadata":{"namespace":"video-cloud-staging-platform","name":"observed"},"data":{}}]}' ;;
  *'get deployments -o json'*)
    case "$*" in
      *"-n $RTK_CHECK_EMPTY_NAMESPACE "*) printf '%s' '{"items":[]}' ;;
      *) printf '%s' '{"items":[{"metadata":{"name":"observed-workload"}}]}' ;;
    esac ;;
  *) exit 99 ;;
esac
`)
			// Existing functions without a checker runtime retain their prior scope.
			if err := verifySecretStoreK8SRuntime(store, time.Now()); err != nil {
				t.Fatalf("legacy verifier changed: %v", err)
			}
			store.checkRuntime = newDeploymentCheckRuntime(context.Background(), "staging")
			err := verifySecretStoreK8SRuntime(store, time.Now())
			deploymentRuntimeCode(t, err, "KUBE_INVENTORY_EMPTY")
			result := deploymentCheckFailure("pki.runtime", err)
			if result.Status != "ERROR" || result.Code != "KUBE_INVENTORY_EMPTY" {
				t.Fatalf("facade lost required inventory error: %+v", result)
			}
		})
	}
}

func TestDeploymentCheckRuntimeRejectsUnobservedSelectedSecrets(t *testing.T) {
	store := makeIsolatedTestSecretStore(t, "staging")
	if err := store.write("kube/kubeconfig.yaml", []byte("fixture"), true); err != nil {
		t.Fatal(err)
	}
	calls := deploymentRuntimeTestKubectl(t, `printf '%s' '{"items":[]}'`)
	store.checkRuntime = newDeploymentCheckRuntime(context.Background(), "staging")
	for _, err := range []error{verifySecretStoreK8SBindings(store), verifySecretStoreK8SRuntime(store, time.Now())} {
		deploymentRuntimeCode(t, err, "KUBE_INVENTORY_EMPTY")
	}
	if got := deploymentRuntimeCallCount(t, calls); got != 1 {
		t.Fatalf("unobserved Secrets caused %d repeated reads", got)
	}
}

func TestDeploymentCheckRuntimeKeepsAllNamespaceInventoryDistinct(t *testing.T) {
	calls := deploymentRuntimeTestKubectl(t, `case "$*" in
 *'get deployments --all-namespaces -o json'*) printf '%s' '{"items":[{"metadata":{"namespace":"video-cloud-staging-billing","name":"worker"}},{"metadata":{"namespace":"video-cloud-staging-video-cloud","name":"api"}}]}' ;;
 *'-n video-cloud-staging-video-cloud get deployments -o json'*) printf '%s' '{"items":[{"metadata":{"namespace":"video-cloud-staging-video-cloud","name":"api"}}]}' ;;
 *) exit 99;; esac`)
	runtime := newDeploymentCheckRuntime(context.Background(), "staging")
	for _, all := range []bool{false, true, true, false} {
		args := []string{"--kubeconfig", "fixture", "-n", "video-cloud-staging-video-cloud", "get", "deployments", "-o", "json"}
		if all {
			args = []string{"--kubeconfig", "fixture", "get", "deployments", "-A", "-o", "json"}
		}
		raw, err := runtime.kubectl(false, args...)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(raw), "worker") != all {
			t.Fatal("namespace inventory cache changed scope")
		}
	}
	if got := deploymentRuntimeCallCount(t, calls); got != 2 {
		t.Fatalf("calls=%d", got)
	}
}
