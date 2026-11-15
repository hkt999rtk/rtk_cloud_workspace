package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func TestCIRolloutExecutionUsesLeaseAndRecordsOutcome(t *testing.T) {
	repo := "hkt999rtk/rtk_cloud_frontend"
	imageRepo := "ghcr.io/" + repo + "/frontend"
	revision := strings.Repeat("a", 40)
	old := imageRepo + "@sha256:" + strings.Repeat("b", 64)
	next := imageRepo + "@sha256:" + strings.Repeat("c", 64)
	plan := &deploymentplan.Plan{Inputs: deploymentplan.Inputs{Environment: "dev", Stack: "video-cloud-dev"}, Resources: []deploymentplan.Resource{{UID: "uid", Desired: deploymentplan.Object{"apiVersion": "apps/v1", "kind": "Deployment", "metadata": map[string]any{"namespace": "video-cloud-dev-frontend", "name": "frontend"}, "spec": map[string]any{"template": map[string]any{"spec": map[string]any{"containers": []any{map[string]any{"name": "frontend", "image": old}}}}}}}}}
	selected := environmentReleaseImage{Reference: next, SourceRevision: revision, Publication: &createVersionPublication{Repository: repo, RunID: 22, Attempt: 1}}
	object := func(image string) []byte {
		return []byte(fmt.Sprintf(`{"apiVersion":"apps/v1","kind":"Deployment","metadata":{"namespace":"video-cloud-dev-frontend","name":"frontend","uid":"uid","resourceVersion":"42"},"spec":{"template":{"spec":{"containers":[{"name":"frontend","image":%q}]}}}}`, image))
	}
	for _, mode := range []string{"success", "stale-branch", "readiness-failed"} {
		t.Run(mode, func(t *testing.T) {
			var events []string
			current := old
			hooks := ciRolloutExecutionHooks{
				Acquire: func(context.Context) (func() error, error) {
					events = append(events, "lease")
					return func() error { events = append(events, "release"); return nil }, nil
				},
				Head: func(context.Context, string, string) (string, error) {
					if mode == "stale-branch" {
						return strings.Repeat("d", 40), nil
					}
					return revision, nil
				},
				Read: func(context.Context, ciRolloutTarget) ([]byte, error) { return object(current), nil },
				Patch: func(_ context.Context, _ ciRolloutTarget, patch []byte, dry bool) error {
					if dry {
						events = append(events, "dry-run")
						return nil
					}
					if strings.Contains(string(patch), next) && current == old {
						current = next
						events = append(events, "apply")
					} else {
						current = old
						events = append(events, "rollback")
					}
					return nil
				},
				Wait: func(context.Context, ciRolloutTarget) error {
					events = append(events, "wait")
					if mode == "readiness-failed" && current == next {
						return errors.New("unhealthy")
					}
					return nil
				},
				Commit: func(context.Context) (func(context.Context) error, error) {
					events = append(events, "commit-version")
					return func(context.Context) error { events = append(events, "undo-version"); return nil }, nil
				},
				Record: func(_ context.Context, status string, _ ciRolloutTarget, _ string) error {
					events = append(events, "record-"+status)
					return nil
				},
			}
			err := executeCIRollout(context.Background(), ciBranchSelection{repo: "main"}, repo, "main", plan, selected, hooks)
			if mode == "success" {
				if err != nil || current != next || strings.Join(events, ",") != "lease,record-started,dry-run,record-applying,apply,wait,commit-version,record-completed,release" {
					t.Fatalf("unexpected success %v: %v", err, events)
				}
				return
			}
			if err == nil || current != old {
				t.Fatalf("unsafe failure %v: %v current=%s", err, events, current)
			}
			if mode == "stale-branch" && strings.Contains(strings.Join(events, ","), "apply") {
				t.Fatal("stale branch updated workload")
			}
			if mode == "readiness-failed" && !strings.Contains(strings.Join(events, ","), "rollback") {
				t.Fatal("failed rollout did not restore prior image")
			}
		})
	}
}
