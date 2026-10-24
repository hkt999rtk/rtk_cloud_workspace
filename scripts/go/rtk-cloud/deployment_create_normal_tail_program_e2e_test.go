package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

// The real program qualifier, active GO, admission, native HTTP client,
// cryptographic output validator, child executor and original parent release
// share one isolated run. Only the native API and resource transport are fake.
func TestCreateNormalTailProgramE2ERecoversEveryStageThroughOriginalRelease(t *testing.T) {
	for _, failure := range []string{"before-program-admit", "after-program-admit", "before-public-tls", "after-public-tls", "producer-acceptance", "producer-parent-receipt", "after-products", "initial-acceptance", "initial-parent-receipt", "after-initial", "after-native-finalize", "after-write-steady-workload", "steady-parent-receipt", "external", "dns", "turn", "full-acceptance", "ledger", "closure"} {
		t.Run(failure, func(t *testing.T) { runNormalProgramE2E(t, failure) })
	}
}

func runNormalProgramE2E(t *testing.T, failure string) {
	t.Helper()
	cfg, store, operator, original := createStateFixture(t, "dev")
	e, err := startEnvironmentCreateExecution(t.Context(), cfg, original, operator, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	for _, key := range []string{"MQTT_FOUNDATION_REGISTRATION_ENABLED", "SHADOW_WORKER_REGISTRATION_ENABLED", "OTA_SERVICE_REGISTRATION_ENABLED", "VIDEO_STORAGE_SERVICE_REGISTRATION_ENABLED", "WEBRTC_SERVICE_REGISTRATION_ENABLED", "LOGGER_SERVICE_REGISTRATION_ENABLED"} {
		cfg.Values[key] = "false"
	}
	cfg.Values["SHADOW_WORKER_REGISTRATION_ENABLED"] = "true"
	raw, inspection, _, service, row, observedAt := productMaterialFixture(t)
	service.BootstrapSessionID = "33333333-3333-4333-8333-333333333333"
	service.Intermediate.RootFingerprint = service.Root.RootFingerprint
	h := &createNormalTailHandoff{Execution: e, Pins: map[string]string{"root": service.Root.RootFingerprint, "redis-archive": deploymentplan.Digest("protected-original-dataset")}}
	protected := deploymentplan.Digest(h.Pins)
	outputs, err := environmentCreateProductIdentityIntent(cfg)
	if err != nil || len(outputs) != 1 {
		t.Fatal(err)
	}
	policy := environmentCreateIssuerPolicy{Domain: "service", ServiceClientIDs: []string{"service:deployment-bootstrap", outputs[0].Subject}}
	outputs[0].RootID, outputs[0].RootSHA256, outputs[0].PolicySHA256 = service.Root.ID, service.Root.RootFingerprint, deploymentplan.Digest(policy)
	consumer := deploymentplan.Object{"apiVersion": "apps/v1", "kind": "Deployment", "metadata": deploymentplan.Object{"namespace": e.Journal.Stack + "-video-cloud", "name": "shadow-worker"}, "spec": deploymentplan.Object{"selector": deploymentplan.Object{"matchLabels": deploymentplan.Object{"app": "shadow-worker"}}, "template": deploymentplan.Object{"metadata": deploymentplan.Object{"labels": deploymentplan.Object{"app": "shadow-worker"}}, "spec": deploymentplan.Object{"containers": []any{deploymentplan.Object{"name": "shadow-worker", "image": "example.invalid/shadow@sha256:" + strings.Repeat("a", 64), "volumeMounts": []any{deploymentplan.Object{"name": "identity", "mountPath": "/run/identity", "readOnly": true}}}}, "volumes": []any{deploymentplan.Object{"name": "identity", "secret": deploymentplan.Object{"secretName": strings.Split(outputs[0].ID, "/")[2]}}}}}}}
	defs := []deploymentplan.Definition{{Object: consumer, Phase: "workload", Dependencies: []string{outputs[0].ID}}}
	l := &createNormalTailProductLifecycle{ParentSHA256: e.Journal.PlanSHA256, OwnerSHA256: deploymentplan.Digest(e.owner), SourceSHA256: deploymentplan.Digest(e.Journal), Root: service.Root, Intermediate: service.Intermediate, Policy: policy, Outputs: outputs, RequiresSuccessor: true, SourceDefinitions: defs, SourceOwners: map[string]string{}, StorageClass: "linode-block-storage-retain", Bootstrap: environmentCreateBootstrapPlan{Environment: cfg.Environment, Stack: e.Journal.Stack}}
	p := createNormalTailProgram{Schema: createNormalTailProgramSchema, ParentSHA256: e.Journal.PlanSHA256, OwnerSHA256: deploymentplan.Digest(e.owner), HandoffSHA256: protected, ConfigurationSHA256: deploymentplan.Digest("frozen-inputs"), MaintainedConfigSHA256: deploymentplan.Digest(cfg), ClusterSHA256: deploymentplan.Digest("cluster"), IdentitySHA256: deploymentplan.Digest("operator"), Release: environmentRelease{Environment: cfg.Environment, Stack: e.Journal.Stack}, Bindings: environmentIdentityBindings{Version: 1, Environment: cfg.Environment, Stack: e.Journal.Stack, Owners: map[string]string{}}, Products: l, Definitions: defs}
	b := &normalTailE2EBackend{e: e, objects: map[string]deploymentplan.Object{}, writes: map[string]int{}, routes: map[string]string{}, failure: failure}
	reserveCalls, finalizeCalls := 0, 0
	publicTLSCalls := 0
	publicTLS := func(context.Context) error {
		if err := b.trip("before-public-tls"); err != nil {
			return err
		}
		// The public certificate producer's native issuance/reconciliation is
		// exercised separately. This shared sequence must resume its completed
		// output before invoking any dependent product or workload stage.
		if publicTLSCalls == 0 {
			publicTLSCalls++
		}
		return b.trip("after-public-tls")
	}
	installedACK := false
	native := environmentCreateBootstrapFinalization{Environment: "dev", SessionID: service.BootstrapSessionID, LeafSHA256: row.Fingerprint, CRLSHA256: deploymentplan.Digest("native-crl"), CleanupSHA256: deploymentplan.Digest("native-cleanup")}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.URL.Path == "/v1/platform/pki/operations" {
			if publicTLSCalls != 1 {
				t.Error("native product creation preceded public TLS completion")
				w.WriteHeader(409)
				return
			}
			reserveCalls++
			var request environmentCreateIssuerRequest
			if json.NewDecoder(r.Body).Decode(&request) != nil || request.ParentID != service.Root.ID || reserveCalls != 1 {
				t.Error("native allocation replay or foreign Root")
				w.WriteHeader(409)
				return
			}
			json.NewEncoder(w).Encode(environmentCreateIssuerOperation{ID: "44444444-4444-4444-8444-444444444444", IssuerID: service.Intermediate.ID, Digest: deploymentplan.Digest(request), Status: "requested"})
			return
		}
		if r.URL.Path == "/v1/platform/pki/deployment-bootstrap/"+service.BootstrapSessionID+"/finalize" {
			if r.Method == http.MethodPost {
				finalizeCalls++
				if !installedACK || finalizeCalls != 1 {
					t.Error("finalization before ACK or replay")
					w.WriteHeader(409)
					return
				}
				native.FinalizedAt = time.Now().UTC()
			}
			if native.FinalizedAt.IsZero() {
				w.WriteHeader(404)
				return
			}
			json.NewEncoder(w).Encode(native)
			return
		}
		t.Error("undeclared native endpoint")
		w.WriteHeader(404)
	}))
	defer server.Close()
	client, err := newEnvironmentCreatePKIClient(server.URL, server.Client(), "dev", "test-operator")
	if err != nil {
		t.Fatal(err)
	}
	client.token = "isolated-native-token"
	client.Execution = e
	client.mutationScope = productIdentityScope(l)
	checks := map[string]func(context.Context) error{}
	for _, id := range deploymentplan.RequiredGateIDs(false) {
		checks[id] = func(ctx context.Context) error {
			if deploymentplan.Digest(h.Pins) != protected {
				return errors.New("protected bootstrap/data changed")
			}
			if _, err := createNormalTailProductOutputContracts(cfg, l); err != nil {
				return err
			}
			return e.Check(ctx)
		}
	}
	q, err := qualifyCreateNormalTailProgram(t.Context(), &p, checks, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	checker, err := environmentPreflightCheckerSHA256()
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	a := createNormalTailProgramArtifact{Program: p, CheckerSHA256: checker, Generation: deploymentplan.Digest("generation"), Qualification: q, QualifiedAt: now, ValidUntil: now.Add(10 * time.Minute)}
	if err = saveCreateNormalTailProgramArtifact(store, e, a); err != nil {
		t.Fatal(err)
	}
	if err = writeEnvironmentPreflightActive(store, environmentPreflightActive{Schema: environmentPreflightActiveSchema, Generation: a.Generation, Environment: "dev", Decision: "GO", PlanSHA256: deploymentplan.Digest(p), ProofSHA256: deploymentplan.Digest(a), Artifact: deploymentplan.Digest(p) + "/qualification-1"}); err != nil {
		t.Fatal(err)
	}
	var firstAdmission createNormalTailProgramAdmission
	var producer, initial, steady *deploymentplan.Plan
	var nativeResponse environmentCreateBootstrapFinalization
	var cancelAttempt context.CancelFunc
	fence := func(ctx context.Context, _ *deploymentplan.Plan) error {
		if deploymentplan.Digest(h.Pins) != protected {
			return errors.New("protected root/data drift")
		}
		return e.Check(ctx)
	}
	inputs := func(operation, stage string) deploymentplan.Inputs {
		return deploymentplan.Inputs{Environment: "dev", Stack: e.Journal.Stack, Operation: operation, ConfigurationSHA256: deploymentplan.Digest(cfg), ReleaseSHA256: deploymentplan.Digest(p.Release), BindingsSHA256: deploymentplan.Digest(stage), CreateParentSHA256: e.Journal.PlanSHA256, CreateHandoffSHA256: protected}
	}
	child := func(ctx context.Context, stage string, plan *deploymentplan.Plan) error {
		b.stage = stage
		return executeCreateNormalTailStageBackend(ctx, store, h, stage, plan, b, func(ctx context.Context, p *deploymentplan.Plan) error { return p.AdmitResumable(ctx, b, store.Root) }, fence, func(ctx context.Context, p *deploymentplan.Plan) error {
			if err := p.Verify(ctx, b); err != nil {
				return err
			}
			return b.trip(stage + "-acceptance")
		}, io.Discard, func(context.Context) error { return b.trip(stage + "-parent-receipt") })
	}
	produce := func(ctx context.Context) error {
		request := environmentCreateIssuerRequest{Environment: "dev", Domain: "service", Kind: "intermediate", ParentID: service.Root.ID, ServiceClientIDs: policy.ServiceClientIDs}
		var operation environmentCreateIssuerOperation
		found, err := readProductMutation(ctx, client, "pki-reserve-service-intermediate", request, &operation)
		if err != nil {
			return err
		}
		if !found {
			if _, err = client.ReserveIssuer(ctx, "isolated-native-product", request); err != nil {
				return err
			}
		}
		material, err := productIdentityMaterial(raw, inspection, outputs[0], service, row, observedAt)
		if err != nil {
			return err
		}
		if producer == nil {
			secret := environmentCreateSecret(e.Journal.Stack+"-video-cloud", strings.Split(outputs[0].ID, "/")[2], material)
			secret.Object["immutable"] = true
			producer, err = deploymentplan.Compile(inputs("scoped", "producer"), []deploymentplan.Definition{secret}, nil)
			if err != nil {
				return err
			}
		}
		if err = child(ctx, "producer", producer); err != nil {
			return err
		}
		return b.trip("after-products")
	}
	workload := func(ctx context.Context) error {
		entry := createOpenBaoACKCompletionEntry(e.Journal, "kubernetes/workload")
		if entry != nil && entry.State == "completed" {
			j, exists, err := initial.ReadExecution(store.Root)
			if err != nil || !exists || !j.Accepted {
				return errors.New("accepted initial workload lost")
			}
			return nil
		}
		if initial == nil {
			secret := deploymentplan.Clone(b.objects[outputs[0].ID])
			app := deploymentplan.Clone(consumer)
			container := deploymentplan.Map(certIssuerObjectList(environmentPKIPod(app)["containers"])[0])
			container["env"] = environmentCreateEnv(map[string]string{"PKI_BOOTSTRAP_CONTROLLER_URL": "https://pki-controller." + e.Journal.Stack + "-video-cloud.svc:18446", "PKI_BOOTSTRAP_ISSUER_ID": service.Intermediate.ID, "PKI_BOOTSTRAP_SESSION_ID": service.BootstrapSessionID})
			var err error
			initial, err = deploymentplan.Compile(inputs("create", "initial"), []deploymentplan.Definition{{Object: secret, Phase: "configuration", Retain: true}, {Object: app, Phase: "workload", Dependencies: []string{outputs[0].ID}}}, []deploymentplan.Object{secret})
			if err != nil {
				return err
			}
		}
		b.stage = "initial"
		if err := executeCreateNormalTailWorkloadBackend(ctx, store, h, initial, b, func(ctx context.Context, p *deploymentplan.Plan) error { return p.AdmitResumable(ctx, b, store.Root) }, fence, func(ctx context.Context, p *deploymentplan.Plan) error {
			if err := p.Verify(ctx, b); err != nil {
				return err
			}
			if err := b.trip("initial-acceptance"); err != nil {
				return err
			}
			installedACK = true
			return nil
		}, io.Discard, func(context.Context) error { return b.trip("initial-parent-receipt") }); err != nil {
			return err
		}
		return b.trip("after-initial")
	}
	finalize := func(ctx context.Context) error {
		key := client.mutationKey("service-session-finalize")
		intent := mustProviderRecoveryJSON(deploymentplan.Object{"session_id": service.BootstrapSessionID, "issuer_id": service.Intermediate.ID})
		entry := createOpenBaoACKCompletionEntry(e.Journal, key)
		if entry == nil {
			if err := e.BeginEntry(key, "bootstrap", intent, time.Now().UTC()); err != nil {
				return err
			}
			if err := client.call(ctx, http.MethodPost, "/v1/platform/pki/deployment-bootstrap/"+service.BootstrapSessionID+"/finalize", "", nil, &nativeResponse); err != nil {
				return err
			}
			if err := e.RecordReceipt(key, mustProviderRecoveryJSON(nativeResponse)); err != nil {
				return err
			}
		} else {
			if entry.State == "pending" {
				return errors.New("unknown native finalization outcome")
			}
			var current environmentCreateBootstrapFinalization
			if err := client.call(ctx, http.MethodGet, "/v1/platform/pki/deployment-bootstrap/"+service.BootstrapSessionID+"/finalize", "", nil, &current); err != nil || current != nativeResponse {
				return errors.New("native finalization changed")
			}
			if entry.State == "completed" {
				return e.Check(ctx)
			}
		}
		if err := b.trip("after-native-finalize"); err != nil {
			return err
		}
		return e.CompleteEntry(key, time.Now().UTC())
	}
	makeSteady := func(ctx context.Context) error {
		if steady == nil {
			app := deploymentplan.Clone(consumer)
			deploymentplan.Map(certIssuerObjectList(environmentPKIPod(app)["containers"])[0])["env"] = []any{}
			deploymentplan.Map(app["metadata"])["uid"] = deploymentplan.Map(b.objects[deploymentplan.ID(app)]["metadata"])["uid"]
			var err error
			steady, err = deploymentplan.Compile(inputs("scoped", "steady"), []deploymentplan.Definition{{Object: app, Phase: "workload"}}, []deploymentplan.Object{b.objects[deploymentplan.ID(app)]})
			if err != nil {
				return err
			}
		}
		return child(ctx, "steady", steady)
	}
	route := func(key string) func(context.Context) error {
		return func(ctx context.Context) error {
			intent, receipt := mustProviderRecoveryJSON(deploymentplan.Object{"resource": key}), mustProviderRecoveryJSON(deploymentplan.Object{"target": "qualified-" + key})
			done, err := createNormalTailRecordedStage(ctx, e, key, "routing", intent, receipt, func(context.Context) error {
				if b.routes[key] != "qualified-"+key {
					return errors.New("routing drift")
				}
				return nil
			})
			if err != nil || done {
				return err
			}
			if err = e.BeginEntry(key, "routing", intent, time.Now().UTC()); err != nil {
				return err
			}
			if b.routes[key] != "" {
				return errors.New("unrecorded route")
			}
			b.routes[key] = "qualified-" + key
			if err = e.RecordReceipt(key, receipt); err != nil {
				return err
			}
			if err = b.trip(key); err != nil {
				return err
			}
			return e.CompleteEntry(key, time.Now().UTC())
		}
	}
	accept := func(ctx context.Context) error {
		if err := steady.Verify(ctx, b); err != nil {
			return err
		}
		if !installedACK || native.FinalizedAt.IsZero() || len(b.routes) != 3 || deploymentplan.Digest(h.Pins) != protected {
			return errors.New("complete runtime missing")
		}
		return b.trip("full-acceptance")
	}
	capture := func(ctx context.Context) error {
		ledger, err := readEnvironmentCreateLedger(e)
		if err != nil {
			return err
		}
		for id, o := range b.objects {
			ledger.Kubernetes[id] = deploymentplan.String(deploymentplan.Map(o["metadata"])["uid"])
		}
		if err = saveEnvironmentCreateLedger(ctx, e, ledger); err != nil {
			return err
		}
		if err = b.trip("ledger"); err != nil {
			return err
		}
		if failure == "closure" && !b.failed {
			b.failed = true
			cancelAttempt()
		}
		return nil
	}
	for attempt := 0; attempt < 2; attempt++ {
		ctx, cancel := context.WithCancel(withCreateNormalTailHandoff(t.Context(), h))
		cancelAttempt = cancel
		err := b.trip("before-program-admit")
		if err == nil {
			var admission createNormalTailProgramAdmission
			admission, err = admitCreateNormalTailProgramWithFence(ctx, cfg, h, a, time.Duration(5+attempt*10)*time.Minute, time.Now().UTC(), func(ctx context.Context) error { return fence(ctx, nil) })
			if err == nil {
				if firstAdmission.Deadline.IsZero() {
					firstAdmission = admission
				} else if admission != firstAdmission {
					t.Fatal("retry reset admission")
				}
				err = b.trip("after-program-admit")
			}
		}
		if err == nil {
			err = executeCreateNormalTailProgramSteps(ctx, e, publicTLS, produce, workload, finalize, makeSteady, route("external"), route("dns"), route("turn"), accept, capture)
		}
		cancel()
		if attempt == 0 && err == nil {
			t.Fatal("expected interruption was not reached")
		}
		if attempt == 1 && err != nil {
			t.Fatal(err)
		}
	}
	if !b.failed || publicTLSCalls != 1 || reserveCalls != 1 || finalizeCalls != 1 || b.writes[outputs[0].ID] != 1 || b.writes[deploymentplan.ID(consumer)] != 2 || e.Journal.CompletedAt.IsZero() {
		t.Fatal("native replay or incomplete closure", reserveCalls, finalizeCalls, b.writes)
	}
	if _, err := os.Lstat(filepath.Join(store.Root, "deployment/.execution-active")); !os.IsNotExist(err) {
		t.Fatal("original fence was not released")
	}
	var archived environmentCreateOwner
	if readEnvironmentPreflightJSON(filepath.Join(e.Directory, "owner", "owner.json"), &archived) != nil || archived.OwnerToken != e.owner.OwnerToken {
		t.Fatal("original owner archive missing")
	}
	if deploymentplan.Digest(h.Pins) != protected {
		t.Fatal("protected Root or Redis dataset changed")
	}
}
