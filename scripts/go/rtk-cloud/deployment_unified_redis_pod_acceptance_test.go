package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func redisTargetAcceptanceFixture(t *testing.T) (*deploymentplan.Plan, deploymentplan.Object, map[string]string, string) {
	t.Helper()
	definitions, err := decodeEnvironmentCreateManifests(lkeRedisDeploymentManifest(map[string]string{"CLOUD_ENV_NAME": "dev", "CLOUD_STACK_NAME": "video-cloud-dev"}))
	if err != nil || len(definitions) != 1 {
		t.Fatal("maintained Redis source unavailable", err)
	}
	image := "docker.io/valkey/valkey@sha256:081c2f5cb575efc901aa80ff9cdbd1ec6a301682fd35e1ebb4b0990a4a4a8507"
	d := &createManagedLateRedisDurable{ParentSHA: strings.Repeat("b", 64), ResourceID: deploymentplan.ID(definitions[0].Object), UID: "11111111-1111-4111-8111-111111111111", Image: image, Original: deploymentplan.Resource{Desired: definitions[0].Object}}
	_, helper, _, err := managedLateRedisRecipes(d, deploymentConfig{Values: map[string]string{"REDIS_DATA_STORAGE": "5Gi", "DURABLE_DATA_STORAGE_CLASS": "selected-retain"}})
	if err != nil {
		t.Fatal(err)
	}
	defs := []deploymentplan.Definition{{Object: helper, Phase: "workload"}}
	in := deploymentplan.Inputs{Environment: "dev", Stack: "video-cloud-dev", Operation: "scoped", ConfigurationSHA256: deploymentplan.Digest("cfg"), ReleaseSHA256: deploymentplan.Digest("release"), BindingsSHA256: environmentCreateStageBindingsSHA("normal-redis-durable-target", defs)}
	plan, err := deploymentplan.Compile(in, defs, nil)
	if err != nil {
		t.Fatal(err)
	}
	pod := deploymentplan.Clone(plan.Resources[0].Desired)
	uid := "22222222-2222-4222-8222-222222222222"
	deploymentplan.Map(pod["metadata"])["uid"] = uid
	deploymentplan.Map(pod["metadata"])["resourceVersion"] = "271"
	pod["status"] = deploymentplan.Object{"phase": "Running", "conditions": []any{deploymentplan.Object{"type": "PodScheduled", "status": "True"}, deploymentplan.Object{"type": "Initialized", "status": "True"}, deploymentplan.Object{"type": "Ready", "status": "True"}, deploymentplan.Object{"type": "ContainersReady", "status": "True"}}, "containerStatuses": []any{deploymentplan.Object{"name": "redis-migration", "ready": true, "started": true, "containerID": "containerd://original-helper", "imageID": image, "state": deploymentplan.Object{"running": deploymentplan.Object{"startedAt": "2026-10-17T17:24:00Z"}}}}}
	return plan, pod, map[string]string{plan.Resources[0].ID: uid}, strings.Split(image, "@")[1]
}

