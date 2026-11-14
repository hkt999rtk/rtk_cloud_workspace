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

// Kubernetes defaults the same field during server dry-run and the write.
type initializerDefaultBackend struct{ freshStageBackend }

func (b initializerDefaultBackend) Write(ctx context.Context, action string, body deploymentplan.Object, dry bool) (deploymentplan.Object, error) {
	out, err := b.freshStageBackend.Write(ctx, action, body, dry)
	if err != nil || out == nil || out["kind"] != "Deployment" {
		return out, err
	}
	deploymentplan.Map(deploymentplan.Map(deploymentplan.Map(out["spec"])["template"])["spec"])["dnsPolicy"] = "ClusterFirst"
	if !dry {
		b.objects[deploymentplan.ID(out)] = deploymentplan.Clone(out)
	}
	return out, nil
}

// Exercise the real q0/q1 native plans, parent journals and interrupted
// successor runner using only the private test store and in-memory Kubernetes.
func TestInitializerForwardRecoversAfterUpdateWithoutRewritingFailedJournals(t *testing.T) {
	runInitializerForwardInterruptedFixture(t, false, false)
}

func TestInitializerForwardDispatcherPreflightCreateRetry(t *testing.T) {
	runInitializerForwardInterruptedFixture(t, true, false)
}

func TestInitializerMountForwardDispatcherPreflightCreateRetry(t *testing.T) {
	runInitializerForwardInterruptedFixture(t, true, true)
}

type initializerSharedSessionFixture struct {
	store                                   secretStore
	execution                               *environmentCreateExecution
	backend                                 freshStageBackend
	nativeBackend                           *sessionNativeFaultBackend
	nativeDir                               string
	admitted                                issuerEnrollmentContinuationProof
	review                                  string
	configurationSHA, releaseSHA, sessionID string
	apiImage                                string
}

