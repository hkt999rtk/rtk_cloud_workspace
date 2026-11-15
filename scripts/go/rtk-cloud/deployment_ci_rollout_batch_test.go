package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func TestCIRolloutBatchUsesEnvironmentOrderAndOnePublication(t *testing.T) {
	repo := "hkt999rtk/rtk_video_cloud"
	a := "ghcr.io/" + repo + "/video-cloud-api"
	b := "ghcr.io/" + repo + "/video-cloud-emqx-pki"
	rev := strings.Repeat("a", 40)
	pub := &createVersionPublication{Repository: repo, Branch: "main", RunID: 7, Attempt: 1, ManifestSHA: strings.Repeat("b", 64)}
	images := map[string]environmentReleaseImage{a: {Reference: a + "@sha256:" + strings.Repeat("c", 64), SourceRevision: rev, Publication: pub}, b: {Reference: b + "@sha256:" + strings.Repeat("d", 64), SourceRevision: rev, Publication: pub}}
	workload := func(name, uid string, cs ...any) deploymentplan.Resource {
		return deploymentplan.Resource{UID: uid, Desired: deploymentplan.Object{"apiVersion": "apps/v1", "kind": "Deployment", "metadata": map[string]any{"namespace": "video-cloud-dev-video-cloud", "name": name}, "spec": map[string]any{"template": map[string]any{"spec": map[string]any{"containers": cs}}}}}
	}
	plan := &deploymentplan.Plan{Inputs: deploymentplan.Inputs{Environment: "dev", Stack: "video-cloud-dev"}, Resources: []deploymentplan.Resource{workload("controller", "uid-1", map[string]any{"name": "controller", "image": images[a].Reference}), workload("mqtt-pki", "uid-2", map[string]any{"name": "broker", "image": images[a].Reference}, map[string]any{"name": "mqtt", "image": images[b].Reference})}}
	steps, err := ciRolloutBatchSteps(plan, ciBranchSelection{repo: "main"}, repo, "main", images)
	if err != nil || len(steps) != 2 || steps[0].Target.Name != "controller" || steps[1].Target.Name != "mqtt-pki" || len(steps[1].Images) != 2 {
		t.Fatalf("invalid ordered steps %#v: %v", steps, err)
	}
	changed := images[b]
	changed.SourceRevision = strings.Repeat("e", 40)
	images[b] = changed
	if _, err = ciRolloutBatchSteps(plan, ciBranchSelection{repo: "main"}, repo, "main", images); err == nil {
		t.Fatal("different source revisions combined into one rollout")
	}
}

