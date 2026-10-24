package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func TestPRHistoricalLateRootCRLsCompleteDurableCheckpoints(t *testing.T) {
	if os.Getenv(historicalWallClockExpirySkipEnv) != "1" {
		t.Skip("nightly runs the complete historical-late contract")
	}
	t.Setenv(historicalWallClockExpirySkipEnv, "")
	device, _ := newHistoricalAcceptedCoreRestoreExecutionContract(t, historicalCompleteOriginalSessionHooks(t))
	mqttArtifact, mqttCompletion := advanceHistoricalLateMQTTRootCRL(t, device)
	if mqttArtifact == nil || mqttCompletion == nil || !mqttCompletion.Completed {
		t.Fatal("bounded MQTT Root CRL checkpoint lacks durable completion proof")
	}
	tlsArtifact, tlsCompletion := advanceHistoricalLateTLSRootCRL(t, device)
	if tlsArtifact == nil || tlsCompletion == nil || !tlsCompletion.Completed {
		t.Fatal("bounded OpenBao TLS Root CRL checkpoint lacks durable completion proof")
	}
}

// All original Core/Apps/MQTT/TLS source and native observers run before this
// explicit isolated historical-admission boundary. It is not a public six-gate
// preflight result. The real qualified reader, fixed budget, native ownership
// leases and complete original-key seed producer run unchanged afterward.
func advanceHistoricalLateSessionSeed(t *testing.T, device *deviceTrustExecutionContract) (context.Context, *createServiceSessionRecovery) {
	t.Helper()
	f := device.acceptedCoreExecutionContract
	overall, cancelOverall := context.WithTimeout(t.Context(), managedLateOverallLimit)
	t.Cleanup(cancelOverall)
	p := createFoundationRecoveryPlan{Plan: f.p.Plan, Materials: map[string]string{}}
	r, late, err := loadManagedLateSessionSource(f.cfg, f.store, f.e, &p, managedLateSessionACKReady)
	if err != nil {
		t.Fatal("actual paired Root CRL source for first late seed", err)
	}
	p.Schema = managedLateSessionSchema
	p.ParentSHA, p.ParentOwnerSHA, p.ParentJournalSHA = f.e.Journal.PlanSHA256, deploymentplan.Digest(f.e.owner), deploymentplan.Digest(f.e.Journal)
	foundation, foundationJournal, err := acceptedCreateStage(f.store, f.e, "kubernetes/foundation")
	if err != nil {
		t.Fatal("actual retained fixture foundation authority", err)
	}
	p.FoundationSHA, p.FoundationJournalSHA = foundation.SHA256(), deploymentplan.Digest(foundationJournal)
	p.KubeconfigSHA = environmentPreflightBytesSHA256(f.backend.KubeconfigContent)
	p.CheckerSHA, err = environmentPreflightCheckerSHA256()
	if err != nil {
		t.Fatal(err)
	}
	if err := prepareManagedLateSessionSeed(overall, f.cfg, f.store, f.e, f.backend, &p, r, late); err != nil {
		t.Fatal("complete maintained first late seed preparation", err)
	}
	if err := bindManagedLateBudgetMaterials(p.Materials); err != nil {
		t.Fatal(err)
	}
	review := deploymentplan.Digest(p)
	late.Review, r.Review, r.Candidate = review, review, late.OwnerCandidate
	now := time.Now().UTC()
	qualification := deploymentplan.Qualification{PlanSHA256: review, Decision: "GO"}
	for _, id := range []string{"deployment.openbao.target", "deployment.openbao.release", "deployment.openbao.storage", "deployment.openbao.bootstrap", "deployment.openbao.network", "deployment.openbao.inputs"} {
		qualification.Gates = append(qualification.Gates, deploymentplan.GateResult{ID: id, Required: true, Status: "PASS", EvidenceSHA256: review})
	}
	artifact := createFoundationRecoveryArtifact{Schema: "rtk-cloud.create-openbao-recovery-artifact/v1", Plan: p, Qualification: qualification, QualifiedAt: now, ValidUntil: now.Add(15 * time.Minute)}
	if err := writeCreateRecoveryAdmissionSource(overall, f.store, f.e, review); err != nil {
		t.Fatal("actual private source capture", err)
	}
	if err := writeEnvironmentCreateDerivedPrivateFile(f.store, filepath.Join("deployment/create/openbao-recovery", review, "qualification.json"), mustProviderRecoveryJSON(artifact)); err != nil {
		t.Fatal(err)
	}
	if err := writeCreateServiceRecoveryCandidates(f.store, review, r, true); err != nil {
		t.Fatal("actual complete late candidate archive", err)
	}
	// Independently consume the immutable archive before any admission or
	// write. A planner's in-memory candidate cannot replace the reader.
	qualified, err := loadManagedLateQualifiedSession(overall, f.cfg, f.store, f.e, &p, review)
	if err != nil || qualified == nil || managedLateSessionSourceDigest(qualified.ManagedLateSession) != managedLateSessionSourceDigest(late) {
		t.Fatal("actual immutable late qualified source", err)
	}
	r = qualified
	late = r.ManagedLateSession
	qualificationBytesSHA, err := readManagedLateQualificationFingerprint(f.store, artifact)
	if err != nil {
		t.Fatal("actual private qualification fingerprint", err)
	}
	ctx, cancelNative, err := beginManagedLateNativeBudget(overall, p.Materials)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(cancelNative)
	_, audit, err := verifyRecoveryInitializedOpenBao(ctx, f.cfg, f.store, f.e, f.backend, r)
	if err != nil {
		t.Fatal("actual original native initialization/audit before late admission", err)
	}
	key := "openbao-recovery-admission-" + review
	intent := map[string]string{"review_sha256": review, "qualification_sha256": deploymentplan.Digest(artifact), "original_parent_journal_sha256": p.ParentJournalSHA}
	if err := beginManagedLateParentAdmission(ctx, f.store, f.e, artifact, qualificationBytesSHA, mustProviderRecoveryJSON(intent), time.Now().UTC()); err != nil {
		t.Fatal("actual source/checker/freshness fence before parent admission", err)
	}
	if err := f.e.RecordReceipt(key, mustProviderRecoveryJSON(map[string]string{"qualified_recovery_sha256": review, "audit_sha256": environmentPreflightBytesSHA256(audit)})); err != nil {
		t.Fatal(err)
	}
	if err := f.e.CompleteEntry(key, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	certificate, nativeSession := historicalTransportSeedLeaf(t, f, late.NewSessionID)
	runtime := f.runtime
	runtime.Backend, runtime.Execution, runtime.ServiceSessionRecovery = f.backend, f.e, r
	ctx = withEnvironmentCreateBootstrapRuntime(ctx, runtime)
	apply := historicalTransportSessionApply(t, f, r, certificate, nativeSession)
	statePath, restoreLease := installHistoricalNativeLeaseProtocol(t, f)
	defer restoreLease()
	before := createBootstrapStateSnapshot(t, f.store.Root)
	subjects, err := environmentCreateBootstrapSubjects(f.cfg)
	if err != nil {
		t.Fatal(err)
	}
	originalClients, err := readManagedSessionClients(ctx, f.cfg, runtime, r.Ceremony, subjects)
	if err != nil {
		t.Fatal(err)
	}
	service, err := executeManagedLateBootstrapSeed(ctx, f.cfg, p.Plan.Bootstrap, f.store, runtime, apply)
	if err != nil {
		t.Fatal("actual original-key first late seed execution", err)
	}
	if service.BootstrapSessionID != late.NewSessionID || service.BootstrapSessionID == late.OriginalSession || len(service.CredentialUIDs) != 3 || !r.Renewal {
		t.Fatal("late seed did not retain its distinct actual session and three owned credentials")
	}
	after := createBootstrapStateSnapshot(t, f.store.Root)
	for path, value := range before {
		if strings.HasPrefix(path, "pki/") && after[path] != value {
			t.Fatal("late seed replaced an original authority, issuer publication or signing custody", path)
		}
	}
	for path := range after {
		if _, existed := before[path]; strings.HasPrefix(path, "pki/") && !existed {
			t.Fatal("late seed created an unqualified authority or signing custody", path)
		}
	}
	currentClients, err := readManagedSessionClients(ctx, f.cfg, runtime, service, subjects)
	if err != nil || currentClients != originalClients || currentClients != late.OriginalClientsSHA {
		t.Fatal("late seed issued or replaced an original workload identity", err)
	}
	var leaseState historicalNativeLeaseState
	if err := readExecutionJSON(statePath, &leaseState); err != nil || leaseState.Lease != nil || leaseState.Creates != 2 || leaseState.Deletes != 2 {
		t.Fatal("native signer/cleanup authorizations lost their actual lease lifecycle", err)
	}
	if _, _, err := managedLateSessionRuntimeProgress(f.store, f.e, late); err != nil {
		t.Fatal("complete late seed cannot be independently re-read as monotonic actual progress", err)
	}
	runtime.ServiceCeremony = &service
	f.runtime, f.p = runtime, p
	return withEnvironmentCreateBootstrapRuntime(ctx, runtime), r
}

// Each prefix is produced once from its original signed source. The real seed
// expiry and two CRL publications remain in this connected runtime test; none
// of their immutable receipts or source validators is replaced after capture.
func TestHistoricalLateBootstrapCompletesFromOriginalSourceAndPairedCRLs(t *testing.T) {
	started := time.Now()
	hooks := historicalCompleteOriginalSessionHooks(t)
	device, _ := newHistoricalAcceptedCoreRestoreExecutionContract(t, hooks)
	t.Logf("accepted original Core restore qualified after %s", time.Since(started).Round(time.Second))
	started = time.Now()
	advanceHistoricalLateMQTTRootCRL(t, device)
	t.Logf("actual MQTT Root CRL publication completed in %s", time.Since(started).Round(time.Second))
	started = time.Now()
	tlsArtifact, _ := advanceHistoricalLateTLSRootCRL(t, device)
	t.Logf("actual OpenBao TLS Root CRL publication completed in %s", time.Since(started).Round(time.Second))
	currentICA := publishHistoricalOpenBaoTLSICASuccessor(t, device, tlsArtifact.Plan.Intermediate)
	if currentICA.Number != "4" {
		t.Fatal("automatic OpenBaoTLS ICA successor did not retain monotonic native generation")
	}
	t.Log("actual same-key OpenBaoTLS ICA successor installed by both native consumers")
	started = time.Now()
	ctx, recovery := advanceHistoricalLateSessionSeed(t, device)
	if ctx.Err() != nil || recovery == nil || recovery.ManagedLateSession == nil || device.runtime.ServiceCeremony == nil {
		t.Fatal("connected original-source continuation did not preserve a live bounded native seed", ctx.Err())
	}
	t.Logf("actual first late seed completed in %s; parent entries=%d native leases=%d", time.Since(started).Round(time.Second), len(device.e.Journal.Entries), len(device.e.Journal.KubernetesLeases))
	started = time.Now()
	finalizer := installHistoricalNativeFinalizer(t, device.acceptedCoreExecutionContract, recovery)
	bindings := completeHistoricalLateBootstrap(t, ctx, device, recovery)
	if finalizer.calls != 1 || finalizer.final.SessionID != recovery.ManagedLateSession.NewSessionID || bindings.BootstrapCleanup == nil {
		t.Fatal("complete bootstrap did not finalize its single actual renewed session")
	}
	t.Logf("actual original-source bootstrap finalization, cleanup and bindings completed in %s", time.Since(started).Round(time.Second))
}

// Continue the actual bounded seed through the maintained bootstrap coordinator.
// The caller installs the external native finalizer endpoint first. All parent
// source/owner checks, scoped children, native Log proof, original-session
// finalizer and final binding readers stay in the production implementation.
func completeHistoricalLateBootstrap(t *testing.T, ctx context.Context, device *deviceTrustExecutionContract, recovery *createServiceSessionRecovery) environmentIdentityBindings {
	t.Helper()
	f := device.acceptedCoreExecutionContract
	runtime := f.runtime
	if recovery == nil || recovery.ManagedLateSession == nil || runtime.ServiceSessionRecovery != recovery || runtime.PKIClient == nil || runtime.ServiceCeremony == nil {
		t.Fatal("actual seed, native finalizer client and original bounded runtime required")
	}
	late, service := recovery.ManagedLateSession, *runtime.ServiceCeremony
	base := historicalAPIApply(t, f)
	logApply := installHistoricalLateLogExecution(t, device, recovery, func(stageCtx context.Context, stage string, definitions []deploymentplan.Definition) error {
		if err := base(stageCtx, stage, definitions); err != nil {
			return err
		}
		if stage == "managed-late-session-owners-"+late.Review {
			if err := restartHistoricalManagedOpenBaoPod(stageCtx, t, f, runtime); err != nil {
				return err
			}
			if err := verifyManagedLateAcceptedOwners(stageCtx, f.cfg, runtime); err != nil {
				return err
			}
			// Only the real accepted nine owner updates can install these eight
			// subjects. The exclusive helper owns Log's eventual ninth receipt.
			publishHistoricalEightInstalledSubjects(t, f, late.NewSessionID)
		}
		return nil
	})
	bundlesObserved := false
	apply := func(stageCtx context.Context, stage string, definitions []deploymentplan.Definition) error {
		if err := logApply(stageCtx, stage, definitions); err != nil {
			return err
		}
		if stage == "managed-late-log-restore-"+late.Review && !bundlesObserved {
			// Capture real public bundle consumption only after all current
			// owners, including the sole Log restorer, have actually rolled out.
			installHistoricalBundleACKProtocol(t, f)
			bundlesObserved = true
		}
		return nil
	}
	before := createBootstrapStateSnapshot(t, f.store.Root)
	originalClients := deploymentplan.Digest(f.protocol["historical_clients"])
	originalClientKeys := deploymentplan.Digest(f.protocol["historical_client_keys"])
	ctx = withEnvironmentCreateBootstrapRuntime(ctx, runtime)
	bindings, err := completeEnvironmentCreateBootstrap(ctx, f.cfg, f.p.Plan.Bootstrap, f.p.Plan.Release, f.store, f.operator, runtime, service, apply)
	if err != nil {
		t.Fatal("actual original-source bootstrap completion and retained binding production", err)
	}
	if !bundlesObserved {
		t.Fatal("completed bootstrap omitted actual current owner bundle consumption")
	}
	if finalized := createOpenBaoACKCompletionEntry(f.e.Journal, "service-session-finalize"); finalized == nil || finalized.State != "completed" || finalized.CompletedAt.IsZero() || len(finalized.Receipt) == 0 {
		t.Fatal("completed bootstrap lacks original native session finalization")
	}
	log, err := readManagedLateLogCompletion(f.store, f.e, late, time.Now().UTC())
	if err != nil || log == nil {
		t.Fatal("completed bootstrap lacks independently authenticated exclusive Log proof", err)
	}
	var installed environmentIdentityBindings
	if err := readExecutionJSON(filepath.Join(f.store.Root, "deployment/create", f.e.Journal.PlanSHA256, "bootstrap/bindings.json"), &installed); err != nil || deploymentplan.Digest(installed) != deploymentplan.Digest(bindings) {
		t.Fatal("original completed bindings cannot be independently re-read", err)
	}
	if bindings.BootstrapCleanup == nil || bindings.BootstrapCleanup.SessionID != late.NewSessionID || bindings.BootstrapCleanup.SecretUID != service.CredentialUIDs[service.BootstrapCredentialNamespace] {
		t.Fatal("final bindings lost the actual new cleanup owner and native Secret UID")
	}
	if deploymentplan.Digest(f.protocol["historical_clients"]) != originalClients || deploymentplan.Digest(f.protocol["historical_client_keys"]) != originalClientKeys {
		t.Fatal("completion replaced a native original client certificate or its private signing key")
	}
	after := createBootstrapStateSnapshot(t, f.store.Root)
	for path, value := range before {
		if strings.HasPrefix(path, "pki/") && after[path] != value {
			t.Fatal("completion replaced original authority, issued identity or signing custody", path)
		}
	}
	for path := range after {
		if _, existed := before[path]; strings.HasPrefix(path, "pki/") && !existed {
			t.Fatal("completion created an unqualified identity or signing authority", path)
		}
	}
	return bindings
}
