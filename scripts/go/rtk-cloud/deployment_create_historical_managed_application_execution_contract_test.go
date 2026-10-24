package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

// This is the original public configuration consumed by the historical serial
// producer. Changes are made before compilation/native writes/source capture.
// Each later recovery uses its actual maintained repair function against these
// immutable bytes; no source journal or private identity is changed afterward.
func historicalManagedApplicationRecipe(definitions []deploymentplan.Definition) []deploymentplan.Definition {
	out := make([]deploymentplan.Definition, len(definitions))
	for i, definition := range definitions {
		d := definition
		d.Object = deploymentplan.Clone(definition.Object)
		out[i] = d
		if d.Object["kind"] == "Secret" {
			data := deploymentplan.Map(d.Object["data"])
			if data == nil {
				data = deploymentplan.Object{}
			}
			for key, value := range deploymentplan.Map(d.Object["stringData"]) {
				data[key] = base64.StdEncoding.EncodeToString([]byte(deploymentplan.String(value)))
			}
			d.Object["data"] = data
			delete(d.Object, "stringData")
		}
		if d.Object["kind"] != "Deployment" {
			continue
		}
		name := deploymentplan.String(deploymentplan.Map(d.Object["metadata"])["name"])
		pod := environmentPKIPod(d.Object)
		domain := ""
		if name == "video-cloud-api-app-pki" {
			domain = "app"
		} else if name == "video-cloud-api-pki" {
			domain = "device"
		}
		for _, raw := range certIssuerObjectList(pod["containers"]) {
			container := deploymentplan.Map(raw)
			var settings, mounts []any
			for _, raw := range certIssuerObjectList(container["env"]) {
				setting := deploymentplan.Map(raw)
				if (name == "video-cloud-api" || domain != "") && setting["name"] == "VIDEO_CLOUD_CONTROLLER_SERVER_CRL_MANIFEST" {
					continue
				}
				if name == "video-cloud-api" || name == "video-cloud-logingester" {
					switch setting["value"] {
					case "/run/pki-mqtt-crls/crls.json":
						setting["value"] = "/run/pki-mqtt/crls.json"
					case "/run/pki-service-crls/outbound-crls.json":
						setting["value"] = environmentCreateServiceTrust + "/outbound-crls.json"
					}
				}
				if name == "video-cloud-api-app-pki" && setting["name"] == "VIDEO_CLOUD_AUTH_PRODUCT_PKI_REQUIRE_CRLS" {
					setting["value"] = "true"
				}
				if domain != "" && setting["name"] == "VIDEO_CLOUD_AUTH_ISSUER_TRUST_MANIFEST" {
					setting["value"] = "/run/pki-" + domain + "/bundles.json"
				}
				settings = append(settings, setting)
			}
			container["env"] = settings
			for _, raw := range certIssuerObjectList(container["volumeMounts"]) {
				mount := deploymentplan.Map(raw)
				if domain != "" && mount["name"] == domain+"-trust-manifest" {
					continue
				}
				if name == "video-cloud-api" || name == "video-cloud-logingester" {
					switch mount["mountPath"] {
					case "/run/pki-mqtt-crls/crls.json":
						mount["mountPath"] = "/run/pki-mqtt/crls.json"
					case "/run/pki-service-crls/outbound-crls.json":
						mount["mountPath"] = environmentCreateServiceTrust + "/outbound-crls.json"
					}
				}
				mounts = append(mounts, mount)
			}
			container["volumeMounts"] = mounts
		}
		if domain != "" {
			var volumes []any
			for _, raw := range certIssuerObjectList(pod["volumes"]) {
				if deploymentplan.Map(raw)["name"] != domain+"-trust-manifest" {
					volumes = append(volumes, raw)
				}
			}
			pod["volumes"] = volumes
		}
	}
	return out
}

