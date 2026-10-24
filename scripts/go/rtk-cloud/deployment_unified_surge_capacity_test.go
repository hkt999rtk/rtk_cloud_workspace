package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func surgeCandidateFixture(cpu string) deploymentplan.Resource {
	object := deploymentplan.Object{"apiVersion": "apps/v1", "kind": "Deployment", "metadata": deploymentplan.Object{"name": "worker", "namespace": "video-cloud-dev-video-cloud"}, "spec": deploymentplan.Object{"replicas": float64(1), "strategy": deploymentplan.Object{"type": "RollingUpdate"}, "template": deploymentplan.Object{"spec": deploymentplan.Object{"containers": []any{deploymentplan.Object{"name": "worker", "image": "candidate", "resources": deploymentplan.Object{"requests": deploymentplan.Object{"cpu": cpu, "memory": "128Mi"}}}}}}}}
	baseline := deploymentplan.Clone(object)
	deploymentplan.Map(certIssuerObjectList(environmentPKIPod(baseline)["containers"])[0])["image"] = "original"
	return deploymentplan.Resource{ID: deploymentplan.ID(object), UID: "original-worker", Action: "update", Desired: object, Baseline: baseline}
}

func surgeNodeFixture() deploymentplan.Object {
	return deploymentplan.Object{"kind": "Node", "metadata": deploymentplan.Object{"name": "general-original", "uid": "original-node", "labels": deploymentplan.Object{"rtk.io/node-class": "general", "kubernetes.io/os": "linux", "kubernetes.io/arch": "amd64"}}, "spec": deploymentplan.Object{}, "status": deploymentplan.Object{"allocatable": deploymentplan.Object{"cpu": "2", "memory": "4Gi"}, "conditions": []any{deploymentplan.Object{"type": "Ready", "status": "True"}}}}
}

func surgeReservedPodFixture(cpu string) deploymentplan.Object {
	return deploymentplan.Object{"kind": "Pod", "metadata": deploymentplan.Object{"name": "foreign-reservation", "namespace": "kube-system", "uid": "original-reserved-pod"}, "spec": deploymentplan.Object{"nodeName": "general-original", "containers": []any{deploymentplan.Object{"name": "app", "resources": deploymentplan.Object{"requests": deploymentplan.Object{"cpu": cpu, "memory": "2Gi"}}}}}, "status": deploymentplan.Object{"phase": "Running"}}
}

func assertSurgeBlocker(t *testing.T, err error, resource string) {
	t.Helper()
	var blocker *deploymentplan.Blocker
	if !errors.As(err, &blocker) || blocker.ID != "deployment.execution.surge-capacity" || blocker.Resource != resource || blocker.Next == "" || strings.Contains(err.Error(), "private-canary") {
		t.Fatal("surge failure lost its safe exact-resource repair", err)
	}
}

func TestSingletonSurgeCapacityUsesFullReplacementRequestsWithoutApplicationReadiness(t *testing.T) {
	for _, tc := range []struct {
		name, request, reserved string
		pass                    bool
	}{
		{"500m does not fit 400m", "500m", "1600m", false},
		{"500m fits 500m", "500m", "1500m", true},
		{"100m fits same original reservations", "100m", "1600m", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := surgeCandidateFixture(tc.request)
			for _, state := range []string{"running", "not-ready", "terminating", "assigned-pending"} {
				pod := surgeReservedPodFixture(tc.reserved)
				switch state {
				case "not-ready":
					deploymentplan.Map(pod["status"])["conditions"] = []any{deploymentplan.Object{"type": "Ready", "status": "False"}}
				case "terminating":
					deploymentplan.Map(pod["metadata"])["deletionTimestamp"] = "now"
				case "assigned-pending":
					deploymentplan.Map(pod["status"])["phase"] = "Pending"
				}
				err := verifyEnvironmentSurgeCapacity([]deploymentplan.Resource{r}, []deploymentplan.Object{surgeNodeFixture()}, []deploymentplan.Object{pod})
				if (err == nil) != tc.pass {
					t.Fatal("application state changed its assigned request reservation", state, err)
				}
				if err != nil {
					assertSurgeBlocker(t, err, r.ID)
				}
			}
		})
	}
	// CPU headroom alone is insufficient; memory is checked in native bytes.
	r := surgeCandidateFixture("100m")
	node := surgeNodeFixture()
	deploymentplan.Map(deploymentplan.Map(node["status"])["allocatable"])["memory"] = "2Gi"
	assertSurgeBlocker(t, verifyEnvironmentSurgeCapacity([]deploymentplan.Resource{r}, []deploymentplan.Object{node}, []deploymentplan.Object{surgeReservedPodFixture("100m")}), r.ID)
	for _, phase := range []string{"Succeeded", "Failed"} {
		pod := surgeReservedPodFixture("2")
		deploymentplan.Map(pod["status"])["phase"] = phase
		if err := verifyEnvironmentSurgeCapacity([]deploymentplan.Resource{r}, []deploymentplan.Object{surgeNodeFixture()}, []deploymentplan.Object{pod}); err != nil {
			t.Fatal("terminal reservation was retained", err)
		}
	}
}

