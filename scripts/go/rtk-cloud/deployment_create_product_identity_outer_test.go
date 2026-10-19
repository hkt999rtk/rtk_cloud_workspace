package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
	"strings"
	"testing"
	"time"
)

type productOuterTargetBackend struct {
	*productProtocolBackend
	retireFailure string
	failed        bool
}

func (b *productOuterTargetBackend) Write(ctx context.Context, action string, object deploymentplan.Object, dry bool) (deploymentplan.Object, error) {
	actual, err := b.productProtocolBackend.Write(ctx, action, object, dry)
	if err == nil && action == "retire" && !dry {
		delete(b.objects, deploymentplan.ID(object))
		// Model the native backend's existing foreground deletion: only exact
		// controller UID descendants disappear with their accepted parent.
		removed := map[string]bool{deploymentplan.String(deploymentplan.Map(actual["metadata"])["uid"]): true}
		for changed := true; changed; {
			changed = false
			for id, child := range b.objects {
				for _, raw := range certIssuerObjectList(deploymentplan.Map(child["metadata"])["ownerReferences"]) {
					if removed[deploymentplan.String(deploymentplan.Map(raw)["uid"])] {
						removed[deploymentplan.String(deploymentplan.Map(child["metadata"])["uid"])] = true
						delete(b.objects, id)
						changed = true
						break
					}
				}
			}
		}
	}
	return actual, err
}
func (b *productOuterTargetBackend) Wait(ctx context.Context, resource deploymentplan.Resource) error {
	if resource.Action == "retire" {
		if b.retireFailure == "known-write" && !b.failed {
			b.failed = true
			return errors.New("isolated cancellation after known conditional delete")
		}
		if b.objects[resource.ID] != nil {
			return errors.New("retirement has not completed")
		}
		return nil
	}
	return b.productProtocolBackend.Wait(ctx, resource)
}

func newProductOuterTargetFixture(t *testing.T, successor bool, selected ...deploymentConfig) (*productProtocolFixture, createNormalTailProgram, environmentRelease, int, environmentCreateBootstrapApply) {
	return newProductOuterTargetFixtureWithSetup(t, successor, nil, selected...)
}

