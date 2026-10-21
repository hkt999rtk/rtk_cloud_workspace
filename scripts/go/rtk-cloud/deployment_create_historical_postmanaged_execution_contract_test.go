package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

// Capture the exact native ledger pointer and six retained host-volume owners
// before the admission is frozen. Later application recovery may authenticate
// these values, but it must never reconstruct or append them to an older
// admission after the source has advanced.
func historicalOriginalVolumeAdmissionMaterials(t *testing.T, f *acceptedCoreExecutionContract, ledger environmentCreateResourceLedger) map[string]string {
	t.Helper()
	var active managedTransportVolumeLedgerActive
	if readExecutionJSON(filepath.Join(f.store.Root, "deployment/resources/active.json"), &active) != nil || validateManagedTransportVolumeLedger(ledger, active) != nil {
		t.Fatal("original native volume ledger pointer differs")
	}
	materials := map[string]string{
		"accepted-service-core-managed-partial-original-volume-ledger":        active.LedgerSHA,
		"accepted-service-core-managed-partial-original-volume-ledger-active": deploymentplan.Digest(active),
	}
	for _, id := range []string{
		"PersistentVolumeClaim/" + f.p.Plan.Stack + "-video-cloud/mqtt-pki-data",
		"PersistentVolumeClaim/" + f.p.Plan.Stack + "-video-cloud/mqtt-pki-trust",
		"PersistentVolumeClaim/" + f.p.Plan.Stack + "-video-cloud/mqtt-pki-host-identity",
		"PersistentVolumeClaim/" + f.p.Plan.Stack + "-secrets/openbao-pki-identity",
		"PersistentVolumeClaim/" + f.p.Plan.Stack + "-secrets/data-openbao-0",
		"PersistentVolumeClaim/" + f.p.Plan.Stack + "-secrets/audit-openbao-0",
	} {
		owner, err := managedTransportRecordedHostVolume(ledger, id)
		if err != nil {
			t.Fatal("original native volume owner missing", id, err)
		}
		claim, claimErr := f.backend.Read(t.Context(), id)
		volume, volumeErr := f.backend.Read(t.Context(), "PersistentVolume//"+owner.PVName)
		if claimErr != nil || volumeErr != nil || validateManagedTransportHostVolume(owner, claim, volume) != nil {
			t.Fatal("original native volume owner differs", id, claimErr, volumeErr)
		}
		materials["accepted-service-core-managed-partial-original-volume-"+id] = deploymentplan.Digest(owner)
	}
	return materials
}