func TestSingletonSurgeSelectionPreservesUnrelatedRolloutProfiles(t *testing.T) {
	for _, mode := range []string{"default", "Recreate", "zero-surge", "allow-unavailable", "zero-replicas", "multiple-replicas", "retain", "create", "unchanged-template"} {
		t.Run(mode, func(t *testing.T) {
			r := surgeCandidateFixture("500m")
			spec := deploymentplan.Map(r.Desired["spec"])
			switch mode {
			case "Recreate":
				spec["strategy"] = deploymentplan.Object{"type": "Recreate"}
			case "zero-surge":
				spec["strategy"] = deploymentplan.Object{"type": "RollingUpdate", "rollingUpdate": deploymentplan.Object{"maxSurge": float64(0), "maxUnavailable": float64(1)}}
			case "allow-unavailable":
				spec["strategy"] = deploymentplan.Object{"type": "RollingUpdate", "rollingUpdate": deploymentplan.Object{"maxSurge": "25%", "maxUnavailable": "100%"}}
			case "zero-replicas":
				spec["replicas"] = float64(0)
			case "multiple-replicas":
				spec["replicas"] = float64(3)
			case "retain", "create":
				r.Action = mode
			case "unchanged-template":
				r.Baseline = deploymentplan.Clone(r.Desired)
			}
			before := deploymentplan.Digest(r)
			candidates, err := environmentSurgeCandidates(&deploymentplan.Plan{Resources: []deploymentplan.Resource{r}})
			if err != nil || (len(candidates) == 1) != (mode == "default") || before != deploymentplan.Digest(r) {
				t.Fatal("selection changed unrelated profile or misread singleton percent rounding", mode, err)
			}
		})
	}
}

