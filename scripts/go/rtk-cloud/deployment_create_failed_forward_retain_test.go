package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func TestFailedFullyWrittenStageCanUseNativeRetainForward(t *testing.T) {
	store, e, p, b := freshStageFixture(t)
	b.failure = "after-write-configuration"
	check := func(ctx context.Context, _ *deploymentplan.Plan) error { return e.Check(ctx) }
	if err := executeEnvironmentCreateStagePlan(t.Context(), store, e, "workload", p, b, check, check, check, func(context.Context) error { return nil }, io.Discard); err == nil {
		t.Fatal("fixture did not fail")
	}
	j, exists, err := p.ReadExecution(store.Root)
	if err != nil || !exists || j.Failed == "" {
		t.Fatal(j, err)
	}
	j.Completed = []string{p.Resources[0].ID}
	raw, _ := json.Marshal(j)
	path := filepath.Join(store.Root, "deployment", "rollback", p.SHA256(), "journal.json")
	if err = os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	qualified, err := qualifyCreateFailedFullyWrittenStage(t.Context(), store, e, "workload", b)
	if err != nil {
		t.Fatal(err)
	}
	var sourceJournal environmentCreateJournal
	if err := json.Unmarshal(mustProviderRecoveryJSON(e.Journal), &sourceJournal); err != nil {
		t.Fatal(err)
	}
	image := environmentReleaseImage{Reference: "ghcr.io/hkt999rtk/rtk_video_cloud/video-cloud-api@sha256:" + strings.Repeat("a", 64), SourceRevision: strings.Repeat("b", 40)}
	job := deploymentplan.Object{"apiVersion": "batch/v1", "kind": "Job", "metadata": deploymentplan.Object{"namespace": e.Journal.Stack + "-video-cloud", "name": "repair-crl"}, "spec": deploymentplan.Object{"template": deploymentplan.Object{"spec": deploymentplan.Object{"containers": []any{deploymentplan.Object{"name": "enroll", "image": image.Reference, "command": []any{"/app/serviceidentity-bootstrap", "initialize-public-crl", "/tmp/path", "issuer", "fingerprint"}}}, "restartPolicy": "Never"}}}}
	repair, err := deploymentplan.Compile(p.Inputs, []deploymentplan.Definition{{Object: job, Phase: "bootstrap"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err = executeEnvironmentCreateStagePlan(t.Context(), store, e, "crl-repair", repair, b, check, check, check, func(context.Context) error { return nil }, io.Discard); err != nil {
		t.Fatal(err)
	}
	live := []deploymentplan.Object{deploymentplan.Clone(b.objects[p.Resources[0].ID])}
	forward, err := compileFailedStageRetainForward(p, j, qualified, live, "workload-retained-forward")
	if err != nil {
		t.Fatal(err)
	}
	if len(forward.Resources) != 1 || forward.Resources[0].Action != "retain" || forward.Resources[0].UID != j.Owners[p.Resources[0].ID] {
		t.Fatal("forward changed original owner")
	}
	changed := deploymentplan.Clone(live[0])
	deploymentplan.Map(changed["metadata"])["uid"] = "replacement"
	if _, err = compileFailedStageRetainForward(p, j, qualified, []deploymentplan.Object{changed}, "workload-retained-forward"); err == nil {
		t.Fatal("replacement owner accepted")
	}
	writes := b.writes[p.Resources[0].ID]
	acceptCalls := 0
	accept := func(context.Context, *deploymentplan.Plan) error {
		acceptCalls++
		if acceptCalls == 1 {
			return errors.New("isolated interrupted acceptance")
		}
		return nil
	}
	run := func() error {
		return executeEnvironmentCreateStagePlan(t.Context(), store, e, "workload-retained-forward", forward, b, check, check, accept, func(context.Context) error { return nil }, io.Discard)
	}
	if err = run(); err == nil {
		t.Fatal("interrupted native forward was accepted")
	}
	if entry := createOpenBaoACKCompletionEntry(e.Journal, "kubernetes/workload"); entry == nil || entry.State != "pending" {
		t.Fatal("failed forward changed original parent entry")
	}
	if err = run(); err != nil {
		t.Fatal(err)
	}
	if err = run(); err != nil {
		t.Fatal(err)
	}
	if b.writes[p.Resources[0].ID] != writes {
		t.Fatal("original resource was rewritten")
	}
	if _, forwardJournal, err := acceptedCreateStage(store, e, "kubernetes/workload-retained-forward"); err != nil || !forwardJournal.Accepted {
		t.Fatal("native forward has no accepted journal", err)
	}
	originalAfter, err := os.ReadFile(path)
	if err != nil || string(originalAfter) != string(raw) {
		t.Fatal("original failed journal changed", err)
	}
	admitted := issuerEnrollmentContinuationProof{Parent: e.Journal.PlanSHA256, ParentOwner: deploymentplan.Digest(e.owner)}
	admissionSHA := deploymentplan.Digest(admitted)
	release := environmentRelease{Images: map[string]environmentReleaseImage{"LKE_VIDEO_CLOUD_IMAGE": image}}
	publication := coreContractPublicationProofs(t, &release)[0]
	rawCaps, _ := json.Marshal(map[string]any{"schema": "rtk-cloud.deployment-capabilities/v1", "service": "serviceidentity-bootstrap", "features": []string{"initial-public-crl-custody-v2"}})
	caps, err := canonicalFailedForwardCapabilities(rawCaps)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	q := createFailedForwardProof{Schema: failedForwardProofSchema, Environment: e.Journal.Environment, ParentOwnerSHA256: admitted.ParentOwner, OriginalAdmissionSHA256: admissionSHA, SourceJournal: sourceJournal, FailedStage: qualified, CandidateSHA256: repair.SHA256(), RetainCandidateSHA256: forward.SHA256(), ToolImage: image, ToolPublication: publication, Capabilities: caps, CapabilitiesSHA256: environmentPreflightBytesSHA256(caps), QualifiedAt: now, ValidUntil: now.Add(time.Hour)}
	token := &verifiedCreateFailedForward{proofSHA: deploymentplan.Digest(q)}
	finish := func() error {
		return completeCreateFailedForwardAlias(t.Context(), store, e, b, q, admitted, admissionSHA, "crl-repair", "workload-retained-forward", token, check, time.Now().UTC())
	}
	// A crash may leave only an allocated alias. It is never accepted without
	// revalidating the exact proof and live retained owners.
	alias := createFailedForwardAlias{QualificationSHA256: deploymentplan.Digest(q), OriginalPlanSHA256: p.SHA256(), OriginalJournalSHA256: qualified.Child.JournalSHA256, FailedResource: j.Failed, RepairStage: "kubernetes/crl-repair", RepairPlanSHA256: repair.SHA256(), RetainStage: "kubernetes/workload-retained-forward", RetainPlanSHA256: forward.SHA256()}
	if err = e.RecordReceipt("kubernetes/workload", mustProviderRecoveryJSON(alias)); err != nil {
		t.Fatal(err)
	}
	badToken := &verifiedCreateFailedForward{proofSHA: deploymentplan.Digest("foreign")}
	if err = completeCreateFailedForwardAlias(t.Context(), store, e, b, q, admitted, admissionSHA, "crl-repair", "workload-retained-forward", badToken, check, time.Now().UTC()); err == nil {
		t.Fatal("unverified alias completed")
	}
	if entry := createOpenBaoACKCompletionEntry(e.Journal, "kubernetes/workload"); entry == nil || entry.State != "allocated" {
		t.Fatal("unverified alias advanced")
	}
	if err = finish(); err != nil {
		t.Fatal(err)
	}
	if err = finish(); err != nil {
		t.Fatal("completed alias retry", err)
	}
	originalAfter, err = os.ReadFile(path)
	if err != nil || string(originalAfter) != string(raw) {
		t.Fatal("alias rewrote original child journal", err)
	}
	if _, journal, err := acceptedCreateStage(store, e, "kubernetes/workload-retained-forward"); err != nil || !journal.Accepted {
		t.Fatal("forward cannot be used after parent alias", err)
	}
}
