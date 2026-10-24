package main

import (
	"context"
	"io"
	"path/filepath"
	"testing"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

// This follows an actual completed MQTT publication on the same original source.
// Its admission is an explicit historical fixture input; native source, original
// ICA3, cache, signature, volume, lease and executor checks remain mandatory.
func advanceHistoricalLateTLSRootCRL(t *testing.T, device *deviceTrustExecutionContract) (*createOpenBaoTLSRootCRLArtifact, *createOpenBaoTLSRootCRLCompletion) {
	t.Helper()
	f := device.acceptedCoreExecutionContract
	waitHistoricalSignedCRLExpiry(t, f, "734487b8-abcf-40be-a813-8b52c64f7954")
	// Keep the complete TLS root publication on the same maintained preparation
	// budget as MQTT.  It includes repeated source and native consumer checks and
	// must not fail only because a healthy run is close to ten minutes.
	ctx, cancel := context.WithTimeout(t.Context(), managedLatePreparationLimit)
	defer cancel()
	p := createFoundationRecoveryPlan{Plan: f.p.Plan, Materials: map[string]string{}}
	r, late, err := loadManagedLateSessionSource(f.cfg, f.store, f.e, &p, managedLateSessionOpenBaoTLSRepair)
	if err != nil {
		t.Fatal("actual completed MQTT source for TLS repair", err)
	}
	if err := verifyManagedLateSessionObservationWithMode(ctx, f.cfg, f.store, f.e, f.backend, r, &p, late, managedLateSessionOpenBaoTLSRepair); err != nil {
		t.Fatal("actual TLS native source", err)
	}
	native := map[string]string{}
	for key, value := range p.Materials {
		native[key] = value
	}
	root := *r.OpenBaoRoot
	snapshot, _, err := readCreateRootCRLNativeSnapshot(ctx, f.cfg, f.e, f.backend, root.ID)
	if err != nil || snapshot.Prior.Number != "1" {
		t.Fatal("actual original TLS revocation inventory", err)
	}
	material, err := createOpenBaoTLSRootCRLMaterial(f.store, f.e.Journal.PlanSHA256, root)
	if err != nil {
		t.Fatal(err)
	}
	installHistoricalOfflineCRLSigner(t, f.store)
	candidate, request, signed, err := prepareOpenBaoTLSRootCRLCandidate(ctx, f.store, f.e, p.Plan.Bootstrap.Image, root, snapshot, material)
	if err != nil {
		t.Fatal("actual original offline TLS signature", err)
	}
	controller, cm, targets, podName, podUID, err := loadOpenBaoTLSRootCRLTargets(ctx, f.cfg, f.e, f.backend, r, late, root, snapshot.Prior)
	if err != nil {
		t.Fatal("actual retained TLS consumers", err)
	}
	checker, err := environmentPreflightCheckerSHA256()
	if err != nil {
		t.Fatal(err)
	}
	source := f.e.Journal
	plan := createOpenBaoTLSRootCRLPlan{Schema: createOpenBaoTLSRootCRLSchema, Parent: f.e.Journal.PlanSHA256, SourceOwnerSHA: deploymentplan.Digest(f.e.owner), SourceJournalSHA: deploymentplan.Digest(source), CheckerSHA: checker, ConfigurationSHA: deploymentplan.Digest(f.cfg), Configuration: f.cfg, Generation: p.Plan, KubeconfigSHA: environmentPreflightBytesSHA256(f.backend.KubeconfigContent), Image: p.Plan.Bootstrap.Image, SourceArtifactSHA: late.SourceArtifactSHA, SourceCoreSHA: late.AcceptedCore.SHA256(), SourceAppsSHA: r.ManagedCorePartial.Apps.SHA256(), Root: root, Intermediate: *r.OpenBaoIntermediate, Snapshot: snapshot, SnapshotSHA: deploymentplan.Digest(snapshot), Request: request, Signed: signed, Candidate: candidate, Material: material, ConfigMap: cm, Controller: controller, ControllerPodName: podName, ControllerPodUID: podUID, Consumers: targets, NativeSourceEvidence: native}
	plan.IntermediateCRL, err = readOpenBaoTLSRootCRLIntermediate(ctx, f.cfg, f.e, f.backend, plan.Root, plan.Intermediate)
	if err != nil {
		t.Fatal("actual original ICA3", err)
	}
	plan.ConsumerVolumes, plan.OriginalVolumes, err = readOpenBaoTLSRootCRLVolumes(f.e, targets, native, late.RestoreReview)
	if err != nil || !openBaoTLSRootCRLAcceptedTargetsValid(plan, r, late) {
		t.Fatal("actual original TLS target and volume binding", err)
	}
	finish := installHistoricalRootCRLPublicationProtocol(t, f, root, request, signed, targets)
	defer finish()
	dryRun, err := runNativeCreateRootCRLImport(ctx, f.cfg, f.backend, openBaoTLSRootCRLNativeImportPlan(plan), false)
	if err != nil || !openBaoTLSRootCRLNativeResultValid(dryRun, plan, false) {
		t.Fatal("actual supported TLS import dry-run", err)
	}
	plan.NativeDryRunSHA = environmentPreflightBytesSHA256(dryRun)
	if err := writeOpenBaoTLSRootCRLImmutable(f.store, filepath.Join(candidate, "native-dry-run.json"), map[string]string{"output_sha256": plan.NativeDryRunSHA, "request_sha256": deploymentplan.Digest(request), "root_sha256": root.RootFingerprint, "domain": "openbao_tls"}); err != nil {
		t.Fatal(err)
	}
	fresh, _, err := readCreateRootCRLNativeSnapshot(ctx, f.cfg, f.e, f.backend, root.ID)
	if err != nil || deploymentplan.Digest(fresh) != plan.SnapshotSHA {
		t.Fatal("native TLS dry-run changed original revocation inventory", err)
	}
	if err := verifyOpenBaoTLSRootCRLInputs(ctx, f.cfg, f.store, f.e, f.backend, plan, false); err != nil {
		t.Fatal("native original TLS requalification", err)
	}
	review := deploymentplan.Digest(plan)
	if err := checkCreateRootCRLLeaseAvailable(ctx, f.e, f.backend, rootCRLLeasePlan(f.cfg, review)); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	qualification := deploymentplan.Qualification{PlanSHA256: review, Decision: "GO"}
	for _, id := range createOpenBaoTLSRootCRLGates {
		qualification.Gates = append(qualification.Gates, deploymentplan.GateResult{ID: id, Required: true, Status: "PASS", EvidenceSHA256: review})
	}
	artifact := createOpenBaoTLSRootCRLArtifact{Schema: createOpenBaoTLSRootCRLSchema + "/artifact", Plan: plan, Qualification: qualification, QualifiedAt: now, ValidUntil: now.Add(15 * time.Minute)}
	for name, value := range map[string]any{"source-journal.json": source, "source-owner.json": f.e.owner, "qualification.json": artifact} {
		if err := writeOpenBaoTLSRootCRLImmutable(f.store, filepath.Join(openBaoTLSRootCRLBase(review), name), value); err != nil {
			t.Fatal(err)
		}
	}
	if err := executeCreateOpenBaoTLSRootCRL(ctx, f.cfg, f.store, f.e, f.backend, artifact, source, io.Discard); err != nil {
		t.Fatal("actual native TLS Root#2 publication", err)
	}
	actual, completed, err := readCreateOpenBaoTLSRootCRLRecovery(f.store, f.e.Journal.PlanSHA256)
	if err != nil || actual == nil || completed == nil || !completed.Completed || completed.Receipt.Digest != signed.Digest {
		t.Fatal("actual TLS publication lacks independent complete proof", err)
	}
	return actual, completed
}
