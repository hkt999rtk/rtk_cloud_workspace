package main

import (
	"context"
	"testing"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

type ciProvisionFixture struct {
	current deploymentplan.Object
	writes  int
	last    deploymentplan.Object
}

func (f *ciProvisionFixture) Read(context.Context, string) (deploymentplan.Object, error) {
	return deploymentplan.Clone(f.current), nil
}
func (f *ciProvisionFixture) Write(_ context.Context, _ string, body deploymentplan.Object, dry bool) (deploymentplan.Object, error) {
	if !dry {
		f.writes++
		f.last = deploymentplan.Clone(body)
		if deploymentplan.String(deploymentplan.Map(body["metadata"])["uid"]) == "" {
			deploymentplan.Map(body["metadata"])["uid"] = "created-uid"
		}
	}
	return deploymentplan.Clone(body), nil
}

func TestCIRolloutProvisionPreservesExistingOwners(t *testing.T) {
	repo := "hkt999rtk/rtk_cloud_frontend"
	desired := deploymentplan.Object{"apiVersion": "v1", "kind": "ServiceAccount", "metadata": map[string]any{"namespace": "video-cloud-dev-video-cloud", "name": "rtk-ci-rtk-cloud-frontend", "annotations": map[string]any{"rtk.realtek.com/ci-rollout-repository": repo}}}
	f := &ciProvisionFixture{}
	if err := ciRolloutEnsureManagedObject(context.Background(), f, repo, desired); err != nil || f.writes != 1 {
		t.Fatalf("create: %v", err)
	}
	foreign := deploymentplan.Clone(desired)
	deploymentplan.Map(deploymentplan.Map(foreign["metadata"])["annotations"])["rtk.realtek.com/ci-rollout-repository"] = "foreign/repository"
	f.current = foreign
	f.writes = 0
	if err := ciRolloutEnsureManagedObject(context.Background(), f, repo, desired); err == nil || f.writes != 0 {
		t.Fatal("foreign existing identity was overwritten")
	}
}

func TestCIRolloutProvisionUpdatesOnlyOwnedResource(t *testing.T) {
	repo := "hkt999rtk/rtk_cloud_frontend"
	current := deploymentplan.Object{"apiVersion": "v1", "kind": "ServiceAccount", "metadata": map[string]any{"namespace": "video-cloud-dev-video-cloud", "name": "rtk-ci-rtk-cloud-frontend", "uid": "original-uid", "resourceVersion": "5", "labels": map[string]any{"existing": "retain"}, "annotations": map[string]any{"rtk.realtek.com/ci-rollout-repository": repo, "unrelated": "retain"}}}
	desired := deploymentplan.Clone(current)
	desiredMeta := deploymentplan.Map(desired["metadata"])
	delete(desiredMeta, "uid")
	delete(desiredMeta, "resourceVersion")
	desiredMeta["labels"] = map[string]any{"selected": "yes"}
	desiredMeta["annotations"] = map[string]any{"rtk.realtek.com/ci-rollout-repository": repo}
	fixture := &ciProvisionFixture{current: current}
	if err := ciRolloutEnsureManagedObject(context.Background(), fixture, repo, desired); err != nil {
		t.Fatal(err)
	}
	meta := deploymentplan.Map(fixture.last["metadata"])
	if fixture.writes != 1 || deploymentplan.String(meta["uid"]) != "original-uid" || deploymentplan.String(meta["resourceVersion"]) != "5" || deploymentplan.Map(meta["labels"])["existing"] != "retain" || deploymentplan.Map(meta["annotations"])["unrelated"] != "retain" {
		t.Fatal("original owner or unrelated metadata was changed")
	}
	fixture.current = deploymentplan.Clone(desired)
	fixture.writes = 0
	if err := ciRolloutEnsureManagedObject(context.Background(), fixture, repo, desired); err != nil || fixture.writes != 0 {
		t.Fatal("matching owned resource should not be replaced", err)
	}
}