func appendHistoricalManagedApplicationOriginal(t *testing.T, f *acceptedCoreExecutionContract, r *createServiceSessionRecovery) *createManagedApplicationPartialRecovery {
	t.Helper()
	d := r.ManagedTransportRecovery
	roots := map[string]environmentCreateRegisteredIssuer{"service": r.Ceremony.Root, "device": d.Device, "app": d.AppRoot, "mqtt": *r.MQTTRoot, "openbao_tls": *r.OpenBaoRoot}
	intermediates := map[string]environmentCreateRegisteredIssuer{"service": r.Ceremony.Intermediate, "app": d.AppIntermediate, "mqtt": *r.MQTTIntermediate, "openbao_tls": *r.OpenBaoIntermediate}
	runtime := f.runtime
	runtime.Backend = f.backend
	runtime.ServiceSessionRecovery = r
	originalVolumes, err := readEnvironmentCreateLedger(f.e)
	if err != nil || len(originalVolumes.Volumes) != 16 {
		t.Fatal("original pre-application volume cohort must contain exactly sixteen owners", err)
	}
	initial, err := renderEnvironmentCreateApplicationOwners(t.Context(), f.cfg, f.p.Plan.Bootstrap, f.p.Plan.Release, f.store, f.operator, r.Ceremony)
	if err != nil {
		t.Fatal("original managed application owners", err)
	}
	definitions, err := finalizeEnvironmentCreateApplicationDefinitions(t.Context(), f.cfg, f.p.Plan.Bootstrap, f.p.Plan.Release, runtime, r.Ceremony, roots, intermediates, initial)
	if err != nil {
		t.Fatal("original managed application finalization", err)
	}
	definitions = historicalManagedApplicationRecipe(definitions)
	plan, journal := f.historicalSerialInterruptedStage(t, "kubernetes/managed-application-owners", definitions, "Deployment/"+f.p.Plan.Stack+"-video-cloud/video-cloud-api")
	if err := validateManagedApplicationPartialJournal(plan, journal); err != nil {
		logHistoricalApplicationMatrix(t, plan, journal)
		t.Fatalf("actual original managed application boundary: %v; completed=%d owners=%d resources=%d", err, len(journal.Completed), len(journal.Owners), len(plan.Resources))
	}
	// These two claims are actual writes of this child. Model the storage
	// controller binding only after they exist, then capture the real expanded
	// ledger without changing any original allocation or historical receipt.
	currentVolumes := historicalTransportCaptureVolumes(t, f)
	if len(currentVolumes.Volumes) != 18 {
		t.Fatal("actual application claims did not extend sixteen owners to eighteen")
	}
	for _, original := range originalVolumes.Volumes {
		id := "PersistentVolumeClaim/" + original.Namespace + "/" + original.PVCName
		current, err := managedTransportRecordedHostVolume(currentVolumes, id)
		if err != nil || current != original {
			t.Fatal("application stage replaced an original durable allocation", id, err)
		}
	}
	f.protocol["objects"] = historicalNativeInventory(f.native.objects)
	f.publish(t)
	before := deploymentplan.Digest(f.e.Journal)
	p := f.p
	p.Materials = map[string]string{}
	derived, err := deriveManagedApplicationPartialRecovery(f.cfg, f.store, f.e, &p)
	if err != nil {
		t.Fatal("complete original managed application source", err)
	}
	if derived.ManagedApplicationPartial == nil || derived.ManagedApplicationPartial.Plan.SHA256() != plan.SHA256() || deploymentplan.Digest(f.e.Journal) != before {
		t.Fatal("original source derivation changed accepted history")
	}
	for _, name := range []string{"video-cloud-api-pki", "video-cloud-api-app-pki"} {
		claimID := "PersistentVolumeClaim/" + f.p.Plan.Stack + "-video-cloud/" + environmentManagedApplicationClaim(name)
		owner, ownerErr := managedTransportRecordedHostVolume(currentVolumes, claimID)
		proof := derived.ManagedApplicationPartial.Exports[name]
		claim, claimErr := f.backend.Read(t.Context(), claimID)
		volume, volumeErr := f.backend.Read(t.Context(), "PersistentVolume//"+owner.PVName)
		if ownerErr != nil || proof == nil || owner.PVCUID != proof.ClaimUID || claimErr != nil || claim == nil || volumeErr != nil || volume == nil || validateManagedTransportHostVolume(owner, claim, volume) != nil {
			t.Fatal("original managed application export lost its native private volume", name, ownerErr, claimErr, volumeErr)
		}
	}
	f.p = p
	return derived.ManagedApplicationPartial
}

