package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

// Retained enrollment producers have genuine native completion and permanent
// outputs. A restarted producer must not contend with its permanent consumer
// merely to satisfy application readiness on an immutable workload retry.
func TestCreateCompletedPreparationReadinessUsesOriginalEnrollmentProof(t *testing.T) {
	f, _, _, _, _ := newFreshProductPreparationFixture(t)
	ctx := withEnvironmentCreateBootstrapRuntime(t.Context(), f.runtime)
	plan, err := readEnvironmentCreateStageInput(f.store, f.e, "workload")
	if err != nil {
		t.Fatal(err)
	}
	live := func() []deploymentplan.Object {
		var out []deploymentplan.Object
		for _, o := range f.b.objects {
			out = append(out, o)
		}
		return out
	}
	readiness, err := newEnvironmentCreateStageReadinessBackend(ctx, f.cfg, f.store, f.e, createWorkloadImageForwardStage, plan, f.runtime.Backend, live())
	owners := readiness.completedPreparations
	if err != nil || len(owners) != 6 {
		t.Fatal("completed producers require their exact six original owners", len(owners), err)
	}
	frozen := plan.SHA256()
	runtimePlan, err := freshPreparationRuntimePlan(plan, owners)
	if err != nil || len(runtimePlan.Resources) != len(plan.Resources)-6 || plan.SHA256() != frozen {
		t.Fatal("runtime acceptance must preserve the frozen plan and remove only completed retained preparations", err)
	}
	for _, r := range runtimePlan.Resources {
		if owners[r.ID] != "" {
			t.Fatal("temporary producer leaked into runtime target")
		}
	}
	// Permanent service readiness remains required even if the caller supplies
	// its UID: it is not an authenticated retained enrollment producer.
	for _, r := range plan.Resources {
		if owners[r.ID] == "" {
			altered := *plan
			altered.Resources = append([]deploymentplan.Resource(nil), plan.Resources...)
			for i := range altered.Resources {
				if altered.Resources[i].ID == r.ID {
					altered.Resources[i].Action = "create"
				}
			}
			forged := map[string]string{r.ID: "ffffffff-ffff-4fff-8fff-ffffffffffff"}
			if _, err := freshPreparationRuntimePlan(&altered, forged); err == nil {
				t.Fatal("non-retained application owner was excluded")
			}
			break
		}
	}
	var originalID string
	for id := range owners {
		originalID = id
		break
	}
	original := f.b.objects[originalID]
	changed := deploymentplan.Clone(original)
	deploymentplan.Map(changed["metadata"])["uid"] = "ffffffff-ffff-4fff-8fff-ffffffffffff"
	f.b.objects[originalID] = changed
	if _, err := completedFreshPreparationOwners(ctx, f.cfg, f.runtime, live()); err == nil {
		t.Fatal("foreign preparation UID accepted")
	}
	f.b.objects[originalID] = original
	// Missing permanent output cannot become completion by name or by an
	// accepted enrollment Deployment alone.
	program, err := readFreshProductProgram(f.store, f.e)
	if err != nil {
		t.Fatal(err)
	}
	outputID := program.Lifecycle.Outputs[0].ID
	saved := f.b.objects[outputID]
	delete(f.b.objects, outputID)
	if _, err := completedFreshPreparationOwners(ctx, f.cfg, f.runtime, live()); err == nil {
		t.Fatal("missing permanent identity output accepted")
	}
	f.b.objects[outputID] = saved
}

func TestCreateBackendCompletedPreparationWaitChecksOwnerAndBody(t *testing.T) {
	for _, mode := range []string{"completed", "foreign-owner", "changed-body", "create", "ordinary"} {
		t.Run(mode, func(t *testing.T) {
			directory := t.TempDir()
			store, err := newSecretStore(filepath.Join(directory, "private"), "dev")
			if err != nil {
				t.Fatal(err)
			}
			desired := deploymentplan.Object{"apiVersion": "apps/v1", "kind": "Deployment", "metadata": deploymentplan.Object{"name": "original-preparation", "namespace": "video-cloud-dev-video-cloud"}, "spec": deploymentplan.Object{"replicas": float64(1)}}
			uid := "11111111-1111-4111-8111-111111111111"
			actual := deploymentplan.Clone(desired)
			deploymentplan.Map(actual["metadata"])["uid"] = uid
			actual["status"] = deploymentplan.Object{"readyReplicas": float64(0)}
			if mode == "foreign-owner" {
				deploymentplan.Map(actual["metadata"])["uid"] = "ffffffff-ffff-4fff-8fff-ffffffffffff"
			}
			if mode == "changed-body" {
				deploymentplan.Map(actual["spec"])["replicas"] = float64(2)
			}
			body, _ := json.Marshal(actual)
			writeTestFile(t, filepath.Join(directory, "object.json"), string(body))
			calls := filepath.Join(directory, "calls")
			kubectl := filepath.Join(directory, "kubectl")
			writeTestFile(t, kubectl, "#!/bin/sh\nset -eu\nprintf '%s\\n' \"$*\" >>\"$PREPARATION_CALLS\"\ncase \"$*\" in *' get '*) cat \"$PREPARATION_OBJECT\" ;; *) exit 1 ;; esac\n")
			if err := os.Chmod(kubectl, 0755); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", directory+string(os.PathListSeparator)+os.Getenv("PATH"))
			t.Setenv("PREPARATION_CALLS", calls)
			t.Setenv("PREPARATION_OBJECT", filepath.Join(directory, "object.json"))
			r := deploymentplan.Resource{ID: deploymentplan.ID(desired), UID: uid, Action: "retain", Desired: desired}
			if mode == "create" {
				r.Action = "create"
			}
			owners := map[string]string{r.ID: uid}
			if mode == "ordinary" {
				owners = nil
			}
			b := environmentCreateKubernetesBackend{environmentKubernetesBackend: environmentKubernetesBackend{Store: store, KubeconfigContent: []byte("isolated-test-config"), RolloutTimeout: time.Second}, completedPreparations: owners}
			err = b.Wait(context.Background(), r)
			if (err == nil) != (mode == "completed") {
				t.Fatal("completed retained producer readiness classification", mode, err)
			}
			if mode == "completed" && strings.Contains(readTestFile(t, calls), "rollout status") {
				t.Fatal("completed producer still waits for a new Pod")
			}
		})
	}
}