// Return both actual API serving exports and the authentic pending-retirement
// recovery history immediately before the first managed application stage.
func advanceHistoricalPostManagedAPI(t *testing.T, f *acceptedCoreExecutionContract, r *createServiceSessionRecovery, sourceChecks ...func()) {
	t.Helper()
	partial := r.ManagedTransportRecovery.Partial
	identities := map[string]historicalAPIIdentity{}
	for _, name := range []string{"video-cloud-api-pki", "video-cloud-api-app-pki"} {
		identities[name] = historicalAPIServerIdentity(t, f, r.Ceremony, name)
	}
	installHistoricalAPIExportProtocol(t, f, identities)
	apply := historicalAPIApply(t, f)
	stop := errors.New("isolated completed first API custody boundary")
	runtime := f.runtime
	runtime.ServiceSessionRecovery = r
	err := publishEnvironmentCreateApplicationServers(t.Context(), f.cfg, f.p.Plan.Bootstrap, runtime, r.Ceremony, func(ctx context.Context, stage string, defs []deploymentplan.Definition) error {
		if err := apply(ctx, stage, defs); err != nil {
			return err
		}
		if stage == "server-custody-video-cloud-api-pki" {
			return stop
		}
		return nil
	})
	if !errors.Is(err, stop) {
		t.Fatal("first API custody interruption", err)
	}
	_, _, accepted, post, err := loadManagedTransportPostManaged(f.store, f.e)
	if err != nil || post == nil {
		t.Fatal("actual PostManaged source", err)
	}
	partial.AcceptedForward, partial.PostManaged = accepted, post
	if err := verifyManagedTransportPostManagedExportOwners(t.Context(), f.cfg, f.e, f.backend, post); err != nil {
		t.Fatal("actual retained first API native owners", err)
	}
	for _, check := range sourceChecks {
		check()
	}
	_, native, err := verifyManagedTransportOpenBaoIdentity(t.Context(), f.cfg, f.store, f.e, f.backend, r, false)
	if err != nil {
		t.Fatal("actual retained managed native proof", err)
	}
	ledger, err := readEnvironmentCreateLedger(f.e)
	if err != nil {
		t.Fatal(err)
	}
	publication, err := os.ReadFile(filepath.Join(f.store.Root, "pki/service/issuer.json"))
	if err != nil {
		t.Fatal(err)
	}
	var defs []deploymentplan.Definition
	for _, resource := range post.Plan.Resources {
		defs = append(defs, deploymentplan.Definition{Phase: resource.Phase, Object: deploymentplan.Clone(resource.Desired), Dependencies: resource.Dependencies})
	}
	candidate := historicalTransportHostPreview(t, f, defs)
	materials := historicalOriginalVolumeAdmissionMaterials(t, f, ledger)
	materials["accepted-service-core-managed-partial-original-plan"] = partial.Original.SHA256()
	materials["accepted-service-core-managed-partial-forward-plan"] = candidate.SHA256()
	materials["accepted-service-core-managed-partial-native-proof"] = deploymentplan.Digest(native)
	materials["accepted-service-core-managed-published-service"] = environmentPreflightBytesSHA256(publication)
	admit := func() {
		r.Review, _ = f.admitRecovery(t, "rtk-cloud.create-managed-transports-partial-recovery/v1", materials)
		r.Candidate = candidate
		f.write(t, filepath.Join("deployment/create/openbao-recovery", r.Review, "openbao-root-trust-plan.json"), candidate)
	}
	admit()
	stopped := errors.New("isolated original retirement before native deletion")
	err = publishEnvironmentCreateApplicationServers(t.Context(), f.cfg, f.p.Plan.Bootstrap, runtime, r.Ceremony, func(ctx context.Context, stage string, defs []deploymentplan.Definition) error {
		if strings.HasPrefix(stage, "retire-pki-export-video-cloud-api-pki-") {
			return stopped
		}
		return apply(ctx, stage, defs)
	})
	if !errors.Is(err, stopped) {
		t.Fatal("actual original completed export/pending retirement", err)
	}
	_, _, _, post, err = loadManagedTransportExportRetirement(f.store, f.e)
	if err != nil || post == nil || post.ExportRetire == nil {
		t.Fatal("actual export-retirement source", err)
	}
	partial.PostManaged = post
	if err := verifyManagedTransportPostManagedExportOwners(t.Context(), f.cfg, f.e, f.backend, post); err != nil {
		t.Fatal("retained export body and native private owner", err)
	}
	admit()
	if err := publishEnvironmentCreateApplicationServers(t.Context(), f.cfg, f.p.Plan.Bootstrap, runtime, r.Ceremony, apply); err != nil {
		t.Fatal("actual retained export retirement and second API producer", err)
	}
	for _, name := range []string{"video-cloud-api-pki", "video-cloud-api-app-pki"} {
		p, j, err := acceptedCreateStage(f.store, f.e, "kubernetes/server-secret-"+name)
		if err != nil || len(p.Resources) != 1 || !j.Accepted {
			t.Fatal("actual API serving publication missing", name, err)
		}
		if f.native.objects["Deployment/"+f.p.Plan.Stack+"-video-cloud/pki-export-"+name+"-"+f.e.Journal.PlanSHA256[:12]] != nil {
			t.Fatal("finished API reader remains")
		}
	}
	f.runtime = runtime
	f.p.Materials = materials
}