func TestManagedLateRedisStandaloneTargetAcceptanceBindsRecipeOwnerAndNativeRuntime(t *testing.T) {
	for _, mode := range []string{"valid", "missing-owner-proof", "duplicate-owner-proof", "wrong-owner", "missing-uid", "replacement-uid", "deleting", "controller-owned", "pending", "terminal", "not-ready", "missing-ready", "duplicate-ready", "container-not-ready", "container-not-started", "missing-status", "wrong-image", "wrong-runtime-image", "missing-oci", "private-oci-error", "changed-command", "changed-claim", "readonly-data", "data-subpath", "ephemeral", "injected-init", "foreign-namespace", "extra-pod", "duplicate-pod", "wrong-bindings", "wrong-phase", "retained-plan", "ordinary-plan", "arbitrary-pod", "self-consistent-foreign-recipe", "cancelled"} {
		t.Run(mode, func(t *testing.T) {
			plan, pod, owners, digest := redisTargetAcceptanceFixture(t)
			objects := []deploymentplan.Object{pod}
			proof := []map[string]string{owners}
			meta, spec, status := deploymentplan.Map(pod["metadata"]), deploymentplan.Map(pod["spec"]), deploymentplan.Map(pod["status"])
			container := deploymentplan.Map(certIssuerObjectList(spec["containers"])[0])
			state := deploymentplan.Map(certIssuerObjectList(status["containerStatuses"])[0])
			ctx := context.Background()
			switch mode {
			case "missing-owner-proof":
				proof = nil
			case "duplicate-owner-proof":
				proof = append(proof, owners)
			case "wrong-owner":
				owners[plan.Resources[0].ID] = "33333333-3333-4333-8333-333333333333"
			case "missing-uid":
				delete(meta, "uid")
			case "replacement-uid":
				meta["uid"] = "33333333-3333-4333-8333-333333333333"
			case "deleting":
				meta["deletionTimestamp"] = "2026-10-17T17:25:00Z"
			case "controller-owned":
				meta["ownerReferences"] = []any{deploymentplan.Object{"kind": "Job", "name": "foreign", "uid": "33333333-3333-4333-8333-333333333333", "controller": true}}
			case "pending":
				status["phase"] = "Pending"
			case "terminal":
				status["phase"] = "Succeeded"
			case "not-ready":
				deploymentplan.Map(certIssuerObjectList(status["conditions"])[2])["status"] = "False"
			case "missing-ready":
				status["conditions"] = []any{}
			case "duplicate-ready":
				status["conditions"] = append(certIssuerObjectList(status["conditions"]), deploymentplan.Object{"type": "Ready", "status": "True"})
			case "container-not-ready":
				state["ready"] = false
			case "container-not-started":
				state["started"] = false
			case "missing-status":
				status["containerStatuses"] = []any{}
			case "wrong-image":
				container["image"] = "valkey/valkey:8-alpine"
			case "wrong-runtime-image":
				state["imageID"] = "containerd://sha256:" + strings.Repeat("c", 64)
			case "changed-command":
				container["command"] = []any{"/bin/sh", "-ec", "different"}
			case "changed-claim":
				deploymentplan.Map(deploymentplan.Map(certIssuerObjectList(spec["volumes"])[0])["persistentVolumeClaim"])["claimName"] = "foreign-data"
			case "readonly-data":
				deploymentplan.Map(certIssuerObjectList(container["volumeMounts"])[0])["readOnly"] = true
			case "data-subpath":
				deploymentplan.Map(certIssuerObjectList(container["volumeMounts"])[0])["subPath"] = "foreign"
			case "ephemeral":
				spec["ephemeralContainers"] = []any{deploymentplan.Object{"name": "debugger"}}
			case "injected-init":
				spec["initContainers"] = []any{deploymentplan.Object{"name": "foreign"}}
			case "foreign-namespace":
				meta["namespace"] = "video-cloud-other-platform"
			case "extra-pod", "duplicate-pod":
				extra := deploymentplan.Clone(pod)
				if mode == "extra-pod" {
					deploymentplan.Map(extra["metadata"])["name"] = "foreign"
				}
				objects = append(objects, extra)
			case "wrong-bindings":
				plan.BindingsSHA256 = deploymentplan.Digest("foreign-stage")
			case "wrong-phase":
				plan.Resources[0].Phase = "bootstrap"
			case "retained-plan":
				plan.Resources[0].Action, plan.Resources[0].UID = "retain", owners[plan.Resources[0].ID]
			case "ordinary-plan":
				plan.Operation = "upgrade"
			case "arbitrary-pod":
				deploymentplan.Map(plan.Resources[0].Desired["metadata"])["name"] = "unrelated-helper"
			case "self-consistent-foreign-recipe":
				desired := managedLateRedisTargetPod(plan.Stack+"-platform", "redis-durable-"+strings.Repeat("b", 12), container["image"].(string))
				deploymentplan.Map(certIssuerObjectList(deploymentplan.Map(desired["spec"])["containers"])[0])["command"] = []any{"/bin/sh", "-ec", "different"}
				defs := []deploymentplan.Definition{{Object: desired, Phase: "workload"}}
				in := plan.Inputs
				in.BindingsSHA256 = environmentCreateStageBindingsSHA("normal-redis-durable-target", defs)
				var err error
				plan, err = deploymentplan.Compile(in, defs, nil)
				if err != nil {
					t.Fatal(err)
				}
			case "cancelled":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			}
			beforePlan, beforeObjects, beforeOwners := plan.SHA256(), deploymentplan.Digest(objects), deploymentplan.Digest(proof)
			calls := 0
			err := verifyEnvironmentTargetWorkloads(ctx, plan, objects, func(context.Context, string) (map[string]bool, error) {
				calls++
				if mode == "missing-oci" {
					return nil, nil
				}
				if mode == "private-oci-error" {
					return nil, errors.New("private-credential-canary")
				}
				return map[string]bool{digest: true}, nil
			}, proof...)
			if (err == nil) != (mode == "valid") {
				t.Fatal("incorrect standalone helper acceptance", err)
			}
			if mode == "valid" {
				if calls != 1 {
					t.Fatal("immutable runtime evidence was not verified")
				}
				raw, _ := json.Marshal(deploymentplan.Object{"items": objects})
				if validateDeploymentWorkloadHealthForStack(plan.Stack, raw) == nil {
					t.Fatal("standalone helper incorrectly became complete environment health")
				}
			}
			if err != nil && strings.Contains(err.Error(), "private-credential-canary") {
				t.Fatal("private OCI error escaped")
			}
			if beforePlan != plan.SHA256() || beforeObjects != deploymentplan.Digest(objects) || beforeOwners != deploymentplan.Digest(proof) {
				t.Fatal("acceptance changed immutable evidence")
			}
		})
	}
}