// Synthetic historical admissions are explicit test inputs. The production
// loader authenticates each captured source/preview and every child below is
// produced by actual compilation, native writes/readback and the parent fence.
func appendHistoricalManagedApplicationForward(t *testing.T, f *acceptedCoreExecutionContract, partial *createManagedApplicationPartialRecovery) *createManagedApplicationInterruptedForward {
	t.Helper()
	previous := partial.Interrupted
	schema := "rtk-cloud.create-managed-application-partial-recovery/v1"
	if previous != nil {
		schema = "rtk-cloud.create-managed-application-interrupted-recovery/v1"
		if previous.Previous != nil {
			schema = "rtk-cloud.create-managed-application-app-role-recovery/v1"
		}
	}
	definitions, err := managedApplicationPartialDefinitionsWithRepairs(partial, previous != nil, previous != nil && previous.Previous != nil)
	if err != nil {
		t.Fatal("historical application forward recipe", err)
	}
	var live []deploymentplan.Object
	for _, object := range f.native.objects {
		live = append(live, object)
	}
	preview, err := deploymentplan.Compile(deploymentplan.Inputs{Environment: f.cfg.Environment, Stack: f.p.Plan.Stack, Operation: "scoped", ConfigurationSHA256: f.p.Plan.ConfigurationSHA256, ReleaseSHA256: deploymentplan.Digest(f.p.Plan.Release), BindingsSHA256: environmentCreateStageBindingsSHA("managed-application-owners-forward", definitions)}, definitions, live)
	if err != nil {
		t.Fatal("historical application candidate", err)
	}
	if err := validateManagedApplicationCandidateWithRepairs(partial, preview, previous != nil, previous != nil && previous.Previous != nil); err != nil {
		t.Fatal("historical application candidate scope", err)
	}
	materials := map[string]string{"accepted-service-core-managed-application-plan": partial.Plan.SHA256(), "accepted-service-core-managed-application-journal": deploymentplan.Digest(partial.Journal), "accepted-service-core-managed-application-source": partial.SourceSHA, "accepted-service-core-managed-application-admission": partial.ArtifactSHA, "accepted-service-core-managed-application-forward-plan": preview.SHA256()}
	if previous != nil {
		materials["accepted-service-core-managed-application-interrupted-plan"] = previous.Plan.SHA256()
		materials["accepted-service-core-managed-application-interrupted-journal"] = deploymentplan.Digest(previous.Journal)
		materials["accepted-service-core-managed-application-interrupted-source"] = previous.SourceSHA
		materials["accepted-service-core-managed-application-interrupted-admission"] = previous.ArtifactSHA
		owner := f.e.Journal.KubernetesLeases[len(f.e.Journal.KubernetesLeases)-1]
		actual, err := f.native.Read(t.Context(), owner.ResourceID)
		if err != nil || validateManagedApplicationInterruptedLease(previous.Plan, owner, actual) != nil {
			t.Fatal("original interrupted native lease", err)
		}
		previous.LeaseSHA = deploymentplan.Digest(actual)
		materials[managedApplicationInterruptedLeaseMaterial] = previous.LeaseSHA
	}
	review, _ := f.admitRecovery(t, schema, materials)
	f.write(t, filepath.Join("deployment/create/openbao-recovery", review, "openbao-root-trust-plan.json"), preview)
	if previous != nil {
		runtime := f.runtime
		runtime.Backend = f.backend
		runtime.ServiceSessionRecovery = &createServiceSessionRecovery{Review: review, ManagedApplicationPartial: partial}
		if err := releaseManagedApplicationInterruptedLeaseWithBackend(context.Background(), runtime, historicalNativeLeaseAPI{f.native}); err != nil {
			t.Fatal("actual recorded lease release", err)
		}
	}
	failed := "Deployment/" + f.p.Plan.Stack + "-video-cloud/video-cloud-api"
	if previous != nil {
		failed += "-app-pki"
	}
	plan, journal := f.historicalSerialInterruptedStage(t, "kubernetes/managed-application-owners-forward-"+review, definitions, failed, previous == nil)
	f.protocol["objects"] = historicalNativeInventory(f.native.objects)
	f.publish(t)
	before := deploymentplan.Digest(f.e.Journal)
	_, _, forward, err := loadManagedApplicationInterruptedSource(f.store, f.e)
	if err != nil || forward.Plan.SHA256() != plan.SHA256() || deploymentplan.Digest(forward.Journal) != deploymentplan.Digest(journal) || deploymentplan.Digest(f.e.Journal) != before {
		t.Fatal("complete immutable application forward source", err)
	}
	partial.Interrupted = forward
	return forward
}

