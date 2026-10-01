package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func otaCoreReadyFixturePods(t *testing.T, deployment map[string]any) []map[string]any {
	t.Helper()
	template := deployment["spec"].(map[string]any)["template"]
	body, err := json.Marshal(template)
	if err != nil {
		t.Fatal(err)
	}
	pods := make([]map[string]any, 2)
	for i := range pods {
		if err := json.Unmarshal(body, &pods[i]); err != nil {
			t.Fatal(err)
		}
		pods[i]["metadata"] = map[string]any{}
		pods[i]["status"] = map[string]any{"phase": "Running", "conditions": []any{map[string]any{"type": "Ready", "status": "True"}}}
	}
	return pods
}

func otaCoreCurrentFixture(t *testing.T) (map[string]any, []map[string]any) {
	t.Helper()
	deployment := otaCoreDeploymentFixture()
	patch, _, err := otaCoreCutoverPatch(deployment, testNewOTAAPIImage, testOTAUpstream)
	if err != nil {
		t.Fatal(err)
	}
	applyOTACoreFixturePatch(t, deployment, patch)
	return deployment, otaCoreReadyFixturePods(t, deployment)
}

func TestOTACoreCurrentReadyRejectsStaleOrOverlappingProcesses(t *testing.T) {
	app := func(pod map[string]any) map[string]any {
		return pod["spec"].(map[string]any)["containers"].([]any)[1].(map[string]any)
	}
	setEnv := func(pod map[string]any, name, value string) {
		for _, raw := range app(pod)["env"].([]any) {
			entry := raw.(map[string]any)
			if entry["name"] == name {
				entry["value"] = value
			}
		}
	}
	for _, tc := range []struct {
		name, want string
		change     func(map[string]any, *[]map[string]any)
	}{
		{name: "selected current ready replicas"},
		{name: "missing generation", want: "current positive replica revision", change: func(d map[string]any, _ *[]map[string]any) { delete(d["metadata"].(map[string]any), "generation") }},
		{name: "template changed controller not observed", want: "current positive replica revision", change: func(d map[string]any, _ *[]map[string]any) { d["metadata"].(map[string]any)["generation"] = float64(2) }},
		{name: "zero replicas cannot qualify", want: "current positive replica revision", change: func(d map[string]any, _ *[]map[string]any) { d["spec"].(map[string]any)["replicas"] = float64(0) }},
		{name: "new desired template old Ready Pod", want: "live Pod: app image", change: func(_ map[string]any, p *[]map[string]any) { app((*p)[0])["image"] = testOldOTAAPIImage }},
		{name: "old live cutover flag", want: "VIDEO_CLOUD_OTA_SERVICE_CUTOVER_ENABLED", change: func(_ map[string]any, p *[]map[string]any) {
			setEnv((*p)[0], "VIDEO_CLOUD_OTA_SERVICE_CUTOVER_ENABLED", "false")
		}},
		{name: "old live upstream", want: "VIDEO_CLOUD_OTA_UPSTREAM_URL", change: func(_ map[string]any, p *[]map[string]any) {
			setEnv((*p)[0], "VIDEO_CLOUD_OTA_UPSTREAM_URL", "http://old")
		}},
		{name: "live entitlement enforcement off", want: "VIDEO_CLOUD_OTA_ENTITLEMENTS_REQUIRED", change: func(_ map[string]any, p *[]map[string]any) {
			setEnv((*p)[0], "VIDEO_CLOUD_OTA_ENTITLEMENTS_REQUIRED", "false")
		}},
		{name: "duplicate live flag", want: "VIDEO_CLOUD_OTA_SERVICE_CUTOVER_ENABLED", change: func(_ map[string]any, p *[]map[string]any) {
			c := app((*p)[0])
			c["env"] = append(c["env"].([]any), map[string]any{"name": "VIDEO_CLOUD_OTA_SERVICE_CUTOVER_ENABLED", "value": "true"})
		}},
		{name: "live literal also has valueFrom", want: "VIDEO_CLOUD_OTA_ENTITLEMENTS_REQUIRED", change: func(_ map[string]any, p *[]map[string]any) {
			app((*p)[0])["env"].([]any)[0].(map[string]any)["valueFrom"] = map[string]any{"fieldRef": map[string]any{"fieldPath": "metadata.name"}}
		}},
		{name: "desired template old image", want: "Deployment: app image", change: func(d map[string]any, _ *[]map[string]any) {
			app(d["spec"].(map[string]any)["template"].(map[string]any))["image"] = testOldOTAAPIImage
		}},
		{name: "desired template missing upstream", want: "Deployment: app must declare VIDEO_CLOUD_OTA_UPSTREAM_URL", change: func(d map[string]any, _ *[]map[string]any) {
			c := app(d["spec"].(map[string]any)["template"].(map[string]any))
			c["env"] = c["env"].([]any)[:3]
		}},
		{name: "missing live app", want: "app container is missing", change: func(_ map[string]any, p *[]map[string]any) { app((*p)[0])["name"] = "other" }},
		{name: "duplicate live app", want: "duplicate app containers", change: func(_ map[string]any, p *[]map[string]any) {
			spec := (*p)[0]["spec"].(map[string]any)
			spec["containers"] = append(spec["containers"].([]any), app((*p)[0]))
		}},
		{name: "terminating API still runs", want: "terminating live Pod", change: func(_ map[string]any, p *[]map[string]any) {
			(*p)[0]["metadata"].(map[string]any)["deletionTimestamp"] = "2026-10-02T00:00:00Z"
		}},
		{name: "current API not Ready", want: "not Running and Ready", change: func(_ map[string]any, p *[]map[string]any) {
			(*p)[0]["status"].(map[string]any)["conditions"] = []any{}
		}},
		{name: "current API not Running", want: "not Running and Ready", change: func(_ map[string]any, p *[]map[string]any) { (*p)[0]["status"].(map[string]any)["phase"] = "Pending" }},
		{name: "not enough live Pods", want: "live Pod count", change: func(_ map[string]any, p *[]map[string]any) { *p = (*p)[:1] }},
		{name: "extra current Pod overlaps", want: "live Pod count", change: func(_ map[string]any, p *[]map[string]any) { *p = append(*p, (*p)[0]) }},
		{name: "completed old Pod cannot serve", change: func(_ map[string]any, p *[]map[string]any) {
			(*p)[0]["status"].(map[string]any)["phase"] = "Succeeded"
			app((*p)[0])["image"] = testOldOTAAPIImage
			*p = append(*p, (*p)[1])
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d, pods := otaCoreCurrentFixture(t)
			if tc.change != nil {
				tc.change(d, &pods)
			}
			err := otaCoreCutoverCurrentReady(d, pods, testNewOTAAPIImage, testOTAUpstream)
			if (err != nil) != (tc.want != "") || (tc.want != "" && !strings.Contains(err.Error(), tc.want)) {
				t.Fatalf("qualification = %v; want %q", err, tc.want)
			}
		})
	}
	for _, counter := range []string{"replicas", "updatedReplicas", "availableReplicas", "readyReplicas", "unavailableReplicas", "terminatingReplicas"} {
		t.Run("incomplete controller "+counter, func(t *testing.T) {
			d, pods := otaCoreCurrentFixture(t)
			d["status"].(map[string]any)[counter] = float64(1)
			if err := otaCoreCutoverCurrentReady(d, pods, testNewOTAAPIImage, testOTAUpstream); err == nil || !strings.Contains(err.Error(), counter) {
				t.Fatalf("incomplete %s accepted: %v", counter, err)
			}
		})
	}
}

