package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func deploymentHealthyInventory(t *testing.T) []deploymentHealthResource {
	t.Helper()
	raw := `[{"kind":"Deployment","metadata":{"namespace":"video-cloud-staging-billing","name":"payment-simulator","generation":2},"spec":{"replicas":1},"status":{"observedGeneration":2,"readyReplicas":1,"updatedReplicas":1,"availableReplicas":1}},{"kind":"Pod","metadata":{"namespace":"video-cloud-staging-billing","name":"payment-simulator-1"},"status":{"phase":"Running","conditions":[{"type":"Ready","status":"True"}],"containerStatuses":[{"name":"app","ready":true,"state":{"running":{}}}]}}]`
	var items []deploymentHealthResource
	if err := json.Unmarshal([]byte(raw), &items); err != nil {
		t.Fatal(err)
	}
	return items
}

func TestDeploymentWorkloadHealthCatchesRuntimeAndRolloutFailures(t *testing.T) {
	for _, tc := range []struct {
		name    string
		change  func([]deploymentHealthResource)
		failure string
	}{
		{"healthy Billing", func([]deploymentHealthResource) {}, ""},
		{"old generation", func(r []deploymentHealthResource) { r[0].Status.ObservedGeneration = 1 }, "deployment rollout"},
		{"old replicas", func(r []deploymentHealthResource) { r[0].Status.UpdatedReplicas = 0 }, "deployment rollout"},
		{"unavailable", func(r []deploymentHealthResource) { r[0].Status.UnavailableReplicas = 1 }, "deployment rollout"},
		{"missing Ready", func(r []deploymentHealthResource) { r[0].Status.ReadyReplicas = 0 }, "deployment rollout"},
		{"pod pending", func(r []deploymentHealthResource) { r[1].Status.Phase = "Pending" }, "not Running and Ready"},
		{"pod unready", func(r []deploymentHealthResource) { r[1].Status.Conditions = nil }, "not Running and Ready"},
		{"CrashLoop", func(r []deploymentHealthResource) {
			var c deploymentHealthContainer
			_ = json.Unmarshal([]byte(`{"name":"app","state":{"waiting":{"reason":"CrashLoopBackOff","message":"secret-sentinel"}}}`), &c)
			r[1].Status.ContainerStatuses = []deploymentHealthContainer{c}
		}, "container app"},
		{"terminated app", func(r []deploymentHealthResource) {
			var c deploymentHealthContainer
			_ = json.Unmarshal([]byte(`{"name":"app","state":{"terminated":{"exitCode":1}}}`), &c)
			r[1].Status.ContainerStatuses = []deploymentHealthContainer{c}
		}, "container app"},
		{"init failure", func(r []deploymentHealthResource) {
			var c deploymentHealthContainer
			_ = json.Unmarshal([]byte(`{"state":{"terminated":{"exitCode":1}}}`), &c)
			r[1].Status.InitContainerStatuses = []deploymentHealthContainer{c}
		}, "init container"},
		{"init waiting", func(r []deploymentHealthResource) {
			var c deploymentHealthContainer
			_ = json.Unmarshal([]byte(`{"state":{"waiting":{"reason":"ImagePullBackOff"}}}`), &c)
			r[1].Status.InitContainerStatuses = []deploymentHealthContainer{c}
		}, "init container"},
		{"completed init", func(r []deploymentHealthResource) {
			var c deploymentHealthContainer
			_ = json.Unmarshal([]byte(`{"state":{"terminated":{"exitCode":0}}}`), &c)
			r[1].Status.InitContainerStatuses = []deploymentHealthContainer{c}
		}, ""},
		{"empty container status", func(r []deploymentHealthResource) { r[1].Status.ContainerStatuses = nil }, "not Running and Ready"},
		{"unnamed resource", func(r []deploymentHealthResource) { r[0].Metadata.Name = "" }, "no resource name"},
		{"unknown resource", func(r []deploymentHealthResource) { r[0].Kind = "Secret" }, "unexpected resource kind"},
		{"foreign similarly named namespace", func(r []deploymentHealthResource) { r[0].Metadata.Namespace = "video-cloud-staging2-billing" }, "no complete controller"},
		{"deleting pod", func(r []deploymentHealthResource) { r[1].Metadata.DeletionTimestamp = "2026-10-05T00:00:00Z" }, "no complete controller"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			items := deploymentHealthyInventory(t)
			tc.change(items)
			raw, _ := json.Marshal(map[string]any{"items": items})
			err := validateDeploymentWorkloadHealth("staging", raw)
			if tc.failure == "" {
				if err != nil {
					t.Fatal(err)
				}
			} else if err == nil || !strings.Contains(err.Error(), tc.failure) {
				t.Fatalf("want %s got %v", tc.failure, err)
			}
			if err != nil && strings.Contains(err.Error(), "secret-sentinel") {
				t.Fatal("leaked raw diagnostic")
			}
		})
	}
}

