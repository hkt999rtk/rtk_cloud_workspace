package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func TestNativeQueryProgressShowsOnlySafeActionAndResource(t *testing.T) {
	for _, environment := range []string{"dev", "staging", "prod"} {
		t.Run(environment, func(t *testing.T) {
			raw, err := yaml.Marshal(frozenKubernetesFixture(t))
			if err != nil {
				t.Fatal(err)
			}
			makeIsolatedTestSecretStore(t, environment)
			backend, err := newEnvironmentKubernetesBackend(string(raw), environment)
			if err != nil {
				t.Fatal(err)
			}
			directory := t.TempDir()
			response := filepath.Join(directory, "response.json")
			calls := filepath.Join(directory, "calls")
			script := `#!/bin/sh
printf 'call\n' >> "$RTK_QUERY_PROGRESS_CALLS"
/bin/sleep 0.05
/bin/cat "$RTK_QUERY_PROGRESS_RESPONSE"
`
			kubectl := filepath.Join(directory, "kubectl")
			writeTestFile(t, kubectl, script)
			if err := os.Chmod(kubectl, 0755); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", directory+string(os.PathListSeparator)+os.Getenv("PATH"))
			t.Setenv("RTK_CLOUD_KUBECTL", kubectl)
			t.Setenv("RTK_QUERY_PROGRESS_CALLS", calls)
			t.Setenv("RTK_QUERY_PROGRESS_RESPONSE", response)
			namespace := "video-cloud-" + environment + "-platform"
			object := deploymentplan.Object{"apiVersion": "v1", "kind": "ConfigMap", "metadata": map[string]any{"namespace": namespace, "name": "settings", "uid": "original", "resourceVersion": "4"}, "data": map[string]any{"private": "private-native-progress-marker"}}
			encoded, _ := json.Marshal(object)
			writeTestFile(t, response, string(encoded))
			secret, _ := json.Marshal(deploymentplan.Object{"apiVersion": "v1", "kind": "Secret", "metadata": map[string]any{"namespace": namespace, "name": "runtime"}, "stringData": map[string]any{"token": "private-native-progress-marker"}})
			permissions, _ := json.Marshal(deploymentplan.Object{"apiVersion": "authorization.k8s.io/v1", "kind": "SelfSubjectAccessReview", "spec": map[string]any{"resourceAttributes": map[string]any{"namespace": namespace, "name": "settings", "resource": "configmaps", "verb": "get"}}})
			updatePermissions, _ := json.Marshal(deploymentplan.Object{"apiVersion": "authorization.k8s.io/v1", "kind": "SelfSubjectAccessReview", "spec": map[string]any{"resourceAttributes": map[string]any{"namespace": namespace, "name": "settings", "resource": "configmaps", "verb": "update"}}})
			unknownPermissions, _ := json.Marshal(deploymentplan.Object{"apiVersion": "authorization.k8s.io/v1", "kind": "SelfSubjectAccessReview", "spec": map[string]any{"resourceAttributes": map[string]any{"namespace": namespace, "name": "settings", "resource": "configmaps", "verb": "private-native-progress-marker"}}})
			cases := []struct {
				name, stage, want string
				input             []byte
				args              []string
				read              bool
			}{
				{"inventory", "deployment.inventory.read", "Inspect resource inventory Namespace//" + namespace, nil, []string{"get", environmentNamespacedInventoryKinds, "--namespace", namespace, "-o", "json", "--opaque=private-native-progress-marker"}, false},
				{"resource-read", "deployment.target", "Read current resource ConfigMap/" + namespace + "/settings", nil, nil, true},
				{"dry-run", "deployment.execution.admission", "Test exact resource body without saving Secret/" + namespace + "/runtime", secret, []string{"create", "-f", "-", "-o", "json", "--dry-run=server"}, false},
				{"apply", "deployment.execution.configuration.apply", "Save approved resource body Secret/" + namespace + "/runtime", secret, []string{"replace", "-f", "-", "-o", "json"}, false},
				{"permissions-get", "deployment.execution.permissions", "Check Kubernetes get permission ConfigMap/" + namespace + "/settings", permissions, []string{"create", "--raw=/apis/authorization.k8s.io/v1/selfsubjectaccessreviews", "-f", "-"}, false},
				{"permissions-update", "deployment.execution.permissions", "Check Kubernetes update permission ConfigMap/" + namespace + "/settings", updatePermissions, []string{"create", "--raw=/apis/authorization.k8s.io/v1/selfsubjectaccessreviews", "-f", "-"}, false},
				{"permissions-unknown", "deployment.execution.permissions", "Check Kubernetes permissions ConfigMap/" + namespace + "/settings", unknownPermissions, []string{"create", "--raw=/apis/authorization.k8s.io/v1/selfsubjectaccessreviews", "-f", "-"}, false},
				{"named-permission", "deployment.execution.lease", "Check Kubernetes delete permission Lease/" + namespace + "/rtk-deployment-execution", nil, []string{"auth", "can-i", "delete", "leases.coordination.k8s.io/rtk-deployment-execution", "--namespace", namespace}, false},
				{"conditional-delete", "deployment.execution.admission", "Test exact resource body without saving Deployment/" + namespace + "/app", []byte(`{"kind":"DeleteOptions","preconditions":{"uid":"private-native-progress-marker"}}`), []string{"delete", "--raw=/apis/apps/v1/namespaces/" + namespace + "/deployments/app?dryRun=All", "-f", "-"}, false},
			}
			for _, tc := range cases {
				t.Run(tc.name, func(t *testing.T) {
					var out bytes.Buffer
					ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
					defer cancel()
					err := runEnvironmentProgressInterval(ctx, &out, tc.stage, time.Millisecond, func(ctx context.Context) error {
						if tc.read {
							_, failure := backend.readOwner(ctx, "ConfigMap/"+namespace+"/settings")
							if failure != nil {
								return fmt.Errorf("read fixture failed [%s]", failure.code)
							}
							return nil
						}
						_, err := backend.command(ctx, tc.input, tc.args...)
						return err
					})
					if err != nil || !strings.Contains(out.String(), tc.want) || !strings.Contains(out.String(), "active=1") {
						t.Fatal("native work was not described while active", err, out.String())
					}
					for _, marker := range []string{"private-native-progress-marker", "private-frozen-token", "kubeconfig.yaml", "--opaque", environmentNamespacedInventoryKinds} {
						if strings.Contains(out.String(), marker) {
							t.Fatal("native progress exposed private input or raw arguments", marker)
						}
					}
					assertPlanAccessPrivateFilesRemoved(t, backend)
				})
			}
			log, err := os.ReadFile(calls)
			if err != nil || strings.Count(string(log), "call\n") != len(cases) {
				t.Fatal("progress instrumentation changed native API request count", err, string(log))
			}
		})
	}
}