func TestHistoricalPostManagedAdmissionCapturesOriginalNativeVolumePins(t *testing.T) {
	device, _, roots, intermediates := newHistoricalDeviceAppExecutionContract(t)
	f := device.acceptedCoreExecutionContract
	identities := historicalTransportIdentities(t, f, roots, intermediates, device.authorityKeys)
	clientsOnly := identities
	clientsOnly.Servers = nil
	installHistoricalTransportProtocol(t, f, clientsOnly)
	ledger := historicalTransportCaptureVolumes(t, f)
	if len(ledger.Volumes) != 16 {
		t.Fatal("original pre-application ledger must retain exactly sixteen native owners", len(ledger.Volumes))
	}
	before := createBootstrapStateSnapshot(t, f.store.Root)
	writes := deploymentplan.Digest(f.native.writes)
	journal := deploymentplan.Digest(f.e.Journal)
	materials := historicalOriginalVolumeAdmissionMaterials(t, f, ledger)
	if len(materials) != 8 {
		t.Fatal("six owner pins and two ledger pins required", len(materials))
	}
	for key, value := range materials {
		if !environmentPreflightDigest.MatchString(value) {
			t.Fatal("original volume material is not an exact digest", key)
		}
	}
	assertRecoveryContractReadOnly(t, f.store, before)
	if deploymentplan.Digest(f.native.writes) != writes || deploymentplan.Digest(f.e.Journal) != journal {
		t.Fatal("volume admission capture replayed native writes or changed execution history")
	}
}

// Full source readers and eventual cleanup need the genuinely issued first
// session, including its immutable original admission and custody pins.
// Select its lifetime and authority policy before any native publication.
func historicalCompleteOriginalSessionHooks(t *testing.T) coreExecutionContractHooks {
	t.Helper()
	hooks := historicalManagedCoreDatabaseHook(t, historicalOriginalFoundationHooks(t))
	// The first managed-transport transition renews this original publication
	// only inside the maintained 45-minute window. Its actual same-root renewal
	// supplies six days of evidence for the later native completion budget.
	hooks.OriginalServiceCRLLifetime = 30 * time.Minute
	hooks.OriginalServiceCALifetime = 7 * 24 * time.Hour
	hooks.OriginalServiceSessionLifecycle = true
	return hooks
}

func newHistoricalPostManagedExecutionContractWithHooks(t *testing.T, hooks *coreExecutionContractHooks, seedLifetime ...time.Duration) (*deviceTrustExecutionContract, *createServiceSessionRecovery) {
	t.Helper()
	lifetime := 10 * time.Minute
	if len(seedLifetime) > 1 || len(seedLifetime) == 1 && seedLifetime[0] <= 0 {
		t.Fatal("one positive original native managed seed lifetime required")
	}
	if len(seedLifetime) == 1 {
		lifetime = seedLifetime[0]
	}
	var selected []coreExecutionContractHooks
	if hooks != nil {
		selected = append(selected, *hooks)
	}
	device, r, roots, intermediates, identities := newHistoricalManagedTransportSourceExecutionContract(t, selected...)
	t.Log("qualified original expired managed transport source")
	f := device.acceptedCoreExecutionContract
	assertHistoricalAdmittedGeneration(t, f, "managed-transport-source")
	artifact, proof := publishHistoricalServiceRootCRL(t, f, roots["service"])
	t.Log("published and independently verified original Service Root CRL")
	assertHistoricalAdmittedGeneration(t, f, "service-root-crl")
	r.Ceremony.RootCRLFile, r.Ceremony.RootCRLPEM = proof.File, artifact.Plan.Signed.PEM
	r, _ = advanceHistoricalManagedTransportPrefix(t, device, r, roots, intermediates, identities, lifetime)
	t.Log("completed actual managed renewal and authenticated both interrupted host forwards")
	assertHistoricalAdmittedGeneration(t, f, "managed-renewal")
	advanceHistoricalManagedOpenBaoPull(t, f, r)
	t.Log("accepted original host pull repair and original-share unseal")
	assertHistoricalAdmittedGeneration(t, f, "openbao-pull")
	advanceHistoricalManagedOpenBaoAudit(t, f, r)
	t.Log("completed original native audit reload and closed failed host aliases")
	assertHistoricalAdmittedGeneration(t, f, "openbao-audit")
	installHistoricalActiveIssuerRegistry(t, f)
	var sourceChecks []func()
	if lifetime > 2*time.Hour {
		sourceChecks = append(sourceChecks, func() { assertHistoricalManagedPartialSource(t, f, r) })
	}
	advanceHistoricalPostManagedAPI(t, f, r, sourceChecks...)
	t.Log("completed both original API exports and exact-owner retirement")
	assertHistoricalAdmittedGeneration(t, f, "post-managed-api")
	return device, r
}

