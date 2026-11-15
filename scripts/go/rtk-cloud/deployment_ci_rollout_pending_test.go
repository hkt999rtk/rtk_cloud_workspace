package main

import (
	"context"

	"encoding/json"
	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
	"strings"
	"testing"
)

func TestCIRolloutPendingStatePreservesRecoveryIntent(t *testing.T) {
	repo := "hkt999rtk/rtk_cloud_frontend"
	image := "ghcr.io/" + repo + "/frontend@sha256:" + strings.Repeat("a", 64)
	next := "ghcr.io/" + repo + "/frontend@sha256:" + strings.Repeat("b", 64)
	raw := []byte(`{"apiVersion":"v1","kind":"ConfigMap","metadata":{"namespace":"video-cloud-dev-video-cloud","name":"rtk-ci-state-rtk-cloud-frontend","uid":"state-uid","resourceVersion":"5"},"data":{"state.json":""}}`)
	pending := ciRolloutPending{Repository: repo, Branch: "main", RunID: 11, Attempt: 1, LeaseUID: "lease-uid", Holder: "holder", Changes: []ciRolloutPendingChange{{Namespace: "video-cloud-dev-frontend", Name: "frontend", UID: "uid", Container: "app", PreviousImage: image, DesiredImage: next}}}
	patch, err := ciRolloutPendingStatePatch(raw, "video-cloud-dev-video-cloud", repo, "main", &pending)
	if err != nil {
		t.Fatal(err)
	}
	var ops []struct {
		Value string `json:"value"`
	}
	if json.Unmarshal(patch, &ops) != nil || len(ops) != 4 || !strings.Contains(ops[3].Value, image) || !strings.Contains(ops[3].Value, next) || !strings.Contains(ops[3].Value, "lease-uid") {
		t.Fatalf("missing durable recovery intent: %s", patch)
	}
}

func TestCIRolloutRecoversOnlyItsCompletedPriorRun(t *testing.T) {
	repo := "hkt999rtk/rtk_cloud_frontend"
	lease := deploymentplan.Object{"apiVersion": "coordination.k8s.io/v1", "kind": "Lease", "metadata": map[string]any{"namespace": "video-cloud-dev-video-cloud", "name": "rtk-deployment-execution", "uid": "lease-uid", "resourceVersion": "3", "annotations": map[string]any{"rtk.realtek.com/ci-repository": repo, "rtk.realtek.com/ci-run-id": "10", "rtk.realtek.com/ci-run-attempt": "1"}}, "spec": map[string]any{"holderIdentity": "holder"}}
	pending := &ciRolloutPending{Repository: repo, Branch: "main", RunID: 10, Attempt: 1, LeaseUID: "lease-uid", Holder: "holder"}
	status := func(_ context.Context, _ string, _ ...string) ([]byte, error) {
		return []byte(`{"id":10,"run_attempt":1,"status":"completed"}`), nil
	}
	ok, err := ciRolloutCanRecoverLease(context.Background(), repo, 11, 1, lease, pending, status)
	if err != nil || !ok {
		t.Fatal("completed original run must be recoverable", err)
	}
	running := func(_ context.Context, _ string, _ ...string) ([]byte, error) {
		return []byte(`{"id":10,"run_attempt":1,"status":"in_progress"}`), nil
	}
	if ok, err = ciRolloutCanRecoverLease(context.Background(), repo, 11, 1, lease, pending, running); err != nil || ok {
		t.Fatal("active owner was treated as stopped", err)
	}
	if ok, err = ciRolloutCanRecoverLease(context.Background(), "hkt999rtk/rtk_billing", 11, 1, lease, pending, status); err != nil || ok {
		t.Fatal("foreign owner was treated as recoverable", err)
	}
	pending.LeaseUID = "replacement"
	if _, err = ciRolloutCanRecoverLease(context.Background(), repo, 11, 1, lease, pending, status); err == nil {
		t.Fatal("different pending lease identity was accepted")
	}
}