func TestSingletonSurgeRejectsUnsuitableNodesAndUnsupportedHardConstraints(t *testing.T) {
	for _, mode := range []string{"foreign-selector", "cordoned", "unready", "terminating-node", "wrong-platform", "tainted", "node-name", "hard-affinity", "hard-spread", "host-port", "runtime-class", "persistent-volume", "pod-resources", "extended-resource"} {
		t.Run(mode, func(t *testing.T) {
			r, node := surgeCandidateFixture("100m"), surgeNodeFixture()
			pod := environmentPKIPod(r.Desired)
			pod["nodeSelector"] = deploymentplan.Object{"rtk.io/node-class": "general"}
			switch mode {
			case "foreign-selector":
				deploymentplan.Map(deploymentplan.Map(node["metadata"])["labels"])["rtk.io/node-class"] = "database"
			case "cordoned":
				deploymentplan.Map(node["spec"])["unschedulable"] = true
			case "unready":
				deploymentplan.Map(certIssuerObjectList(deploymentplan.Map(node["status"])["conditions"])[0])["status"] = "False"
			case "terminating-node":
				deploymentplan.Map(node["metadata"])["deletionTimestamp"] = "now"
			case "wrong-platform":
				deploymentplan.Map(deploymentplan.Map(node["metadata"])["labels"])["kubernetes.io/arch"] = "arm64"
			case "tainted":
				deploymentplan.Map(node["spec"])["taints"] = []any{deploymentplan.Object{"key": "dedicated", "value": "database", "effect": "NoSchedule"}}
			case "node-name":
				pod["nodeName"] = "foreign-node"
			case "hard-affinity":
				pod["affinity"] = deploymentplan.Object{"nodeAffinity": deploymentplan.Object{"requiredDuringSchedulingIgnoredDuringExecution": deploymentplan.Object{"nodeSelectorTerms": []any{}}}}
			case "hard-spread":
				pod["topologySpreadConstraints"] = []any{deploymentplan.Object{"whenUnsatisfiable": "DoNotSchedule"}}
			case "host-port":
				deploymentplan.Map(certIssuerObjectList(pod["containers"])[0])["ports"] = []any{deploymentplan.Object{"hostPort": float64(8080)}}
			case "runtime-class":
				pod["runtimeClassName"] = "special"
			case "persistent-volume":
				pod["volumes"] = []any{deploymentplan.Object{"persistentVolumeClaim": deploymentplan.Object{"claimName": "original"}}}
			case "pod-resources":
				pod["resources"] = deploymentplan.Object{"requests": deploymentplan.Object{"cpu": "1"}}
			case "extended-resource":
				deploymentplan.Map(deploymentplan.Map(deploymentplan.Map(certIssuerObjectList(pod["containers"])[0])["resources"])["requests"])["example.com/gpu"] = "1"
			}
			assertSurgeBlocker(t, verifyEnvironmentSurgeCapacity([]deploymentplan.Resource{r}, []deploymentplan.Object{node}, nil), r.ID)
		})
	}
	for _, effect := range []string{"NoSchedule", "NoExecute"} {
		r, node := surgeCandidateFixture("100m"), surgeNodeFixture()
		deploymentplan.Map(node["spec"])["taints"] = []any{deploymentplan.Object{"key": "dedicated", "value": "general", "effect": effect}}
		environmentPKIPod(r.Desired)["tolerations"] = []any{deploymentplan.Object{"key": "dedicated", "operator": "Equal", "value": "general", "effect": effect}}
		if err := verifyEnvironmentSurgeCapacity([]deploymentplan.Resource{r}, []deploymentplan.Object{node}, nil); err != nil {
			t.Fatal("matching native toleration ignored", err)
		}
	}
}

func TestSingletonSurgeEffectiveRequestsIncludeOrderedInitSidecarsAndOverhead(t *testing.T) {
	pod := environmentPKIPod(surgeCandidateFixture("100m").Desired)
	container := func(name, cpu, memory string, sidecar bool) deploymentplan.Object {
		c := deploymentplan.Object{"name": name, "resources": deploymentplan.Object{"requests": deploymentplan.Object{"cpu": cpu, "memory": memory}}}
		if sidecar {
			c["restartPolicy"] = "Always"
		}
		return c
	}
	pod["initContainers"] = []any{container("sidecar-first", "50m", "32Mi", true), container("preparation", "300m", "256Mi", false), container("sidecar-last", "70m", "32Mi", true)}
	pod["overhead"] = deploymentplan.Object{"cpu": "25m", "memory": "16Mi"}
	request, ok := environmentEffectivePodRequest(pod)
	if !ok || request.cpu != 375 || request.memory != 304<<20 {
		t.Fatal("init sidecar overlap or overhead was underestimated", request, ok)
	}
	request, ok = environmentSurgeRequests(deploymentplan.Object{"memory": "1.5Mi", "cpu": "0.0005"}, nil)
	if !ok || request.cpu != 1 || request.memory != 1572864 {
		t.Fatal("native quantity rounding lost request bytes", request, ok)
	}
	for _, raw := range []string{"NaN", "Inf", "-1", "9999999999999999999999"} {
		if _, ok = environmentSurgeRequests(deploymentplan.Object{"cpu": raw, "memory": "128Mi"}, nil); ok {
			t.Fatal("invalid/overflowed native request accepted", raw)
		}
	}
}

