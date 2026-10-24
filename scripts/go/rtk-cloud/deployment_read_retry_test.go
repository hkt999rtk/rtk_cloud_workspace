package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func TestReadRetryRuntimeRecoversThirdAttemptAndDoesNotCacheTransientFailure(t *testing.T) {
	for _, recoverAfter := range []int{2, 3} {
		t.Run(fmt.Sprint(recoverAfter), func(t *testing.T) {
			calls := deploymentRuntimeTestKubectl(t, fmt.Sprintf(`count=$(wc -l < "$RTK_CHECK_CALLS")
if [ "$count" -le %d ]; then printf 'TLS handshake timeout private-canary' >&2; exit 1; fi
printf '%%s' '{"items":[{"metadata":{"name":"original-pod","uid":"original-uid"}}]}'
`, recoverAfter))
			runtime := newDeploymentCheckRuntime(context.Background(), "dev")
			runtime.retryDelay = time.Millisecond
			args := []string{"get", "pods", "-o", "json"}
			raw, err := runtime.kubectl(false, args...)
			if recoverAfter == 2 {
				if err != nil || !strings.Contains(string(raw), "original-uid") || deploymentRuntimeCallCount(t, calls) != 3 {
					t.Fatal("third bounded attempt did not return fresh original metadata", err)
				}
			} else {
				deploymentRuntimeCode(t, err, "KUBE_TIMEOUT")
				if raw != nil || len(runtime.cache) != 0 || deploymentRuntimeCallCount(t, calls) != 3 {
					t.Fatal("exhausted transient response became permanent cached evidence", err)
				}
				raw, err = runtime.kubectl(false, args...)
				if err != nil || !strings.Contains(string(raw), "original-uid") || deploymentRuntimeCallCount(t, calls) != 4 {
					t.Fatal("later read reused an exhausted failure instead of fresh metadata", err)
				}
			}
			before := deploymentRuntimeCallCount(t, calls)
			if _, err = runtime.kubectl(false, args...); err != nil || deploymentRuntimeCallCount(t, calls) != before {
				t.Fatal("successful same-invocation snapshot was not deduplicated", err)
			}
		})
	}
}

func TestReadRetryRecognizesOnlyActualGetAndPreservesNonReadBudgets(t *testing.T) {
	for _, args := range [][]string{
		{"exec", "original-pod", "--", "get", "private-canary"},
		{"exec", "original-pod", "--", "sh", "-c", "get"},
		{"create", "configmap", "get", "-o", "json"},
		{"replace", "-f", "get"},
		{"auth", "can-i", "get", "secret"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			arguments := filepath.Join(t.TempDir(), "arguments")
			t.Setenv("RTK_CHECK_ARGUMENTS", arguments)
			calls := deploymentRuntimeTestKubectl(t, `printf '%s\n' "$@" > "$RTK_CHECK_ARGUMENTS"
printf 'TLS handshake timeout private-canary' >&2; exit 1
`)
			runtime := newDeploymentCheckRuntime(context.Background(), "dev")
			_, err := runtime.kubectl(false, args...)
			deploymentRuntimeCode(t, err, "KUBE_TIMEOUT")
			raw, _ := os.ReadFile(arguments)
			if deploymentRuntimeCallCount(t, calls) != 1 || len(runtime.cache) != 0 || !strings.Contains(string(raw), "--request-timeout=10s\n") || strings.Contains(err.Error(), "private-canary") {
				t.Fatal("nested get/mutation/auth was cached, retried or broadened", err)
			}
		})
	}
	for _, args := range [][]string{{"get", "pods"}, {"--kubeconfig", "fixture", "-n", "original", "get", "pods"}, {"--namespace=original", "get", "pod", "original-pod"}} {
		if deploymentKubectlGetIndex(args) < 0 {
			t.Fatal("actual API GET was not recognized", args)
		}
	}
}

func TestReadRetryCancellationDuringDelayDoesNotStartAnotherGetOrReuseCache(t *testing.T) {
	calls := deploymentRuntimeTestKubectl(t, "printf 'Service Unavailable private-canary' >&2; exit 1\n")
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	runtime := newDeploymentCheckRuntime(ctx, "dev")
	runtime.retryDelay = time.Second
	count := func() int {
		raw, err := os.ReadFile(calls)
		if os.IsNotExist(err) {
			return 0 // A short parent deadline can expire before process startup.
		}
		if err != nil {
			t.Fatal(err)
		}
		return strings.Count(string(raw), "call\n")
	}
	_, err := runtime.kubectl(false, "get", "pods", "-o", "json")
	deploymentRuntimeCode(t, err, "KUBE_TIMEOUT")
	if count() > 1 || len(runtime.cache) != 0 {
		t.Fatal("parent deadline continued retries or retained failed snapshot", err)
	}
	before := count()
	_, err = runtime.kubectl(false, "get", "pods", "-o", "json")
	deploymentRuntimeCode(t, err, "KUBE_TIMEOUT")
	if count() != before {
		t.Fatal("cancelled invocation loaded another snapshot")
	}
	defaults := newDeploymentCheckRuntime(context.Background(), "dev")
	if defaults.requestTimeout != 30*time.Second || defaults.processTimeout != 45*time.Second || defaults.sqlTimeout != 30*time.Second || environmentDeploymentCheckConcurrency != 12 {
		t.Fatal("shared GET budget, SQL budget or concurrency contract changed incorrectly")
	}
}

