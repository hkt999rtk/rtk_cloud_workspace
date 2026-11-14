package main

import (
	"bytes"
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

func TestFailedForwardJobsUseOriginalResourcesAndRepairImage(t *testing.T) {
	cfg, p, release, runtime, service, roots, intermediates, initial := createApplicationFinalizeFixture(t, "dev")
	cfg.Values["PKI_STATE_BACKEND"], cfg.Values["PKI_STATE_KV_MOUNT"], cfg.Values["PKI_STATE_LEASE_DURATION"] = "openbao", "identities", "30s"
	final, err := finalizeEnvironmentCreateApplicationDefinitions(t.Context(), cfg, p, release, runtime, service, roots, intermediates, initial)
	if err != nil {
		t.Fatal(err)
	}
	source := &deploymentplan.Plan{Version: deploymentplan.Version, Inputs: deploymentplan.Inputs{Environment: cfg.Environment, Stack: p.Stack, Operation: "scoped"}}
	for _, d := range final {
		source.Resources = append(source.Resources, deploymentplan.Resource{ID: deploymentplan.ID(d.Object), Desired: deploymentplan.Clone(d.Object), Phase: d.Phase})
	}
	trust := deploymentplan.Object{"apiVersion": "v1", "kind": "ConfigMap", "metadata": deploymentplan.Object{"name": "pki-create-service-trust", "namespace": p.Stack + "-video-cloud"}, "data": deploymentplan.Object{"outbound-crls.json": environmentCreateServiceCRLManifest(service.Root, environmentCreateCoreCRLPath("outbound"), service.Intermediate)}}
	issuers := []environmentCreateRegisteredIssuer{service.Root, service.Intermediate, roots["mqtt"], intermediates["mqtt"], roots["device"], roots["app"], intermediates["app"]}
	before := source.SHA256()
	tool := environmentReleaseImage{Reference: p.Image, SourceRevision: "1111111111111111111111111111111111111111"}
	jobs, err := renderCreateFailedForwardJobs(t.Context(), cfg, p, runtime, service, source, source.SHA256(), trust, issuers, tool)
	if err != nil || len(jobs) == 0 {
		t.Fatal(len(jobs), err)
	}
	if source.SHA256() != before {
		t.Fatal("rewrote original child plan")
	}
	for _, j := range jobs {
		c := managedNamedField(environmentPKIPod(j.Object)["containers"], "enroll")
		if c["image"] != tool.Reference {
			t.Fatal("job did not use exact repair image")
		}
	}
	if _, err = renderCreateFailedForwardJobs(t.Context(), cfg, p, runtime, service, source, deploymentplan.Digest("other"), trust, issuers, tool); err == nil {
		t.Fatal("unqualified source accepted")
	}
}

func TestPrepareFailedForwardCandidatesFromOriginalStageAndTrust(t *testing.T) {
	cfg, p, release, runtime, service, roots, intermediates, initial := createApplicationFinalizeFixture(t, "dev")
	cfg.Values["PKI_STATE_BACKEND"], cfg.Values["PKI_STATE_KV_MOUNT"], cfg.Values["PKI_STATE_LEASE_DURATION"] = "openbao", "identities", "30s"
	final, err := finalizeEnvironmentCreateApplicationDefinitions(t.Context(), cfg, p, release, runtime, service, roots, intermediates, initial)
	if err != nil {
		t.Fatal(err)
	}
	store, e, _, backend := freshStageFixture(t)
	check := func(ctx context.Context, _ *deploymentplan.Plan) error { return e.Check(ctx) }
	trust := deploymentplan.Object{"apiVersion": "v1", "kind": "ConfigMap", "metadata": deploymentplan.Object{"name": "pki-create-service-trust", "namespace": p.Stack + "-video-cloud"}, "data": deploymentplan.Object{"outbound-crls.json": environmentCreateServiceCRLManifest(service.Root, environmentCreateCoreCRLPath("outbound"), service.Intermediate)}}
	inputs := deploymentplan.Inputs{Environment: cfg.Environment, Stack: p.Stack, Operation: "scoped", ConfigurationSHA256: deploymentplan.Digest(cfg), ReleaseSHA256: deploymentplan.Digest(release), BindingsSHA256: deploymentplan.Digest("source")}
	trustPlan, err := deploymentplan.Compile(inputs, []deploymentplan.Definition{{Object: trust, Phase: "configuration"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err = executeEnvironmentCreateStagePlan(t.Context(), store, e, "original-service-trust", trustPlan, backend, check, check, check, func(context.Context) error { return nil }, io.Discard); err != nil {
		t.Fatal(err)
	}
	entry := createOpenBaoACKCompletionEntry(e.Journal, "kubernetes/original-service-trust")
	var formatted bytes.Buffer
	if entry == nil || json.Indent(&formatted, entry.Receipt, "", "  ") != nil {
		t.Fatal("cannot format original receipt fixture")
	}
	entry.Receipt = formatted.Bytes()
	if err = e.save(); err != nil {
		t.Fatal("cannot save isolated formatted receipt", err)
	}
	original, err := deploymentplan.Compile(inputs, final, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err = saveEnvironmentCreateStageInput(t.Context(), store, e, "managed-application-owners", original); err != nil {
		t.Fatal(err)
	}
	intent, err := environmentCreateKubernetesStageIntent(original)
	if err != nil {
		t.Fatal(err)
	}
	if err = e.BeginEntry("kubernetes/managed-application-owners", "managed-application-owners", intent, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	lease := deploymentplan.LeaseOwner{ResourceID: "Lease/" + p.Stack + "-video-cloud/rtk-deployment-execution", UID: "00000002-2222-4222-8222-222222222222", Holder: original.SHA256() + ":fixture"}
	if err = e.RecordKubernetesLease(lease); err != nil {
		t.Fatal(err)
	}
	journal := deploymentplan.Journal{PlanSHA256: original.SHA256(), Owners: map[string]string{}}
	for i, r := range original.Resources {
		uid := fmt.Sprintf("%08d-1111-4111-8111-111111111111", i+20)
		actual := deploymentplan.Clone(r.Desired)
		deploymentplan.Map(actual["metadata"])["uid"] = uid
		backend.objects[r.ID] = actual
		journal.Completed = append(journal.Completed, r.ID)
		journal.Owners[r.ID] = uid
		if r.ID == "Deployment/"+p.Stack+"-video-cloud/factoryenroll" {
			journal.Failed = r.ID
		}
	}
	if journal.Failed == "" {
		t.Fatal("fixture missing factory")
	}
	planRaw, _ := json.Marshal(original)
	journalRaw, _ := json.Marshal(journal)
	base := filepath.Join("deployment", "rollback", original.SHA256())
	if err = store.write(filepath.Join(base, "plan.json"), planRaw, false); err != nil {
		t.Fatal(err)
	}
	if err = store.write(filepath.Join(base, "journal.json"), journalRaw, false); err != nil {
		t.Fatal(err)
	}
	image := environmentReleaseImage{Reference: p.Image, SourceRevision: strings.Repeat("b", 40)}
	imageName, imageDigest, ok := strings.Cut(image.Reference, "@")
	if !ok {
		t.Fatal("invalid immutable test image")
	}
	manifest, _ := json.Marshal(map[string]any{"schema": "rtk-service-image/v1", "repository": "hkt999rtk/rtk_video_cloud", "source_commit": image.SourceRevision, "run_id": "70", "run_attempt": "2", "image": imageName, "digest": imageDigest, "service_registration_manifests": map[string]any{"ota": otaRegistrationManifestFixture()}})
	manifest = append(manifest, '\n')
	image, err = selectEnvironmentPublishedImage(manifest, "hkt999rtk/rtk_video_cloud", 70, 2, image.SourceRevision, imageName, false)
	if err != nil || image.OTARegistration == nil {
		t.Fatal("formal API manifest did not provide its OTA descriptor", err)
	}
	archive := publicationZIP(t, "image-manifest.json", manifest)
	sourceRun := []byte(`{"id":70,"head_sha":"` + image.SourceRevision + `","head_branch":"main","event":"push","status":"completed","conclusion":"success","run_attempt":2}`)
	artifactInventory := []byte(fmt.Sprintf(`{"total_count":1,"artifacts":[{"id":71,"name":"image-manifest","size_in_bytes":%d,"expired":false}]}`, len(archive)))
	rawCaps := []byte(" \n{\"features\":[\"owned-server-export-v1\",\"committed-identity-inspection-v1\",\"committed-identity-batch-admission-v1\",\"committed-public-crl-maintenance-v1\",\"initial-service-crl-custody-v1\",\"initial-public-crl-custody-v2\"],\"schema\":\"rtk-cloud.deployment-capabilities/v1\",\"service\":\"serviceidentity-bootstrap\"}\n")
	caps, err := canonicalFailedForwardCapabilities(rawCaps)
	if err != nil {
		t.Fatal("real-format capability rejected", err)
	}
	run := func(_ context.Context, command string, args ...string) ([]byte, error) {
		if command == "gh" && len(args) == 2 {
			switch {
			case strings.HasSuffix(args[1], "/runs/70"):
				return sourceRun, nil
			case strings.HasSuffix(args[1], "/artifacts?per_page=100"):
				return artifactInventory, nil
			case strings.HasSuffix(args[1], "/artifacts/71/zip"):
				return archive, nil
			}
		}
		if command == "docker" && len(args) == 4 && args[0] == "pull" && args[1] == "--platform" && args[2] == "linux/amd64" && args[3] == image.Reference {
			return nil, nil
		}
		if command == "docker" && len(args) > 2 && args[len(args)-2] == image.Reference && args[len(args)-1] == "--deployment-capabilities" {
			return rawCaps, nil
		}
		return nil, errors.New("unexpected isolated CI command")
	}
	proofCtx := newEnvironmentReleaseProofCapture(t.Context())
	if _, err = verifyEnvironmentPublication(proofCtx, "hkt999rtk/rtk_video_cloud", 70, image, false, run); err != nil {
		t.Fatal(err)
	}
	rel := environmentRelease{Images: map[string]environmentReleaseImage{"LKE_VIDEO_CLOUD_IMAGE": image}}
	pub := snapshotVerifiedReleaseProofs(proofCtx, rel)[0]
	tool := failedForwardTool{Image: image, Publication: pub, Capabilities: caps}
	admitted := issuerEnrollmentContinuationProof{Parent: e.Journal.PlanSHA256, ParentOwner: deploymentplan.Digest(e.owner)}
	admission := deploymentplan.Digest(admitted)
	issuerList := []environmentCreateRegisteredIssuer{service.Root, service.Intermediate, roots["mqtt"], intermediates["mqtt"], roots["device"], roots["app"], intermediates["app"]}
	binding, err := createEnvironmentCustodyBinding(cfg, p, e.Journal.PlanSHA256, "")
	if err != nil {
		t.Fatal(err)
	}
	project := func(ctx context.Context, defs []deploymentplan.Definition) ([]deploymentplan.Definition, error) {
		return projectEnvironmentCustodyResources(cfg.Values, cfg.Environment, binding, strings.Repeat("b", 64), defs)
	}
	inventory := func(context.Context) ([]deploymentplan.Object, error) {
		out := make([]deploymentplan.Object, 0, len(backend.objects))
		for _, v := range backend.objects {
			out = append(out, deploymentplan.Clone(v))
		}
		return out, nil
	}
	parent := environmentCreatePlan{Environment: cfg.Environment, Stack: p.Stack, ConfigurationSHA256: deploymentplan.Digest(cfg), Release: release, Bootstrap: p}
	before := deploymentplan.Digest(e.Journal)
	q, repair, retained, err := prepareFailedForwardCandidates(t.Context(), cfg, store, e, backend, &parent, runtime, service, issuerList, admitted, admission, tool, project, inventory, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	if q.CandidateSHA256 != repair.SHA256() || q.RetainCandidateSHA256 != retained.SHA256() || len(retained.Resources) != len(original.Resources) || deploymentplan.Digest(e.Journal) != before {
		t.Fatal("qualification changed owner or failed to bind candidates")
	}
	tampered := q
	altered := *q.ToolImage.OTARegistration
	altered.ManifestVersion += "-changed"
	tampered.ToolImage.OTARegistration = &altered
	if err := validateCreateFailedForwardProofBindings(tampered, admitted, admission, q.FailedStage, repair, retained, q.QualifiedAt); err == nil || !strings.Contains(err.Error(), "formal image registration descriptor") {
		t.Fatalf("changed OTA descriptor passed or lacked safe diagnostic: %v", err)
	}
	tampered.ToolImage.OTARegistration = nil
	if err := validateCreateFailedForwardProofBindings(tampered, admitted, admission, q.FailedStage, repair, retained, q.QualifiedAt); err != nil {
		t.Fatal("optional descriptor without selected OTA metadata was rejected", err)
	}
	var captured []deploymentplan.Object
	for _, resource := range retained.Resources {
		body := deploymentplan.Clone(resource.Baseline)
		if body == nil || resource.UID == "" {
			t.Fatal("missing retained baseline", resource.ID)
		}
		deploymentplan.Map(body["metadata"])["uid"] = resource.UID
		captured = append(captured, body)
	}
	_, retainStage := failedForwardStageNames(q)
	again, err := compileFailedStageRetainForward(original, journal, q.FailedStage, captured, retainStage)
	if err != nil || again.SHA256() != retained.SHA256() {
		t.Fatal("retained plan could not be reconstructed after interruption", err)
	}
	// Preserve an unadmitted old-format qualification exactly as it was saved
	// before native stdout whitespace was canonicalized.
	old := q
	old.Capabilities = append(json.RawMessage(nil), rawCaps...)
	old.CapabilitiesSHA256 = environmentPreflightBytesSHA256(rawCaps)
	oldReview := deploymentplan.Digest(old)
	if err = saveFailedForwardBundle(store, e, old, repair, retained); err != nil {
		t.Fatal(err)
	}
	oldPath := filepath.Join(store.Root, "deployment", "create", e.Journal.PlanSHA256, "failed-stage-forward", oldReview, "qualification.json")
	oldBytes, err := os.ReadFile(oldPath)
	if err != nil {
		t.Fatal(err)
	}
	runtime.ServiceSessionRecovery = &createServiceSessionRecovery{Ceremony: service, MQTTRoot: &issuerList[2], MQTTIntermediate: &issuerList[3], ManagedTransportRecovery: &createManagedTransportRecovery{Device: issuerList[4], AppRoot: issuerList[5], AppIntermediate: issuerList[6]}}
	backend.leases = 2 // Trust and original application stages used distinct owners.
	stopped := false
	preflightAt := time.Now().UTC()
	clockTime := preflightAt
	adapters := failedForwardDispatchAdapters{backend: backend, command: run, selectTool: func(context.Context) (failedForwardTool, error) {
		clockTime = preflightAt.Add(time.Minute)
		observed := tool
		observed.Publication.ObservedAt = clockTime
		return observed, nil
	}, project: project, inventory: func(ctx context.Context) ([]deploymentplan.Object, error) {
		clockTime = preflightAt.Add(2 * time.Minute)
		return inventory(ctx)
	}, accept: check, clock: func() time.Time { return clockTime },
		execute: func(ctx context.Context, name string, plan *deploymentplan.Plan) error {
			if strings.HasPrefix(name, "failed-stage-retain-") && !stopped {
				stopped = true
				return errors.New("isolated interruption")
			}
			return executeEnvironmentCreateStagePlan(ctx, store, e, name, plan, backend, check, check, check, func(context.Context) error { return nil }, io.Discard)
		},
	}
	qualified, err := dispatchFailedApplicationForward(t.Context(), cfg, store, e, environmentKubernetesBackend{}, &parent, runtime, admitted, admission, "preflight", "", io.Discard, preflightAt, time.Time{}, adapters)
	if err != nil || qualified == nil {
		t.Fatal("dispatcher preflight", err)
	}
	if !qualified.ToolPublication.ObservedAt.Equal(preflightAt.Add(time.Minute)) || !qualified.QualifiedAt.Equal(preflightAt.Add(2*time.Minute)) {
		t.Fatal("qualification was not dated after CI evidence and candidate inventory")
	}
	review := deploymentplan.Digest(*qualified)
	oldAfter, err := os.ReadFile(oldPath)
	if err != nil || review == oldReview || !bytes.Equal(oldBytes, oldAfter) {
		t.Fatal("old qualification was replaced", err)
	}
	reloaded, _, _, err := loadFailedForwardBundle(store, e, review)
	if err != nil || !bytes.Equal(reloaded.Capabilities, caps) || reloaded.CapabilitiesSHA256 != environmentPreflightBytesSHA256(caps) || !bytes.Equal(reloaded.ToolPublication.Manifest, manifest) || deploymentplan.Digest(reloaded.SourceJournal) != qualified.FailedStage.ParentJournalSHA256 {
		t.Fatal("saved qualification lost canonical native capability evidence", err)
	}
	if _, err = dispatchFailedApplicationForward(t.Context(), cfg, store, e, environmentKubernetesBackend{}, &parent, runtime, admitted, admission, "create", review, io.Discard, qualified.QualifiedAt.Add(time.Second), time.Time{}, adapters); err == nil {
		t.Fatal("interruption not reached")
	}
	if entry := createOpenBaoACKCompletionEntry(e.Journal, "kubernetes/managed-application-owners"); entry == nil || entry.State != "pending" {
		t.Fatal("original stage prematurely marked complete")
	}
	if _, err = dispatchFailedApplicationForward(t.Context(), cfg, store, e, environmentKubernetesBackend{}, &parent, runtime, admitted, admission, "preflight", "", io.Discard, preflightAt.Add(2*time.Hour), time.Time{}, adapters); err != nil {
		t.Fatal("retry preflight", err)
	}
	if _, err = dispatchFailedApplicationForward(t.Context(), cfg, store, e, environmentKubernetesBackend{}, &parent, runtime, admitted, admission, "create", review, io.Discard, preflightAt.Add(2*time.Hour), time.Time{}, adapters); err != nil {
		t.Fatal("dispatcher retry", err)
	}
	if entry := createOpenBaoACKCompletionEntry(e.Journal, "kubernetes/managed-application-owners"); entry == nil || entry.State != "completed" {
		t.Fatal("original alias not completed")
	}
	after, exists, err := original.ReadExecution(store.Root)
	if err != nil || !exists || deploymentplan.Digest(after) != q.FailedStage.Child.JournalSHA256 {
		t.Fatal("original failed journal changed", err)
	}
	if _, err = completedApplicationOwnerWrites(t.Context(), store, e, backend); err != nil {
		t.Fatal("accepted repair alias lost its original application write evidence", err)
	}
}

func TestFailedForwardQualificationTimeRespectsOriginalDeadline(t *testing.T) {
	start := time.Now().UTC()
	deadline := start.Add(time.Minute)
	ctx, cancel := context.WithDeadline(t.Context(), deadline)
	defer cancel()
	before := deadline.Add(-time.Nanosecond)
	if got, err := failedForwardQualificationTime(ctx, func() time.Time { return before }); err != nil || !got.Equal(before) {
		t.Fatal("qualification before original deadline rejected", err)
	}
	for _, instant := range []time.Time{deadline, deadline.Add(time.Second)} {
		if _, err := failedForwardQualificationTime(ctx, func() time.Time { return instant }); err == nil {
			t.Fatal("qualification at or after original deadline accepted")
		}
	}
	cancel()
	if _, err := failedForwardQualificationTime(ctx, func() time.Time { return before }); err == nil {
		t.Fatal("cancelled parent accepted")
	}
}
