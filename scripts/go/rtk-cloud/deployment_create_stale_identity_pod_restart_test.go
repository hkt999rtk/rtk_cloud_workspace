package main

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

type stalePodTestBackend struct {
	freshStageBackend
	interrupt   bool
	deletes     int
	replacement deploymentplan.Object
	leases      []string
}

func (b *stalePodTestBackend) Inventory(ctx context.Context, _ string) ([]deploymentplan.Object, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	var objects []deploymentplan.Object
	for _, o := range b.objects {
		objects = append(objects, deploymentplan.Clone(o))
	}
	return objects, nil
}
func (b *stalePodTestBackend) Write(ctx context.Context, action string, body deploymentplan.Object, dry bool) (deploymentplan.Object, error) {
	if action != "retire" {
		return nil, errors.New("unexpected write")
	}
	if dry {
		return deploymentplan.Clone(body), nil
	}
	id := deploymentplan.ID(body)
	current := b.objects[id]
	if current == nil || deploymentplan.Map(current["metadata"])["uid"] != deploymentplan.Map(body["metadata"])["uid"] {
		return nil, errors.New("UID precondition failed")
	}
	b.deletes++
	delete(b.objects, id)
	return deploymentplan.Clone(body), nil
}
func (b *stalePodTestBackend) Wait(ctx context.Context, r deploymentplan.Resource) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if r.Action == "retire" && b.interrupt {
		b.interrupt = false
		return errors.New("interrupted after deletion")
	}
	if r.Action == "retain" {
		b.objects[deploymentplan.ID(b.replacement)] = deploymentplan.Clone(b.replacement)
		uid := deploymentplan.String(deploymentplan.Map(b.replacement["metadata"])["uid"])
		for _, id := range b.leases {
			spec := deploymentplan.Map(b.objects[id]["spec"])
			spec["holderIdentity"] = fmt.Sprint(uid, ":new-instance")
			spec["renewTime"] = time.Now().UTC().Format(time.RFC3339Nano)
		}
	}
	return nil
}

func TestStaleIdentityPodRestartResumesAfterDelete(t *testing.T) {
	store, e, _, base := freshStageFixture(t)
	plan, journal, target, objects, now := staleIdentityPodFixture(t)
	candidate, rsID, rsUID, podID, podUID, leases, err := selectStaleIdentityPod(plan, journal, target, objects, now)
	if err != nil {
		t.Fatal(err)
	}
	q := staleIdentityPodProof{Schema: staleIdentityPodProofSchema, Environment: store.Environment, ParentSHA: e.Journal.PlanSHA256, ParentOwnerSHA: deploymentplan.Digest(e.owner), SourceJournal: e.Journal, SourceJournalSHA: deploymentplan.Digest(e.Journal), MountReview: deploymentplan.Digest("mount"), RetainStage: "retained-stage", RetainPlanSHA: deploymentplan.Digest("retained"), RetainJournalSHA: deploymentplan.Digest("journal"), DeploymentID: target.ID, DeploymentUID: target.UID, ReplicaSetID: rsID, ReplicaSetUID: rsUID, PodID: podID, PodUID: podUID, Leases: leases, Candidate: candidate, QualifiedAt: now, ExecutionDeadline: now.Add(time.Hour)}
	b := &stalePodTestBackend{freshStageBackend: base, interrupt: true, replacement: deploymentplan.Clone(objects[2])}
	for _, obj := range objects {
		b.objects[deploymentplan.ID(obj)] = deploymentplan.Clone(obj)
	}
	m := deploymentplan.Map(b.replacement["metadata"])
	m["name"], m["uid"] = "mqtt-replacement", "66666666-6666-4666-8666-666666666666"
	deploymentplan.Map(certIssuerObjectList(deploymentplan.Map(b.replacement["status"])["conditions"])[0])["status"] = "True"
	for _, l := range leases {
		b.leases = append(b.leases, l.ID)
	}
	if err = executeStaleIdentityPodRestart(t.Context(), store, e, b, b, q, target); err == nil {
		t.Fatal("expected interruption after deleting old Pod")
	}
	if b.deletes != 1 || b.objects[podID] != nil {
		t.Fatal("old Pod not deleted exactly once")
	}
	if err = executeStaleIdentityPodRestart(t.Context(), store, e, b, b, q, target); err != nil {
		t.Fatal("retry failed", err)
	}
	if b.deletes != 1 {
		t.Fatal("retry deleted a Pod again")
	}
	entry := createOpenBaoACKCompletionEntry(e.Journal, "stale-identity-pod-"+deploymentplan.Digest(q))
	if entry == nil || entry.State != "completed" {
		t.Fatal("retry did not record completion")
	}
}
