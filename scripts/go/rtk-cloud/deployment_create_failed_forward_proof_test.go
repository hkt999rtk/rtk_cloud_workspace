package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func TestCreateFailedForwardProofBindsAdmissionStageRepairAndTool(t *testing.T) {
	now := time.Now().UTC()
	sha := func(s string) string { return deploymentplan.Digest(s) }
	admitted := issuerEnrollmentContinuationProof{Parent: sha("parent"), ParentOwner: sha("owner"), ParentJournal: sha("old-journal"), Checker: sha("checker"), Selection: sha("selection"), Release: sha("release"), FirstStage: sha("first")}
	admission := deploymentplan.Digest(admitted)
	source := environmentCreateJournal{Environment: "dev", Stack: "video-cloud-dev", PlanSHA256: admitted.Parent, Entries: []environmentCreateJournalEntry{{Key: "kubernetes/example-workloads", Phase: "example-workloads", State: "pending", IntentSHA256: sha("intent"), StartedAt: now.Add(-time.Minute)}}}
	stage := createFailedStageQualification{ParentSHA256: admitted.Parent, ParentJournalSHA256: deploymentplan.Digest(source), ParentIntentSHA256: sha("intent"), Stage: "example-workloads", Child: deploymentplan.FailedForwardEvidence{PlanSHA256: sha("child"), JournalSHA256: sha("failed-child"), FailedResource: "Deployment/video-cloud-dev-video-cloud/example"}}
	image := environmentReleaseImage{Reference: "ghcr.io/hkt999rtk/rtk_video_cloud/video-cloud-api@sha256:" + strings.Repeat("a", 64), SourceRevision: strings.Repeat("b", 40)}
	release := environmentRelease{Images: map[string]environmentReleaseImage{"LKE_VIDEO_CLOUD_IMAGE": image}}
	publication := coreContractPublicationProofs(t, &release)[0]
	publication.ObservedAt = now.Add(-time.Minute)
	job := deploymentplan.Object{"apiVersion": "batch/v1", "kind": "Job", "metadata": deploymentplan.Object{"namespace": "video-cloud-dev-video-cloud", "name": "test-crl"}, "spec": deploymentplan.Object{"template": deploymentplan.Object{"spec": deploymentplan.Object{"containers": []any{deploymentplan.Object{"name": "enroll", "image": image.Reference, "command": []any{"/app/serviceidentity-bootstrap", "initialize-public-crl", "/tmp/path", "issuer", "fingerprint"}}}, "restartPolicy": "Never"}}}}
	candidate, err := deploymentplan.Compile(deploymentplan.Inputs{Environment: "dev", Stack: "video-cloud-dev", Operation: "scoped", ConfigurationSHA256: sha("config"), ReleaseSHA256: sha("new-release"), BindingsSHA256: sha("bindings")}, []deploymentplan.Definition{{Object: job, Phase: "bootstrap"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	retainedDesired := deploymentplan.Object{"apiVersion": "v1", "kind": "ConfigMap", "metadata": deploymentplan.Object{"namespace": "video-cloud-dev-video-cloud", "name": "retained-owner", "labels": deploymentplan.Object{"rtk.realtek.com/stack": "video-cloud-dev"}}, "data": deploymentplan.Object{"trust": "original"}}
	retainedLive := deploymentplan.Clone(retainedDesired)
	deploymentplan.Map(retainedLive["metadata"])["uid"] = "00000001-1111-4111-8111-111111111111"
	retained, err := deploymentplan.Compile(candidate.Inputs, []deploymentplan.Definition{{Object: retainedDesired, Phase: "configuration", Retain: true}}, []deploymentplan.Object{retainedLive})
	if err != nil {
		t.Fatal(err)
	}
	rawCaps, _ := json.Marshal(map[string]any{"schema": "rtk-cloud.deployment-capabilities/v1", "service": "serviceidentity-bootstrap", "features": []string{"initial-public-crl-custody-v2"}})
	caps, err := canonicalFailedForwardCapabilities(rawCaps)
	if err != nil {
		t.Fatal(err)
	}
	q := createFailedForwardProof{Schema: failedForwardProofSchema, Environment: "dev", ParentOwnerSHA256: admitted.ParentOwner, OriginalAdmissionSHA256: admission, SourceJournal: source, FailedStage: stage, CandidateSHA256: candidate.SHA256(), RetainCandidateSHA256: retained.SHA256(), ToolImage: image, ToolPublication: publication, Capabilities: caps, CapabilitiesSHA256: environmentPreflightBytesSHA256(caps), QualifiedAt: now, ValidUntil: now.Add(time.Hour)}
	validate := func(v createFailedForwardProof) error {
		return validateCreateFailedForwardProofBindings(v, admitted, admission, stage, candidate, retained, now)
	}
	if err = validate(q); err != nil {
		t.Fatal(err)
	}
	t.Run("live evidence", func(t *testing.T) {
		store, execution, _, backend := freshStageFixture(t)
		backend.objects[deploymentplan.ID(retainedLive)] = deploymentplan.Clone(retainedLive)
		archive := publicationZIP(t, "image-manifest.json", publication.Manifest)
		source := []byte(fmt.Sprintf(`{"id":%d,"head_sha":%q,"head_branch":"main","event":"push","status":"completed","conclusion":"success","run_attempt":%d}`, publication.RunID, image.SourceRevision, publication.RunAttempt))
		inventory := []byte(fmt.Sprintf(`{"total_count":1,"artifacts":[{"id":%d,"name":"image-manifest","size_in_bytes":%d,"expired":false}]}`, publication.ArtifactID, len(archive)))
		mode := "valid"
		run := func(ctx context.Context, command string, args ...string) ([]byte, error) {
			if command == "gh" && len(args) == 2 && args[0] == "api" {
				if strings.HasSuffix(args[1], fmt.Sprintf("/runs/%d", publication.RunID)) {
					if mode == "failed-run" {
						return []byte(`{}`), nil
					}
					return source, nil
				}
				if strings.HasSuffix(args[1], "/artifacts?per_page=100") {
					return inventory, nil
				}
				if strings.HasSuffix(args[1], fmt.Sprintf("/artifacts/%d/zip", publication.ArtifactID)) {
					return archive, nil
				}
			}
			if command == "docker" {
				if len(args) == 4 && args[0] == "pull" && args[1] == "--platform" && args[2] == "linux/amd64" && args[3] == image.Reference {
					return nil, nil
				}
				if len(args) > 2 && args[len(args)-2] == image.Reference && args[len(args)-1] == "--deployment-capabilities" {
					if mode == "wrong-capability" {
						return []byte(`{}`), nil
					}
					return caps, nil
				}
			}
			return nil, errors.New("unexpected command")
		}
		capture := newEnvironmentReleaseProofCapture(t.Context())
		if _, err := verifyEnvironmentPublication(capture, publication.Repository, publication.RunID, image, false, run); err != nil {
			t.Fatal(err)
		}
		captured := snapshotVerifiedReleaseProofs(capture, release)
		if len(captured) != 1 {
			t.Fatal("missing captured proof")
		}
		liveNow := time.Now().UTC().Add(time.Second)
		live := q
		live.ToolPublication = captured[0]
		live.QualifiedAt = liveNow
		live.ValidUntil = liveNow.Add(time.Hour)
		render := func(context.Context) (*deploymentplan.Plan, *deploymentplan.Plan, error) {
			if mode == "changed-render" {
				changed := *candidate
				changed.BindingsSHA256 = sha("foreign")
				return &changed, retained, nil
			}
			return candidate, retained, nil
		}
		for _, mode = range []string{"valid", "failed-run", "wrong-capability", "changed-render"} {
			_, err := verifyCreateFailedForwardLiveEvidence(t.Context(), live, admitted, admission, stage, liveNow, store.Root, render, backend, run)
			if (err == nil) != (mode == "valid") {
				t.Fatalf("mode %s: %v", mode, err)
			}
		}
		mode = "valid"
		backend.failure = "after-write-bootstrap"
		check := func(ctx context.Context, _ *deploymentplan.Plan) error { return execution.Check(ctx) }
		if err := executeEnvironmentCreateStagePlan(t.Context(), store, execution, "partial-repair", candidate, backend, check, check, check, func(context.Context) error { return nil }, io.Discard); err == nil {
			t.Fatal("fixture did not interrupt after native Job write")
		}
		if _, err := verifyCreateFailedForwardLiveEvidence(t.Context(), live, admitted, admission, stage, liveNow, store.Root, render, backend, run); err != nil {
			t.Fatal("original partially written repair could not qualify", err)
		}
	})
	t.Run("nondefault environment stack", func(t *testing.T) {
		source2 := source
		source2.Stack = "configured-stack"
		stage2 := stage
		stage2.ParentJournalSHA256 = deploymentplan.Digest(source2)
		candidate2 := *candidate
		candidate2.Stack = source2.Stack
		retained2 := *retained
		retained2.Stack = source2.Stack
		q2 := q
		q2.SourceJournal = source2
		q2.FailedStage = stage2
		q2.CandidateSHA256 = candidate2.SHA256()
		q2.RetainCandidateSHA256 = retained2.SHA256()
		if err := validateCreateFailedForwardProofBindings(q2, admitted, admission, stage2, &candidate2, &retained2, now); err != nil {
			t.Fatal("configured stack rejected", err)
		}
	})
	for _, mode := range []string{"admission", "parent-owner", "parent-journal", "parent-intent", "child-journal", "candidate", "retain-candidate", "image", "manifest", "capabilities", "capability-content", "expired", "foreign-stack"} {
		t.Run(mode, func(t *testing.T) {
			v := q
			switch mode {
			case "admission":
				v.OriginalAdmissionSHA256 = sha("other")
			case "parent-owner":
				v.ParentOwnerSHA256 = sha("other")
			case "parent-journal":
				v.FailedStage.ParentJournalSHA256 = sha("other")
			case "parent-intent":
				v.FailedStage.ParentIntentSHA256 = sha("other")
			case "child-journal":
				v.FailedStage.Child.JournalSHA256 = sha("other")
			case "candidate":
				v.CandidateSHA256 = sha("other")
			case "retain-candidate":
				v.RetainCandidateSHA256 = sha("foreign-retain")
			case "image":
				v.ToolImage.Reference = "ghcr.io/hkt999rtk/rtk_video_cloud/video-cloud-api@sha256:" + strings.Repeat("c", 64)
			case "manifest":
				v.ToolPublication.Manifest = []byte(`{}`)
			case "capabilities":
				v.CapabilitiesSHA256 = sha("other")
			case "capability-content":
				v.Capabilities = []byte(`{"schema":"rtk-cloud.deployment-capabilities/v1","service":"serviceidentity-bootstrap","features":[]}`)
				v.CapabilitiesSHA256 = environmentPreflightBytesSHA256(v.Capabilities)
			case "expired":
				v.ValidUntil = now
			case "foreign-stack":
				v.Environment = "staging"
			}
			reasons := map[string]string{"admission": "original admission", "parent-owner": "original admission", "parent-journal": "original admission", "parent-intent": "original admission", "child-journal": "original admission", "candidate": "repair plan binding", "retain-candidate": "retained plan binding", "image": "formal image publication", "manifest": "formal image publication", "capabilities": "capability digest", "capability-content": "capability content", "expired": "qualification validity", "foreign-stack": "original parent journal"}
			if err := validate(v); err == nil || !strings.Contains(err.Error(), reasons[mode]) {
				t.Fatalf("mode %s lacked its safe diagnostic: %v", mode, err)
			}
		})
	}
}

func TestFailedForwardCapabilitiesCanonicalRoundTripAndRejectTampering(t *testing.T) {
	raw := []byte(" \n{\"features\":[\"owned-server-export-v1\",\"initial-service-crl-custody-v1\",\"initial-public-crl-custody-v2\"],\"schema\":\"rtk-cloud.deployment-capabilities/v1\",\"service\":\"serviceidentity-bootstrap\"}\n")
	canonical, err := canonicalFailedForwardCapabilities(raw)
	if err != nil {
		t.Fatal("real stdout rejected", err)
	}
	proof := createFailedForwardProof{Capabilities: canonical, CapabilitiesSHA256: environmentPreflightBytesSHA256(canonical)}
	serialized, err := json.Marshal(proof)
	if err != nil {
		t.Fatal(err)
	}
	var reloaded createFailedForwardProof
	if err = json.Unmarshal(serialized, &reloaded); err != nil {
		t.Fatal(err)
	}
	again, err := canonicalFailedForwardCapabilities(reloaded.Capabilities)
	if err != nil || !bytes.Equal(again, canonical) || environmentPreflightBytesSHA256(again) != reloaded.CapabilitiesSHA256 {
		t.Fatal("capability changed during persistence", err)
	}
	for _, invalid := range [][]byte{
		[]byte(`{"schema":"rtk-cloud.deployment-capabilities/v1","service":"serviceidentity-bootstrap","features":[]}`),
		[]byte(`{"schema":"rtk-cloud.deployment-capabilities/v1","service":"other","features":["initial-public-crl-custody-v2"]}`),
		[]byte(`{"schema":"rtk-cloud.deployment-capabilities/v1","service":"serviceidentity-bootstrap","features":["initial-public-crl-custody-v2"],"unexpected":"private"}`),
		append(append([]byte(nil), raw...), []byte(` {}`)...),
		[]byte(`{"schema":`),
	} {
		if _, err := canonicalFailedForwardCapabilities(invalid); err == nil {
			t.Fatal("accepted invalid or tampered capability")
		}
	}
	changed := []byte(`{"schema":"rtk-cloud.deployment-capabilities/v1","service":"serviceidentity-bootstrap","features":["initial-public-crl-custody-v2","different-feature"]}`)
	altered, err := canonicalFailedForwardCapabilities(changed)
	if err != nil || bytes.Equal(altered, canonical) || environmentPreflightBytesSHA256(altered) == proof.CapabilitiesSHA256 {
		t.Fatal("changed valid capability content retained original binding", err)
	}
}