func TestCIRolloutBatchRollsBackEveryChangedWorkloadBeforeReleasingLease(t *testing.T) {
	repo := "hkt999rtk/rtk_video_cloud"
	a := "ghcr.io/" + repo + "/video-cloud-api"
	b := "ghcr.io/" + repo + "/video-cloud-emqx-pki"
	rev := strings.Repeat("a", 40)
	pub := &createVersionPublication{Repository: repo, Branch: "main", RunID: 7, Attempt: 1, ManifestSHA: strings.Repeat("b", 64)}
	oldA := a + "@sha256:" + strings.Repeat("1", 64)
	oldB := b + "@sha256:" + strings.Repeat("2", 64)
	nextA := a + "@sha256:" + strings.Repeat("3", 64)
	nextB := b + "@sha256:" + strings.Repeat("4", 64)
	images := map[string]environmentReleaseImage{a: {Reference: nextA, SourceRevision: rev, Publication: pub}, b: {Reference: nextB, SourceRevision: rev, Publication: pub}}
	workload := func(name, uid string, containers ...any) deploymentplan.Object {
		return deploymentplan.Object{"apiVersion": "apps/v1", "kind": "Deployment", "metadata": map[string]any{"namespace": "video-cloud-dev-video-cloud", "name": name, "uid": uid, "resourceVersion": "1"}, "spec": map[string]any{"template": map[string]any{"spec": map[string]any{"containers": containers}}}}
	}
	first := workload("controller", "uid-1", map[string]any{"name": "controller", "image": oldA})
	second := workload("mqtt-pki", "uid-2", map[string]any{"name": "broker", "image": oldA}, map[string]any{"name": "mqtt", "image": oldB})
	plan := &deploymentplan.Plan{Inputs: deploymentplan.Inputs{Environment: "dev", Stack: "video-cloud-dev"}, Resources: []deploymentplan.Resource{{UID: "uid-1", Desired: first}, {UID: "uid-2", Desired: second}}}
	objects := map[string]deploymentplan.Object{"controller": first, "mqtt-pki": second}
	var events []string
	hooks := ciRolloutBatchHooks{
		RecoveryTimeout: time.Second,
		Fence:           func(context.Context) error { return nil },
		Acquire: func(context.Context) (func() error, error) {
			events = append(events, "lease")
			return func() error { events = append(events, "release"); return nil }, nil
		},
		Head: func(context.Context, string, string) (string, error) { return rev, nil },
		Read: func(_ context.Context, tgt ciRolloutTarget) ([]byte, error) { return json.Marshal(objects[tgt.Name]) },
		Patch: func(_ context.Context, tgt ciRolloutTarget, raw []byte, dry bool) error {
			if dry {
				return nil
			}
			var ops []struct{ Op, Path, Value string }
			if json.Unmarshal(raw, &ops) != nil {
				return errors.New("bad patch")
			}
			obj := objects[tgt.Name]
			pod := deploymentplan.Map(deploymentplan.Map(deploymentplan.Map(obj["spec"])["template"])["spec"])
			containers := pod["containers"].([]any)
			for _, op := range ops {
				if op.Op != "replace" {
					continue
				}
				var index int
				if _, err := fmt.Sscanf(op.Path, "/spec/template/spec/containers/%d/image", &index); err != nil {
					return err
				}
				deploymentplan.Map(containers[index])["image"] = op.Value
			}
			events = append(events, "patch-"+tgt.Name)
			return nil
		},
		Wait: func(_ context.Context, tgt ciRolloutTarget) error {
			if tgt.Name == "mqtt-pki" && environmentReleaseSlotImage(objects[tgt.Name], "mqtt") == nextB {
				return errors.New("unhealthy")
			}
			return nil
		},
		Commit: func(context.Context) (func(context.Context) error, error) {
			events = append(events, "commit")
			return nil, nil
		},
		Record: func(_ context.Context, status string, _ ciRolloutTarget, _, _ string) error {
			events = append(events, "record-"+status)
			return nil
		},
	}
	err := executeCIRolloutBatch(context.Background(), plan, ciBranchSelection{repo: "main"}, repo, "main", images, hooks)
	if err == nil || environmentReleaseSlotImage(first, "controller") != oldA || environmentReleaseSlotImage(second, "broker") != oldA || environmentReleaseSlotImage(second, "mqtt") != oldB || events[len(events)-1] != "release" || strings.Contains(strings.Join(events, ","), "commit") {
		t.Fatalf("batch recovery failed: %v %v", err, events)
	}
}

func TestCIRolloutImageRepositoriesComeFromSelectedPlan(t *testing.T) {
	repo := "hkt999rtk/rtk_video_cloud"
	a := "ghcr.io/" + repo + "/video-cloud-api"
	b := "ghcr.io/" + repo + "/video-cloud-emqx-pki"
	workload := deploymentplan.Object{"apiVersion": "apps/v1", "kind": "Deployment", "metadata": map[string]any{"namespace": "video-cloud-dev-video-cloud", "name": "mqtt-pki"}, "spec": map[string]any{"template": map[string]any{"spec": map[string]any{"containers": []any{map[string]any{"name": "mqtt", "image": b + "@sha256:" + strings.Repeat("a", 64)}, map[string]any{"name": "broker", "image": a + "@sha256:" + strings.Repeat("b", 64)}, map[string]any{"name": "other", "image": "other/image:v1"}}}}}}
	plan := &deploymentplan.Plan{Inputs: deploymentplan.Inputs{Environment: "dev", Stack: "video-cloud-dev"}, Resources: []deploymentplan.Resource{{UID: "uid", Desired: workload}}}
	got, err := ciRolloutImageRepositories(plan, repo)
	if err != nil || len(got) != 2 || got[0] != a || got[1] != b {
		t.Fatalf("unexpected image repositories %#v: %v", got, err)
	}
	if _, err = ciRolloutImageRepositories(plan, "hkt999rtk/rtk_billing"); err == nil {
		t.Fatal("unselected image repository accepted")
	}
}
