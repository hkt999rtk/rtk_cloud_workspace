package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

// Exercise the real resumable stage runner: completed schema Jobs remain owned,
// only the two selected packages change, and a readiness retry writes no Job.
func TestCreateWorkloadImageForwardKeepsMigrationsAndOriginalFailedJournal(t *testing.T) {
	for _, stop := range []string{"after-write-workload", "acceptance", "hosts-and-session"} {
		t.Run(stop, func(t *testing.T) {
			store, e, _, baseBackend := freshStageFixture(t)
			b := workloadNativeDefaultsBackend{baseBackend}
			stack := e.Journal.Stack
			old := "ghcr.io/hkt999rtk/rtk_video_cloud/video-cloud-api@sha256:" + strings.Repeat("a", 64)
			selected := map[string]environmentReleaseImage{}
			for _, role := range []string{"LKE_PKI_CONTROLLER_IMAGE", "LKE_PKI_MANAGEMENT_IMAGE"} {
				selected[role] = environmentReleaseImage{Reference: "ghcr.io/hkt999rtk/rtk_video_cloud/video-cloud-api@sha256:" + strings.Repeat("b", 64)}
			}
			obj := func(kind, ns, name string) deploymentplan.Object {
				return deploymentplan.Object{"apiVersion": "apps/v1", "kind": kind, "metadata": deploymentplan.Object{"namespace": stack + ns, "name": name}}
			}
			large := obj("ConfigMap", "-video-cloud", "large-public-configuration")
			large["apiVersion"] = "v1"
			large["data"] = deploymentplan.Object{"configuration": strings.Repeat("x", 300<<10)}
			ns := deploymentplan.Object{"apiVersion": "v1", "kind": "Namespace", "metadata": deploymentplan.Object{"name": stack + "-account-manager"}}
			svc := obj("Service", "-account-manager", "account-manager")
			svc["apiVersion"] = "v1"
			svc["spec"] = deploymentplan.Object{"ports": []any{deploymentplan.Object{"name": "https", "port": float64(443), "targetPort": float64(8443)}}}
			defs := []deploymentplan.Definition{{Object: ns, Phase: "namespace"}, {Object: svc, Phase: "configuration"}, {Object: large, Phase: "configuration"}}
			for _, name := range []string{"billing-migrate", "zammad-init"} {
				j := obj("Job", "-billing", name)
				j["apiVersion"] = "batch/v1"
				j["spec"] = deploymentplan.Object{"template": deploymentplan.Object{"spec": deploymentplan.Object{"restartPolicy": "Never", "containers": []any{deploymentplan.Object{"name": "migrate", "image": old}}}}}
				defs = append(defs, deploymentplan.Definition{Object: j, Phase: "migration"})
			}
			for _, item := range []struct{ ns, name, container string }{{"-account-manager", "account-manager", "pkimanagement"}, {"-video-cloud", "pki-controller", "pki-controller"}} {
				d := obj("Deployment", item.ns, item.name)
				d["spec"] = deploymentplan.Object{"replicas": float64(1), "selector": deploymentplan.Object{"matchLabels": deploymentplan.Object{"app": item.name}}, "template": deploymentplan.Object{"metadata": deploymentplan.Object{"labels": deploymentplan.Object{"app": item.name}}, "spec": deploymentplan.Object{"containers": []any{deploymentplan.Object{"name": item.container, "image": old, "env": environmentCreateEnv(map[string]string{"PKI_BOOTSTRAP_SESSION_ID": "11111111-1111-4111-8111-111111111111"})}}, "volumes": []any{deploymentplan.Object{"name": "original-identity", "emptyDir": deploymentplan.Object{}}}}}}
				defs = append(defs, deploymentplan.Definition{Object: d, Phase: "workload"})
			}
			route := obj("Ingress", "-video-cloud", "public")
			route["apiVersion"] = "networking.k8s.io/v1"
			route["spec"] = deploymentplan.Object{"rules": []any{deploymentplan.Object{"host": "isolated.test"}}}
			defs = append(defs, deploymentplan.Definition{Object: route, Phase: "routing"})
			p, err := deploymentplan.Compile(deploymentplan.Inputs{Environment: store.Environment, Stack: stack, Operation: "scoped", ConfigurationSHA256: deploymentplan.Digest("settings"), ReleaseSHA256: deploymentplan.Digest("release"), BindingsSHA256: environmentCreateStageBindingsSHA("workload", defs)}, defs, nil)
			if err != nil {
				t.Fatal(err)
			}
			check := func(ctx context.Context, _ *deploymentplan.Plan) error { return e.Check(ctx) }
			run := func(stage string, p *deploymentplan.Plan, accept func(context.Context, *deploymentplan.Plan) error) error {
				return executeEnvironmentCreateStagePlan(t.Context(), store, e, stage, p, b, check, check, accept, func(context.Context) error { return nil }, io.Discard)
			}
			b.captureJobLifecycle = true
			b.failure = "after-write-workload"
			if run("workload", p, check) == nil {
				t.Fatal("original readiness interruption missing")
			}
			j, exists, err := p.ReadExecution(store.Root)
			if err != nil || !exists || len(j.Completed) != len(p.Resources)-1 {
				t.Fatal("known original prefix missing", err)
			}
			path := filepath.Join(store.Root, "deployment/rollback", p.SHA256(), "journal.json")
			before, _ := os.ReadFile(path)
			next, err := buildCreateWorkloadImageForward(p, j, selected, b.objects, "workload-image-forward")
			if err != nil {
				t.Fatal(err)
			}
			if stop == "hosts-and-session" {
				q := &createWorkloadImageForward{Schema: createWorkloadImageForwardSchema, Parent: e.Journal.PlanSHA256, OriginalPlan: p.SHA256(), OriginalJournal: deploymentplan.Digest(j), Images: selected, Plan: next}
				relative, _ := filepath.Rel(store.Root, workloadImageForwardPath(e))
				if err := writeCreateWorkloadImageArtifact(store, relative, q); err != nil {
					t.Fatal(err)
				}
				b.failure, b.failed = "", false
				hosts, err := createRuntimeHostPlan(q)
				if err != nil || len(hosts.Resources) != 2 {
					t.Fatal("early runtime host scope", err)
				}
				if err := run(createRuntimeHostStage, hosts, check); err != nil {
					t.Fatal(err)
				}
				var sessionDefs []deploymentplan.Definition
				var live []deploymentplan.Object
				for _, res := range hosts.Resources {
					body := deploymentplan.Clone(res.Desired)
					deploymentplan.Map(body["metadata"])["uid"] = deploymentplan.Map(b.objects[res.ID]["metadata"])["uid"]
					environmentPKIPod(body)["initContainers"] = []any{}
					environmentPKIPod(body)["securityContext"] = deploymentplan.Object{"fsGroup": float64(999)}
					if _, err := serviceSessionSIDSlots(body, "11111111-1111-4111-8111-111111111111", "22222222-2222-4222-8222-222222222222"); err != nil {
						t.Fatal(err)
					}
					sessionDefs = append(sessionDefs, deploymentplan.Definition{Object: body, Phase: "workload"})
					live = append(live, b.objects[res.ID])
				}
				in := next.Inputs
				in.BindingsSHA256 = environmentCreateStageBindingsSHA("native-session-after-runtime", sessionDefs)
				session, err := deploymentplan.Compile(in, sessionDefs, live)
				if err != nil {
					t.Fatal(err)
				}
				if err := run("native-session-after-runtime", session, check); err != nil {
					t.Fatal(err)
				}
				effective, err := readCreateWorkloadImageForward(store, e)
				if err != nil || effective == nil {
					t.Fatal(err)
				}
				if err := effective.Plan.AdmitResumable(t.Context(), b, store.Root); err != nil {
					t.Fatal("final workload would undo the accepted host/session repair", err)
				}
				// Exact native defaults remain fenced, including allocated Service IPs.
				for _, id := range []string{deploymentplan.ID(ns), deploymentplan.ID(svc)} {
					original := deploymentplan.Clone(b.objects[id])
					changed := deploymentplan.Clone(original)
					if changed["kind"] == "Namespace" {
						deploymentplan.Map(changed["metadata"])["uid"] = "foreign"
					} else {
						deploymentplan.Map(changed["spec"])["clusterIP"] = "10.0.0.99"
					}
					b.objects[id] = changed
					if err := effective.Plan.AdmitResumable(t.Context(), b, store.Root); err == nil {
						t.Fatal("native drift ignored", id)
					}
					b.objects[id] = original
				}
				next = effective.Plan
			}
			b.failure = stop
			if stop == "hosts-and-session" {
				b.failure = "acceptance"
			}
			b.failed = false
			accept := func(ctx context.Context, p *deploymentplan.Plan) error {
				if stop == "acceptance" || stop == "hosts-and-session" {
					return b.trip("acceptance")
				}
				return check(ctx, p)
			}
			if run("workload-image-forward", next, accept) == nil {
				t.Fatal("forward interruption missing")
			}
			if err := run("workload-image-forward", next, check); err != nil {
				t.Fatal("forward resume", err)
			}
			q := &createWorkloadImageForward{Schema: createWorkloadImageForwardSchema, Parent: e.Journal.PlanSHA256, OriginalPlan: p.SHA256(), OriginalJournal: deploymentplan.Digest(j), Images: selected, Plan: next}
			relative, _ := filepath.Rel(store.Root, workloadImageForwardPath(e))
			artifactQ := q
			if stop == "hosts-and-session" {
				artifactQ, err = readOriginalCreateWorkloadImageForward(store, e)
				if err != nil {
					t.Fatal(err)
				}
			}
			if err := writeCreateWorkloadImageArtifact(store, relative, artifactQ); err != nil {
				t.Fatal(err)
			}
			if err := writeCreateWorkloadImageArtifact(store, relative, artifactQ); err != nil {
				t.Fatal("idempotent large private artifact retry", err)
			}
			changed := *q
			changed.Schema = "foreign"
			if err := writeCreateWorkloadImageArtifact(store, relative, &changed); err == nil {
				t.Fatal("large immutable artifact overwritten")
			}
			if err := completeCreateWorkloadImageForward(t.Context(), store, e, q); err != nil {
				t.Fatal("original workload completion", err)
			}
			effective, accepted, err := acceptedCreateStage(store, e, "kubernetes/workload")
			if err != nil || effective.SHA256() != next.SHA256() || !accepted.Accepted {
				t.Fatal("effective workload not accepted", err)
			}
			literal, exists, err := readEnvironmentCreateStageProgress(store, e, "workload", p)
			if err != nil || !exists || literal.Accepted || literal.Failed != j.Failed || len(literal.Completed) != len(j.Completed) {
				t.Fatal("source failure replaced by success", err)
			}
			if _, err := verifyAcceptedStageAfterRecordedHistory(t.Context(), store, e, b, "kubernetes/workload"); err != nil {
				t.Fatal("completed workload history", err)
			}

			e.versionState = &createOperatorVersionState{cache: &createOperatorVersionCache{RuntimeImages: selected}}
			review := strings.Repeat("d", 64)
			fullGO, err := writeCurrentCoreForwardEnvelope(store, e.Journal.PlanSHA256, review, "", "", deploymentplan.Digest(q))
			if err != nil {
				t.Fatal("exact successor GO envelope", err)
			}
			envelope, err := loadCurrentCoreForwardEnvelope(t.Context(), store, e, fullGO, review, nil, environmentCreateBootstrapRuntime{})
			if err != nil || envelope == nil || envelope.WorkloadImageForward != deploymentplan.Digest(q) {
				t.Fatal("exact successor review lost", err)
			}
			e.versionState.cache.RuntimeImages = map[string]environmentReleaseImage{}
			if _, err := loadCurrentCoreForwardEnvelope(t.Context(), store, e, fullGO, review, nil, environmentCreateBootstrapRuntime{}); err == nil {
				t.Fatal("GO accepted changed selected runtime images")
			}
			after, _ := os.ReadFile(path)
			if string(before) != string(after) {
				t.Fatal("original failure rewritten")
			}
			for _, r := range p.Resources {
				want := 1
				if r.Desired["kind"] == "Deployment" {
					want = 2
					if stop == "hosts-and-session" {
						want = 3
					}
				}
				if b.writes[r.ID] != want {
					t.Fatal("unexpected native writes", r.ID, b.writes[r.ID])
				}
				if r.Desired["kind"] == "Job" && deploymentplan.Map(b.objects[r.ID]["status"])["completionTime"] == nil {
					t.Fatal("migration completion lost")
				}
			}
			for _, mode := range []string{"unknown-write", "uid-drift", "other-setting", "in-flight", "extra-role"} {
				t.Run(mode, func(t *testing.T) {
					observed := map[string]deploymentplan.Object{}
					for id, o := range b.objects {
						observed[id] = deploymentplan.Clone(o)
					}
					// Recreate only the authenticated original prefix for compiler negatives.
					for _, r := range p.Resources[:len(j.Completed)] {
						o := deploymentplan.Clone(r.Desired)
						deploymentplan.Map(o["metadata"])["uid"] = j.Owners[r.ID]
						observed[r.ID] = o
					}
					delete(observed, deploymentplan.ID(route))
					source := j
					images := map[string]environmentReleaseImage{}
					for k, v := range selected {
						images[k] = v
					}
					switch mode {
					case "unknown-write":
						observed[deploymentplan.ID(route)] = route
					case "uid-drift":
						deploymentplan.Map(observed[p.Resources[0].ID]["metadata"])["uid"] = "foreign"
					case "other-setting":
						deploymentplan.Map(observed["Deployment/"+stack+"-account-manager/account-manager"]["spec"])["replicas"] = float64(2)
					case "in-flight":
						source.InFlight = p.Resources[len(j.Completed)].ID
					case "extra-role":
						images["LKE_VIDEO_CLOUD_IMAGE"] = selected["LKE_PKI_CONTROLLER_IMAGE"]
					}
					if _, err := buildCreateWorkloadImageForward(p, source, images, observed, "workload-image-forward"); err == nil {
						t.Fatal("unsafe forward accepted")
					}
				})
			}
		})
	}
}