func runInitializerForwardInterruptedFixture(t *testing.T, viaDispatch, mountForward bool, shared ...initializerSharedSessionFixture) {
	t.Helper()
	var store secretStore
	var e *environmentCreateExecution
	var b freshStageBackend
	if len(shared) == 0 {
		store, e, _, b = freshStageFixture(t)
	} else if len(shared) == 1 {
		store, e, b = shared[0].store, shared[0].execution, shared[0].backend
	} else {
		t.Fatal("ambiguous initializer fixture")
	}
	var api deploymentplan.Backend = initializerDefaultBackend{b}
	var native *sessionNativeFaultBackend
	if len(shared) == 1 && shared[0].nativeBackend != nil {
		native = shared[0].nativeBackend
		if shared[0].nativeDir == "" {
			t.Fatal("isolated native API directory required")
		}
		api = native
	}
	setFailure := func(phase string) {
		if native != nil {
			native.failAfterPhase, native.failed = phase, false
			return
		}
		b.failure, b.failed = "", false
		if phase != "" {
			b.failure = "after-write-" + phase
		}
	}
	readObject := func(ctx context.Context, id string) deploymentplan.Object {
		object, err := api.Read(ctx, id)
		if err != nil || object == nil {
			t.Fatalf("isolated owner %s: %v", id, err)
		}
		return object
	}
	inventory := func(ctx context.Context) ([]deploymentplan.Object, error) {
		if native != nil {
			return native.Inventory(ctx, e.Journal.Stack)
		}
		items := make([]deploymentplan.Object, 0, len(b.objects))
		for _, object := range b.objects {
			items = append(items, deploymentplan.Clone(object))
		}
		return items, nil
	}
	// Intentional drift is confined to this test-only isolated API. It is never
	// written to an actual cluster or used to manufacture an accepted receipt.
	setDrift := func(id string, object deploymentplan.Object) {
		if native == nil {
			b.objects[id] = deploymentplan.Clone(object)
			return
		}
		parts := strings.Split(id, "/")
		if len(parts) != 3 {
			t.Fatal("invalid isolated resource identity")
		}
		path := filepath.Join(shared[0].nativeDir, strings.Join(parts, "_")+".json")
		if err := os.WriteFile(path, mustProviderRecoveryJSON(object), 0600); err != nil {
			t.Fatal(err)
		}
	}
	writeCount := func(id string) int {
		if native != nil {
			return native.writes[id]
		}
		return b.writes[id]
	}
	namespace := e.Journal.Stack + "-video-cloud"
	defs := []deploymentplan.Definition{}
	for i := 0; i < 58; i++ {
		defs = append(defs, deploymentplan.Definition{Phase: "configuration", Object: deploymentplan.Object{"apiVersion": "v1", "kind": "ConfigMap", "metadata": deploymentplan.Object{"name": fmt.Sprintf("fixture-%02d", i), "namespace": namespace}, "data": deploymentplan.Object{"value": "original"}}})
	}
	apiImage := "test-image"
	if len(shared) == 1 && shared[0].apiImage != "" {
		apiImage = shared[0].apiImage
		if !immutableEnvironmentImage.MatchString(apiImage) {
			t.Fatal("joined original API image must be immutable before admission")
		}
	}
	state := "/run/pki-state/root-trust-01010101-0202-4303-8404-050505050505.json"
	target := deploymentplan.Object{"apiVersion": "apps/v1", "kind": "Deployment", "metadata": deploymentplan.Object{"name": "video-cloud-api-pki", "namespace": namespace}, "spec": deploymentplan.Object{"replicas": float64(1), "selector": deploymentplan.Object{"matchLabels": deploymentplan.Object{"app": "api"}}, "template": deploymentplan.Object{"metadata": deploymentplan.Object{"labels": deploymentplan.Object{"app": "api"}}, "spec": deploymentplan.Object{"initContainers": []any{deploymentplan.Object{"name": "initialize-trust-state", "image": apiImage, "command": []any{"/bin/sh", "-ec", deviceNativeTrustPreparation(state)}, "volumeMounts": []any{deploymentplan.Object{"name": "pki-state", "mountPath": "/run/pki-state"}}}}, "containers": []any{deploymentplan.Object{"name": "api", "image": apiImage, "volumeMounts": []any{deploymentplan.Object{"name": "pki-state", "mountPath": "/run/pki-state"}}}}, "volumes": []any{deploymentplan.Object{"name": "pki-state", "emptyDir": deploymentplan.Object{"medium": "Memory"}}}}}}}
	if len(shared) == 1 {
		container := deploymentplan.Map(certIssuerObjectList(environmentPKIPod(target)["containers"])[0])
		container["env"] = environmentCreateEnv(map[string]string{"PKI_BOOTSTRAP_SESSION_ID": shared[0].sessionID})
	}
	if len(shared) == 1 && shared[0].apiImage != "" {
		containers, err := environmentPKIPodContainers(environmentPKIPod(target))
		if err != nil || len(containers) != 2 || containers["containers/api"] == nil || containers["initContainers/initialize-trust-state"] == nil {
			t.Fatal("joined original API container shape before admission", err)
		}
		for _, container := range containers {
			if container["image"] != apiImage {
				t.Fatal("joined original API/init image differs before admission")
			}
		}
	}
	defs = append(defs, deploymentplan.Definition{Phase: "workload", Object: target})
	if len(shared) == 1 && shared[0].apiImage != "" {
		auditJoinedNativeContainerImages(t, defs)
	}
	inputs := deploymentplan.Inputs{Environment: e.Journal.Environment, Stack: e.Journal.Stack, Operation: "scoped", ConfigurationSHA256: deploymentplan.Digest("config"), ReleaseSHA256: deploymentplan.Digest("release"), BindingsSHA256: environmentCreateStageBindingsSHA("managed-application-owners", defs)}
	if len(shared) == 1 {
		inputs.ConfigurationSHA256, inputs.ReleaseSHA256 = shared[0].configurationSHA, shared[0].releaseSHA
	}
	original, err := deploymentplan.Compile(inputs, defs, nil)
	if err != nil {
		t.Fatal(err)
	}
	check := func(ctx context.Context, _ *deploymentplan.Plan) error { return e.Check(ctx) }
	executeStage := func(ctx context.Context, name string, p *deploymentplan.Plan) error {
		return executeEnvironmentCreateStagePlan(ctx, store, e, name, p, api, check, check, check, func(context.Context) error { return nil }, io.Discard)
	}
	setFailure("workload")
	if err = executeStage(t.Context(), "managed-application-owners", original); err == nil {
		t.Fatal("expected original readiness interruption")
	}
	source, ok, err := original.ReadExecution(store.Root)
	if err != nil || !ok || len(source.Completed) != 59 {
		t.Fatal("original was not fully written", err)
	}
	// The fake API adds a Deployment default that is absent from original desired.
	liveTarget := readObject(t.Context(), deploymentplan.ID(target))
	if deploymentplan.Map(deploymentplan.Map(deploymentplan.Map(liveTarget["spec"])["template"])["spec"])["dnsPolicy"] != "ClusterFirst" {
		t.Fatal("server Deployment default missing")
	}
	setFailure("")
	stage, err := qualifyCreateFailedFullyWrittenStage(t.Context(), store, e, "managed-application-owners", api)
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
	runRaw := []byte(`{"id":70,"head_sha":"` + image.SourceRevision + `","head_branch":"main","event":"push","status":"completed","conclusion":"success","run_attempt":2}`)
	artifactInventory := []byte(fmt.Sprintf(`{"total_count":1,"artifacts":[{"id":71,"name":"image-manifest","size_in_bytes":%d,"expired":false}]}`, len(archive)))
	caps, _ := canonicalFailedForwardCapabilities([]byte(`{"schema":"rtk-cloud.deployment-capabilities/v1","service":"serviceidentity-bootstrap","features":["initial-public-crl-custody-v2"]}`))
	run := func(_ context.Context, command string, args ...string) ([]byte, error) {
		if command == "gh" && len(args) == 2 {
			switch {
			case strings.HasSuffix(args[1], "/runs/70"):
				return runRaw, nil
			case strings.HasSuffix(args[1], "/artifacts?per_page=100"):
				return artifactInventory, nil
			case strings.HasSuffix(args[1], "/artifacts/71/zip"):
				return archive, nil
			}
		}
		if command == "docker" {
			if len(args) == 4 && args[0] == "pull" {
				return nil, nil
			}
			if len(args) > 2 && args[len(args)-1] == "--deployment-capabilities" {
				return caps, nil
			}
		}
		return nil, errors.New("unexpected command")
	}
	capture := newEnvironmentReleaseProofCapture(t.Context())
	if _, err = verifyEnvironmentPublication(capture, "hkt999rtk/rtk_video_cloud", 70, image, false, run); err != nil {
		t.Fatal(err)
	}
	publication := snapshotVerifiedReleaseProofs(capture, environmentRelease{Images: map[string]environmentReleaseImage{"LKE_VIDEO_CLOUD_IMAGE": image}})[0]
	job := deploymentplan.Object{"apiVersion": "batch/v1", "kind": "Job", "metadata": deploymentplan.Object{"namespace": namespace, "name": "repair-crl"}, "spec": deploymentplan.Object{"template": deploymentplan.Object{"spec": deploymentplan.Object{"containers": []any{deploymentplan.Object{"name": "enroll", "image": image.Reference, "command": []any{"/app/serviceidentity-bootstrap", "initialize-public-crl", "/tmp/path", "issuer", "fingerprint"}}}, "restartPolicy": "Never"}}}}
	repair, err := deploymentplan.Compile(inputs, []deploymentplan.Definition{{Object: job, Phase: "bootstrap"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	live, err := inventory(t.Context())
	if err != nil {
		t.Fatal("isolated inventory", err)
	}
	oldRetain, err := compileFailedStageRetainForward(original, source, stage, live, "old-retain")
	if err != nil {
		t.Fatal(err)
	}
	admitted := issuerEnrollmentContinuationProof{Parent: e.Journal.PlanSHA256, ParentOwner: deploymentplan.Digest(e.owner)}
	admissionSHA := deploymentplan.Digest(admitted)
	if len(shared) == 1 {
		admitted, admissionSHA = shared[0].admitted, shared[0].review
		if deploymentplan.Digest(admitted) != admissionSHA {
			t.Fatal("shared partial admission changed")
		}
	}
	now := time.Now().UTC()
	q0 := createFailedForwardProof{Schema: failedForwardProofSchema, Environment: e.Journal.Environment, ParentOwnerSHA256: admitted.ParentOwner, OriginalAdmissionSHA256: admissionSHA, SourceJournal: snapshot, FailedStage: stage, CandidateSHA256: repair.SHA256(), RetainCandidateSHA256: oldRetain.SHA256(), ToolImage: image, ToolPublication: publication, Capabilities: caps, CapabilitiesSHA256: environmentPreflightBytesSHA256(caps), QualifiedAt: now, ValidUntil: now.Add(time.Hour)}
	if err = saveFailedForwardBundle(store, e, q0, repair, oldRetain); err != nil {
		t.Fatal(err)
	}
	_, oldStage := failedForwardStageNames(q0)
	q0Execute := func(ctx context.Context, name string, p *deploymentplan.Plan) error {
		if name == oldStage {
			setFailure("workload")
		}
		return executeStage(ctx, name, p)
	}
	if err = executeCreateFailedForward(t.Context(), store, e, api, q0, admitted, admissionSHA, func(context.Context) (*deploymentplan.Plan, *deploymentplan.Plan, error) {
		return repair, oldRetain, nil
	}, run, q0Execute, check, now); err == nil {
		t.Fatal("expected old retain readiness failure")
	}
	setFailure("")
	originalPath := filepath.Join(store.Root, "deployment", "rollback", original.SHA256(), "journal.json")
	oldPath := filepath.Join(store.Root, "deployment", "rollback", oldRetain.SHA256(), "journal.json")
	originalRaw, _ := os.ReadFile(originalPath)
	oldRaw, _ := os.ReadFile(oldPath)
	live, err = inventory(t.Context())
	if err != nil {
		t.Fatal("isolated inventory", err)
	}
	deadline := time.Now().UTC().Add(24 * time.Hour)
	// The qualification command has its own shorter timeout; it must not
	// replace the already-admitted total execution deadline.
	ctx, cancel := context.WithTimeout(t.Context(), 45*time.Minute)
	defer cancel()
	stopped := false
	stopRetain := false
	interrupted := func(ctx context.Context, name string, p *deploymentplan.Plan) error {
		if stopRetain && strings.HasPrefix(name, "private-state-retain-") {
			stopRetain = false
			setFailure("workload")
			err := executeStage(ctx, name, p)
			setFailure("")
			return err
		}
		if mountForward && strings.HasPrefix(name, "initializer-update-") && !stopped {
			stopped = true
			setFailure("workload")
			err := executeStage(ctx, name, p)
			setFailure("")
			return err
		}
		if !mountForward && strings.HasPrefix(name, "initializer-retain-") && !stopped {
			stopped = true
			return errors.New("isolated interruption after update")
		}
		return executeStage(ctx, name, p)
	}
	var perform func() error
	var dispatch func(string, string) (*createFailedForwardProof, error)
	loadAdmission := func(secretStore, *environmentCreateExecution, createInitializerForwardProof) (issuerEnrollmentContinuationProof, string, error) {
		return admitted, admissionSHA, nil
	}
	if len(shared) == 1 {
		loadAdmission = authenticatedInitializerForwardAdmission
	}
	var expectedUpdate *deploymentplan.Plan
	scopedRuntime := false
	if viaDispatch {
		issuer := &environmentCreateRegisteredIssuer{}
		runtime := environmentCreateBootstrapRuntime{ServiceSessionRecovery: &createServiceSessionRecovery{ManagedTransportRecovery: &createManagedTransportRecovery{}, MQTTRoot: issuer, MQTTIntermediate: issuer}}
		parent := &environmentCreatePlan{Stack: e.Journal.Stack}
		adapters := failedForwardDispatchAdapters{
			backend: api, command: run,
			selectTool: func(context.Context) (failedForwardTool, error) {
				return failedForwardTool{}, errors.New("q0 must already be admitted")
			},
			project: func(_ context.Context, defs []deploymentplan.Definition) ([]deploymentplan.Definition, error) {
				return defs, nil
			},
			inventory: inventory, execute: interrupted, accept: check, clock: time.Now,
			mountAlias: func(ctx context.Context, store secretStore, e *environmentCreateExecution, b deploymentplan.Backend, q2 createInitializerMountForwardProof, q1 createInitializerForwardProof, q0 createFailedForwardProof, admitted issuerEnrollmentContinuationProof, admission string, accept func(context.Context, *deploymentplan.Plan) error, now time.Time) error {
				return completeInitializerMountForwardAliasWithAdmission(ctx, store, e, b, q2, q1, q0, admitted, admission, accept, now, loadAdmission)
			},
		}
		dispatch = func(action, selected string) (*createFailedForwardProof, error) {
			selectedRuntime := runtime
			if scopedRuntime {
				selectedRuntime.ServiceSessionRecovery = nil
			}
			return dispatchFailedApplicationForward(ctx, deploymentConfig{Environment: store.Environment}, store, e, environmentKubernetesBackend{}, parent, selectedRuntime, admitted, admissionSHA, action, selected, io.Discard, time.Now().UTC(), deadline, adapters)
		}
		qualified, err := dispatch("preflight", "")
		if err != nil || qualified == nil || qualified.initializer == nil {
			t.Fatal("dispatcher preflight", err)
		}
		q1SHA := deploymentplan.Digest(*qualified.initializer)
		_, expectedUpdate, _, err = loadInitializerForwardBundle(store, e, q1SHA)
		if err != nil {
			t.Fatal("load reviewed update", err)
		}
		perform = func() error {
			result, runErr := dispatch("create", q1SHA)
			if runErr == nil && (result == nil || result.initializer == nil || deploymentplan.Digest(*result.initializer) != q1SHA) {
				t.Fatal("dispatcher changed q1 qualification")
			}
			return runErr
		}
	} else {
		q1, update, retain, err := prepareInitializerForward(ctx, store, e, api, q0, admitted, admissionSHA, deadline, live, time.Now)
		if err != nil {
			t.Fatal("prepare q1", err)
		}
		expectedUpdate = update
		perform = func() error {
			return executeInitializerForward(ctx, store, e, api, q1, q0, admitted, admissionSHA, update, retain, deadline, interrupted, check, time.Now)
		}
	}
	if err = perform(); err == nil || !stopped {
		t.Fatal("did not interrupt after update", err)
	}
	early := func() (*deploymentplan.Plan, error) {
		return earlyInitializerForwardSuccessorWithAdmission(ctx, store, e, api, original, source, loadAdmission)
	}
	observed, earlyErr := early()
	if earlyErr != nil || observed == nil || expectedUpdate == nil || observed.SHA256() != expectedUpdate.SHA256() {
		t.Fatal("early preflight did not recover the exact recorded update", earlyErr)
	}
	id := deploymentplan.ID(target)
	preserved := deploymentplan.Clone(readObject(ctx, id))
	for _, drift := range []string{"uid", "body"} {
		changed := deploymentplan.Clone(preserved)
		if drift == "uid" {
			deploymentplan.Map(changed["metadata"])["uid"] = "replacement"
		} else {
			deploymentplan.Map(changed["spec"])["replicas"] = float64(99)
		}
		setDrift(id, changed)
		if _, earlyErr = early(); earlyErr == nil {
			t.Fatal("early preflight accepted target drift", drift)
		}
		setDrift(id, preserved)
	}
	if mountForward {
		// The isolated q2 repair does not require the expired bootstrap session.
		scopedRuntime = true
		previousStage := ""
		for _, entry := range e.Journal.Entries {
			if strings.HasPrefix(entry.Key, "kubernetes/initializer-update-") {
				previousStage = strings.TrimPrefix(entry.Key, "kubernetes/")
			}
		}
		previous, err := readEnvironmentCreateStageInput(store, e, previousStage)
		if err != nil || previous == nil {
			t.Fatal("missing previous update", err)
		}
		previousPath := filepath.Join(store.Root, "deployment", "rollback", previous.SHA256(), "journal.json")
		previousRaw, err := os.ReadFile(previousPath)
		if err != nil {
			t.Fatal(err)
		}
		qualified, err := dispatch("preflight", "")
		if err != nil || qualified == nil || qualified.initializer == nil || qualified.initializer.mount == nil {
			t.Fatal("q2 preflight", err)
		}
		q2sha := deploymentplan.Digest(*qualified.initializer.mount)
		_, mountUpdate, _, err := loadInitializerMountForwardBundle(store, e, q2sha)
		if err != nil {
			t.Fatal(err)
		}
		// Interrupt after the single update has been recorded, before retain.
		// The execute callback is shared by q1 and q2 and can be steered here.
		stopped = false
		// Stop the q2 retain through the fake backend's readiness failure.
		setFailure("workload")
		_, err = dispatch("create", q2sha)
		setFailure("")
		if err == nil {
			t.Fatal("q2 did not interrupt")
		}
		if _, found, err := serviceSessionBoundaryWithAdmission(ctx, store, e, api, loadAdmission); err != nil || found {
			t.Fatal("session route claimed q2 before retained owners started", err)
		}
		observed, earlyErr = early()
		if earlyErr != nil || observed == nil || observed.SHA256() != mountUpdate.SHA256() {
			t.Fatal("early q2 continuation", earlyErr)
		}
		pendingWrites, err := authenticatedRecordedPendingWritesWithAdmission(ctx, store, e, api, loadAdmission)
		if err != nil || !pendingWrites["kubernetes/managed-application-owners"].Written || !pendingWrites["kubernetes/"+previousStage].Written {
			t.Fatal("q2 pending recorded writes were not authenticated", err)
		}
		stopRetain = true
		if _, err = dispatch("create", q2sha); err == nil || stopRetain {
			t.Fatal("q2 retained owner did not interrupt", err)
		}
		boundary, found, err := serviceSessionBoundaryWithAdmission(ctx, store, e, api, loadAdmission)
		if err != nil || !found || deploymentplan.Digest(boundary) != q2sha {
			t.Fatal("session route did not select the authenticated interrupted q2", err)
		}
		if len(shared) == 1 {
			return
		}
		if _, err = dispatch("create", q2sha); err != nil {
			t.Fatal("q2 retry", err)
		}
		aliasedWrites, err := authenticatedRecordedPendingWritesWithAdmission(ctx, store, e, api, loadAdmission)
		if err != nil || !aliasedWrites["kubernetes/managed-application-owners"].Written {
			t.Fatal("completed q2 alias did not preserve original written owner", err)
		}
		boundary, found, err = serviceSessionBoundaryWithAdmission(ctx, store, e, api, loadAdmission)
		if err != nil || !found || deploymentplan.Digest(boundary) != q2sha {
			t.Fatal("session route did not select the authenticated q2", err)
		}
		got, err := os.ReadFile(previousPath)
		if err != nil || string(got) != string(previousRaw) {
			t.Fatal("q1 journal changed", err)
		}
	} else if err = perform(); err != nil {
		t.Fatal("q1 retry", err)
	}
	for path, old := range map[string][]byte{originalPath: originalRaw, oldPath: oldRaw} {
		got, e := os.ReadFile(path)
		if e != nil || string(got) != string(old) {
			t.Fatal("original journal changed", path, e)
		}
	}
	expectedWrites := 2
	if mountForward {
		expectedWrites = 3
	}
	if writeCount(deploymentplan.ID(target)) != expectedWrites {
		t.Fatal("initializer update replayed", writeCount(deploymentplan.ID(target)))
	}
	if mountForward {
		if len(source.Owners) != 59 {
			t.Fatal("expected 59 original owners")
		}
		for id, uid := range source.Owners {
			live := readObject(ctx, id)
			if live == nil || deploymentplan.Map(live["metadata"])["uid"] != uid {
				t.Fatalf("owner changed: %s", id)
			}
		}
	}
	if entry := createOpenBaoACKCompletionEntry(e.Journal, "kubernetes/managed-application-owners"); entry == nil || entry.State != "completed" {
		t.Fatal("original stage not completed through successor")
	}
	if mountForward {
		t.Run("later-recorded-pending-workload", func(t *testing.T) {
			var selected deploymentplan.Resource
			for _, resource := range original.Resources {
				if resource.ID != deploymentplan.ID(target) && (resource.Desired["kind"] == "ConfigMap" || resource.Desired["kind"] == "Secret") {
					selected = resource
					break
				}
			}
			if selected.ID == "" {
				t.Fatal("no ordinary shared owner in the real q2 chain")
			}
			actual := readObject(ctx, selected.ID)
			desired := deploymentplan.Clone(actual)
			deploymentplan.Map(desired["data"])["value"] = "normal-workload"
			deploymentplan.Map(desired["data"])["normal-workload"] = "cmVjb3JkZWQ="
			later := deploymentplan.Definition{Phase: "migration", Object: deploymentplan.Object{"apiVersion": "batch/v1", "kind": "Job", "metadata": deploymentplan.Object{"name": "unstarted-after-known-prefix", "namespace": e.Journal.Stack + "-video-cloud"}, "spec": deploymentplan.Object{"template": deploymentplan.Object{"spec": deploymentplan.Object{"restartPolicy": "Never", "containers": []any{deploymentplan.Object{"name": "isolated", "image": "alpine:isolated", "command": []any{"true"}}}}}}}}
			p, err := deploymentplan.Compile(original.Inputs, []deploymentplan.Definition{{Object: desired, Phase: "configuration"}, later}, []deploymentplan.Object{actual})
			if err != nil {
				t.Fatal(err)
			}
			check := func(ctx context.Context, _ *deploymentplan.Plan) error { return e.Check(ctx) }
			fence := func(ctx context.Context, p *deploymentplan.Plan) error {
				if deploymentplan.Map(readObject(ctx, selected.ID)["data"])["normal-workload"] != nil {
					return errors.New("known prefix before unwritten migration")
				}
				return check(ctx, p)
			}
			if err := executeEnvironmentCreateStagePlan(ctx, store, e, "workload", p, api, check, fence, check, func(context.Context) error { return nil }, io.Discard); err == nil {
				t.Fatal("did not interrupt after the actual ordinary write")
			}
			before := createBootstrapStateSnapshot(t, store.Root)
			pending, err := authenticatedRecordedPendingWritesWithAdmission(ctx, store, e, api, loadAdmission)
			if err != nil || !pending["kubernetes/workload"].Written {
				t.Fatal("historical q2 body was required after its recorded ordinary successor", err)
			}
			folded, err := completedRecordedOwnerSuccessorsAfterHistory(ctx, store, e, api, "kubernetes/managed-application-owners", []activeTransportRenewalResource{sessionRecordedOwnerBase(selected.ID, source.Owners[selected.ID], selected.Desired)}, pending)
			if err != nil || len(folded) != 1 || !deploymentplan.Matches(folded[0].Desired, readObject(ctx, selected.ID)) {
				t.Fatal("ordinary written prefix did not fold to the same-UID native state", err)
			}
			if _, err := completedApplicationOwnerWritesWithAdmission(ctx, store, e, api, loadAdmission); err != nil {
				t.Fatal("current application reader required superseded historical bodies", err)
			}
			if _, err := early(); err != nil {
				t.Fatal("early native reader ignored completed original history", err)
			}
			if _, err := dispatch("preflight", ""); err != nil {
				t.Fatal("completed initializer dispatcher reopened historical native bodies", err)
			}
			assertRecoveryContractReadOnly(t, store, before)
		})
	}
}