func TestManagedLateRedisTargetAcceptanceRequiresOriginalChildWriteReceipt(t *testing.T) {
	for _, mode := range []string{"valid", "missing", "foreign-plan", "inflight", "failed", "not-started", "missing-completed", "foreign-completed", "missing-owner", "foreign-owner", "unsafe-mode"} {
		t.Run(mode, func(t *testing.T) {
			store := makeIsolatedTestSecretStore(t, "dev")
			plan, _, owners, _ := redisTargetAcceptanceFixture(t)
			j := deploymentplan.Journal{PlanSHA256: plan.SHA256(), Completed: []string{plan.Resources[0].ID}, Owners: owners, AcceptanceStarted: true}
			switch mode {
			case "foreign-plan":
				j.PlanSHA256 = strings.Repeat("c", 64)
			case "inflight":
				j.InFlight = plan.Resources[0].ID
			case "failed":
				j.Failed = "deployment.post-deploy.workloads"
			case "not-started":
				j.AcceptanceStarted = false
			case "missing-completed":
				j.Completed = nil
			case "foreign-completed":
				j.Completed = []string{"Pod/video-cloud-dev-platform/foreign"}
			case "missing-owner":
				j.Owners = nil
			case "foreign-owner":
				j.Owners = map[string]string{"Pod/video-cloud-dev-platform/foreign": "22222222-2222-4222-8222-222222222222"}
			}
			path := filepath.Join(store.Root, "deployment", "rollback", plan.SHA256(), "journal.json")
			if mode != "missing" {
				if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
					t.Fatal(err)
				}
				original, err := json.Marshal(plan)
				if err != nil || os.WriteFile(filepath.Join(filepath.Dir(path), "plan.json"), original, 0600) != nil {
					t.Fatal("original child plan fixture", err)
				}
				raw, _ := json.Marshal(j)
				if err := os.WriteFile(path, raw, 0600); err != nil {
					t.Fatal(err)
				}
				if mode == "unsafe-mode" {
					if err := os.Chmod(path, 0644); err != nil {
						t.Fatal(err)
					}
				}
			}
			got, err := environmentRedisTargetPodOwners(store, plan)
			if (err == nil) != (mode == "valid") || err == nil && deploymentplan.Digest(got) != deploymentplan.Digest(owners) {
				t.Fatal("unbound child write receipt accepted", err)
			}
		})
	}
}