func TestReadRetryExecRetainsEarlierExplicitProcessBudgetWithoutRetry(t *testing.T) {
	calls := deploymentRuntimeTestKubectl(t, "sleep 2\n")
	runtime := newDeploymentCheckRuntime(context.Background(), "dev")
	runtime.processTimeout = 150 * time.Millisecond
	started := time.Now()
	_, err := runtime.kubectl(false, "exec", "original-pod", "--", "stat", "get")
	deploymentRuntimeCode(t, err, "KUBE_TIMEOUT")
	if time.Since(started) > time.Second || len(runtime.cache) != 0 {
		t.Fatal("exec ignored an explicit earlier budget or cached its failure", err)
	}
	raw, readErr := os.ReadFile(calls)
	if readErr != nil && !os.IsNotExist(readErr) || strings.Count(string(raw), "call\n") > 1 {
		t.Fatal("exec timeout was retried", readErr)
	}
}

func TestReadRetryBackendCommandsBoundGetAndNeverReplayMutationOrExec(t *testing.T) {
	for _, tc := range []struct {
		name, diagnostic string
		args             []string
		calls            int
	}{
		{"get transient", "Service Unavailable private-canary", []string{"get", "namespace", "-o", "json"}, 3},
		{"get forbidden", "Forbidden private-canary", []string{"get", "namespace", "-o", "json"}, 1},
		{"get unauthorized", "Unauthorized private-canary", []string{"get", "namespace", "-o", "json"}, 1},
		{"get missing", "NotFound private-canary", []string{"get", "namespace", "-o", "json"}, 1},
		{"mutation named get", "Service Unavailable private-canary", []string{"create", "configmap", "get"}, 1},
		{"nested get", "Service Unavailable private-canary", []string{"exec", "original-pod", "--", "get", "original"}, 1},
		{"auth get", "Service Unavailable private-canary", []string{"auth", "can-i", "get", "secret"}, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			calls := deploymentRuntimeTestKubectl(t, `printf '%s\n' "$*" >> "$RTK_READ_RETRY_ARGS"
printf '%s' "$RTK_READ_RETRY_DIAGNOSTIC" >&2; exit 1
`)
			if err := os.Symlink(os.Getenv("RTK_CLOUD_KUBECTL"), filepath.Join(dir, "kubectl")); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
			t.Setenv("RTK_READ_RETRY_ARGS", filepath.Join(dir, "arguments"))
			t.Setenv("RTK_READ_RETRY_DIAGNOSTIC", tc.diagnostic)
			config, _ := yaml.Marshal(frozenKubernetesFixture(t))
			backend, err := newEnvironmentKubernetesBackend(string(config), "dev")
			if err != nil {
				t.Fatal(err)
			}
			_, err = backend.command(context.Background(), nil, tc.args...)
			if err == nil || deploymentRuntimeCallCount(t, calls) != tc.calls || strings.Contains(err.Error(), "private-canary") {
				t.Fatal("backend replayed a non-read or lost its bounded safe failure", err)
			}
			raw, _ := os.ReadFile(filepath.Join(dir, "arguments"))
			budget := "--request-timeout=90s"
			if deploymentKubectlGetIndex(tc.args) >= 0 {
				budget = "--request-timeout=30s"
			}
			if !strings.Contains(string(raw), budget) {
				t.Fatal("GET budget changed a mutation/exec budget", string(raw))
			}
			assertPlanAccessPrivateFilesRemoved(t, backend)
		})
	}
}

func TestReadRetryBackendInventoryGetRecoversOnThirdAttempt(t *testing.T) {
	dir := t.TempDir()
	calls := deploymentRuntimeTestKubectl(t, `count=$(wc -l < "$RTK_CHECK_CALLS")
if [ "$count" -lt 3 ]; then printf 'TooManyRequests private-canary' >&2; exit 1; fi
printf '%s' '{"items":[]}'
`)
	if err := os.Symlink(os.Getenv("RTK_CLOUD_KUBECTL"), filepath.Join(dir, "kubectl")); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	config, _ := yaml.Marshal(frozenKubernetesFixture(t))
	backend, err := newEnvironmentKubernetesBackend(string(config), "dev")
	if err != nil {
		t.Fatal(err)
	}
	raw, err := backend.command(context.Background(), nil, "get", "namespace", "-o", "json")
	if err != nil || string(raw) != `{"items":[]}` || deploymentRuntimeCallCount(t, calls) != 3 {
		t.Fatal("bounded inventory GET did not obtain fresh third-attempt evidence", err)
	}
	assertPlanAccessPrivateFilesRemoved(t, backend)
}

func TestControllerIdentityReadFailuresKeepTransportCodeAndRepair(t *testing.T) {
	for _, verb := range []string{"get replicasets", "get pods"} {
		for _, code := range []string{"KUBE_TIMEOUT", "KUBE_FORBIDDEN", "KUBE_UNAVAILABLE"} {
			t.Run(verb+"/"+code, func(t *testing.T) {
				resource := identityPermissionFixture("dev")
				plan := &deploymentplan.Plan{Resources: []deploymentplan.Resource{resource}}
				err := checkControllerIdentityPermissions(plan, "isolated", identityPermissionAlpine, func(args ...string) ([]byte, error) {
					if strings.Contains(strings.Join(args, " "), verb) {
						return nil, fmt.Errorf("private-canary: %w", &deploymentRuntimeError{code, "required metadata read failed"})
					}
					return []byte(`{"items":[]}`), nil
				})
				var blocker *deploymentplan.Blocker
				if !errors.As(err, &blocker) || !strings.Contains(blocker.Cause, "["+code+"]") || !strings.Contains(blocker.Next, "API access and availability") || strings.Contains(blocker.Next, "repair private directory") || strings.Contains(err.Error(), "private-canary") || blocker.Resource != resource.ID {
					t.Fatal("failed metadata GET became a chmod recommendation or lost its safe API diagnosis", err)
				}
			})
		}
	}
}