// This connects the genuine transport/API prefix to all three documented
// application interruption generations. Admissions are isolated historical
// inputs; source loaders, lease release and native stage execution stay real.
func newHistoricalManagedApplicationExecutionContract(t *testing.T, hooks ...coreExecutionContractHooks) (*deviceTrustExecutionContract, *createServiceSessionRecovery) {
	t.Helper()
	if len(hooks) > 1 {
		t.Fatal("one original Core lifecycle selection required")
	}
	var selected *coreExecutionContractHooks
	if len(hooks) == 1 {
		selected = &hooks[0]
	}
	device, r := newHistoricalPostManagedExecutionContractWithHooks(t, selected)
	f := device.acceptedCoreExecutionContract
	partial := appendHistoricalManagedApplicationOriginal(t, f, r)
	r.ManagedApplicationPartial = partial
	r.ManagedTransportRecovery.Partial = partial.Historical
	// API exports exist before the application interruption descriptor. Bind
	// their actual native CSR/certificate rows only after the complete partial
	// supplies both exported-chain plans, and before any historical admission
	// freezes the source materials consumed by Core, MQTT and TLS recovery.
	if f.p.Materials == nil {
		f.p.Materials = map[string]string{}
	}
	if err := verifyManagedApplicationCurrentAPIServers(t.Context(), f.cfg, f.e, f.backend, r, f.p.Materials); err != nil {
		t.Fatal("original application source native API CSR/certificates", err)
	}
	for generation := 0; generation < 3; generation++ {
		appendHistoricalManagedApplicationForward(t, f, partial)
		t.Logf("actual application interruption generation %d authenticated", generation+1)
	}
	f.runtime.ServiceSessionRecovery = r
	assertHistoricalManagedApplicationSource(t, device, r)
	return device, r
}

func assertHistoricalManagedApplicationSource(t *testing.T, device *deviceTrustExecutionContract, r *createServiceSessionRecovery) {
	t.Helper()
	f := device.acceptedCoreExecutionContract
	partial := r.ManagedApplicationPartial
	if partial.Interrupted == nil || partial.Interrupted.Previous == nil || partial.Interrupted.Previous.Previous == nil || partial.Interrupted.Previous.Previous.Previous != nil {
		t.Fatal("original application source lost one of its three exact interrupted generations")
	}
	ledger, err := readEnvironmentCreateLedger(f.e)
	if err != nil || len(ledger.Volumes) != 18 {
		t.Fatal("native application history lacks all eighteen retained volumes", err)
	}
	before := createBootstrapStateSnapshot(t, f.store.Root)
	writes := deploymentplan.Digest(f.native.writes)
	_, _, loaded, err := loadManagedApplicationInterruptedSource(f.store, f.e)
	if err != nil || deploymentplan.Digest(loaded) != deploymentplan.Digest(partial.Interrupted) {
		t.Fatal("independent original application history read differs", err)
	}
	assertRecoveryContractReadOnly(t, f.store, before)
	if deploymentplan.Digest(f.native.writes) != writes {
		t.Fatal("source qualification wrote native application owners")
	}
}

// The first application source records the exact API CSR/certificate digest
// that every later Core, MQTT and TLS recovery must re-read from native SQL.
// Check it before the expensive expired-Core continuation so drift fails at its
// producer rather than twenty minutes later in a CRL adapter.
func TestHistoricalManagedApplicationSourceRetainsFreshNativeAPIPin(t *testing.T) {
	hooks := historicalCompleteOriginalSessionHooks(t)
	device, r := newHistoricalManagedApplicationExecutionContract(t, hooks)
	f := device.acceptedCoreExecutionContract
	expected := f.p.Materials[managedLateAPINativeMaterial]
	materials := map[string]string{}
	if err := verifyManagedApplicationCurrentAPIServers(t.Context(), f.cfg, f.e, f.backend, r, materials); err != nil {
		t.Fatal("historical application source cannot re-read its native API CSR/certificates", err)
	}
	if err := bindManagedLateFreshAPIServers(materials, expected); err != nil {
		t.Fatal("historical application source did not preserve its original native API digest", err)
	}
	generation, configurationSHA, err := loadEnvironmentCreateAdmittedGeneration(f.store, f.e.Journal.PlanSHA256, f.e.Journal.ArtifactSHA256)
	if err != nil {
		t.Fatal("historical application source cannot resolve its immutable admitted create qualification", err)
	}
	if !environmentCreateGenerationMatchesAdmitted(generation, f.p.Plan) {
		t.Fatalf("historical application generation drifted from immutable admission: admitted=%s current=%s", deploymentplan.Digest(generation), deploymentplan.Digest(f.p.Plan))
	}
	if configurationSHA != deploymentplan.Digest(f.cfg) {
		t.Fatalf("historical application configuration drifted from immutable admission: admitted=%s current=%s plan=%s", configurationSHA, deploymentplan.Digest(f.cfg), f.p.Plan.ConfigurationSHA256)
	}
}