func TestOTACoreCutoverReadOnlyRequiresLiveRevisionAndNeverMutates(t *testing.T) {
	for _, scenario := range []string{"current", "old Pod", "not observed", "Pod inspection error"} {
		t.Run(scenario, func(t *testing.T) {
			f := newOTACoreCommandFixtureForEnvironment(t, "staging")
			if err := runDeploymentOTACoreCutoverWithOps(append(f.args, "--confirm", "video-cloud-staging"), f.ops); err != nil {
				t.Fatal(err)
			}
			readOnlyBindings := 0
			f.ops.readOnlyCredentials = func(environment string) (func(), error) {
				if environment != "staging" {
					t.Fatal("wrong read-only environment")
				}
				readOnlyBindings++
				return func() {}, nil
			}
			f.ops.credentials = func(string) (func(), error) { t.Fatal("read-only used mutating credentials"); return nil, nil }
			switch scenario {
			case "old Pod":
				f.pods = otaCoreReadyFixturePods(t, f.deployment)
				f.pods[0]["spec"].(map[string]any)["containers"].([]any)[1].(map[string]any)["image"] = testOldOTAAPIImage
			case "not observed":
				f.deployment["metadata"].(map[string]any)["generation"] = float64(2)
			case "Pod inspection error":
				f.fail = "pods"
			}
			err := runDeploymentOTACoreCutoverWithOps(append(f.args, "--read-only"), f.ops)
			if (err == nil) != (scenario == "current") || readOnlyBindings != 1 || f.patches != 1 || f.rollouts != 1 || f.persisted != 1 {
				t.Fatalf("read-only: err=%v bindings=%d writes=%d/%d/%d", err, readOnlyBindings, f.patches, f.rollouts, f.persisted)
			}
		})
	}
}

func TestOTACorePodInspectionOnlyGetsSelectedNamespace(t *testing.T) {
	bin, calls := t.TempDir(), filepath.Join(t.TempDir(), "calls")
	kubectl := filepath.Join(bin, "kubectl")
	if err := os.WriteFile(kubectl, []byte("#!/bin/sh\nprintf '%s\\n' \"$*\" >> \"$OTA_CORE_GET_CALLS\"\ncase \"$OTA_CORE_GET_MODE\" in\n fail) exit 1 ;;\n invalid) printf 'sensitive invalid json' ;;\n *) printf '{\"items\":[{\"metadata\":{\"name\":\"api-current\"}}]}' ;;\nesac\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("RTK_CLOUD_KUBECTL", kubectl)
	t.Setenv("RTK_CLOUD_KUBECONFIG", "/selected/staging/kubeconfig.yaml")
	t.Setenv("RTK_CLOUD_KUBECTL_REQUEST_TIMEOUT", "10s")
	t.Setenv("OTA_CORE_GET_CALLS", calls)
	for _, mode := range []string{"ok", "fail", "invalid"} {
		t.Setenv("OTA_CORE_GET_MODE", mode)
		pods, err := otaCoreCutoverPods("video-cloud-staging-video-cloud")
		if mode == "ok" {
			if err != nil || len(pods) != 1 || pods[0]["metadata"].(map[string]any)["name"] != "api-current" {
				t.Fatalf("Pod GET: %v %+v", err, pods)
			}
		} else if err == nil || strings.Contains(err.Error(), "sensitive") {
			t.Fatalf("inspection failed open or exposed body: %v", err)
		}
	}
	body, err := os.ReadFile(calls)
	if err != nil {
		t.Fatal(err)
	}
	for _, call := range strings.Split(strings.TrimSpace(string(body)), "\n") {
		if call != "--kubeconfig /selected/staging/kubeconfig.yaml --request-timeout=10s -n video-cloud-staging-video-cloud get pods -l app.kubernetes.io/name=video-cloud-api -o json" {
			t.Fatalf("qualification used non-read-only/wrong target command: %s", call)
		}
	}
}
