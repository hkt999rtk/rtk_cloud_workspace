package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func TestFailedForwardRunnerRecoversAcrossNativeStageInterruption(t *testing.T) {
	store, e, source, b := freshStageFixture(t)
	b.failure = "after-write-configuration"
	check := func(ctx context.Context, _ *deploymentplan.Plan) error { return e.Check(ctx) }
	if err := executeEnvironmentCreateStagePlan(t.Context(), store, e, "workload", source, b, check, check, check, func(context.Context) error { return nil }, io.Discard); err == nil {
		t.Fatal("source did not fail")
	}
	j, _, err := source.ReadExecution(store.Root)
	if err != nil {
		t.Fatal(err)
	}
	j.Completed = []string{source.Resources[0].ID}
	original, _ := json.Marshal(j)
	journalPath := filepath.Join(store.Root, "deployment", "rollback", source.SHA256(), "journal.json")
	if err = os.WriteFile(journalPath, original, 0600); err != nil {
		t.Fatal(err)
	}
	stage, err := qualifyCreateFailedFullyWrittenStage(t.Context(), store, e, "workload", b)
	if err != nil {
		t.Fatal(err)
	}
	var snapshot environmentCreateJournal
	if err = json.Unmarshal(mustProviderRecoveryJSON(e.Journal), &snapshot); err != nil {
		t.Fatal(err)
	}
	image := environmentReleaseImage{Reference: "ghcr.io/hkt999rtk/rtk_video_cloud/video-cloud-api@sha256:" + strings.Repeat("a", 64), SourceRevision: strings.Repeat("b", 40)}
	manifest, _ := json.Marshal(map[string]string{"schema": "rtk-service-image/v1", "repository": "hkt999rtk/rtk_video_cloud", "source_commit": image.SourceRevision, "run_id": "70", "run_attempt": "2", "image": strings.Split(image.Reference, "@")[0], "digest": "sha256:" + strings.Repeat("a", 64)})
	archive := publicationZIP(t, "image-manifest.json", manifest)
	sourceRun := []byte(`{"id":70,"head_sha":"` + image.SourceRevision + `","head_branch":"main","event":"push","status":"completed","conclusion":"success","run_attempt":2}`)
	inventory := []byte(fmt.Sprintf(`{"total_count":1,"artifacts":[{"id":71,"name":"image-manifest","size_in_bytes":%d,"expired":false}]}`, len(archive)))
	caps := []byte(`{"schema":"rtk-cloud.deployment-capabilities/v1","service":"serviceidentity-bootstrap","features":["initial-public-crl-custody-v2"]}`)
	run := func(ctx context.Context, command string, args ...string) ([]byte, error) {
		if command == "gh" && len(args) == 2 {
			switch {
			case strings.HasSuffix(args[1], "/runs/70"):
				return sourceRun, nil
			case strings.HasSuffix(args[1], "/artifacts?per_page=100"):
				return inventory, nil
			case strings.HasSuffix(args[1], "/artifacts/71/zip"):
				return archive, nil
			}
		}
		if command == "docker" {
			if len(args) == 4 && args[0] == "pull" && args[1] == "--platform" && args[2] == "linux/amd64" && args[3] == image.Reference {
				return nil, nil
			}
			if len(args) > 2 && args[len(args)-2] == image.Reference && args[len(args)-1] == "--deployment-capabilities" {
				return caps, nil
			}
		}
		return nil, errors.New("unexpected command")
	}
	capture := newEnvironmentReleaseProofCapture(t.Context())
	if _, err = verifyEnvironmentPublication(capture, "hkt999rtk/rtk_video_cloud", 70, image, false, run); err != nil {
		t.Fatal(err)
	}
	release := environmentRelease{Images: map[string]environmentReleaseImage{"LKE_VIDEO_CLOUD_IMAGE": image}}
	publication := snapshotVerifiedReleaseProofs(capture, release)[0]
	job := deploymentplan.Object{"apiVersion": "batch/v1", "kind": "Job", "metadata": deploymentplan.Object{"namespace": e.Journal.Stack + "-video-cloud", "name": "repair-crl"}, "spec": deploymentplan.Object{"template": deploymentplan.Object{"spec": deploymentplan.Object{"containers": []any{deploymentplan.Object{"name": "enroll", "image": image.Reference, "command": []any{"/app/serviceidentity-bootstrap", "initialize-public-crl", "/tmp/path", "issuer", "fingerprint"}}}, "restartPolicy": "Never"}}}}
	repair, err := deploymentplan.Compile(source.Inputs, []deploymentplan.Definition{{Object: job, Phase: "bootstrap"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	retained, err := compileFailedStageRetainForward(source, j, stage, []deploymentplan.Object{deploymentplan.Clone(b.objects[source.Resources[0].ID])}, "repair-retain")
	if err != nil {
		t.Fatal(err)
	}
	admitted := issuerEnrollmentContinuationProof{Parent: e.Journal.PlanSHA256, ParentOwner: deploymentplan.Digest(e.owner)}
	admissionSHA := deploymentplan.Digest(admitted)
	now := time.Now().UTC()
	q := createFailedForwardProof{Schema: failedForwardProofSchema, Environment: e.Journal.Environment, ParentOwnerSHA256: admitted.ParentOwner, OriginalAdmissionSHA256: admissionSHA, SourceJournal: snapshot, FailedStage: stage, CandidateSHA256: repair.SHA256(), RetainCandidateSHA256: retained.SHA256(), ToolImage: image, ToolPublication: publication, Capabilities: caps, CapabilitiesSHA256: environmentPreflightBytesSHA256(caps), QualifiedAt: now, ValidUntil: now.Add(time.Hour)}
	render := func(context.Context) (*deploymentplan.Plan, *deploymentplan.Plan, error) {
		return repair, retained, nil
	}
	repairName, retainName := failedForwardStageNames(q)
	stopped := false
	execute := func(ctx context.Context, name string, plan *deploymentplan.Plan) error {
		if name == retainName && !stopped {
			stopped = true
			return errors.New("isolated interruption")
		}
		return executeEnvironmentCreateStagePlan(ctx, store, e, name, plan, b, check, check, check, func(context.Context) error { return nil }, io.Discard)
	}
	writes := b.writes[source.Resources[0].ID]
	perform := func(at time.Time) error {
		return executeCreateFailedForward(t.Context(), store, e, b, q, admitted, admissionSHA, render, run, execute, check, at)
	}
	if err = perform(now); err == nil {
		t.Fatal("native interruption did not stop")
	}
	if e := createOpenBaoACKCompletionEntry(e.Journal, "kubernetes/workload"); e == nil || e.State != "pending" {
		t.Fatal("original parent prematurely completed")
	}
	if err = perform(now.Add(2 * time.Hour)); err != nil {
		t.Fatal("admitted retry failed", err)
	}
	if err = perform(now.Add(3 * time.Hour)); err != nil {
		t.Fatal("completed retry failed", err)
	}
	if got, err := os.ReadFile(journalPath); err != nil || string(got) != string(original) {
		t.Fatal("original failed journal changed", err)
	}
	if b.writes[source.Resources[0].ID] != writes {
		t.Fatal("original workload rewritten")
	}
	if entry := createOpenBaoACKCompletionEntry(e.Journal, "kubernetes/workload"); entry == nil || entry.State != "completed" {
		t.Fatal("original parent not completed")
	}
	if entry := createOpenBaoACKCompletionEntry(e.Journal, "kubernetes/"+repairName); entry == nil || entry.State != "completed" {
		t.Fatal("CRL repair did not complete")
	}
}

func TestFailedForwardQualificationStoresLargeOriginalJournalPrivately(t *testing.T) {
	store, e, _, _ := freshStageFixture(t)
	q := createFailedForwardProof{Environment: e.Journal.Environment, SourceJournal: environmentCreateJournal{Entries: []environmentCreateJournalEntry{{Key: "large", Receipt: json.RawMessage(`"` + strings.Repeat("a", 300<<10) + `"`)}}}}
	review := deploymentplan.Digest(q)
	if err := writeFailedForwardQualification(store, e, review, q); err != nil {
		t.Fatal(err)
	}
	if err := writeFailedForwardQualification(store, e, review, q); err != nil {
		t.Fatal("idempotent retry", err)
	}
	path := filepath.Join(store.Root, "deployment", "create", e.Journal.PlanSHA256, "failed-stage-forward", review, "qualification.json")
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0600 || info.Size() < 300<<10 {
		t.Fatal("large private proof unavailable", err)
	}
	q.Environment = "other"
	if err := writeFailedForwardQualification(store, e, review, q); err == nil {
		t.Fatal("changed proof accepted")
	}
}