func TestCreateWorkloadImageForwardAfterFreshFinalizationPreservesAcceptedHistory(t *testing.T) {
	f, l, installed, _, apply := newFreshProductPreparationFixture(t, func(f *productProtocolFixture) freshProductWorkloadTestOptions {
		controller := deploymentplan.Clone(f.b.objects["Deployment/"+f.p.Stack+"-video-cloud/pki-controller"])
		_, _, catalog := completeEnvironmentRenderFixture("dev")
		manager := deploymentplan.Object{"apiVersion": "apps/v1", "kind": "Deployment", "metadata": deploymentplan.Object{"namespace": f.p.Stack + "-account-manager", "name": "account-manager"}, "spec": deploymentplan.Object{"replicas": float64(1), "selector": deploymentplan.Object{"matchLabels": deploymentplan.Object{"app": "account-manager"}}, "template": deploymentplan.Object{"metadata": deploymentplan.Object{"labels": deploymentplan.Object{"app": "account-manager"}}, "spec": deploymentplan.Object{"containers": []any{deploymentplan.Object{"name": "app", "image": catalog.Images["LKE_ACCOUNT_MANAGER_IMAGE"].Reference}, deploymentplan.Object{"name": "pkimanagement", "image": f.p.Image, "env": environmentCreateEnv(map[string]string{"PUBLIC_CRL_EXPORT": "/var/lib/original/public.pem"})}}, "volumes": []any{deploymentplan.Object{"name": "identity", "persistentVolumeClaim": deploymentplan.Object{"claimName": "original-management-identity"}}}}}}}
		route := deploymentplan.Object{"apiVersion": "networking.k8s.io/v1", "kind": "Ingress", "metadata": deploymentplan.Object{"namespace": f.p.Stack + "-video-cloud", "name": "public-late-image-fixture"}, "spec": deploymentplan.Object{"rules": []any{deploymentplan.Object{"host": "isolated.example"}}}}
		return freshProductWorkloadTestOptions{Definitions: []deploymentplan.Definition{{Object: controller, Phase: "workload", Retain: true}, {Object: manager, Phase: "workload"}, {Object: route, Phase: "routing"}}, Execute: func(ctx context.Context, apply environmentCreateBootstrapApply, definitions []deploymentplan.Definition) error {
			f.b.failure, f.b.failed = "after-write-workload", false
			if err := apply(ctx, "workload", definitions); err == nil {
				t.Fatal("original source failure was not reached")
			}
			original, err := readEnvironmentCreateStageInput(f.store, f.e, "workload")
			if err != nil {
				return err
			}
			journal, exists, err := original.ReadExecution(f.store.Root)
			if err != nil || !exists {
				t.Fatal("genuine original failed workload", err)
			}
			images := map[string]environmentReleaseImage{"LKE_PKI_CONTROLLER_IMAGE": {Reference: "ghcr.io/hkt999rtk/rtk_video_cloud/video-cloud-api@sha256:" + strings.Repeat("b", 64)}, "LKE_PKI_MANAGEMENT_IMAGE": {Reference: "ghcr.io/hkt999rtk/rtk_video_cloud/video-cloud-api@sha256:" + strings.Repeat("b", 64)}}
			forward, err := buildCreateWorkloadImageForward(original, journal, images, f.b.objects, createWorkloadImageForwardStage)
			if err != nil {
				return err
			}
			q := createWorkloadImageForward{Schema: createWorkloadImageForwardSchema, Parent: f.e.Journal.PlanSHA256, OriginalPlan: original.SHA256(), OriginalJournal: deploymentplan.Digest(journal), Images: images, Plan: forward}
			relative, _ := filepath.Rel(f.store.Root, workloadImageForwardPath(f.e))
			if err := writeCreateWorkloadImageArtifact(f.store, relative, q); err != nil {
				return err
			}
			f.b.failure, f.b.failed = "", false
			backend := &productOuterTargetBackend{productProtocolBackend: f.b}
			check := func(ctx context.Context, _ *deploymentplan.Plan) error { return f.e.Check(ctx) }
			if err := executeEnvironmentCreateStagePlan(ctx, f.store, f.e, createWorkloadImageForwardStage, forward, backend, check, check, func(ctx context.Context, p *deploymentplan.Plan) error { return p.Verify(ctx, backend) }, func(context.Context) error { return nil }, io.Discard); err != nil {
				return err
			}
			return completeCreateWorkloadImageForward(ctx, f.store, f.e, &q)
		}}
	})
	ctx := withEnvironmentCreateBootstrapRuntime(t.Context(), f.runtime)
	if err := completeFreshProductPreparations(ctx, f.cfg, f.store, f.e, l, installed, f.runtime.Backend, apply); err != nil {
		t.Fatal("genuine finalized fresh steady and preparation retirement", err)
	}
	p, j, err := acceptedCreateStage(f.store, f.e, "kubernetes/workload")
	if err != nil || p == nil || !j.Accepted || !f.e.Journal.CompletedAt.IsZero() {
		t.Fatal("original accepted workload unavailable", err)
	}
	before := map[string][]byte{}
	for _, stage := range []string{"workload", createWorkloadImageForwardStage, "fresh-product-steady", "fresh-product-producer-retire"} {
		input, err := readEnvironmentCreateStageInput(f.store, f.e, stage)
		if err != nil || input == nil {
			t.Fatal(err)
		}
		path := filepath.Join(f.store.Root, "deployment/rollback", input.SHA256(), "journal.json")
		before[path], err = os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
	}
	prefix := deploymentplan.Digest(f.e.Journal.Entries)
	images := map[string]environmentReleaseImage{"LKE_PKI_CONTROLLER_IMAGE": {Reference: "ghcr.io/hkt999rtk/rtk_video_cloud/video-cloud-api@sha256:" + strings.Repeat("b", 64)}, "LKE_PKI_MANAGEMENT_IMAGE": {Reference: "ghcr.io/hkt999rtk/rtk_video_cloud/video-cloud-api@sha256:" + strings.Repeat("f", 64), SourceRevision: strings.Repeat("e", 40), Publication: &createVersionPublication{Repository: "hkt999rtk/rtk_video_cloud", RunID: 78, Attempt: 2, ManifestSHA: strings.Repeat("d", 64)}}}
	f.e.versionState = &createOperatorVersionState{cache: &createOperatorVersionCache{RuntimeImages: images}}
	backend := &productOuterTargetBackend{productProtocolBackend: f.b}
	q, stage, err := selectedCreateWorkloadImageForward(ctx, f.store, f.e, backend)
	if err != nil || q == nil || stage == createWorkloadImageForwardStage || len(q.Plan.Resources) != 1 || q.Plan.Resources[0].ID != "Deployment/"+f.p.Stack+"-account-manager/account-manager" || q.Plan.Resources[0].Action != "update" || q.Plan.Resources[0].UID != j.Owners[q.Plan.Resources[0].ID] {
		t.Fatal("finalized fresh workload has no exact Management-only ordinary image successor", err)
	}

	// The early bootstrap router only falls through. The ordinary full GO
	// envelope must bind this exact new image child, while the old GO is stale.
	if handled, err := tryUnifiedRuntimeHostCompatibility(ctx, f.cfg, unifiedDeploymentOptions{}, &unifiedDeploymentReport{}, f.store, f.e, f.runtime.Backend, &environmentCreatePlan{}, io.Discard, io.Discard); handled || err != nil {
		t.Fatal("completed direct fresh cleanup was routed back to bootstrap compatibility", err)
	}
	originalReview := strings.Repeat("a", 64)
	historical, err := readCreateWorkloadImageForward(f.store, f.e)
	if err != nil || historical == nil {
		t.Fatal("immutable original image authority", err)
	}

	// Exercise the existing CLI commit helper with raw neutral aliases and a
	// canonical SSH public key. The role projection must not corrupt its lock
	// comparison or its other settings, and preview must publish nothing.
	oldSelection := environmentImageSelection{Environment: f.cfg.Environment, Images: historical.Images}
	for key, value := range map[string]string{
		environmentImagesKey:   string(mustProviderRecoveryJSON(oldSelection)),
		"PKI_CONTROLLER_IMAGE": "ghcr.io/hkt999rtk/rtk_video_cloud/video-cloud-api@sha256:" + strings.Repeat("1", 64),
		"PKI_MANAGEMENT_IMAGE": "ghcr.io/hkt999rtk/rtk_cloud_dev/video-cloud-api@sha256:" + strings.Repeat("2", 64),
		"ACCOUNT_MANAGER_SERVICE_REGISTRATION_CRL_REFRESH_PROFILE": "managed-public-pem-v1",
	} {
		if err := f.store.write("operator/env/"+key, []byte(value), true); err != nil {
			t.Fatal(err)
		}
	}
	if err := f.store.write("operator/ssh/id_ed25519.pub", []byte("ssh-ed25519 isolated-fresh-cli-public\n"), true); err != nil {
		t.Fatal(err)
	}
	_, operator, err := readEnvironmentCreateOperator(f.cfg)
	if err != nil {
		t.Fatal("original raw CLI operator", err)
	}
	artifactBefore, err := os.ReadFile(workloadImageForwardPath(f.e))
	if err != nil {
		t.Fatal(err)
	}
	previewBefore := createOpenBaoACKStoreSnapshot(t, f.store)
	published := images["LKE_PKI_MANAGEMENT_IMAGE"]
	roles, err := createRuntimeImageUpdateRoles("LKE_PKI_MANAGEMENT_IMAGE")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := prepareCreateCompletedRuntimeImageSelection(ctx, f.cfg, f.store, f.e, operator, published, roles, false, backend, io.Discard); err != nil {
		t.Fatal("single-role CLI preview", err)
	}
	if !reflect.DeepEqual(previewBefore, createOpenBaoACKStoreSnapshot(t, f.store)) {
		t.Fatal("CLI preview published configuration or receipts")
	}
	if _, err := prepareCreateCompletedRuntimeImageSelection(ctx, f.cfg, f.store, f.e, operator, published, roles, true, backend, io.Discard); err != nil {
		t.Fatal("single-role CLI commit with raw neutral aliases", err)
	}
	_, updated, err := readEnvironmentCreateOperator(f.cfg)
	if err != nil {
		t.Fatal(err)
	}
	for key, value := range operator {
		if key != environmentImagesKey && updated[key] != value {
			t.Fatal("single-role commit changed unrelated setting", key)
		}
	}
	selection, err := parseEnvironmentImageSelection(f.cfg.Environment, updated[environmentImagesKey])
	if err != nil || deploymentplan.Digest(selection.Images["LKE_PKI_MANAGEMENT_IMAGE"]) != deploymentplan.Digest(published) || deploymentplan.Digest(selection.Images["LKE_PKI_CONTROLLER_IMAGE"]) != deploymentplan.Digest(historical.Images["LKE_PKI_CONTROLLER_IMAGE"]) {
		t.Fatal("single-role updater changed Controller or lost candidate publication", err)
	}
	artifactAfter, err := os.ReadFile(workloadImageForwardPath(f.e))
	if err != nil || !bytes.Equal(artifactBefore, artifactAfter) {
		t.Fatal("completed updater rewrote original workload image authority", err)
	}
	committed := createOpenBaoACKStoreSnapshot(t, f.store)
	if _, err := prepareCreateCompletedRuntimeImageSelection(ctx, f.cfg, f.store, f.e, updated, published, roles, true, backend, io.Discard); err != nil || !reflect.DeepEqual(committed, createOpenBaoACKStoreSnapshot(t, f.store)) {
		t.Fatal("committed role selection retry changed state", err)
	}
	f.e.versionState.cache = &createOperatorVersionCache{RuntimeImages: images}
	oldGO, err := writeCurrentCoreForwardEnvelope(f.store, f.e.Journal.PlanSHA256, originalReview, "", "", deploymentplan.Digest(historical))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := loadCurrentCoreForwardEnvelope(ctx, f.store, f.e, oldGO, originalReview, nil, f.runtime); err == nil {
		t.Fatal("new role selection reused the old full GO envelope")
	}
	fullGO, err := writeCurrentCoreForwardEnvelope(f.store, f.e.Journal.PlanSHA256, originalReview, "", "", deploymentplan.Digest(q))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := loadCurrentCoreForwardEnvelope(ctx, f.store, f.e, fullGO, originalReview, nil, f.runtime); err != nil {
		t.Fatal("exact new role selection and ordinary child lost complete GO binding", err)
	}

	_, _, candidateRelease := completeEnvironmentRenderFixture("dev")
	for role, image := range images {
		candidateRelease.Images[role] = image
	}
	preRolloutWrites := deploymentplan.Digest(f.b.writes)
	if _, restored, err := restoreEnvironmentFreshProductBootstrap(ctx, f.cfg, f.p, candidateRelease, f.store, f.runtime, apply); err != nil || !restored || preRolloutWrites != deploymentplan.Digest(f.b.writes) {
		t.Fatal("candidate image incorrectly required the old finalized source to already run the new package", err)
	}
	originalParentEntries := len(f.e.Journal.Entries)
	writes := deploymentplan.Digest(f.b.writes)
	for _, mode := range []string{"uid", "setting", "unchanged-controller-uid", "unchanged-controller-setting", "role", "evidence"} {
		t.Run(mode, func(t *testing.T) {
			id := q.Plan.Resources[0].ID
			if strings.HasPrefix(mode, "unchanged-controller-") {
				id = "Deployment/" + f.p.Stack + "-video-cloud/pki-controller"
			}
			original := deploymentplan.Clone(f.b.objects[id])
			defer func() { f.b.objects[id] = original; f.e.versionState.cache.RuntimeImages = images }()
			switch mode {
			case "uid", "unchanged-controller-uid":
				deploymentplan.Map(f.b.objects[id]["metadata"])["uid"] = "99999999-9999-4999-8999-999999999999"
			case "setting", "unchanged-controller-setting":
				deploymentplan.Map(f.b.objects[id]["spec"])["replicas"] = float64(2)
			case "role":
				f.e.versionState.cache.RuntimeImages = map[string]environmentReleaseImage{"LKE_CERTISSUER_IMAGE": images["LKE_PKI_MANAGEMENT_IMAGE"]}
			case "evidence":
				f.e.Journal.KubernetesLeases = f.e.Journal.KubernetesLeases[:0]
				defer func() {
					var restored environmentCreateJournal
					if readEnvironmentPreflightJSON(f.e.JournalPath, &restored) != nil {
						t.Fatal("original durable parent")
					}
					f.e.Journal = restored
				}()
			}
			if _, _, err := selectedCreateWorkloadImageForward(ctx, f.store, f.e, backend); err == nil {
				t.Fatal("runtime successor accepted foreign owner, settings, role or native proof")
			}
		})
	}
	if writes != deploymentplan.Digest(f.b.writes) {
		t.Fatal("runtime successor qualification wrote native state")
	}
	check := func(ctx context.Context, p *deploymentplan.Plan) error { return f.e.Check(ctx) }
	failAcceptance := true
	run := func() error {
		return executeEnvironmentCreateStagePlan(ctx, f.store, f.e, stage, q.Plan, backend, check, check, func(ctx context.Context, p *deploymentplan.Plan) error {
			if err := p.Verify(ctx, backend); err != nil {
				return err
			}
			if failAcceptance {
				failAcceptance = false
				return errors.New("isolated image acceptance interruption")
			}
			return nil
		}, func(context.Context) error { return nil }, io.Discard)
	}
	if err := run(); err == nil {
		t.Fatal("known image write acceptance interruption was not reached")
	}
	writtenBeforeAcceptance := deploymentplan.Digest(f.b.writes)
	partial, sameStage, err := selectedCreateWorkloadImageForward(ctx, f.store, f.e, backend)
	if err != nil || sameStage != stage || deploymentplan.Digest(partial) != deploymentplan.Digest(q) {
		t.Fatal("known own writes changed their original source or child", err)
	}
	if err := run(); err != nil || writtenBeforeAcceptance != deploymentplan.Digest(f.b.writes) {
		t.Fatal("ordinary image acceptance retry replayed a known write", err)
	}
	written := deploymentplan.Digest(f.b.writes)
	again, sameStage, err := selectedCreateWorkloadImageForward(ctx, f.store, f.e, backend)
	if err != nil || sameStage != stage || deploymentplan.Digest(again) != deploymentplan.Digest(q) {
		t.Fatal("same selection did not reuse exact original child", err)
	}
	if err := run(); err != nil || written != deploymentplan.Digest(f.b.writes) {
		t.Fatal("completed runtime child replayed native writes", err)
	}
	if prefix != deploymentplan.Digest(f.e.Journal.Entries[:originalParentEntries]) {
		t.Fatal("runtime successor rewrote accepted parent prefix or aliases")
	}
	for path, raw := range before {
		after, err := os.ReadFile(path)
		if err != nil || !bytes.Equal(raw, after) {
			t.Fatal("runtime successor rewrote original accepted or cleanup journal", path, err)
		}
	}
	if _, err := verifyAcceptedStageAfterRecordedHistory(ctx, f.store, f.e, backend, "kubernetes/workload"); err != nil {
		t.Fatal("original accepted workload no longer follows genuine image child", err)
	}
	_, _, release := completeEnvironmentRenderFixture("dev")
	for role, image := range images {
		release.Images[role] = image
	}
	if _, restored, err := restoreEnvironmentFreshProductBootstrap(ctx, f.cfg, f.p, release, f.store, f.runtime, apply); err != nil || !restored || written != deploymentplan.Digest(f.b.writes) {
		t.Fatal("fresh finalized restore repeated identity/cleanup effects or rejected the recorded image child", err)
	}
	if _, err := loadCurrentCoreForwardEnvelope(ctx, f.store, f.e, fullGO, originalReview, nil, f.runtime); err != nil {
		t.Fatal("recorded image outcome changed its original full GO evidence", err)
	}
}

func TestCreateRuntimeImageUpdateRoleArgument(t *testing.T) {
	for _, tc := range []struct {
		role string
		want []string
		bad  bool
	}{
		{"", []string{"LKE_PKI_CONTROLLER_IMAGE", "LKE_PKI_MANAGEMENT_IMAGE"}, false},
		{"LKE_PKI_MANAGEMENT_IMAGE", []string{"LKE_PKI_MANAGEMENT_IMAGE"}, false},
		{"LKE_PKI_CONTROLLER_IMAGE", []string{"LKE_PKI_CONTROLLER_IMAGE"}, false},
		{"LKE_CERTISSUER_IMAGE", nil, true},
		{"management", nil, true},
	} {
		got, err := createRuntimeImageUpdateRoles(tc.role)
		if (err != nil) != tc.bad || !reflect.DeepEqual(got, tc.want) {
			t.Fatalf("role=%s got=%v err=%v", tc.role, got, err)
		}
	}
}