func TestHistoricalOriginalAdmissionBindsSelectedGenerationAndConfiguration(t *testing.T) {
	hooks := historicalCompleteOriginalSessionHooks(t)
	f := newCoreExecutionContractAtBoundaryWithHooks(t, false, true, false, hooks, historicalCompleteDomainPolicy(t))
	generation, configurationSHA, err := loadEnvironmentCreateAdmittedGeneration(f.store, f.e.Journal.PlanSHA256, f.e.Journal.ArtifactSHA256)
	if err != nil {
		t.Fatal(err)
	}
	if !environmentCreateGenerationMatchesAdmitted(generation, f.p.Plan) {
		t.Fatalf("accepted Core generation differs from original admission: admitted=%s current=%s", deploymentplan.Digest(generation), deploymentplan.Digest(f.p.Plan))
	}
	if configurationSHA != deploymentplan.Digest(f.cfg) {
		t.Fatalf("accepted Core configuration differs from original admission: admitted=%s current=%s plan=%s", configurationSHA, deploymentplan.Digest(f.cfg), f.p.Plan.ConfigurationSHA256)
	}
}

// This bounded renderer test authenticates the complete original resource
// recipe before the expensive history producer captures its first Apps child.
// It uses real renderers and models only external native UIDs; no accepted
// parent or child receipt is synthesized by this fixture.
func TestHistoricalManagedApplicationRecipeKeepsCompleteRoutingBeforeSourceCapture(t *testing.T) {
	policy := historicalCompleteDomainPolicy(t)
	cfg, p, release, runtime, service, roots, intermediates, _ := createApplicationFinalizeFixture(t, "dev")
	if p.AppRoutingMode != "" {
		t.Fatal("narrow fixture default unexpectedly changed")
	}
	policy(&p)
	if p.AppRoutingMode != "internal-service" {
		t.Fatal("full historical source omitted maintained routing mode")
	}
	store := runtime.Backend.Store
	for _, entry := range rtkSecretCatalog() {
		path := filepath.Join("runtime", entry.ID)
		if _, err := store.read(path); err == nil {
			continue
		}
		if err := store.write(path, []byte("isolated-original-apps-"+entry.ID+"\n"), false); err != nil {
			t.Fatal(err)
		}
	}
	initial, err := renderEnvironmentCreateApplicationOwners(t.Context(), cfg, p, release, store, nil, service)
	if err != nil {
		t.Fatal(err)
	}
	final, err := finalizeEnvironmentCreateApplicationDefinitions(t.Context(), cfg, p, release, runtime, service, roots, intermediates, initial)
	if err != nil {
		t.Fatal(err)
	}
	final = historicalManagedApplicationRecipe(final)
	input := deploymentplan.Inputs{Environment: cfg.Environment, Stack: p.Stack, Operation: "scoped", ConfigurationSHA256: strings.Repeat("a", 64), ReleaseSHA256: deploymentplan.Digest(release), BindingsSHA256: environmentCreateStageBindingsSHA("managed-application-owners", final)}
	fresh, err := deploymentplan.Compile(input, final, nil)
	if err != nil {
		t.Fatal(err)
	}
	ids := make([]string, len(fresh.Resources))
	for i, r := range fresh.Resources {
		ids[i] = r.ID
	}
	if !slices.Equal(ids, managedApplicationPartialResourceIDs(p.Stack+"-video-cloud")) {
		t.Fatalf("original Apps resource catalog differs: %v", ids)
	}
	// A missing mode reproduces precisely the lost internal API Service and
	// NetworkPolicy; the complete policy must restore both as real create actions.
	narrow := p
	narrow.AppRoutingMode = ""
	incomplete, err := finalizeEnvironmentCreateApplicationDefinitions(t.Context(), cfg, narrow, release, runtime, service, roots, intermediates, initial)
	if err != nil {
		t.Fatal(err)
	}
	if len(incomplete) != 24 {
		t.Fatalf("missing routing reproduction has %d resources", len(incomplete))
	}
	for _, id := range []string{"NetworkPolicy/" + p.Stack + "-video-cloud/allow-video-cloud-api-app-pki", "Service/" + p.Stack + "-video-cloud/video-cloud-api-app-pki"} {
		found := false
		for _, resource := range fresh.Resources {
			if resource.ID == id {
				found = true
				if resource.Action != "create" || resource.Phase != "configuration" {
					t.Fatal("original routing resource is not an actual new configuration owner", id)
				}
			}
		}
		if !found {
			t.Fatal("original routing owner omitted", id)
		}
	}
	// The complete real initial owner catalog remains unchanged by either render.
	again, err := renderEnvironmentCreateApplicationOwners(t.Context(), cfg, p, release, store, nil, service)
	if err != nil || deploymentplan.Digest(again) != deploymentplan.Digest(initial) {
		t.Fatal("historical finalization modified original enrollment inputs", err)
	}
}