func newProductOuterTargetFixtureWithSetup(t *testing.T, successor bool, setup func(*productProtocolFixture, *createNormalTailProgram), selected ...deploymentConfig) (*productProtocolFixture, createNormalTailProgram, environmentRelease, int, environmentCreateBootstrapApply) {
	t.Helper()
	f := newProductProtocolFixture(t, successor)
	if len(selected) == 1 {
		original := f.cfg.Workspace
		f.cfg = selected[0]
		template := "repos/rtk_video_cloud/deploy/pki/render-staging-controller.py"
		writeTestFile(t, filepath.Join(f.cfg.Workspace, template), readTestFile(t, filepath.Join(original, template)))
	}
	// These are the finite original bootstrap owners used by the maintained
	// Controller/CertIssuer recipes, not generated future dependency stubs.
	ns := f.p.Stack + "-video-cloud"
	originals := []deploymentplan.Definition{
		environmentCreateServiceClaimDefinition(f.cfg, ns, "pki-controller-state"),
		environmentCreateServiceClaimDefinition(f.cfg, ns, "certissuer-service-state"),
		environmentCreateSecret(ns, "pki-controller-database", map[string]string{"url": "postgres://isolated-original-bootstrap"}),
		environmentCreateSecret(ns, "pki-platform-key", map[string]string{"key": strings.Repeat("a", 32)}),
		{Object: deploymentplan.Object{"apiVersion": "v1", "kind": "ServiceAccount", "metadata": deploymentplan.Object{"name": "pki-controller", "namespace": ns}}},
		{Object: deploymentplan.Object{"apiVersion": "v1", "kind": "ServiceAccount", "metadata": deploymentplan.Object{"name": "certissuer", "namespace": ns}}},
		{Object: deploymentplan.Object{"apiVersion": "v1", "kind": "ConfigMap", "metadata": deploymentplan.Object{"name": "pki-openbao-transport-ca", "namespace": ns}, "data": deploymentplan.Object{"ca.crt": f.runtime.TransportCAPEM}}},
	}
	for n, d := range originals {
		o := deploymentplan.Clone(d.Object)
		m := deploymentplan.Map(o["metadata"])
		m["uid"] = fmt.Sprintf("aaaaaaaa-bbbb-4ccc-8ddd-%012d", n+1)
		m["resourceVersion"] = "1"
		m["labels"] = deploymentplan.Object{"rtk.realtek.com/stack": f.p.Stack}
		if values := deploymentplan.Map(o["stringData"]); values != nil {
			data := deploymentplan.Object{}
			for k, v := range values {
				data[k] = base64.StdEncoding.EncodeToString([]byte(deploymentplan.String(v)))
			}
			o["data"] = data
			delete(o, "stringData")
		}
		f.b.objects[deploymentplan.ID(o)] = o
		f.l.SourceOwners[deploymentplan.ID(o)] = deploymentplan.String(m["uid"])
		f.l.SourceDefinitions = append(f.l.SourceDefinitions, deploymentplan.Definition{Object: deploymentplan.Projection(o), Phase: environmentResourcePhase(deploymentplan.String(o["kind"])), Retain: true})
	}
	for _, object := range f.b.objects {
		if object["kind"] == "Namespace" {
			f.l.SourceOwners[deploymentplan.ID(object)] = deploymentplan.String(deploymentplan.Map(object["metadata"])["uid"])
			f.l.SourceDefinitions = append(f.l.SourceDefinitions, deploymentplan.Definition{Object: deploymentplan.Projection(object), Phase: "namespace", Retain: true})
		}
	}
	// Reuse the maintained native Controller/CertIssuer source recipes and
	// actual five-product protocol. The separate trust-only nine-reader
	// fixture deliberately omits OpenBao configuration and is not a full
	// environment observation fixture.
	for i := range f.l.Outputs {
		f.l.Outputs[i].RootID, f.l.Outputs[i].RootSHA256, f.l.Outputs[i].PolicySHA256 = f.l.Root.ID, f.l.Root.RootFingerprint, deploymentplan.Digest(f.l.Policy)
		if !successor {
			f.l.Outputs[i].IssuerID = f.l.Intermediate.ID
		}
	}

	f.runtime.ProductIdentityScope = productIdentityScope(f.l)
	f.runtime.PKIClient.mutationScope = f.runtime.ProductIdentityScope
	_, _, formal := completeEnvironmentRenderFixture("dev")
	installProductCleanupHandoffFixture(t, f, formal)
	for id := range f.l.SourceOwners {
		f.l.SourceOwners[id] = deploymentplan.String(deploymentplan.Map(f.b.objects[id]["metadata"])["uid"])
	}
	for _, d := range f.l.SourceDefinitions {
		if d.Object["kind"] == "Namespace" {
			deploymentplan.Map(d.Object["metadata"])["uid"] = f.l.SourceOwners[deploymentplan.ID(d.Object)]
		}
	}
	f.runtime.ProductIdentityScope = productIdentityScope(f.l)
	f.runtime.PKIClient.mutationScope = f.runtime.ProductIdentityScope
	digest := deploymentplan.Digest("frozen-protocol-inputs")
	p := createNormalTailProgram{Schema: createNormalTailProgramSchema, ParentSHA256: f.e.Journal.PlanSHA256, OwnerSHA256: deploymentplan.Digest(f.e.owner), HandoffSHA256: deploymentplan.Digest(f.h.Pins), ConfigurationSHA256: digest, MaintainedConfigSHA256: deploymentplan.Digest(f.cfg), ClusterSHA256: digest, IdentitySHA256: digest, Release: formal, Bindings: environmentIdentityBindings{Version: 1, Environment: "dev", Stack: f.p.Stack, Owners: f.l.SourceOwners}, Products: f.l, Definitions: f.l.SourceDefinitions}
	if setup != nil {
		setup(f, &p)
		f.runtime.ProductIdentityScope = productIdentityScope(f.l)
		f.runtime.PKIClient.mutationScope = f.runtime.ProductIdentityScope
		p.Products = f.l
		p.Definitions = f.l.SourceDefinitions
		p.Bindings.Owners = f.l.SourceOwners
	}
	checks := map[string]func(context.Context) error{}
	for _, id := range deploymentplan.RequiredGateIDs(false) {
		checks[id] = func(ctx context.Context) error {
			if _, err := createNormalTailProductOutputContracts(f.cfg, f.l); err != nil {
				return err
			}
			return f.e.Check(ctx)
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
	a := createNormalTailProgramArtifact{Program: p, Qualification: q, CheckerSHA256: checker, Generation: digest, QualifiedAt: now, ValidUntil: now.Add(time.Hour)}
	if err = saveCreateNormalTailProgramArtifact(f.store, f.e, a); err != nil {
		t.Fatal(err)
	}
	if err = writeEnvironmentPreflightActive(f.store, environmentPreflightActive{Schema: environmentPreflightActiveSchema, Generation: digest, Environment: "dev", Decision: "GO", PlanSHA256: deploymentplan.Digest(p), ProofSHA256: deploymentplan.Digest(a), Artifact: deploymentplan.Digest(p) + "/qualification-1"}); err != nil {
		t.Fatal(err)
	}
	if _, err = admitCreateNormalTailProgramWithFence(t.Context(), f.cfg, f.h, a, time.Hour, time.Now().UTC(), f.e.Check); err != nil {
		t.Fatal(err)
	}
	start := len(f.e.Journal.Entries)
	apply := func(ctx context.Context, stage string, defs []deploymentplan.Definition) error {
		full := "product-identities-" + productIdentityScope(f.l)[:12] + "-" + stage
		if f.plans[full] == nil {
			var live []deploymentplan.Object
			for _, o := range f.b.objects {
				live = append(live, o)
			}
			plan, err := deploymentplan.Compile(deploymentplan.Inputs{Operation: "scoped", Environment: "dev", Stack: f.p.Stack, ConfigurationSHA256: deploymentplan.Digest(f.cfg), ReleaseSHA256: deploymentplan.Digest(p.Release), BindingsSHA256: environmentCreateStageBindingsSHA(full, defs), CreateParentSHA256: f.e.Journal.PlanSHA256, CreateHandoffSHA256: deploymentplan.Digest(f.h.Pins)}, defs, live)
			if err != nil {
				return err
			}
			f.plans[full] = plan
		}
		if err := saveCreateNormalTailStagePlan(ctx, f.store, f.e, full, f.plans[full]); err != nil {
			return err
		}
		return f.apply(ctx, stage, defs)
	}
	return f, p, formal, start, apply
}

func TestProductOuterTargetCompilesRealProtocolAndRetiresOnlyFinishedProducers(t *testing.T) {
	compileCfg, snapshotStore, _, _ := maintainedDeploymentSnapshotFixture(t, "dev")
	architecture := filepath.Join(compileCfg.Workspace, "cloud_deploy/architectures/kubernetes/architecture.env")
	architectureText := readTestFile(t, architecture)
	settings, _, _ := completeEnvironmentRenderFixture("dev")
	settings["MQTT_FOUNDATION_REGISTRATION_ENABLED"], settings["ACCOUNT_MANAGER_SERVICE_REGISTRATION_ENABLED"] = "true", "true"
	settings["VIDEO_CLOUD_OTA_ENTITLEMENTS_REQUIRED"] = "true"
	settings["SHADOW_WORKER_REGISTRATION_ENABLED"], settings["OTA_SERVICE_REGISTRATION_ENABLED"], settings["VIDEO_STORAGE_SERVICE_REGISTRATION_ENABLED"], settings["WEBRTC_SERVICE_REGISTRATION_ENABLED"] = "true", "true", "true", "true"
	for key, value := range settings {
		if !(environmentAuthArchitectureKey(key) || environmentControllerPolicySetting(key) || sharedWebRTCDeploymentKey(key) || sharedVideoStorageDeploymentKey(key) || sharedShadowDeploymentKey(key) || sharedOTADeploymentKey(key) || key == "MQTT_FOUNDATION_REGISTRATION_ENABLED" || key == "ACCOUNT_MANAGER_SERVICE_REGISTRATION_ENABLED" || key == "ACCOUNT_MANAGER_SERVICE_REGISTRATION_PORT") {
			continue
		}
		lines, found := strings.Split(architectureText, "\n"), false
		for n, line := range lines {
			if strings.HasPrefix(line, key+"=") {
				lines[n] = key + "=" + value
				found = true
			}
		}
		if !found {
			lines = append(lines, key+"="+value)
		}
		architectureText = strings.Join(lines, "\n")
	}
	writeTestFile(t, architecture, architectureText)
	writeTestFile(t, filepath.Join(compileCfg.EnvironmentRoot, "storage.env"), "RUNTIME_MEDIA_STORAGE_POLICY=colocated\nRUNTIME_MEDIA_STORAGE_BUCKET=rtk-cloud-dev-runtime-us-sea\nRUNTIME_MEDIA_STORAGE_PREFIX=environments/video-cloud-dev\nRUNTIME_OTA_STORAGE_MODE=dedicated\nRUNTIME_OTA_STORAGE_POLICY=colocated\nRUNTIME_OTA_STORAGE_BUCKET=rtk-cloud-dev-ota-firmware-us-sea\nRUNTIME_OTA_STORAGE_PREFIX=environments/video-cloud-dev\n")
	var err error
	compileCfg, err = resolveDeploymentConfig(compileCfg.Workspace, "dev", "")
	if err != nil {
		t.Fatal(err)
	}
	f, program, formal, start, apply := newProductOuterTargetFixture(t, false, compileCfg)
	ctx := withCreateNormalTailHandoff(t.Context(), f.h)
	if err := executeProductIdentityProtocol(ctx, f.cfg, f.p, f.store, f.runtime, f.l, apply); err != nil {
		t.Fatal("real native protocol", err)
	}
	installed, err := readProductInstalled(f.store, f.l)
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := productPreparationDefinitions(f.cfg, f.store, f.e, f.l, installed, false)
	if err != nil || len(prepared) != 6 {
		t.Fatal("accepted preparation declarations", len(prepared), err)
	}
	for _, mode := range []string{"missing-parent", "foreign-receipt-owner", "missing-lease"} {
		t.Run(mode, func(t *testing.T) {
			view := *f.e
			view.Journal = f.e.Journal
			view.Journal.Entries = append([]environmentCreateJournalEntry(nil), f.e.Journal.Entries...)
			key := "kubernetes/product-identities-" + productIdentityScope(f.l)[:12] + "-service-custody-reader"
			for n, entry := range view.Journal.Entries {
				if entry.Key == key {
					if mode == "missing-parent" {
						view.Journal.Entries = append(view.Journal.Entries[:n], view.Journal.Entries[n+1:]...)
					}
					if mode == "foreign-receipt-owner" {
						var receipt struct {
							Plan   string            `json:"plan_sha256"`
							Owners map[string]string `json:"owners"`
						}
						if json.Unmarshal(entry.Receipt, &receipt) != nil {
							t.Fatal("fixture receipt")
						}
						for id := range receipt.Owners {
							receipt.Owners[id] = "99999999-9999-4999-8999-999999999999"
						}
						view.Journal.Entries[n].Receipt = mustProviderRecoveryJSON(receipt)
					}
					break
				}
			}
			if mode == "missing-lease" {
				view.Journal.KubernetesLeases = nil
			}
			if _, err := productPreparationDefinitions(f.cfg, f.store, &view, f.l, installed, false); err == nil {
				t.Fatal("unbound preparation accepted")
			}
		})
	}
	for _, d := range prepared {
		if !d.Retain || d.Retire || deploymentplan.Map(d.Object["metadata"])["uid"] != deploymentplan.Map(f.b.objects[deploymentplan.ID(d.Object)]["metadata"])["uid"] {
			t.Fatal("original preparation was rebound")
		}
		meta := deploymentplan.Map(d.Object["metadata"])
		rsName := deploymentplan.String(meta["name"]) + "-native-rs"
		rsUID := "aaaaaaaa-1111-4111-8111-" + deploymentplan.Digest(rsName)[:12]
		for _, o := range []deploymentplan.Object{
			{"apiVersion": "apps/v1", "kind": "ReplicaSet", "metadata": deploymentplan.Object{"name": rsName, "namespace": meta["namespace"], "uid": rsUID, "ownerReferences": []any{deploymentplan.Object{"apiVersion": "apps/v1", "kind": "Deployment", "name": meta["name"], "uid": meta["uid"], "controller": true}}}},
			{"apiVersion": "v1", "kind": "Pod", "metadata": deploymentplan.Object{"name": rsName + "-pod", "namespace": meta["namespace"], "uid": "bbbbbbbb-1111-4111-8111-" + deploymentplan.Digest(rsName)[:12], "ownerReferences": []any{deploymentplan.Object{"apiVersion": "apps/v1", "kind": "ReplicaSet", "name": rsName, "uid": rsUID, "controller": true}}}, "spec": deploymentplan.Clone(environmentPKIPod(d.Object)), "status": deploymentplan.Object{"phase": "Running"}},
		} {
			f.b.objects[deploymentplan.ID(o)] = o
		}
	}
	// The compiler reads only the selected isolated store. Its actual common
	// snapshot, dependency checks, full inventory and input fence remain enabled.
	for _, entry := range rtkSecretCatalog() {
		if err := f.store.write(filepath.Join("runtime", entry.ID), []byte(strings.Repeat("a", 32)), true); err != nil {
			t.Fatal(err)
		}
	}
	kube, err := os.ReadFile(snapshotStore.KubeconfigPath())
	if err != nil {
		t.Fatal(err)
	}
	if err := f.store.write("kube/kubeconfig.yaml", kube, true); err != nil {
		t.Fatal(err)
	}
	t.Setenv("RTK_CLOUD_CONFIG_ROOT", f.store.ConfigRoot)
	// Object-storage allocation is an existing input to this test's completed
	// bootstrap; it is not produced by the product identity protocol.
	proof := []byte("isolated original storage migration proof")
	proofSHA := environmentPreflightBytesSHA256(proof)
	for name, value := range map[string]any{
		"storage-migration-ota.json": json.RawMessage(mustProviderRecoveryJSON(deploymentplan.Object{"proof_sha256": proofSHA})),
		"storage-cutover-ota.json":   deploymentplan.Object{"environment": "dev", "bucket": compileCfg.Storage.OTAFirmware.Bucket, "region": compileCfg.Storage.OTAFirmware.Region, "prefix": compileCfg.Storage.OTAFirmware.Prefix, "cutover_id": "original-isolated-storage", "migration_receipt_sha256": proofSHA, "cutover_at": time.Now().UTC(), "rollback_credentials_retained": true, "service_ready": true},
	} {
		var raw []byte
		if name == "storage-migration-ota.json" {
			raw = proof
		} else {
			raw = mustProviderRecoveryJSON(value)
		}
		if err := f.store.write(filepath.Join("deployment/storage", name), raw, false); err != nil {
			t.Fatal(err)
		}
	}
	if err := f.store.write(storageCutoverJournalName("ota"), mustProviderRecoveryJSON(storageCutoverJournal{Environment: "dev", Purpose: "ota", Status: "complete", ID: "original-isolated-storage", MigrationSHA256: proofSHA}), false); err != nil {
		t.Fatal(err)
	}
	backend, err := newEnvironmentKubernetesBackend(string(kube), "dev")
	if err != nil {
		t.Fatal(err)
	}
	_ = formal
	volumes := map[string]deploymentplan.Object{}
	for _, object := range f.b.objects {
		if object["kind"] == "Job" {
			object["status"] = deploymentplan.Object{"succeeded": float64(1), "conditions": []any{deploymentplan.Object{"type": "Complete", "status": "True"}}}
		}
		if object["kind"] == "PersistentVolumeClaim" {
			meta := deploymentplan.Map(object["metadata"])
			name := "volume-" + deploymentplan.String(meta["name"])
			deploymentplan.Map(object["spec"])["volumeName"] = name
			object["status"] = deploymentplan.Object{"phase": "Bound"}
			volumes[name] = deploymentplan.Object{"apiVersion": "v1", "kind": "PersistentVolume", "metadata": deploymentplan.Object{"name": name, "uid": "11111111-2222-4333-8444-555555555555"}, "spec": deploymentplan.Object{"claimRef": deploymentplan.Object{"name": meta["name"], "namespace": meta["namespace"], "uid": meta["uid"]}}, "status": deploymentplan.Object{"phase": "Bound"}}
		}
	}
	// PersistentVolumes are named readbacks, outside the stack inventory's
	// maintained Kubernetes kind selection.
	originalKubectl, err := exec.LookPath("kubectl")
	if err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	raw, _ := json.Marshal(volumes)
	writeTestFile(t, filepath.Join(bin, "volumes.json"), string(raw))
	writeTestFile(t, filepath.Join(bin, "kubectl"), `#!/bin/sh
case "$*" in
 *' get PersistentVolume '*) python3 -B -c 'import json,sys; a=sys.argv[1:]; print(json.dumps(json.load(open(sys.argv[-1])).get(a[a.index("get")+2])))' "$@" "$PRODUCT_OUTER_VOLUMES" ;;
 *) exec "$PRODUCT_OUTER_KUBECTL" "$@" ;;
esac
`)
	writeTestFile(t, filepath.Join(bin, "git"), "#!/bin/sh\ncase \"$*\" in *'/repos/'*) printf '"+strings.Repeat("b", 40)+"\\n';; *) printf '"+strings.Repeat("a", 40)+"\\n';; esac\n")
	if err := os.Chmod(filepath.Join(bin, "kubectl"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(bin, "git"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PRODUCT_OUTER_KUBECTL", originalKubectl)
	t.Setenv("PRODUCT_OUTER_VOLUMES", filepath.Join(bin, "volumes.json"))
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	initial, err := compileCreateNormalTailProductTarget(ctx, compileCfg, f.h, &program, backend, false)
	if err != nil {
		t.Fatal("actual initial target compiler", err)
	}
	for _, d := range prepared {
		found := false
		for _, r := range initial.Plan.Resources {
			if r.ID == deploymentplan.ID(d.Object) {
				found = r.Action == "retain" && r.UID == deploymentplan.Map(d.Object["metadata"])["uid"]
			}
		}
		if !found {
			t.Fatal("initial target dropped accepted producer", deploymentplan.ID(d.Object))
		}
	}
	if err := saveCreateNormalTailTarget(initial, f.h); err != nil {
		t.Fatal(err)
	}
	boundary := &productOuterTargetBackend{productProtocolBackend: f.b}
	qualify := func(ctx context.Context, p *deploymentplan.Plan) error {
		return p.AdmitResumable(ctx, boundary, f.store.Root)
	}
	fence := func(ctx context.Context, p *deploymentplan.Plan) error { return f.e.Check(ctx) }
	accept := func(ctx context.Context, p *deploymentplan.Plan) error { return p.Verify(ctx, boundary) }
	if err := executeCreateNormalTailWorkloadBackend(ctx, f.store, f.h, initial.Plan, boundary, qualify, fence, accept, io.Discard, func(context.Context) error { return nil }); err != nil {
		t.Fatal("actual initial compiled child", err)
	}
	// The native registry observes every actual installed consumer before the
	// real finalizer runs. Its original key and copy-erasure protocol is shared.
	f.installed = append([]string(nil), f.subjects...)
	if err := completeProductIdentitySession(ctx, f.cfg, f.p, f.store, f.runtime, f.l, installed, nil); err != nil {
		t.Fatal("genuine finalizer", err)
	}
	if _, err := verifyProductFinalization(ctx, f.cfg, f.runtime, f.l, installed); err != nil {
		t.Fatal(err)
	}
	// A newly created consumer has no UID in the pre-production source. The
	// original accepted workload, not a later inventory, owns its steady update.
	steadyDefs, err := productSteadyDefinitions(f.cfg, f.l, installed)
	if err != nil {
		t.Fatal(err)
	}
	consumerID := deploymentplan.ID(steadyDefs[0].Object)
	originalUID := deploymentplan.Map(f.b.objects[consumerID]["metadata"])["uid"]
	deploymentplan.Map(f.b.objects[consumerID]["metadata"])["uid"] = "99999999-9999-4999-8999-999999999999"
	var replacedLive []deploymentplan.Object
	for _, object := range f.b.objects {
		replacedLive = append(replacedLive, object)
	}
	steadyStage := "product-identities-" + productIdentityScope(f.l)[:12] + "-steady"
	foreignSteady, err := deploymentplan.Compile(deploymentplan.Inputs{Operation: "scoped", Environment: "dev", Stack: f.p.Stack, ConfigurationSHA256: deploymentplan.Digest(f.cfg), ReleaseSHA256: deploymentplan.Digest(program.Release), BindingsSHA256: environmentCreateStageBindingsSHA(steadyStage, steadyDefs), CreateParentSHA256: f.e.Journal.PlanSHA256, CreateHandoffSHA256: deploymentplan.Digest(f.h.Pins)}, steadyDefs, replacedLive)
	if err != nil {
		t.Fatal("replacement must reach original-owner validation", err)
	}
	writesBeforeReplacement := deploymentplan.Digest(f.b.writes)
	if err := saveCreateNormalTailStagePlan(ctx, f.store, f.e, steadyStage, foreignSteady); err == nil || deploymentplan.Digest(f.b.writes) != writesBeforeReplacement {
		t.Fatal("steady selected a replacement owner")
	}
	if _, err := os.Lstat(createNormalTailStagePlanPath(f.e, steadyStage)); !os.IsNotExist(err) {
		t.Fatal("foreign steady input was persisted")
	}
	deploymentplan.Map(f.b.objects[consumerID]["metadata"])["uid"] = originalUID
	protected := map[string]string{}
	for _, object := range f.b.objects {
		if object["kind"] == "PersistentVolumeClaim" || object["kind"] == "Secret" {
			protected[deploymentplan.ID(object)] = deploymentplan.Digest(object)
		}
	}
	beforeRetireAdmission, captureRetire := true, false
	applyCleanup := func(ctx context.Context, stage string, defs []deploymentplan.Definition) error {
		full := "product-identities-" + productIdentityScope(f.l)[:12] + "-" + stage
		plan := f.plans[full]
		if plan == nil {
			var live []deploymentplan.Object
			for _, object := range f.b.objects {
				live = append(live, object)
			}
			var err error
			plan, err = deploymentplan.Compile(deploymentplan.Inputs{Operation: "scoped", Environment: "dev", Stack: f.p.Stack, ConfigurationSHA256: deploymentplan.Digest(f.cfg), ReleaseSHA256: deploymentplan.Digest(program.Release), BindingsSHA256: environmentCreateStageBindingsSHA(full, defs), CreateParentSHA256: f.e.Journal.PlanSHA256, CreateHandoffSHA256: deploymentplan.Digest(f.h.Pins)}, defs, live)
			if err != nil {
				return err
			}
			f.plans[full] = plan
			if err := saveCreateNormalTailStagePlan(ctx, f.store, f.e, full, plan); err != nil {
				return err
			}
		}
		stageQualify := func(ctx context.Context, p *deploymentplan.Plan) error {
			if stage == "producer-retire" && beforeRetireAdmission {
				beforeRetireAdmission = false
				return errors.New("isolated interruption before retirement admission")
			}
			return qualify(ctx, p)
		}
		return executeCreateNormalTailStageBackend(ctx, f.store, f.h, full, plan, boundary, stageQualify, fence, accept, io.Discard, func(context.Context) error {
			if stage == "producer-retire" && captureRetire {
				captureRetire = false
				return errors.New("isolated accepted retirement before parent close")
			}
			return nil
		})
	}
	boundary.retireFailure = "known-write"
	if err := applyProductSteadyCleanup(ctx, f.cfg, f.store, f.e, f.l, installed, applyCleanup); err == nil {
		t.Fatal("before-admission interruption absent")
	}
	for _, d := range prepared {
		if f.b.objects[deploymentplan.ID(d.Object)] == nil || f.b.writes[deploymentplan.ID(d.Object)] != 1 {
			t.Fatal("unstarted retirement performed a delete")
		}
	}
	entries := f.e.Journal.Entries[start:]
	if n, _, err := validateCreateNormalTailProductStagesWithConfig(f.cfg, f.store, f.e, f.l, entries); err != nil || n != len(entries) {
		t.Fatal("unstarted retirement prefix", n, len(entries), err)
	}
	if err := applyProductSteadyCleanup(ctx, f.cfg, f.store, f.e, f.l, installed, applyCleanup); err == nil {
		t.Fatal("retirement interruption absent")
	}
	entries = f.e.Journal.Entries[start:]
	if n, _, err := validateCreateNormalTailProductStagesWithConfig(f.cfg, f.store, f.e, f.l, entries); err != nil || n != len(entries) {
		t.Fatal("known partial retirement prefix", n, len(entries), err)
	}
	retirePlan := f.plans["product-identities-"+productIdentityScope(f.l)[:12]+"-producer-retire"]
	journalPath := filepath.Join(f.store.Root, "deployment/rollback", retirePlan.SHA256(), "journal.json")
	originalJournal, err := os.ReadFile(journalPath)
	if err != nil {
		t.Fatal(err)
	}
	var unknown deploymentplan.Journal
	if json.Unmarshal(originalJournal, &unknown) != nil {
		t.Fatal("original retirement journal")
	}
	unknown.InFlight = retirePlan.Resources[1].ID
	if err := os.WriteFile(journalPath, mustProviderRecoveryJSON(unknown), 0600); err != nil {
		t.Fatal(err)
	}
	writesBefore := deploymentplan.Digest(f.b.writes)
	if err := applyProductSteadyCleanup(ctx, f.cfg, f.store, f.e, f.l, installed, applyCleanup); err == nil || deploymentplan.Digest(f.b.writes) != writesBefore {
		t.Fatal("unknown delete replayed a native write")
	}
	if err := os.WriteFile(journalPath, originalJournal, 0600); err != nil {
		t.Fatal(err)
	}
	captureRetire = true
	if err := applyProductSteadyCleanup(ctx, f.cfg, f.store, f.e, f.l, installed, applyCleanup); err == nil {
		t.Fatal("accepted-child interruption absent")
	}
	entries = f.e.Journal.Entries[start:]
	if n, _, err := validateCreateNormalTailProductStagesWithConfig(f.cfg, f.store, f.e, f.l, entries); err != nil || n != len(entries) {
		t.Fatal("accepted-before-parent retirement prefix", n, len(entries), err)
	}
	if err := productPreparationsRetired(ctx, f.cfg, f.store, f.e, f.l, installed, boundary); err == nil {
		t.Fatal("allocated parent was treated as complete retirement")
	}
	if err := applyProductSteadyCleanup(ctx, f.cfg, f.store, f.e, f.l, installed, applyCleanup); err != nil {
		t.Fatal("same retirement child retry", err)
	}
	if err := productPreparationsRetired(ctx, f.cfg, f.store, f.e, f.l, installed, boundary); err != nil {
		t.Fatal(err)
	}
	for id, digest := range protected {
		if deploymentplan.Digest(f.b.objects[id]) != digest {
			t.Fatal("retirement changed persistent product state", id)
		}
	}
	for _, d := range prepared {
		if f.b.objects[deploymentplan.ID(d.Object)] != nil || f.b.writes[deploymentplan.ID(d.Object)] != 2 {
			t.Fatal("producer not retired exactly once", deploymentplan.ID(d.Object))
		}
	}
	final, err := compileCreateNormalTailProductTarget(ctx, compileCfg, f.h, &program, backend, true)
	if err != nil {
		t.Fatal("actual steady target compiler", err)
	}
	if err := final.Plan.Verify(ctx, boundary); err != nil {
		t.Fatal("steady full target verification", err)
	}
	if err := verifyProductIdentityOutputs(ctx, f.cfg, f.runtime, f.l, installed); err != nil {
		t.Fatal("permanent native identities after producer retirement", err)
	}
	finalizations := 0
	for path, count := range f.posts {
		if strings.HasSuffix(path, "/finalize") {
			finalizations += count
		}
	}
	if finalizations != 1 {
		t.Fatal("retirement recovery repeated native finalization", finalizations)
	}
	entries = f.e.Journal.Entries[start:]
	for end := 1; end <= len(entries); end++ {
		view := *f.e
		view.Journal = f.e.Journal
		view.Journal.Entries = f.e.Journal.Entries[:start+end]
		if n, _, err := validateCreateNormalTailProductStagesWithConfig(f.cfg, f.store, &view, f.l, entries[:end]); err != nil || n != end {
			t.Fatal("real persisted full-flow prefix", end, n, err)
		}
	}
	if err := executeCreateNormalTailSequence(ctx, f.e, []func(context.Context) error{func(ctx context.Context) error { return final.Plan.Verify(ctx, boundary) }}); err != nil {
		t.Fatal("original full-flow closure", err)
	}
	if f.e.Journal.CompletedAt.IsZero() {
		t.Fatal("original owner did not complete")
	}
}