func TestNativeAccessProgressDistinguishesConcurrentVerbsForSameResource(t *testing.T) {
	makeIsolatedTestSecretStore(t, "dev")
	kube, err := yaml.Marshal(frozenKubernetesFixture(t))
	if err != nil {
		t.Fatal(err)
	}
	backend, err := newEnvironmentKubernetesBackend(string(kube), "dev")
	if err != nil {
		t.Fatal(err)
	}
	directory := t.TempDir()
	calls := filepath.Join(directory, "calls")
	requests := filepath.Join(directory, "requests")
	kubectl := filepath.Join(directory, "kubectl")
	writeTestFile(t, kubectl, `#!/bin/sh
shift 3
test "$*" = 'create --raw=/apis/authorization.k8s.io/v1/selfsubjectaccessreviews -f -' || exit 91
printf 'call\n' >> "$RTK_ACCESS_PROGRESS_CALLS"
python3 -c 'import json,os,sys; o=json.load(sys.stdin); assert o["kind"]=="SelfSubjectAccessReview"; a=o["spec"]["resourceAttributes"]; assert a["resource"]=="configmaps" and a["name"]=="settings" and a["namespace"]=="video-cloud-dev-platform" and a["verb"] in ["get","update"]; f=open(os.environ["RTK_ACCESS_PROGRESS_REQUESTS"],"a"); f.write(a["verb"]+"\n"); f.close()' || exit 92
/bin/sleep 0.2
printf '%s' '{"status":{"allowed":true,"reason":"private-permission-response"}}'
`)
	if err := os.Chmod(kubectl, 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", directory+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("RTK_CLOUD_KUBECTL", kubectl)
	t.Setenv("RTK_ACCESS_PROGRESS_CALLS", calls)
	t.Setenv("RTK_ACCESS_PROGRESS_REQUESTS", requests)
	var out bytes.Buffer
	err = runEnvironmentProgressInterval(context.Background(), &out, "deployment.execution.permissions", time.Millisecond, func(ctx context.Context) error {
		var joined sync.WaitGroup
		failures := make(chan error, 2)
		for _, verb := range []string{"get", "update"} {
			joined.Add(1)
			go func() {
				defer joined.Done()
				failures <- backend.checkEnvironmentAccess(ctx, environmentAccessRequirement{Resource: "configmaps", Namespace: "video-cloud-dev-platform", Name: "settings", Verb: verb}, "")
			}()
		}
		joined.Wait()
		close(failures)
		for err := range failures {
			if err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal("permission presentation changed access review outcome", err, out.String())
	}
	sameResource := "ConfigMap/video-cloud-dev-platform/settings"
	distinct := false
	for _, line := range strings.Split(out.String(), "\n") {
		if strings.Contains(line, "active=2") && strings.Contains(line, "Check Kubernetes get permission "+sameResource) && strings.Contains(line, "Check Kubernetes update permission "+sameResource) {
			distinct = true
		}
	}
	if !distinct || strings.Contains(out.String(), "private-") {
		t.Fatal("same resource's independent access reviews were indistinguishable or exposed private data", out.String())
	}
	observedCalls, err := os.ReadFile(calls)
	if err != nil || strings.Count(string(observedCalls), "call\n") != 2 {
		t.Fatal("permission display changed request count", err, string(observedCalls))
	}
	observedRequests, err := os.ReadFile(requests)
	if err != nil || strings.Count(string(observedRequests), "get\n") != 1 || strings.Count(string(observedRequests), "update\n") != 1 {
		t.Fatal("permission display changed exact authorization verbs or identities", err, string(observedRequests))
	}
	assertPlanAccessPrivateFilesRemoved(t, backend)
}

func TestNativeCommandClassifiesBoundedPrivateTimeoutDiagnostic(t *testing.T) {
	raw, err := yaml.Marshal(frozenKubernetesFixture(t))
	if err != nil {
		t.Fatal(err)
	}
	backend, err := newEnvironmentKubernetesBackend(string(raw), "dev")
	if err != nil {
		t.Fatal(err)
	}
	directory := t.TempDir()
	kubectl := filepath.Join(directory, "kubectl")
	writeTestFile(t, kubectl, "#!/bin/sh\nprintf 'error: timed out waiting for condition private-timeout-marker\\n' >&2\nprintf '%070000d' 0 >&2\nexit 1\n")
	if err := os.Chmod(kubectl, 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", directory+string(os.PathListSeparator)+os.Getenv("PATH"))
	_, err = backend.command(context.Background(), nil, "get", "namespace", "-o", "json")
	var classified *deploymentRuntimeError
	if !errors.As(err, &classified) || classified.code != "KUBE_TIMEOUT" || strings.Contains(err.Error(), "private-timeout-marker") {
		t.Fatal("command timeout lost its safe classification or exposed private stderr", err)
	}
	var private environmentPrivateAPIErrorOutput
	payload := []byte(strings.Repeat("x", 100000))
	for count := 0; count < 3; count++ {
		if written, err := private.Write(payload); err != nil || written != len(payload) {
			t.Fatal("private diagnostic remainder was not drained", written, err)
		}
	}
	if private.buffer.Len() != 64<<10 {
		t.Fatal("native private stderr exceeded its 64 KiB bound", private.buffer.Len())
	}
	assertPlanAccessPrivateFilesRemoved(t, backend)
	for _, canceled := range []bool{true, false} {
		ctx, cancel := context.WithCancel(context.Background())
		if canceled {
			cancel()
		} else {
			cancel()
			ctx, cancel = context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
		}
		_, err := backend.command(ctx, nil, "get", "namespace", "-o", "json")
		if !errors.Is(err, ctx.Err()) {
			t.Fatal("command replaced original cancellation/deadline error", err, ctx.Err())
		}
		cancel()
	}
}

func TestNativeWaitCommandUsesConfiguredTimeoutAndHonorsEarlierDeadline(t *testing.T) {
	raw, err := yaml.Marshal(frozenKubernetesFixture(t))
	if err != nil {
		t.Fatal(err)
	}
	backend, err := newEnvironmentKubernetesBackend(string(raw), "dev")
	if err != nil {
		t.Fatal(err)
	}
	backend.RolloutTimeout = 300 * time.Second
	directory := t.TempDir()
	log := filepath.Join(directory, "arguments")
	kubectl := filepath.Join(directory, "kubectl")
	writeTestFile(t, kubectl, "#!/bin/sh\nprintf '%s\\n' \"$*\" >> \"$RTK_NATIVE_TIMEOUT_LOG\"\n/bin/sleep 0.05\nprintf '{}\\n'\n")
	if err := os.Chmod(kubectl, 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", directory+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("RTK_NATIVE_TIMEOUT_LOG", log)
	for _, args := range [][]string{
		{"--namespace", "video-cloud-dev-platform", "wait", "--for=condition=complete", "Job/initialize", "--timeout=5m0s"},
		{"--namespace=video-cloud-dev-platform", "rollout", "status", "Deployment/app", "--timeout=5m0s"},
		{"get", "namespace", "-o", "json"},
	} {
		if _, err := backend.command(context.Background(), nil, args...); err != nil {
			t.Fatal(err)
		}
	}
	logged, err := os.ReadFile(log)
	lines := strings.Split(strings.TrimSpace(string(logged)), "\n")
	if err != nil || len(lines) != 3 || !strings.Contains(lines[0], "--request-timeout=5m0s") || !strings.Contains(lines[1], "--request-timeout=5m0s") || !strings.Contains(lines[2], "--request-timeout=30s") {
		t.Fatal("configured native wait deadline altered normal queries or remained hardcoded", err, string(logged))
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Millisecond)
	defer cancel()
	started := time.Now()
	_, err = backend.command(ctx, nil, "--namespace", "video-cloud-dev-platform", "wait", "--for=condition=complete", "Job/initialize", "--timeout=5m0s")
	if !errors.Is(err, context.DeadlineExceeded) || time.Since(started) > 2*time.Second {
		t.Fatal("native wait extended the earlier caller deadline", err)
	}
	backend.RolloutTimeout = 0
	if _, err := backend.command(context.Background(), nil, "wait", "--for=condition=complete", "Job/initialize"); err == nil {
		t.Fatal("native wait invented a timeout without maintained configuration")
	}
	assertPlanAccessPrivateFilesRemoved(t, backend)
}