func assertHistoricalAdmittedGeneration(t *testing.T, f *acceptedCoreExecutionContract, stage string) {
	t.Helper()
	generation, configurationSHA, err := loadEnvironmentCreateAdmittedGeneration(f.store, f.e.Journal.PlanSHA256, f.e.Journal.ArtifactSHA256)
	if err != nil {
		t.Fatal(stage, "cannot resolve immutable admitted create qualification", err)
	}
	if !environmentCreateGenerationMatchesAdmitted(generation, f.p.Plan) {
		t.Fatalf("%s changed immutable admitted generation: admitted=%s current=%s", stage, deploymentplan.Digest(generation), deploymentplan.Digest(f.p.Plan))
	}
	if configurationSHA != deploymentplan.Digest(f.cfg) {
		t.Fatalf("%s changed immutable admitted configuration: admitted=%s current=%s plan=%s", stage, configurationSHA, deploymentplan.Digest(f.cfg), f.p.Plan.ConfigurationSHA256)
	}
}

func TestHistoricalPostManagedCompletesOriginalTransportAndBothAPIExports(t *testing.T) {
	// This source loader requires two hours of remaining native seed life.
	// Select that lifetime before the actual issuer signs it; Core recovery's
	// separate default uses a ten-minute seed and waits for its genuine expiry.
	hooks := historicalCompleteOriginalSessionHooks(t)
	device, r := newHistoricalPostManagedExecutionContractWithHooks(t, &hooks, 3*time.Hour)
	f := device.acceptedCoreExecutionContract
	partial := r.ManagedTransportRecovery.Partial
	if partial == nil || partial.PostManaged == nil || partial.PostManaged.ExportRetire == nil || partial.AcceptedForward == nil {
		t.Fatal("actual post-managed export-retirement handoff missing")
	}
	for _, name := range []string{"video-cloud-api-pki", "video-cloud-api-app-pki"} {
		_, journal, err := acceptedCreateStage(f.store, f.e, "kubernetes/server-secret-"+name)
		if err != nil || !journal.Accepted {
			t.Fatal("actual API producer handoff missing", name, err)
		}
	}
}

// The whole source reader is valid at the actual first-custody checkpoint.
// After both exports close, the next application checkpoint must be selected
// instead; this check never reconstructs an older current journal afterward.
func assertHistoricalManagedPartialSource(t *testing.T, f *acceptedCoreExecutionContract, r *createServiceSessionRecovery) {
	t.Helper()
	before := createBootstrapStateSnapshot(t, f.store.Root)
	writes := deploymentplan.Digest(f.native.writes)
	p := createFoundationRecoveryPlan{Plan: f.p.Plan, Materials: map[string]string{}}
	loaded, err := loadManagedTransportPartialServiceCore(t.Context(), f.cfg, f.store, f.e, f.backend, f.operator, &p)
	if err != nil || loaded == nil || loaded.ManagedTransportRecovery == nil || loaded.ManagedTransportRecovery.Partial == nil || loaded.ManagedTransportRecovery.Partial.PostManaged == nil || loaded.Ceremony.BootstrapSessionID != r.Ceremony.BootstrapSessionID || loaded.OriginalKeySHA != r.OriginalKeySHA || loaded.OriginalCSRSHA != r.OriginalCSRSHA || loaded.Candidate == nil || len(loaded.ManagedTransportRecovery.Partial.PVCs) != 6 {
		t.Fatal("complete maintained partial-source reader lost actual retained native ancestry", err)
	}
	assertRecoveryContractReadOnly(t, f.store, before)
	if deploymentplan.Digest(f.native.writes) != writes {
		t.Fatal("complete source reader replayed native issuance or deployment writes")
	}
}
