package main

import (
	"crypto/rand"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"io"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

// The offline approval/admission is an explicit historical fixture boundary.
// Its request and signature use the actual retained root, then the production
// publisher and independent receipt reader run against the original owners.
func publishHistoricalServiceRootCRL(t *testing.T, f *acceptedCoreExecutionContract, root environmentCreateRegisteredIssuer) (*createRootCRLRecoveryArtifact, *createRootCRLRecoveryProof) {
	t.Helper()
	if !isManagedTransportCheckpoint(f.e.Journal) {
		t.Fatal("Service Root CRL source is not the original managed transport checkpoint")
	}
	source := f.e.Journal
	snapshot, native, err := readCreateRootCRLNativeSnapshot(t.Context(), f.cfg, f.e, f.backend, root.ID)
	if err != nil {
		t.Fatal("original Service revocation inventory", err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	if snapshot.Prior.NextUpdate.After(now.Add(45 * time.Minute)) {
		t.Fatal("original CRL is outside actual renewal window")
	}
	request, err := buildCreateRootCRLRequest(root, snapshot, now)
	if err != nil {
		t.Fatal("original root cannot issue its fixed bounded CRL request", err)
	}
	directory := filepath.Join("deployment/repairs", "root-crl-contract-"+deploymentplan.Digest(request)[:20])
	material, err := createRootCRLMaterial(f.store, f.e.Journal.PlanSHA256, root)
	if err != nil {
		t.Fatal(err)
	}
	encrypted, err := readBoundedEnvironmentFile(filepath.Join(f.store.Root, "pki/custody/roots", root.ID, "ca-key.encrypted.pem"), true)
	if err != nil {
		t.Fatal(err)
	}
	passphrase, err := readBoundedEnvironmentFile(filepath.Join(f.store.Root, "pki/custody/service-root-v1-key-passphrase"), true)
	block, _ := pem.Decode(encrypted)
	if err != nil || block == nil {
		t.Fatal("original encrypted offline root unavailable", err)
	}
	plain, err := x509.DecryptPEMBlock(block, passphrase)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := x509.ParseECPrivateKey(plain)
	cert, certErr := parseFirstCertificatePEM([]byte(root.Certificate))
	if err != nil || certErr != nil {
		t.Fatal("original root parsing", err, certErr)
	}
	public, err := x509.MarshalPKIXPublicKey(&signer.PublicKey)
	if err != nil || string(public) != string(cert.RawSubjectPublicKeyInfo) {
		t.Fatal("offline signing key changed", err)
	}
	number, ok := new(big.Int).SetString(request.Number, 10)
	if !ok {
		t.Fatal("CRL sequence malformed")
	}
	var revoked []x509.RevocationListEntry
	for _, entry := range request.Entries {
		serial, ok := new(big.Int).SetString(entry.Serial, 16)
		if !ok {
			t.Fatal("native revoked serial malformed")
		}
		revoked = append(revoked, x509.RevocationListEntry{SerialNumber: serial, RevocationTime: entry.RevokedAt, ReasonCode: entry.Reason})
	}
	der, err := x509.CreateRevocationList(rand.Reader, &x509.RevocationList{Number: number, ThisUpdate: request.ThisUpdate, NextUpdate: request.NextUpdate, RevokedCertificateEntries: revoked}, cert, signer)
	if err != nil {
		t.Fatal("offline same-root CRL signing", err)
	}
	signed := pem.EncodeToMemory(&pem.Block{Type: "X509 CRL", Bytes: der})
	record, err := validateCreateRootCRLSigned(root, snapshot, request, signed, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	for name, value := range map[string]any{"request.json": request, "issuer.json": root.Document} {
		if _, err := writeEnvironmentCreateBootstrapJSON(f.store, filepath.Join(directory, name), value); err != nil {
			t.Fatal(err)
		}
	}
	if err := f.store.write(filepath.Join(directory, "signed/revocations.pem"), signed, false); err != nil {
		t.Fatal(err)
	}
	nativeObjects := historicalNativeInventory(f.native.objects)
	state := crlExecutorBoundary{Objects: nativeObjects, Snapshot: snapshot, Signed: record, Request: request, Fingerprint: root.RootFingerprint, Caches: map[string]createRootCRLRecord{}}
	paths := []string{environmentCreateServiceState + "/private/crl/listener-root.json", environmentCreateServiceState + "/private/crl/outbound-root.json"}
	ns := f.p.Plan.Stack + "-video-cloud"
	for _, object := range nativeObjects {
		if object["kind"] != "Pod" {
			continue
		}
		controller, err := environmentPodController(object, nativeObjects)
		if err != nil {
			continue
		}
		if controller != "Deployment/"+ns+"/pki-controller" && controller != "Deployment/"+ns+"/certissuer" {
			continue
		}
		name := deploymentplan.String(deploymentplan.Map(object["metadata"])["name"])
		for _, path := range paths {
			state.Caches[name+":"+path] = snapshot.Prior
		}
	}
	if len(state.Caches) != 4 {
		t.Fatal("original two native Service consumers and four owned cache paths missing", len(state.Caches))
	}
	protocol := &crlExecutorFixture{cfg: f.cfg, store: f.store, execution: f.e, backend: f.backend}
	previousPath, previousKubectl, previousState := os.Getenv("PATH"), os.Getenv("RTK_CLOUD_KUBECTL"), os.Getenv("RTK_CRL_EXECUTOR_STATE")
	installCRLExecutorProtocol(t, protocol, state)
	defer func() {
		t.Setenv("PATH", previousPath)
		t.Setenv("RTK_CLOUD_KUBECTL", previousKubectl)
		t.Setenv("RTK_CRL_EXECUTOR_STATE", previousState)
	}()
	targets, err := loadCreateRootCRLStateTargets(t.Context(), f.cfg, f.store, f.e, f.backend, root, snapshot.Prior, f.p.Plan.Bootstrap.Image)
	if err != nil || len(targets) != 2 {
		t.Fatal("actual current Service CRL owner qualification", err)
	}
	stage, journal, err := acceptedCreateStage(f.store, f.e, "kubernetes/service-active-trust")
	if err != nil || len(stage.Resources) != 1 {
		t.Fatal("original CRL Secret allocation", err)
	}
	original := stage.Resources[0]
	actual, err := f.backend.Read(t.Context(), original.ID)
	if err != nil || actual == nil || !deploymentplan.Matches(original.Desired, actual) || deploymentplan.Map(actual["metadata"])["uid"] != journal.Owners[original.ID] {
		t.Fatal("original CRL Secret owner/body changed", err)
	}
	desired := deploymentplan.Clone(original.Desired)
	deploymentplan.Map(desired["data"])["revocations.pem"] = base64.StdEncoding.EncodeToString(signed)
	checker, err := environmentPreflightCheckerSHA256()
	if err != nil {
		t.Fatal(err)
	}
	plan := createRootCRLRecoveryPlan{Schema: createRootCRLRecoverySchema, Parent: f.e.Journal.PlanSHA256, OwnerSHA: deploymentplan.Digest(f.e.owner), JournalSHA: deploymentplan.Digest(source), CheckerSHA: checker, ConfigurationSHA: deploymentplan.Digest(f.cfg), KubeconfigSHA: environmentPreflightBytesSHA256(f.backend.KubeconfigContent), Image: f.p.Plan.Bootstrap.Image, Candidate: directory, Root: root, Snapshot: snapshot, SnapshotSHA: environmentPreflightBytesSHA256(native), Request: request, Signed: record, Material: material, Secret: activeTransportRenewalResource{ID: original.ID, UID: journal.Owners[original.ID], Before: original.Desired, Desired: desired}, Targets: targets}
	if err := checkCreateRootCRLLeaseAvailable(t.Context(), f.e, f.backend, rootCRLLeasePlan(f.cfg, deploymentplan.Digest(plan))); err != nil {
		t.Fatal(err)
	}
	body, err := activeTransportResourceBody(plan.Secret, actual)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.backend.Write(t.Context(), "update", body, true); err != nil {
		t.Fatal(err)
	}
	if _, err := runNativeCreateRootCRLImport(t.Context(), f.cfg, f.backend, plan, false); err != nil {
		t.Fatal("native import dry-run", err)
	}
	review := deploymentplan.Digest(plan)
	qualifiedAt := time.Now().UTC()
	qualification := deploymentplan.Qualification{Decision: "GO", PlanSHA256: review}
	for _, id := range createRootCRLGates {
		qualification.Gates = append(qualification.Gates, deploymentplan.GateResult{ID: id, Required: true, Status: "PASS", EvidenceSHA256: review})
	}
	artifact := createRootCRLRecoveryArtifact{Schema: createRootCRLRecoverySchema + "/artifact", Plan: plan, Qualification: qualification, QualifiedAt: qualifiedAt, ValidUntil: qualifiedAt.Add(15 * time.Minute)}
	base := filepath.Join("deployment/create/root-crl-recovery", review)
	for name, value := range map[string]any{"qualification.json": artifact, "source-journal.json": source} {
		if err := f.store.write(filepath.Join(base, name), mustProviderRecoveryJSON(value), false); err != nil {
			t.Fatal(err)
		}
	}
	before := createBootstrapStateSnapshot(t, f.store.Root)
	if err := executeCreateRootCRLRecovery(t.Context(), f.cfg, f.store, f.e, f.backend, artifact, io.Discard); err != nil {
		t.Fatal("actual original Service Root CRL publication", err)
	}
	completed, proof, err := readCreateRootCRLRecovery(f.store, f.e.Journal.PlanSHA256)
	if err != nil || completed == nil || proof == nil || proof.Digest != record.Digest || !proof.Completed {
		t.Fatal("executed publication lacks independent original proof", err)
	}
	after := createBootstrapStateSnapshot(t, f.store.Root)
	for path, value := range before {
		if strings.HasPrefix(path, "pki/") && after[path] != value {
			t.Fatal("CRL renewal replaced original signing custody", path)
		}
	}
	result := protocol.state(t)
	if result.Imports != 1 || result.SecretWrites != 1 || result.Installs != 2 || result.LeaseCreates != 1 || result.LeaseDeletes != 1 {
		t.Fatal("actual publication effects changed", result.Imports, result.SecretWrites, result.Installs, result.LeaseCreates, result.LeaseDeletes)
	}
	for id, object := range result.Objects {
		if object["kind"] != "Pod" && object["kind"] != "ReplicaSet" {
			f.native.objects[id] = deploymentplan.Clone(object)
		}
	}
	publishHistoricalNativeCRL(t, f, record)
	return completed, proof
}

// Stop at the actual original signing/publication boundary. The later session
// ancestry is deliberately absent; this verifies the selected connected-fixture
// lifetime against the production renewal window without replaying that ancestry.
func TestHistoricalOriginalServiceCRLLifetimeAllowsRequiredRenewal(t *testing.T) {
	hooks := historicalCompleteOriginalSessionHooks(t)
	var service environmentCreateServiceCeremony
	hooks.BeforeServiceSession = func(_ *acceptedCoreExecutionContract, _ environmentCreateBootstrapRuntime, original environmentCreateServiceCeremony, _ []byte) {
		service = original
	}
	f := newCoreExecutionContractAtBoundaryWithHooks(t, false, true, false, hooks, historicalCompleteDomainPolicy(t))
	if service.RootCRLFile == "" || service.RootCRLPEM == "" {
		t.Fatal("actual original signing boundary did not publish the Service CRL")
	}
	raw, err := readBoundedEnvironmentFile(filepath.Join(f.store.Root, service.RootCRLFile), true)
	if err != nil || string(raw) != service.RootCRLPEM {
		t.Fatal("actual original signed publication differs", err)
	}
	original, err := managedLateOriginalRecord(service.Root.ID, raw)
	if err != nil {
		t.Fatal(err)
	}
	prior := createRootCRLRecord{original.IssuerID, original.Digest, original.Number, original.PEM, original.ThisUpdate, original.NextUpdate}
	now := time.Now().UTC().Truncate(time.Second)
	if _, err := parseCreateRootCRL(service.Root, prior, false, now); err != nil || !prior.NextUpdate.After(now) {
		t.Fatal("original Service CRL is not genuine live evidence", err)
	}
	// This is the same window selected by the public root-CRL coordinator and
	// publishHistoricalServiceRootCRL, not a replacement admission decision.
	if prior.NextUpdate.After(now.Add(45 * time.Minute)) {
		t.Fatalf("selected original Service CRL has %s remaining; required renewal accepts at most 45m", prior.NextUpdate.Sub(now))
	}
	if _, err := parseCreateRootCRL(service.Root, prior, true, now); err == nil {
		t.Fatal("short original publication incorrectly satisfies the maintained fresh-CRL reserve")
	}
	before := createBootstrapStateSnapshot(t, f.store.Root)
	request, err := buildCreateRootCRLRequest(service.Root, createRootCRLNativeSnapshot{Prior: prior}, now)
	root, rootErr := parseFirstCertificatePEM([]byte(service.Root.Certificate))
	if err != nil || rootErr != nil || request.Number != "2" || !request.NextUpdate.Equal(now.Add(6*24*time.Hour)) || request.NextUpdate.After(root.NotAfter) {
		t.Fatal("same-root renewal cannot cover the subsequent complete native budget", err, rootErr)
	}
	assertRecoveryContractReadOnly(t, f.store, before)
}

func TestHistoricalServiceRootCRLExecutionContractAuthenticatesCompleteOriginalHistory(t *testing.T) {
	hooks := historicalManagedCoreDatabaseHook(t, historicalOriginalFoundationHooks(t))
	hooks.OriginalServiceCALifetime = 7 * 24 * time.Hour
	hooks.OriginalServiceCRLLifetime = 30 * time.Minute
	device, _, roots, _ := newHistoricalDeviceAppExecutionContract(t, hooks)
	artifact, proof := publishHistoricalServiceRootCRL(t, device.acceptedCoreExecutionContract, roots["service"])
	if artifact.Plan.Request.Number != "2" || !proof.Completed || !artifact.Plan.Request.NextUpdate.Equal(artifact.Plan.Request.ThisUpdate.Add(6*24*time.Hour)) {
		t.Fatal("Service CRL repair changed original sequence or bounded validity")
	}
	if _, _, err := readCreateRootCRLRecovery(device.store, device.e.Journal.PlanSHA256); err != nil {
		t.Fatal("completed source cannot be read independently", err)
	}
}
