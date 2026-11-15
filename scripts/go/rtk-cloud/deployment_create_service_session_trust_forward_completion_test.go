package main

import (
	"context"
	"io"
	"testing"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func TestServiceSessionTrustForwardCompletesOriginalParentWithoutReplayingJob(t *testing.T) {
	for _, allocated := range []bool{false, true} {
		t.Run(map[bool]string{false: "pending", true: "interrupted-after-receipt"}[allocated], func(t *testing.T) {
			store, e, template, backend := freshStageFixture(t)
			originalReview, forwardReview, batchReview := deploymentplan.Digest("original"), deploymentplan.Digest("forward"), deploymentplan.Digest("batch")
			makePlan := func(stage, name string) *deploymentplan.Plan {
				body := deploymentplan.Object{"apiVersion": "batch/v1", "kind": "Job", "metadata": deploymentplan.Object{"name": name, "namespace": e.Journal.Stack + "-video-cloud"}, "spec": deploymentplan.Object{"template": deploymentplan.Object{"spec": deploymentplan.Object{"containers": []any{deploymentplan.Object{"name": "bootstrap", "image": "fixture"}}, "restartPolicy": "Never"}}}}
				defs := []deploymentplan.Definition{{Object: body, Phase: "bootstrap"}}
				in := template.Inputs
				in.BindingsSHA256 = environmentCreateStageBindingsSHA(stage, defs)
				p, err := deploymentplan.Compile(in, defs, nil)
				if err != nil {
					t.Fatal(err)
				}
				return p
			}
			check := func(ctx context.Context, _ *deploymentplan.Plan) error { return e.Check(ctx) }
			run := func(stage string, p *deploymentplan.Plan) error {
				return executeEnvironmentCreateStagePlan(t.Context(), store, e, stage, p, backend, check, check, check, func(context.Context) error { return nil }, io.Discard)
			}
			failedStage := "service-session-original-key-renewal-" + originalReview
			failedPlan := makePlan(failedStage, "failed-job")
			backend.failure = "after-write-bootstrap"
			if err := run(failedStage, failedPlan); err == nil {
				t.Fatal("expected interrupted original Job")
			}
			backend.failure, backend.failed = "", false
			failed, ok, err := failedPlan.ReadExecution(store.Root)
			if err != nil || !ok || failed.Failed == "" {
				t.Fatal("missing failed original journal", err)
			}
			stage := "partial-session-trust-forward-" + forwardReview
			candidate := makePlan(stage, "successor-job")
			f := &createManagedLateSeedForward{OriginalReview: originalReview, FailedPlanSHA: failedPlan.SHA256(), FailedJournalSHA: deploymentplan.Digest(failed), Candidate: candidate}
			forward := issuerEnrollmentContinuationProof{Parent: e.Journal.PlanSHA256, PartialTrustForward: &partialSessionTrustForwardProof{OriginalReview: originalReview, Forward: f}}
			key := "partial-session-trust-forward-" + forwardReview
			if err := e.BeginEntry(key, "partial-session-trust-forward", mustProviderRecoveryJSON(partialSessionTrustForwardIntent(forward.Parent, forwardReview, forward.PartialTrustForward)), time.Now().UTC()); err != nil {
				t.Fatal(err)
			}
			if err := run(stage, candidate); err != nil {
				t.Fatal(err)
			}
			alias := mustProviderRecoveryJSON(partialSessionTrustForwardAlias(f, forwardReview))
			if err := e.RecordReceipt("kubernetes/"+failedStage, alias); err != nil {
				t.Fatal(err)
			}
			if err := e.CompleteEntry("kubernetes/"+failedStage, time.Now().UTC()); err != nil {
				t.Fatal(err)
			}
			original := issuerEnrollmentContinuationProof{PartialSession: &partialSessionContinuationProof{Candidate: failedPlan}}
			journal, ok, err := candidate.ReadExecution(store.Root)
			if err != nil || !ok {
				t.Fatal(err)
			}
			if allocated {
				receipt := mustProviderRecoveryJSON(deploymentplan.Object{"qualification_sha256": forwardReview, "candidate_plan_sha256": candidate.SHA256(), "candidate_journal_sha256": deploymentplan.Digest(journal), "job_uid": journal.Owners[candidate.Resources[0].ID], "completed_session_batch_sha256": batchReview})
				if err := e.RecordReceipt(key, receipt); err != nil {
					t.Fatal(err)
				}
			}
			wrong := *f
			wrong.FailedJournalSHA = deploymentplan.Digest("wrong")
			badForward := forward
			badForward.PartialTrustForward = &partialSessionTrustForwardProof{OriginalReview: originalReview, Forward: &wrong}
			if err := completeServiceSessionTrustForwardRecord(t.Context(), store, e, original, originalReview, badForward, forwardReview, batchReview, time.Now().UTC()); err == nil {
				t.Fatal("changed original evidence accepted")
			}
			before := backend.writes[candidate.Resources[0].ID]
			for i := 0; i < 2; i++ {
				if err := completeServiceSessionTrustForwardRecord(t.Context(), store, e, original, originalReview, forward, forwardReview, batchReview, time.Now().UTC()); err != nil {
					t.Fatal(err)
				}
			}
			entry := createOpenBaoACKCompletionEntry(e.Journal, key)
			if entry == nil || entry.State != "completed" || backend.writes[candidate.Resources[0].ID] != before {
				t.Fatal("parent completion replayed or lost its Job")
			}
		})
	}
}
