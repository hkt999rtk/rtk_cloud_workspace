package main

import (
	"context"
	"io"
	"strings"
	"testing"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

// Exercise the real native child journal across a template-write interruption.
// The Pod remains unchanged until the separately guarded sidecar update.
func TestOpenBaoInPlaceTemplateStageResumesRecordedWrite(t *testing.T) {
	store, e, source, b := freshStageFixture(t)
	_, _, inventory := existingFileOpenBaoFixture("dev", true)
	original := deploymentplan.Clone(inventory[0])
	meta := deploymentplan.Map(original["metadata"])
	meta["labels"] = deploymentplan.Object{"rtk.realtek.com/stack": e.Journal.Stack, "app.kubernetes.io/managed-by": "rtk-cloud"}
	pod := deploymentplan.Object{"apiVersion": "v1", "kind": "Pod", "metadata": deploymentplan.Object{"name": "openbao-0", "namespace": meta["namespace"], "uid": "original-pod", "resourceVersion": "3", "ownerReferences": []any{deploymentplan.Object{"controller": true, "kind": "StatefulSet", "name": "openbao", "uid": meta["uid"]}}}, "spec": deploymentplan.Clone(environmentPKIPod(original))}
	addOpenBaoFixtureClaimVolumes(original, pod)
	target := "ghcr.io/hkt999rtk/rtk_video_cloud/video-cloud-openbao-pki@sha256:" + strings.Repeat("f", 64)
	before := deploymentplan.Digest(pod)
	id := deploymentplan.ID(original)
	b.objects[id] = deploymentplan.Clone(original)
	p, err := openBaoInPlaceTemplatePlan(source, original, original, target, "openbao-fixture")
	if err != nil || len(p.Resources) != 1 || p.Resources[0].Action != "update" {
		t.Fatal("no template-only candidate", err)
	}
	b.failure = "after-write-workload"
	qualify := func(ctx context.Context, p *deploymentplan.Plan) error { return p.AdmitResumable(ctx, b, store.Root) }
	fence := func(ctx context.Context, p *deploymentplan.Plan) error {
		return b.CheckParentExecution(ctx, p, store.Root)
	}
	accept := func(ctx context.Context, p *deploymentplan.Plan) error { return p.Verify(ctx, b) }
	run := func() error {
		return executeEnvironmentCreateStagePlan(t.Context(), store, e, "openbao-fixture", p, b, qualify, fence, accept, func(context.Context) error { return nil }, io.Discard)
	}
	if err := run(); err == nil || b.writes[id] != 1 {
		t.Fatal("did not interrupt after the original template write", err)
	}
	entry := createOpenBaoACKCompletionEntry(e.Journal, "kubernetes/openbao-fixture")
	if entry == nil {
		t.Fatal("missing parent intent")
	}
	started := entry.StartedAt
	intent := entry.IntentSHA256
	if err := run(); err != nil {
		t.Fatal("recorded original template did not resume", err)
	}
	entry = createOpenBaoACKCompletionEntry(e.Journal, "kubernetes/openbao-fixture")
	if entry == nil || entry.State != "completed" || !entry.StartedAt.Equal(started) || entry.IntentSHA256 != intent || b.writes[id] != 1 || deploymentplan.Digest(pod) != before {
		t.Fatal("retry replaced original owner or touched Pod")
	}
	tp, pp, err := compileOpenBaoInPlaceImagePatches(original, b.objects[id], pod, target)
	if err != nil || len(tp) != 0 || len(pp) != 5 {
		t.Fatal("recorded template must leave only guarded original Pod sidecar update", err)
	}
}