func TestSingletonSurgeRejectsIncompleteNativeReservationEvidence(t *testing.T) {
	for _, mode := range []string{"duplicate-node", "duplicate-pod", "missing-pod-uid", "unknown-assigned-node", "missing-containers", "malformed-requests", "malformed-init", "malformed-allocation", "ambiguous-node-ready"} {
		t.Run(mode, func(t *testing.T) {
			r, node, pod := surgeCandidateFixture("100m"), surgeNodeFixture(), surgeReservedPodFixture("1600m")
			nodes, pods := []deploymentplan.Object{node}, []deploymentplan.Object{pod}
			switch mode {
			case "duplicate-node":
				nodes = append(nodes, node)
			case "duplicate-pod":
				pods = append(pods, pod)
			case "missing-pod-uid":
				delete(deploymentplan.Map(pod["metadata"]), "uid")
			case "unknown-assigned-node":
				deploymentplan.Map(pod["spec"])["nodeName"] = "unobserved"
			case "missing-containers":
				delete(deploymentplan.Map(pod["spec"]), "containers")
			case "malformed-requests":
				deploymentplan.Map(deploymentplan.Map(certIssuerObjectList(deploymentplan.Map(pod["spec"])["containers"])[0])["resources"])["requests"] = "private-canary"
			case "malformed-init":
				deploymentplan.Map(pod["spec"])["initContainers"] = "private-canary"
			case "malformed-allocation":
				deploymentplan.Map(pod["status"])["containerStatuses"] = []any{deploymentplan.Object{"name": "app", "allocatedResources": "private-canary"}}
			case "ambiguous-node-ready":
				deploymentplan.Map(node["status"])["conditions"] = append(certIssuerObjectList(deploymentplan.Map(node["status"])["conditions"]), deploymentplan.Object{"type": "Ready", "status": "False"})
			}
			assertSurgeBlocker(t, verifyEnvironmentSurgeCapacity([]deploymentplan.Resource{r}, nodes, pods), r.ID)
		})
	}
}

func TestSingletonSurgeReservationsTakeMaxOfSpecActualAndAllocatedRequests(t *testing.T) {
	for _, tc := range []struct {
		desired, actual, allocated string
		reserved                   int64
	}{
		{"1600m", "1500m", "1400m", 1600},
		{"1500m", "1600m", "1400m", 1600},
		{"1500m", "1400m", "1600m", 1600},
	} {
		pod := surgeReservedPodFixture(tc.desired)
		deploymentplan.Map(pod["status"])["containerStatuses"] = []any{deploymentplan.Object{"name": "app", "resources": deploymentplan.Object{"requests": deploymentplan.Object{"cpu": tc.actual, "memory": "2Gi"}, "limits": deploymentplan.Object{"memory": "3Gi"}}, "allocatedResources": deploymentplan.Object{"cpu": tc.allocated, "memory": "2Gi"}}}
		before := deploymentplan.Digest(pod)
		request, ok := environmentObservedPodRequest(pod)
		if !ok || request.cpu != tc.reserved || request.memory != 2<<30 || deploymentplan.Digest(pod) != before {
			t.Fatal("normal allocation status was rejected, double-counted, underestimated or mutated", request, ok)
		}
		r := surgeCandidateFixture("500m")
		assertSurgeBlocker(t, verifyEnvironmentSurgeCapacity([]deploymentplan.Resource{r}, []deploymentplan.Object{surgeNodeFixture()}, []deploymentplan.Object{pod}), r.ID)
		if err := verifyEnvironmentSurgeCapacity([]deploymentplan.Resource{surgeCandidateFixture("100m")}, []deploymentplan.Object{surgeNodeFixture()}, []deploymentplan.Object{pod}); err != nil {
			t.Fatal("ordinary 100m worker rejected common allocation fields", err)
		}
	}
}

