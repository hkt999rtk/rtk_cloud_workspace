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

func TestEnvironmentReleaseRetainsAcceptedDevControllerWithoutReclassifyingAsSchemaOwner(t *testing.T) {
	configRoot := t.TempDir()
	if err := os.Chmod(configRoot, 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("RTK_CLOUD_CONFIG_ROOT", configRoot)
	store, err := newSecretStore("", "dev")
	if err != nil {
		t.Fatal(err)
	}
	const key = "LKE_PKI_CONTROLLER_IMAGE"
	const id = "Deployment/video-cloud-dev-video-cloud/pki-controller"
	const uid = "90b2a36e-e6a5-4d1f-9b10-a45571191df3"
	ref := "ghcr.io/hkt999rtk/rtk_cloud_dev/video-cloud-api@sha256:" + strings.Repeat("1", 64)
	release := environmentRelease{
		Schema: "rtk-cloud.release/v1", Environment: "dev", Stack: "video-cloud-dev", WorkspaceRevision: strings.Repeat("a", 40),
		Images: map[string]environmentReleaseImage{key: {Reference: ref}},
	}
	workload := func(name, image string) deploymentplan.Object {
		return deploymentplan.Object{"apiVersion": "apps/v1", "kind": "Deployment",
			"metadata": map[string]any{"namespace": "video-cloud-dev-video-cloud", "name": name, "uid": uid},
			"spec": map[string]any{"template": map[string]any{"spec": map[string]any{
				"containers": []any{map[string]any{"name": "pki-controller", "image": image}},
			}}},
		}
	}
	live := workload("pki-controller", ref)
	prior := &deploymentplan.Plan{Version: deploymentplan.Version, Inputs: deploymentplan.Inputs{Environment: "dev", Stack: "video-cloud-dev", Operation: "scoped"},
		Resources: []deploymentplan.Resource{{ID: id, UID: uid, Action: "update", Desired: live, Baseline: workload("pki-controller", "registry.example/pki@sha256:"+strings.Repeat("2", 64))}}}
	sha := prior.SHA256()
	write := func(name string, object any) {
		t.Helper()
		raw, err := json.Marshal(object)
		if err != nil {
			t.Fatal(err)
		}
		if err := store.write("deployment/rollback/"+sha+"/"+name, raw, true); err != nil {
			t.Fatal(err)
		}
	}
	journal := deploymentplan.Journal{PlanSHA256: sha, Completed: []string{id}, Owners: map[string]string{id: uid}, AcceptanceStarted: true}
	write("plan.json", prior)
	write("journal.json", journal)
	if _, err := bindEnvironmentRetainedDevImages(store, release, []deploymentplan.Object{live}); err == nil {
		t.Fatal("unaccepted plan was permitted")
	}
	journal.Accepted = true
	write("journal.json", journal)
	dir := filepath.Join(store.Root, "deployment", "rollback", sha)
	if err := validateEnvironmentPreflightDirectories(store, dir); err != nil {
		t.Fatal(err)
	}
	var disk deploymentplan.Plan
	if err := readEnvironmentPreflightJSON(filepath.Join(dir, "plan.json"), &disk); err != nil {
		t.Fatal(err)
	}
	if _, found, err := disk.ReadExecution(store.Root); err != nil || !found {
		t.Fatalf("test fixture was not a canonical execution: %v", err)
	}
	selected, err := bindEnvironmentRetainedDevImages(store, release, []deploymentplan.Object{live})
	if err != nil || selected.Images[key].RetainedPlanSHA256 != sha || release.Images[key].RetainedPlanSHA256 != "" {
		t.Fatalf("did not freeze exact accepted scoped source: %v", err)
	}
	plan := &deploymentplan.Plan{Inputs: deploymentplan.Inputs{Environment: "dev", Stack: "video-cloud-dev", Operation: "upgrade"},
		Resources: []deploymentplan.Resource{{ID: id, UID: uid, Action: "retain", Desired: live, Baseline: live}}}
	observed := map[string]deploymentplan.Object{id: live}
	if err := verifyEnvironmentRetainedDevImages(store, selected, plan, observed); err != nil {
		t.Fatal(err)
	}
	ctx := t.Context()
	cfg := deploymentConfig{Environment: release.Environment, Workspace: "/synthetic/workspace"}
	if err := verifyEnvironmentReleaseWithCommand(ctx, cfg, selected, func(_ context.Context, command string, args ...string) ([]byte, error) {
		if command == "git" {
			return []byte(release.WorkspaceRevision), nil
		}
		if command != "docker" || len(args) != 4 || args[0] != "pull" || args[3] != ref {
			t.Fatalf("unexpected release lookup: %s %v", command, args)
		}
		return nil, nil
	}); err != nil {
		t.Fatalf("retained controller should remain pull-verified: %v", err)
	}
	if err := verifyEnvironmentReleaseSchemasForPlan(ctx, cfg, store, selected, plan, nil); err != nil {
		t.Fatalf("retained controller should not be treated as an unknown schema candidate: %v", err)
	}
	proof := environmentPreflightProof{Environment: "dev", Stack: "video-cloud-dev", Operation: "upgrade", QualifiedAt: time.Now()}
	if err := validateEnvironmentPreflightReleaseProofs(proof, selected); err != nil {
		t.Fatal(err)
	}
	if err := validateEnvironmentPreflightReleaseProofs(proof, release); err == nil {
		t.Fatal("unproven native image skipped formal publication")
	}
	foreign := workload("foreign", ref)
	plan.Resources = append(plan.Resources, deploymentplan.Resource{ID: deploymentplan.ID(foreign), UID: uid, Action: "retain", Desired: foreign, Baseline: foreign})
	if err := verifyEnvironmentRetainedDevImages(store, selected, plan, observed); err == nil {
		t.Fatal("retained image authorized an unrelated owner")
	}
	plan.Resources = plan.Resources[:1]
	journal.Accepted = false
	write("journal.json", journal)
	if err := verifyEnvironmentRetainedDevImages(store, selected, plan, observed); err == nil {
		t.Fatal("fence did not detect removed acceptance")
	}
}
