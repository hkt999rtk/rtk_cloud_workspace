package main

import (
	"context"
	"io"
	"strings"
	"testing"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func TestObservedStalePodReconciliationPreservesExpiredDeadlineAndNeverDeletes(t *testing.T) {
	store, e, _, base := freshStageFixture(t)
	plan, journal, target, objects, now := staleIdentityPodFixture(t)
	candidate, rsID, rsUID, podID, podUID, leases, err := selectStaleIdentityPod(plan, journal, target, objects, now)
	if err != nil {
		t.Fatal(err)
	}
	b := &stalePodTestBackend{freshStageBackend: base}
	for _, object := range objects {
		b.objects[deploymentplan.ID(object)] = deploymentplan.Clone(object)
	}
	check := func(ctx context.Context, _ *deploymentplan.Plan) error { return e.Check(ctx) }
	if err := executeEnvironmentCreateStagePlan(t.Context(), store, e, "retained-stage", plan, base, check, check, check, func(context.Context) error { return nil }, io.Discard); err != nil {
		t.Fatal(err)
	}
	accepted, exists, err := plan.ReadExecution(store.Root)
	if err != nil || !exists || !accepted.Accepted {
		t.Fatal(err)
	}
	qualified := time.Now().UTC()
	q := staleIdentityPodProof{Schema: staleIdentityPodProofSchema, Environment: store.Environment, ParentSHA: e.Journal.PlanSHA256, ParentOwnerSHA: deploymentplan.Digest(e.owner), SourceJournal: e.Journal, SourceJournalSHA: deploymentplan.Digest(e.Journal), MountReview: deploymentplan.Digest("mount"), RetainStage: "retained-stage", RetainPlanSHA: plan.SHA256(), RetainJournalSHA: deploymentplan.Digest(accepted), DeploymentID: target.ID, DeploymentUID: target.UID, ReplicaSetID: rsID, ReplicaSetUID: rsUID, PodID: podID, PodUID: podUID, Leases: leases, Candidate: candidate, QualifiedAt: qualified, ExecutionDeadline: qualified.Add(time.Nanosecond)}
	key := "stale-identity-pod-" + deploymentplan.Digest(q)
	if err := e.BeginEntry(key, "stale-identity-pod", staleIdentityPodIntent(q), qualified); err != nil {
		t.Fatal(err)
	}
	before := deploymentplan.Digest(q)
	if !time.Now().After(q.ExecutionDeadline) {
		t.Fatal("synthetic historical deadline must already be expired; no expiration wait is used")
	}
	if err := reconcileObservedStaleIdentityPod(t.Context(), store, e, b, q); err == nil {
		t.Fatal("accepted original UID still present")
	}
	delete(b.objects, podID)
	replacement := deploymentplan.Clone(objects[2])
	deploymentplan.Map(replacement["metadata"])["name"] = "mqtt-replacement"
	replacementUID := "66666666-6666-4666-8666-666666666666"
	deploymentplan.Map(replacement["metadata"])["uid"] = replacementUID
	deploymentplan.Map(certIssuerObjectList(deploymentplan.Map(replacement["status"])["conditions"])[0])["status"] = "True"
	b.objects[deploymentplan.ID(replacement)] = replacement
	for _, lease := range leases {
		spec := deploymentplan.Map(b.objects[lease.ID]["spec"])
		spec["holderIdentity"] = replacementUID + ":instance"
		spec["renewTime"] = time.Now().UTC().Format(time.RFC3339Nano)
	}
	// A Ready Pod cannot adopt a replaced permanent identity Lease.
	oldUID := deploymentplan.Map(b.objects[leases[0].ID]["metadata"])["uid"]
	deploymentplan.Map(b.objects[leases[0].ID]["metadata"])["uid"] = strings.Repeat("f", 36)
	if err := reconcileObservedStaleIdentityPod(t.Context(), store, e, b, q); err == nil {
		t.Fatal("accepted a foreign Lease UID")
	}
	deploymentplan.Map(b.objects[leases[0].ID]["metadata"])["uid"] = oldUID
	if err := reconcileObservedStaleIdentityPod(t.Context(), store, e, b, q); err != nil {
		t.Fatal(err)
	}
	entry := createOpenBaoACKCompletionEntry(e.Journal, key)
	if entry.State != "completed" || deploymentplan.Digest(q) != before || b.deletes != 0 {
		t.Fatal("reconciliation mutated deadline, identity, or deleted a Pod")
	}
	snapshot := deploymentplan.Digest(e.Journal)
	if err := reconcileObservedStaleIdentityPod(t.Context(), store, e, b, q); err != nil || deploymentplan.Digest(e.Journal) != snapshot {
		t.Fatal("reconciliation retry changed its receipt", err)
	}
	originalReceipt := append([]byte(nil), entry.Receipt...)
	entry.Receipt = mustProviderRecoveryJSON(deploymentplan.Object{"qualification_sha256": "foreign"})
	if err := e.save(); err != nil {
		t.Fatal(err)
	}
	if err := reconcileObservedStaleIdentityPod(t.Context(), store, e, b, q); err == nil {
		t.Fatal("accepted a changed completed reconciliation receipt")
	}
	entry.Receipt = originalReceipt
	if err := e.save(); err != nil {
		t.Fatal(err)
	}
}