func TestDeploymentWorkloadHealthStatefulDaemonAndJobBoundaries(t *testing.T) {
	for _, tc := range []struct {
		kind    string
		healthy bool
	}{{"StatefulSet", true}, {"StatefulSet", false}, {"DaemonSet", true}, {"DaemonSet", false}} {
		t.Run(tc.kind+strings.Repeat("healthy", map[bool]int{true: 1, false: 0}[tc.healthy]), func(t *testing.T) {
			items := deploymentHealthyInventory(t)
			items[0].Kind = tc.kind
			items[0].Status.CurrentRevision = "a"
			items[0].Status.UpdateRevision = "a"
			items[0].Status.DesiredNumberScheduled = 1
			items[0].Status.NumberReady = 1
			items[0].Status.UpdatedNumberScheduled = 1
			if !tc.healthy {
				items[0].Status.UpdateRevision = "b"
				items[0].Status.NumberUnavailable = 1
			}
			// Failed old test Jobs do not represent current application availability.
			var job deploymentHealthResource
			_ = json.Unmarshal([]byte(`{"kind":"Pod","metadata":{"name":"old-acceptance-job","namespace":"video-cloud-staging-billing","ownerReferences":[{"kind":"Job"}]},"status":{"phase":"Failed"}}`), &job)
			items = append(items, job)
			var foreign deploymentHealthResource
			_ = json.Unmarshal([]byte(`{"kind":"Pod","metadata":{"name":"foreign","namespace":"video-cloud-dev-billing"},"status":{"phase":"Failed"}}`), &foreign)
			items = append(items, foreign)
			raw, _ := json.Marshal(map[string]any{"items": items})
			err := validateDeploymentWorkloadHealth("staging", raw)
			if (err == nil) != tc.healthy {
				t.Fatalf("healthy=%t err=%v", tc.healthy, err)
			}
		})
	}
	for _, kind := range []string{"Deployment", "StatefulSet"} {
		items := deploymentHealthyInventory(t)
		items[0].Kind = kind
		zero := int32(0)
		items[0].Spec.Replicas = &zero
		raw, _ := json.Marshal(map[string]any{"items": items})
		if err := validateDeploymentWorkloadHealth("staging", raw); err != nil {
			t.Fatal(err)
		}
	}
	for _, raw := range []string{"broken", `{}`, `{"items":null}`, `{"items":[]}`} {
		if err := validateDeploymentWorkloadHealth("staging", []byte(raw)); err == nil {
			t.Fatal("empty/malformed inventory passed")
		}
	}
}

func TestDeploymentWorkloadHealthBoundedReadsAndFailureRedaction(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(map[bool]string{false: "healthy", true: "forbidden"}[fail], func(t *testing.T) {
			store := makeIsolatedTestSecretStore(t, "staging")
			store.checkRuntime = newDeploymentCheckRuntime(context.Background(), "staging")
			if fail {
				deploymentRuntimeTestKubectl(t, "printf 'Forbidden secret-sentinel' >&2; exit 1\n")
			} else {
				items := deploymentHealthyInventory(t)
				raw, _ := json.Marshal(map[string]any{"items": items})
				deploymentRuntimeTestKubectl(t, "printf '%s' '"+string(raw)+"'\n")
			}
			err := verifyDeploymentWorkloadHealth(store)
			if (err != nil) != fail {
				t.Fatal(err)
			}
			if err != nil && strings.Contains(err.Error(), "secret-sentinel") {
				t.Fatal("raw output leaked")
			}
		})
	}
}

func TestDeploymentWorkloadHealthOnDeleteRequiresReadyOwnedPodsAtDeclaredImages(t *testing.T) {
	for _, name := range []string{"ready", "old-image", "missing-owner", "missing-template", "old-init"} {
		t.Run(name, func(t *testing.T) {
			items := deploymentHealthyInventory(t)
			items[0].Kind = "StatefulSet"
			items[0].Spec.UpdateStrategy.Type = "OnDelete"
			items[0].Status.UpdatedReplicas = 0
			items[0].Status.CurrentRevision = "old"
			items[0].Status.UpdateRevision = "new"
			items[0].Spec.Template.Spec.Containers = []deploymentHealthImage{{Name: "app", Image: "reviewed:1"}}
			items[0].Spec.Template.Spec.InitContainers = []deploymentHealthImage{{Name: "init", Image: "reviewed:1"}}
			items[1].Spec.Containers = []deploymentHealthImage{{Name: "app", Image: "reviewed:1"}}
			items[1].Spec.InitContainers = []deploymentHealthImage{{Name: "init", Image: "reviewed:1"}}
			owner := `[{"kind":"StatefulSet","name":"payment-simulator"}]`
			_ = json.Unmarshal([]byte(owner), &items[1].Metadata.OwnerReferences)
			switch name {
			case "old-image":
				items[1].Spec.Containers[0].Image = "old:1"
			case "old-init":
				items[1].Spec.InitContainers[0].Image = "old:1"
			case "missing-owner":
				items[1].Metadata.OwnerReferences = nil
			case "missing-template":
				items[0].Spec.Template.Spec.Containers = nil
			}
			raw, _ := json.Marshal(map[string]any{"items": items})
			err := validateDeploymentWorkloadHealth("staging", raw)
			if (err == nil) != (name == "ready") {
				t.Fatal(err)
			}
		})
	}
}