func TestSingletonSurgeAcceptsNormalEmptyAndAggregatePodResourceStatus(t *testing.T) {
	for _, mode := range []string{"empty", "empty-nested", "actual-lower", "actual-higher", "allocated-higher", "spec-pod-higher"} {
		t.Run(mode, func(t *testing.T) {
			pod := surgeReservedPodFixture("1600m")
			spec, status := deploymentplan.Map(pod["spec"]), deploymentplan.Map(pod["status"])
			wantCPU, wantMemory := int64(1600), int64(2<<30)
			switch mode {
			case "empty":
				status["resources"] = deploymentplan.Object{}
			case "empty-nested":
				status["resources"] = deploymentplan.Object{"requests": deploymentplan.Object{}, "limits": deploymentplan.Object{}}
			case "actual-lower":
				status["resources"] = deploymentplan.Object{"requests": deploymentplan.Object{"cpu": "1590m", "memory": "1Gi"}, "limits": deploymentplan.Object{"memory": "4Gi"}}
			case "actual-higher":
				status["resources"] = deploymentplan.Object{"requests": deploymentplan.Object{"cpu": "1700m", "memory": "3Gi"}}
				wantCPU, wantMemory = 1700, 3<<30
			case "allocated-higher":
				status["resources"] = deploymentplan.Object{}
				status["allocatedResources"] = deploymentplan.Object{"cpu": "1700m", "memory": "3Gi"}
				wantCPU, wantMemory = 1700, 3<<30
			case "spec-pod-higher":
				spec["resources"] = deploymentplan.Object{"requests": deploymentplan.Object{"cpu": "1700m", "memory": "3Gi"}}
				wantCPU, wantMemory = 1700, 3<<30
			}
			before := deploymentplan.Digest(pod)
			request, ok := environmentObservedPodRequest(pod)
			if !ok || request.cpu != wantCPU || request.memory != wantMemory || before != deploymentplan.Digest(pod) {
				t.Fatal("ordinary Pod aggregate was rejected, summed twice, underestimated or mutated", mode, request, ok)
			}
			if err := verifyEnvironmentSurgeCapacity([]deploymentplan.Resource{surgeCandidateFixture("100m")}, []deploymentplan.Object{surgeNodeFixture()}, []deploymentplan.Object{pod}); err != nil {
				t.Fatal("ordinary aggregate status blocked an executable 100m worker", mode, err)
			}
			if mode == "actual-higher" || mode == "allocated-higher" || mode == "spec-pod-higher" {
				r := surgeCandidateFixture("400m")
				assertSurgeBlocker(t, verifyEnvironmentSurgeCapacity([]deploymentplan.Resource{r}, []deploymentplan.Object{surgeNodeFixture()}, []deploymentplan.Object{pod}), r.ID)
			}
		})
	}
	for _, raw := range []any{"private-canary", deploymentplan.Object{"requests": "private-canary"}, deploymentplan.Object{"requests": deploymentplan.Object{"cpu": "private-canary"}}} {
		pod := surgeReservedPodFixture("100m")
		deploymentplan.Map(pod["status"])["resources"] = raw
		if _, ok := environmentObservedPodRequest(pod); ok {
			t.Fatal("malformed Pod aggregate was silently treated as zero")
		}
	}
}