func logHistoricalApplicationMatrix(t *testing.T, plan *deploymentplan.Plan, journal deploymentplan.Journal) {
	t.Helper()
	for i, r := range plan.Resources {
		t.Logf("Apps resource[%d] id=%s action=%s phase=%s uid=%t baseline=%t owner=%t completed=%t dependencies=%v", i, r.ID, r.Action, r.Phase, r.UID != "", r.Baseline != nil, journal.Owners[r.ID] != "", slices.Contains(journal.Completed, r.ID), r.Dependencies)
		if r.Desired["kind"] == "Service" && r.Baseline != nil {
			t.Logf("Apps public Service desired=%s baseline=%s", mustProviderRecoveryJSON(r.Desired), mustProviderRecoveryJSON(r.Baseline))
		}
	}
}

func TestHistoricalManagedApplicationOriginalNativeActionMatrix(t *testing.T) {
	f, roots, intermediates, _ := newHistoricalTransportCoordinatorExecutionContract(t)
	p, runtime, service := f.p.Plan.Bootstrap, f.runtime, f.service
	initial, err := renderEnvironmentCreateApplicationOwners(t.Context(), f.cfg, p, f.p.Plan.Release, f.store, f.operator, service)
	if err != nil {
		t.Fatal(err)
	}
	before := initial
	before, err = configureEnvironmentCreateMQTTConsumers(p, service, roots["mqtt"], []environmentCreateRegisteredIssuer{intermediates["mqtt"]}, before)
	if err != nil {
		t.Fatal(err)
	}
	var readyDocument deploymentplan.Object
	if err := readExecutionJSON(filepath.Join(f.store.Root, "deployment/create", f.e.Journal.PlanSHA256, "bootstrap/device/ready.json"), &readyDocument); err != nil {
		t.Fatal(err)
	}
	deviceReady, err := environmentCreateRegisteredIssuerFromMap(readyDocument, "")
	if err != nil || deviceReady.ID != roots["device"].ID || deviceReady.RootFingerprint != roots["device"].RootFingerprint {
		t.Fatal("original Device READY lineage differs from the activated authority", err)
	}
	before, err = configureEnvironmentCreateDeviceConsumers(f.cfg, p, deviceReady, before)
	if err != nil {
		t.Fatal(err)
	}
	before, err = configureEnvironmentCreateAppConsumers(p, roots["app"], []environmentCreateRegisteredIssuer{intermediates["app"]}, before)
	if err != nil {
		t.Fatal(err)
	}
	before = environmentCreateApplicationDefinitionsFor(before, "video-cloud-api", "video-cloud-logingester", "video-cloud-api-pki", "video-cloud-api-app-pki", "mqtt-pki")
	for _, definition := range before {
		if deploymentplan.ID(definition.Object) == "ConfigMap/"+p.Stack+"-video-cloud/pki-create-mqtt-consumer-trust" {
			deploymentplan.Map(definition.Object["metadata"])["annotations"] = deploymentplan.Object{"isolated.example/original-note": "preserve"}
		}
	}
	f.acceptedHistoricalStage(t, "isolated-pre-apps-native-owners", before)
	apply := historicalTransportSessionApply(t, f, &createServiceSessionRecovery{}, "", managedSessionState{})
	if err := executeEnvironmentCreateManagedTransports(t.Context(), f.cfg, p, f.p.Plan.Release, runtime, service, roots, intermediates, apply); err != nil {
		t.Fatal("actual prior transport owners", err)
	}
	definitions, err := finalizeEnvironmentCreateApplicationDefinitions(t.Context(), f.cfg, p, f.p.Plan.Release, runtime, service, roots, intermediates, initial)
	if err != nil {
		t.Fatal(err)
	}
	definitions = historicalManagedApplicationRecipe(definitions)
	mqttID := "ConfigMap/" + p.Stack + "-video-cloud/pki-create-mqtt-consumer-trust"
	mqttWrites, mqttBody := f.native.writes[mqttID], deploymentplan.Digest(f.native.objects[mqttID])
	plan, journal := f.historicalSerialInterruptedStage(t, "kubernetes/managed-application-owners", definitions, "Deployment/"+p.Stack+"-video-cloud/video-cloud-api")
	mqtt := plan.Resources[7]
	if mqtt.Action != "retain" || !deploymentplan.Matches(mqtt.Desired, mqtt.Baseline) || mqtt.UID != journal.Owners[mqtt.ID] {
		t.Fatal("unchanged maintained MQTT Root+ICA trust was rewritten or lost its original owner")
	}
	t.Logf("maintained MQTT intermediate and final Apps ConfigMap projections match; action=%s, original owner retained=%t", mqtt.Action, mqtt.UID == journal.Owners[mqtt.ID])
	if err := validateManagedApplicationPartialJournal(plan, journal); err != nil {
		logHistoricalApplicationMatrix(t, plan, journal)
		t.Fatal("complete real original Apps action/phase/owner matrix", err)
	}
	if f.native.writes[mqttID] != mqttWrites || deploymentplan.Digest(f.native.objects[mqttID]) != mqttBody {
		t.Fatal("Apps replayed or changed the already complete MQTT ConfigMap")
	}
	legacy, err := configureEnvironmentCreateMQTTConsumers(p, service, roots["mqtt"], nil, initial)
	if err != nil {
		t.Fatal(err)
	}
	var legacyData deploymentplan.Object
	for _, definition := range legacy {
		if deploymentplan.ID(definition.Object) == mqttID {
			legacyData = deploymentplan.Map(definition.Object["data"])
		}
	}
	if legacyData == nil {
		t.Fatal("actual original Root-only producer omitted its ConfigMap")
	}
	for _, mode := range []string{"original-update", "unchanged-retain", "changed-data", "changed-desired-data", "foreign-baseline-id", "foreign-baseline-owner", "foreign-resource-owner", "foreign-journal-owner", "wrong-resource", "other-resource-retain", "wrong-phase", "missing-baseline", "changed-plan-hash"} {
		t.Run(mode, func(t *testing.T) {
			var changed deploymentplan.Plan
			var recorded deploymentplan.Journal
			if json.Unmarshal(mustProviderRecoveryJSON(plan), &changed) != nil || json.Unmarshal(mustProviderRecoveryJSON(journal), &recorded) != nil {
				t.Fatal("cloning actual native plan/journal")
			}
			r := &changed.Resources[7]
			switch mode {
			case "original-update":
				r.Action = "update"
				r.Baseline["data"] = deploymentplan.Clone(legacyData)
			case "changed-data":
				deploymentplan.Map(r.Baseline["data"])["crls.json"] = "[]"
			case "changed-desired-data":
				deploymentplan.Map(r.Desired["data"])["roots.pem"] = "foreign root"
			case "foreign-baseline-id":
				deploymentplan.Map(r.Baseline["metadata"])["name"] = "foreign"
			case "foreign-baseline-owner":
				deploymentplan.Map(r.Baseline["metadata"])["uid"] = "99999999-1111-4111-8111-111111111111"
			case "foreign-resource-owner":
				r.UID = "99999999-1111-4111-8111-111111111111"
			case "foreign-journal-owner":
				recorded.Owners[r.ID] = "99999999-1111-4111-8111-111111111111"
			case "wrong-resource":
				r.ID = "ConfigMap/" + p.Stack + "-video-cloud/foreign"
			case "other-resource-retain":
				changed.Resources[4].Action = "retain"
			case "wrong-phase":
				r.Phase = "workload"
			case "missing-baseline":
				r.Baseline = nil
			}
			recorded.PlanSHA256 = changed.SHA256()
			if mode == "changed-plan-hash" {
				recorded.PlanSHA256 = strings.Repeat("f", 64)
			}
			before := deploymentplan.Digest([]any{changed, recorded})
			accepted := validateManagedApplicationPartialJournal(&changed, recorded) == nil
			if accepted != (mode == "original-update" || mode == "unchanged-retain") || deploymentplan.Digest([]any{changed, recorded}) != before {
				t.Fatal("partial validator changed its exact source/owner/body boundary", mode, accepted)
			}
		})
	}
	byID := map[string]deploymentplan.Definition{}
	for _, d := range definitions {
		byID[deploymentplan.ID(d.Object)] = d
	}
	for _, r := range plan.Resources {
		if !slices.Equal(r.Dependencies, byID[r.ID].Dependencies) {
			t.Fatal("compiler changed actual source dependencies", r.ID)
		}
		for _, id := range r.Dependencies {
			if _, ok := byID[id]; !ok {
				if f.native.objects[id] == nil {
					t.Fatal("source dependency is neither an actual source resource nor existing native owner", r.ID, id)
				}
			}
		}
	}
}

func TestHistoricalNativeServiceDefaultKeepsOriginalExplicitType(t *testing.T) {
	for _, declared := range []string{"", "ClusterIP", "NodePort"} {
		t.Run("type="+declared, func(t *testing.T) {
			_, e, _, fixture := freshStageFixture(t)
			b := fixture.normalTailE2EBackend
			b.defaultNativeServices = true
			body := deploymentplan.Object{"apiVersion": "v1", "kind": "Service", "metadata": deploymentplan.Object{"name": "original-service", "namespace": e.Journal.Stack + "-video-cloud", "labels": deploymentplan.Object{"rtk.realtek.com/stack": e.Journal.Stack, "app.kubernetes.io/managed-by": "rtk-cloud"}}, "spec": deploymentplan.Object{"selector": deploymentplan.Object{"app.kubernetes.io/name": "original"}, "ports": []any{deploymentplan.Object{"name": "https", "port": float64(18445), "targetPort": float64(18445)}}}}
			if declared != "" {
				deploymentplan.Map(body["spec"])["type"] = declared
			}
			original := deploymentplan.Digest(body)
			dry, err := b.Write(t.Context(), "create", body, true)
			if err != nil || len(b.objects) != 0 || len(b.writes) != 0 {
				t.Fatal("native dry run changed state", err)
			}
			actual, err := b.Write(t.Context(), "create", body, false)
			expected := declared
			if expected == "" {
				expected = "ClusterIP"
			}
			if err != nil || deploymentplan.Map(actual["spec"])["type"] != expected || deploymentplan.Map(dry["spec"])["type"] != expected || deploymentplan.Digest(body) != original {
				t.Fatal("native Service default replaced an explicit type or original input", err)
			}
			desired := deploymentplan.Clone(body)
			deploymentplan.Map(desired["spec"])["type"] = "ClusterIP"
			plan, err := deploymentplan.Compile(deploymentplan.Inputs{Environment: e.Journal.Environment, Stack: e.Journal.Stack, Operation: "scoped", ConfigurationSHA256: strings.Repeat("a", 64), ReleaseSHA256: strings.Repeat("b", 64), BindingsSHA256: deploymentplan.Digest(desired)}, []deploymentplan.Definition{{Phase: "configuration", Object: desired}}, []deploymentplan.Object{actual})
			if err != nil {
				t.Fatal(err)
			}
			want := "retain"
			if declared == "NodePort" {
				want = "update"
			}
			if plan.Resources[0].Action != want || plan.Resources[0].UID != deploymentplan.Map(actual["metadata"])["uid"] {
				t.Fatal("original native default changed ownership or masked a real type change")
			}
		})
	}
}