func TestSingletonSurgeNativeReadsOnlySelectedMetadataAndHonorsDeadline(t *testing.T) {
	for _, mode := range []string{"valid", "foreign-plan", "missing-items", "partial-list", "forbidden", "cancelled", "deadline", "Recreate"} {
		t.Run(mode, func(t *testing.T) {
			config, _ := yaml.Marshal(frozenKubernetesFixture(t))
			backend, err := newEnvironmentKubernetesBackend(string(config), "dev")
			if err != nil {
				t.Fatal(err)
			}
			r := surgeCandidateFixture("100m")
			if mode == "Recreate" {
				deploymentplan.Map(r.Desired["spec"])["strategy"] = deploymentplan.Object{"type": "Recreate"}
			}
			plan := &deploymentplan.Plan{Inputs: deploymentplan.Inputs{Environment: "dev", Stack: "video-cloud-dev"}, Resources: []deploymentplan.Resource{r}}
			compiled := &compiledEnvironmentDeployment{Plan: plan, Store: backend.Store, Backend: backend}
			selected := plan
			if mode == "foreign-plan" {
				copy := *plan
				copy.Environment = "prod"
				selected = &copy
			}
			dir := t.TempDir()
			nodes, _ := json.Marshal(map[string]any{"items": []any{surgeNodeFixture()}})
			pods, _ := json.Marshal(map[string]any{"items": []any{surgeReservedPodFixture("1600m")}})
			if mode == "missing-items" {
				pods = []byte(`{}`)
			}
			if mode == "partial-list" {
				pods = []byte(`{"items":[],"metadata":{"continue":"private-canary"}}`)
			}
			writeTestFile(t, filepath.Join(dir, "nodes"), string(nodes))
			writeTestFile(t, filepath.Join(dir, "pods"), string(pods))
			writeTestFile(t, filepath.Join(dir, "kubectl"), `#!/bin/sh
printf '%s\n' "$*" >> "$RTK_SURGE_CALLS"
shift 3
if test "$RTK_SURGE_MODE" = forbidden; then printf 'Forbidden private-canary' >&2; exit 1; fi
if test "$RTK_SURGE_MODE" = deadline; then sleep 3; exit 1; fi
case "$*" in
 'get nodes -o json') cat "$RTK_SURGE_NODES" ;;
 'get pods --all-namespaces -o json') cat "$RTK_SURGE_PODS" ;;
 *) printf 'unexpected operation private-canary' >&2; exit 1 ;;
esac
`)
			if err := os.Chmod(filepath.Join(dir, "kubectl"), 0755); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
			t.Setenv("RTK_SURGE_CALLS", filepath.Join(dir, "calls"))
			t.Setenv("RTK_SURGE_NODES", filepath.Join(dir, "nodes"))
			t.Setenv("RTK_SURGE_PODS", filepath.Join(dir, "pods"))
			t.Setenv("RTK_SURGE_MODE", mode)
			ctx, cancel := context.WithCancel(context.Background())
			if mode == "cancelled" {
				cancel()
			}
			if mode == "deadline" {
				cancel()
				ctx, cancel = context.WithTimeout(context.Background(), 100*time.Millisecond)
			}
			defer cancel()
			started, before := time.Now(), plan.SHA256()
			err = compiled.CheckSurgeCapacity(ctx, selected)
			if (err == nil) != (mode == "valid" || mode == "Recreate") || plan.SHA256() != before {
				t.Fatal("native preflight decision or original plan changed", mode, err)
			}
			if err != nil && strings.Contains(err.Error(), "private-canary") {
				t.Fatal("private native output escaped", err)
			}
			if mode == "deadline" && time.Since(started) > 2*time.Second {
				t.Fatal("surge inventory ignored parent deadline", err)
			}
			calls, readErr := os.ReadFile(filepath.Join(dir, "calls"))
			if readErr != nil && !os.IsNotExist(readErr) {
				t.Fatal(readErr)
			}
			if mode == "foreign-plan" || mode == "cancelled" || mode == "Recreate" {
				if len(calls) != 0 {
					t.Fatal("skipped/cancelled plan launched native operations", string(calls))
				}
			} else if mode == "valid" && strings.Count(string(calls), "--request-timeout=30s") != 2 {
				t.Fatal("surge qualification did not use exactly two fresh bounded GETs", string(calls))
			}
			if mode == "deadline" && strings.Count(string(calls), "--request-timeout=30s") > 1 {
				t.Fatal("parent deadline started a further inventory/retry", string(calls))
			}
			for _, forbidden := range []string{" exec ", " create ", " replace ", " apply ", " delete "} {
				if strings.Contains(string(calls), forbidden) {
					t.Fatal("surge preflight mutated or required a running workload", string(calls))
				}
			}
			assertPlanAccessPrivateFilesRemoved(t, backend)
		})
	}
}
